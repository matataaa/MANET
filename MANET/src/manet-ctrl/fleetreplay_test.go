package main

import (
	"bytes"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withTempAppliedRecord points appliedConfigFilePath at a throwaway file for
// the duration of the test, restoring it afterward. Never touches the real
// /var/lib path.
func withTempAppliedRecord(t *testing.T) {
	t.Helper()
	orig := appliedConfigFilePath
	appliedConfigFilePath = filepath.Join(t.TempDir(), "applied.json")
	t.Cleanup(func() { appliedConfigFilePath = orig })
}

func TestIsPkgIDAppliedInitiallyFalse(t *testing.T) {
	withTempAppliedRecord(t)
	if isPkgIDApplied("nonexistent") {
		t.Fatalf("isPkgIDApplied should be false before anything has been recorded")
	}
	if isPkgIDApplied("") {
		t.Fatalf("isPkgIDApplied(\"\") must always be false")
	}
}

func TestRecordPkgIDAppliedThenIsPkgIDApplied(t *testing.T) {
	withTempAppliedRecord(t)

	if err := recordPkgIDApplied("pkg-1", "v1", 1000); err != nil {
		t.Fatalf("recordPkgIDApplied failed: %v", err)
	}
	// This is the record-before-apply invariant: once recordPkgIDApplied
	// returns successfully, isPkgIDApplied must immediately and durably
	// report true — a caller (fleetCheckActivation) that crashes right
	// after this call, before actually applying anything, must still see
	// the package as "already applied" on the next boot, so it never
	// retries the apply. We can't literally crash the process mid-test, but
	// we CAN assert the on-disk record is authoritative and synchronous —
	// there is no async/deferred write here.
	if !isPkgIDApplied("pkg-1") {
		t.Fatalf("isPkgIDApplied must be true immediately after recordPkgIDApplied returns")
	}
	if isPkgIDApplied("pkg-2") {
		t.Fatalf("isPkgIDApplied must not be true for an unrelated pkg_id")
	}
}

func TestRecordPkgIDAppliedRejectsEmptyID(t *testing.T) {
	withTempAppliedRecord(t)
	if err := recordPkgIDApplied("", "v1", 1000); err == nil {
		t.Fatalf("recordPkgIDApplied must refuse an empty pkg_id")
	}
}

func TestAppliedRecordRingEviction(t *testing.T) {
	withTempAppliedRecord(t)

	total := appliedRecordMax + 5
	for i := 0; i < total; i++ {
		pkgID := fmt.Sprintf("pkg-%03d", i)
		if err := recordPkgIDApplied(pkgID, "v", int64(i+1)); err != nil {
			t.Fatalf("recordPkgIDApplied(%s) failed: %v", pkgID, err)
		}
	}

	rec := loadAppliedRecord()
	if len(rec.Entries) != appliedRecordMax {
		t.Fatalf("expected the ring to be capped at %d entries, got %d", appliedRecordMax, len(rec.Entries))
	}

	// The oldest (evicted) entries must no longer be found...
	for i := 0; i < total-appliedRecordMax; i++ {
		pkgID := fmt.Sprintf("pkg-%03d", i)
		if isPkgIDApplied(pkgID) {
			t.Fatalf("pkg_id %s should have been evicted from the ring but isPkgIDApplied still reports true", pkgID)
		}
	}
	// ...while the most recent appliedRecordMax entries must still be
	// present.
	for i := total - appliedRecordMax; i < total; i++ {
		pkgID := fmt.Sprintf("pkg-%03d", i)
		if !isPkgIDApplied(pkgID) {
			t.Fatalf("pkg_id %s should still be in the ring but isPkgIDApplied reports false", pkgID)
		}
	}
}

func TestHighestAppliedStagedAtNeverDecreases(t *testing.T) {
	withTempAppliedRecord(t)

	if got := highestAppliedStagedAt(); got != 0 {
		t.Fatalf("expected watermark 0 before anything applied, got %d", got)
	}

	if err := recordPkgIDApplied("pkg-a", "va", 5000); err != nil {
		t.Fatalf("recordPkgIDApplied failed: %v", err)
	}
	if got := highestAppliedStagedAt(); got != 5000 {
		t.Fatalf("expected watermark 5000, got %d", got)
	}

	// Applying an OLDER staged_at (e.g. an operator force-applying an older
	// config on purpose via a freshly-staged package that happens to carry
	// an old timestamp is not how staging works in practice — staged_at is
	// always fresh at stage time — but this asserts the watermark's own
	// invariant regardless: it must never decrease.
	if err := recordPkgIDApplied("pkg-b", "vb", 3000); err != nil {
		t.Fatalf("recordPkgIDApplied failed: %v", err)
	}
	if got := highestAppliedStagedAt(); got != 5000 {
		t.Fatalf("watermark must never decrease: expected 5000, got %d", got)
	}

	if err := recordPkgIDApplied("pkg-c", "vc", 9000); err != nil {
		t.Fatalf("recordPkgIDApplied failed: %v", err)
	}
	if got := highestAppliedStagedAt(); got != 9000 {
		t.Fatalf("expected watermark to advance to 9000, got %d", got)
	}
}

func TestActivateAtInBounds(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		delta  time.Duration
		wantOK bool
	}{
		{"now", 0, true},
		{"4min_past", -4 * time.Minute, true},
		{"6min_past", -6 * time.Minute, false},
		{"59min_future", 59 * time.Minute, true},
		{"61min_future", 61 * time.Minute, false},
		{"1hr_past_way_out", -24 * time.Hour, false},
		{"1day_future_way_out", 24 * time.Hour, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := now.Add(c.delta).Unix()
			if got := activateAtInBounds(at, now); got != c.wantOK {
				t.Fatalf("activateAtInBounds(%v, %v) = %v, want %v", c.delta, now, got, c.wantOK)
			}
		})
	}
}

