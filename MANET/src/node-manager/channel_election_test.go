package main

import "testing"

func TestRequiredDisqualifyVotes(t *testing.T) {
	cases := []struct {
		reporters int
		want      int
	}{
		{1, 1},
		{2, 1},
		{3, 2},
		{4, 2},
		{10, 4},
	}
	for _, c := range cases {
		got := requiredDisqualifyVotes(c.reporters)
		if got != c.want {
			t.Errorf("requiredDisqualifyVotes(%d) = %d, want %d", c.reporters, got, c.want)
		}
	}
}

func makeReport(results ...ChannelScanResult) ChannelReport {
	return ChannelReport{Results: results}
}

// busyPtr is a small helper for building ChannelScanResult literals with a
// non-nil BusyPct in tests (occupancy scoring, feat/acs-occupancy-scoring-offchannel).
func busyPtr(v float64) *float64 { return &v }

// TestSingleBadReporterNoLongerDisqualifies: 4 reporters on one channel,
// only 1 sees noise worse than noiseDisqualifyDBM (-70dBm; higher/less
// negative is worse). Today's old max-based rule would disqualify the
// channel outright on that single report. Under the quorum fix, 1 bad vote
// out of 4 reporters is below requiredDisqualifyVotes(4) == 2, so the
// channel must survive.
func TestSingleBadReporterNoLongerDisqualifies(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1}),
		"b":    makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
		"c":    makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
		"d":    makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
	}
	scored, hadAnyData := scoreCandidates(reports, map[int]int{}, []int{2437}, "2.4GHz")
	if !hadAnyData {
		t.Fatal("expected hadAnyData true")
	}
	if len(scored) != 1 {
		t.Fatalf("expected channel 2437 to survive quorum-based disqualification, got scored=%v", scored)
	}
	if scored[0].ch != 2437 {
		t.Fatalf("expected surviving channel 2437, got %d", scored[0].ch)
	}
}

// TestSoloReporterBadNoiseDisqualified: an isolated node (reporters == 1)
// must still be able to reject a jammed channel on its own reading alone —
// requiredDisqualifyVotes(1) == 1, so a single bad vote from a single
// reporter is enough.
func TestSoloReporterBadNoiseDisqualified(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1}),
	}
	scored, hadAnyData := scoreCandidates(reports, map[int]int{}, []int{2437}, "2.4GHz")
	if !hadAnyData {
		t.Fatal("expected hadAnyData true")
	}
	if len(scored) != 0 {
		t.Fatalf("expected solo reporter's bad reading to disqualify channel 2437, got scored=%v", scored)
	}
}

// TestQuorumBoundaryTwoOfFour: 4 reporters, exactly 2 vote bad.
// requiredDisqualifyVotes(4) == 2, so badVotes >= required and the channel
// must be disqualified right at the boundary (not just when it's exceeded).
func TestQuorumBoundaryTwoOfFour(t *testing.T) {
	reports := map[string]ChannelReport{
		"a": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1}),
		"b": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1}),
		"c": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
		"d": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{2437}, "2.4GHz")
	if len(scored) != 0 {
		t.Fatalf("expected 2-of-4 bad votes to disqualify channel 2437 at the quorum boundary, got scored=%v", scored)
	}
}

// TestAllReportersBadDisqualifies: every reporter votes bad — the obvious
// case the quorum mechanism must still catch.
func TestAllReportersBadDisqualifies(t *testing.T) {
	reports := map[string]ChannelReport{
		"a": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -60, BSSCount: 1}),
		"b": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1}),
		"c": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -50, BSSCount: 1}),
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{2437}, "2.4GHz")
	if len(scored) != 0 {
		t.Fatalf("expected all-bad reporters to disqualify channel 2437, got scored=%v", scored)
	}
}

// TestMeanNotSumBSS: two channels with identical per-reporter BSS counts,
// one seen by 1 reporter and the other by 4 reporters, must score
// identically now that BSS is averaged rather than summed.
func TestMeanNotSumBSS(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(
			ChannelScanResult{Channel: 2437, NoiseFloor: -80, BSSCount: 5},
			ChannelScanResult{Channel: 2462, NoiseFloor: -80, BSSCount: 5},
		),
		"b": makeReport(
			ChannelScanResult{Channel: 2462, NoiseFloor: -80, BSSCount: 5},
		),
		"c": makeReport(
			ChannelScanResult{Channel: 2462, NoiseFloor: -80, BSSCount: 5},
		),
		"d": makeReport(
			ChannelScanResult{Channel: 2462, NoiseFloor: -80, BSSCount: 5},
		),
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{2437, 2462}, "2.4GHz")
	scoreByCh := map[int]float64{}
	for _, s := range scored {
		scoreByCh[s.ch] = s.rawScore
	}
	if len(scoreByCh) != 2 {
		t.Fatalf("expected both channels scored, got %v", scoreByCh)
	}
	if scoreByCh[2437] != scoreByCh[2462] {
		t.Fatalf("expected identical scores for 1-reporter vs 4-reporter channel with equal per-reporter BSS, got %d=%.4f %d=%.4f",
			2437, scoreByCh[2437], 2462, scoreByCh[2462])
	}
}

