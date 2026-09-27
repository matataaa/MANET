package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file covers W-a: the staged_at watermark had no clock-sanity bound,
// so a single node with a future-skewed clock (these boards have no RTC)
// could stage a package that, once applied, poisons highestAppliedStagedAt
// into the future -- permanently rejecting every legitimate push fleet-wide
// as a "rollback" until real time catches up.

// installFakeAlfred puts a stub `alfred` executable at the front of PATH for
// the duration of the test (restoring PATH afterward), so apiAdminStage's
// broadcastConfigPackage call succeeds instead of failing with "alfred: not
// found" -- letting this test exercise the real success path (including the
// staged_at computation) without needing the actual alfred daemon or a real
// mesh.
func installFakeAlfred(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake alfred stub is a POSIX shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\nexit 0\n"
	path := filepath.Join(dir, "alfred")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake alfred: %v", err)
	}
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
}

// TestApiAdminStageStagedAtMonotonicWatermark seeds a future-looking
// highestAppliedStagedAt watermark (simulating this node itself having
// previously applied a package from a peer with a skewed-forward clock),
// then stages a brand new package and confirms staged_at is computed as
// watermark+1 (strictly greater), not a plain time.Now().Unix() that would
// fall at or below the poisoned watermark and get rejected by every node
// (including this one) as a rollback replay.
func TestApiAdminStageStagedAtMonotonicWatermark(t *testing.T) {
	// withTempFleetProcessFiles covers PendingConfFile/AckVersionFile/
	// FleetPrefsFile/appliedConfigFilePath; apiAdminStage also reads
	// MeshConfFile directly, so override that too.
	withTempFleetProcessFiles(t)
	origMesh := MeshConfFile
	dir := t.TempDir()
	MeshConfFile = filepath.Join(dir, "mesh.conf")
	t.Cleanup(func() { MeshConfFile = origMesh })
	if err := os.WriteFile(MeshConfFile, []byte("admin_password=stage-test-pw\nmesh_ssid=test-mesh\n"), 0644); err != nil {
		t.Fatalf("seed mesh.conf: %v", err)
	}
	installFakeAlfred(t)

	farFuture := time.Now().Unix() + 100000
	if err := recordPkgIDApplied("poisoned-watermark-pkg", "v-skewed", farFuture); err != nil {
		t.Fatalf("seed poisoned watermark: %v", err)
	}

	body := `{"config":{"node_hostname":"renamed-node"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/stage", strings.NewReader(body))
	rr := httptest.NewRecorder()

	apiAdminStage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	pending := getPendingConfig()
	if pending == nil {
		t.Fatalf("expected a pending config to have been saved")
	}
	var pkg map[string]interface{}
	if err := json.Unmarshal(pending, &pkg); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	stagedAt, _ := pkg["staged_at"].(float64)
	if int64(stagedAt) != farFuture+1 {
		t.Fatalf("expected staged_at to be watermark+1 (%d), got %d", farFuture+1, int64(stagedAt))
	}
}

// TestFleetProcessPackageRejectsFutureSkewedStagedAt covers the receiving
// side: a package whose staged_at is implausibly far in the future relative
// to THIS node's own (assumed-sane) clock must be rejected before it can
// ever be staged/applied and poison this node's own watermark.
func TestFleetProcessPackageRejectsFutureSkewedStagedAt(t *testing.T) {
	withTempFleetProcessFiles(t)

	skewed := map[string]interface{}{
		"version":    "v-skewed-sender",
		"pkg_id":     "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		"staged_at":  float64(time.Now().Add(24 * time.Hour).Unix()),
		"expires_at": float64(time.Now().Add(48 * time.Hour).Unix()),
		"config":     map[string]interface{}{"node_hostname": "x"},
	}

	fleetProcessPackage(mustMarshal(t, skewed))

	if getPendingConfig() != nil {
		t.Fatalf("a package with a wildly future staged_at must not be staged")
	}
	ackBytes, _ := os.ReadFile(AckVersionFile)
	if string(ackBytes) == "v-skewed-sender" {
		t.Fatalf("a package with a wildly future staged_at must not be ACKed")
	}
}

// TestFleetProcessPackageAcceptsStagedAtWithinBounds is the negative
// counterpart -- a staged_at only a couple minutes in the future (ordinary
// clock skew between nodes, not a broken clock) must still be accepted.
func TestFleetProcessPackageAcceptsStagedAtWithinBounds(t *testing.T) {
	withTempFleetProcessFiles(t)

	fine := map[string]interface{}{
		"version":    "v-slightly-ahead",
		"pkg_id":     "ffffffffffffffffffffffffffffffff",
		"staged_at":  float64(time.Now().Add(2 * time.Minute).Unix()),
		"expires_at": float64(time.Now().Add(1 * time.Hour).Unix()),
		"config":     map[string]interface{}{"node_hostname": "x"},
	}

	fleetProcessPackage(mustMarshal(t, fine))

	ackBytes, _ := os.ReadFile(AckVersionFile)
	if string(ackBytes) != "v-slightly-ahead" {
		t.Fatalf("expected v-slightly-ahead to be accepted/ACKed, AckVersionFile contains %q", string(ackBytes))
	}
}