func TestFleetPackageFresh(t *testing.T) {
	normalNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	unsetClockNow := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("no_expires_at_is_fresh", func(t *testing.T) {
		pkg := map[string]interface{}{}
		if !fleetPackageFresh(pkg, normalNow) {
			t.Fatalf("a package with no expires_at must be treated as fresh")
		}
	})
	t.Run("future_expiry_is_fresh", func(t *testing.T) {
		pkg := map[string]interface{}{"expires_at": float64(normalNow.Add(time.Hour).Unix())}
		if !fleetPackageFresh(pkg, normalNow) {
			t.Fatalf("a package expiring in the future must be fresh")
		}
	})
	t.Run("past_expiry_is_not_fresh", func(t *testing.T) {
		pkg := map[string]interface{}{"expires_at": float64(normalNow.Add(-time.Hour).Unix())}
		if fleetPackageFresh(pkg, normalNow) {
			t.Fatalf("a package that expired an hour ago must not be fresh")
		}
	})
	t.Run("exactly_at_expiry_is_fresh", func(t *testing.T) {
		pkg := map[string]interface{}{"expires_at": float64(normalNow.Unix())}
		if !fleetPackageFresh(pkg, normalNow) {
			t.Fatalf("a package expiring exactly now should still be accepted (<=, not <)")
		}
	})
	t.Run("unset_clock_bypasses_expiry_check", func(t *testing.T) {
		// Even a wildly-expired-looking expires_at must be treated as fresh
		// when the local clock itself looks unset (year < 2025) — these
		// boards have no RTC. isPkgIDApplied/highestAppliedStagedAt are the
		// real backstop in that case, not this function.
		pkg := map[string]interface{}{"expires_at": float64(1)}
		if !fleetPackageFresh(pkg, unsetClockNow) {
			t.Fatalf("an unset-looking clock must bypass the expires_at check")
		}
	})
}

// TestLogRejectV1OncePerSlotIndependentBudget is the regression test for the
// shared-rate-limit-timer bug: fleetConfigWatcher always polls slot 70
// before slot 71 in the same tick, so a single shared 60s timer let slot 70
// rejections silently consume the whole budget and permanently suppress
// slot 71's rejection line whenever both slots had something to reject in
// the same window -- a real forged slot-71 update-trigger was correctly
// rejected, but the log line proving it never appeared. Uses two slot
// labels unique to this test run (not "70"/"71") so it can't be affected by
// rate-limit state left over from any other test in this package sharing
// the same process.
func TestLogRejectV1OncePerSlotIndependentBudget(t *testing.T) {
	var buf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	})

	unique := strconv.FormatInt(time.Now().UnixNano(), 36)
	slotA := "test-slot-A-" + unique
	slotB := "test-slot-B-" + unique

	logRejectV1Once(slotA, "aa:aa:aa:aa:aa:aa")
	// A DIFFERENT slot, in the same instant, must NOT be suppressed by
	// slotA's just-consumed budget -- this is the actual fix.
	logRejectV1Once(slotB, "bb:bb:bb:bb:bb:bb")
	// The SAME slot again, immediately, must still be suppressed --
	// per-slot rate limiting must not have accidentally removed rate
	// limiting altogether.
	logRejectV1Once(slotA, "aa:aa:aa:aa:aa:aa")

	out := buf.String()
	countA := strings.Count(out, slotA)
	countB := strings.Count(out, slotB)
	if countA != 1 {
		t.Fatalf("expected exactly 1 log line for slotA (rate-limited on repeat), got %d; output=%q", countA, out)
	}
	if countB != 1 {
		t.Fatalf("expected exactly 1 log line for slotB (independent budget from slotA), got %d; output=%q", countB, out)
	}
}
