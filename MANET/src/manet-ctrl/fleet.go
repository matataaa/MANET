package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fleetMcastAddr  = "239.30.2.70:17070"
	fleetMcastIface = "br0"
)

var (
	fleetAcksMu sync.Mutex
	fleetAcks   = map[string]string{} // mac -> version
)

func fleetConfigWatcher() {
	for {
		time.Sleep(10 * time.Second)
		fleetPollAlfred()
		fleetCheckActivation()
		fleetPollUpdateAlfred()
	}
}

func fleetCheckActivation() {
	raw := getPendingConfig()
	if raw == nil {
		return
	}
	var pkg map[string]interface{}
	if json.Unmarshal(raw, &pkg) != nil {
		return
	}
	at, ok := pkg["activate_at"].(float64)
	if !ok || at == 0 {
		return
	}
	if time.Now().Unix() < int64(at) {
		return
	}

	pkgID, _ := pkg["pkg_id"].(string)
	version, _ := pkg["version"].(string)
	stagedAt, _ := pkg["staged_at"].(float64)
	if pkgID == "" {
		log.Printf("fleet: pending config has no pkg_id, refusing to apply (stale/legacy package?)")
		clearPendingConfig()
		return
	}
	if isPkgIDApplied(pkgID) {
		log.Printf("fleet: pkg_id %s (version %s) already applied, skipping re-apply — replay?", pkgID, version)
		clearPendingConfig()
		return
	}
	// Record BEFORE apply, not after: a crash mid-apply must never be able
	// to replay this exact package again on the next boot.
	if err := recordPkgIDApplied(pkgID, version, int64(stagedAt)); err != nil {
		log.Printf("fleet: failed to record pkg_id %s as applied, refusing to apply: %v", pkgID, err)
		return
	}

	log.Printf("fleet: activation time reached, applying config (pkg_id=%s)", pkgID)
	fleetApplyConfig(pkg)
	clearPendingConfig()
	log.Printf("fleet: config applied and pending cleared")
}

// expandNodeTemplates replaces {{hostname}} in staged config values with this
// node's current hostname prefix, so one fleet profile can be deployed to
// every node. The fleet UI has advertised this placeholder since the start,
// but nothing expanded it — the literal braces landed in mesh.conf and the
// hostname-apply path fell back to the "node" default.
func expandNodeTemplates(updates map[string]string, conf map[string]string) {
	prefix := conf["node_hostname"]
	if prefix == "" {
		// Derive the prefix from the OS hostname by stripping the
		// generated -<ssid>-<mac> suffix.
		host, _ := os.Hostname()
		if suffix := getMACsuffix(); suffix != "" {
			host = strings.TrimSuffix(host, "-"+suffix)
		}
		if ssid := conf["mesh_ssid"]; ssid != "" {
			host = strings.TrimSuffix(host, "-"+ssid)
		}
		prefix = host
	}
	for k, v := range updates {
		if strings.Contains(v, "{{hostname}}") {
			updates[k] = strings.ReplaceAll(v, "{{hostname}}", prefix)
		}
	}
}

// fleetLocalIdentityKeys are saveableKeys that must NEVER be applied
// literally from a fleet push, regardless of value -- they describe a
// receiving node's own local identity, not shared fleet state.
// node_hostname is the prototypical (and currently only) case: the fleet
// UI's normal "load current config -> edit one unrelated field -> save"
// flow naturally round-trips the STAGING node's own current node_hostname
// value alongside whatever the operator actually meant to change. Without
// this guard, pushing ANY unrelated field (hardware-confirmed: a bare
// callsign edit was enough) silently overwrites every OTHER node's real OS
// hostname prefix with the sender's, via the setHostname() call further
// down in fleetApplyConfig -- this happened live during hardware testing
// and needed manual SSH recovery on two nodes. The {{hostname}} templating
// feature (expandNodeTemplates above) exists precisely so OTHER fields can
// reference each node's own identity without node_hostname itself ever
// needing to travel as a literal fleet-wide value -- confirming
// node_hostname was never meant to be pushed literally in the first place.
// Shaped as a list (not a single hardcoded check) so another local-identity
// field could be added here later without restructuring this function.
var fleetLocalIdentityKeys = []string{"node_hostname"}

