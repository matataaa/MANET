package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Candidate channels, in MHz. Lobby frequencies (2412/5180) are deliberately
// excluded: if an election ever landed on the lobby pair, every node would
// flip into lobby state and scanning/elections would silently stop.
//
// band5Channels is the full KNOWN candidate superset, not what's actually
// scanned/elected — it includes 5745-5825 (UNII-3), which is illegal under
// ETSI (EU). It's kept as-is for tourguide.go's wifiChannelFreq, which
// translates a peer's gossiped channel *number* back to MHz and needs the
// full known space regardless of this node's own regulatory domain. Actual
// scanning/election uses activeBand5Channels below, which filters this list
// against what the local phy currently permits — see that function's
// comment for why this is derived live instead of hardcoding a second
// EU-only list next to this one.
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
// candidates5 is the caller's already-filtered (via activeBand5Channels)
// 5GHz candidate list, threaded in rather than recomputed here so the same
// list is used for both scanning and the election call in the same tick —
// see runACSTick.
func performScan(iface24, iface5 string, candidates5 []int) ChannelReport {
	var results []ChannelScanResult
	if iface24 != "" {
		results = append(results, scanIface(iface24, band24Channels)...)
	}
	if iface5 != "" && len(candidates5) > 0 {
		// len(candidates5) == 0 means activeBand5Channels found nothing
		// currently usable on this phy (e.g. every 5GHz candidate is
		// (disabled)/(no IR) under the local regulatory domain, or iw
		// itself errored) — skip the scan rather than call `iw ... scan
		// freq` with no frequency arguments, which is a malformed
		// invocation, not a "scan everything" request.
		results = append(results, scanIface(iface5, candidates5)...)
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
			// No real survey entry for this candidate — e.g. an unscannable
			// or regulatory-domain-illegal channel. Previously this
			// synthesized NoiseFloor = -100, which looks like an extremely
			// quiet (great) channel and made electBand's scoring
			// (channel_election.go) crown it winner outright. Drop the
			// candidate from this report entirely instead: with no entry at
			// all, aggregateChannelReports (channel_election.go) correctly
			// sees zero readings for this channel and returns ok=false, so
			// electBand skips it as a candidate rather than electing it.
			continue
		}
		out = append(out, ChannelScanResult{
			Channel:    f,
			NoiseFloor: noise,
			BSSCount:   countBSSOnFreq(scanText, f),
		})
	}
	return out
}

// phyForIface returns the phy identifier (e.g. "phy0") backing iface, by
// parsing `iw dev <iface> info`'s "wiphy N" line — mirrors radio-setup.sh's
// iface_phy() shell helper so both sides derive the identifier the same way.
func phyForIface(ctx context.Context, iface string) (string, error) {
	out, err := exec.CommandContext(ctx, "iw", "dev", iface, "info").Output()
	if err != nil {
		return "", fmt.Errorf("iw dev %s info: %w", iface, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "wiphy" {
			return "phy" + fields[1], nil
		}
	}
	return "", fmt.Errorf("iw dev %s info: no wiphy line found", iface)
}

// phyUsableFreqs returns the set of frequencies (MHz) phy currently reports
// as usable, by parsing the "Frequencies:" block(s) of `iw phy <phy> info`.
// A frequency line is excluded if it's flagged "(disabled)", "(no IR)", or
// "(radar detection)" — the first two mean cfg80211 won't let a radio
// transmit there right now at all (regulatory-domain-forbidden, or
// passive-scan/no-initiate-radiation only); the third is a DFS channel
// awaiting/undergoing Channel Availability Check and is equally unusable
// *right now* even though it isn't flagged disabled — none of today's
// band5Channels candidates are DFS, but this function is reused as-is by
// the planned ACS self-heal (see below) as a general "is this frequency
// legal right now" guard, so it must not silently pass a CAC-pending
// channel as available. This is the exact check radio-setup.sh's
// iface_supports_freq is missing (it greps for "<freq>.0 MHz" without
// excluding any of these flags) — don't port that bug into Go.
//
// iw prints frequencies with a fractional part (e.g. "* 5180.0 MHz ..."),
// confirmed against this project's own shell greps for the dotted form
// (radio-setup.sh, manet-wlan-reconcile.sh) — parse as a float and round,
// not strconv.Atoi, or every line fails to parse and this returns an
// empty map on every real node, silently. A phy that genuinely has zero
// frequency lines is a parse failure, not a real state, so treat an empty
// result as an error rather than a quietly-empty success — this is what
// makes a future parsing regression loud instead of silently degrading
// every node into permanent lobby+limp mode (see activeBand5Channels).
func phyUsableFreqs(ctx context.Context, phy string) (map[int]bool, error) {
	out, err := exec.CommandContext(ctx, "iw", "phy", phy, "info").Output()
	if err != nil {
		return nil, fmt.Errorf("iw phy %s info: %w", phy, err)
	}
	freqs := make(map[int]bool)
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "* ") {
			continue
		}
		fields := strings.Fields(trimmed)
		// "* <freq(.0)> MHz [<chan>] (<tx power>) [(disabled)|(no IR)|(radar detection)|...]"
		if len(fields) < 3 || fields[2] != "MHz" {
			continue
		}
		freqF, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		// Rounds to the nearest whole MHz — fine for every 2.4/5GHz
		// candidate this project uses today, but HaLow/S1G phys report
		// half-MHz-spaced frequencies (e.g. 903.5) that would round-collide
		// with an adjacent whole-MHz channel (904). Don't point this
		// function at a HaLow interface without switching to integer-kHz
		// keys first — confirmed via review, not yet needed since only the
		// 5GHz mesh interface calls this today.
		freq := int(math.Round(freqF))
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "(disabled)") || strings.Contains(lower, "no ir") || strings.Contains(lower, "radar detection") {
			continue
		}
		freqs[freq] = true
	}
	if len(freqs) == 0 {
		return nil, fmt.Errorf("iw phy %s info: no frequency lines parsed", phy)
	}
	return freqs, nil
}

