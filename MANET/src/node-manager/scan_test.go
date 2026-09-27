package main

import (
	"errors"
	"testing"
	"time"
)

// resetOcc clears the package-level occupancy sampling state between test
// cases — busyPctForFreq's whole point is stateful sampling across calls,
// so tests that simulate "successive ticks" must control exactly what
// state exists beforehand.
func resetOcc() {
	occ = make(map[string]map[int]*occState)
}

func TestParseSurveyBlocksFullBlock(t *testing.T) {
	out := `Survey data from wlan1
	frequency:			5200 MHz
	noise:				-89 dBm
	channel active time:		20502 ms
	channel busy time:		1235 ms
	channel receive time:		590 ms
	channel transmit time:		46 ms
Survey data from wlan1
	frequency:			5220 MHz [in use]
	noise:				-91 dBm
	channel active time:		184300 ms
	channel busy time:		183900 ms
	channel receive time:		183000 ms
	channel transmit time:		850 ms
`
	blocks := parseSurveyBlocks(out)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(blocks), blocks)
	}
	b, ok := blocks[5200]
	if !ok {
		t.Fatal("expected block for 5200")
	}
	if !b.noiseOK || b.noise != -89 {
		t.Fatalf("expected noise -89, got %+v", b)
	}
	if !b.timesOK || b.activeMs != 20502 || b.busyMs != 1235 || b.txMs != 46 {
		t.Fatalf("expected full time fields parsed, got %+v", b)
	}

	b2, ok := blocks[5220]
	if !ok {
		t.Fatal("expected block for 5220")
	}
	if !b2.timesOK || b2.activeMs != 184300 || b2.busyMs != 183900 || b2.txMs != 850 {
		t.Fatalf("expected full time fields parsed for 5220, got %+v", b2)
	}
}

func TestParseSurveyBlocksKeepsFirstOnDuplicateFrequency(t *testing.T) {
	out := `Survey data from wlan1
	frequency:			5200 MHz
	noise:				-70 dBm
	channel active time:		100 ms
	channel busy time:		10 ms
	channel transmit time:		1 ms
Survey data from wlan1
	frequency:			5200 MHz
	noise:				-99 dBm
	channel active time:		999 ms
	channel busy time:		999 ms
	channel transmit time:		999 ms
`
	blocks := parseSurveyBlocks(out)
	b := blocks[5200]
	if b.noise != -70 || b.activeMs != 100 {
		t.Fatalf("expected first occurrence kept, got %+v", b)
	}
}

func TestParseSurveyBlocksPartialTimesNotOK(t *testing.T) {
	out := `Survey data from wlan1
	frequency:			5200 MHz
	noise:				-70 dBm
	channel active time:		100 ms
`
	blocks := parseSurveyBlocks(out)
	b := blocks[5200]
	if b.timesOK {
		t.Fatalf("expected timesOK=false when busy/transmit time missing, got %+v", b)
	}
}

// TestBusyPctForFreqSingleVisitOverFloor: on real hardware, one visit's
// absolute activeMs (~70-85ms, confirmed live) already clears
// minSurveyActiveDeltaMs (50) on its own, so a reading should appear after
// just the first sample.
func TestBusyPctForFreqSingleVisitOverFloor(t *testing.T) {
	resetOcc()
	blk := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 40, txMs: 0}
	got := busyPctForFreq("wlan1", 5220, blk, false, true)
	if got == nil {
		t.Fatal("expected an immediate reading from a single over-floor visit")
	}
	if *got != 50 {
		t.Fatalf("busyPct = %.4f, want 50", *got)
	}
}