func dropLocalIdentityKeys(updates map[string]string) {
	for _, k := range fleetLocalIdentityKeys {
		delete(updates, k)
	}
}

func fleetApplyConfig(pkg map[string]interface{}) {
	configRaw, ok := pkg["config"].(map[string]interface{})
	if !ok {
		return
	}
	updates := make(map[string]string)
	for k, v := range configRaw {
		if saveableKeys[k] {
			updates[k] = fmt.Sprintf("%v", v)
		}
	}
	// See fleetLocalIdentityKeys's doc comment: a fleet push must never
	// rename a receiving node's own hostname.
	dropLocalIdentityKeys(updates)
	// Backstop: apiAdminSave/apiAdminStage/apiFleetPreferences already strip
	// an empty secret value before it's ever persisted or broadcast (see
	// dropEmptySecrets, admin.go), but this is the function that actually
	// writes mesh.conf on every OTHER node in the fleet -- if something
	// upstream ever missed that check (a new call path, a bug), this is the
	// last line of defense against writing admin_password="" / mesh_key=""
	// fleet-wide.
	dropEmptySecrets(updates)
	if len(updates) == 0 {
		return
	}

	existingConf := loadKVFile(MeshConfFile)

	// halow_channel and halow_bw must be persisted as a coupled, valid pair
	// or not persisted at all -- a fleet push can span nodes on different
	// domains/hardware, so a combination valid on the node that staged the
	// config is not guaranteed valid here. Validating only AFTER the write
	// below (as applyFleetHalowBW does further down, for the operational
	// apply/restart step) still leaves an invalid combination sitting in
	// mesh.conf, silently, with only a journalctl line to reveal it. Catch
	// it BEFORE the write instead: if the resulting pair is invalid for
	// this node's own resolved domain, drop both keys from updates so this
	// node's existing, already-working pair is left untouched -- silently,
	// by design, matching the fleet-apply convention of skip-not-abort.
	_, bwInUpdate := updates["halow_bw"]
	_, chInUpdate := updates["halow_channel"]
	if bwInUpdate || chInUpdate {
		effective := make(map[string]string, len(existingConf)+len(updates))
		for k, v := range existingConf {
			effective[k] = v
		}
		for k, v := range updates {
			effective[k] = v
		}
		domain := resolveHalowDomain(effective)
		if err := validateHalowChannel(domain, effectiveHalowBW(effective), effective["halow_channel"]); err != nil {
			log.Printf("fleet: dropping halow_bw/halow_channel from this node's apply, invalid for domain %q: %v", domain, err)
			delete(updates, "halow_bw")
			delete(updates, "halow_channel")
		}
	}

	// NOTE: admin_password rotation has no grace-period/fallback key here by
	// design (see fleetOpen's doc comment) — a node that misses a rotation
	// push simply can't open any subsequent fleet package until its
	// admin_password is fixed locally (SSH) to match. That's the accepted
	// manual-recovery story for a small, operator-controlled fleet, in
	// exchange for an old, possibly-leaked password never staying valid
	// indefinitely.

	expandNodeTemplates(updates, existingConf)
	if err := saveKVFile(MeshConfFile, updates); err != nil {
		log.Printf("fleet: apply save error: %v", err)
		return
	}

	conf := loadKVFile(MeshConfFile)

	// Skip when no prefix is configured — the "node" default fallback is
	// how fleet deploys renamed nodes to node-<mac>.
	if (updates["node_hostname"] != "" || updates["mesh_ssid"] != "") && conf["node_hostname"] != "" {
		prefix := conf["node_hostname"]
		meshSSID := conf["mesh_ssid"]
		macSuffix := getMACsuffix()
		full := prefix
		if meshSSID != "" {
			full += "-" + meshSSID
		}
		if macSuffix != "" {
			full += "-" + macSuffix
		}
		setHostname(full)
	}
	if updates["gateway"] != "" || updates["gateway_nat"] != "" || updates["gateway_mss_clamp"] != "" || updates["gateway_bandwidth"] != "" {
		runCmd(5*time.Second, "systemctl", "reload", "gateway-manager")
	}
	if (updates["lan_ap_ssid"] != "" || updates["lan_ap_key"] != "") && eudWantsAP(conf["eud"]) {
		applyHostapdConfig(conf)
		runCmd(10*time.Second, "systemctl", "restart", "hostapd")
	}
	// Narrow trigger, mirroring mesh_5ghz_channel's guard in apiAdminSave:
	// a fleet push resends every field every time, not just changed ones,
	// so without the != existingConf comparison this restarts wpa_supplicant
	// on every mesh radio (tearing down every plink) on any unrelated push.
	if (updates["mesh_ssid"] != "" && updates["mesh_ssid"] != existingConf["mesh_ssid"]) ||
		(updates["mesh_key"] != "" && updates["mesh_key"] != existingConf["mesh_key"]) {
		applyWPAConfig(conf)
	}
	if updates["multicast_mode"] != "" {
		applyMulticastMode(updates["multicast_mode"])
	}
	if updates["voice_mic_volume"] != "" || updates["voice_speaker_volume"] != "" {
		applyVoiceVolume(conf)
	}
	if updates["voice_enabled"] != "" {
		if conf["voice_enabled"] == "n" {
			runCmd(5*time.Second, "systemctl", "stop", "mesh-voice")
		} else {
			runCmd(5*time.Second, "systemctl", "restart", "mesh-voice")
		}
	}
	if conf["voice_enabled"] != "n" && (updates["voice_ptt_mode"] != "" || updates["voice_channel"] != "") {
		txCh := int(voiceTxCh.Load())
		if txCh <= 0 {
			txCh = 1
		}
		voiceRestartDaemon(txCh)
	}
	if updates["dns_servers"] != "" {
		applyDNSServers(updates["dns_servers"])
	}
	if (updates["lan_ap_channel"] != "" || updates["lan_ap_bw"] != "") && eudWantsAP(conf["eud"]) {
		runCmd(10*time.Second, "systemctl", "restart", "hostapd")
	}
	if updates["qos_enabled"] != "" || updates["qos_voice_band"] != "" || updates["qos_cot_band"] != "" || updates["qos_chat_band"] != "" {
		applyQoSFromConf(conf)
	}
	_, bwChanged := updates["halow_bw"]
	_, chChanged := updates["halow_channel"]
	if bwChanged || chChanged {
		applyFleetHalowBW(conf)
	}
	if _, ch5Changed := updates["mesh_5ghz_channel"]; ch5Changed {
		applyFleetMesh5GHzChannel(conf)
	}
	// Mirrors apiAdminSave's gps block (api.go) — a stop/restart-only
	// toggle here looks like it worked but silently reverts on the node's
	// next reboot, since radio-setup.sh only sets gpsd's boot-enabled
	// state once at first provisioning.
	if updates["gps"] != "" || updates["gps_source"] != "" {
		if conf["gps"] == "n" {
			runCmd(5*time.Second, "systemctl", "disable", "--now", "gps-reader")
			// gpsd.socket must be disabled too — see api.go's matching
			// block for why (socket activation silently respawns gpsd
			// otherwise).
			runCmd(5*time.Second, "systemctl", "disable", "--now", "gpsd.socket")
			runCmd(5*time.Second, "systemctl", "disable", "--now", "gpsd")
		} else if conf["gps_source"] == "static" {
			runCmd(5*time.Second, "systemctl", "disable", "--now", "gpsd.socket")
			runCmd(5*time.Second, "systemctl", "disable", "--now", "gpsd")
			runCmd(5*time.Second, "systemctl", "enable", "--now", "gps-reader")
		} else {
			if _, err := exec.LookPath("gpsd"); err != nil {
				runCmd(60*time.Second, "apt-get", "install", "-y", "gpsd", "gpsd-clients")
			}
			runCmd(5*time.Second, "systemctl", "enable", "--now", "gpsd.socket")
			runCmd(5*time.Second, "systemctl", "enable", "--now", "gpsd")
			runCmd(5*time.Second, "systemctl", "enable", "--now", "gps-reader")
		}
	}
}

