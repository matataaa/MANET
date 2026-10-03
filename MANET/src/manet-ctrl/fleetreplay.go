package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// appliedRecordMax bounds the persistent applied-package ring — this only
// needs to be big enough to survive a reboot racing a still-gossiping Alfred
// entry, not a full history. The ring is a convenience/fast-path for exact
// pkg_id matches; HighestStagedAt below (which never shrinks and is never
// evicted) is what actually stops an older package — including one that has
// aged out of the ring — from ever being re-applied.
const appliedRecordMax = 10

type appliedRecordEntry struct {
	PkgID     string `json:"pkg_id"`
	Version   string `json:"version"`
	StagedAt  int64  `json:"staged_at"`
	AppliedAt int64  `json:"applied_at"`
}

// appliedRecord is the on-disk shape of AppliedConfigFile.
type appliedRecord struct {
	Entries []appliedRecordEntry `json:"entries"`
	// HighestStagedAt is the highest staged_at value of any package this
	// node has ever actually applied. Unlike the Entries ring (bounded to
	// appliedRecordMax, oldest evicted first), this value only ever
	// increases and is never evicted — see fleetProcessPackage's watermark
	// check, which is what actually stops a rollback replay of a package
	// older than the newest one already applied, even after its pkg_id has
	// fallen out of the ring.
	HighestStagedAt int64 `json:"highest_staged_at"`
}

var appliedRecordMu sync.Mutex

// appliedConfigFilePath is a var (not a direct reference to the
// AppliedConfigFile const) purely so tests can point it at a throwaway
// temp file instead of the real /var/lib path — production code never
// reassigns it.
var appliedConfigFilePath = AppliedConfigFile

func loadAppliedRecord() appliedRecord {
	data, err := os.ReadFile(appliedConfigFilePath)
	if err != nil {
		return appliedRecord{}
	}
	var rec appliedRecord
	if json.Unmarshal(data, &rec) != nil {
		return appliedRecord{}
	}
	return rec
}

// isPkgIDApplied reports whether pkgID has already been applied by this
// node, per the persistent (non-tmpfs) record in AppliedConfigFile. This is
// the replay check that survives a reboot — AckVersionFile alone doesn't,
// since it lives on tmpfs and a peer may still be gossiping the exact same
// already-applied package on its next Alfred poll.
func isPkgIDApplied(pkgID string) bool {
	if pkgID == "" {
		return false
	}
	appliedRecordMu.Lock()
	defer appliedRecordMu.Unlock()
	for _, e := range loadAppliedRecord().Entries {
		if e.PkgID == pkgID {
			return true
		}
	}
	return false
}

// highestAppliedStagedAt returns the staged_at of the newest package this
// node has ever actually applied (0 if none yet). See fleetProcessPackage's
// watermark check.
func highestAppliedStagedAt() int64 {
	appliedRecordMu.Lock()
	defer appliedRecordMu.Unlock()
	return loadAppliedRecord().HighestStagedAt
}

// recordPkgIDApplied appends pkgID to the persistent applied-record ring,
// bumps HighestStagedAt if stagedAt is newer, and fsyncs both the file and
// its parent directory. Callers MUST call this BEFORE actually applying the
// corresponding config (see fleetCheckActivation) — record-before-apply means
// a crash mid-apply can never replay the same package again on the next
// boot, at the cost of (harmlessly) treating an apply that started but never
// finished as "done."
func recordPkgIDApplied(pkgID, version string, stagedAt int64) error {
	if pkgID == "" {
		return fmt.Errorf("fleet: refusing to record empty pkg_id as applied")
	}

	appliedRecordMu.Lock()
	defer appliedRecordMu.Unlock()

	rec := loadAppliedRecord()
	rec.Entries = append(rec.Entries, appliedRecordEntry{
		PkgID: pkgID, Version: version, StagedAt: stagedAt, AppliedAt: time.Now().Unix(),
	})
	if len(rec.Entries) > appliedRecordMax {
		rec.Entries = rec.Entries[len(rec.Entries)-appliedRecordMax:]
	}
	if stagedAt > rec.HighestStagedAt {
		rec.HighestStagedAt = stagedAt
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("fleet: marshal applied record: %w", err)
	}

	if err := writeFileFsync(appliedConfigFilePath, data); err != nil {
		return fmt.Errorf("fleet: persist applied record: %w", err)
	}
	return nil
}

