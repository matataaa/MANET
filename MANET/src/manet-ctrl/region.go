package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// regionRoot is the filesystem root the radio config files live under;
// tests point it at a temp dir.
var regionRoot = "/"

// dutyCycleOffOptions is the Morse module line that turns automatic duty
// cycling off. Line formats here must match write_morse_conf in
// radio-setup.sh.
const dutyCycleOffOptions = "options morse enable_auto_duty_cycle=0 enable_auto_mpsw=0"

// spiDefaultTxMaxMBM is radio-setup.sh's SPI driver cap when
// halow_txpower_dbm is unset; USB boards keep the driver default.
const spiDefaultTxMaxMBM = "2400"

var (
	regionCodeRE     = regexp.MustCompile(`^[A-Z]{2}$`)
	morseCountryRE   = regexp.MustCompile(`(?m)^options morse country=\S*$`)
	morseTxMaxRE     = regexp.MustCompile(`(?m)^options morse tx_max_power_mbm=\S*\n?`)
	morseSPIRE       = regexp.MustCompile(`(?m)^options morse spi_clock_speed=`)
	legacyDutyNRE    = regexp.MustCompile(` enable_auto_duty_cycle=N\b`)
	wpaCountryRE     = regexp.MustCompile(`(?m)^country=\S*$`)
	hostapdCountryRE = regexp.MustCompile(`(?m)^country_code=.*$`)
	txpowerFixedRE   = regexp.MustCompile(`txpower fixed \d+`)
)

// regionValueError rejects anything but a real two-letter country code. The
// value is written verbatim into modprobe options and radio configs, where a
// space or '=' would add options of its own. "EU" is refused: the kernel's
// regulatory.db has no EU entry, so cfg80211 would leave the Wi-Fi radios on
// the world domain (5 GHz no-initiate, no mesh). EU countries get the EU
// HaLow plan from their own code (see resolveHalowDomain).
func regionValueError(key, value string) error {
	if !regionCodeRE.MatchString(value) {
		return fmt.Errorf("%s must be a two-letter country code such as US or NL", key)
	}
	if value == "EU" {
		return fmt.Errorf("%s must be a real country code (e.g. NL or DE), not EU; EU countries use the EU HaLow plan automatically", key)
	}
	return nil
}

// halowOptionValueError validates halow_duty_cycle and halow_txpower_dbm.
// 30 dBm is the highest any Morse regulatory table allows; empty means the
// default for both.
func halowOptionValueError(key, value string) error {
	if value == "" {
		return nil
	}
	switch key {
	case "halow_duty_cycle":
		if value != "off" && value != "auto" {
			return fmt.Errorf("halow_duty_cycle must be off or auto")
		}
	case "halow_txpower_dbm":
		if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 30 {
			return fmt.Errorf("halow_txpower_dbm must be a whole number from 1 to 30")
		}
	}
	return nil
}

// halowDutyCycleOff mirrors halow_duty_cycle_off in radio-setup.sh: the
// operator's choice when set, otherwise off on the EU plan (whose Morse
// regulatory rule would cap airtime at 10%/2.8%) and the driver's automatic
// regional value elsewhere (100% for US).
func halowDutyCycleOff(conf map[string]string, halow string) bool {
	switch conf["halow_duty_cycle"] {
	case "off":
		return true
	case "auto":
		return false
	}
	return halow == "EU"
}

// halowTxpowerMBM is the HaLow power request in mBm: halow_txpower_dbm when
// set, otherwise the per-bandwidth default. The second result says whether
// it is an operator override.
func halowTxpowerMBM(conf map[string]string) (string, bool) {
	if v := conf["halow_txpower_dbm"]; v != "" && halowOptionValueError("halow_txpower_dbm", v) == nil {
		return v + "00", true
	}
	_, _, _, tx := halowBWParams(effectiveHalowBW(conf), resolveHalowDomain(conf))
	return tx, false
}

// morseConf sets the country, duty-cycle and driver power-cap lines in a
// morse.conf, keeping every other option (bcf, spi_clock_speed, power save)
// as radio-setup.sh wrote it. txMaxMBM empty restores radio-setup.sh's
// default cap: 2400 on SPI boards, none (driver default) on USB.
func morseConf(text, halow string, dutyOff bool, txMaxMBM string) string {
	country := "options morse country=" + halow
	if morseCountryRE.MatchString(text) {
		text = morseCountryRE.ReplaceAllLiteralString(text, country)
	} else {
		text = appendLine(text, country)
	}

	// Older radio-setup.sh wrote enable_auto_duty_cycle=N into the SPI
	// power-save line, which would override an "auto" choice.
	text = legacyDutyNRE.ReplaceAllLiteralString(text, "")

	if txMaxMBM == "" && morseSPIRE.MatchString(text) {
		txMaxMBM = spiDefaultTxMaxMBM
	}
	switch {
	case txMaxMBM == "":
		text = morseTxMaxRE.ReplaceAllLiteralString(text, "")
	case morseTxMaxRE.MatchString(text):
		text = morseTxMaxRE.ReplaceAllLiteralString(text, "options morse tx_max_power_mbm="+txMaxMBM+"\n")
	default:
		text = appendLine(text, "options morse tx_max_power_mbm="+txMaxMBM)
	}

	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if l != dutyCycleOffOptions {
			kept = append(kept, l)
		}
	}
	text = strings.Join(kept, "\n")
	if dutyOff {
		text = appendLine(text, dutyCycleOffOptions)
	}
	return text
}

