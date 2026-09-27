package main

import (
	"encoding/json"
	"log"
	"math"
	"sort"
	"strconv"
	"time"
)

const (
	// Ignore scan reports older than this (scans run every ACS tick, but a
	// peer's registry entry can lag behind its actual publish cadence).
	reportStaleAfter = 240 * time.Second
	// A reporter "votes bad" on a channel if its own noise reading is worse
	// than this. A channel is only disqualified once at least
	// requiredDisqualifyVotes(reporters) of that channel's own reporters
	// have voted bad — see disqualifyQuorum below. This intentionally no
	// longer disqualifies mesh-wide on a single reporter's reading: one bad
	// radio, connector, or driver shouldn't be able to veto a channel for
	// every other node.
	noiseDisqualifyDBM = -70
	// A reporter also votes a channel bad if its own measured occupancy
	// (BusyPct, scan.go) is at or above this. Same quorum mechanism as
	// noiseDisqualifyDBM — not a second disqualification path (see
	// aggregateChannelReports: a single reporter can only ever cast ONE bad
	// vote total, from noise OR occupancy OR both, never two). Only
	// reporters that actually have a BusyPct reading for this channel can
	// cast this vote (see aggregateChannelReports); a reporter with no
	// reading (nil BusyPct, e.g. the incumbent's own measurement of its own
	// channel, or an old-code peer with no occupancy field at all) simply
	// doesn't participate in this vote, same as it doesn't contribute to
	// medianBusy below.
	//
	// CALIBRATION WARNING: this is a placeholder, not field-validated —
	// unlike limpModeScoreThreshold (below), which at least inherits a
	// pre-existing, previously-live-validated number, 85.0 has no prior art
	// in this codebase at all (occupancy scoring is new in this branch).
	// Also note the implicit weighting this creates in scoreCandidates:
	// rawScore adds medianBusy directly (1 percentage point of occupancy
	// counts the same as 1dBm of noise), which was never a deliberate
	// cross-unit calibration decision — just the simplest additive formula
	// upstream's cff714e used. Revisit both the threshold and the
	// weighting once real occupancy readings exist on real hardware.
	occupancyDisqualifyPct = 85.0
	// Fraction of a channel's own reporters that must independently vote a
	// channel bad (see noiseDisqualifyDBM/occupancyDisqualifyPct) before
	// it's disqualified. ~1/3 of reporters must agree; matches upstream
	// very-srs/MANET's cff714e fix, calibrated for our own 2-3-candidate-
	// channel EU regulatory domain case (fewer candidates = worse impact
	// from a single-reporter false disqualification).
	disqualifyQuorum = 0.34
	// Peer scan values arrive over alfred/mesh-registry gossip, which has no
	// message authentication yet (separate open item) — a malformed or
	// malicious CHANNEL_REPORT_JSON could otherwise inject an implausible
	// reading that dominates the mean/median or overflows bssSum. Any
	// reading outside these ranges is dropped entirely rather than clamped,
	// so it contributes to neither reporters, badVotes, nor the score.
	minValidNoiseDBM = -120
	maxValidNoiseDBM = 0
	maxValidBSSCount = 1000
	// Same untrusted-gossip concern as minValidNoiseDBM/maxValidNoiseDBM
	// above, applied to the occupancy field: an out-of-range BusyPct (e.g.
	// a malicious/buggy peer claiming -9000% or 500% busy) is ignored for
	// occupancy purposes only — unlike an out-of-range noise/BSS reading,
	// this does NOT drop the whole reporter's entry, since the noise/BSS
	// half of the same reading may still be perfectly valid. scan.go's own
	// busyPctForFreq already clamps to this exact range before ever setting
	// BusyPct, so a well-behaved peer's value is always in range; this only
	// guards against a peer that isn't well-behaved.
	minValidBusyPct = 0.0
	maxValidBusyPct = 100.0
	// If even the best surviving channel scores worse than this, the RF
	// environment itself is the problem, not the choice of channel —
	// fall back to the lobby frequency and raise limp mode.
	//
	// CALIBRATION WARNING: this threshold was tuned against the old
	// dBm-ish rawScore range (roughly -100..-60, medianNoise dominant).
	// This branch (feat/acs-occupancy-scoring-offchannel) adds an
	// occupancy term of up to +100 to rawScore for channels with a real
	// off-channel busy_pct reading (see scoreCandidates below), which
	// pushes the achievable rawScore range up to roughly -100..+40 on
	// channels where occupancy is measured. -60.0 has NOT been
	// re-validated against that wider range. Left untouched here
	// deliberately rather than guessed: getting this number wrong in
	// either direction is the most dangerous mistake possible in this
	// branch — too low (more negative) and a genuinely occupied channel
	// never triggers limp mode; too high and occupancy on every real
	// off-channel candidate pushes rawScore above threshold on every
	// tick, putting the whole mesh into permanent limp mode. MUST be
	// recalibrated against real occupancy readings on real hardware
	// before this is trusted in the field — see docs/ACS.md.
	limpModeScoreThreshold = -60.0

	lobbyFreq24 = "2412"
	lobbyFreq5  = "5180"
)