// writeFileFsync atomically (write-tmp, fsync, rename) writes data to path,
// then fsyncs the parent directory too — a rename is only durable across a
// crash once the directory entry pointing at it has itself been synced, not
// just the file's own contents.
func writeFileFsync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open tmp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}

	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open parent dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync parent dir: %w", err)
	}
	return nil
}

// activateAtInBounds rejects an incoming activate_at timestamp that is
// implausibly far in the past or future relative to now — a few minutes of
// past clock skew is tolerated, but anything wider suggests a forged or
// stale package rather than a genuine near-term activation. Takes `now`
// explicitly (rather than calling time.Now() internally) so it's
// deterministically testable.
func activateAtInBounds(activateAt int64, now time.Time) bool {
	nowUnix := now.Unix()
	return activateAt >= nowUnix-5*60 && activateAt <= nowUnix+60*60
}

// fleetPackageFresh checks a staged package's expires_at against now. These
// boards have no RTC, so a clock that looks unset (year < 2025) falls
// through to true rather than rejecting everything — the pkg_id replay-
// record check (isPkgIDApplied) and the staged_at watermark
// (highestAppliedStagedAt) are the freshness backstops in that case, not
// this. Takes `now` explicitly so it's deterministically testable.
func fleetPackageFresh(pkg map[string]interface{}, now time.Time) bool {
	expiresAt, ok := pkg["expires_at"].(float64)
	if !ok || expiresAt <= 0 {
		return true
	}
	if now.Year() < 2025 {
		log.Printf("fleet: local clock looks unset (year %d), skipping expires_at freshness check", now.Year())
		return true
	}
	return float64(now.Unix()) <= expiresAt
}

// --- rate-limited logging for envelope-authentication rejections ---

var (
	fleetV1RejectLogMu   sync.Mutex
	fleetV1RejectLogLast = map[string]time.Time{}
)

// logRejectV1Once logs an envelope-authentication-failure line at most once
// every 60s PER SLOT, regardless of how many rejected packages/candidates
// are seen in that window on that slot — a flood of garbage, tampered, or
// not-yet-upgraded-peer packets must not turn into a log flood of its own.
// Keyed per-slot (not one shared timer across all slots): fleetConfigWatcher
// always polls slot 70 before slot 71 in the same tick, so a single shared
// timer let slot 70 rejections silently consume the whole rate-limit budget
// and permanently suppress slot 71's rejection line whenever both slots had
// something to reject in the same window -- verified live: a real forged
// slot-71 update-trigger was correctly rejected (nothing was ever applied),
// but the log line proving it never appeared because slot 70 had also
// rejected something moments earlier. Each slot now gets its own
// independent budget, so an attacker spraying both slots at once can no
// longer hide one behind the other in the logs.
// The wording is deliberately neutral about WHY authentication failed (wrong
// admin_password, corrupted/tampered payload, or a genuinely not-yet-
// upgraded v1 peer all land here) — an operator debugging an actual attack
// should not be steered toward "must just be a rollout timing issue."
func logRejectV1Once(slot, senderMAC string) {
	fleetV1RejectLogMu.Lock()
	defer fleetV1RejectLogMu.Unlock()
	if time.Since(fleetV1RejectLogLast[slot]) < 60*time.Second {
		return
	}
	fleetV1RejectLogLast[slot] = time.Now()
	who := senderMAC
	if who == "" {
		who = "unknown"
	}
	log.Printf("fleet: rejecting unauthenticated/unverifiable config package on slot %s from %s (wrong admin_password, corrupted/tampered payload, or a not-yet-upgraded peer)", slot, who)
}