// TestBusyPctForFreqAccumulatesAcrossVisits: verifies the ring-accumulation
// logic generally, independent of this specific hardware's per-visit
// magnitude — several visits individually below minSurveyActiveDeltaMs
// must still sum up and produce a reading once their SUMMED activeMs
// crosses the floor, not before.
func TestBusyPctForFreqAccumulatesAcrossVisits(t *testing.T) {
	resetOcc()
	visit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 20, busyMs: 10, txMs: 0}

	// Visit 1: sum=20, below floor (50) -> nil.
	if got := busyPctForFreq("wlan1", 5220, visit, false, true); got != nil {
		t.Fatalf("expected nil after visit 1 (sum=20 < floor), got %v", *got)
	}
	// Visit 2: sum=40, still below floor -> nil.
	if got := busyPctForFreq("wlan1", 5220, visit, false, true); got != nil {
		t.Fatalf("expected nil after visit 2 (sum=40 < floor), got %v", *got)
	}
	// Visit 3: sum=60, crosses floor -> a reading appears.
	got := busyPctForFreq("wlan1", 5220, visit, false, true)
	if got == nil {
		t.Fatal("expected a reading once the ring's summed activeMs crosses the floor")
	}
	// Σbusy=30, Σtx=0, Σactive=60 => 50%
	if *got != 50 {
		t.Fatalf("busyPct = %.4f, want 50", *got)
	}
}

// TestBusyPctForFreqFailedScanContributesNoSample: scanOK=false means the
// `iw scan` call itself failed/was rejected, so the survey dump's counters
// for this frequency are stale leftovers from a previous visit, not a new
// one — must not be appended to the ring at all.
func TestBusyPctForFreqFailedScanContributesNoSample(t *testing.T) {
	resetOcc()
	staleLooking := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 40, txMs: 0}

	// scanOK=false: must contribute nothing, so no reading yet.
	if got := busyPctForFreq("wlan1", 5220, staleLooking, false, false); got != nil {
		t.Fatalf("expected nil when scanOK=false (no sample should be appended), got %v", *got)
	}

	// A subsequent genuinely successful scan must behave as if the failed
	// one never happened — i.e. this is still effectively the first real
	// sample, immediately over the floor.
	got := busyPctForFreq("wlan1", 5220, staleLooking, false, true)
	if got == nil {
		t.Fatal("expected a reading from the first real (scanOK=true) sample")
	}
	if *got != 50 {
		t.Fatalf("busyPct = %.4f, want 50", *got)
	}
}

// TestBusyPctForFreqHeldValueReturnedBetweenComputations: once a reading
// has been computed, it must still be returned on a subsequent tick that
// doesn't itself produce a fresh computation (e.g. a failed scan) — the
// "hold" behavior — rather than immediately reverting to nil.
func TestBusyPctForFreqHeldValueReturnedBetweenComputations(t *testing.T) {
	resetOcc()
	visit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 40, txMs: 0}
	got := busyPctForFreq("wlan1", 5220, visit, false, true)
	if got == nil || *got != 50 {
		t.Fatalf("expected initial reading of 50, got %v", got)
	}

	// Next tick: scan failed, no new sample — must still return the held
	// value, not nil.
	held := busyPctForFreq("wlan1", 5220, surveyBlock{}, false, false)
	if held == nil {
		t.Fatal("expected the held value to be returned when no fresh sample is available")
	}
	if *held != 50 {
		t.Fatalf("held busyPct = %.4f, want 50", *held)
	}
}

// TestBusyPctForFreqHeldValueExpiresAfterHoldMax: a held value older than
// occHoldMax must no longer be returned — directly backdates the internal
// occState (same package) rather than waiting occHoldMax in real time.
func TestBusyPctForFreqHeldValueExpiresAfterHoldMax(t *testing.T) {
	resetOcc()
	visit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 40, txMs: 0}
	if got := busyPctForFreq("wlan1", 5220, visit, false, true); got == nil {
		t.Fatal("expected an initial reading")
	}

	// Backdate the held value past occHoldMax.
	occ["wlan1"][5220].lastPctAt = time.Now().Add(-occHoldMax - time.Minute)

	// No fresh sample this tick (failed scan) — the now-expired held value
	// must not be returned.
	got := busyPctForFreq("wlan1", 5220, surveyBlock{}, false, false)
	if got != nil {
		t.Fatalf("expected nil once the held value has expired past occHoldMax, got %v", *got)
	}
}

