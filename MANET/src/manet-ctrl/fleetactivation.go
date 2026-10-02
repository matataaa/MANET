package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"syscall"
	"time"
)

// A fleet activation must survive a reboot. The pending config lives in
// /var/run (tmpfs), so a node that rebooted between arming an activation and
// reaching activate_at lost it and came back on the old config. For a
// mesh_key or channel change that orphans the node: the rest of the fleet
// has switched and it can no longer even receive the package again.
// Observed on EUD2 during the 2026-10-02 fleet mesh-key test.
//
// So an armed activation (a pending config with activate_at) is also written
// to /var/lib with the kernel boot_id. After a reboot it is applied
// straight away: activation is never more than one window (60s) away when
// armed and a reboot takes longer, and an activated push cannot be
// cancelled.
//
// These boards have no RTC: after boot the clock is a stale image or
// fake-hwclock time, often weeks behind, until chrony syncs. Wall-clock
// comparisons against the stager's activate_at are meaningless then, so with
// an unsynchronized clock a node arms its own deadline one window from when
// it received the activation instead.
const (
	PendingActivationFile = "/var/lib/manet_pending_activation.json"
	activationWindow      = 60 * time.Second // apiAdminActivate uses now+60s
	staUnsync             = 0x0040           // STA_UNSYNC in timex.status
)

// Vars rather than direct calls so tests can substitute them.
var (
	pendingActivationPath = PendingActivationFile
	currentBootID         = readBootID
	clockSynchronized     = kernelClockSynchronized
)

type persistedActivation struct {
	BootID string                 `json:"boot_id"`
	Pkg    map[string]interface{} `json:"pkg"`
}

func readBootID() string {
	data, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data))
}

// kernelClockSynchronized reports whether the kernel considers the clock
// disciplined (chrony clears STA_UNSYNC once synced). If it can't tell, it
// returns true so the strict wall-clock checks stay in force.
func kernelClockSynchronized() bool {
	var tx syscall.Timex
	if _, err := syscall.Adjtimex(&tx); err != nil {
		return true
	}
	return tx.Status&staUnsync == 0
}

func persistActivation(pkg map[string]interface{}) error {
	data, err := json.Marshal(persistedActivation{BootID: currentBootID(), Pkg: pkg})
	if err != nil {
		return err
	}
	return writeFileFsync(pendingActivationPath, data)
}

func clearPersistedActivation() {
	os.Remove(pendingActivationPath)
}

// activationAcceptable is activateAtInBounds when the clock can be trusted.
// With an unsynchronized clock the bound can't be judged (a stale clock puts
// a genuine activation weeks in the "future"), so the package's GCM
// authentication and the pkg_id replay record are the remaining checks.
func activationAcceptable(activateAt int64, now time.Time) bool {
	if !clockSynchronized() {
		return true
	}
	return activateAtInBounds(activateAt, now)
}

// localActivateAt is the activate_at this node arms: the stager's absolute
// time when the clock is synchronized, otherwise one activation window from
// now on the local clock.
func localActivateAt(activateAt int64, now time.Time) int64 {
	if clockSynchronized() {
		return activateAt
	}
	return now.Add(activationWindow).Unix()
}

// fleetResumeActivation runs once at startup. If an armed activation was
// persisted and not yet applied, it is put back as the pending config: as
// is after a manet-ctrl restart within the same boot, or due immediately
// after a reboot (fleetCheckActivation then applies it on its next tick).
func fleetResumeActivation() {
	data, err := os.ReadFile(pendingActivationPath)
	if err != nil {
		return
	}
	var pa persistedActivation
	if json.Unmarshal(data, &pa) != nil || pa.Pkg == nil {
		clearPersistedActivation()
		return
	}
	pkgID, _ := pa.Pkg["pkg_id"].(string)
	if pkgID == "" || isPkgIDApplied(pkgID) {
		clearPersistedActivation()
		return
	}

	pkg := pa.Pkg
	if pa.BootID != currentBootID() {
		now := time.Now()
		at, _ := pkg["activate_at"].(float64)
		if !clockSynchronized() || now.Unix() >= int64(at) {
			pkg["activate_at"] = now.Unix()
		}
		log.Printf("fleet: rebooted with activation pkg_id=%s armed; applying it", pkgID)
	} else if getPendingConfig() != nil {
		return
	}
	if err := savePendingConfig(pkg); err != nil {
		log.Printf("fleet: failed to restore armed activation pkg_id=%s: %v", pkgID, err)
	}
}
