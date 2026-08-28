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
	log.Printf("fleet: activation time reached, applying config")
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
	if len(updates) == 0 {
		return
	}
	expandNodeTemplates(updates, loadKVFile(MeshConfFile))
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
	if updates["mesh_ssid"] != "" || updates["mesh_key"] != "" {
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

// applyFleetHalowBW validates halow_bw/halow_channel against this node's own
// regulatory domain before applying — a fleet push may span nodes on
// different domains (e.g. US and EU), so a channel/bandwidth combo valid on
// the node that staged the config is not guaranteed valid here. Unlike
// apiAdminSave (which can reject the whole save before it is persisted),
// fleet config is already committed fleet-wide by the time it activates —
// so an invalid combination is logged and skipped rather than applied,
// leaving this node's current working HaLow config running.
func applyFleetHalowBW(conf map[string]string) {
	domain := resolveHalowDomain(conf)
	if err := validateHalowChannel(domain, effectiveHalowBW(conf), conf["halow_channel"]); err != nil {
		log.Printf("fleet: skipping halow_bw/halow_channel apply, invalid for this node's domain %q: %v", domain, err)
		return
	}
	applyHalowBW(conf)
}

func fleetPollAlfred() {
	out, err := exec.Command("/usr/sbin/alfred", "-r", "70").Output()
	if err != nil || len(out) == 0 {
		return
	}
	if best := parseAlfredBest(out, getMyMAC(), "staged_at"); best != nil {
		fleetProcessPackage(best)
	}
}

// parseAlfredBest scans `alfred -r <slot>` output — one line per node:
// { "mac", "json_payload" } — and returns the raw JSON payload with the
// largest value of tsField, skipping this node's own entry. Shared by the
// config-push and update-trigger watchers, which differ only in which
// timestamp field orders "most recent."
func parseAlfredBest(out []byte, myMAC, tsField string) []byte {
	var best []byte
	var bestTS int64

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
		var pkg map[string]interface{}
		if json.Unmarshal([]byte(raw), &pkg) != nil {
			continue
		}
		ts, _ := pkg[tsField].(float64)
		if int64(ts) > bestTS {
			bestTS = int64(ts)
			best = []byte(raw)
		}
	}
	return best
}

// broadcastUpdatePackage pushes a fleet-wide "force update" command via the
// same Alfred gossip mechanism config-push already uses, on a separate slot
// so the two package schemas never collide.
func broadcastUpdatePackage(channel string) bool {
	pkg := map[string]interface{}{
		"channel":      channel,
		"triggered_at": time.Now().Unix(),
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		return false
	}
	cmd := exec.Command("alfred", "-s", "71")
	cmd.Stdin = strings.NewReader(string(data))
	return cmd.Run() == nil
}

func fleetPollUpdateAlfred() {
	out, err := exec.Command("/usr/sbin/alfred", "-r", "71").Output()
	if err != nil || len(out) == 0 {
		return
	}
	if best := parseAlfredBest(out, getMyMAC(), "triggered_at"); best != nil {
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
	os.WriteFile(FleetUpdateAckFile, []byte(strconv.FormatInt(int64(triggeredAt), 10)), 0644)
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

	// Check if we already have this version ACKed
	existing, _ := os.ReadFile(AckVersionFile)
	if strings.TrimSpace(string(existing)) == version {
		// Already ACKed — but check if remote added activate_at that we don't have yet
		if activateAt, ok := pkg["activate_at"].(float64); ok && activateAt > 0 {
			local := getPendingConfig()
			if local != nil {
				var localPkg map[string]interface{}
				if json.Unmarshal(local, &localPkg) == nil {
					if _, has := localPkg["activate_at"]; !has {
						savePendingConfig(pkg)
						log.Printf("fleet: activation received for version %s (at %d)", version, int64(activateAt))
					}
				}
			}
		}
		return
	}

	// Save as pending config
	savePendingConfig(pkg)

	// Sync profiles from the staging node so all nodes share the same view
	fleetSyncProfiles(pkg)

	// Write ACK
	os.WriteFile(AckVersionFile, []byte(version), 0644)
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

func fleetMcastSendActivation(version string, activateAt int64) {
	addr, err := net.ResolveUDPAddr("udp4", fleetMcastAddr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	msg := map[string]interface{}{
		"type":        "fleet_activate",
		"version":     version,
		"activate_at": activateAt,
	}
	data, _ := json.Marshal(msg)
	conn.Write(data)
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
		var msg map[string]interface{}
		if json.Unmarshal(buf[:n], &msg) != nil {
			continue
		}
		msgType, _ := msg["type"].(string)
		if msgType == "fleet_stage" {
			fleetProcessPackage(buf[:n])
		} else if msgType == "fleet_ack" {
			mac, _ := msg["mac"].(string)
			version, _ := msg["version"].(string)
			if mac != "" && version != "" {
				fleetAcksMu.Lock()
				fleetAcks[normMAC(mac)] = version
				fleetAcksMu.Unlock()
			}
		} else if msgType == "fleet_activate" {
			version, _ := msg["version"].(string)
			activateAt, _ := msg["activate_at"].(float64)
			if version != "" && activateAt > 0 {
				local := getPendingConfig()
				if local != nil {
					var localPkg map[string]interface{}
					if json.Unmarshal(local, &localPkg) == nil {
						localVer, _ := localPkg["version"].(string)
						if localVer == version {
							if _, has := localPkg["activate_at"]; !has {
								localPkg["activate_at"] = activateAt
								savePendingConfig(localPkg)
								log.Printf("fleet: mcast activation for version %s", version)
							}
						}
					}
				}
			}
		}
	}
}