// TestDifferentReporterCountsPerChannel: two candidate channels scored in
// the same scoreCandidates call, with different reporter counts and
// different bad-vote outcomes, must each be evaluated against their OWN
// reporter count's quorum, not a shared/global one. Channel 2437 has 1
// reporter voting bad (disqualified, quorum of 1). Channel 2462 has 4
// reporters with only 1 voting bad (survives, quorum of 2 not met).
func TestDifferentReporterCountsPerChannel(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
			ChannelScanResult{Channel: 2462, NoiseFloor: -65, BSSCount: 1},
		),
		"b": makeReport(ChannelScanResult{Channel: 2462, NoiseFloor: -90, BSSCount: 1}),
		"c": makeReport(ChannelScanResult{Channel: 2462, NoiseFloor: -90, BSSCount: 1}),
		"d": makeReport(ChannelScanResult{Channel: 2462, NoiseFloor: -90, BSSCount: 1}),
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{2437, 2462}, "2.4GHz")
	surviving := map[int]bool{}
	for _, s := range scored {
		surviving[s.ch] = true
	}
	if surviving[2437] {
		t.Errorf("expected channel 2437 (1 reporter, 1 bad vote, quorum 1) to be disqualified")
	}
	if !surviving[2462] {
		t.Errorf("expected channel 2462 (4 reporters, 1 bad vote, quorum 2) to survive")
	}
}

// TestDuplicateEntriesFromOneReporter (W1): a single peer's report contains
// 3 duplicate entries for the same channel, all bad. Before the fix this
// inflated both reporters and badVotes to 3, disqualifying the channel
// mesh-wide from one real node. After the fix, only the first matching
// entry counts — 1 reporter, 1 bad vote, requiredDisqualifyVotes(1) == 1 —
// so it's still disqualified here (correctly, on its own single real
// report), but reporters/badVotes must reflect 1 real peer, not 3.
func TestDuplicateEntriesFromOneReporter(t *testing.T) {
	reports := map[string]ChannelReport{
		"peer1": makeReport(
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
		),
	}
	stats, ok := aggregateChannelReports(reports, 2437)
	if !ok {
		t.Fatal("expected data for channel 2437")
	}
	if stats.reporters != 1 {
		t.Errorf("expected 1 reporter after deduping duplicate entries, got %d", stats.reporters)
	}
	if stats.badVotes != 1 {
		t.Errorf("expected 1 bad vote after deduping duplicate entries, got %d", stats.badVotes)
	}
}

// TestDuplicateEntriesDoNotFalselyDisqualifyMeshWide (W1): the scenario from
// the review — one real peer's report has 3 duplicate bad-noise entries for
// a channel alongside 3 other genuinely distinct, good-noise reporters. The
// buggy pre-fix behavior counted reporters=6/badVotes=3, meeting
// requiredDisqualifyVotes(6)==3 and disqualifying the channel from a single
// real bad reporter. After the fix this must be reporters=4/badVotes=1,
// which is below requiredDisqualifyVotes(4)==2, so the channel survives.
func TestDuplicateEntriesDoNotFalselyDisqualifyMeshWide(t *testing.T) {
	reports := map[string]ChannelReport{
		"dup": makeReport(
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
			ChannelScanResult{Channel: 2437, NoiseFloor: -65, BSSCount: 1},
		),
		"good1": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
		"good2": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
		"good3": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 1}),
	}
	stats, ok := aggregateChannelReports(reports, 2437)
	if !ok {
		t.Fatal("expected data for channel 2437")
	}
	if stats.reporters != 4 {
		t.Fatalf("expected 4 real reporters (not 6), got %d", stats.reporters)
	}
	if stats.badVotes != 1 {
		t.Fatalf("expected 1 real bad vote (not 3), got %d", stats.badVotes)
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{2437}, "2.4GHz")
	if len(scored) != 1 {
		t.Fatalf("expected channel 2437 to survive (1 bad vote of 4 real reporters, quorum 2), got scored=%v", scored)
	}
}

