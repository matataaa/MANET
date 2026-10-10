package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func withTempUpdateDirs(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	oldState, oldSystemd, oldSoftware := updateStateDir, systemdDir, channelVersionFile["software"]
	updateStateDir = filepath.Join(dir, "state")
	systemdDir = filepath.Join(dir, "systemd")
	channelVersionFile["software"] = filepath.Join(dir, "release_version.txt")
	t.Cleanup(func() {
		updateStateDir, systemdDir = oldState, oldSystemd
		channelVersionFile["software"] = oldSoftware
	})
	return dir
}

func TestArmRecoveryKeepsPackageAndArmsBootUnit(t *testing.T) {
	dir := withTempUpdateDirs(t)
	pkg := filepath.Join(dir, "tools.tar.gz")
	if err := os.WriteFile(pkg, []byte("package"), 0644); err != nil {
		t.Fatal(err)
	}

	kept, err := armRecovery("software", pkg, "0.5.6")
	if err != nil {
		t.Fatal(err)
	}
	if kept != filepath.Join(updateStateDir, "software.tar.gz") {
		t.Errorf("kept = %s", kept)
	}
	if _, err := os.Stat(pkg); !os.IsNotExist(err) {
		t.Errorf("download still at %s", pkg)
	}
	if b, _ := os.ReadFile(filepath.Join(updateStateDir, "software.pending")); string(b) != "0.5.6\n" {
		t.Errorf("pending = %q", b)
	}
	if target, err := os.Readlink(filepath.Join(systemdDir, "sysinit.target.wants", recoverUnitName)); err != nil || target != "../"+recoverUnitName {
		t.Errorf("wants link = %q, %v", target, err)
	}
	unit, _ := os.ReadFile(filepath.Join(systemdDir, recoverUnitName))
	if !strings.Contains(string(unit), "ExecStart=/bin/sh "+updateStateDir+"/recover.sh") {
		t.Errorf("unit does not run the recovery script:\n%s", unit)
	}
	script := filepath.Join(updateStateDir, "recover.sh")
	if out, err := exec.Command("sh", "-n", script).CombinedOutput(); err != nil {
		t.Errorf("recover.sh does not parse: %v: %s", err, out)
	}

	// A second update arms again over the existing link and script.
	if err := os.WriteFile(pkg, []byte("package2"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := armRecovery("software", pkg, "0.5.7"); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
}

func TestFinishRecoveryRecordsVersionThenClears(t *testing.T) {
	withTempUpdateDirs(t)
	if err := os.MkdirAll(updateStateDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"software.pending", "software.tar.gz"} {
		if err := os.WriteFile(filepath.Join(updateStateDir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := finishRecovery("software", "0.5.6"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(channelVersionFile["software"]); string(b) != "0.5.6\n" {
		t.Errorf("version file = %q", b)
	}
	for _, name := range []string{"software.pending", "software.tar.gz"} {
		if _, err := os.Stat(filepath.Join(updateStateDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present", name)
		}
	}
}