// TestBusyPctForFreqRejectsImplausibleActiveMs: a single visit with
// activeMs above occMaxVisitMs is rejected outright — not appended to the
// ring, so it can't contribute to (or by itself satisfy) the floor.
func TestBusyPctForFreqRejectsImplausibleActiveMs(t *testing.T) {
	resetOcc()
	implausible := surveyBlock{noiseOK: true, timesOK: true, activeMs: occMaxVisitMs + 100, busyMs: 40, txMs: 0}
	got := busyPctForFreq("wlan1", 5220, implausible, false, true)
	if got != nil {
		t.Fatalf("expected nil — implausible activeMs (%d > occMaxVisitMs) must not be appended, got %v", implausible.activeMs, *got)
	}

	// A genuinely plausible visit afterward must still work normally (the
	// rejected sample didn't corrupt any state).
	plausible := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 40, txMs: 0}
	got = busyPctForFreq("wlan1", 5220, plausible, false, true)
	if got == nil || *got != 50 {
		t.Fatalf("expected a normal reading of 50 after the implausible sample was rejected, got %v", got)
	}
}

// TestBusyPctForFreqIncumbentExcluded: a candidate currently flagged
// incumbent must never return a reading, regardless of how "valid" its
// counters would otherwise look.
func TestBusyPctForFreqIncumbentExcluded(t *testing.T) {
	resetOcc()
	blk := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 70, txMs: 60}
	if got := busyPctForFreq("wlan1", 5220, blk, true, true); got != nil {
		t.Fatalf("expected nil for incumbent, got %v", *got)
	}
	// Repeated incumbent calls must also never produce a reading.
	if got := busyPctForFreq("wlan1", 5220, blk, true, true); got != nil {
		t.Fatalf("expected nil for incumbent on repeated calls, got %v", *got)
	}
}

// TestBusyPctForFreqIncumbentClearsHeldValueOnDeparture (W1, reviewer
// follow-up): isIncumbent must clear the ENTIRE occState — including
// lastPct/lastPctAt, not just the sample ring — when a frequency becomes
// the incumbent. Otherwise a value held from before the node moved onto
// that channel could incorrectly reappear as a "fresh-looking" reading
// once the node leaves it again, without ever having taken a real
// off-channel sample post-departure.
func TestBusyPctForFreqIncumbentClearsHeldValueOnDeparture(t *testing.T) {
	resetOcc()
	// Establish a real held value while NOT incumbent.
	visit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 40, txMs: 0}
	got := busyPctForFreq("wlan1", 5220, visit, false, true)
	if got == nil || *got != 50 {
		t.Fatalf("expected an initial held value of 50, got %v", got)
	}

	// Node moves onto 5220 — now incumbent. Must return nil (already
	// covered by TestBusyPctForFreqIncumbentExcluded) AND must have wiped
	// the held value, not just hidden it.
	if got := busyPctForFreq("wlan1", 5220, visit, true, true); got != nil {
		t.Fatalf("expected nil while incumbent, got %v", *got)
	}

	// Node leaves 5220 again, but this tick's scan fails (no fresh
	// sample) — if the old held value (50) had survived the incumbent
	// period, it would incorrectly reappear here. It must not: expect nil.
	got = busyPctForFreq("wlan1", 5220, surveyBlock{}, false, false)
	if got != nil {
		t.Fatalf("expected nil — the pre-incumbency held value (%v) must have been cleared, not just hidden while incumbent", *got)
	}
}

// TestScanSucceededDetectsKernelAbortedScan (W1, reviewer follow-up): a
// nonzero exit code is NOT the only failure signal — `iw` exits 0 even
// when the KERNEL aborts the scan partway through (confirmed against
// upstream iw's scan.c: handle_scan_combined() prints "scan aborted!" and
// returns 0 on NL80211_CMD_SCAN_ABORTED). When that happens, every
// candidate frequency after the abort point still holds its previous
// visit's stale counters despite the clean exit status — scanSucceeded
// must catch this via the captured output, not just the error.
func TestScanSucceededDetectsKernelAbortedScan(t *testing.T) {
	cases := []struct {
		name string
		out  []byte
		err  error
		want bool
	}{
		{"normal completed scan", []byte("BSS 11:22:33:44:55:66(on wlan1)\n\tTSF: ...\n"), nil, true},
		{"kernel-aborted scan, clean exit", []byte("scan aborted!\n"), nil, false},
		{"kernel-aborted scan, substring mid-output", []byte("some output\nscan aborted!\nmore output\n"), nil, false},
		{"command error", []byte(""), errors.New("exit status 1"), false},
		{"command error even with clean-looking output", []byte("ok\n"), errors.New("timeout"), false},
	}
	for _, c := range cases {
		if got := scanSucceeded(c.out, c.err); got != c.want {
			t.Errorf("%s: scanSucceeded(%q, %v) = %v, want %v", c.name, c.out, c.err, got, c.want)
		}
	}
}

