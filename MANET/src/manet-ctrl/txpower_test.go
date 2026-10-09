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

// eud4SKU2G is EUD4's phy1 (2.4 GHz, channel 6) txpower_sku at a 30 dBm
// request, trimmed; values are 0.5 dB units.
const eud4SKU2G = `
Phy0 Tx power table (channel 6)
                      1m     2m     5m    11m
CCK (TMAC)      :     35     35     35     35
                      6m     9m    12m    18m    24m    36m    48m    54m
OFDM (TMAC)     :     32     32     32     32     31     31     29     29
                    mcs0   mcs1   mcs2   mcs3   mcs4   mcs5   mcs6   mcs7
HT_BW20 (TMAC)  :     31     31     31     29     29     29     28     27
HE_RU242 (TMAC) :     31     31     31     29     29     29     28     27     24     24     22     22

Tx power (bbp)  :     31
`

func fakeSKU(t *testing.T, phy, table string) {
	t.Helper()
	dir := t.TempDir()
	if table != "" {
		os.MkdirAll(filepath.Join(dir, phy, "mt76"), 0755)
		os.WriteFile(filepath.Join(dir, phy, "mt76/txpower_sku"), []byte(table), 0644)
	}
	old := debugfsIEEE80211
	debugfsIEEE80211 = dir
	t.Cleanup(func() { debugfsIEEE80211 = old })
}

func TestEffectiveTxPowerUsesMT76RateTable(t *testing.T) {
	fakeNet(t, map[string][2]string{"wlan0": {"phy1", "0x1003"}})
	fakeSKU(t, "phy1", eud4SKU2G)
	// CCK's 35 (17.5 dBm) is ignored; OFDM 6M's 32 is the highest = 16 dBm.
	if got := effectiveTxPower("wlan0", "30.00"); got != "16" {
		t.Fatalf("30 dBm ceiling over a 16 dBm table: got %q, want 16", got)
	}
	// A request below the table is what the radio uses.
	if got := effectiveTxPower("wlan0", "10.00"); got != "10.00" {
		t.Fatalf("10 dBm request: got %q, want the report", got)
	}
}

func TestEffectiveTxPowerFallsBackToReport(t *testing.T) {
	fakeNet(t, map[string][2]string{"wlan2": {"phy0", "0x1003"}})
	fakeSKU(t, "phy0", "") // HaLow: no mt76 table
	if got := effectiveTxPower("wlan2", "24.00"); got != "24.00" {
		t.Fatalf("got %q, want the iw report", got)
	}
	fakeSKU(t, "phy0", "garbage without rate rows\n")
	if got := effectiveTxPower("wlan2", "24.00"); got != "24.00" {
		t.Fatalf("unparsable table: got %q, want the iw report", got)
	}
}