// applyFleetHalowBW validates halow_bw/halow_channel against the resolved
// domain in `conf`, which is read AFTER fleetApplyConfig's saveKVFile call --
// so this validates against the domain that results from this push, not a
// pre-existing per-node domain that might genuinely differ from what was
// just written. In practice regulatory_domain/halow_regulatory_domain are
// themselves network-wide fields that fleet.js always collects and pushes
// alongside every save, so by the time this runs every node in the fleet
// already has the identical newly-pushed domain -- there is no surviving
// cross-node divergence left to detect for THIS push.
//
// This is now a defensive re-check, not the primary gate: fleetApplyConfig
// already validates and drops an invalid halow_bw/halow_channel pair BEFORE
// persisting it (so mesh.conf never ends up holding a mismatched pair in
// the first place), and only calls this function at all when that pair
// survived the pre-write filter -- so by the time we get here the
// combination should already be valid. Kept as a cheap belt-and-suspenders
// check rather than removed. Unlike apiAdminSave (which rejects the whole
// save before it is persisted), a genuinely-invalid combination reaching
// this point is logged and the operational apply is skipped rather than
// applied, leaving this node's current working HaLow config running.
func applyFleetHalowBW(conf map[string]string) {
	domain := resolveHalowDomain(conf)
	if err := validateHalowChannel(domain, effectiveHalowBW(conf), conf["halow_channel"]); err != nil {
		log.Printf("fleet: skipping halow_bw/halow_channel apply, invalid for this node's domain %q: %v", domain, err)
		return
	}
	applyHalowBW(conf)
}