// TestInvalidPeerValuesDropped (W2): peer-supplied bss_count/noise_floor are
// untrusted (no message auth on alfred/mesh-registry gossip) and must be
// range-validated. An out-of-range reading (negative BSS count here) is
// dropped entirely — it must not count toward reporters, badVotes, or the
// score, and must not let a wildly negative BSSCount produce a
// suspiciously-good winning score.
func TestInvalidPeerValuesDropped(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -80, BSSCount: 5}),
		"evil": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -80, BSSCount: -1000}),
	}
	stats, ok := aggregateChannelReports(reports, 2437)
	if !ok {
		t.Fatal("expected data for channel 2437")
	}
	if stats.reporters != 1 {
		t.Fatalf("expected the out-of-range BSSCount reading to be dropped, leaving 1 reporter, got %d", stats.reporters)
	}
	if stats.meanBSS != 5 {
		t.Fatalf("expected meanBSS unaffected by the dropped reading, got %.4f", stats.meanBSS)
	}
}

// TestInvalidPeerNoiseDropped (W2): same as above, for an out-of-range
// noise floor reading rather than BSS count.
func TestInvalidPeerNoiseDropped(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -80, BSSCount: 1}),
		"evil": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -500, BSSCount: 1}),
	}
	stats, ok := aggregateChannelReports(reports, 2437)
	if !ok {
		t.Fatal("expected data for channel 2437")
	}
	if stats.reporters != 1 {
		t.Fatalf("expected the out-of-range NoiseFloor reading to be dropped, leaving 1 reporter, got %d", stats.reporters)
	}
	if stats.medianNoise != -80 {
		t.Fatalf("expected medianNoise unaffected by the dropped reading, got %.4f", stats.medianNoise)
	}
}

// TestInvalidPeerBusyPctIgnored: mirrors the W2 noise/BSS range-validation
// tests above, for the occupancy field added by
// feat/acs-occupancy-scoring-offchannel. An out-of-range BusyPct (untrusted,
// same unauthenticated gossip path as noise/BSS) must be ignored for
// occupancy purposes ONLY — unlike an invalid noise/BSS reading, it must
// NOT drop the rest of that reporter's (still valid) noise/BSS reading.
func TestInvalidPeerBusyPctIgnored(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(20)}),
		"evil": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -81, BSSCount: 1, BusyPct: busyPtr(-9000)}),
	}
	stats, ok := aggregateChannelReports(reports, 5220)
	if !ok {
		t.Fatal("expected data for channel 5220")
	}
	// Both reporters' noise/BSS readings are valid and must still count.
	if stats.reporters != 2 {
		t.Fatalf("expected both reporters' noise/BSS readings kept despite one's invalid BusyPct, got reporters=%d", stats.reporters)
	}
	// Only "self"'s in-range BusyPct (20) should feed medianBusy.
	if !stats.haveBusy {
		t.Fatal("expected haveBusy=true from the one valid BusyPct reading")
	}
	if stats.medianBusy != 20 {
		t.Fatalf("expected medianBusy=20 (only the valid reading), got %.4f", stats.medianBusy)
	}
}

// TestInvalidPeerBusyPctNeverCastsBadVote (W2, vote side): an out-of-range
// BusyPct must not merely be excluded from medianBusy — it must also never
// cast an occupancy bad vote. TestInvalidPeerBusyPctIgnored above only
// exercises the median/reporter-count side with a value (-9000) that
// wouldn't have crossed occupancyDisqualifyPct even without the range
// check; this uses an out-of-range value (500) that WOULD trip the
// disqualify vote if the range check were missing, across enough reporters
// to meet quorum, and asserts the channel survives.
func TestInvalidPeerBusyPctNeverCastsBadVote(t *testing.T) {
	reports := map[string]ChannelReport{
		"a": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(500)}),
		"b": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(500)}),
		"c": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
		"d": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
	}
	stats, ok := aggregateChannelReports(reports, 5220)
	if !ok {
		t.Fatal("expected data for channel 5220")
	}
	if stats.badVotes != 0 {
		t.Fatalf("expected 0 bad votes — both out-of-range BusyPct=500 readings must be ignored, got badVotes=%d", stats.badVotes)
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{5220}, "5GHz")
	if len(scored) != 1 {
		t.Fatalf("expected channel 5220 to survive (invalid BusyPct must never cast a bad vote), got scored=%v", scored)
	}
}

