package main

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Candidate channels, in MHz. Lobby frequencies (2412/5180) are deliberately
// excluded: if an election ever landed on the lobby pair, every node would
// flip into lobby state and scanning/elections would silently stop.
var (
	band24Channels = []int{2437, 2462}
	band5Channels  = []int{5200, 5220, 5240, 5745, 5765, 5785, 5805, 5825}
)

type ChannelScanResult struct {
	Channel    int `json:"channel"`
	NoiseFloor int `json:"noise_floor"`
	BSSCount   int `json:"bss_count"`
}

type ChannelReport struct {
	Results []ChannelScanResult `json:"results"`
}

// performScan surveys both mesh radios' candidate channels and returns a
// combined report. Each radio only scans its own band's candidates.
func performScan(iface24, iface5 string) ChannelReport {
	var results []ChannelScanResult
	if iface24 != "" {
		results = append(results, scanIface(iface24, band24Channels)...)
	}
	if iface5 != "" {
		results = append(results, scanIface(iface5, band5Channels)...)
	}
	return ChannelReport{Results: results}
}

func scanIface(iface string, freqs []int) []ChannelScanResult {
	args := []string{"dev", iface, "scan", "freq"}
	for _, f := range freqs {
		args = append(args, strconv.Itoa(f))
	}

	// iw dev scan can hang on a busy/hostile RF environment — cap it rather
	// than block the whole node-manager loop; a timed-out or partial scan
	// still leaves useful data in the survey/scan dump caches read below.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "iw", args...).Run()

	surveyOut, _ := exec.Command("iw", "dev", iface, "survey", "dump").Output()
	scanOut, _ := exec.Command("iw", "dev", iface, "scan", "dump").Output()

	noiseByFreq := parseSurveyNoise(string(surveyOut))
	scanText := string(scanOut)

	out := make([]ChannelScanResult, 0, len(freqs))
	for _, f := range freqs {
		noise, ok := noiseByFreq[f]
		if !ok {
			noise = -100
		}
		out = append(out, ChannelScanResult{
			Channel:    f,
			NoiseFloor: noise,
			BSSCount:   countBSSOnFreq(scanText, f),
		})
	}
	return out
}

// parseSurveyNoise reads `iw dev <iface> survey dump` output. Each entry is
// a "frequency: <MHz> ..." line followed shortly by a "noise: <dBm> dBm"
// line. `iw` retains one historical survey entry per frequency ever seen,
// so a frequency can appear more than once — keep only the first (matches
// upstream's `head -1` on the equivalent awk/grep pipeline).
func parseSurveyNoise(out string) map[int]int {
	noise := make(map[int]int)
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "frequency:" {
			continue
		}
		freq, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if _, seen := noise[freq]; seen {
			continue
		}
		if i+1 >= len(lines) {
			continue
		}
		nfields := strings.Fields(lines[i+1])
		if len(nfields) < 2 || nfields[0] != "noise:" {
			continue
		}
		if n, err := strconv.Atoi(nfields[1]); err == nil {
			noise[freq] = n
		}
	}
	return noise
}

// countBSSOnFreq counts scan-dump lines like "freq: 2437.0" for the given
// frequency — one such line per visible BSS on that channel.
func countBSSOnFreq(scanOut string, freq int) int {
	needle := "freq: " + strconv.Itoa(freq) + "."
	count := 0
	for _, line := range strings.Split(scanOut, "\n") {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

func writeChannelReport(report ChannelReport) {
	data, err := json.Marshal(report)
	if err != nil {
		log.Printf("[acs] marshal channel report: %v", err)
		return
	}
	if err := writeStateFile(channelReportFile, string(data)); err != nil {
		log.Printf("[acs] write channel report: %v", err)
	}
}
