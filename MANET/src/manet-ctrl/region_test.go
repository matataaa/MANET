package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The morse.conf the current radio-setup.sh write_morse_conf produces for an
// SPI board on the US plan.
const spiMorseConf = `options morse enable_mcast_whitelist=0 enable_mcast_rate_control=1
options morse country=US
options morse enable_ps=0 enable_dynamic_ps_offload=N enable_twt=N
options morse tx_max_power_mbm=2400
options morse bcf=bcf_fgh100mhaamd.bin
options morse spi_clock_speed=15000000
`

// ... and for a USB MM81xx board: no BCF, SPI or power lines.
const usbMorseConf = `options morse enable_mcast_whitelist=0 enable_mcast_rate_control=1
options morse country=US
`

func TestMorseConfUSToEUAndBack(t *testing.T) {
	eu := morseConf(spiMorseConf, "EU", true, "")
	if !strings.Contains(eu, "\noptions morse country=EU\n") || strings.Contains(eu, "country=US") {
		t.Fatalf("country not switched to EU:\n%s", eu)
	}
	if !strings.HasSuffix(eu, dutyCycleOffOptions+"\n") {
		t.Fatalf("duty-cycle-off line missing:\n%s", eu)
	}
	for _, keep := range []string{"bcf=bcf_fgh100mhaamd.bin", "spi_clock_speed=15000000", "tx_max_power_mbm=2400", "enable_ps=0"} {
		if !strings.Contains(eu, keep) {
			t.Fatalf("lost %q:\n%s", keep, eu)
		}
	}
	if again := morseConf(eu, "EU", true, ""); again != eu {
		t.Fatalf("not idempotent:\n%s\nvs\n%s", again, eu)
	}
	back := morseConf(eu, "US", false, "")
	if strings.Contains(back, dutyCycleOffOptions) || !strings.Contains(back, "country=US") {
		t.Fatalf("EU -> US did not clear EU state:\n%s", back)
	}
	if morseConf(back, "US", false, "") != back {
		t.Fatalf("US result not stable")
	}
}

func TestMorseConfPowerCap(t *testing.T) {
	// Override on USB adds the cap; clearing it removes it again.
	usb := morseConf(usbMorseConf, "US", false, "2800")
	if !strings.Contains(usb, "options morse tx_max_power_mbm=2800\n") {
		t.Fatalf("override cap missing:\n%s", usb)
	}
	if got := morseConf(usb, "US", false, ""); got != usbMorseConf {
		t.Fatalf("clearing the override on USB must restore the driver default:\n%s", got)
	}
	// On SPI, clearing restores radio-setup.sh's 2400 rather than removing it.
	spi := morseConf(spiMorseConf, "US", false, "3000")
	if strings.Count(spi, "tx_max_power_mbm=") != 1 || !strings.Contains(spi, "tx_max_power_mbm=3000") {
		t.Fatalf("SPI override not a single 3000 line:\n%s", spi)
	}
	if got := morseConf(spi, "US", false, ""); !strings.Contains(got, "tx_max_power_mbm=2400") || strings.Contains(got, "3000") {
		t.Fatalf("clearing the override on SPI must restore 2400:\n%s", got)
	}
}

func TestMorseConfDropsLegacyDutyCycleN(t *testing.T) {
	legacy := "options morse enable_ps=0 enable_dynamic_ps_offload=N enable_auto_duty_cycle=N enable_twt=N\noptions morse country=US\n"
	got := morseConf(legacy, "US", false, "")
	if strings.Contains(got, "enable_auto_duty_cycle") {
		t.Fatalf("auto must not keep the old =N override:\n%s", got)
	}
	if !strings.Contains(got, "enable_ps=0 enable_dynamic_ps_offload=N enable_twt=N") {
		t.Fatalf("power-save options damaged:\n%s", got)
	}
}

func TestMorseConfAddsMissingCountry(t *testing.T) {
	got := morseConf("options morse bcf=x.bin", "US", false, "")
	if got != "options morse bcf=x.bin\noptions morse country=US\n" {
		t.Fatalf("got %q", got)
	}
}

func TestHalowDutyCycleOff(t *testing.T) {
	for _, c := range []struct {
		setting, halow string
		want           bool
	}{
		{"", "EU", true}, {"", "US", false},
		{"off", "US", true}, {"auto", "EU", false},
	} {
		if got := halowDutyCycleOff(map[string]string{"halow_duty_cycle": c.setting}, c.halow); got != c.want {
			t.Errorf("halow_duty_cycle=%q on %s: got %v, want %v", c.setting, c.halow, got, c.want)
		}
	}
}

