package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// fleetSecretKeys is the ONE list of config keys that must never be served
// to an unauthenticated caller, in any of the several places they can show
// up: current_config, a staged/pending package's config, and every fleet
// profile's config (fleetSyncProfiles, fleet.go, populates
// prefs.Profiles/prefs.MeshConfig with whatever was pushed, on EVERY node --
// so after the very first fleet push these are populated fleet-wide, not
// just on the node that originally staged it).
var fleetSecretKeys = []string{"admin_password", "mesh_key", "lan_ap_key"}

func redactSecretKeys(m map[string]string) {
	for _, k := range fleetSecretKeys {
		delete(m, k)
	}
}

func redactSecretKeysAny(m map[string]interface{}) {
	for _, k := range fleetSecretKeys {
		delete(m, k)
	}
}

// dropEmptySecrets removes any fleetSecretKeys entry from m whose value is
// an empty string. This is the ONE shared guard against a real incident: the
// redaction added above (redactSecretKeys/redactPendingSecrets/
// redactFleetPreferences) means an unauthenticated GET of /api/admin/status
// renders admin_password/mesh_key/lan_ap_key as absent, i.e. "" once a form
// reads it back. If an operator opens the Fleet or Config edit form before
// authenticating, then logs in and retries the SAME submitted body, that
// body now legitimately contains "" for these keys — and without this
// guard, apiAdminSave/apiAdminStage/apiFleetPreferences would all happily
// persist/broadcast admin_password="" and mesh_key="" fleet-wide. An empty
// secret must ALWAYS be read as "field left untouched," never as "set to
// blank" — there is no legitimate way to intentionally blank these via this
// API; clearing a password is done by setting a new one, not an empty one.
// Call this on every client-supplied config map before it's persisted or
// broadcast — do not reimplement this check per call site.
func dropEmptySecrets(m map[string]string) {
	for _, k := range fleetSecretKeys {
		if v, ok := m[k]; ok && v == "" {
			delete(m, k)
		}
	}
}

// dropEmptySecretsAny is dropEmptySecrets for a map[string]interface{} (the
// raw, not-yet-stringified shape client JSON bodies arrive in, e.g.
// apiAdminStage's configMap). Treats both an explicit "" string and a JSON
// null (decodes to a nil interface) as empty.
func dropEmptySecretsAny(m map[string]interface{}) {
	for _, k := range fleetSecretKeys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if v == nil {
			delete(m, k)
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			delete(m, k)
		}
	}
}

// redactPendingSecrets strips fleetSecretKeys from a pending package's
// top-level config AND from every profile's nested config before it's ever
// returned to an unauthenticated caller. Fails closed: if raw can't be
// parsed or re-marshaled for any reason, this returns nil rather than risk
// returning the original, unredacted bytes.
func redactPendingSecrets(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	var pkg map[string]interface{}
	if json.Unmarshal(raw, &pkg) != nil {
		return nil
	}
	if configRaw, ok := pkg["config"].(map[string]interface{}); ok {
		redactSecretKeysAny(configRaw)
	}
	if profilesRaw, ok := pkg["profiles"].(map[string]interface{}); ok {
		for _, pv := range profilesRaw {
			if pm, ok := pv.(map[string]interface{}); ok {
				if cfgRaw, ok := pm["config"].(map[string]interface{}); ok {
					redactSecretKeysAny(cfgRaw)
				}
			}
		}
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

// redactFleetPreferences strips fleetSecretKeys from prefs.MeshConfig and
// from every prefs.Profiles[*].Config before an unauthenticated caller sees
// them. Mutates prefs in place -- callers must pass a value they own (e.g.
// the fresh copy loadFleetPreferences() returns on every call), never the
// long-lived FleetPrefsFile-backed state, since this is a destructive
// redaction, not a read-only view.
func redactFleetPreferences(prefs *FleetPreferences) {
	redactSecretKeys(prefs.MeshConfig)
	for pid, prof := range prefs.Profiles {
		redactSecretKeys(prof.Config)
		prefs.Profiles[pid] = prof
	}
}

func makeConfigVersion(config map[string]string) string {
	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf(`"%s":"%s"`, k, config[k])
	}
	data := fmt.Sprintf("{%s}@%d", strings.Join(parts, ","), time.Now().UnixNano())
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)[:8]
}

