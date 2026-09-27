package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempFleetProcessFiles points every file fleetProcessPackage touches at
// throwaway temp paths, restoring the originals afterward. Never touches the
// real /var/run or /var/lib paths.
func withTempFleetProcessFiles(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	origPending := PendingConfFile
	origAck := AckVersionFile
	origPrefs := FleetPrefsFile
	origApplied := appliedConfigFilePath

	PendingConfFile = filepath.Join(dir, "pending.json")
	AckVersionFile = filepath.Join(dir, "ack_version")
	FleetPrefsFile = filepath.Join(dir, "fleet_prefs.json")
	appliedConfigFilePath = filepath.Join(dir, "applied.json")

	t.Cleanup(func() {
		PendingConfFile = origPending
		AckVersionFile = origAck
		FleetPrefsFile = origPrefs
		appliedConfigFilePath = origApplied
	})
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// TestFleetProcessPackageRejectsExpiredNewVersion covers the "genuinely new
// version" branch's fleetPackageFresh gate: a never-before-seen version
// whose expires_at has already passed must be rejected -- no ACK written, no
// pending config staged.
func TestFleetProcessPackageRejectsExpiredNewVersion(t *testing.T) {
	withTempFleetProcessFiles(t)

	pkg := map[string]interface{}{
		"version":    "v-expired",
		"pkg_id":     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"staged_at":  float64(1000),
		"expires_at": float64(1001), // already long past by the time this runs
		"config":     map[string]interface{}{"node_hostname": "x"},
	}

	fleetProcessPackage(mustMarshal(t, pkg))

	if getPendingConfig() != nil {
		t.Fatalf("an expired new-version package must not be staged as pending")
	}
	ackBytes, _ := os.ReadFile(AckVersionFile)
	if string(ackBytes) == "v-expired" {
		t.Fatalf("an expired new-version package must not be ACKed")
	}
}

// TestFleetProcessPackageAlreadyAckedAddsActivateAt covers the branch where
// this node has already ACKed a version and a later gossip round adds
// activate_at -- the local pending copy must be updated with it (and this
// path must NOT be blocked by fleetPackageFresh, since expires_at reflects
// the original STAGING time, not the later activation).
func TestFleetProcessPackageAlreadyAckedAddsActivateAt(t *testing.T) {
	withTempFleetProcessFiles(t)

	const version = "v-acked"
	const pkgID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// This node already ACKed this version and has it pending, without an
	// activate_at yet.
	if err := os.WriteFile(AckVersionFile, []byte(version), 0644); err != nil {
		t.Fatalf("seed AckVersionFile: %v", err)
	}
	localPkg := map[string]interface{}{
		"version":   version,
		"pkg_id":    pkgID,
		"staged_at": float64(1000),
		"config":    map[string]interface{}{"node_hostname": "x"},
	}
	if err := savePendingConfig(localPkg); err != nil {
		t.Fatalf("seed pending config: %v", err)
	}

	// Incoming gossip: same version/pkg_id, now carrying a near-term,
	// in-bounds activate_at, and an expires_at that (deliberately) looks
	// expired relative to "now" -- this must NOT block the activate_at
	// update, since that update is a distinct code path from first-time
	// acceptance.
	incoming := map[string]interface{}{
		"version":     version,
		"pkg_id":      pkgID,
		"staged_at":   float64(1000),
		"expires_at":  float64(1001),
		"activate_at": float64(time.Now().Add(30 * time.Second).Unix()),
		"config":      map[string]interface{}{"node_hostname": "x"},
	}

	fleetProcessPackage(mustMarshal(t, incoming))

	pending := getPendingConfig()
	if pending == nil {
		t.Fatalf("pending config was cleared, expected it to remain with activate_at added")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(pending, &got); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	if _, has := got["activate_at"]; !has {
		t.Fatalf("expected activate_at to have been added to the pending config, got %v", got)
	}
}

// TestFleetProcessPackageRejectsRollbackBelowWatermark is the regression
// test for W2: once this node has actually applied a package with a given
// staged_at, gossip re-publishing an OLDER package (different pkg_id,
// different version -- simulating one whose pkg_id has already aged out of
// the applied-record ring) must be rejected outright, even though it would
// otherwise look like a brand new, never-seen, unexpired version.
func TestFleetProcessPackageRejectsRollbackBelowWatermark(t *testing.T) {
	withTempFleetProcessFiles(t)

	// Simulate having already applied a newer package.
	if err := recordPkgIDApplied("already-applied-newer", "v-newer", 5000); err != nil {
		t.Fatalf("seed applied record: %v", err)
	}

	rollback := map[string]interface{}{
		"version":    "v-older-rollback",
		"pkg_id":     "cccccccccccccccccccccccccccccccc",
		"staged_at":  float64(4000), // older than the 5000 watermark
		"expires_at": float64(time.Now().Unix() + 3600),
		"config":     map[string]interface{}{"node_hostname": "x"},
	}

	fleetProcessPackage(mustMarshal(t, rollback))

	if getPendingConfig() != nil {
		t.Fatalf("a package staged_at at or below the applied watermark must not be staged as pending")
	}
	ackBytes, _ := os.ReadFile(AckVersionFile)
	if string(ackBytes) == "v-older-rollback" {
		t.Fatalf("a rollback package below the watermark must not be ACKed")
	}
}

// TestFleetProcessPackageAcceptsNewerThanWatermark is the positive
// counterpart: a genuinely newer package (staged_at above the watermark)
// must still be accepted normally.
func TestFleetProcessPackageAcceptsNewerThanWatermark(t *testing.T) {
	withTempFleetProcessFiles(t)

	if err := recordPkgIDApplied("already-applied-older", "v-older", 1000); err != nil {
		t.Fatalf("seed applied record: %v", err)
	}

	newer := map[string]interface{}{
		"version":    "v-newer-accepted",
		"pkg_id":     "dddddddddddddddddddddddddddddddd",
		"staged_at":  float64(2000), // above the 1000 watermark
		"expires_at": float64(time.Now().Unix() + 3600),
		"config":     map[string]interface{}{"node_hostname": "x"},
	}

	fleetProcessPackage(mustMarshal(t, newer))

	ackBytes, _ := os.ReadFile(AckVersionFile)
	if string(ackBytes) != "v-newer-accepted" {
		t.Fatalf("expected v-newer-accepted to be ACKed, AckVersionFile contains %q", string(ackBytes))
	}
	if getPendingConfig() == nil {
		t.Fatalf("expected the newer package to be staged as pending")
	}
}