func TestHalowTxpowerMBM(t *testing.T) {
	if got, override := halowTxpowerMBM(map[string]string{"halow_bw": "4MHz"}); got != "2200" || override {
		t.Errorf("US 4MHz default: got %s override=%v", got, override)
	}
	if got, override := halowTxpowerMBM(map[string]string{"halow_bw": "4MHz", "halow_txpower_dbm": "27"}); got != "2700" || !override {
		t.Errorf("override: got %s override=%v", got, override)
	}
}

func TestRegionAndHalowOptionValidation(t *testing.T) {
	for _, ok := range [][2]string{
		{"regulatory_domain", "US"}, {"regulatory_domain", "NL"}, {"regulatory_domain", "JP"},
		{"halow_duty_cycle", ""}, {"halow_duty_cycle", "off"}, {"halow_duty_cycle", "auto"},
		{"halow_txpower_dbm", ""}, {"halow_txpower_dbm", "1"}, {"halow_txpower_dbm", "30"},
	} {
		if err := configValueError(ok[0], ok[1]); err != nil {
			t.Errorf("%s=%q rejected: %v", ok[0], ok[1], err)
		}
	}
	for _, bad := range [][2]string{
		{"regulatory_domain", ""}, {"regulatory_domain", "EU"}, {"regulatory_domain", "us"}, {"regulatory_domain", "US bcf=evil.bin"},
		{"halow_duty_cycle", "0"}, {"halow_duty_cycle", "off x=1"},
		{"halow_txpower_dbm", "0"}, {"halow_txpower_dbm", "31"}, {"halow_txpower_dbm", "24.5"}, {"halow_txpower_dbm", "24 bcf=x"},
	} {
		if err := configValueError(bad[0], bad[1]); err == nil {
			t.Errorf("%s=%q accepted", bad[0], bad[1])
		}
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func readTree(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func withRegionRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	orig := regionRoot
	regionRoot = root
	t.Cleanup(func() { regionRoot = orig })
	return root
}

const halowTxpowerUnit = "[Service]\nType=oneshot\nExecStart=/usr/sbin/iw dev wlan2 set txpower fixed 2200\n"

func TestApplyRadioConfigFiles(t *testing.T) {
	root := withRegionRoot(t)
	s1g := "network={\n    country=\"US\"\n    op_class=69\n}\n"
	writeTree(t, root, map[string]string{
		"etc/modprobe.d/cfg80211.conf":                       "options cfg80211 ieee80211_regdom=US\n",
		"etc/modprobe.d/morse.conf":                          spiMorseConf,
		"etc/hostapd/hostapd.conf":                           "interface=wlan1\ncountry_code=US\nchannel=36\n",
		"etc/wpa_supplicant/wpa_supplicant-wlan0.conf":       "ctrl_interface=/var/run/wpa_supplicant\ncountry=US\nnetwork={\n ssid=\"country=US\"\n}\n",
		"etc/wpa_supplicant/wpa_supplicant-wlan1-lobby.conf": "country=US\n",
		"etc/wpa_supplicant/wpa_supplicant-wlan2-s1g.conf":   s1g,
		"etc/systemd/system/halow-txpower-wlan2.service":     halowTxpowerUnit,
	})
	os.MkdirAll(filepath.Join(root, "etc/default"), 0755)

	changed, err := applyRadioConfigFiles(map[string]string{"regulatory_domain": "NL", "halow_bw": "1MHz"})
	if err != nil {
		t.Fatal(err)
	}
	// crda is created; everything else but the s1g conf is rewritten (the
	// unit moves from 22 to the EU 1MHz default of 24 dBm).
	if len(changed) != 7 {
		t.Fatalf("changed %d files, want 7: %v", len(changed), changed)
	}
	for rel, want := range map[string]string{
		"etc/modprobe.d/cfg80211.conf":                       "options cfg80211 ieee80211_regdom=NL\n",
		"etc/default/crda":                                   "REGDOMAIN=NL\n",
		"etc/hostapd/hostapd.conf":                           "interface=wlan1\ncountry_code=NL\nchannel=36\n",
		"etc/wpa_supplicant/wpa_supplicant-wlan0.conf":       "ctrl_interface=/var/run/wpa_supplicant\ncountry=NL\nnetwork={\n ssid=\"country=US\"\n}\n",
		"etc/wpa_supplicant/wpa_supplicant-wlan1-lobby.conf": "country=NL\n",
		"etc/wpa_supplicant/wpa_supplicant-wlan2-s1g.conf":   s1g,
		"etc/systemd/system/halow-txpower-wlan2.service":     strings.Replace(halowTxpowerUnit, "2200", "2400", 1),
	} {
		if got := readTree(t, root, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	morse := readTree(t, root, "etc/modprobe.d/morse.conf")
	if !strings.Contains(morse, "options morse country=EU\n") || !strings.Contains(morse, dutyCycleOffOptions) {
		t.Errorf("NL must put HaLow on the EU plan with duty cycling off:\n%s", morse)
	}
	if fi, _ := os.Stat(filepath.Join(root, "etc/wpa_supplicant/wpa_supplicant-wlan0.conf")); fi.Mode().Perm() != 0600 {
		t.Errorf("file mode not preserved: %v", fi.Mode().Perm())
	}

	if changed, err := applyRadioConfigFiles(map[string]string{"regulatory_domain": "NL", "halow_bw": "1MHz"}); err != nil || len(changed) != 0 {
		t.Fatalf("second apply must be a no-op, changed %v, err %v", changed, err)
	}

	// Operator overrides: EU with the regional duty cycle and a 27 dBm request.
	if _, err := applyRadioConfigFiles(map[string]string{"regulatory_domain": "NL", "halow_bw": "1MHz",
		"halow_duty_cycle": "auto", "halow_txpower_dbm": "27"}); err != nil {
		t.Fatal(err)
	}
	morse = readTree(t, root, "etc/modprobe.d/morse.conf")
	if strings.Contains(morse, dutyCycleOffOptions) || !strings.Contains(morse, "tx_max_power_mbm=2700") {
		t.Errorf("overrides not written:\n%s", morse)
	}
	if got := readTree(t, root, "etc/systemd/system/halow-txpower-wlan2.service"); !strings.Contains(got, "txpower fixed 2700") {
		t.Errorf("power request not written: %q", got)
	}

	// Back to US with defaults restores the original files. A stale
	// halow_regulatory_domain is ignored: HaLow follows the country.
	if _, err := applyRadioConfigFiles(map[string]string{"regulatory_domain": "US", "halow_regulatory_domain": "EU", "halow_bw": "2MHz"}); err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, root, "etc/modprobe.d/morse.conf"); got != spiMorseConf {
		t.Errorf("US with defaults did not restore the US morse.conf:\n%s", got)
	}
	if got := readTree(t, root, "etc/default/crda"); got != "REGDOMAIN=US\n" {
		t.Errorf("crda = %q, want US", got)
	}
}

func TestResolveHalowDomainFollowsCountry(t *testing.T) {
	for _, c := range []struct{ country, stale, want string }{
		{"", "", "US"},
		{"US", "", "US"},
		{"NL", "", "EU"},
		{"NL", "US", "EU"}, // old override ignored
		{"US", "EU", "US"},
		{"JP", "", "JP"},
	} {
		conf := map[string]string{"halow_regulatory_domain": c.stale}
		if c.country != "" {
			conf["regulatory_domain"] = c.country
		}
		if got := resolveHalowDomain(conf); got != c.want {
			t.Errorf("country %q (stale HaLow %q): got %s, want %s", c.country, c.stale, got, c.want)
		}
	}
}

func TestApplyRadioConfigFilesSkipsMissingRadios(t *testing.T) {
	root := withRegionRoot(t)
	writeTree(t, root, map[string]string{"etc/modprobe.d/cfg80211.conf": "options cfg80211 ieee80211_regdom=US\n"})
	os.MkdirAll(filepath.Join(root, "etc/default"), 0755)

	if _, err := applyRadioConfigFiles(map[string]string{"regulatory_domain": "NL"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/modprobe.d/morse.conf")); !os.IsNotExist(err) {
		t.Errorf("morse.conf must not be created on a node without HaLow")
	}
	if _, err := os.Stat(filepath.Join(root, "etc/hostapd/hostapd.conf")); !os.IsNotExist(err) {
		t.Errorf("hostapd.conf must not be created")
	}
}

func TestApplyRadioConfigFilesRejectsBadValue(t *testing.T) {
	root := withRegionRoot(t)
	for _, conf := range []map[string]string{
		{"regulatory_domain": "US bcf=x"},
		{"regulatory_domain": "US", "halow_txpower_dbm": "40"},
	} {
		if _, err := applyRadioConfigFiles(conf); err == nil {
			t.Fatalf("expected an error for %v", conf)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("nothing may be written for an invalid value, got %d entries", len(entries))
	}
}