// TestSingleReporterBadOnBothNoiseAndOccupancyCastsOneVote (C1): a reporter
// with BOTH bad noise (>noiseDisqualifyDBM) AND high occupancy
// (>=occupancyDisqualifyPct) must cast exactly ONE bad vote total, not two.
// Before this fix, noise and occupancy each incremented badVotes
// independently, so a single such reporter alone could reach
// requiredDisqualifyVotes' quorum (e.g. quorum of 2 with badVotes=2 from
// one reporter), reintroducing the "one node vetoes a channel mesh-wide"
// failure the quorum mechanism exists to prevent — just via the occupancy
// path instead of noise. Here: 1 reporter bad on both axes + 3 clean
// reporters = requiredDisqualifyVotes(4) == 2; with the bug, badVotes would
// be 2 (disqualified); fixed, badVotes must be 1 (survives).
func TestSingleReporterBadOnBothNoiseAndOccupancyCastsOneVote(t *testing.T) {
	reports := map[string]ChannelReport{
		"badBoth": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -60, BSSCount: 1, BusyPct: busyPtr(95)}),
		"good1":   makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -90, BSSCount: 1, BusyPct: busyPtr(5)}),
		"good2":   makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -90, BSSCount: 1, BusyPct: busyPtr(5)}),
		"good3":   makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -90, BSSCount: 1, BusyPct: busyPtr(5)}),
	}
	stats, ok := aggregateChannelReports(reports, 5220)
	if !ok {
		t.Fatal("expected data for channel 5220")
	}
	if stats.badVotes != 1 {
		t.Fatalf("expected exactly 1 bad vote from the one reporter bad on both noise AND occupancy, got %d", stats.badVotes)
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{5220}, "5GHz")
	if len(scored) != 1 {
		t.Fatalf("expected channel 5220 to survive (1 bad vote of 4 reporters, quorum 2), got scored=%v", scored)
	}
}

// permutations returns every ordering of vals (small slices only — O(n!)).
func permutations(vals []float64) [][]float64 {
	var result [][]float64
	arr := make([]float64, len(vals))
	copy(arr, vals)
	var helper func(int)
	helper = func(k int) {
		if k == len(arr) {
			cp := make([]float64, len(arr))
			copy(cp, arr)
			result = append(result, cp)
			return
		}
		for i := k; i < len(arr); i++ {
			arr[k], arr[i] = arr[i], arr[k]
			helper(k + 1)
			arr[k], arr[i] = arr[i], arr[k]
		}
	}
	helper(0)
	return result
}

// TestMedianAllPermutationsStable: median must be identical across every
// possible ordering of the same underlying values — this election runs
// independently on every mesh node from the same gossiped data with no
// coordinator, and Go's map iteration order is randomized per process, so
// median must never depend on the order its input slice was built in.
func TestMedianAllPermutationsStable(t *testing.T) {
	vals := []float64{-95, -90, -85, -75}
	want := median(vals)
	for _, perm := range permutations(vals) {
		if got := median(perm); got != want {
			t.Fatalf("median(%v) = %.4f, want %.4f (order-dependent!)", perm, got, want)
		}
	}
}

func TestMedianHelper(t *testing.T) {
	cases := []struct {
		name string
		vals []float64
		want float64
	}{
		{"empty", nil, 0},
		{"single", []float64{-80}, -80},
		{"odd", []float64{-90, -70, -80}, -80},
		{"even", []float64{-90, -70, -80, -60}, -75},
	}
	for _, c := range cases {
		got := median(c.vals)
		if got != c.want {
			t.Errorf("median(%v) = %.4f, want %.4f", c.vals, got, c.want)
		}
	}
}

