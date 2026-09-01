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
	"strconv"
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

	// Must run before any width/static-channel reconcile below, at every
	// startup — this is what closes the OTA gap noscanCapable's own comment
	// describes: an OTA lands the patched binary but never runs
	// radio-setup.sh (first-provision only), so nothing else regenerates
	// the systemd drop-in wpa_supplicant@ actually needs to run it.
	ensureNoscanDropIn()

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
		ensureNoscanDropIn()
		if _, iface5 := meshIfaces(); iface5 != "" {
			reconcile5GHzWidth(iface5)
		}
		// Re-read acs from mesh.conf every tick rather than reusing the
		// startup snapshot above — acs is now a live-settable key (Config
		// UI, Fleet management, `mesh` CLI), and a cached bool here would
		// mean toggling it has zero effect until node-manager restarts.
		if loadConf("acs") == "y" {
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

// restartWpaSupplicant restarts wpa_supplicant@<iface> and gives it a fixed
// settle window — the one restart mechanism both setIfaceFrequency's
// election-driven/static-enforcement changes and the ACS self-heal's
// corrective restart (acsVerifyAfterApply, acs_selfheal.go) use, so there's
// exactly one place in this codebase issuing this systemctl call.
func restartWpaSupplicant(iface string) {
	svc := "wpa_supplicant@" + iface + ".service"
	log.Printf("restarting %s", svc)
	if err := exec.Command("systemctl", "restart", svc).Run(); err != nil {
		log.Printf("restart %s: %v", svc, err)
	}
	time.Sleep(5 * time.Second)
}

// setIfaceFrequency rewrites the frequency and restarts wpa_supplicant for
// iface — the thorough path, used by static-channel enforcement and ACS's
// election-driven channel changes (both infrequent enough to afford a full
// service restart + settle time). Tourguide's time-boxed lobby hop uses the
// lighter rewriteFrequencyLine + wpa_cli reconfigure path instead — see
// tourguide.go's hopFrequency.
//
// Returns whether the config actually changed. A false return means the
// config already matched targetFreq, so nothing restarted from this call —
// callers in both ACS and static mode follow this up with
// acsVerifyAfterApply (acs_selfheal.go) specifically to cover the case
// where the config matches but live radio state has silently diverged from
// it (a failed join, a driver hiccup, a race during some other restart).
func setIfaceFrequency(iface, confPath, targetFreq, label string) bool {
	if iface == "" {
		return false
	}
	if !rewriteFrequencyLine(confPath, targetFreq, label) {
		return false
	}

	if radioIfaceEnabled(iface) {
		restartWpaSupplicant(iface)
	}
	return true
}

// ensureStaticIfaceChannel enforces staticFreq (static/acs=n mode) and, per
// ACS.md point 6 of the verify-after-apply design, runs the same live-state
// self-heal ACS mode gets via runACSTick — a static node's config can
// silently diverge from live radio state exactly the same ways an ACS
// node's can, and it would otherwise lose this coverage entirely just
// because it never calls runACSTick.
func ensureStaticIfaceChannel(iface, confPath, staticFreq, band string) {
	configChanged := setIfaceFrequency(iface, confPath, staticFreq, band)
	acsVerifyAfterApply(iface, staticFreq, band, configChanged)
}

// Static mode permanently parks the mesh on the same frequencies ACS uses
// as its lobby/rendezvous pair (lobbyFreq24/lobbyFreq5, channel_election.go)
// — there's only one "the fixed, always-known channel" concept in this
// codebase, not two, so both modes share the same constants — except the
// 5GHz side can now be pinned to an operator-chosen channel via
// mesh_5ghz_channel; see desiredMesh5GHzChannel.
func ensureStaticChannels() {
	iface24, iface5 := meshIfaces()
	if iface24 != "" {
		ensureStaticIfaceChannel(iface24, wpaConfPath(iface24), lobbyFreq24, "2.4 GHz")
	}
	if iface5 != "" {
		ensureStaticIfaceChannel(iface5, wpaConfPath(iface5), desiredMesh5GHzChannel(), "5 GHz")
	}
}

// mesh5GHzChannelKey is the fleet-wide mesh.conf key pinning the 5GHz
// static-mode (acs=n) data channel to a specific channel *number* —
// matching the existing lan_ap_channel/halow_channel convention (a
// human-readable channel number, not MHz), not a fleet-wide toggle like
// mesh5GHzWidthKey.
const mesh5GHzChannelKey = "mesh_5ghz_channel"

var loggedMesh5GHzChannelFallback bool

// freqForBand5ChannelNum translates a 5GHz WiFi channel *number* to its
// MHz frequency, validated only against band5Channels (the full known
// 5GHz candidate superset) plus lobbyFreq5's own channel number — not
// against activeBand5Channels. activeBand5Channels is derived far more
// often here than in ACS mode (ensureStaticChannels runs every 15s tick,
// not gated to the 180s ACS cycle), and even though it now fails open on
// a transient `iw` error rather than closed, it can still legitimately
// return a phy-filtered subset — validating against that narrower set
// here would reject an operator-picked static channel the phy actually
// supports just because this tick's live filter happened to exclude it.
// Phy-legality is already independently guarded downstream, at the right
// layer: ensureStaticIfaceChannel calls acsVerifyAfterApply, which
// consults freqAvailableOnPhy before ever firing a corrective restart.
//
// Deliberately 5GHz-only, not tourguide.go's wifiChannelFreq — that
// checks band24Channels first and could misresolve a 5GHz-only channel
// number that happens to collide with a 2.4GHz channel number's mapping.
func freqForBand5ChannelNum(channelNum string) (string, bool) {
	for _, freq := range band5Channels {
		if strconv.Itoa(wifiFreqToChannelNum(freq)) == channelNum {
			return strconv.Itoa(freq), true
		}
	}
	if lobbyFreqInt, err := strconv.Atoi(lobbyFreq5); err == nil {
		if strconv.Itoa(wifiFreqToChannelNum(lobbyFreqInt)) == channelNum {
			return lobbyFreq5, true
		}
	}
	return "", false
}

// desiredMesh5GHzChannel reads mesh_5ghz_channel from mesh.conf (a channel
// number, e.g. "44") and resolves it to an MHz frequency for
// ensureStaticChannels' 5GHz static-mode call site. Absent, non-numeric,
// or not a member of band5Channels/not equal to lobbyFreq5's own channel
// number resolves to lobbyFreq5 — matching desiredMeshWidth's own stated
// "absent or unrecognized resolves to the safe default" convention.
// Logged once per process (not every 15s tick) so a standing
// misconfiguration doesn't spam the log.
func desiredMesh5GHzChannel() string {
	raw := loadConf(mesh5GHzChannelKey)
	if raw == "" {
		return lobbyFreq5
	}
	if freq, ok := freqForBand5ChannelNum(raw); ok {
		return freq
	}
	if !loggedMesh5GHzChannelFallback {
		log.Printf("mesh_5ghz_channel=%q not a valid 5GHz channel, using lobby channel", raw)
		loggedMesh5GHzChannelFallback = true
	}
	return lobbyFreq5
}

func wpaConfPath(iface string) string {
	return "/etc/wpa_supplicant/wpa_supplicant-" + iface + ".conf"
}

func wpaLobbyConfPath(iface string) string {
	return "/etc/wpa_supplicant/wpa_supplicant-" + iface + "-lobby.conf"
}

// mesh5GHzWidthKey is the fleet-wide mesh.conf toggle for 5GHz mesh
// channel width. See ACS.md "Decision: 20MHz-only 5GHz mesh" and "Mixed-
// width peering" — this must never be mixed per-node in normal operation,
// hence one key applied identically across the fleet, not a per-radio
// override.
const mesh5GHzWidthKey = "mesh_5ghz_bw"

// patchedWpaSupplicantPath is this project's own build of wpa_supplicant
// with the mesh `noscan` patch (MANET/src/wpa-supplicant-mesh/,
// docs/wpa-supplicant-mesh-noscan.md), installed alongside — never over —
// the system wpa_supplicant. manet-ctrl/collect.go checks this exact same
// path for its own fault-visibility signal; kept in sync by convention/
// naming, not by import (separate Go modules/binaries, no shared package).
const patchedWpaSupplicantPath = "/usr/sbin/wpa_supplicant_mesh"

// noscanDropInPath is the systemd drop-in that actually points
// wpa_supplicant@<iface> at patchedWpaSupplicantPath. ensureNoscanDropIn
// (below) is what creates/removes it — this must stay in sync with the
// binary's presence on every boot, not just at first provision.
const noscanDropInPath = "/etc/systemd/system/wpa_supplicant@.service.d/20-mesh-binary.conf"

const noscanDropInContent = "[Service]\nExecStart=\nExecStart=" + patchedWpaSupplicantPath +
	" -c/etc/wpa_supplicant/wpa_supplicant-%I.conf -i%I\n"

// noscanCapable reports whether this node has the patched wpa_supplicant
// BOTH installed and actually wired into the unit that runs it. This is the
// ONLY signal permitted to gate writing noscan=1 (or any other patch-added
// key) into a mesh wpa_supplicant conf file — no binary-content scanning,
// no version parsing. The stock system wpa_supplicant fails to parse an
// entire network={} block on an unrecognized key and exits (status=255),
// dropping that radio out of the mesh — see
// docs/wpa-supplicant-mesh-noscan.md for the incident this guards against.
//
// Checking only the binary's presence was a real, reviewed-and-caught bug:
// radio-setup.sh (which originally generated noscanDropInPath) only runs at
// first provision, never on an OTA software update — an existing fleet node
// taking an update gets the binary but keeps running wpa_supplicant@ under
// the OLD unit (still pointed at stock) until something regenerates the
// drop-in. ensureNoscanDropIn below is that "something" (called once at
// startup and once per loop tick, so it self-heals within one 15s tick of
// either the binary or the drop-in changing state), but this check also
// verifies the drop-in directly rather than trusting ensureNoscanDropIn ran
// first — no ordering assumption, no window where a true binary-presence
// read could gate a write before the unit is actually wired to use it.
func noscanCapable() bool {
	info, err := os.Stat(patchedWpaSupplicantPath)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return false
	}
	dropIn, err := os.ReadFile(noscanDropInPath)
	if err != nil {
		return false
	}
	return string(dropIn) == noscanDropInContent
}