func getPendingConfig() json.RawMessage {
	data, err := os.ReadFile(PendingConfFile)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

// savePendingConfig also persists an armed activation (one with
// activate_at) so it survives a reboot; see fleetactivation.go.
func savePendingConfig(pkg map[string]interface{}) error {
	data, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(PendingConfFile, data, 0644); err != nil {
		return err
	}
	if _, armed := pkg["activate_at"]; armed {
		return persistActivation(pkg)
	}
	return nil
}

func clearPendingConfig() {
	os.Remove(PendingConfFile)
	clearPersistedActivation()
}

// broadcastConfigPackage seals pkg into a v2 envelope (see fleetcrypto.go)
// keyed on this node's current admin_password before writing it to Alfred
// slot 70. Any new admin_password being rolled out travels entirely inside
// this ciphertext, same as any other config field — see apiAdminStage's
// staging call for why that ordering matters. Refuses outright (no fallback
// to mesh_key or any fixed key) when admin_password is empty.
func broadcastConfigPackage(pkg map[string]interface{}) bool {
	conf := loadKVFile(MeshConfFile)
	password := conf["admin_password"]
	if password == "" {
		log.Printf("fleet: refusing to broadcast slot 70 config package, admin_password is empty")
		return false
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		log.Printf("fleet: failed to marshal slot 70 config package: %v", err)
		return false
	}
	envelope, err := fleetSeal("70", data, password, conf["mesh_ssid"])
	if err != nil {
		log.Printf("fleet: failed to seal slot 70 config package: %v", err)
		return false
	}
	cmd := exec.Command("alfred", "-s", "70")
	cmd.Stdin = strings.NewReader(string(envelope))
	if err := cmd.Run(); err != nil {
		log.Printf("fleet: alfred -s 70 failed: %v", err)
		return false
	}
	return true
}

func loadFleetPreferences() FleetPreferences {
	var prefs FleetPreferences
	data, err := os.ReadFile(FleetPrefsFile)
	if err == nil {
		json.Unmarshal(data, &prefs)
	}
	if prefs.Profiles == nil || len(prefs.Profiles) == 0 {
		prefs.Profiles = map[string]FleetProfile{
			"default": {Name: "Default", Config: map[string]string{}},
		}
	}
	if prefs.NodeProfiles == nil {
		prefs.NodeProfiles = map[string]string{}
	}
	if prefs.MeshConfig == nil {
		prefs.MeshConfig = map[string]string{}
	}
	return prefs
}

func saveFleetPreferences(prefs FleetPreferences) error {
	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return os.WriteFile(FleetPrefsFile, data, 0644)
}

func assembleAdminStatus(authed bool) AdminStatus {
	conf := loadKVFile(MeshConfFile)
	registry := parseRegistry()
	pending := getPendingConfig()
	prefs := loadFleetPreferences()

	currentConfig := map[string]string{
		"node_hostname":           conf["node_hostname"],
		"eud":                     confGet(conf, "eud", "wired"),
		"lan_ap_ssid":             conf["lan_ap_ssid"],
		"lan_ap_key":              conf["lan_ap_key"],
		"max_euds_per_node":       confGet(conf, "max_euds_per_node", "0"),
		"mesh_ssid":               conf["mesh_ssid"],
		"mesh_key":                conf["mesh_key"],
		"ipv4_network":            confGet(conf, "ipv4_network", "10.30.2.0/24"),
		"regulatory_domain":       confGet(conf, "regulatory_domain", "US"),
		"admin_password":          conf["admin_password"],
		"gateway":                 confGet(conf, "gateway", "y"),
		"gateway_nat":             confGet(conf, "gateway_nat", "y"),
		"gateway_mss_clamp":       confGet(conf, "gateway_mss_clamp", "y"),
		"gateway_bandwidth":       confGet(conf, "gateway_bandwidth", ""),
		"halow_bw":                confGet(conf, "halow_bw", ""),
		"halow_channel":           conf["halow_channel"],
		"halow_regulatory_domain": conf["halow_regulatory_domain"],
		"acs":                     confGet(conf, "acs", "n"),
		"mesh_5ghz_bw":            confGet(conf, "mesh_5ghz_bw", "20"),
		"mesh_5ghz_channel":       conf["mesh_5ghz_channel"],
		"multicast_mode":          confGet(conf, "multicast_mode", "flood"),
		"lan_ap_channel":          conf["lan_ap_channel"],
		"lan_ap_bw":               confGet(conf, "lan_ap_bw", "20"),
		"voice_mic_volume":        confGet(conf, "voice_mic_volume", "80"),
		"voice_speaker_volume":    confGet(conf, "voice_speaker_volume", "80"),
		"voice_channel":           confGet(conf, "voice_channel", "1"),
		"voice_rx_channels":       confGet(conf, "voice_rx_channels", "1"),
		"voice_ptt_mode":          confGet(conf, "voice_ptt_mode", "openvlm"),
		"voice_enabled":           confGet(conf, "voice_enabled", "y"),
		"dns_servers":             confGet(conf, "dns_servers", "8.8.8.8,8.8.4.4"),
		"qos_enabled":             confGet(conf, "qos_enabled", "y"),
		"qos_voice_band":          confGet(conf, "qos_voice_band", "0"),
		"qos_cot_band":            confGet(conf, "qos_cot_band", "1"),
		"qos_chat_band":           confGet(conf, "qos_chat_band", "2"),
		"require_auth":            confGet(conf, "require_auth", "n"),
		"auto_update":             confGet(conf, "auto_update", "n"),
		"update_url":              confGet(conf, "update_url", ""),
		"auto_update_overlay":     confGet(conf, "auto_update_overlay", "n"),
		"auto_update_min_mbps":    confGet(conf, "auto_update_min_mbps", "10"),
		"eud_bandwidth":           confGet(conf, "eud_bandwidth", "0"),
		"battery_monitor":         confGet(conf, "battery_monitor", "y"),
		"voice_beep_tx_start":     confGet(conf, "voice_beep_tx_start", "y"),
		"voice_beep_rx_end":       confGet(conf, "voice_beep_rx_end", "y"),
		"voice_gain":              confGet(conf, "voice_gain", "3.0"),
		"gps":                     confGet(conf, "gps", "y"),
		"gps_source":              confGet(conf, "gps_source", "receiver"),
		"gps_static_lat":          conf["gps_static_lat"],
		"gps_static_lon":          conf["gps_static_lon"],
		"gps_static_alt":          conf["gps_static_alt"],
		"callsign":                conf["callsign"],
		"cot_type":                conf["cot_type"],
		"cot_team":                conf["cot_team"],
		"cot_role":                conf["cot_role"],
		"cot_icon":                conf["cot_icon"],
	}

	if currentConfig["halow_bw"] == "" {
		info := getHalowDriverInfo("wlan2")
		if bw, ok := info["halow_bw"]; ok {
			currentConfig["halow_bw"] = bw
		} else {
			currentConfig["halow_bw"] = "8MHz"
		}
	}

	// admin_password, mesh_key, and lan_ap_key are secrets. This endpoint is
	// registered with no auth gate (main.go) so the UI can render most of
	// the status page before login, and manet-ui-firewall.sh only limits
	// HTTPS reachability to the mesh side (any node and any EUD in the mesh,
	// plus the uplink LAN when ui_uplink_access=y) — that's still an
	// untrusted device class, not just an authenticated admin. Strip the secrets unless the caller already passed the same
	// checkAuth gate the requireAuth-wrapped /api/admin/* and /api/control/*
	// routes require (main.go). This must cover
	// every place a secret can appear in this response, not just
	// current_config: a staged Pending package's config (and every fleet
	// profile inside it) and Preferences.MeshConfig/Profiles[*].Config are
	// ALL populated with pushed config by fleetSyncProfiles on every node
	// after the first fleet push — missing any one of them defeats the
	// whole point, since admin_password IS the fleet encryption key.
	if !authed {
		redactSecretKeys(currentConfig)
		redactFleetPreferences(&prefs)
	}

	myMAC := getMyMAC()
	localAck := ""
	if b, err := os.ReadFile(AckVersionFile); err == nil {
		localAck = strings.TrimSpace(string(b))
	}

	var adminNodes []AdminNode
	activeCount := 0
	for _, rn := range registry {
		state := rn["NODE_STATE"]
		if state == "ACTIVE" {
			activeCount++
		}
		mac := normMAC(rn["MAC_ADDRESS"])
		profile := prefs.NodeProfiles[mac]
		if profile == "" {
			profile = "default"
		}
		adminNodes = append(adminNodes, AdminNode{
			Hostname:  rn["HOSTNAME"],
			IP:        rn["IPV4_ADDRESS"],
			MAC:       rn["MAC_ADDRESS"],
			Ack:       nodeAckVersion(rn, myMAC, localAck),
			LastSeen:  rn["LAST_SEEN_TIMESTAMP"],
			NodeState: state,
			Profile:   profile,
		})
	}

	// Compute ack/dangerous-key/offline-node info from the ORIGINAL,
	// unredacted pending package -- dangerousKeyChanges needs to see key
	// NAMES like "admin_password" in pkg["config"], which redactPending
	// Secrets below removes entirely (not just the value). Only the
	// response's Pending field itself gets redacted, after this.
	var ackInfo *AckStatusResult
	if pending != nil {
		var pkg map[string]interface{}
		if json.Unmarshal(pending, &pkg) == nil {
			if version, _ := pkg["version"].(string); version != "" {
				acked, total, missing := ackStatus(version)
				ackInfo = &AckStatusResult{Version: version, Acked: acked, Total: total, Missing: missing}
				if configRaw, ok := pkg["config"].(map[string]interface{}); ok {
					ackInfo.DangerousKeys = dangerousKeyChanges(configRaw, conf)
					if len(ackInfo.DangerousKeys) > 0 {
						ackInfo.OfflineNodes = offlineNodeNames()
					}
				}
			}
		}
	}

	pendingForResponse := pending
	if !authed {
		pendingForResponse = redactPendingSecrets(pending)
	}

	return AdminStatus{
		CurrentConfig: currentConfig,
		Pending:       pendingForResponse,
		Nodes:         adminNodes,
		TotalNodes:    len(registry),
		ActiveNodes:   activeCount,
		MyHostname:    getMyHostname(),
		Preferences:   prefs,
		AckStatus:     ackInfo,
	}
}

// fleetDangerousKeys are config keys whose change can alter or partition the
// fleet's crypto key (admin_password) or its mesh membership/addressing
// (mesh_ssid, mesh_key, ipv4_network). Pushing any of these while a registry
// node is OFFLINE risks silently orphaning that node — it can come back
// unable to open ANY future fleet package under the new key, or unable to
// rejoin the mesh at all, with no operator visibility until someone notices.
var fleetDangerousKeys = []string{"admin_password", "mesh_ssid", "mesh_key", "ipv4_network"}

// dangerousKeyChanges returns the subset of fleetDangerousKeys present in
// configRaw whose value actually differs from currentConf — a fleet push
// resends every field every time, so presence alone isn't a change (mirrors
// the same "!= existing" convention fleetApplyConfig already uses to decide
// whether to restart wpa_supplicant).
func dangerousKeyChanges(configRaw map[string]interface{}, currentConf map[string]string) []string {
	var changed []string
	for _, k := range fleetDangerousKeys {
		v, ok := configRaw[k]
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", v) != currentConf[k] {
			changed = append(changed, k)
		}
	}
	return changed
}

