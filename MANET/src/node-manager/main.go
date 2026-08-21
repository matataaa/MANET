package main

import (
	"encoding/json"
	"fmt"
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
)

var Version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(Version)
		return
	}

	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("[node-manager] ")
	log.Printf("starting (version %s)", Version)

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

// rewriteFrequencyLine rewrites confPath's frequency= line to targetFreq,
// if it isn't already there. Doesn't touch wpa_supplicant itself — callers
// decide how to make the running process pick it up (a full restart is
// thorough but slow; wpa_cli reconfigure is faster but lighter-weight).
// Returns whether a change was actually made.
func rewriteFrequencyLine(confPath, targetFreq, label string) bool {
	if confPath == "" || !fileExists(confPath) {
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
	return true
}

// setIfaceFrequency rewrites the frequency and restarts wpa_supplicant for
// iface — the thorough path, used by static-channel enforcement and ACS's
// election-driven channel changes (both infrequent enough to afford a full
// service restart + settle time). Tourguide's time-boxed lobby hop uses the
// lighter rewriteFrequencyLine + wpa_cli reconfigure path instead — see
// tourguide.go's hopFrequency.
func setIfaceFrequency(iface, confPath, targetFreq, label string) bool {
	if iface == "" {
		return false
	}
	if !rewriteFrequencyLine(confPath, targetFreq, label) {
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

// Static mode permanently parks the mesh on the same frequencies ACS uses
// as its lobby/rendezvous pair (lobbyFreq24/lobbyFreq5, channel_election.go)
// — there's only one "the fixed, always-known channel" concept in this
// codebase, not two, so both modes share the same constants.
func ensureStaticChannels() {
	iface24, iface5 := meshIfaces()
	if iface24 != "" {
		ensureStaticIfaceChannel(iface24, wpaConfPath(iface24), lobbyFreq24, "2.4 GHz")
	}
	if iface5 != "" {
		ensureStaticIfaceChannel(iface5, wpaConfPath(iface5), lobbyFreq5, "5 GHz")
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

// runACSTick is the ACS-mode replacement for ensureStaticChannels(), and
// the top-level orchestration for every ACS piece: scan, publish the
// result for peers (via mesh-registry picking up channelReportFile),
// aggregate self + fresh peer reports from the registry, elect a channel
// per band (channel_election.go), override to the lobby frequency if
// quorum says this node is isolated (quorum.go), reconcile mesh-wide
// limp-mode consensus (limpmode.go), and — if elected and quorum holds —
// take a tourguide turn to look for a foreign partition to merge with
// (tourguide.go). Every node runs this same deterministic computation
// independently — there is no coordinator.
func runACSTick() {
	if !lastACSCycle.IsZero() && time.Since(lastACSCycle) < acsCycleInterval {
		return
	}

	iface24, iface5 := meshIfaces()
	if iface24 == "" && iface5 == "" {
		return
	}

	selfMAC := myRegistryMAC()
	if selfMAC == "" {
		// bat0/br0 not up yet (early boot, racing batman-enslave — a real
		// startup condition, not hypothetical). Running the election under
		// a blank identity would fail to exclude our own registry entry
		// from peer aggregation and could let this node win elections
		// (tourguide, eventually service placement) under a bogus
		// identity — better to just wait for the next cycle once the
		// interface exists. Deliberately NOT stamping lastACSCycle here:
		// doing so before this guard used to mean the very first call
		// (from main(), before bat0 is up) would still arm the 3-minute
		// cycle gate despite doing nothing, silently delaying the actual
		// first scan/election by up to 3 minutes on every cold boot.
		log.Println("[acs] no bat0/br0 MAC yet, skipping this cycle")
		return
	}

	lastACSCycle = time.Now()

	report := performScan(iface24, iface5)
	writeChannelReport(report)

	registry := readRegistry(registryFile)
	reports := collectFreshReports(registry, report)

	// Count batman-adv originators once, up front, and reuse it everywhere
	// this cycle needs a "how big is my partition" answer (quorum, the
	// gossiped partition size, and tourguide's merge decision). Computing
	// it separately in each place meant up to three inconsistent snapshots
	// per cycle — and the one a partition-merge decision used was taken
	// ~12+ seconds later, after this node's own tourguide radio had
	// already hopped off to the lobby channel, undercounting reachability
	// through it.
	originators := uniqueBatmanOriginators()

	// Quorum failure means this node can't actually reach enough of the
	// mesh it believes exists — retreat to the lobby regardless of what
	// the election would otherwise have picked, so it has the best chance
	// of finding (or being found by) the rest of the mesh again.
	quorum := quorumOK(registry, originators)

	limp := false

	if iface24 != "" {
		cur := getConfFreq(wpaConfPath(iface24))
		result := electBand(reports, registry, band24Channels, cur, lobbyFreq24, "2.4GHz")
		freq := result.freq
		if !quorum {
			freq = lobbyFreq24
		}
		setIfaceFrequency(iface24, wpaConfPath(iface24), freq, "2.4 GHz (ACS)")
		limp = limp || result.limp
	}
	if iface5 != "" {
		cur := getConfFreq(wpaConfPath(iface5))
		result := electBand(reports, registry, band5Channels, cur, lobbyFreq5, "5GHz")
		freq := result.freq
		if !quorum {
			freq = lobbyFreq5
		}
		setIfaceFrequency(iface5, wpaConfPath(iface5), freq, "5 GHz (ACS)")
		limp = limp || result.limp
	}

	setLimpMode(limp)

	writePartitionSize(originators)
	if quorum {
		// Tourguide duty means briefly hopping off the data channel this
		// node already just fought to defend — pointless (and disruptive)
		// on a cycle where quorum already forced a retreat to lobby.
		//
		// Must run before reconcileLimpMode: tourguide's return-to-data
		// hop unconditionally clears that radio's bitrate limit (it
		// doesn't know whether mesh-wide limp mode is active), so
		// reconcileLimpMode needs to run after it in the same cycle to
		// re-throttle immediately if consensus still says limp — matching
		// upstream's own stage order (tourguide, then limp-mode
		// management). Running it the other way around would leave that
		// radio un-throttled for up to a full ACS cycle.
		maybeRunTourguide(registry, selfMAC, iface24, iface5, originators+1)
	}

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