// ensureNoscanDropIn keeps noscanDropInPath in sync with
// patchedWpaSupplicantPath's presence — the fix for the OTA gap described
// on noscanCapable above. Idempotent and cheap (a stat plus, on the common
// no-change path, one more stat/read); safe to call every loop tick, not
// just at startup, so it also self-heals if the binary is later removed.
// Only ever writes/removes this one drop-in file — never touches
// 10-mesh-prep.conf (radio-setup.sh's own drop-in in the same directory)
// or any wpa_supplicant conf.
func ensureNoscanDropIn() {
	capableNow := false
	if info, err := os.Stat(patchedWpaSupplicantPath); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
		capableNow = true
	}

	existing, readErr := os.ReadFile(noscanDropInPath)
	upToDate := readErr == nil && string(existing) == noscanDropInContent

	if capableNow == upToDate {
		return
	}

	if capableNow {
		if err := os.MkdirAll(filepath.Dir(noscanDropInPath), 0755); err != nil {
			log.Printf("[acs] mkdir %s: %v", filepath.Dir(noscanDropInPath), err)
			return
		}
		if err := os.WriteFile(noscanDropInPath, []byte(noscanDropInContent), 0644); err != nil {
			log.Printf("[acs] write %s: %v", noscanDropInPath, err)
			return
		}
		log.Printf("[acs] wpa_supplicant_mesh present, wired %s", noscanDropInPath)
	} else {
		if err := os.Remove(noscanDropInPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[acs] remove %s: %v", noscanDropInPath, err)
			return
		}
		log.Printf("[acs] wpa_supplicant_mesh absent, removed %s", noscanDropInPath)
	}

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Printf("[acs] daemon-reload after noscan drop-in change: %v", err)
	}
}

