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
	freq       string
	limp       bool
	hold       bool
	coldStart  bool
	winnerCh   int
	score      float64
	hadAnyData bool
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
// actually guarantees convergence. biasFreq (see acsBiasFreq, main.go) is
// never consulted anywhere in this totalVotes > 0 path.
//
// When nobody has voted for anything yet (totalVotes == 0), this used to
// hold the current channel and wait, on the theory that a simultaneous
// mesh-wide power loss still converges without persisted state because
// mesh-boot-lobby.service puts every node's conf back on the lobby
// frequency before wpa_supplicant even starts — every node meshes at the
// lobby, gossips, and elects together on the next tick. That theory had a
// gap, confirmed live 2026-09-01 (EUD3+EUD4, 30+ min continuous hold):
// peerChannelVotes only counts a peer's vote for a channel that's actually
// in candidates, which deliberately excludes the lobby frequency — so
// while every node is still sitting at lobby, every peer's "vote" is for a
// channel that can never be counted, totalVotes stays 0 forever, and the
// old code returned before ever running an election at all. Nobody could
// ever cast the first real vote.
//
// Fix (docs/ACS.md, approved by manet-architect 2026-08-27/28): when
// totalVotes == 0, still score every candidate from self+peer scan report
// data exactly as the totalVotes > 0 path does below, and pick a winner —
// just with the sort's primary key swapped from "votes desc" (structurally
// always a 0-0 tie here) to "matches biasFreq" as a tiebreak, so nodes that
// share the same prior elected channel (e.g. every node persisted the same
// pre-outage frequency) converge on it together instead of each
// independently picking whatever scores best locally. biasFreq itself is
// resolved by the caller (acsBiasFreq, main.go): the live conf value when
// it's already a real candidate, else the persisted last-known-good value
// — never fed into this function's return value directly, only into the
// comparator, so a stale or cross-regulatory-domain bias just fails to
// match anything and this degrades to the old undirected local-noise pick.
// A band with no scan data at all yet (hadAnyData false) still holds, same
// as before — there is nothing to score.
//
// electionResult.coldStart (set here only when currentFreq is still the
// lobby frequency and no election happened this cycle — never on an
// already-converged node with a momentary vote gap) tells the caller
// (runACSTick, main.go) to retry on the very next 15s loop tick instead of
// waiting out the full acsCycleInterval — hardware-verified 2026-08-30
// (EUD3+EUD4 reboot) that without this, the hold's own throttle turns
// "wait for a peer vote" into "wait up to 180s for one".
func electBand(reports map[string]ChannelReport, registry map[string]map[string]string, candidates []int, currentFreq, biasFreq, lobbyFreq, band string) electionResult {
	currentCh, _ := strconv.Atoi(currentFreq)
	votes := peerChannelVotes(registry, candidates, band)
	totalVotes := 0
	for _, v := range votes {
		totalVotes += v
	}

	scored, hadAnyData := scoreCandidates(reports, votes, candidates, band)

	if totalVotes == 0 {
		// electColdStart only runs when this node has nothing real elected
		// yet (still on the lobby frequency) — never on an already-converged
		// node whose peer just temporarily dropped out of gossip. Without
		// this gate, a converged node that loses its own current channel
		// from this cycle's scored set (no survey entry, or disqualified by
		// a single peer's noise reading) would unilaterally hop to whatever
		// scores best now and restart wpa_supplicant on zero peer votes —
		// exactly the disruption the original hold existed to prevent, and
		// the opposite of what a rebooting peer needs from a stable
		// rendezvous point. It also only runs when there's actually
		// something to score — hadAnyData false means no candidate had any
		// reading this cycle at all, nothing for the bias comparator to
		// rank between.
		if currentFreq == lobbyFreq && hadAnyData {
			return electColdStart(scored, biasFreq, currentFreq, lobbyFreq, band)
		}
		if hadAnyData {
			log.Printf("[acs] %s: no peer votes yet (cold start or isolated) — holding current channel", band)
		} else {
			// Distinct from the case above: acsTrackHold (acs_selfheal.go)
			// escalates a sustained hold into a loud "persistent data
			// outage" log line and marker file, worded for exactly this
			// case — not for an isolated-but-scanning node, which is a
			// gossip/quorum problem, not a data problem. hadAnyData carried
			// on electionResult is what lets it tell the two apart instead
			// of mislabeling every sustained hold as a data outage.
			log.Printf("[acs] %s: no peer votes and no scan data yet (cold start or isolated) — holding current channel", band)
		}
		// coldStart only when we haven't elected anything real yet (still
		// on the lobby frequency) — never on an already-converged node
		// whose peer just temporarily dropped out of gossip. The caller
		// uses coldStart to retry sooner than the normal cycle interval;
		// doing that for a converged node with a momentary vote gap would
		// make its tourguide start yanking an already-working data-channel
		// radio to the lobby every retry for no benefit, right when a
		// rebooting peer needs it to stay put as a stable rendezvous point.
		return electionResult{freq: currentFreq, winnerCh: currentCh, hold: true, coldStart: currentFreq == lobbyFreq, hadAnyData: hadAnyData}
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
			return electionResult{freq: currentFreq, winnerCh: currentCh, hold: true, hadAnyData: false}
		}
		log.Printf("[acs] %s: all channels disqualified, falling back to lobby", band)
		return electionResult{freq: lobbyFreq, limp: true, hadAnyData: true}
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
		return electionResult{freq: lobbyFreq, limp: true, hadAnyData: true}
	}

	log.Printf("[acs] %s: elected channel %d (score %.2f, votes %d)", band, winner.ch, winner.rawScore, winner.votes)
	return electionResult{freq: strconv.Itoa(winner.ch), winnerCh: winner.ch, score: winner.rawScore, hadAnyData: true}
}