// TestMedianStableAcrossReportOrderings: two orderings of the same
// underlying per-channel data (simulating randomized map iteration order)
// must produce the identical median, and therefore the identical score and
// winner. Complements TestMedianAllPermutationsStable (which exercises the
// median() helper directly) by exercising the same property through the
// full aggregateChannelReports/scoreCandidates pipeline.
func TestMedianStableAcrossReportOrderings(t *testing.T) {
	orderA := map[string]ChannelReport{
		"n1": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 2}),
		"n2": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -85, BSSCount: 2}),
		"n3": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -75, BSSCount: 2}),
		"n4": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -95, BSSCount: 2}),
	}
	orderB := map[string]ChannelReport{
		"z4": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -95, BSSCount: 2}),
		"z1": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -75, BSSCount: 2}),
		"z3": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -90, BSSCount: 2}),
		"z2": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -85, BSSCount: 2}),
	}

	statsA, okA := aggregateChannelReports(orderA, 2437)
	statsB, okB := aggregateChannelReports(orderB, 2437)
	if !okA || !okB {
		t.Fatal("expected both orderings to have data")
	}
	if statsA.medianNoise != statsB.medianNoise {
		t.Fatalf("median differs across map orderings: %.4f vs %.4f", statsA.medianNoise, statsB.medianNoise)
	}

	scoredA, _ := scoreCandidates(orderA, map[int]int{}, []int{2437}, "2.4GHz")
	scoredB, _ := scoreCandidates(orderB, map[int]int{}, []int{2437}, "2.4GHz")
	if len(scoredA) != 1 || len(scoredB) != 1 {
		t.Fatalf("expected channel 2437 scored in both orderings: %v %v", scoredA, scoredB)
	}
	if scoredA[0].rawScore != scoredB[0].rawScore {
		t.Fatalf("rawScore differs across map orderings: %.4f vs %.4f", scoredA[0].rawScore, scoredB[0].rawScore)
	}
}

// TestNoBusyPctScoresIdenticallyToPreOccupancy: no report carries a
// BusyPct for this channel at all (nil on every ChannelScanResult) — this
// is exactly the shape of every report produced by pre-occupancy
// node-manager code, and also the incumbent's own measurement of its own
// channel. rawScore must come out identical to the pure noise+BSS formula,
// with no occupancy term contributed.
func TestNoBusyPctScoresIdenticallyToPreOccupancy(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -80, BSSCount: 3}),
		"b":    makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -85, BSSCount: 2}),
	}
	stats, ok := aggregateChannelReports(reports, 2437)
	if !ok {
		t.Fatal("expected data")
	}
	if stats.haveBusy {
		t.Fatalf("expected haveBusy=false when no report carries BusyPct, got stats=%+v", stats)
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{2437}, "2.4GHz")
	if len(scored) != 1 {
		t.Fatalf("expected 1 scored candidate, got %v", scored)
	}
	want := stats.medianNoise + stats.meanBSS*0.1
	if scored[0].rawScore != want {
		t.Fatalf("rawScore = %.4f, want %.4f (pure noise+BSS, no occupancy term)", scored[0].rawScore, want)
	}
}

// TestMixedBusyPctUsesOnlyReportsThatHaveIt: some reporters carry BusyPct,
// others (e.g. the incumbent's own report, or an old-code peer) don't —
// medianBusy must be computed only from the ones that do.
func TestMixedBusyPctUsesOnlyReportsThatHaveIt(t *testing.T) {
	reports := map[string]ChannelReport{
		"incumbent": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: nil}),
		"peerA":     makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -82, BSSCount: 1, BusyPct: busyPtr(10)}),
		"peerB":     makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -81, BSSCount: 1, BusyPct: busyPtr(20)}),
		"oldCode":   makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -79, BSSCount: 1}), // BusyPct nil, no field at all
	}
	stats, ok := aggregateChannelReports(reports, 5220)
	if !ok {
		t.Fatal("expected data")
	}
	if !stats.haveBusy {
		t.Fatal("expected haveBusy=true, at least 2 reports carry BusyPct")
	}
	// median of [10, 20] == 15
	if stats.medianBusy != 15 {
		t.Fatalf("medianBusy = %.4f, want 15 (median of only the 2 reports that had a reading)", stats.medianBusy)
	}
}

// TestOccupancyQuorumDisqualification: mirrors the existing noise quorum
// test, but for occupancy — 1 out of 4 reporters over occupancyDisqualifyPct
// must NOT disqualify (below quorum), while enough reporters over the
// threshold must.
func TestOccupancyQuorumDisqualification(t *testing.T) {
	// 1/4 over threshold: requiredDisqualifyVotes(4) == 2, must survive.
	survives := map[string]ChannelReport{
		"a": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(95)}),
		"b": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
		"c": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
		"d": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
	}
	scored, _ := scoreCandidates(survives, map[int]int{}, []int{5220}, "5GHz")
	if len(scored) != 1 {
		t.Fatalf("expected channel to survive with only 1/4 reporters over occupancyDisqualifyPct, got %v", scored)
	}

	// 2/4 over threshold: requiredDisqualifyVotes(4) == 2, must disqualify.
	disqualified := map[string]ChannelReport{
		"a": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(95)}),
		"b": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(90)}),
		"c": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
		"d": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -80, BSSCount: 1, BusyPct: busyPtr(10)}),
	}
	scored, _ = scoreCandidates(disqualified, map[int]int{}, []int{5220}, "5GHz")
	if len(scored) != 0 {
		t.Fatalf("expected channel disqualified with 2/4 reporters over occupancyDisqualifyPct, got %v", scored)
	}
}

