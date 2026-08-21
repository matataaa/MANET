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
	// Cold-start-only tiebreak (see electBand): once any peer has voted
	// for a candidate this cycle, incumbentBiasDB plays no role at all —
	// peerChannelVotes decides. It only prevents flapping between two
	// channels whose scores differ purely by scan-to-scan noise before
	// any peer data exists yet. Picked from observed live noise drift on
	// a repeatedly-re-elected channel (~2-4dB cycle to cycle): large
	// enough to absorb that, deliberately far below a single peer vote's
	// weight so it can never compete with real peer consensus.
	incumbentBiasDB = 4.0
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
// no incumbent bias at all in this branch. That's deliberate: an additive
// combination of votes and an incumbent bonus was tried and rejected
// during design, because it reintroduces the same failure this replaces —
// a tied vote split still lets each node's own incumbent bias break the
// tie in its own favor, so two nodes can independently "agree to disagree"
// forever. A comparator with zero per-node state once real peer data
// exists is what actually guarantees convergence.
//
// Only when nobody has voted for anything yet (true cold start — a lone
// or freshly-booting node with no gossiped peer channel data) does the
// election fall back to raw noise score with a small incumbentBiasDB
// tiebreak, matching the original behavior for that case.
func electBand(reports map[string]ChannelReport, registry map[string]map[string]string, candidates []int, currentFreq, lobbyFreq, band string) electionResult {
	currentCh, _ := strconv.Atoi(currentFreq)
	votes := peerChannelVotes(registry, candidates, band)
	totalVotes := 0
	for _, v := range votes {
		totalVotes += v
	}

	type candidate struct {
		votes    int
		rawScore float64
		ch       int
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
		rawScore := stats.avgNoise + float64(stats.totalBSS)*0.1
		scored = append(scored, candidate{votes: votes[ch], rawScore: rawScore, ch: ch})
	}

	if len(scored) == 0 {
		log.Printf("[acs] %s: all channels disqualified, falling back to lobby", band)
		return electionResult{freq: lobbyFreq, limp: true}
	}

	sort.Slice(scored, func(i, j int) bool {
		if totalVotes > 0 {
			if scored[i].votes != scored[j].votes {
				return scored[i].votes > scored[j].votes
			}
			if scored[i].rawScore != scored[j].rawScore {
				return scored[i].rawScore < scored[j].rawScore
			}
			return scored[i].ch < scored[j].ch
		}
		iScore, jScore := scored[i].rawScore, scored[j].rawScore
		if scored[i].ch == currentCh {
			iScore -= incumbentBiasDB
		}
		if scored[j].ch == currentCh {
			jScore -= incumbentBiasDB
		}
		if iScore != jScore {
			return iScore < jScore
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