// incumbentBiasDB nudges electColdStart's comparator toward biasFreq by
// this many dB of effective score, rather than making a bias match win
// outright regardless of how much worse it scores. Bounded on purpose: the
// design's split-risk analysis (docs/ACS.md) reasons about this in terms of
// a small, fixed nudge that a sufficiently large real noise/BSS gap can
// still override — an absolute "bias always wins" rule would let two nodes
// with divergent stale persisted values reject a clearly-better, actually
// shared-RF-correlated channel, which is strictly worse than the case the
// design accepted as a bounded, rare risk.
const incumbentBiasDB = 4.0

type scoredCandidate struct {
	votes    int
	rawScore float64
	ch       int
}

// scoreCandidates aggregates self+peer scan reports into a per-candidate
// noise/BSS score, disqualifying anything too noisy. Shared by electBand's
// normal (totalVotes > 0) path and electColdStart's totalVotes == 0 path
// so both work from identical scan data — only the ranking differs.
func scoreCandidates(reports map[string]ChannelReport, votes map[int]int, candidates []int, band string) (scored []scoredCandidate, hadAnyData bool) {
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
		scored = append(scored, scoredCandidate{votes: votes[ch], rawScore: rawScore, ch: ch})
	}
	return scored, hadAnyData
}

// electColdStart is electBand's totalVotes == 0, still-at-lobby,
// hadAnyData-true branch (see electBand's doc comment for the full
// rationale) — the caller has already established there's real scan data
// to rank. scored is electBand's own scoreCandidates result, passed in
// rather than recomputed so the disqualification log lines fire exactly
// once per cycle regardless of which branch runs. Always produces a real
// decision: either a genuine election, ranked by incumbentBiasDB-adjusted
// score then channel number (never by votes, which are structurally all
// zero here), or the same all-disqualified lobby/limp fallback the normal
// path uses — with coldStart and hadAnyData still set on that fallback so
// the caller keeps retrying every 15s instead of waiting out
// acsCycleInterval, and acsTrackHold's escalation log stays accurate.
func electColdStart(scored []scoredCandidate, biasFreq, currentFreq, lobbyFreq, band string) electionResult {
	coldStart := currentFreq == lobbyFreq
	if len(scored) == 0 {
		log.Printf("[acs] %s: all channels disqualified, falling back to lobby", band)
		return electionResult{freq: lobbyFreq, limp: true, coldStart: coldStart, hadAnyData: true}
	}

	biasCh, _ := strconv.Atoi(biasFreq)
	effScore := func(c scoredCandidate) float64 {
		if c.ch == biasCh {
			return c.rawScore - incumbentBiasDB
		}
		return c.rawScore
	}
	sort.Slice(scored, func(i, j int) bool {
		si, sj := effScore(scored[i]), effScore(scored[j])
		if si != sj {
			return si < sj
		}
		return scored[i].ch < scored[j].ch
	})
	winner := scored[0]

	if winner.rawScore > limpModeScoreThreshold {
		log.Printf("[acs] %s: best channel %d still poor (score %.2f), falling back to lobby", band, winner.ch, winner.rawScore)
		return electionResult{freq: lobbyFreq, limp: true, coldStart: coldStart, hadAnyData: true}
	}

	log.Printf("[acs] %s: no peer votes yet — electing channel %d from local/peer scan data (score %.2f, bias %s)", band, winner.ch, winner.rawScore, biasFreq)
	return electionResult{freq: strconv.Itoa(winner.ch), winnerCh: winner.ch, score: winner.rawScore, hadAnyData: true}
}