// TestBaseScoreExcludesOccupancy (C2): scoredCandidate.baseScore must be
// the pre-occupancy formula (medianNoise + meanBSS*0.1) regardless of
// whether the channel has an occupancy reading — rawScore is the one that
// includes it. This is what lets electBand/electColdStart compare the
// limp-mode threshold against a score range that's actually been
// validated, instead of the wider, uncalibrated occupancy-inclusive range.
func TestBaseScoreExcludesOccupancy(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 5220, NoiseFloor: -85, BSSCount: 2, BusyPct: busyPtr(40)}),
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{5220}, "5GHz")
	if len(scored) != 1 {
		t.Fatalf("expected 1 scored candidate, got %v", scored)
	}
	wantBase := -85 + 2*0.1
	if scored[0].baseScore != wantBase {
		t.Fatalf("baseScore = %.4f, want %.4f (must exclude occupancy)", scored[0].baseScore, wantBase)
	}
	if scored[0].rawScore == scored[0].baseScore {
		t.Fatalf("rawScore (%.4f) should differ from baseScore (%.4f) when occupancy is present", scored[0].rawScore, scored[0].baseScore)
	}
	wantRaw := wantBase + 40
	if scored[0].rawScore != wantRaw {
		t.Fatalf("rawScore = %.4f, want %.4f (baseScore + occupancy)", scored[0].rawScore, wantRaw)
	}
}

// TestLimpModeUsesBaseScoreNotRawScore (C2): mirrors the reviewer-confirmed
// live scenario — ordinary-for-2.4GHz noise (-95dBm) plus moderate off-
// channel occupancy (42%, comfortably in the "common, not an edge case"
// range the review flagged) produces a baseScore well clear of
// limpModeScoreThreshold (-60.0) but a rawScore that crosses it
// (-95 + 42 = -53). electBand's cold-start path must elect the channel
// normally (no limp fallback) because the threshold compares against
// baseScore, not rawScore.
func TestLimpModeUsesBaseScoreNotRawScore(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -95, BSSCount: 0, BusyPct: busyPtr(42)}),
		"peer": makeReport(ChannelScanResult{Channel: 2437, NoiseFloor: -95, BSSCount: 0, BusyPct: busyPtr(45)}),
	}
	registry := map[string]map[string]string{} // no peer votes -> totalVotes == 0, cold-start path
	result := electBand(reports, registry, []int{2437, 2462}, lobbyFreq24, "", lobbyFreq24, "2.4GHz")
	if result.limp {
		t.Fatalf("expected no limp fallback (baseScore ~-95 is well under threshold), got limp=true, result=%+v", result)
	}
	if result.freq != "2437" {
		t.Fatalf("expected channel 2437 elected, got freq=%s result=%+v", result.freq, result)
	}
}

// TestScoreOrderingLowOccupancyBeatsHighOccupancy: same noise, different
// occupancy — the low-occupancy channel must win (lower score).
func TestScoreOrderingLowOccupancyBeatsHighOccupancy(t *testing.T) {
	reports := map[string]ChannelReport{
		"self": makeReport(
			ChannelScanResult{Channel: 5200, NoiseFloor: -85, BSSCount: 1, BusyPct: busyPtr(5)},
			ChannelScanResult{Channel: 5220, NoiseFloor: -85, BSSCount: 1, BusyPct: busyPtr(60)},
		),
	}
	scored, _ := scoreCandidates(reports, map[int]int{}, []int{5200, 5220}, "5GHz")
	scoreByCh := map[int]float64{}
	for _, s := range scored {
		scoreByCh[s.ch] = s.rawScore
	}
	if len(scoreByCh) != 2 {
		t.Fatalf("expected both channels scored, got %v", scoreByCh)
	}
	if !(scoreByCh[5200] < scoreByCh[5220]) {
		t.Fatalf("expected low-occupancy channel 5200 (score %.2f) to beat high-occupancy channel 5220 (score %.2f)",
			scoreByCh[5200], scoreByCh[5220])
	}
}