// desiredMeshWidth reads mesh_5ghz_bw from mesh.conf. Absent, empty, or
// anything other than "40"/"80" resolves to "20" — deliberate
// default-to-safe (deterministic, lower-throughput 20MHz link), not
// default-to-legacy (non-deterministic VHT80). Matches the lan_ap_bw-style
// "default read with fallback" convention used elsewhere in this
// codebase's shell scripts.
//
// "40" additionally requires noscanCapable() — the patched wpa_supplicant.
// Without it, HT40 shares the identical coex-scan nondeterminism VHT80
// has (docs/wpa-supplicant-mesh-noscan.md section 1: "40MHz does not
// dodge the bug"), so a node lacking the fix resolves to 20, never "40
// without noscan" — that would silently reintroduce the exact HT40
// coex-scan nondeterminism this project already fixed, just under a new
// config value.
func desiredMeshWidth() string {
	switch loadConf(mesh5GHzWidthKey) {
	case "40":
		if noscanCapable() {
			return "40"
		}
		return "20"
	case "80":
		return "80"
	default:
		return "20"
	}
}

// meshWidthKeyNames enumerates every network={} block key setMeshWidthKeys
// manages, in the fixed order they're written when added. Order only
// affects output layout, not correctness.
var meshWidthKeyNames = []string{"disable_ht40=1", "disable_vht=1", "noscan=1", "max_oper_chwidth=1"}

