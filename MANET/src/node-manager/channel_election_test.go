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