// offlineNodeNames returns the hostname (or IP, if hostname is blank) of
// every registry node NOT in mesh-registry's ACTIVE state — the same
// liveness classification ackStatus already uses.
func offlineNodeNames() []string {
	var offline []string
	for _, rn := range parseRegistry() {
		if rn["NODE_STATE"] == "ACTIVE" {
			continue
		}
		name := rn["HOSTNAME"]
		if name == "" {
			name = rn["IPV4_ADDRESS"]
		}
		offline = append(offline, name)
	}
	return offline
}

// nodeAckVersion resolves the config-ack version a given registry node last
// reported, preferring this node's own authoritative local AckVersionFile
// over the registry's CONFIG_ACK_VERSION field for itself (which is only as
// fresh as mesh-registry's last republish), then falling back to a
// multicast-gossiped ack for a node the registry hasn't caught up with yet.
// Shared by assembleAdminStatus's per-node display and ackStatus's gate
// computation below, so both use identical logic.
func nodeAckVersion(rn RegistryNode, myMAC, localAck string) string {
	mac := normMAC(rn["MAC_ADDRESS"])
	ack := rn["CONFIG_ACK_VERSION"]
	if mac == myMAC && localAck != "" {
		ack = localAck
	}
	if ack == "" {
		fleetAcksMu.Lock()
		if mcastAck, ok := fleetAcks[mac]; ok && mcastAck != "" {
			ack = mcastAck
		}
		fleetAcksMu.Unlock()
	}
	return ack
}