type channelStats struct {
	medianNoise float64
	meanBSS     float64
	medianBusy  float64
	haveBusy    bool
	badVotes    int
	reporters   int
}

// requiredDisqualifyVotes returns how many of a channel's own reporters
// must vote it bad (noiseDisqualifyDBM/occupancyDisqualifyPct) before it's
// disqualified. Always at least 1, so a solo/isolated node (reporters == 1)
// can still reject a jammed channel by itself — same as before this fix.
func requiredDisqualifyVotes(reporters int) int {
	n := int(math.Ceil(float64(reporters) * disqualifyQuorum))
	if n < 1 {
		n = 1
	}
	return n
}

// median returns the median of vals. It sorts a copy rather than relying on
// input order, and callers must never rely on map iteration order to build
// vals — this election runs independently on every mesh node from the same
// gossiped data, so two nodes that fed the same values in different orders
// must still compute the identical median (and therefore the identical
// winner), or the mesh partitions.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

// validScanResult rejects implausible peer-supplied noise/BSS readings (see
// the minValidNoiseDBM/maxValidNoiseDBM/maxValidBSSCount comment) — dropped
// entirely rather than clamped, so a bad reading can't silently become a
// boundary value that still skews the mean/median. Deliberately does NOT
// validate BusyPct here: an out-of-range occupancy reading should not
// discard an otherwise-valid noise/BSS reading from the same reporter — see
// aggregateChannelReports, which range-checks BusyPct independently and
// only ignores the occupancy contribution, not the whole entry.
func validScanResult(res ChannelScanResult) bool {
	return res.NoiseFloor >= minValidNoiseDBM && res.NoiseFloor <= maxValidNoiseDBM &&
		res.BSSCount >= 0 && res.BSSCount <= maxValidBSSCount
}

