package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNet builds a /sys/class/net tree; each entry is name -> {phy, flags}.
// An empty phy means no phy80211/name (not a wireless netdev).
func fakeNet(t *testing.T, ifaces map[string][2]string) {
	t.Helper()
	dir := t.TempDir()
	for name, v := range ifaces {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(p, "phy80211"), 0755); err != nil {
			t.Fatal(err)
		}
		if v[0] != "" {
			os.WriteFile(filepath.Join(p, "phy80211/name"), []byte(v[0]+"\n"), 0644)
		}
		if v[1] != "" {
			os.WriteFile(filepath.Join(p, "flags"), []byte(v[1]+"\n"), 0644)
		}
	}
	old := sysClassNet
	sysClassNet = dir
	t.Cleanup(func() { sysClassNet = old })
}

func TestTxPowerTargetUsesPHYForWifi(t *testing.T) {
	fakeNet(t, map[string][2]string{
		"wlan0": {"phy0", "0x1003"},
		"wlan1": {"phy1", "0x1003"},
		"bat0":  {"", "0x1003"},
	})
	got, err := txPowerTarget("wlan1")
	if err != nil || got != "phy1" {
		t.Fatalf("got %q, %v; want phy1", got, err)
	}
}

func TestTxPowerTargetRefusesSharedUpPHY(t *testing.T) {
	for _, flags := range []string{"0x1003", "", "garbage"} {
		fakeNet(t, map[string][2]string{
			"wlan0": {"phy0", "0x1003"},
			"ap0":   {"phy0", flags},
		})
		_, err := txPowerTarget("wlan0")
		if err == nil || !strings.Contains(err.Error(), "phy0 also serves ap0") {
			t.Fatalf("flags %q: got err %v, want shared-PHY refusal", flags, err)
		}
	}
}

func TestTxPowerTargetAllowsSharedDownPHY(t *testing.T) {
	fakeNet(t, map[string][2]string{
		"wlan0": {"phy0", "0x1003"},
		"ap0":   {"phy0", "0x1002"},
	})
	if _, err := txPowerTarget("wlan0"); err != nil {
		t.Fatal(err)
	}
}

func TestTxPowerTargetRefusesUnknownPHY(t *testing.T) {
	for _, phy := range []string{"", "wlan0"} {
		fakeNet(t, map[string][2]string{"wlan0": {phy, "0x1003"}})
		if _, err := txPowerTarget("wlan0"); err == nil {
			t.Fatalf("phy %q: expected an error, not a fallback", phy)
		}
	}
}

// morse ignores the per-netdev request just like mt76, so HaLow gets no
// exception.
func TestTxPowerTargetUsesPHYForHaLow(t *testing.T) {
	fakeNet(t, map[string][2]string{"wlan2": {"phy0", "0x1003"}})
	drv := filepath.Join(t.TempDir(), "morse_usb")
	os.MkdirAll(drv, 0755)
	os.MkdirAll(filepath.Join(sysClassNet, "wlan2/device"), 0755)
	if err := os.Symlink(drv, filepath.Join(sysClassNet, "wlan2/device/driver")); err != nil {
		t.Fatal(err)
	}
	if got, err := txPowerTarget("wlan2"); err != nil || got != "phy0" {
		t.Fatalf("got %q, %v; want phy0", got, err)
	}
}