// ackStatus reports, for a given staged config version, how many currently
// ACTIVE nodes (mesh-registry's own liveness classification, see
// mesh-registry/main.go's NODE_STATE field) have acknowledged it. This is the
// one shared computation apiAdminActivate's non-force gate and
// assembleAdminStatus's UI-facing ack_status must both use — previously the
// gate checked raw registry CONFIG_ACK_VERSION only (no local/multicast
// merge, no liveness filter), while the UI computed a richer merged number,
// so the UI could show 4/4 and enable Activate while the server still
// rejected with "N nodes have not ACKed." Filtering to ACTIVE nodes also
// means a permanently-dead or swapped-out node no longer blocks activation
// forever.
func ackStatus(version string) (acked, total int, missing []string) {
	registry := parseRegistry()
	myMAC := getMyMAC()
	localAck := ""
	if b, err := os.ReadFile(AckVersionFile); err == nil {
		localAck = strings.TrimSpace(string(b))
	}
	for _, rn := range registry {
		if rn["NODE_STATE"] != "ACTIVE" {
			continue
		}
		total++
		if nodeAckVersion(rn, myMAC, localAck) == version {
			acked++
			continue
		}
		name := rn["HOSTNAME"]
		if name == "" {
			name = rn["IPV4_ADDRESS"]
		}
		missing = append(missing, name)
	}
	return acked, total, missing
}

func getMACsuffix() string {
	out, err := runCmdStdout(3*time.Second, "ip", "-br", "link", "show")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && (strings.HasPrefix(fields[0], "eth") || strings.HasPrefix(fields[0], "end")) {
			mac := fields[2]
			parts := strings.Split(mac, ":")
			if len(parts) >= 4 {
				return strings.Join(parts[len(parts)-4:], "")
			}
		}
	}
	return ""
}