// meshWidthKeys is the single source of truth setMeshWidthKeys reconciles
// a conf file's network={} block against: the exact key set wanted for
// desiredWidth ("20", "40", or "80") given whether this node has the
// patched wpa_supplicant (capable).
//
// "40" is only ever passed in already gated by desiredMeshWidth's own
// capability check above (it never returns "40" when !capable) — but "80"
// without capable is a real, reachable case, and deliberately keeps
// today's existing behavior (no disable_* keys, no noscan/
// max_oper_chwidth either): that's the already-documented, already-UI-
// warned-about VHT80 nondeterminism risk, not something this change needs
// to newly protect against. "20" (default/fallback) stays byte-identical
// to pre-mesh_5ghz_bw=40 behavior regardless of capable.
func meshWidthKeys(desiredWidth string, capable bool) map[string]bool {
	switch desiredWidth {
	case "40":
		if capable {
			return map[string]bool{"noscan=1": true, "disable_vht=1": true}
		}
		return map[string]bool{"disable_ht40=1": true, "disable_vht=1": true}
	case "80":
		if capable {
			return map[string]bool{"noscan=1": true, "max_oper_chwidth=1": true}
		}
		return map[string]bool{}
	default: // "20", and any unrecognized value
		return map[string]bool{"disable_ht40=1": true, "disable_vht=1": true}
	}
}

func isMeshWidthKey(trimmed string) bool {
	for _, key := range meshWidthKeyNames {
		if trimmed == key {
			return true
		}
	}
	return false
}

// setMeshWidthKeys makes path's network={} block match exactly the key set
// meshWidthKeys(desiredWidth, capable) wants: adds whichever of
// {disable_ht40, disable_vht, noscan, max_oper_chwidth} are wanted and
// missing, removes whichever are present and not wanted. Returns whether
// the file was actually changed. A no-op (false) if the file doesn't exist
// or already matches — this is a plain compare-and-fix, not a template
// rewrite.
func setMeshWidthKeys(path, desiredWidth string, capable bool) bool {
	if !fileExists(path) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[acs] reading %s: %v", path, err)
		return false
	}

	want := meshWidthKeys(desiredWidth, capable)

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines)+len(meshWidthKeyNames))
	inNetwork := false
	present := make(map[string]bool, len(meshWidthKeyNames))
	changed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "network={":
			inNetwork = true
			out = append(out, line)
			continue
		case inNetwork && trimmed == "}":
			for _, key := range meshWidthKeyNames {
				if want[key] && !present[key] {
					out = append(out, "    "+key)
					changed = true
				}
			}
			inNetwork = false
			out = append(out, line)
			continue
		}
		if inNetwork && isMeshWidthKey(trimmed) {
			present[trimmed] = true
			if !want[trimmed] {
				changed = true
				continue // drop the line
			}
		}
		out = append(out, line)
	}

	if !changed {
		return false
	}

	result := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	if err := os.WriteFile(path, []byte(result), 0644); err != nil {
		log.Printf("[acs] writing %s: %v", path, err)
		return false
	}
	return true
}

