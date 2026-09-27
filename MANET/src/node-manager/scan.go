package main

import (
	"bytes"
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
	// BusyPct is this channel's occupancy (percent of the scan interval
	// spent busy, self-traffic partially subtracted — see busyPctForFreq)
	// for OFF-CHANNEL candidates only; nil for the incumbent (this
	// interface's own current operating channel) and for any candidate
	// this tick couldn't produce a trustworthy reading for. Deliberately a
	// pointer, not a plain float64: a plain float64 unmarshals a
	// JSON-absent field (e.g. from a peer still running pre-occupancy
	// node-manager code) as 0.0, which reads as "perfectly idle" — exactly
	// backwards from "no data." A nil pointer lets channel_election.go's
	// aggregation tell "no reading" apart from "measured 0% busy."
	BusyPct *float64 `json:"busy_pct,omitempty"`
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
	// scanOK is threaded into busyPctForFreq below: on THIS fleet's mt7915e
	// hardware, `iw dev <iface> survey dump`'s per-frequency counters reset
	// on each channel visit rather than accumulating cumulatively (confirmed
	// live — see busyPctForFreq's doc comment), so a rejected/failed scan
	// leaves the PREVIOUS visit's now-stale counters still sitting in the
	// survey dump. Without scanOK, that stale block would be misread as a
	// brand-new visit and double-counted into the occupancy sample ring.
	//
	// A nonzero exit code alone is NOT sufficient to detect this: `iw`
	// exits 0 even when the KERNEL aborts the scan partway through —
	// confirmed against upstream iw's scan.c, handle_scan_combined() prints
	// "scan aborted!" and returns 0 on NL80211_CMD_SCAN_ABORTED. When that
	// happens, every candidate frequency after the abort point still holds
	// its previous visit's stale counters despite a clean exit status, so
	// captured output is checked for that exact string as well — this is
	// the whole reason scanOK exists in the first place, so a false
	// positive here silently defeats the entire safeguard.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scanCmdOut, scanCmdErr := exec.CommandContext(ctx, "iw", args...).CombinedOutput()
	scanOK := scanSucceeded(scanCmdOut, scanCmdErr)

	surveyOut, _ := exec.Command("iw", "dev", iface, "survey", "dump").Output()
	scanOut, _ := exec.Command("iw", "dev", iface, "scan", "dump").Output()

	blocksByFreq := parseSurveyBlocks(string(surveyOut))
	scanText := string(scanOut)

	// curFreq is this interface's CONFIGURED operating channel — read from
	// the wpa_supplicant conf (getConfFreq/wpaConfPath, main.go) rather
	// than an extra `iw dev info` call, matching the signal electBand's
	// own bias tiebreak already uses (main.go's runACSTick). liveFreq is
	// the LIVE channel, read via readIfaceFreq (acs_selfheal.go, already
	// used by the verify-after-apply self-heal). Both are checked — not
	// just the conf value — because they can genuinely differ: a failed
	// `systemctl restart` inside setIfaceFrequency is only logged, not
	// handled, and docs/ACS.md's open issue documents wpa_supplicant's own
	// beacon-avoidance reselect moving the radio off the elected channel
	// on its own. When they differ, checking only the conf value would
	// let the radio's REAL operating channel get scored as if it were an
	// ordinary off-channel candidate (contaminated occupancy), while the
	// configured-but-not-actually-current channel would wrongly lose a
	// legitimate off-channel reading it should have gotten. A candidate is
	// treated as incumbent if it matches EITHER. If neither can be
	// determined (e.g. very first boot, before any conf exists, and `iw`
	// fails), the candidate falls through to being scored like any other —
	// a rare startup-only edge case rather than a steady-state concern.
	curFreq, _ := strconv.Atoi(getConfFreq(wpaConfPath(iface)))
	liveFreqStr, liveOK := readIfaceFreq(iface)
	liveFreq, _ := strconv.Atoi(liveFreqStr)

	out := make([]ChannelScanResult, 0, len(freqs))
	for _, f := range freqs {
		blk, ok := blocksByFreq[f]
		if !ok || !blk.noiseOK {
			// No real survey entry (or no noise reading in it) for this
			// candidate — e.g. an unscannable or regulatory-domain-illegal
			// channel. Previously this synthesized NoiseFloor = -100, which
			// looks like an extremely quiet (great) channel and made
			// electBand's scoring (channel_election.go) crown it winner
			// outright. Drop the candidate from this report entirely
			// instead: with no entry at all, aggregateChannelReports
			// (channel_election.go) correctly sees zero readings for this
			// channel and returns ok=false, so electBand skips it as a
			// candidate rather than electing it.
			continue
		}
		isIncumbent := (curFreq != 0 && f == curFreq) || (liveOK && f == liveFreq)
		out = append(out, ChannelScanResult{
			Channel:    f,
			NoiseFloor: blk.noise,
			BSSCount:   countBSSOnFreq(scanText, f),
			BusyPct:    busyPctForFreq(iface, f, blk, isIncumbent, scanOK),
		})
	}
	return out
}