// applyFleetMesh5GHzChannel validates mesh_5ghz_channel against the resolved
// domain in `conf`, which — like applyFleetHalowBW above — is read after the
// fleet push has already been written, so this checks the channel just
// pushed against the domain just pushed alongside it, not a genuinely
// surviving per-node divergence (see applyFleetHalowBW's comment for why
// that divergence doesn't survive a network-wide push). Resolves the domain via
// resolveMesh5GHzDomain (api.go) -- deliberately not resolveHalowDomain/
// halow_regulatory_domain, since HaLow and 5GHz WiFi can run different
// domains on the same node. Unlike applyFleetHalowBW there is no
// restart/apply step for mesh_5ghz_channel to skip -- node-manager reads it
// straight from mesh.conf on its own live 15s tick and already falls back
// to the default lobby channel for a value it doesn't recognize -- so this
// is a log-only guardrail for operator visibility: it never errors/aborts
// the fleet push, and it leaves the pushed value in mesh.conf untouched for
// node-manager's own fallback to handle.
func applyFleetMesh5GHzChannel(conf map[string]string) {
	ch := conf["mesh_5ghz_channel"]
	if ch == "" {
		return
	}
	domain := resolveMesh5GHzDomain(conf)
	if err := validateMesh5GHzChannel(domain, ch); err != nil {
		log.Printf("fleet: mesh_5ghz_channel invalid for this node's domain %q, node-manager will fall back to its default: %v", domain, err)
	}
}

func fleetPollAlfred() {
	out, err := exec.Command("/usr/sbin/alfred", "-r", "70").Output()
	if err != nil || len(out) == 0 {
		return
	}
	conf := loadKVFile(MeshConfFile)
	password := conf["admin_password"]
	if password == "" {
		log.Printf("fleet: admin_password is empty, refusing to process slot 70 packages")
		return
	}
	if best := parseAlfredBest(out, getMyMAC(), "70", "staged_at", password, conf["mesh_ssid"]); best != nil {
		fleetProcessPackage(best)
	}
}

// parseAlfredBest scans `alfred -r <slot>` output — one line per node:
// { "mac", "envelope_json" }, where envelope_json is now a fully sealed v2
// envelope (see fleetcrypto.go), not a plaintext package. It authenticates
// and decrypts EVERY candidate entry FIRST, discarding anything that fails to
// open (including plain v1 packages — hard rejection, no dual-accept, see
// the rollout policy), and only THEN ranks the survivors by their INNER
// (now-authenticated) tsField.
//
// This order matters: the previous version picked the single entry with the
// largest OUTER, unauthenticated tsField and only afterward would-be verify
// it, which let an attacker who couldn't even decrypt permanently shadow
// every legitimate push with a bogus far-future timestamp. Ranking only ever
// happens on content that has already passed GCM authentication now, so that
// class of attack no longer has anything to act on.
func parseAlfredBest(out []byte, myMAC, slot, tsField, password, meshSSID string) []byte {
	var best []byte
	var bestTS int64
	haveBest := false

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "\", \"")
		if idx < 0 {
			continue
		}
		mac := strings.TrimLeft(line[:idx], "{ \"")
		if strings.ReplaceAll(mac, ":", "") == strings.ReplaceAll(myMAC, ":", "") {
			continue
		}
		rest := line[idx+4:]
		end := strings.LastIndex(rest, "\"")
		if end < 0 {
			continue
		}
		payload := rest[:end]

		var raw string
		if json.Unmarshal([]byte("\""+payload+"\""), &raw) != nil {
			raw = payload
		}

		pt, err := fleetOpen(slot, []byte(raw), password, meshSSID)
		if err != nil {
			logRejectV1Once(slot, mac)
			continue
		}

		var pkg map[string]interface{}
		if json.Unmarshal(pt, &pkg) != nil {
			continue
		}
		ts, _ := pkg[tsField].(float64)
		if !haveBest || int64(ts) > bestTS {
			bestTS = int64(ts)
			best = pt
			haveBest = true
		}
	}
	return best
}

