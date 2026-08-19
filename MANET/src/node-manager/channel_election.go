package main

import (
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"time"
)

const (
	// Ignore scan reports older than this (scans run every ACS tick, but a
	// peer's registry entry can lag behind its actual publish cadence).
	reportStaleAfter = 240 * time.Second
	// Disqualify a channel if ANY reporting node saw noise worse than this.
	noiseDisqualifyDBM = -70
	// A channel must be at least this much quieter than the incumbent to
	// win — without this, the election would flap between two channels
	// whose scores differ only by scan-to-scan noise.
	channelBiasDB = 10.0
	// If even the best surviving channel scores worse than this, the RF
	// environment itself is the problem, not the choice of channel —
	// fall back to the lobby frequency and raise limp mode.
	limpModeScoreThreshold = -60.0

	lobbyFreq24 = "2412"
	lobbyFreq5  = "5180"
)

type channelStats struct {
	maxNoise int
	avgNoise float64
	totalBSS int
}

// aggregateChannelReports merges every fresh report (self + peers, keyed
// arbitrarily) into a per-channel view: worst noise anyone saw, mean noise
// across vantage points, and summed BSS count. Aggregating across nodes
// rather than trusting only the local scan accounts for hidden-node
// effects — a channel can look clean locally but be busy from a
// neighbor's vantage point.
func aggregateChannelReports(reports map[string]ChannelReport, channel int) (channelStats, bool) {
	var noises []int
	var bssSum int
	for _, r := range reports {
		for _, res := range r.Results {
			if res.Channel != channel {
				continue
			}
			noises = append(noises, res.NoiseFloor)
			bssSum += res.BSSCount
		}
	}
	if len(noises) == 0 {
		return channelStats{}, false
	}
	max, sum := noises[0], 0
	for _, n := range noises {
		if n > max {
			max = n
		}
		sum += n
	}
	return channelStats{
		maxNoise: max,
		avgNoise: float64(sum) / float64(len(noises)),
		totalBSS: bssSum,
	}, true
}

// collectFreshReports gathers every node's channel report that's still
// within reportStaleAfter of its last registry timestamp, plus the local
// scan just taken (which hasn't round-tripped through alfred/mesh-registry
// yet, so it wouldn't otherwise be included this tick).
func collectFreshReports(registry map[string]map[string]string, selfReport ChannelReport) map[string]ChannelReport {
	reports := map[string]ChannelReport{"self": selfReport}
	selfMAC := myRegistryMAC()
	now := time.Now().Unix()
	for mac, fields := range registry {
		if selfMAC != "" && mac == selfMAC {
			// Already included above as the freshly-scanned "self" entry —
			// our own (staler) published copy in the registry would
			// otherwise double-count self's readings in the aggregate.
			continue
		}
		raw := fields["CHANNEL_REPORT_JSON"]
		if raw == "" {
			continue
		}
		ts, err := strconv.ParseInt(fields["LAST_SEEN_TIMESTAMP"], 10, 64)
		if err != nil || now-ts > int64(reportStaleAfter.Seconds()) {
			continue
		}
		var r ChannelReport
		if json.Unmarshal([]byte(raw), &r) != nil {
			continue
		}
		reports[mac] = r
	}
	return reports
}

type electionResult struct {
	freq     string
	limp     bool
	winnerCh int
	score    float64
}

// electBand runs the deterministic, decentralized election for one band:
// every node computes this from the same aggregated (self+peer) report
// data and a fixed scoring rule, so they converge on the same winner
// without a coordinator. Lower score wins; the incumbent gets a bias so a
// channel only slightly quieter doesn't win and cause flapping.
func electBand(reports map[string]ChannelReport, candidates []int, currentFreq, lobbyFreq, band string) electionResult {
	currentCh, _ := strconv.Atoi(currentFreq)

	type candidate struct {
		score float64
		ch    int
	}
	var scored []candidate

	for _, ch := range candidates {
		stats, ok := aggregateChannelReports(reports, ch)
		if !ok {
			continue
		}
		if stats.maxNoise > noiseDisqualifyDBM {
			log.Printf("[acs] %s: channel %d disqualified (max_noise %ddBm)", band, ch, stats.maxNoise)
			continue
		}

		score := stats.avgNoise + float64(stats.totalBSS)*0.1
		if ch == currentCh {
			score -= channelBiasDB
		}
		scored = append(scored, candidate{score: score, ch: ch})
	}

	if len(scored) == 0 {
		log.Printf("[acs] %s: all channels disqualified, falling back to lobby", band)
		return electionResult{freq: lobbyFreq, limp: true}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score < scored[j].score
		}
		return scored[i].ch < scored[j].ch
	})
	winner := scored[0]

	if winner.score > limpModeScoreThreshold {
		log.Printf("[acs] %s: best channel %d still poor (score %.2f), falling back to lobby", band, winner.ch, winner.score)
		return electionResult{freq: lobbyFreq, limp: true}
	}

	log.Printf("[acs] %s: elected channel %d (score %.2f)", band, winner.ch, winner.score)
	return electionResult{freq: strconv.Itoa(winner.ch), winnerCh: winner.ch, score: winner.score}
}