// aggregateChannelReports merges every fresh report (self + peers, keyed
// arbitrarily) into a per-channel view: median noise and mean BSS count
// across vantage points, plus how many of those vantage points voted the
// channel bad. Aggregating across nodes rather than trusting only the local
// scan accounts for hidden-node effects — a channel can look clean locally
// but be busy from a neighbor's vantage point. Median (not max) and mean
// (not sum) keep a single outlier reporter, or a channel simply visible to
// more nodes, from distorting the result — see requiredDisqualifyVotes for
// the matching quorum-based disqualify decision.
//
// Takes at most one reading per (reporter, channel) pair — the first
// Results entry matching channel in a given report, regardless of whether
// it turns out to be valid. A report with duplicate entries for the same
// channel (a buggy/older build, or — since alfred/mesh-registry gossip has
// no message authentication yet — a malformed/malicious peer payload)
// would otherwise inflate both reporters and badVotes from a single real
// peer, letting one node's report still disqualify a channel mesh-wide
// under the new quorum rule exactly as it could under the old any-single-
// reporter rule.
//
// Occupancy (medianBusy/haveBusy) is aggregated inside this SAME
// deduped/validated per-(reporter,channel) block, from whichever of those
// reports also carry a non-nil, in-range BusyPct (scan.go's
// ChannelScanResult.BusyPct, a pointer specifically so "no reading" is
// distinguishable from "measured 0% busy"). Keeping it in the same loop
// (rather than a separate pass over reports) is deliberate: a separate pass
// would re-introduce exactly the kind of duplicate-counting the dedup
// fix above exists to prevent, just for the occupancy vote specifically.
// This is also what implements the off-channel-only design decision
// without any explicit per-node "am I the incumbent" bookkeeping: a node
// scanning its own current operating channel (scan.go's busyPctForFreq)
// never produces a trustworthy busy_pct for it, so that channel's
// occupancy is simply never populated by that reporter — it's populated
// instead by whichever OTHER fresh reporters have this channel as one of
// their own off-channel scan candidates. Every node computes
// haveBusy/medianBusy identically from the same gossiped reports, so
// there's no risk of two nodes disagreeing about which channels get an
// occupancy term.
//
// A single reporter casts AT MOST ONE bad vote per channel, regardless of
// how many separate reasons it has (bad noise AND high occupancy both
// present on the same reading still counts as 1, via the single `bad`
// bool below) — this was a real bug found in review: counting noise and
// occupancy as two independent badVotes++ sites let one reporter alone
// reach requiredDisqualifyVotes' quorum (e.g. quorum of 2, one reporter
// bad on both axes), reintroducing the exact "one node vetoes a channel
// mesh-wide" failure this whole quorum mechanism exists to close, just via
// the occupancy path instead of noise.
//
// KNOWN STRUCTURAL GAP, flagged by architect review, NOT fixed here (a
// future pass, deliberately out of scope for this branch): occupancy can
// only ever penalize OTHER candidate channels — it can never make the
// mesh's own currently-elected/incumbent channel look bad, no matter how
// busy that channel actually gets. This isn't just the per-node
// isIncumbent exclusion (scan.go) working as designed — it's a gap in
// what the exclusion implies at the mesh level: once every (or nearly
// every) node in the mesh has converged onto the same winning channel,
// NO fresh reporter has that channel as an off-channel scan candidate
// anymore (everyone's incumbent is the same channel), so haveBusy for it
// simply stops becoming true at all — not just from one node's own
// measurement, from the WHOLE mesh's. Contrary to what this file (and
// docs/ACS.md) describe elsewhere ("gets scored normally from any other
// fresh reporter for whom this channel is one of their own off-channel
// scan candidates"), that only holds while at least one fresh reporter is
// actually NOT on this channel — it silently stops holding once the mesh
// has fully converged, which is exactly the steady state this scoring
// exists to operate in most of the time. Net effect: occupancy can
// reshuffle which ALTERNATIVE channel looks best whenever a real election
// runs (quorum loss, noise disqualification, etc.), but can never be the
// reason the mesh decides to leave a channel it's already sitting on,
// however busy that channel becomes. This is not merely a failure to act
// on the incumbent's true occupancy — it's an ACTIVE, asymmetric bias IN
// FAVOR of the incumbent: scoreCandidates' rawScore adds +medianBusy for
// every measured (haveBusy=true) candidate, but the unmeasured incumbent
// contributes exactly 0 for that term, structurally, every time. So a
// busy-but-unmeasured incumbent is scored as if it had zero occupancy
// while every measured alternative is penalized for whatever occupancy it
// actually has — the incumbent doesn't just fail to lose ground, it
// systematically looks better than reality relative to any channel that
// DOES get measured. Harmless today only because occupancy was always nil
// before this branch (scan.go's per-visit sample ring,
// feat/acs-occupancy-scoring-offchannel) — now that it actually computes
// real values, this is a real (if not urgent) design gap, not a
// hypothetical one.
func aggregateChannelReports(reports map[string]ChannelReport, channel int) (channelStats, bool) {
	var noises []float64
	var busys []float64
	var bssSum int
	badVotes := 0
	for _, r := range reports {
		for _, res := range r.Results {
			if res.Channel != channel {
				continue
			}
			if validScanResult(res) {
				noises = append(noises, float64(res.NoiseFloor))
				bssSum += res.BSSCount
				bad := res.NoiseFloor > noiseDisqualifyDBM
				if res.BusyPct != nil {
					if bp := *res.BusyPct; bp >= minValidBusyPct && bp <= maxValidBusyPct {
						busys = append(busys, bp)
						if bp >= occupancyDisqualifyPct {
							bad = true
						}
					}
					// Out-of-range BusyPct is silently ignored for
					// occupancy purposes only — the noise/BSS reading from
					// this same reporter (already recorded above) is still
					// used, and it can still independently make `bad` true
					// via the noise check above.
				}
				if bad {
					badVotes++
				}
			}
			break
		}
	}
	if len(noises) == 0 {
		return channelStats{}, false
	}
	stats := channelStats{
		medianNoise: median(noises),
		meanBSS:     float64(bssSum) / float64(len(noises)),
		badVotes:    badVotes,
		reporters:   len(noises),
	}
	if len(busys) > 0 {
		stats.medianBusy = median(busys)
		stats.haveBusy = true
	}
	return stats, true
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
		// from this cycle's scored set (no survey entry, or disqualified
		// because enough of its own reporters' noise/occupancy votes
		// crossed the requiredDisqualifyVotes quorum) would unilaterally
		// hop to whatever scores best now and restart wpa_supplicant on
		// zero peer votes — exactly the disruption the original hold
		// existed to prevent, and the opposite of what a rebooting peer
		// needs from a stable rendezvous point. It also only runs when
		// there's actually something to score — hadAnyData false means no
		// candidate had any reading this cycle at all, nothing for the
		// bias comparator to rank between.
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

	// Compared against baseScore (pre-occupancy formula), not rawScore —
	// see scoreCandidates' doc comment for why: limpModeScoreThreshold was
	// only ever validated against the pre-occupancy range, and comparing
	// it against the occupancy-inclusive rawScore instead would trip limp
	// mode under ordinary conditions (every 2.4GHz candidate gets a real
	// occupancy reading during cold start).
	if winner.baseScore > limpModeScoreThreshold {
		log.Printf("[acs] %s: best channel %d still poor (score %.2f), falling back to lobby", band, winner.ch, winner.baseScore)
		return electionResult{freq: lobbyFreq, limp: true, hadAnyData: true}
	}

	log.Printf("[acs] %s: elected channel %d (score %.2f, votes %d)", band, winner.ch, winner.rawScore, winner.votes)
	return electionResult{freq: strconv.Itoa(winner.ch), winnerCh: winner.ch, score: winner.rawScore, hadAnyData: true}
}