// broadcastUpdatePackage pushes a fleet-wide "force update" command via the
// same Alfred gossip mechanism config-push already uses, on a separate slot
// so the two package schemas never collide, sealed the same way slot 70 is
// (see fleetSeal's slot-bound AAD — a slot 70 envelope can't be replayed
// here and vice versa).
func broadcastUpdatePackage(channel string) bool {
	conf := loadKVFile(MeshConfFile)
	password := conf["admin_password"]
	if password == "" {
		log.Printf("fleet: refusing to broadcast slot 71 update trigger, admin_password is empty")
		return false
	}
	pkg := map[string]interface{}{
		"channel":      channel,
		"triggered_at": time.Now().Unix(),
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		log.Printf("fleet: failed to marshal update package: %v", err)
		return false
	}
	envelope, err := fleetSeal("71", data, password, conf["mesh_ssid"])
	if err != nil {
		log.Printf("fleet: failed to seal update package: %v", err)
		return false
	}
	cmd := exec.Command("alfred", "-s", "71")
	cmd.Stdin = strings.NewReader(string(envelope))
	if err := cmd.Run(); err != nil {
		log.Printf("fleet: alfred -s 71 failed: %v", err)
		return false
	}
	return true
}

func fleetPollUpdateAlfred() {
	out, err := exec.Command("/usr/sbin/alfred", "-r", "71").Output()
	if err != nil || len(out) == 0 {
		return
	}
	conf := loadKVFile(MeshConfFile)
	password := conf["admin_password"]
	if password == "" {
		log.Printf("fleet: admin_password is empty, refusing to process slot 71 packages")
		return
	}
	if best := parseAlfredBest(out, getMyMAC(), "71", "triggered_at", password, conf["mesh_ssid"]); best != nil {
		fleetProcessUpdatePackage(best)
	}
}

// fleetProcessUpdatePackage applies a fleet-wide update trigger locally via
// the same trigger-file + SIGUSR1 mechanism the per-node manual "Update Now"
// endpoint already uses — no separate apply path to maintain. Alfred is a
// repeating gossip store, so the same package keeps being read on every 10s
// poll; FleetUpdateAckFile records the last triggered_at already acted on
// so a node doesn't re-download/re-reboot for a trigger it already applied.
func fleetProcessUpdatePackage(data []byte) {
	var pkg map[string]interface{}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	triggeredAt, _ := pkg["triggered_at"].(float64)
	channel, _ := pkg["channel"].(string)
	if triggeredAt <= 0 || (channel != "software" && channel != "overlay" && channel != "both") {
		return
	}

	existing, _ := os.ReadFile(FleetUpdateAckFile)
	if last, err := strconv.ParseInt(strings.TrimSpace(string(existing)), 10, 64); err == nil && last >= int64(triggeredAt) {
		return
	}

	log.Printf("fleet: update trigger received (channel=%s, triggered_at=%d)", channel, int64(triggeredAt))
	if err := os.WriteFile(UpdateTriggerFile, []byte(channel), 0644); err != nil {
		log.Printf("fleet: failed to write update trigger: %v", err)
		return
	}
	if _, err := runCmd(5*time.Second, "pkill", "-USR1", "-x", "node-update"); err != nil {
		log.Printf("fleet: failed to signal node-update: %v", err)
		return
	}
	// FleetUpdateAckFile is this node's persistent (non-tmpfs, see config.go)
	// replay record for slot 71 — check and fsync its write the same way
	// recordPkgIDApplied does for slot 70's record, so a crash right after
	// signaling node-update can't leave this unrecorded and replay the same
	// trigger again on the next boot.
	if err := writeFileFsync(FleetUpdateAckFile, []byte(strconv.FormatInt(int64(triggeredAt), 10))); err != nil {
		log.Printf("fleet: failed to persist update-trigger ack: %v", err)
	}
}

