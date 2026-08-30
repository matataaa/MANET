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

// peerChannelVotes tallies how many OTHER active, fresh peers (same
// freshness definition as quorum.go's activeAlfredCount: NODE_STATE ==
// "ACTIVE" and within staleNodeThreshold of LAST_SEEN_TIMESTAMP) currently
// report each candidate channel via the registry's DATA_CHANNEL_2_4/
// DATA_CHANNEL_5_0 gossip fields. Self is excluded — its own current
// channel is handled separately as the cold-start incumbent tiebreak in
// electBand, not counted here. candidates are MHz; the registry field is a
// channel *number* (mesh-registry's getChannel()), translated via
// wifiFreqToChannelNum (tourguide.go) so both sides compare in the same
// unit — the same translation analyzeForeignPartitions already does for a
// different purpose.
func peerChannelVotes(registry map[string]map[string]string, candidates []int, band string) map[int]int {
	field := "DATA_CHANNEL_2_4"
	if band == "5GHz" {
		field = "DATA_CHANNEL_5_0"
	}
	selfMAC := myRegistryMAC()
	now := time.Now().Unix()

	freqByNum := make(map[string]int, len(candidates))
	for _, freq := range candidates {
		freqByNum[strconv.Itoa(wifiFreqToChannelNum(freq))] = freq
	}

	votes := make(map[int]int)
	for mac, fields := range registry {
		if selfMAC != "" && mac == selfMAC {
			continue
		}
		if fields["NODE_STATE"] != "ACTIVE" {
			continue
		}
		ts, err := strconv.ParseInt(fields["LAST_SEEN_TIMESTAMP"], 10, 64)
		if err != nil || now-ts > int64(staleNodeThreshold.Seconds()) {
			continue
		}
		if freq, ok := freqByNum[fields[field]]; ok {
			votes[freq]++
		}
	}
	return votes
}

type electionResult struct {
	freq     string
	limp     bool
	hold     bool
	winnerCh int
	score    float64
}

// electBand runs the deterministic, decentralized election for one band:
// every node computes this from the same aggregated (self+peer) report
// data and the same gossiped peer-channel votes, so they converge on the
// same winner without a coordinator.
//
// Once any peer has voted for any candidate (peerChannelVotes), the
// election is decided purely by (votes desc, rawScore asc, channel asc) —
// no incumbent bias at all. That's deliberate: an additive combination of
// votes and an incumbent bonus was tried and rejected during design,
// because it reintroduces the same failure this replaces — a tied vote
// split still lets each node's own incumbent bias break the tie in its own
// favor, so two nodes can independently "agree to disagree" forever. A
// comparator with zero per-node state once real peer data exists is what
// actually guarantees convergence.
//
// When nobody has voted for anything yet (totalVotes == 0 — a lone or
// freshly-booting node with no gossiped peer channel data), this doesn't
// run an election at all: it holds the current channel and waits. This
// removes the cold-start race outright rather than biasing it toward a
// remembered value (the previous approach, see git history's
// mesh_acs_last_channels/acs_channel_persist.go): a truly isolated node
// has no mesh to optimize for yet, and a simultaneous mesh-wide power loss
// still converges without any persisted state, because
// mesh-boot-lobby.service puts every node's conf back on the lobby
// frequency before wpa_supplicant even starts — a simultaneous cold start
// means every node meshes at the lobby, gossips, gets votes, and elects
// together on the next tick. The tradeoff: a node that is and stays
// genuinely alone (no peer ever) now parks on the lobby frequency
// indefinitely instead of self-optimizing to the quietest channel it can
// see — acceptable since there's no mesh to serve by moving. NOT yet
// field-verified for the true-first-boot and EU-regulatory-domain cases
// the superseded persisted-bias fix explicitly called out as untested
// (see docs/ACS.md) — confirm on real hardware before trusting this over
// the old fix in the field.
func electBand(reports map[string]ChannelReport, registry map[string]map[string]string, candidates []int, currentFreq, lobbyFreq, band string) electionResult {
	currentCh, _ := strconv.Atoi(currentFreq)
	votes := peerChannelVotes(registry, candidates, band)
	totalVotes := 0
	for _, v := range votes {
		totalVotes += v
	}

	if totalVotes == 0 {
		log.Printf("[acs] %s: no peer votes yet (cold start or isolated) — holding current channel", band)
		return electionResult{freq: currentFreq, winnerCh: currentCh, hold: true}
	}

	type candidate struct {
		votes    int
		rawScore float64
		ch       int
	}
	var scored []candidate
	hadAnyData := false

	for _, ch := range candidates {
		stats, ok := aggregateChannelReports(reports, ch)
		if !ok {
			continue
		}
		hadAnyData = true
		if stats.maxNoise > noiseDisqualifyDBM {
			log.Printf("[acs] %s: channel %d disqualified (max_noise %ddBm)", band, ch, stats.maxNoise)
			continue
		}
		rawScore := stats.avgNoise + float64(stats.totalBSS)*0.1
		scored = append(scored, candidate{votes: votes[ch], rawScore: rawScore, ch: ch})
	}

	if len(scored) == 0 {
		if !hadAnyData {
			// No candidate had ANY reading this cycle — an empty/filtered
			// candidate list (e.g. activeBand5Channels found nothing usable
			// on this phy right now, scan.go) or a failed scan with no
			// survey data yet. This is a data outage, not "every candidate
			// is too noisy" — falling back to lobby+limp here would be a
			// mesh-wide disruption (limp throttles every radio in the
			// mesh, setIfaceFrequency restarts wpa_supplicant) triggered
			// by a transient/missing-data condition rather than a real RF
			// problem, and the lobby frequency itself can be illegal under
			// some regulatory domains (WORLD/00 makes 5170-5250 NO-IR,
			// which includes lobbyFreq5's 5180). Hold the current channel
			// instead and let the next cycle try again.
			log.Printf("[acs] %s: no scan data for any candidate — holding current channel", band)
			return electionResult{freq: currentFreq, winnerCh: currentCh, hold: true}
		}
		log.Printf("[acs] %s: all channels disqualified, falling back to lobby", band)
		return electionResult{freq: lobbyFreq, limp: true}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].votes != scored[j].votes {
			return scored[i].votes > scored[j].votes
		}
		if scored[i].rawScore != scored[j].rawScore {
			return scored[i].rawScore < scored[j].rawScore
		}
		return scored[i].ch < scored[j].ch
	})
	winner := scored[0]

	if winner.rawScore > limpModeScoreThreshold {
		log.Printf("[acs] %s: best channel %d still poor (score %.2f), falling back to lobby", band, winner.ch, winner.rawScore)
		return electionResult{freq: lobbyFreq, limp: true}
	}

	log.Printf("[acs] %s: elected channel %d (score %.2f, votes %d)", band, winner.ch, winner.rawScore, winner.votes)
	return electionResult{freq: strconv.Itoa(winner.ch), winnerCh: winner.ch, score: winner.rawScore}
}