// scanSucceeded interprets the combined output and error from the `iw dev
// <iface> scan freq ...` command into whether the scan actually completed
// (as opposed to just exiting cleanly). A nonzero exit / command error is
// the obvious failure case, but is NOT sufficient on its own: `iw` exits 0
// even when the KERNEL aborts the scan partway through — confirmed against
// upstream iw's scan.c, handle_scan_combined() prints "scan aborted!" and
// returns 0 on NL80211_CMD_SCAN_ABORTED. Factored out as its own function
// (rather than inlined in scanIface) specifically so this string-matching
// logic has a unit test independent of actually invoking `iw`.
func scanSucceeded(out []byte, err error) bool {
	return err == nil && !bytes.Contains(out, []byte("scan aborted"))
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
// failure, etc.), this fails OPEN — returns the full unfiltered
// band5Channels superset for this cycle — rather than excluding every
// candidate: an empty candidate list here takes the whole band off the
// air on every node simultaneously on a transient read failure, which is
// worse than briefly scanning a candidate this filter couldn't confirm is
// illegal. Matches upstream (very-srs/MANET)'s node-manager-acs.sh
// phy_usable_freqs, which fails open for the same stated reason. This is
// a fast-path optimization, not the only safety net: a real
// regulatory-domain restriction is still caught downstream by
// freqAvailableOnPhy (acs_selfheal.go) before any corrective restart, and
// disqualified by noise/vote scoring in electBand if it's actually unusable.
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
		log.Printf("[acs] 5GHz candidates: %v — failing open, offering unfiltered candidate list this cycle", err)
		out := make([]int, len(band5Channels))
		copy(out, band5Channels)
		return out
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

// surveyBlock holds one frequency's worth of `iw dev <iface> survey dump`
// fields. timesOK is true only when active/busy/transmit time were all
// present in this block — busyPctForFreq refuses to compute a delta from a
// partial set (see its own comment for why).
type surveyBlock struct {
	noise    int
	noiseOK  bool
	activeMs int64
	busyMs   int64
	txMs     int64
	timesOK  bool

	haveActive, haveBusy, haveTx bool
}

// parseSurveyBlocks reads `iw dev <iface> survey dump` output. Each entry
// starts with a "frequency: <MHz> ..." line and is followed by that
// frequency's fields (noise, channel active/busy/transmit time, etc.) until
// the next "frequency:" line or end of output — walking the FULL block is
// required for the busy/active/transmit fields added in this branch, which
// appear further into each block than a one-line lookahead (the previous
// parseSurveyNoise's approach, kept only for the noise field's sake) would
// reach. `iw` retains one historical survey entry per frequency ever seen,
// so a frequency's block can appear more than once — keep only the first
// (matches upstream's `head -1` on the equivalent awk/grep pipeline, and
// this function's own prior single-line-lookahead behavior).
func parseSurveyBlocks(out string) map[int]surveyBlock {
	blocks := make(map[int]surveyBlock)
	var curFreq int
	var cur surveyBlock
	haveFreq := false

	flush := func() {
		if !haveFreq {
			return
		}
		if _, seen := blocks[curFreq]; seen {
			return
		}
		cur.timesOK = cur.haveActive && cur.haveBusy && cur.haveTx
		blocks[curFreq] = cur
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "frequency:" {
			flush()
			freq, err := strconv.Atoi(fields[1])
			if err != nil {
				haveFreq = false
				continue
			}
			curFreq = freq
			cur = surveyBlock{}
			haveFreq = true
			continue
		}
		if !haveFreq {
			continue
		}
		switch {
		case len(fields) >= 2 && fields[0] == "noise:":
			if n, err := strconv.Atoi(fields[1]); err == nil {
				cur.noise = n
				cur.noiseOK = true
			}
		case len(fields) >= 4 && fields[0] == "channel" && fields[1] == "active" && fields[2] == "time:":
			if ms, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
				cur.activeMs = ms
				cur.haveActive = true
			}
		case len(fields) >= 4 && fields[0] == "channel" && fields[1] == "busy" && fields[2] == "time:":
			if ms, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
				cur.busyMs = ms
				cur.haveBusy = true
			}
		case len(fields) >= 4 && fields[0] == "channel" && fields[1] == "transmit" && fields[2] == "time:":
			if ms, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
				cur.txMs = ms
				cur.haveTx = true
			}
		}
	}
	flush()
	return blocks
}