// incumbentBiasScore nudges electColdStart's comparator toward biasFreq by
// this many points of effective score, rather than making a bias match win
// outright regardless of how much worse it scores. Bounded on purpose: the
// design's split-risk analysis (docs/ACS.md) reasons about this in terms of
// a small, fixed nudge that a sufficiently large real noise/BSS/occupancy
// gap can still override — an absolute "bias always wins" rule would let
// two nodes with divergent stale persisted values reject a clearly-better,
// actually shared-RF-correlated channel, which is strictly worse than the
// case the design accepted as a bounded, rare risk.
//
// Renamed from incumbentBiasDB (was a dB quantity when rawScore was pure
// noise+BSS) now that rawScore can also include an up-to-100-point
// occupancy term (see scoreCandidates) — it's no longer meaningfully "dB"
// at all. Value picked conservatively small (2.0, was 4.0 pre-occupancy)
// and is explicitly a placeholder needing field calibration, not a derived
// number: docs/ACS.md's incident history records that this project has
// already broken mesh-wide convergence once by setting an incumbent bias
// too large (the original 10dB bias swamped the ~1.4dB real noise-score
// gaps between candidates, so nodes "agreed" on paper but never actually
// converged) — this value only ever matters in the totalVotes == 0
// cold-start path, but the same failure mode (a bias too large relative to
// the real gaps between candidates) applies just as much to the new,
// wider occupancy-inclusive score range. Do not raise this without
// re-reading that incident.
const incumbentBiasScore = 2.0

type scoredCandidate struct {
	votes int
	// rawScore includes the occupancy term (when haveBusy) and is used for
	// ranking/quorum decisions — which channel wins, and (via badVotes,
	// aggregateChannelReports) disqualification. baseScore is the SAME
	// formula MINUS occupancy — exactly what rawScore would have been on
	// the pre-occupancy code — and is used ONLY for the limp-mode
	// threshold comparison (limpModeScoreThreshold) in electBand/
	// electColdStart. See scoreCandidates for why these two are
	// deliberately kept separate rather than comparing rawScore against
	// limpModeScoreThreshold directly.
	rawScore  float64
	baseScore float64
	ch        int
}

