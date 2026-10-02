package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain keeps every test in this package away from the real persisted
// activation in /var/lib: savePendingConfig writes it whenever a pending
// config carries activate_at.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "manet-ctrl-test")
	if err != nil {
		panic(err)
	}
	pendingActivationPath = filepath.Join(dir, "pending_activation.json")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func withActivationEnv(t *testing.T, bootID string, synced bool) {
	t.Helper()
	origPath, origBoot, origSync := pendingActivationPath, currentBootID, clockSynchronized
	pendingActivationPath = filepath.Join(t.TempDir(), "pending_activation.json")
	setActivationEnv(bootID, synced)
	t.Cleanup(func() {
		pendingActivationPath, currentBootID, clockSynchronized = origPath, origBoot, origSync
	})
}

func setActivationEnv(bootID string, synced bool) {
	currentBootID = func() string { return bootID }
	clockSynchronized = func() bool { return synced }
}

func armedPkg(activateAt int64) map[string]interface{} {
	return map[string]interface{}{
		"version":     "v1",
		"pkg_id":      "pkg-1",
		"staged_at":   float64(time.Now().Unix()),
		"activate_at": float64(activateAt),
		"config":      map[string]interface{}{"callsign": "NEW"},
	}
}

func TestOnlyArmedPendingConfigIsPersisted(t *testing.T) {
	withTempFleetProcessFiles(t)
	withActivationEnv(t, "boot-A", true)

	staged := armedPkg(0)
	delete(staged, "activate_at")
	if err := savePendingConfig(staged); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingActivationPath); err == nil {
		t.Fatalf("a staged (not activated) config must not be persisted")
	}

	if err := savePendingConfig(armedPkg(time.Now().Unix() + 60)); err != nil {
		t.Fatal(err)
	}
	var pa persistedActivation
	data, err := os.ReadFile(pendingActivationPath)
	if err != nil || json.Unmarshal(data, &pa) != nil || pa.BootID != "boot-A" || pa.Pkg["pkg_id"] != "pkg-1" {
		t.Fatalf("armed activation not persisted correctly: %v %s", err, data)
	}

	clearPendingConfig()
	if _, err := os.Stat(pendingActivationPath); err == nil {
		t.Fatalf("clearPendingConfig must also clear the persisted activation")
	}
}

// The EUD2 case: armed, rebooted before activate_at (tmpfs pending lost),
// came back with a stale, unsynchronized clock.
func TestArmedActivationAppliedAfterReboot(t *testing.T) {
	withTempMeshConf(t, "callsign=OLD\n")
	withTempFleetProcessFiles(t)
	withActivationEnv(t, "boot-A", true)

	if err := savePendingConfig(armedPkg(time.Now().Unix() + 30)); err != nil {
		t.Fatal(err)
	}
	os.Remove(PendingConfFile) // reboot: /var/run is gone
	setActivationEnv("boot-B", false)

	fleetResumeActivation()
	fleetCheckActivation()

	if got := loadKVFile(MeshConfFile)["callsign"]; got != "NEW" {
		t.Fatalf("armed activation was not applied after reboot: callsign=%q", got)
	}
	if !isPkgIDApplied("pkg-1") {
		t.Fatalf("applied package not recorded")
	}
	if _, err := os.Stat(pendingActivationPath); err == nil {
		t.Fatalf("persisted activation not cleared after apply")
	}
}

func TestRestartWithinSameBootKeepsSchedule(t *testing.T) {
	withTempMeshConf(t, "callsign=OLD\n")
	withTempFleetProcessFiles(t)
	withActivationEnv(t, "boot-A", true)

	future := time.Now().Unix() + 45
	if err := savePendingConfig(armedPkg(future)); err != nil {
		t.Fatal(err)
	}
	os.Remove(PendingConfFile)

	fleetResumeActivation()
	fleetCheckActivation()

	if got := loadKVFile(MeshConfFile)["callsign"]; got != "OLD" {
		t.Fatalf("activation applied before activate_at within the same boot: callsign=%q", got)
	}
	var pkg map[string]interface{}
	if json.Unmarshal(getPendingConfig(), &pkg) != nil || int64(pkg["activate_at"].(float64)) != future {
		t.Fatalf("pending activation not restored with its original activate_at: %v", pkg)
	}
}

func TestAlreadyAppliedPersistedActivationIsDropped(t *testing.T) {
	withTempMeshConf(t, "callsign=OLD\n")
	withTempFleetProcessFiles(t)
	withActivationEnv(t, "boot-A", true)

	if err := savePendingConfig(armedPkg(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	if err := recordPkgIDApplied("pkg-1", "v1", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	os.Remove(PendingConfFile)
	setActivationEnv("boot-B", true)

	fleetResumeActivation()

	if getPendingConfig() != nil {
		t.Fatalf("an already-applied activation must not be restored")
	}
	if _, err := os.Stat(pendingActivationPath); err == nil {
		t.Fatalf("persisted record of an applied package not cleared")
	}
}

func TestActivationTimingWithUnsynchronizedClock(t *testing.T) {
	withActivationEnv(t, "boot-A", true)
	now := time.Now()
	weeksAhead := now.Add(46 * 24 * time.Hour).Unix()

	if activationAcceptable(weeksAhead, now) {
		t.Fatalf("with a synchronized clock, an activate_at weeks away must be rejected")
	}
	if got := localActivateAt(weeksAhead, now); got != weeksAhead {
		t.Fatalf("synchronized clock must use the stager's activate_at, got %d", got)
	}

	setActivationEnv("boot-A", false)
	if !activationAcceptable(weeksAhead, now) {
		t.Fatalf("with an unsynchronized clock the bound can't be judged and must not reject")
	}
	if got := localActivateAt(weeksAhead, now); got != now.Add(activationWindow).Unix() {
		t.Fatalf("unsynchronized clock must arm one window from now, got %d", got)
	}
}