// surveyEntry holds one visit's ABSOLUTE channel active/busy/transmit-time
// counters (ms), as read directly from a single survey block, plus `at`
// (when this sample was captured). Originally this held a delta between two
// ticks' cumulative counters, back when this fleet's hardware was assumed
// to accumulate them cumulatively since interface-up. Live testing found
// that assumption wrong for this fleet's mt7915e driver (see
// occState/busyPctForFreq below) — the counters reset per channel visit
// rather than accumulating — so each surveyEntry now represents one visit's
// own absolute reading, stored in occState's ring rather than diffed
// against a "previous tick" baseline. `at` exists specifically so
// busyPctForFreq can evict samples older than occHoldMax before summing —
// without a per-sample timestamp, occHoldMax only bounded how long the
// aggregate lastPct/lastPctAt was held, not how old the individual samples
// feeding a NEW computation were allowed to be, letting up to occMaxSamples-1
// arbitrarily old samples get silently averaged in alongside one fresh one.
type surveyEntry struct {
	activeMs, busyMs, txMs int64
	at                     time.Time
}

// occState is one (iface, frequency)'s occupancy sampling state: a bounded
// ring of recent per-visit samples, plus the last successfully computed
// busy percentage and when it was computed. Package-level state keyed
// iface -> frequency (MHz), same convention as lastACSCycle (main.go) — no
// locking needed since runACSTick's single loop goroutine is the only
// caller.
type occState struct {
	samples   []surveyEntry
	lastPct   *float64
	lastPctAt time.Time
}

// occ replaces the old delta-based lastSurvey map. Keyed iface -> frequency
// (MHz) -> that frequency's occState.
var occ = make(map[string]map[int]*occState)

// occMaxSamples bounds occState.samples as a sliding ring — old visits age
// out once occMaxSamples newer ones exist, so the computed percentage
// reflects recent conditions rather than an ever-growing all-time average.
const occMaxSamples = 5

// occMaxVisitMs is a sanity ceiling on a single visit's activeMs. Real
// off-channel dwell visits on this fleet's hardware measure ~70-85ms
// (confirmed live); a value far above that on a single visit is more
// likely a parsing artifact or a driver quirk than a real reading, and is
// rejected rather than let it dominate the ring's sum.
const occMaxVisitMs = 500