// scoreCandidates aggregates self+peer scan reports into a per-candidate
// noise/BSS/occupancy score, disqualifying anything too noisy or too busy.
// Shared by electBand's normal (totalVotes > 0) path and electColdStart's
// totalVotes == 0 path so both work from identical scan data — only the
// ranking differs.
//
// rawScore = medianNoise + meanBSS*0.1, plus medianBusy added on top ONLY
// when at least one reporter had a real off-channel occupancy reading for
// this channel (stats.haveBusy) — see aggregateChannelReports for how that
// off-channel-only condition is derived purely from the gossiped data, with
// no explicit incumbent bookkeeping. When haveBusy is false (no reporter
// currently has this channel as an off-channel scan candidate — which is
// exactly the incumbent/no-data case), this scores identically to the
// pre-occupancy branch, so a mid-rollout mesh with a mix of old and new
// node-manager code degrades cleanly rather than splitting into two
// incomparable score families: every node either has a real occupancy
// reading for a channel or doesn't, and either way computes the same
// rawScore from the same gossiped inputs.
//
// baseScore is ALWAYS the pre-occupancy formula (medianNoise + meanBSS*0.1),
// regardless of haveBusy — kept alongside rawScore specifically so
// electBand/electColdStart can compare baseScore (not rawScore) against
// limpModeScoreThreshold. This matters because during a cold start every
// 2.4GHz candidate is off-channel for every node (band24Channels excludes
// the lobby frequency, so both 2.4GHz candidates get a real occupancy
// reading the moment any scan runs) — comparing the occupancy-inclusive
// rawScore against a threshold that was only ever validated against the
// pre-occupancy range would trip limp mode under perfectly ordinary RF
// conditions (confirmed: two reporters at -95dBm with 40-45% busy already
// exceeds -60.0). Occupancy still fully participates in ranking (which
// channel wins) and disqualification (badVotes, above) — it just doesn't
// feed the limp-mode judgment call until limpModeScoreThreshold itself has
// been recalibrated against real occupancy data.
func scoreCandidates(reports map[string]ChannelReport, votes map[int]int, candidates []int, band string) (scored []scoredCandidate, hadAnyData bool) {
	for _, ch := range candidates {
		stats, ok := aggregateChannelReports(reports, ch)
		if !ok {
			continue
		}
		hadAnyData = true
		if required := requiredDisqualifyVotes(stats.reporters); stats.badVotes >= required {
			log.Printf("[acs] %s: channel %d disqualified (%d/%d reporters over %ddBm noise or %.0f%% busy, quorum %d)", band, ch, stats.badVotes, stats.reporters, noiseDisqualifyDBM, occupancyDisqualifyPct, required)
			continue
		}
		// Explicit float64(...) conversion on the multiply forces IEEE-754
		// rounding before the addition, preventing the compiler from fusing
		// this into a single FMA instruction. Without it, arm64 (fuses) and
		// amd64 (doesn't) can compute a different rawScore from identical
		// input — this election runs coordinator-free with no shared state,
		// and build-x86-tarball.sh ships this same binary for x86 nodes, so
		// a mixed-architecture mesh must never see two different scores (or
		// therefore two different winners) for the same reports.
		baseScore := stats.medianNoise + float64(stats.meanBSS*0.1)
		rawScore := baseScore
		if stats.haveBusy {
			// A plain addition, not a multiply — no FMA-fusion risk here to
			// guard against the same way as the meanBSS term above.
			rawScore += stats.medianBusy
		}
		scored = append(scored, scoredCandidate{votes: votes[ch], rawScore: rawScore, baseScore: baseScore, ch: ch})
	}
	return scored, hadAnyData
}

// electColdStart is electBand's totalVotes == 0, still-at-lobby,
// hadAnyData-true branch (see electBand's doc comment for the full
// rationale) — the caller has already established there's real scan data
// to rank. scored is electBand's own scoreCandidates result, passed in
// rather than recomputed so the disqualification log lines fire exactly
// once per cycle regardless of which branch runs. Always produces a real
// decision: either a genuine election, ranked by incumbentBiasScore-adjusted
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
			return c.rawScore - incumbentBiasScore
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

	// See electBand's matching comment: compared against baseScore, not
	// rawScore, so the limp-mode decision doesn't inherit occupancy's
	// wider, not-yet-validated score range.
	if winner.baseScore > limpModeScoreThreshold {
		log.Printf("[acs] %s: best channel %d still poor (score %.2f), falling back to lobby", band, winner.ch, winner.baseScore)
		return electionResult{freq: lobbyFreq, limp: true, coldStart: coldStart, hadAnyData: true}
	}

	log.Printf("[acs] %s: no peer votes yet — electing channel %d from local/peer scan data (score %.2f, bias %s)", band, winner.ch, winner.rawScore, biasFreq)
	return electionResult{freq: strconv.Itoa(winner.ch), winnerCh: winner.ch, score: winner.rawScore, hadAnyData: true}
}