// phyUsableFreqsForIface resolves iface's phy and returns its usable
// frequency set in one call — the shared primitive both freqAvailableOnPhy
// and activeBand5Channels use, so the phy is resolved once per caller
// rather than once per candidate frequency checked.
func phyUsableFreqsForIface(ctx context.Context, iface string) (map[int]bool, error) {
	phy, err := phyForIface(ctx, iface)
	if err != nil {
		return nil, fmt.Errorf("resolve phy for %s: %w", iface, err)
	}
	usable, err := phyUsableFreqs(ctx, phy)
	if err != nil {
		return nil, fmt.Errorf("query usable frequencies for %s: %w", phy, err)
	}
	return usable, nil
}

// freqAvailableOnPhy reports whether freqMHz is currently usable on the phy
// backing iface (see phyUsableFreqs for exactly what "usable" excludes).
// Deliberately standalone and iface-scoped — signature and semantics kept
// stable regardless of activeBand5Channels' own internal call pattern —
// because the planned ACS self-heal (see docs/ACS.md, "verify-after-apply")
// needs this exact same single-frequency check as an independent guard
// before ever firing a corrective wpa_supplicant restart on an elected
// channel. Costs one phy resolution + one iw phy info call per invocation;
// a caller checking multiple frequencies for the same iface (like
// activeBand5Channels) should call phyUsableFreqsForIface directly instead
// and do its own map lookups, rather than calling this in a loop.
func freqAvailableOnPhy(iface string, freqMHz int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	usable, err := phyUsableFreqsForIface(ctx, iface)
	if err != nil {
		return false, err
	}
	return usable[freqMHz], nil
}

// activeBand5Channels filters band5Channels (the full known 5GHz candidate
// superset, including US-only UNII-3) down to whichever are currently
// usable on iface5's phy, via a single phyUsableFreqsForIface call (not one
// freqAvailableOnPhy call per candidate — resolving the phy and reading its
// usable-frequency set doesn't depend on which candidate is being checked,
// and this function's caller runs once per 15s ACS tick against an already
// tight subprocess/timeout budget). Derived live every call instead of
// hardcoding a second EU-specific list: node-manager previously read no
// regulatory-domain information at all, so an EU node's phy (which reports
// 5745-5825 as "(disabled)" under ETSI) would still offer those as election
// candidates — scanIface's now-removed fake -100dBm synthesis for exactly
// these unscannable channels meant electBand would crown one of them winner
// outright. Filtering here means an EU node's own live phy capability
// governs its candidate set, without maintaining a parallel EU-only
// frequency list next to the US one.
//
// Returns nil for a node with no 5GHz mesh interface (iface5 == "") — a
// clean no-op; callers already guard on iface5 != "" before scanning or
// electing on this band, so an empty candidate list here is never reached
// on such a node anyway. On any error resolving the phy or its usable
// frequencies (iw missing, interface mid-teardown, a phy-info parse
// failure, etc.), this fails closed — returns no candidates for this
// cycle — rather than falling back to the unfiltered superset, since a
// transient iw failure should never risk offering a possibly-illegal
// channel as an election candidate. A caller seeing an empty result must
// not treat that the same as "every candidate is too noisy" — electBand
// (channel_election.go) holds the current channel rather than escalating
// to lobby+limp mode specifically to keep a transient failure here from
// becoming a mesh-wide disruption.
//
// Note: every node in the mesh now derives its own candidate set from its
// own live phy/regulatory domain. That's correct for a mesh where every
// node genuinely shares the same real-world regulatory domain (the normal
// case), but a misconfigured or mixed-regdomain mesh could now have peers
// with different candidate sets for electBand's peer-voting — this wasn't
// handled cleanly before either (the illegal channels just silently failed
// to score), so this isn't a regression, but it's not a full fix for that
// case either.
func activeBand5Channels(iface5 string) []int {
	if iface5 == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	usable, err := phyUsableFreqsForIface(ctx, iface5)
	if err != nil {
		log.Printf("[acs] 5GHz candidates: %v — excluding all candidates this cycle", err)
		return nil
	}
	out := make([]int, 0, len(band5Channels))
	for _, f := range band5Channels {
		if usable[f] {
			out = append(out, f)
		}
	}
	log.Printf("[acs] 5GHz candidates this cycle: %v", out)
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

// countBSSOnFreq counts scan-dump lines reporting the given frequency —
// one such line per visible BSS on that channel. `iw`'s dotted-decimal
// suffix (e.g. "freq: 2437.0") only appears for a nonzero frequency
// offset (6GHz sub-channels / S1G); standard 2.4/5GHz output can be a
// plain integer ("freq: 2437") depending on iw version. Field-matching
// (mirroring parseSurveyNoise above) handles both instead of assuming one.
func countBSSOnFreq(scanOut string, freq int) int {
	target := strconv.Itoa(freq)
	count := 0
	for _, line := range strings.Split(scanOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "freq:" {
			continue
		}
		val := strings.TrimSuffix(fields[1], ".0")
		if val == target {
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