// occHoldMax is how long a successfully computed busy percentage is held
// and returned before this function reports "no reading" (nil) again, if
// no new valid sample has arrived to refresh it. This matters in general —
// so a channel's score doesn't visibly jump only on cycles where fresh
// data happens to compute, which could flip an election — even though on
// this fleet's hardware a fresh sample should be available almost every
// tick (each visit is well over minSurveyActiveDeltaMs on its own).
const occHoldMax = 60 * time.Minute

// minSurveyActiveDeltaMs is the minimum SUMMED channel-active-time (ms)
// across an (iface, frequency)'s current sample ring required to trust a
// computed busy percentage. Originally this gated a single delta between
// two ticks; it now gates the ring's running sum instead (see
// busyPctForFreq) — the value itself is unchanged (50ms) and still correct
// for the new math: at a few-ms-style single reading, iw's whole-
// millisecond rounding made the ratio nearly worthless (could only land on
// a handful of coarse percentages), but this fleet's real ~70-85ms
// per-visit absolute readings comfortably clear this floor on their own,
// giving reasonable resolution — do not lower this further.
const minSurveyActiveDeltaMs = 50

// busyPctForFreq turns freq's per-visit survey counters (blk) into a 0-100
// busy percentage, or nil when there's no trustworthy reading.
//
// Live hardware finding (this fleet's mt7915e driver): `iw dev <iface>
// survey dump`'s per-frequency active/busy/transmit-time counters do NOT
// accumulate cumulatively since interface-up as originally assumed —
// confirmed via 5 scans spanning ~0.15s to 4 minutes wall time, all
// showing channel active time flat at 71-85ms for the same frequency (a
// true cumulative counter would have grown into the seconds over a
// 244-second session; it did not). This phy also advertises no
// nl80211 SCAN_DWELL capability, so lengthening the dwell isn't an option
// either. The original delta-based design (computing (Δbusy-Δtx)/Δactive
// between this tick's counters and a stored "previous tick" baseline) is
// simply wrong for this hardware: current-minus-a-stale-prior-visit's
// counters is comparing two unrelated, already-reset snapshots, not a
// meaningful delta at all.
//
// Replacement design: each visit's ABSOLUTE counters are one sample in a
// bounded ring (occState.samples, capped at occMaxSamples). Once the ring's
// SUMMED activeMs reaches minSurveyActiveDeltaMs, (Σbusy-Σtx)/Σactive is
// computed, clamped to [0,100], and held as lastPct/lastPctAt — recomputed
// (not just computed once) on every valid new sample, so the value stays a
// smoothed reflection of the most recent (up to occMaxSamples) visits
// rather than either a single noisy sample or a stale one-time snapshot.
// Samples are NOT cleared purely on a successful computation (kept
// accumulating, ring-bounded by count) — on this hardware's observed
// ~70-85ms per-visit magnitude the ring fills past the floor after just 1
// sample almost every time regardless, so a clear-and-restart approach
// would have behaved almost identically in practice; accumulating was
// chosen as the simpler invariant (one code path, no separate "just
// crossed the floor" vs. "already past it" branching). Samples ARE evicted
// by AGE, though (see the cutoff/fresh logic right before the sum below):
// without this, a long gap (a candidate temporarily dropped by regulatory
// filtering, or scans failing for over an hour) would let up to
// occMaxSamples-1 arbitrarily old samples still sitting in the ring get
// averaged in alongside one fresh one — occHoldMax bounds both the held
// lastPct's age AND each individual sample's age with the same "how stale
// is too stale" constant.
//
// Returns nil when:
//   - isIncumbent is true — freq is iface's own current operating channel.
//     This is the off-channel-only design decision: the incumbent's own
//     busy/active time is dominated by real mesh traffic (it's parked
//     there almost all the time, unlike a candidate only visited for a
//     brief scan dwell) — not trusted enough to score, so excluded
//     outright. This is a per-node, per-scan local decision (this
//     interface knows its own current channel); channel_election.go's
//     aggregation never needs an explicit cross-node "is this the
//     incumbent" concept, because a channel a node is sitting on simply
//     never gets a reading FROM THAT NODE. isIncumbent DELETES the entire
//     occState for that frequency (ring AND lastPct/lastPctAt together) —
//     not just the ring — specifically so a held value computed before
//     this node moved onto that channel can't incorrectly reappear as a
//     "fresh-looking" reading once it leaves again.
//   - scanOK is false (the `iw dev <iface> scan freq ...` call itself
//     failed/was rejected) — the survey dump's counters for this
//     frequency are then just whatever was left over from a previous
//     visit, not a new one, and must not be appended as if they were.
//   - blk.timesOK is false, blk.activeMs is 0, or blk.activeMs exceeds
//     occMaxVisitMs — an incomplete or implausible sample is simply not
//     appended (not treated as an error otherwise; the existing ring/held
//     value, if any, is left untouched and still returned subject to
//     occHoldMax below).
//   - no valid sample has ever pushed the ring's summed activeMs past
//     minSurveyActiveDeltaMs (no lastPct computed yet), OR the last
//     successful computation is older than occHoldMax (held value expired).
func busyPctForFreq(iface string, freq int, blk surveyBlock, isIncumbent bool, scanOK bool) *float64 {
	ifaceOcc, ok := occ[iface]
	if !ok {
		ifaceOcc = make(map[int]*occState)
		occ[iface] = ifaceOcc
	}

	if isIncumbent {
		delete(ifaceOcc, freq)
		return nil
	}

	st, hadState := ifaceOcc[freq]
	if !hadState {
		st = &occState{}
		ifaceOcc[freq] = st
	}

	if scanOK && blk.timesOK && blk.activeMs > 0 && blk.activeMs <= occMaxVisitMs {
		now := time.Now()
		st.samples = append(st.samples, surveyEntry{activeMs: blk.activeMs, busyMs: blk.busyMs, txMs: blk.txMs, at: now})
		if len(st.samples) > occMaxSamples {
			// Ring: drop the oldest sample(s) beyond the cap.
			st.samples = st.samples[len(st.samples)-occMaxSamples:]
		}

		// Evict any sample older than occHoldMax BEFORE summing — without
		// this, a long gap (a candidate temporarily dropped by regulatory
		// filtering, scans failing for over an hour, etc.) would let up to
		// occMaxSamples-1 arbitrarily old samples get averaged in alongside
		// one fresh one, e.g. several stale 100%-busy samples plus one
		// fresh 0%-busy visit reporting a falsely high percentage instead
		// of reflecting the fresh reading. occHoldMax doing double duty
		// (bounding both the held lastPct's age AND each individual
		// sample's age) is deliberate — one constant, one "how stale is
		// too stale" answer, rather than a second unrelated knob.
		cutoff := now.Add(-occHoldMax)
		fresh := st.samples[:0:0] // fresh capacity, don't alias st.samples' backing array while iterating it
		for _, s := range st.samples {
			if s.at.After(cutoff) {
				fresh = append(fresh, s)
			}
		}
		st.samples = fresh

		var sumActive, sumBusy, sumTx int64
		for _, s := range st.samples {
			sumActive += s.activeMs
			sumBusy += s.busyMs
			sumTx += s.txMs
		}
		if sumActive >= minSurveyActiveDeltaMs {
			pct := float64(sumBusy-sumTx) / float64(sumActive) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			st.lastPct = &pct
			st.lastPctAt = time.Now()
		}
	}
	// else: failed scan, or an incomplete/implausible sample this tick —
	// don't append, don't recompute; fall through to the held-value check
	// below exactly as if this call hadn't happened.

	if st.lastPct != nil && time.Since(st.lastPctAt) <= occHoldMax {
		// Return a copy, not st.lastPct itself — the caller must not be
		// able to mutate this function's held state through the pointer.
		v := *st.lastPct
		return &v
	}
	return nil
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