func fleetProcessPackage(data []byte) {
	var pkg map[string]interface{}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	version, _ := pkg["version"].(string)
	if version == "" {
		return
	}
	pkgID, _ := pkg["pkg_id"].(string)
	if pkgID == "" {
		log.Printf("fleet: rejecting slot 70 package version %s with no pkg_id (stale/legacy format?)", version)
		return
	}
	if isPkgIDApplied(pkgID) {
		// Already applied this exact package before (possibly across a
		// reboot that lost AckVersionFile's tmpfs record while a peer kept
		// gossiping the same already-applied package) — refuse to re-stage.
		return
	}
	// Sanity-bound staged_at itself, before it's ever compared against (and
	// could poison) the watermark below. These boards have no RTC: a node
	// with a future-skewed clock could stage a package whose staged_at, if
	// accepted and later applied, becomes this node's new
	// highestAppliedStagedAt — permanently rejecting every subsequent
	// legitimate push fleet-wide as a "rollback" until real time catches up
	// (recovery needs SSH on every node). Only enforced when THIS node's own
	// clock looks sane (year >= 2025) -- fleetPackageFresh already has the
	// same unset-clock bypass for the same reason.
	now := time.Now()
	if now.Year() >= 2025 {
		if stagedAt, _ := pkg["staged_at"].(float64); int64(stagedAt) > now.Add(10*time.Minute).Unix() {
			log.Printf("fleet: rejecting slot 70 package version %s, staged_at %d is more than 10min ahead of this node's clock — skewed sender clock?", version, int64(stagedAt))
			return
		}
	}
	// Reject anything older than (or equal to) the newest package this node
	// has ever actually applied. The pkg_id ring above only remembers the
	// last appliedRecordMax packages; this watermark never shrinks, so it's
	// what actually stops a rollback replay of an older, already-superseded
	// package once its pkg_id has aged out of that ring — e.g. this node
	// ACKed newer version C, but still has an older version A's pkg_id (now
	// evicted from the ring) re-published by some mesh member reading
	// `alfred -r 70`, within A's own still-valid expires_at window.
	if stagedAt, _ := pkg["staged_at"].(float64); int64(stagedAt) <= highestAppliedStagedAt() {
		log.Printf("fleet: rejecting slot 70 package version %s, staged_at %d is at or before the last applied watermark %d — stale/rollback replay?", version, int64(stagedAt), highestAppliedStagedAt())
		return
	}

	// Check if we already have this version ACKed
	existing, _ := os.ReadFile(AckVersionFile)
	if strings.TrimSpace(string(existing)) == version {
		// Already ACKed — but check if remote added activate_at that we don't
		// have yet. Deliberately NOT gated on fleetPackageFresh below: that's
		// a staging-time freshness bound, and an operator who stages then
		// activates more than expires_at later must not have that
		// legitimate, already-authenticated activation silently dropped just
		// because the ORIGINAL staging now looks "expired." activate_at
		// itself is separately bounds-checked (activateAtInBounds).
		if activateAt, ok := pkg["activate_at"].(float64); ok && activateAt > 0 && activateAtInBounds(int64(activateAt), time.Now()) {
			local := getPendingConfig()
			if local != nil {
				var localPkg map[string]interface{}
				if json.Unmarshal(local, &localPkg) == nil {
					if _, has := localPkg["activate_at"]; !has {
						if err := savePendingConfig(pkg); err != nil {
							log.Printf("fleet: failed to save pending config with activate_at for version %s: %v", version, err)
						} else {
							log.Printf("fleet: activation received for version %s (at %d)", version, int64(activateAt))
						}
					}
				}
			}
		}
		return
	}

	// A genuinely new version this node hasn't seen/ACKed yet — apply the
	// staging-time freshness bound here (not above), so it only ever gates
	// first-time acceptance of a package, never a later, still-in-bounds
	// activation of one already accepted.
	if !fleetPackageFresh(pkg, time.Now()) {
		log.Printf("fleet: rejecting slot 70 package version %s, expired", version)
		return
	}

	// Save as pending config. Check the error and stop here rather than
	// falling through to ACK below: a node that failed to actually persist
	// the pending package must not ACK it anyway -- that would count toward
	// the fleet's ack-quorum gate (ackStatus, admin.go) while this node has
	// nothing pending to actually activate later.
	if err := savePendingConfig(pkg); err != nil {
		log.Printf("fleet: failed to save pending config for version %s: %v", version, err)
		return
	}

	// Sync profiles from the staging node so all nodes share the same view
	fleetSyncProfiles(pkg)

	// Write ACK -- same reasoning: don't broadcast/report an ACK for a
	// version whose local ack-version record failed to actually write.
	if err := os.WriteFile(AckVersionFile, []byte(version), 0644); err != nil {
		log.Printf("fleet: failed to write local ack version %s: %v", version, err)
		return
	}
	log.Printf("fleet: ACKed config version %s", version)

	// Broadcast ACK via multicast for fast propagation
	fleetMcastSendAck(version)
}

