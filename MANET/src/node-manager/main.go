package main

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	confFile        = "/etc/mesh.conf"
	meshIfFile      = "/var/lib/mesh_if"
	radioStateFile  = "/var/lib/mesh_radio_state.json"
	gwCheckInterval = 60
	loopInterval    = 15
	staticFreq24    = "2412"
	staticFreq5     = "5180"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("[node-manager] ")
	log.Println("starting")

	acsEnabled := loadConf("acs") == "y"
	if acsEnabled {
		log.Println("ACS (automatic channel selection) enabled")
		runACSTick()
	} else {
		ensureStaticChannels()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	tick := time.NewTicker(loopInterval * time.Second)
	defer tick.Stop()

	var lastGWCheck time.Time

	loop := func() {
		radioStateSync()
		if acsEnabled {
			runACSTick()
		} else {
			ensureStaticChannels()
		}
		if time.Since(lastGWCheck) >= gwCheckInterval*time.Second {
			gatewayReconcile()
			lastGWCheck = time.Now()
		}
		runElections()
	}

	loop()
	for {
		select {
		case <-tick.C:
			loop()
		case <-sig:
			log.Println("shutting down")
			return
		}
	}
}

func radioStateSync() {
	out, err := exec.Command("/usr/local/bin/mesh-radio-state", "sync").CombinedOutput()
	if err != nil {
		log.Printf("radio-state sync: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func gatewayReconcile() {
	path := "/usr/local/bin/manet-uplink-dispatch.sh"
	if !fileExists(path) {
		return
	}
	out, err := exec.Command(path, "reconcile").CombinedOutput()
	if err != nil {
		log.Printf("uplink-dispatch reconcile: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func loadConf(key string) string {
	data, err := os.ReadFile(confFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return strings.Trim(v, "'\"")
		}
	}
	return ""
}

func ifaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

func meshIfaces() (iface24, iface5 string) {
	data, err := os.ReadFile(meshIfFile)
	if err != nil {
		if ifaceExists("wlan0") {
			return "wlan0", ""
		}
		return "", ""
	}
	lines := strings.Fields(strings.TrimSpace(string(data)))
	if len(lines) > 0 && ifaceExists(lines[0]) {
		iface24 = lines[0]
	}
	if len(lines) > 1 && ifaceExists(lines[1]) {
		iface5 = lines[1]
	}
	return
}

func radioIfaceEnabled(iface string) bool {
	data, err := os.ReadFile(radioStateFile)
	if err != nil {
		return true
	}
	var state map[string]interface{}
	if json.Unmarshal(data, &state) != nil {
		return true
	}
	desired, _ := state["desired"].(map[string]interface{})
	if desired == nil {
		return true
	}
	v, _ := desired[iface].(string)
	return v != "down"
}

func getConfFreq(confPath string) string {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "frequency" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// setIfaceFrequency rewrites confPath's frequency= line to targetFreq (if it
// isn't already there) and restarts wpa_supplicant for iface. Shared by
// static-channel enforcement and ACS's election-driven channel changes —
// the only difference between the two modes is where targetFreq comes from.
// Returns whether a change was actually made.
func setIfaceFrequency(iface, confPath, targetFreq, label string) bool {
	if iface == "" || confPath == "" {
		return false
	}
	if !fileExists(confPath) {
		log.Printf("wpa config not ready for %s: %s", label, confPath)
		return false
	}
	freq := getConfFreq(confPath)
	if freq == targetFreq {
		return false
	}

	log.Printf("setting %s to channel %s (was %s)", label, targetFreq, freq)
	data, err := os.ReadFile(confPath)
	if err != nil {
		return false
	}
	var out strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == "frequency" {
			out.WriteString("frequency=" + targetFreq + "\n")
		} else {
			out.WriteString(line + "\n")
		}
	}
	result := strings.TrimRight(out.String(), "\n") + "\n"
	if err := os.WriteFile(confPath, []byte(result), 0644); err != nil {
		return false
	}

	if radioIfaceEnabled(iface) {
		svc := "wpa_supplicant@" + iface + ".service"
		log.Printf("restarting %s", svc)
		exec.Command("systemctl", "restart", svc).Run()
		time.Sleep(5 * time.Second)
	}
	return true
}

func ensureStaticIfaceChannel(iface, confPath, staticFreq, band string) {
	setIfaceFrequency(iface, confPath, staticFreq, band)
}

func ensureStaticChannels() {
	iface24, iface5 := meshIfaces()
	if iface24 != "" {
		ensureStaticIfaceChannel(iface24, wpaConfPath(iface24), staticFreq24, "2.4 GHz")
	}
	if iface5 != "" {
		ensureStaticIfaceChannel(iface5, wpaConfPath(iface5), staticFreq5, "5 GHz")
	}
}

func wpaConfPath(iface string) string {
	return "/etc/wpa_supplicant/wpa_supplicant-" + iface + ".conf"
}

// acsCycleInterval matches upstream's scan cadence (its main loop runs
// scan/publish/registry/election on a clock-synchronized 3-minute cycle).
// node-manager's own loop ticks every 15s for its other duties (radio-state
// sync, gateway reconcile, service elections) — runACSTick is called every
// tick but only actually scans/elects once this interval has passed, so
// ACS doesn't take radios off-channel far more often than upstream does.
const acsCycleInterval = 180 * time.Second

var lastACSCycle time.Time

// runACSTick is the ACS-mode replacement for ensureStaticChannels(): scan,
// publish the result for peers (via mesh-registry picking up
// channelReportFile), aggregate self + fresh peer reports from the
// registry, elect a channel per band, and apply it. Every node runs this
// same deterministic computation independently — there is no coordinator.
func runACSTick() {
	if !lastACSCycle.IsZero() && time.Since(lastACSCycle) < acsCycleInterval {
		return
	}
	lastACSCycle = time.Now()

	iface24, iface5 := meshIfaces()
	if iface24 == "" && iface5 == "" {
		return
	}

	report := performScan(iface24, iface5)
	writeChannelReport(report)

	registry := readRegistry(registryFile)
	reports := collectFreshReports(registry, report)

	// Quorum failure means this node can't actually reach enough of the
	// mesh it believes exists — retreat to the lobby regardless of what
	// the election would otherwise have picked, so it has the best chance
	// of finding (or being found by) the rest of the mesh again.
	quorum := quorumOK(registry)

	limp := false

	if iface24 != "" {
		cur := getConfFreq(wpaConfPath(iface24))
		result := electBand(reports, band24Channels, cur, lobbyFreq24, "2.4GHz")
		freq := result.freq
		if !quorum {
			freq = lobbyFreq24
		}
		setIfaceFrequency(iface24, wpaConfPath(iface24), freq, "2.4 GHz (ACS)")
		limp = limp || result.limp
	}
	if iface5 != "" {
		cur := getConfFreq(wpaConfPath(iface5))
		result := electBand(reports, band5Channels, cur, lobbyFreq5, "5GHz")
		freq := result.freq
		if !quorum {
			freq = lobbyFreq5
		}
		setIfaceFrequency(iface5, wpaConfPath(iface5), freq, "5 GHz (ACS)")
		limp = limp || result.limp
	}

	setLimpMode(limp)
	reconcileLimpMode(registry, iface24, iface5)
}

// setLimpMode records this node's own read on RF conditions from this
// tick's election (existence of /var/run/mesh_limp_mode, same file
// mesh-registry's collectLocal() already checks for the IsLimp field, and
// what gets gossiped as the IS_IN_LIMP_MODE registry field). This is only
// this node's own signal — reconcileLimpMode (limpmode.go) is the separate
// mesh-wide consensus check that decides whether to actually throttle
// bitrates from the aggregate of everyone's signal, including this one.
func setLimpMode(limp bool) {
	const limpFile = "/var/run/mesh_limp_mode"
	if limp {
		os.WriteFile(limpFile, []byte{}, 0644)
	} else {
		os.Remove(limpFile)
	}
}

func runElections() {
	matches, _ := filepath.Glob("/usr/local/bin/*-election.sh")
	for _, script := range matches {
		base := filepath.Base(script)
		if strings.Contains(base, "channel-election") {
			continue
		}
		info, err := os.Stat(script)
		if err != nil || info.Mode()&0111 == 0 {
			continue
		}
		cmd := exec.Command(script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Printf("election %s: %v", base, err)
			continue
		}
		go func(name string, c *exec.Cmd) {
			if err := c.Wait(); err != nil {
				log.Printf("election %s: %v", name, err)
			}
		}(base, cmd)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