func appendLine(text, line string) string {
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + line + "\n"
}

// applyRadioConfigFiles writes the region, HaLow duty cycle and HaLow power
// into the radio files radio-setup.sh only writes once, at provisioning: the
// cfg80211 and Morse module options, crda, hostapd, the Wi-Fi mesh
// supplicants and the halow-txpower units. Without it a region change from
// the UI or a fleet push reached mesh.conf and the HaLow supplicant only,
// and the Morse module kept its provisioned country -- so an EU config
// still transmitted on US-plan frequencies.
//
// Module options are read only when the drivers load, and the Morse driver
// applies power when it sets a channel, so everything here takes effect at
// the next boot. The HaLow supplicant's own country and channel are
// applyHalowBW's job, which uses the same resolveHalowDomain.
//
// Returns the files it changed; a file already holding the right value is
// left untouched, so this is safe to call on every save.
func applyRadioConfigFiles(conf map[string]string) ([]string, error) {
	country := confGet(conf, "regulatory_domain", "US")
	if err := regionValueError("regulatory_domain", country); err != nil {
		return nil, err
	}
	halow := resolveHalowDomain(conf)
	for _, k := range []string{"halow_duty_cycle", "halow_txpower_dbm"} {
		if err := halowOptionValueError(k, conf[k]); err != nil {
			return nil, err
		}
	}
	txMBM, override := halowTxpowerMBM(conf)
	txMaxMBM := ""
	if override {
		txMaxMBM = txMBM
	}
	dutyOff := halowDutyCycleOff(conf, halow)

	path := func(rel string) string { return filepath.Join(regionRoot, rel) }
	var changed []string
	var errs []string
	update := func(p string, transform func(string) string, create bool) {
		data, err := os.ReadFile(p)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, err.Error())
				return
			}
			if !create {
				return
			}
		}
		next := transform(string(data))
		if err == nil && next == string(data) {
			return
		}
		mode := os.FileMode(0644)
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := writeFileFsync(p, []byte(next)); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
			return
		}
		os.Chmod(p, mode)
		changed = append(changed, p)
	}
	set := func(content string) func(string) string {
		return func(string) string { return content }
	}

	update(path("etc/modprobe.d/cfg80211.conf"), set("options cfg80211 ieee80211_regdom="+country+"\n"), true)
	update(path("etc/default/crda"), set("REGDOMAIN="+country+"\n"), true)
	// morse.conf only exists on nodes with a HaLow radio; never create it.
	update(path("etc/modprobe.d/morse.conf"), func(t string) string {
		return morseConf(t, halow, dutyOff, txMaxMBM)
	}, false)
	update(path("etc/hostapd/hostapd.conf"), func(t string) string {
		return hostapdCountryRE.ReplaceAllLiteralString(t, "country_code="+country)
	}, false)

	// Wi-Fi mesh supplicants (including the -lobby variants) carry an
	// unquoted top-level country=; the HaLow -s1g ones are applyHalowBW's.
	wpaFiles, _ := filepath.Glob(path("etc/wpa_supplicant/wpa_supplicant-wlan*.conf"))
	for _, p := range wpaFiles {
		if strings.Contains(filepath.Base(p), "s1g") {
			continue
		}
		update(p, func(t string) string {
			return wpaCountryRE.ReplaceAllLiteralString(t, "country="+country)
		}, false)
	}

	units, _ := filepath.Glob(path("etc/systemd/system/halow-txpower-*.service"))
	for _, p := range units {
		update(p, func(t string) string {
			return txpowerFixedRE.ReplaceAllLiteralString(t, "txpower fixed "+txMBM)
		}, false)
	}

	if len(changed) > 0 {
		log.Printf("radio config: region %s, HaLow %s, duty cycle off=%v, HaLow power %s mBm written to %s; takes effect at next boot",
			country, halow, dutyOff, txMBM, strings.Join(changed, ", "))
	}
	if len(errs) > 0 {
		return changed, fmt.Errorf("radio config files not fully updated: %s", strings.Join(errs, "; "))
	}
	return changed, nil
}