func fleetSyncProfiles(pkg map[string]interface{}) {
	prefs := loadFleetPreferences()

	if profiles, ok := pkg["profiles"].(map[string]interface{}); ok {
		synced := make(map[string]FleetProfile)
		for pid, pv := range profiles {
			pm, _ := pv.(map[string]interface{})
			name, _ := pm["name"].(string)
			cfg := make(map[string]string)
			if cfgRaw, ok := pm["config"].(map[string]interface{}); ok {
				for k, v := range cfgRaw {
					cfg[k] = fmt.Sprintf("%v", v)
				}
			}
			synced[pid] = FleetProfile{Name: name, Config: cfg}
		}
		if len(synced) > 0 {
			prefs.Profiles = synced
		}
	}

	if np, ok := pkg["node_profiles"].(map[string]interface{}); ok {
		synced := make(map[string]string)
		for mac, pid := range np {
			synced[mac], _ = pid.(string)
		}
		prefs.NodeProfiles = synced
	}

	if config, ok := pkg["config"].(map[string]interface{}); ok {
		mc := make(map[string]string)
		for k, v := range config {
			mc[k] = fmt.Sprintf("%v", v)
		}
		prefs.MeshConfig = mc
	}

	saveFleetPreferences(prefs)
	log.Printf("fleet: synced profiles from staged package")
}

// fleetMcastSendActivation seals the activation trigger the same way slot 70
// itself is sealed, under its own distinct AAD label ("70-mcast" — not "70",
// even though this activates a slot-70-staged package: a distinct label
// costs nothing and means a real slot-70 config envelope can never even be
// tried against this path or vice versa, on top of the type/pkg_id checks
// below that already prevent it in practice) so a bare unauthenticated
// multicast packet can no longer force an activation — this closes the
// injection path where any device on br0 (mesh member or not, since br0
// bridges bat0 and this listener's own firewall only filters TCP 80/5201,
// not UDP 17070) could previously just craft {"type":"fleet_activate",...}
// directly and skip both the normal 60s window and the ACK-quorum gate.
func fleetMcastSendActivation(version string, activateAt int64) {
	conf := loadKVFile(MeshConfFile)
	password := conf["admin_password"]
	if password == "" {
		log.Printf("fleet: refusing to send mcast activation, admin_password is empty")
		return
	}
	addr, err := net.ResolveUDPAddr("udp4", fleetMcastAddr)
	if err != nil {
		log.Printf("fleet mcast: resolve error: %v", err)
		return
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("fleet mcast: dial error: %v", err)
		return
	}
	defer conn.Close()
	msg := map[string]interface{}{
		"type":        "fleet_activate",
		"version":     version,
		"activate_at": activateAt,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("fleet mcast: marshal error: %v", err)
		return
	}
	envelope, err := fleetSeal("70-mcast", data, password, conf["mesh_ssid"])
	if err != nil {
		log.Printf("fleet mcast: seal error: %v", err)
		return
	}
	if _, err := conn.Write(envelope); err != nil {
		log.Printf("fleet mcast: write error: %v", err)
	}
}