// TestBusyPctForFreqStaleSamplesEvictedBeforeSumming (W2, reviewer
// follow-up): without a per-sample timestamp, occHoldMax only bounded how
// long the AGGREGATE lastPct was held, not how old the individual samples
// feeding a NEW computation were allowed to be. Reproduces the reviewer's
// exact scratch scenario: 5 stale 100%-busy samples backdated 10 hours
// (occHoldMax is 60 minutes) plus one fresh 0%-busy visit. Before the fix
// this reported ~80% busy (dragged by the stale samples); the fix must
// evict anything older than occHoldMax before summing, so the result
// reflects only the fresh visit.
func TestBusyPctForFreqStaleSamplesEvictedBeforeSumming(t *testing.T) {
	resetOcc()
	staleAt := time.Now().Add(-10 * time.Hour)
	stale := surveyEntry{activeMs: 80, busyMs: 80, txMs: 0, at: staleAt} // 100% busy, but ancient
	occ["wlan1"] = map[int]*occState{
		5220: {samples: []surveyEntry{stale, stale, stale, stale, stale}},
	}

	freshVisit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 0, txMs: 0} // 0% busy
	got := busyPctForFreq("wlan1", 5220, freshVisit, false, true)
	if got == nil {
		t.Fatal("expected a reading from the fresh visit")
	}
	if *got != 0 {
		t.Fatalf("busyPct = %.4f, want 0 — the 5 ten-hour-old 100%%-busy samples must be evicted before summing, not averaged in with the fresh 0%%-busy visit", *got)
	}
}

// TestBusyPctForFreqRingCapDropsOldestByCount (S1): appending well past
// occMaxSamples must keep the ring's length capped and must actually drop
// the oldest entries (not just stop accepting new ones) — verified by
// checking the COMPUTED value reflects the ring's true composition after
// eviction, not just that len(samples) == occMaxSamples.
func TestBusyPctForFreqRingCapDropsOldestByCount(t *testing.T) {
	resetOcc()
	busyVisit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 80, txMs: 0} // 100% busy

	// Fill well past the cap with 100%-busy visits.
	for i := 0; i < occMaxSamples+3; i++ {
		busyPctForFreq("wlan1", 5220, busyVisit, false, true)
	}
	st := occ["wlan1"][5220]
	if len(st.samples) != occMaxSamples {
		t.Fatalf("expected ring length capped at %d, got %d", occMaxSamples, len(st.samples))
	}

	// One more visit, 0% busy — must evict exactly one oldest 100%-busy
	// sample to make room, not reset or ignore the ring.
	idleVisit := surveyBlock{noiseOK: true, timesOK: true, activeMs: 80, busyMs: 0, txMs: 0}
	got := busyPctForFreq("wlan1", 5220, idleVisit, false, true)
	if len(st.samples) != occMaxSamples {
		t.Fatalf("expected ring length to stay capped at %d after another append, got %d", occMaxSamples, len(st.samples))
	}
	// Ring composition should now be (occMaxSamples-1) 100%-busy samples +
	// 1 0%-busy sample: Σbusy=(occMaxSamples-1)*80, Σactive=occMaxSamples*80.
	want := float64(occMaxSamples-1) / float64(occMaxSamples) * 100
	if got == nil || *got != want {
		t.Fatalf("busyPct = %v, want %.4f — ring must reflect dropping exactly the oldest sample, not all of them or none", got, want)
	}
}