// reconcile5GHzWidth keeps iface's 5GHz mesh wpa_supplicant config —
// both the live conf and its -lobby.conf (mesh-boot-lobby.service
// re-copies the lobby conf over the live one on every boot, so both need
// the fix to survive a reboot) — in sync with the fleet-wide
// mesh_5ghz_bw setting. Idempotent: a fast no-op read-and-compare once
// both files already match, one wpa_supplicant restart only when
// something actually changed. Not a rate-limited retry loop like
// setIfaceFrequency's ACS self-heal — there's no failure mode here to
// back off from, just "does the config match, if not fix it once."
func reconcile5GHzWidth(iface string) {
	if iface == "" {
		return
	}
	// meshIfaces() identifies iface purely by its position in /var/lib/mesh_if
	// (line 0 = 2.4GHz, line 1 = 5GHz), with no band cross-check -- unlike
	// this same reconciler's shell counterparts (radio-setup.sh,
	// manet-wlan-reconcile.sh), which both gate their WIDTH_LINES on
	// `FREQ -ge 5000`. Before noscan existed, a mis-ordered mesh_if writing
	// disable_ht40/disable_vht into a 2.4GHz conf was a harmless no-op; now
	// it would write noscan=1 (which the mesh-noscan patch's own table
	// extends to enable 2.4GHz HT40 with the coex scan disabled on exactly
	// the band where that scan is a real requirement, not a workaround).
	// Cross-check the conf's actual frequency before doing anything.
	if freq := getConfFreq(wpaConfPath(iface)); freq != "" {
		if f, err := strconv.Atoi(freq); err != nil || f < 5000 {
			return
		}
	}
	targetWidth := desiredMeshWidth()
	capable := noscanCapable()

	liveChanged := setMeshWidthKeys(wpaConfPath(iface), targetWidth, capable)
	lobbyChanged := setMeshWidthKeys(wpaLobbyConfPath(iface), targetWidth, capable)
	if !liveChanged && !lobbyChanged {
		return
	}

	target := targetWidth + "MHz"
	switch {
	case targetWidth == "20":
		target += " (disable_ht40+disable_vht)"
	case targetWidth == "40" && capable:
		target += " (HT40, noscan+disable_vht)"
	case targetWidth == "80" && capable:
		target += " (VHT80, noscan+max_oper_chwidth)"
	case targetWidth == "80":
		target += " (VHT80, no noscan — patched wpa_supplicant not present)"
	}
	log.Printf("[acs] %s 5GHz mesh width now targets %s, restarting wpa_supplicant", iface, target)

	if !radioIfaceEnabled(iface) {
		return
	}
	svc := "wpa_supplicant@" + iface + ".service"
	if err := exec.Command("systemctl", "restart", svc).Run(); err != nil {
		log.Printf("[acs] restart %s: %v", svc, err)
	}
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

	// Computed once per tick and reused for both scanning and electBand's
	// candidate list below (rather than recomputed separately for each),
	// so both operate against the exact same phy-filtered candidate set —
	// see activeBand5Channels (scan.go) for why this is derived live.
	candidates5 := activeBand5Channels(iface5)

	report := performScan(iface24, iface5, candidates5)
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
	coldStart := false

	if iface24 != "" {
		cur := getConfFreq(wpaConfPath(iface24))
		result := electBand(reports, registry, band24Channels, cur, lobbyFreq24, "2.4GHz")
		freq := result.freq
		if !quorum {
			freq = lobbyFreq24
		}
		// The self-heal check must run immediately here, between this call
		// and maybeRunTourguide below — never moved to the end of the tick
		// or into the 15s loop closure. maybeRunTourguide can hop this same
		// radio off-channel for up to tourguideDwell (~12s); running the
		// check any later would see that expected, temporary lobby hop and
		// false-trigger a corrective restart on top of it. See ACS.md's
		// validated design, point 2.
		configChanged := setIfaceFrequency(iface24, wpaConfPath(iface24), freq, "2.4 GHz (ACS)")
		acsVerifyAfterApply(iface24, freq, "2.4GHz", configChanged)
		acsTrackHold(iface24, result, "2.4GHz")
		limp = limp || result.limp
		coldStart = coldStart || result.coldStart
	}
	if iface5 != "" {
		cur := getConfFreq(wpaConfPath(iface5))
		result := electBand(reports, registry, candidates5, cur, lobbyFreq5, "5GHz")
		freq := result.freq
		if !quorum {
			freq = lobbyFreq5
		}
		// Same ordering constraint as the 2.4GHz block above — must stay
		// immediately after this setIfaceFrequency call.
		configChanged := setIfaceFrequency(iface5, wpaConfPath(iface5), freq, "5 GHz (ACS)")
		acsVerifyAfterApply(iface5, freq, "5GHz", configChanged)
		acsTrackHold(iface5, result, "5GHz")
		limp = limp || result.limp
		coldStart = coldStart || result.coldStart
	}

	if coldStart {
		// At least one band came back with no peer votes yet. The
		// acsCycleInterval throttle above exists to stop a *converged*
		// mesh from rescanning/re-electing too often — it was never meant
		// to make a node that has nothing elected yet wait a full 180s
		// between attempts. Rewinding lastACSCycle to zero here means the
		// very next 15s loopInterval tick (main, above) retries the real
		// election immediately instead of waiting for the next full
		// cycle — hardware-verified (2026-08-30, EUD3/EUD4 reboot test)
		// that without this, a cold-start hold's own gate turns "wait for
		// a peer vote" into "wait up to 180s for a peer vote", reproducing
		// the original 3.5-4 minute outage this fix lineage exists to
		// eliminate.
		lastACSCycle = time.Time{}
	}

	setLimpMode(limp)

	writePartitionSize(originators)
	if quorum && !coldStart {
		// Tourguide duty means briefly hopping off the data channel this
		// node already just fought to defend — pointless (and disruptive)
		// on a cycle where quorum already forced a retreat to lobby.
		// Likewise skipped while coldStart is fast-retrying (above): its
		// own radio is already sitting at the lobby waiting for a vote,
		// and dwelling there via tourguide too doesn't make that vote
		// arrive any sooner — it would just risk yanking this node's
		// *other*, already-working band to the lobby every ~15s while
		// that band's gossip link is exactly what the cold-starting band
		// is waiting on.
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