func fleetMcastSendAck(version string) {
	addr, err := net.ResolveUDPAddr("udp4", fleetMcastAddr)
	if err != nil {
		return
	}
	iface, _ := net.InterfaceByName(fleetMcastIface)
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = iface // used for receive side

	hostname, _ := os.Hostname()
	mac := getMyMAC()
	msg := map[string]string{
		"type":     "fleet_ack",
		"version":  version,
		"hostname": hostname,
		"mac":      mac,
	}
	data, _ := json.Marshal(msg)
	conn.Write(data)
}

func fleetMcastListener() {
	addr, err := net.ResolveUDPAddr("udp4", fleetMcastAddr)
	if err != nil {
		log.Printf("fleet mcast: resolve error: %v", err)
		return
	}
	iface, err := net.InterfaceByName(fleetMcastIface)
	if err != nil {
		log.Printf("fleet mcast: interface %s not found, retrying in 30s", fleetMcastIface)
		time.Sleep(30 * time.Second)
		iface, err = net.InterfaceByName(fleetMcastIface)
		if err != nil {
			log.Printf("fleet mcast: giving up on %s", fleetMcastIface)
			return
		}
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, addr)
	if err != nil {
		log.Printf("fleet mcast: listen error: %v", err)
		return
	}
	defer conn.Close()
	conn.SetReadBuffer(4096)

	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		raw := append([]byte(nil), buf[:n]...)

		var probe map[string]interface{}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}

		if msgType, _ := probe["type"].(string); msgType == "fleet_ack" {
			// Deliberately left unauthenticated by design: fleet_ack rides
			// the already-unauthenticated identity/telemetry channel (same
			// trust level as mesh-registry's own gossip). A forged ACK CAN
			// satisfy the non-force Activate gate's ack count (ackStatus,
			// admin.go, merges this map in) — it is not a no-op — but it
			// can't by itself trigger an activation, and it can't cause
			// anything to actually be applied: that still requires a fully
			// authenticated sealed package on slot 70/71 (or the mcast
			// fleet_activate path below, which is itself now sealed).
			mac, _ := probe["mac"].(string)
			version, _ := probe["version"].(string)
			if mac != "" && version != "" {
				fleetAcksMu.Lock()
				fleetAcks[normMAC(mac)] = version
				fleetAcksMu.Unlock()
			}
			continue
		}

		// Everything else must be a sealed v2 envelope. The legacy plaintext
		// "fleet_stage" injection path has been removed entirely — nothing
		// in this repo ever legitimately sent it, so there was no
		// compatibility reason to keep accepting it, and it required no
		// mesh membership at all to exploit (br0 bridges bat0, and this
		// listener's own multicast port is unfiltered by
		// manet-ui-firewall.sh, which only covers TCP 80/5201).
		conf := loadKVFile(MeshConfFile)
		password := conf["admin_password"]
		if password == "" {
			continue
		}
		pt, err := fleetOpen("70-mcast", raw, password, conf["mesh_ssid"])
		if err != nil {
			logRejectV1Once("70-mcast", "")
			continue
		}

		var msg map[string]interface{}
		if json.Unmarshal(pt, &msg) != nil {
			continue
		}
		if msgType, _ := msg["type"].(string); msgType != "fleet_activate" {
			continue
		}
		version, _ := msg["version"].(string)
		activateAt, _ := msg["activate_at"].(float64)
		if version == "" || activateAt <= 0 || !activateAtInBounds(int64(activateAt), time.Now()) {
			continue
		}
		local := getPendingConfig()
		if local == nil {
			continue
		}
		var localPkg map[string]interface{}
		if json.Unmarshal(local, &localPkg) != nil {
			continue
		}
		localVer, _ := localPkg["version"].(string)
		if localVer != version {
			continue
		}
		if _, has := localPkg["activate_at"]; has {
			continue
		}
		localPkg["activate_at"] = activateAt
		if err := savePendingConfig(localPkg); err != nil {
			log.Printf("fleet: failed to save pending config after mcast activation: %v", err)
			continue
		}
		log.Printf("fleet: mcast activation for version %s", version)
	}
}
