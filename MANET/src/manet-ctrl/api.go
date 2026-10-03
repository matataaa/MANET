package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// maxJSONBody caps readBody. Every JSON request the UI sends is a few KiB;
// without a cap, one oversized POST (several routes read the body before
// checking auth) is buffered whole into memory. Applet uploads have their
// own, larger limit (maxUploadSize).
const maxJSONBody = 1 << 20

func readBody(r *http.Request) map[string]interface{} {
	m := make(map[string]interface{})
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	if err != nil || len(body) == 0 {
		return m
	}
	json.Unmarshal(body, &m)
	return m
}

func jsonStr(m map[string]interface{}, key, def string) string {
	if v, ok := m[key]; ok {
		switch s := v.(type) {
		case string:
			return s
		case float64:
			return strconv.FormatFloat(s, 'f', -1, 64)
		}
	}
	return def
}

func jsonFloat(m map[string]interface{}, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case string:
			f, _ := strconv.ParseFloat(n, 64)
			return f
		}
	}
	return def
}

func jsonInt(m map[string]interface{}, key string, def int) int {
	return int(jsonFloat(m, key, float64(def)))
}

func jsonBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

var validateTargetRE = regexp.MustCompile(`^[a-zA-Z0-9._:-]+$`)

// --- Status endpoints ---

func apiData(w http.ResponseWriter, r *http.Request) {
	data := cachedStatusData()
	writeJSON(w, 200, data)
}

func apiLocal(w http.ResponseWriter, r *http.Request) {
	data := cachedLocalData()
	writeJSON(w, 200, data)
}

func apiDaemons(w http.ResponseWriter, r *http.Request) {
	result := map[string]interface{}{}

	for _, d := range []struct {
		key  string
		path string
	}{
		{"gps", "/run/gps_status.json"},
		{"battery", "/run/battery_status.json"},
		{"cot_emitter", "/run/cot_emitter_status.json"},
	} {
		data, err := os.ReadFile(d.path)
		if err != nil {
			result[d.key] = map[string]interface{}{"available": false}
			continue
		}
		var m map[string]interface{}
		if json.Unmarshal(data, &m) != nil {
			result[d.key] = map[string]interface{}{"available": false}
			continue
		}
		m["available"] = true
		result[d.key] = m
	}

	writeJSON(w, 200, result)
}

func apiPeer(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/peer/")
	if rest == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Missing peer IP"})
		return
	}
	peerIP := rest
	subPath := ""
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		peerIP = rest[:idx]
		subPath = rest[idx:]
	}
	if ip := net.ParseIP(peerIP); ip == nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid IP"})
		return
	}
	if !isMeshPeerIP(peerIP, loadKVFile(MeshConfFile)) {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "Peer must be a mesh node address"})
		return
	}
	if subPath != "" && !strings.HasPrefix(subPath, "/api/") {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Only /api/ paths can be proxied"})
		return
	}
	if strings.HasPrefix(subPath, "/api/peer/") {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Proxy requests cannot be chained"})
		return
	}

	if subPath == "" {
		data := getPeerLocalData(peerIP, 2*time.Second)
		if data == nil {
			data = make(map[string]interface{})
		}
		writeJSON(w, 200, data)
		return
	}

	peerProxyRequest(w, r, peerIP, subPath)
}

// isMeshPeerIP reports whether ip is an address inside this mesh's
// ipv4_network. /api/peer only proxies to mesh nodes: it used to dial any
// address over HTTPS, an open proxy into whatever network the node sits on
// (reachable from a gateway's uplink LAN too). A bare network without a
// prefix length (older images) is taken as /24, like mesh-manager does.
func isMeshPeerIP(ipStr string, conf map[string]string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	network := confGet(conf, "ipv4_network", "10.30.2.0/24")
	if !strings.Contains(network, "/") {
		network += "/24"
	}
	_, cidr, err := net.ParseCIDR(network)
	return err == nil && cidr.Contains(ip)
}

func peerProxyURL(peerIP, path, rawQuery string) string {
	u := "https://" + peerIP + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// peerProxyAuthToken carries the user's login across to the peer: when the
// request to this node has a live login session, the proxied request gets a
// fleet peer token bound to peerIP and the API domain, which the peer
// accepts as authenticated (proxiedAPIRequestAuthenticated). Only a session
// counts here, never a relayed token, and mintFleetPeerToken returns "" on a
// node without require_auth=y, so an open node can't relay unauthenticated
// callers into a locked one. The fleet shares one admin password, so a
// login on any node already proves the same thing.
func peerProxyAuthToken(r *http.Request, peerIP string) string {
	if required, authenticated := sessionAuthState(r); !required || !authenticated {
		return ""
	}
	return mintFleetPeerToken(peerIP, FleetPeerAuthDomainAPI)
}

// copyPeerHeaders copies the peer's response headers, except Set-Cookie: a
// peer's session cookie must never be set on this node's origin, where it
// would replace the browser's session with this node.
func copyPeerHeaders(dst, src http.Header) {
	for k, vals := range src {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func peerProxyRequest(w http.ResponseWriter, r *http.Request, peerIP, path string) {
	if r.ContentLength > maxJSONBody {
		writeJSON(w, 413, map[string]interface{}{"ok": false, "error": "Request body too large"})
		return
	}
	targetURL := peerProxyURL(peerIP, path, r.URL.RawQuery)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: peerTLSConfig,
		},
	}

	proxyReq, err := http.NewRequest(r.Method, targetURL, http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		writeJSON(w, 502, map[string]interface{}{"ok": false, "error": "proxy request failed"})
		return
	}
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if token := peerProxyAuthToken(r, peerIP); token != "" {
		proxyReq.Header.Set(FleetPeerAuthHeader, token)
	}
	proxyReq.ContentLength = r.ContentLength

	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, 502, map[string]interface{}{"ok": false, "error": "peer unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	copyPeerHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func apiVoice(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		body := readBody(r)
		action := jsonStr(body, "action", "")
		// Keying the radio is what every EUD user does (the Android app
		// has no login), and /ws/voice already lets anyone on the mesh
		// transmit; volume is a local comfort setting. Reconfiguring or
		// restarting the voice service stays admin-only.
		if action == "volume" || action == "ptt_on" || action == "ptt_off" {
			apiVoiceConfig(w, r, body)
			return
		}
		if !checkAuth(w, r) {
			return
		}
		apiVoiceConfig(w, r, body)
		return
	}
	writeJSON(w, 200, getVoiceStatus())
}

func apiVoiceConfig(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
	action := jsonStr(body, "action", "")

	if action == "ptt_on" || action == "ptt_off" {
		val := "0"
		if action == "ptt_on" {
			val = "1"
		}
		if err := os.WriteFile("/run/mesh-voice-ptt-remote", []byte(val), 0644); err != nil {
			writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]interface{}{"ok": true})
		return
	}

	if action == "start" || action == "stop" || action == "restart" {
		ok, errMsg := serviceAction("mesh-voice", action)
		writeJSON(w, 200, map[string]interface{}{"ok": ok, "error": errMsg})
		return
	}

	if action == "volume" {
		micVol := jsonStr(body, "mic_volume", "")
		spkVol := jsonStr(body, "speaker_volume", "")
		if micVol == "" && spkVol == "" {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "no volume specified"})
			return
		}
		// Reachable without a login, so nothing but a 0-100 level may
		// reach mesh.conf.
		for _, v := range []string{micVol, spkVol} {
			if n, err := strconv.Atoi(v); v != "" && (err != nil || n < 0 || n > 100) {
				writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "volume must be 0-100"})
				return
			}
		}
		volUpdates := map[string]string{}
		if micVol != "" {
			volUpdates["voice_mic_volume"] = micVol
		}
		if spkVol != "" {
			volUpdates["voice_speaker_volume"] = spkVol
		}
		saveKVFile(MeshConfFile, volUpdates)
		applyVoiceVolume(loadKVFile(MeshConfFile))
		writeJSON(w, 200, map[string]interface{}{"ok": true})
		return
	}

	if action != "configure" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "unknown action"})
		return
	}

	ptt := jsonStr(body, "ptt_mode", "openvlm")
	iface := jsonStr(body, "interface", "br0")
	addr := jsonStr(body, "mcast_addr", "239.69.0.1")
	port := jsonStr(body, "port", "4370")
	micVol := jsonStr(body, "mic_volume", "")
	spkVol := jsonStr(body, "speaker_volume", "")

	validPTT := map[string]bool{"always": true, "gpio": true, "openvlm": true, "vox": true}
	if !validPTT[ptt] {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid ptt_mode"})
		return
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`).MatchString(iface) {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid interface"})
		return
	}
	if net.ParseIP(addr) == nil || !strings.HasPrefix(addr, "239.") {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid multicast address"})
		return
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1024 || portNum > 65535 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid port"})
		return
	}

	// Persist PTT mode and volume to mesh.conf
	confUpdates := map[string]string{"voice_ptt_mode": ptt}
	if micVol != "" {
		confUpdates["voice_mic_volume"] = micVol
	}
	if spkVol != "" {
		confUpdates["voice_speaker_volume"] = spkVol
	}
	saveKVFile(MeshConfFile, confUpdates)
	if micVol != "" || spkVol != "" {
		applyVoiceVolume(loadKVFile(MeshConfFile))
	}

	execLine := fmt.Sprintf("/usr/local/bin/mesh-voice -iface %s -addr %s -port %s -ptt %s", iface, addr, port, ptt)

	unit := fmt.Sprintf(`[Unit]
Description=Mesh Voice PTT over multicast
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, execLine)

	if err := os.WriteFile("/etc/systemd/system/mesh-voice.service", []byte(unit), 0644); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	exec.Command("systemctl", "daemon-reload").Run()
	// mesh-voice exits cleanly on its own when voice_enabled=n, but restarting
	// a service that's about to immediately exit again is pointless churn —
	// the Config tab saves this same field via /api/admin/save in parallel
	// with this call, so mesh.conf may already reflect "disabled" here.
	if confGet(loadKVFile(MeshConfFile), "voice_enabled", "y") == "n" {
		exec.Command("systemctl", "stop", "mesh-voice").Run()
	} else {
		exec.Command("systemctl", "restart", "mesh-voice").Run()
	}

	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiAdminStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, assembleAdminStatus(isAuthed(r)))
}

// apiUpdateStatus returns node-update's own status file verbatim — the
// software/overlay detect results and current uplink reading it writes on
// every check cycle, regardless of whether auto_update is enabled.
func apiUpdateStatus(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(UpdateStatusFile)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"software":    map[string]interface{}{"available": false},
			"overlay":     map[string]interface{}{"available": false},
			"uplink_mbps": 0,
			"uplink_type": "unknown",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// apiUpdateNow triggers a manual, deliberate update on this node — the
// UI has already shown the operator the bandwidth warning before calling
// this. Writes the trigger file node-update's SIGUSR1 handler reads, then
// signals it directly (systemctl reload is already used for SIGHUP/recheck,
// so this is a distinct signal rather than overloading that path).
func apiUpdateNow(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	channel := jsonStr(body, "channel", "")
	if channel != "software" && channel != "overlay" && channel != "both" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "channel must be software, overlay, or both"})
		return
	}
	if err := os.WriteFile(UpdateTriggerFile, []byte(channel), 0644); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	if _, err := runCmd(5*time.Second, "pkill", "-USR1", "-x", "node-update"); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "failed to signal node-update: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// apiForceUpdate broadcasts a fleet-wide update trigger — every node picks
// it up within one Alfred poll cycle (~10s) and applies via the same local
// mechanism as a manual per-node "Update Now" (see fleet.go,
// fleetProcessUpdatePackage), bypassing each node's bandwidth gate the same
// way a manual trigger does. The Fleet Control UI has already shown the
// aggregate bandwidth warning before calling this.
func apiForceUpdate(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	channel := jsonStr(body, "channel", "")
	if channel != "software" && channel != "overlay" && channel != "both" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "channel must be software, overlay, or both"})
		return
	}
	if !broadcastUpdatePackage(channel) {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "failed to broadcast update trigger"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// Peer's plain HTTP port only ever redirects to HTTPS (see main.go), and
// every node's cert is self-signed — HTTPS + peerTLSConfig is the pattern
// already used for peer-to-peer calls elsewhere (see peerProxyRequest).
func getPeerUpdateStatus(peerIP string, timeout time.Duration) map[string]interface{} {
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: peerTLSConfig},
	}
	resp, err := client.Get(fmt.Sprintf("https://%s/api/admin/update-status", peerIP))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return result
}

// apiUpdateSummary aggregates every fleet node's update-status (including
// this one) for the Fleet Control "N of M nodes have an update available"
// banner. Read-only, fanned out in parallel with a short per-node timeout
// so one unreachable node can't stall the whole response.
func apiUpdateSummary(w http.ResponseWriter, r *http.Request) {
	registry := parseRegistry()
	myMAC := getMyMAC()

	type nodeSummary struct {
		Hostname string      `json:"hostname"`
		IP       string      `json:"ip"`
		Status   interface{} `json:"status"`
		Reached  bool        `json:"reached"`
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	nodes := make([]nodeSummary, 0, len(registry))

	for _, rn := range registry {
		rn := rn
		ip := rn["IPV4_ADDRESS"]
		hostname := rn["HOSTNAME"]
		isMe := normMAC(rn["MAC_ADDRESS"]) == myMAC

		wg.Add(1)
		go func() {
			defer wg.Done()
			var status map[string]interface{}
			reached := false
			if isMe {
				data, err := os.ReadFile(UpdateStatusFile)
				if err == nil && json.Unmarshal(data, &status) == nil {
					reached = true
				}
			} else if ip != "" {
				if s := getPeerUpdateStatus(ip, 3*time.Second); s != nil {
					status = s
					reached = true
				}
			}
			mu.Lock()
			nodes = append(nodes, nodeSummary{Hostname: hostname, IP: ip, Status: status, Reached: reached})
			mu.Unlock()
		}()
	}
	wg.Wait()

	writeJSON(w, 200, map[string]interface{}{"nodes": nodes})
}

func apiFleetPreferences(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		writeJSON(w, 200, loadFleetPreferences())
		return
	}
	body := readBody(r)
	prefs := loadFleetPreferences()
	if mc, ok := body["mesh_config"]; ok {
		if m, ok := mc.(map[string]interface{}); ok {
			prefs.MeshConfig = make(map[string]string, len(m))
			for k, v := range m {
				prefs.MeshConfig[k] = fmt.Sprintf("%v", v)
			}
			dropEmptySecrets(prefs.MeshConfig)
		}
	}
	if np, ok := body["node_profiles"]; ok {
		if m, ok := np.(map[string]interface{}); ok {
			prefs.NodeProfiles = make(map[string]string, len(m))
			for k, v := range m {
				prefs.NodeProfiles[k] = fmt.Sprintf("%v", v)
			}
		}
	}
	if pr, ok := body["profiles"]; ok {
		if m, ok := pr.(map[string]interface{}); ok {
			prefs.Profiles = make(map[string]FleetProfile, len(m))
			for pid, pv := range m {
				if pm, ok := pv.(map[string]interface{}); ok {
					fp := FleetProfile{Name: fmt.Sprintf("%v", pm["name"])}
					fp.Config = make(map[string]string)
					if cfg, ok := pm["config"].(map[string]interface{}); ok {
						for k, v := range cfg {
							fp.Config[k] = fmt.Sprintf("%v", v)
						}
					}
					dropEmptySecrets(fp.Config)
					prefs.Profiles[pid] = fp
				}
			}
		}
	}
	if err := saveFleetPreferences(prefs); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"services": getAllServices()})
}

func meshMACLookup() map[string]map[string]string {
	lookup := make(map[string]map[string]string)
	reg := parseRegistry()
	for _, node := range reg {
		info := map[string]string{
			"hostname":  node["HOSTNAME"],
			"ip":        node["IPV4_ADDRESS"],
			"last_seen": node["LAST_SEEN_TIMESTAMP"],
		}
		if mac := normMAC(node["MAC_ADDRESS"]); mac != "" {
			lookup[mac] = info
		}
		for _, mac := range strings.Split(node["MAC_ADDRESSES"], ",") {
			mac = normMAC(mac)
			if mac != "" {
				lookup[mac] = info
			}
		}
	}
	return lookup
}

func apiRegistry(w http.ResponseWriter, r *http.Request) {
	registry := parseRegistry()
	myMAC := getMyMAC()

	type RegEntry struct {
		ID     string            `json:"id"`
		Fields map[string]string `json:"fields"`
		IsMe   bool              `json:"is_me"`
	}

	var entries []RegEntry
	for id, rn := range registry {
		isMe := normMAC(rn["MAC_ADDRESS"]) == myMAC
		fields := make(map[string]string)
		for k, v := range rn {
			if k != "id" {
				fields[k] = v
			}
		}
		entries = append(entries, RegEntry{ID: id, Fields: fields, IsMe: isMe})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes":     entries,
		"count":     len(entries),
		"timestamp": time.Now().Unix(),
	})
}

func apiMesh(w http.ResponseWriter, r *http.Request) {
	snap := cachedBatmanSnapshot()
	origMap := snap.OrigMap
	neighbors := snap.Neighbors
	gateways := snap.Gateways
	conf := loadKVFile(MeshConfFile)
	macInfo := meshMACLookup()

	bat0 := map[string]string{"state": "unknown", "algo": "", "gw_mode": ""}
	var bat0Addrs []string

	if out, err := runCmdStdout(5*time.Second, "ip", "-br", "addr", "show", "bat0"); err == nil {
		fields := strings.Fields(out)
		if len(fields) >= 2 {
			bat0["state"] = fields[1]
		}
		for _, f := range fields[2:] {
			bat0Addrs = append(bat0Addrs, f)
		}
	}
	if out, err := runCmdStdout(5*time.Second, "batctl", "routing_algo"); err == nil {
		if m := regexp.MustCompile(`bat0:\s*(\S+)`).FindStringSubmatch(out); len(m) > 1 {
			bat0["algo"] = m[1]
		} else {
			bat0["algo"] = strings.TrimSpace(out)
		}
	}
	if out, err := runCmdStdout(5*time.Second, "batctl", "gw_mode"); err == nil {
		bat0["gw_mode"] = strings.TrimSpace(out)
	}

	enrichMAC := func(mac string) map[string]interface{} {
		m := map[string]interface{}{"mac": mac}
		if info, ok := macInfo[mac]; ok {
			m["hostname"] = info["hostname"]
			m["ip"] = info["ip"]
			m["last_seen"] = info["last_seen"]
		}
		return m
	}

	now := time.Now().Unix()

	var origList []map[string]interface{}
	for mac, o := range origMap {
		entry := enrichMAC(mac)
		entry["tq"] = o.TQ
		entry["nexthop"] = o.Nexthop
		entry["iface"] = o.Iface
		entry["last_seen"] = fmt.Sprintf("%d", now-int64(o.LastSeen))
		if nhInfo, ok := macInfo[o.Nexthop]; ok {
			entry["nexthop_hostname"] = nhInfo["hostname"]
		}
		origList = append(origList, entry)
	}

	stations := map[string]*StationLink{}
	seenIface := map[string]bool{}
	for _, n := range neighbors {
		if n.Iface == "" || seenIface[n.Iface] {
			continue
		}
		seenIface[n.Iface] = true
		for mac, st := range runStationDump(n.Iface) {
			stations[mac] = st
		}
	}
	halowBW := getHalowDriverInfo("wlan2")["halow_bw"]

	var neighList []map[string]interface{}
	for _, n := range neighbors {
		entry := enrichMAC(n.MAC)
		entry["tq"] = n.TQ
		entry["iface"] = n.Iface
		entry["last_seen"] = fmt.Sprintf("%d", now-int64(n.LastSeen))
		if st, ok := stations[n.MAC]; ok {
			entry["link"] = buildLinkBudget(st, n.Iface, halowBW, n.RawTP)
		}
		neighList = append(neighList, entry)
	}

	var gwList []map[string]interface{}
	for _, gw := range gateways {
		entry := enrichMAC(gw.MAC)
		entry["tq"] = gw.TQ
		entry["selected"] = gw.Selected
		gwList = append(gwList, entry)
	}

	myHostname, _ := os.Hostname()
	state := loadKVFile(MeshStateFile)
	myIP := stateIP(state)

	var dnsRecords []map[string]interface{}
	reg := parseRegistry()
	for _, node := range reg {
		h := node["HOSTNAME"]
		ip := node["IPV4_ADDRESS"]
		if h == "" || ip == "" {
			continue
		}
		stale := false
		if ts := node["LAST_SEEN_TIMESTAMP"]; ts != "" {
			if seen, err := strconv.ParseInt(ts, 10, 64); err == nil {
				stale = (now - seen) > 300
			}
		}
		dnsRecords = append(dnsRecords, map[string]interface{}{
			"name": h + ".mesh", "ip": ip, "type": "node",
			"source": h, "stale": stale,
		})
	}
	dnsRecords = append(dnsRecords, map[string]interface{}{
		"name": "radio.mesh", "ip": myIP, "type": "local",
		"source": myHostname, "stale": false,
	})
	for _, rec := range collectAppletDNS(myIP, myHostname) {
		dnsRecords = append(dnsRecords, rec)
	}

	writeJSON(w, 200, map[string]interface{}{
		"bat0": map[string]interface{}{
			"state":   bat0["state"],
			"addrs":   bat0Addrs,
			"algo":    bat0["algo"],
			"gw_mode": bat0["gw_mode"],
		},
		"hostname":         myHostname,
		"halow_bw":         halowBW,
		"mesh_ssid":        conf["mesh_ssid"],
		"network":          confGet(conf, "ipv4_network", "10.30.2.0/24"),
		"originators":      origList,
		"neighbors":        neighList,
		"gateways":         gwList,
		"originator_count": len(origMap),
		"neighbor_count":   len(neighbors),
		"gateway_count":    len(gateways),
		"dns_records":      dnsRecords,
		"euds":             getEUDs(),
	})
}

// --- Control endpoints ---

func apiControlInterface(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	iface := jsonStr(body, "iface", "")
	state := jsonStr(body, "state", "")
	if iface == "" || (state != "up" && state != "down") {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid iface or state"})
		return
	}

	isHalow := false
	if out, err := runCmdStdout(3*time.Second, "ethtool", "-i", iface); err == nil {
		isHalow = strings.Contains(out, "morse_usb") || strings.Contains(out, "morse")
	}

	var cmds [][]string
	if isHalow {
		if state == "down" {
			cmds = [][]string{
				{"systemctl", "stop", "wpa_supplicant-s1g-" + iface + ".service"},
				{"ip", "link", "set", iface, "down"},
			}
		} else {
			cmds = [][]string{
				{"ip", "link", "set", iface, "up"},
				{"systemctl", "start", "wpa_supplicant-s1g-" + iface + ".service"},
			}
		}
	} else {
		if state == "down" {
			cmds = [][]string{
				{"batctl", "if", "del", iface},
				{"systemctl", "stop", "wpa_supplicant@" + iface + ".service"},
				{"ip", "link", "set", iface, "down"},
			}
		} else {
			cmds = [][]string{
				{"ip", "link", "set", iface, "up"},
				{"systemctl", "start", "wpa_supplicant@" + iface + ".service"},
			}
		}
	}

	for _, cmd := range cmds {
		runCmd(10*time.Second, cmd[0], cmd[1:]...)
	}

	if state == "up" && !isHalow {
		if out, err := runCmdStdout(5*time.Second, "batctl", "if"); err == nil {
			if !strings.Contains(out, iface) {
				runCmd(5*time.Second, "batctl", "if", "add", iface)
			}
		}
	}

	writeJSON(w, 200, map[string]interface{}{"ok": true, "iface": iface, "state": state})
}

func apiControlTxPower(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	iface := jsonStr(body, "iface", "")
	dbm := jsonFloat(body, "dbm", 0)
	if iface == "" || dbm == 0 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Missing iface or dbm"})
		return
	}

	cap := getIfaceTxPowerCap(iface)
	capF, _ := strconv.ParseFloat(cap, 64)
	if dbm > capF && capF > 0 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": fmt.Sprintf("Exceeds cap %s dBm", cap)})
		return
	}

	requested, actual, err := setIfaceTxPower(iface, dbm)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "iface": iface, "dbm": requested, "actual_dbm": actual, "cap": cap,
	})
}

func apiControlHalowChannel(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	channel := jsonInt(body, "channel", 0)
	bw := jsonStr(body, "bw", "1MHz")

	if channel < 1 || channel > 5 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid channel (1-5)"})
		return
	}

	freqKHz := HalowEUChannels[channel-1]
	bwMHz := strings.TrimSuffix(bw, "MHz")

	out, err := runCmd(5*time.Second, "morse_cli", "-i", "wlan2", "channel",
		"-c", strconv.Itoa(freqKHz), "-o", bwMHz, "-p", bwMHz)
	if err != nil {
		runCmd(5*time.Second, "systemctl", "restart", "wpa_supplicant-s1g-wlan2.service")
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "morse_cli failed: " + strings.TrimSpace(out)})
		return
	}

	// Write override file
	os.WriteFile("/var/run/halow-channel-override", []byte(fmt.Sprintf("%d,%s", channel, bw)), 0644)

	resp := map[string]interface{}{
		"ok": true, "channel": channel, "freq_khz": freqKHz, "bw": bw,
	}

	if dbm := jsonFloat(body, "dbm", 0); dbm > 0 {
		requested, actual, _ := setIfaceTxPower("wlan2", dbm)
		resp["dbm"] = requested
		resp["actual_dbm"] = actual
	}

	writeJSON(w, 200, resp)
}

func apiControlWifiChannel(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	iface := jsonStr(body, "interface", "")
	if iface == "" {
		iface = jsonStr(body, "iface", "")
	}
	channel := jsonInt(body, "channel", 0)

	if iface == "" || channel == 0 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Missing interface or channel"})
		return
	}

	freq := wifiChannelToFreq(iface, channel)
	if freq == 0 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid channel for interface"})
		return
	}

	// Update wpa_supplicant config
	confPath := fmt.Sprintf("/etc/wpa_supplicant/wpa_supplicant-%s.conf", iface)
	if data, err := os.ReadFile(confPath); err == nil {
		text := string(data)
		freqStr := fmt.Sprintf("frequency=%d", freq)
		freqRE := regexp.MustCompile(`frequency=\d+`)
		if freqRE.MatchString(text) {
			text = freqRE.ReplaceAllString(text, freqStr)
		} else {
			text = strings.Replace(text, "}", "\t"+freqStr+"\n}", 1)
		}
		os.WriteFile(confPath, []byte(text), 0644)
	}

	runCmd(5*time.Second, "systemctl", "restart", "wpa_supplicant@"+iface+".service")

	resp := map[string]interface{}{"ok": true, "iface": iface, "channel": channel, "frequency": freq}

	if dbm := jsonFloat(body, "dbm", 0); dbm > 0 {
		requested, actual, _ := setIfaceTxPower(iface, dbm)
		resp["dbm"] = requested
		resp["actual_dbm"] = actual
	}

	writeJSON(w, 200, resp)
}

func apiControlHostname(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	prefix := jsonStr(body, "hostname", "")
	prefix = regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(prefix, "")
	if prefix == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Empty hostname"})
		return
	}

	conf := loadKVFile(MeshConfFile)
	meshSSID := conf["mesh_ssid"]
	macSuffix := getMACsuffix()

	full := prefix
	if meshSSID != "" {
		full += "-" + meshSSID
	}
	if macSuffix != "" {
		full += "-" + macSuffix
	}

	saveKVFile(MeshConfFile, map[string]string{"node_hostname": prefix})
	setHostname(full)

	writeJSON(w, 200, map[string]interface{}{"ok": true, "hostname": full})
}

// --- Admin endpoints ---

var saveableKeys = map[string]bool{
	"node_hostname": true, "eud": true, "lan_ap_ssid": true, "lan_ap_key": true,
	"lan_ap_channel": true, "lan_ap_bw": true,
	"max_euds_per_node": true, "mesh_ssid": true, "mesh_key": true,
	"ipv4_network": true, "regulatory_domain": true, "halow_bw": true, "halow_channel": true, "halow_duty_cycle": true, "halow_txpower_dbm": true, "mesh_5ghz_bw": true, "mesh_5ghz_channel": true,
	"acs":             true,
	"battery_monitor": true, "admin_password": true, "require_auth": true, "ui_uplink_access": true, "ssh_uplink_access": true,
	"gateway": true, "gateway_nat": true, "gateway_mss_clamp": true, "gateway_bandwidth": true,
	"multicast_mode":   true,
	"voice_mic_volume": true, "voice_speaker_volume": true,
	"voice_channel": true, "voice_rx_channels": true,
	"voice_ptt_mode": true, "voice_gain": true, "voice_enabled": true,
	"voice_beep_tx_start": true, "voice_beep_rx_end": true,
	"dns_servers":   true,
	"eud_bandwidth": true,
	"qos_enabled":   true, "qos_voice_band": true, "qos_cot_band": true, "qos_chat_band": true,
	"auto_update": true, "update_url": true, "auto_update_overlay": true, "auto_update_min_mbps": true,
	"gps": true, "gps_source": true, "gps_static_lat": true, "gps_static_lon": true, "gps_static_alt": true,
	"callsign": true, "cot_type": true, "cot_team": true, "cot_role": true, "cot_icon": true,
}

// keyDescriptions documents a subset of saveableKeys for `mesh config keys`
// and any future admin UI. Keys with no entry here still show up in
// apiConfigKeys, just without a description.
var keyDescriptions = map[string]string{
	"node_hostname":        "Hostname prefix for this node (full hostname adds mesh SSID + MAC suffix)",
	"eud":                  "Enable End User Device access (WiFi AP / wired bridge)",
	"lan_ap_ssid":          "SSID for the EUD-facing WiFi access point",
	"lan_ap_key":           "WPA2-PSK passphrase for the EUD-facing WiFi access point",
	"lan_ap_channel":       "Channel for the EUD-facing WiFi access point",
	"lan_ap_bw":            "Channel bandwidth for the EUD-facing WiFi access point",
	"max_euds_per_node":    "Maximum number of EUD clients this node will serve",
	"mesh_ssid":            "Mesh network name shared by all nodes",
	"mesh_key":             "SAE (WPA3) passphrase for the mesh backhaul",
	"ipv4_network":         "Base IPv4 CIDR the mesh allocates node addresses from",
	"regulatory_domain":    "Country code (e.g. US, NL) for all radios: the Wi-Fi radios use it directly, HaLow uses the EU plan for EU countries and the same country otherwise. Applies at the next reboot.",
	"halow_bw":             "802.11ah HaLow channel bandwidth — EU domain supports 1MHz only; changing regulatory domain/bandwidth changes the on-air channel, so roll out to all HaLow nodes together, not one at a time",
	"halow_channel":        "802.11ah HaLow channel (empty = Auto, domain/bandwidth default)",
	"halow_duty_cycle":     "HaLow duty cycling: off (no airtime limit) or auto (the driver's regional limit: none in US, 10%/2.8% in EU). Empty = off on the EU plan, auto elsewhere. Applies at the next reboot. Off in EU exceeds the ETSI 863-868 MHz duty-cycle rules.",
	"halow_txpower_dbm":    "HaLow TX power request and driver cap in dBm (1-30). Empty = per-bandwidth default (24/24/22/20 dBm for 1/2/4/8 MHz). The Morse regulatory table (EU 16 dBm EIRP, US 30) and the board's BCF still cap it; the radio reports what it actually uses. Applies at the next reboot.",
	"mesh_5ghz_bw":         "5GHz mesh channel width: 20 (deterministic peering, default), 40 or 80 (higher throughput — 40 requires the patched wpa_supplicant and silently falls back to 20 without it; 80 without the patch can mismatch primary channel between nodes) — fleet-wide, never mixed per node",
	"mesh_5ghz_channel":    "5GHz mesh channel number to pin the static-mode (acs=n) data channel to — valid: 36, 40, 44, 48, 149, 153, 157, 161, 165 (last five US-only, illegal under ETSI); unrecognized/absent falls back to the default lobby channel; has no effect when acs=y",
	"acs":                  "5GHz/2.4GHz mesh channel selection mode: n (default) pins static channels, y runs automatic channel selection/election — live, applies within one 15s node-manager tick, no restart needed",
	"battery_monitor":      "Enable Waveshare UPS HAT battery monitoring",
	"admin_password":       "Password gating write/control API access when require_auth is set",
	"require_auth":         "Require admin_password for control/config endpoints",
	"ui_uplink_access":     "Allow the web UI (HTTPS) from this node's uplink network, e.g. a monitoring workstation on a gateway's LAN: y/n (default n, mesh side only)",
	"ssh_uplink_access":    "Allow SSH from this node's uplink network, e.g. lab troubleshooting from the LAN: y/n (default n, mesh side only)",
	"gateway":              "Enable gateway election and internet uplink for the mesh",
	"gateway_nat":          "Enable NAT/masquerade on the elected gateway node",
	"gateway_mss_clamp":    "Enable TCP MSS clamping on the gateway uplink",
	"gateway_bandwidth":    "Uplink bandwidth cap advertised by the gateway",
	"multicast_mode":       "Mesh multicast handling: flood or optimized (IGMP snooping)",
	"voice_mic_volume":     "PTT microphone input gain",
	"voice_speaker_volume": "PTT speaker output volume",
	"voice_channel":        "Default PTT voice channel",
	"voice_rx_channels":    "Additional PTT channels to receive on",
	"voice_ptt_mode":       "Hardware PTT trigger mode",
	"voice_gain":           "PTT audio gain applied before encoding",
	"voice_enabled":        "Enable the PTT voice service",
	"voice_beep_tx_start":  "Play a beep when PTT transmission starts",
	"voice_beep_rx_end":    "Play a beep when incoming PTT transmission ends",
	"dns_servers":          "Upstream DNS servers for .mesh resolution fallthrough",
	"eud_bandwidth":        "Bandwidth cap applied to connected EUD clients",
	"qos_enabled":          "Enable tc prio QoS bands on br0",
	"qos_voice_band":       "QoS priority band assigned to voice traffic",
	"qos_cot_band":         "QoS priority band assigned to CoT traffic",
	"qos_chat_band":        "QoS priority band assigned to chat/bulk traffic",
	"auto_update":          "Enable automatic OTA tools tarball updates",
	"update_url":           "URL node-update polls for tarball updates",
	"auto_update_overlay":  "Enable automatic overlay (no-rollback) updates",
	"auto_update_min_mbps": "Minimum measured bandwidth required before an auto-update proceeds",
	"gps":                  "Enable GPS (gpsd) on this node",
	"gps_source":           "GPS source: receiver (gpsd) or static",
	"gps_static_lat":       "Static latitude reported when gps_source=static",
	"gps_static_lon":       "Static longitude reported when gps_source=static",
	"gps_static_alt":       "Static altitude reported when gps_source=static",
	"callsign":             "Callsign used in CoT position reports",
	"cot_type":             "CoT type code broadcast for this node's position",
	"cot_team":             "CoT team/affiliation for this node's position",
	"cot_role":             "CoT role for this node's position",
	"cot_icon":             "CoT icon override for this node's position",
}

func apiConfigKeys(w http.ResponseWriter, r *http.Request) {
	keys := make([]map[string]interface{}, 0, len(saveableKeys))
	for k := range saveableKeys {
		keys = append(keys, map[string]interface{}{
			"key":         k,
			"description": keyDescriptions[k],
		})
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i]["key"].(string) < keys[j]["key"].(string)
	})
	writeJSON(w, 200, map[string]interface{}{"keys": keys})
}

func apiAdminSave(w http.ResponseWriter, r *http.Request) {
	if getPendingConfig() != nil {
		writeJSON(w, 409, map[string]interface{}{"ok": false, "error": "Cannot save while fleet config is staged — activate or cancel it first"})
		return
	}
	body := readBody(r)
	configRaw, ok := body["config"]
	if !ok {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Missing config"})
		return
	}
	configMap, ok := configRaw.(map[string]interface{})
	if !ok {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid config format"})
		return
	}

	updates := make(map[string]string)
	for k, v := range configMap {
		if saveableKeys[k] {
			updates[k] = fmt.Sprintf("%v", v)
		}
	}
	// An empty string for a secret key must never be interpreted as "set
	// this secret to blank" -- only as "this field was left untouched,"
	// typically because the caller's form was populated from a redacted
	// (unauthenticated) /api/admin/status response before they logged in.
	// See dropEmptySecrets's doc comment (admin.go) for the full incident
	// this guards against.
	dropEmptySecrets(updates)

	if len(updates) == 0 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "No valid keys"})
		return
	}

	var saved []string
	for k := range updates {
		saved = append(saved, k)
	}

	if err := validateConfigUpdates(updates); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	existingConf := loadKVFile(MeshConfFile)

	// Validate halow_bw/halow_channel/regulatory_domain against the real
	// per-domain HaLow channel table before anything is persisted — an
	// invalid combination (e.g. an explicit channel illegal for this
	// domain/bw, or EU + a bandwidth other than 1MHz, a genuine hardware
	// constraint) must reject the whole save rather than silently
	// coercing or ignoring it.
	_, bwSubmitted := updates["halow_bw"]
	_, chSubmitted := updates["halow_channel"]
	_, rdSubmitted := updates["regulatory_domain"]
	if bwSubmitted || chSubmitted || rdSubmitted {
		effective := make(map[string]string, len(existingConf)+len(updates))
		for k, v := range existingConf {
			effective[k] = v
		}
		for k, v := range updates {
			effective[k] = v
		}
		domain := resolveHalowDomain(effective)
		if err := validateHalowChannel(domain, effectiveHalowBW(effective), effective["halow_channel"]); err != nil {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
	}
	// Narrow trigger: only fires when mesh_5ghz_channel is actually being
	// changed to a new value, not merely present in the submission --
	// config.js's configSave submits every meshFields entry on every save
	// (not just edited ones), so without the != existingConf comparison
	// this would re-validate the already-persisted channel on every
	// unrelated save (e.g. changing the hostname), rejecting the whole
	// save with a 400 on any node with a channel pinned outside its
	// resolved domain's list.
	//
	// Deliberately not widened to also fire on a bare regulatory_domain
	// change (unlike the HaLow block above, which does fire on domain
	// change): a US->EU domain switch on a node already pinned to e.g.
	// channel 149 would then reject the *entire* config save with an error
	// that doesn't clearly point at the 5GHz channel field as the problem.
	// Known, accepted gap -- not fixed here.
	if ch, ok := updates["mesh_5ghz_channel"]; ok && ch != "" && ch != existingConf["mesh_5ghz_channel"] {
		effective := make(map[string]string, len(existingConf)+len(updates))
		for k, v := range existingConf {
			effective[k] = v
		}
		for k, v := range updates {
			effective[k] = v
		}
		if err := validateMesh5GHzChannel(resolveMesh5GHzDomain(effective), ch); err != nil {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
	}

	expandNodeTemplates(updates, existingConf)
	if err := saveKVFile(MeshConfFile, updates); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	applied := make(map[string]interface{})
	conf := loadKVFile(MeshConfFile)

	// admin_password is now the fleet encryption key (see fleetcrypto.go) --
	// changing it via a per-node LOCAL save (as opposed to a fleet-wide
	// Stage + Activate push) silently splits this node onto a different key
	// than the rest of the mesh: it will fail to seal/open any subsequent
	// slot 70/71 package until every other node receives the identical
	// change. Log it prominently and surface it back to the caller so the
	// UI can warn the operator, rather than let this happen invisibly.
	var warnings []string
	if newPW, ok := updates["admin_password"]; ok && newPW != existingConf["admin_password"] {
		log.Printf("admin: WARNING - admin_password changed via local /api/admin/save; this node now derives a DIFFERENT fleet crypto key than the rest of the mesh until every other node receives this exact change via a fleet-wide Stage+Activate push")
		warnings = append(warnings, "admin_password changed locally — this node now uses a different fleet encryption key than the rest of the mesh until this same change is pushed fleet-wide via Stage + Activate")
	}

	// Apply hostname. Skip when no prefix is configured — falling back to
	// the "node" default here is how nodes ended up renamed to node-<mac>.
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
		applied["hostname"] = full
	}

	// Apply gateway config
	if updates["gateway"] != "" || updates["gateway_nat"] != "" || updates["gateway_mss_clamp"] != "" || updates["gateway_bandwidth"] != "" {
		runCmd(5*time.Second, "systemctl", "reload", "gateway-manager")
		applied["gateway_reloaded"] = true
	}

	// Apply EUD bandwidth cap
	if updates["eud_bandwidth"] != "" {
		applyEUDBandwidth(conf["eud_bandwidth"])
		applied["eud_bandwidth_applied"] = true
	}

	// Apply EUD mode changes
	if updates["eud"] != "" {
		eud := conf["eud"]
		if eudWantsAP(eud) {
			// The reconcile script itself selects/regenerates the AP
			// interface (hostapd.conf, ap-interface-setup.service,
			// ap-txpower.service) and stops any stale mesh
			// wpa_supplicant on it — must run before hostapd is
			// (re)started so it picks up a config that actually
			// targets the current AP interface, not a stale one.
			// 60s budget: on this path the script itself restarts 4
			// services sequentially, two of them oneshot units with a
			// 2s ExecStartPre sleep each — a shorter timeout risks
			// SIGKILLing it mid-sequence, leaving e.g. ap-txpower.service
			// never applied while api.go's own follow-up calls still
			// report success.
			if out, err := runCmd(60*time.Second, "manet-wlan-reconcile.sh"); err != nil {
				log.Printf("manet-wlan-reconcile: %v (%s)", err, strings.TrimSpace(out))
			} else if strings.TrimSpace(out) != "" {
				log.Printf("manet-wlan-reconcile: %s", strings.TrimSpace(out))
			}
			runCmd(5*time.Second, "systemctl", "enable", "hostapd")
			runCmd(5*time.Second, "systemctl", "start", "hostapd")
		} else if eud == "wired" || eud == "none" {
			runCmd(5*time.Second, "systemctl", "stop", "hostapd")
			// The radio that was AP just got reclassified as mesh in
			// /var/lib/mesh_if, but never had a wpa_supplicant config
			// generated (that only happens once, at first provisioning)
			// and ap-txpower.service still holds its txpower fixed low —
			// reconcile both now rather than leaving it a non-functional
			// mesh radio until the node is fully re-provisioned.
			if out, err := runCmd(60*time.Second, "manet-wlan-reconcile.sh"); err != nil {
				log.Printf("manet-wlan-reconcile: %v (%s)", err, strings.TrimSpace(out))
			} else if strings.TrimSpace(out) != "" {
				log.Printf("manet-wlan-reconcile: %s", strings.TrimSpace(out))
			}
		}
		applied["eud_mode_applied"] = true
	}

	// Apply AP settings. Gated on eud actually wanting an AP: the web UI's
	// Config tab always resends the node's current (unchanged) lan_ap_*
	// values on every save, not just when the user edited them, so this
	// would otherwise fire on an eud=wired/none save too -- restarting
	// hostapd right after the eud block above may have just stopped and
	// disabled it.
	apChanged := updates["lan_ap_ssid"] != "" || updates["lan_ap_key"] != "" ||
		updates["lan_ap_channel"] != "" || updates["lan_ap_bw"] != ""
	if apChanged && eudWantsAP(conf["eud"]) {
		applyHostapdConfig(conf)
		runCmd(10*time.Second, "systemctl", "restart", "hostapd")
		applied["ap_restarted"] = true
	}

	// Apply DHCP pool changes
	if updates["max_euds_per_node"] != "" || updates["ipv4_network"] != "" {
		runCmd(10*time.Second, "systemctl", "restart", "mesh-manager")
		applied["mesh_manager_restarted"] = true
	}

	// Apply HaLow bandwidth/channel. Validated above (before saveKVFile),
	// so conf["halow_channel"] here is guaranteed either empty (Auto) or a
	// legal channel for conf's domain/bw.
	if bwSubmitted || chSubmitted || rdSubmitted {
		if applyHalowBW(conf) {
			applied["halow_bw_applied"] = true
		}
	}

	// Region, HaLow duty cycle and HaLow power: module options and radio
	// configs. Idempotent, so it runs on every save that carries one of
	// them (config.js always sends them).
	_, dutySubmitted := updates["halow_duty_cycle"]
	_, powerSubmitted := updates["halow_txpower_dbm"]
	if rdSubmitted || bwSubmitted || dutySubmitted || powerSubmitted {
		changed, err := applyRadioConfigFiles(conf)
		if err != nil {
			log.Printf("radio config: %v", err)
			warnings = append(warnings, err.Error())
		}
		if len(changed) > 0 {
			applied["radio_reboot_required"] = true
			warnings = append(warnings, "Region / HaLow duty cycle / HaLow power written to the radio module options; reboot this node for the radios to use them")
		}
	}

	// Apply mesh key/SSID changes to wpa_supplicant configs. Narrow
	// trigger, mirroring mesh_5ghz_channel's guard above: config.js
	// resends every field on every save, not just edited ones, so without
	// the != existingConf comparison this restarts wpa_supplicant on
	// every mesh radio (tearing down every plink) on any unrelated save.
	if (updates["mesh_ssid"] != "" && updates["mesh_ssid"] != existingConf["mesh_ssid"]) ||
		(updates["mesh_key"] != "" && updates["mesh_key"] != existingConf["mesh_key"]) {
		applyWPAConfig(conf)
		applied["mesh_updated"] = true
	}

	// Apply multicast mode
	if updates["multicast_mode"] != "" {
		applyMulticastMode(updates["multicast_mode"])
		applied["multicast_applied"] = true
	}

	if uplinkAccessChanged(updates, existingConf) {
		runUIFirewall()
		applied["ui_firewall_applied"] = true
	}

	// Apply voice volume
	if updates["voice_mic_volume"] != "" || updates["voice_speaker_volume"] != "" {
		applyVoiceVolume(conf)
		applied["voice_volume_applied"] = true
	}

	// Apply voice_enabled: stop the daemon outright on disable (mesh-voice
	// exits cleanly on its own if restarted while disabled, but stopping it
	// directly avoids a pointless restart cycle and updates the UI's
	// running-state immediately rather than after RestartSec).
	if updates["voice_enabled"] != "" {
		if conf["voice_enabled"] == "n" {
			runCmd(5*time.Second, "systemctl", "stop", "mesh-voice")
		} else {
			runCmd(5*time.Second, "systemctl", "restart", "mesh-voice")
		}
		applied["voice_enabled_applied"] = true
	}

	// Apply gps: stop gpsd/gps-reader outright on disable — this hardware
	// has no GPS module, no point leaving either running. cot-emitter stays
	// untouched; its EUD relay is GPS-independent. Enabling on a node that
	// was originally provisioned with gps=n needs gpsd installed first —
	// radio-setup.sh only apt-installs it when gps=y at boot.
	//
	// enable/disable alongside stop/restart: radio-setup.sh sets the
	// boot-time enabled state once, at first provisioning, and never
	// re-runs on a live gps= change — a stop/restart-only toggle here
	// looks like it worked but silently reverts on the node's next
	// reboot in both directions.
	//
	// gps_source=static (a node with no receiver reporting a fixed
	// position, e.g. a stationary gateway) never needs gpsd at all --
	// gps-reader itself re-reads gps_source/gps_static_* on every poll
	// tick and writes the configured position straight into
	// /run/gps_status.json, so no restart is needed here for lat/lon/alt
	// edits alone, only for gps or gps_source actually changing.
	if updates["gps"] != "" || updates["gps_source"] != "" {
		if conf["gps"] == "n" {
			runCmd(5*time.Second, "systemctl", "disable", "--now", "gps-reader")
			// gpsd.socket must be disabled too, not just gpsd.service — if
			// the socket unit stays enabled, systemd's socket activation
			// silently respawns gpsd the next time anything connects to
			// its port, undoing the disable within seconds (confirmed live:
			// the service stops, then "Starting gpsd.service..." appears in
			// the journal ~15-25s later with no explicit request).
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
		applied["gps_applied"] = true
	}

	// Apply battery_monitor: like voice/gps, battery-reader only re-reads its
	// enable flag at startup, so a live battery_monitor=n edit otherwise
	// leaves the running daemon retrying an absent HAT every 30s until the
	// next reboot. Stop (and disable, so it stays off across reboots) on
	// disable; enable + start on re-enable.
	if updates["battery_monitor"] != "" {
		if conf["battery_monitor"] == "n" {
			runCmd(5*time.Second, "systemctl", "disable", "--now", "battery-reader")
		} else {
			runCmd(5*time.Second, "systemctl", "enable", "--now", "battery-reader")
		}
		applied["battery_monitor_applied"] = true
	}

	// Apply CoT identity changes (callsign/type/team/role/icon) — cot-emitter
	// reads these once at startup, so a live edit needs a restart to take
	// effect. configSave() always submits every field on the Config page,
	// including ones intentionally cleared back to blank (e.g. reverting
	// cot_team to "no team affiliation"), so check key presence in updates
	// rather than non-blank value — a blank submission is still a real edit.
	cotIdentityKeys := []string{"callsign", "cot_type", "cot_team", "cot_role", "cot_icon"}
	cotIdentityChanged := false
	for _, k := range cotIdentityKeys {
		if _, ok := updates[k]; ok {
			cotIdentityChanged = true
			break
		}
	}
	if cotIdentityChanged {
		runCmd(5*time.Second, "systemctl", "restart", "cot-emitter")
		applied["cot_identity_applied"] = true
	}

	// Apply voice PTT mode / channel changes
	if conf["voice_enabled"] != "n" && (updates["voice_ptt_mode"] != "" || updates["voice_channel"] != "" || updates["voice_gain"] != "" ||
		updates["voice_beep_tx_start"] != "" || updates["voice_beep_rx_end"] != "") {
		txCh := int(voiceTxCh.Load())
		if txCh <= 0 {
			txCh = 1
		}
		voiceRestartDaemon(txCh)
		applied["voice_restarted"] = true
	}

	// Apply DNS servers
	if updates["dns_servers"] != "" {
		applyDNSServers(updates["dns_servers"])
		applied["dns_applied"] = true
	}

	// Apply QoS
	if updates["qos_enabled"] != "" || updates["qos_voice_band"] != "" || updates["qos_cot_band"] != "" || updates["qos_chat_band"] != "" {
		applyQoSFromConf(conf)
		applied["qos_applied"] = true
	}

	if updates["auto_update"] != "" || updates["update_url"] != "" || updates["auto_update_overlay"] != "" || updates["auto_update_min_mbps"] != "" {
		runCmd(5*time.Second, "systemctl", "reload", "node-update")
		applied["node_update_reloaded"] = true
	}

	go func() {
		args := []string{"config-change"}
		for _, k := range saved {
			args = append(args, "KEY="+k)
		}
		exec.Command("/usr/local/bin/mesh-hook", args...).Run()
	}()

	writeJSON(w, 200, map[string]interface{}{"ok": true, "saved": saved, "applied": applied, "warnings": warnings})
}

func apiAdminStage(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	configRaw, ok := body["config"]
	if !ok {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Missing config"})
		return
	}
	configMap, ok := configRaw.(map[string]interface{})
	if !ok {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid config format"})
		return
	}
	// See dropEmptySecrets's doc comment (admin.go): an empty secret value
	// must never be interpreted as "blank this out," only as "this field
	// was left untouched" (e.g. the staging form was populated from a
	// redacted, pre-login /api/admin/status response).
	dropEmptySecretsAny(configMap)

	currentConf := loadKVFile(MeshConfFile)
	if currentConf["admin_password"] == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "admin_password must be set on this node before a fleet config can be securely staged"})
		return
	}

	now := time.Now()
	if now.Year() < 2025 {
		// These boards have no RTC. Staging while the local clock looks
		// unset would produce a staged_at that, once applied, becomes this
		// node's new highestAppliedStagedAt watermark (see fleetreplay.go) —
		// permanently poisoning it (e.g. far in the past, or worse, wildly
		// wrong in a way that later looks like every subsequent legitimate
		// push is a rollback). Refuse outright rather than risk creating a
		// bad watermark that then requires SSH on every node to recover
		// from.
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "local clock looks unset (system time is before 2025) — refusing to stage until the clock is correct"})
		return
	}

	strConf := make(map[string]string)
	for k, v := range configMap {
		strConf[k] = fmt.Sprintf("%v", v)
	}
	if err := validateConfigUpdates(strConf); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	version := makeConfigVersion(strConf)

	// Reuses the exact same fleetDangerousKeys list AND comparison function
	// apiAdminActivate's real safety gate (and assembleAdminStatus's
	// ack_status) use -- previously this was a separately hardcoded 3-key
	// check (missing admin_password), so staging an admin_password rotation
	// could momentarily show dangerous:false in the immediate stage
	// response before ack_status caught up a few seconds later. The actual
	// Activate-time gate was never affected; this only fixes the stage-time
	// UI signal to match it.
	dangerous := len(dangerousKeyChanges(configMap, currentConf)) > 0

	prefs := loadFleetPreferences()

	pkgID, err := newPkgID()
	if err != nil {
		log.Printf("fleet: failed to generate pkg_id for stage: %v", err)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "internal error generating package id"})
		return
	}

	// staged_at must be strictly greater than the highest staged_at this
	// node has ever actually applied — never merely time.Now().Unix() on
	// its own. A clock a few seconds slow, or two stages within the same
	// wall-clock second, would otherwise produce a staged_at at or below
	// the watermark, and every node (including this one) would reject the
	// freshly-staged package outright as a rollback replay (fleetProcessPackage).
	stagedAt := now.Unix()
	if wm := highestAppliedStagedAt(); wm >= stagedAt {
		stagedAt = wm + 1
	}

	// NOTE: this ordering is security-load-bearing for admin_password
	// rotation. fleetSaveAndStage() in fleet.js calls /api/admin/preferences
	// (fleet mesh-config prefs only) and then THIS endpoint — it never calls
	// /api/admin/save first. So the package below is always sealed
	// (broadcastConfigPackage, admin.go) under the admin_password that is
	// CURRENTLY on disk, and a new password travels to every other node only
	// inside this ciphertext. If that frontend ordering ever changes (a
	// local save of the new password before staging), a rotation would
	// silently seal itself under the password it's trying to replace.
	pkg := map[string]interface{}{
		"version":       version,
		"config":        configMap,
		"profiles":      prefs.Profiles,
		"node_profiles": prefs.NodeProfiles,
		"staged_by":     getMyHostname(),
		"staged_at":     stagedAt,
		"pkg_id":        pkgID,
		"sender_mac":    getMyMAC(),
		"expires_at":    now.Add(1 * time.Hour).Unix(),
	}

	if !broadcastConfigPackage(pkg) {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "failed to seal/broadcast staged config (admin_password unset, or alfred error) — check logs"})
		return
	}
	// Check both writes before declaring success: if either fails, this
	// node has already broadcast a package it can't locally track as
	// pending/ACKed — better to surface that loudly than silently proceed
	// as if staging fully succeeded (mirrors apiAdminActivate's pattern).
	if err := savePendingConfig(pkg); err != nil {
		log.Printf("admin: failed to save pending config after staging broadcast: %v", err)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "staged config was broadcast but failed to save locally — check logs"})
		return
	}
	if err := os.WriteFile(AckVersionFile, []byte(version), 0644); err != nil {
		log.Printf("admin: failed to write local ack version after staging: %v", err)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "staged config was broadcast but failed to record local ACK — check logs"})
		return
	}
	go fleetMcastSendAck(version)

	writeJSON(w, 200, map[string]interface{}{"ok": true, "version": version, "dangerous": dangerous})
}

func apiAdminActivate(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	force := jsonBool(body, "force")

	pending := getPendingConfig()
	if pending == nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "No pending config"})
		return
	}

	var pkg map[string]interface{}
	json.Unmarshal(pending, &pkg)

	version := jsonStr(pkg, "version", "")

	if !force {
		// ackStatus is the SAME merged registry+local-ack+multicast, ACTIVE-
		// filtered computation assembleAdminStatus uses for the UI's
		// Activate-button state — see admin.go for why using two different
		// computations here used to let the UI show 4/4 while this gate
		// still rejected.
		acked, total, missing := ackStatus(version)
		if total > 0 && acked < total {
			writeJSON(w, 400, map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("%d nodes have not ACKed: %s", len(missing), strings.Join(missing, ", ")),
			})
			return
		}

		// Even with full ACK from every ACTIVE node, a push that changes the
		// fleet crypto key or partitions mesh membership/addressing must not
		// silently orphan a node that's currently OFFLINE — it can come back
		// unable to open any future fleet package (or rejoin the mesh at
		// all) under the new value, with no operator visibility. Use the
		// same dangerousKeyChanges/offlineNodeNames pair assembleAdminStatus
		// already surfaces in ack_status, so the UI and this gate agree.
		if configRaw, ok := pkg["config"].(map[string]interface{}); ok {
			if changedKeys := dangerousKeyChanges(configRaw, loadKVFile(MeshConfFile)); len(changedKeys) > 0 {
				if offline := offlineNodeNames(); len(offline) > 0 {
					writeJSON(w, 400, map[string]interface{}{
						"ok": false,
						"error": fmt.Sprintf("this push changes %s while %d node(s) are offline, risking orphaning them: %s — use force to proceed anyway",
							strings.Join(changedKeys, ", "), len(offline), strings.Join(offline, ", ")),
						"dangerous_keys": changedKeys,
						"offline_nodes":  offline,
					})
					return
				}
			}
		}
	}

	activateAt := time.Now().Add(60 * time.Second).Unix()
	pkg["activate_at"] = activateAt

	// Check broadcastConfigPackage's return before persisting anything
	// locally: it can fail (seal error, alfred error, or admin_password
	// unset), and if it does, this node must NOT be left believing
	// activation is in progress while the rest of the fleet never received
	// it — mirrors apiAdminStage's already-fixed pattern.
	if !broadcastConfigPackage(pkg) {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "failed to seal/broadcast activation (admin_password unset, or alfred error) — check logs; this node has NOT been marked to activate"})
		return
	}
	if err := savePendingConfig(pkg); err != nil {
		log.Printf("admin: failed to save pending config after activation broadcast: %v", err)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "activation was broadcast but failed to save locally — check logs"})
		return
	}
	go fleetMcastSendActivation(version, activateAt)

	writeJSON(w, 200, map[string]interface{}{"ok": true, "activate_at": activateAt})
}

func apiAdminCancel(w http.ResponseWriter, r *http.Request) {
	clearPendingConfig()
	// Keep AckVersionFile so fleetPollAlfred won't re-stage the same version from Alfred
	// Clear the Alfred slot to stop other nodes from picking it up
	cmd := exec.Command("alfred", "-s", "70")
	cmd.Stdin = strings.NewReader("")
	cmd.Run()
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiAdminDeleteNode(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	nodeID, _ := body["id"].(string)
	if nodeID == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "missing node id"})
		return
	}
	nodeID = strings.ReplaceAll(strings.ToLower(nodeID), ":", "")

	data, err := os.ReadFile(RegistryFile)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "cannot read registry"})
		return
	}

	prefix := "NODE_" + nodeID + "_"
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
		}
	}

	if err := os.WriteFile(RegistryFile, []byte(strings.Join(kept, "\n")), 0644); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]interface{}{"ok": true, "deleted": nodeID})
}

// --- Service action ---

func apiServiceAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "missing service id"})
		return
	}
	serviceID := parts[3]
	body := readBody(r)
	action := jsonStr(body, "action", "")
	if action == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "missing action"})
		return
	}
	ok, errMsg := serviceAction(serviceID, action)
	writeJSON(w, 200, map[string]interface{}{"ok": ok, "error": errMsg})
}

// --- Perf endpoints ---

var (
	activeStreams  = make(map[string]*exec.Cmd)
	activeStreamMu sync.Mutex
)

func killStream(key string) {
	activeStreamMu.Lock()
	defer activeStreamMu.Unlock()
	if cmd, ok := activeStreams[key]; ok {
		cmd.Process.Kill()
		cmd.Wait()
		delete(activeStreams, key)
	}
}

func apiIperfServerStart(w http.ResponseWriter, r *http.Request) {
	exec.Command("pkill", "-f", "iperf3 -s").Run()
	cmd := exec.Command("iperf3", "-s", "--one-off", "-J", "--logfile", "/tmp/iperf3-server.log")
	if err := cmd.Start(); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiIperfServerStop(w http.ResponseWriter, r *http.Request) {
	exec.Command("pkill", "-f", "iperf3 -s").Run()
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiIperfClientRun(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	serverIP := jsonStr(body, "server_ip", "")
	testType := jsonStr(body, "test_type", "tcp_1stream")
	duration := jsonInt(body, "duration", 30)
	bitrate := jsonStr(body, "bitrate", "4M")
	parallel := jsonInt(body, "parallel", 1)
	reverse := jsonBool(body, "reverse")

	if serverIP == "" || !validateTargetRE.MatchString(serverIP) {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid server_ip"})
		return
	}

	args := []string{"-c", serverIP, "-t", strconv.Itoa(duration), "-J"}
	if strings.HasPrefix(testType, "udp") {
		args = append(args, "-u", "-b", bitrate)
	}
	if parallel > 1 {
		args = append(args, "-P", strconv.Itoa(parallel))
	}
	if reverse {
		args = append(args, "-R")
	}

	timeout := time.Duration(duration+15) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "iperf3", args...).CombinedOutput()
	var result interface{}
	json.Unmarshal(out, &result)

	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": err.Error(), "result": result})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "result": result})
}

func apiIperfClientStream(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	serverIP := jsonStr(body, "server_ip", "")
	testType := jsonStr(body, "test_type", "tcp_1stream")
	duration := jsonInt(body, "duration", 30)
	bitrate := jsonStr(body, "bitrate", "4M")

	if !validateTargetRE.MatchString(serverIP) {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid server_ip"})
		return
	}

	killStream("iperf")
	args := []string{"-c", serverIP, "-t", strconv.Itoa(duration), "--forceflush"}
	if strings.HasPrefix(testType, "udp") {
		args = append(args, "-u", "-b", bitrate)
	}

	cmd := exec.Command("iperf3", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	activeStreamMu.Lock()
	activeStreams["iperf"] = cmd
	activeStreamMu.Unlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	cmd.Wait()
	killStream("iperf")
}

func apiIperfStop(w http.ResponseWriter, r *http.Request) {
	killStream("iperf")
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiPingRun(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	target := jsonStr(body, "target", "")
	count := jsonInt(body, "count", 100)
	interval := jsonFloat(body, "interval", 0.2)

	if !validateTargetRE.MatchString(target) {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid target"})
		return
	}

	timeout := time.Duration(float64(count)*interval+10) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, _ := exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(count),
		"-i", fmt.Sprintf("%.1f", interval), target).CombinedOutput()

	output := string(out)
	result := map[string]interface{}{"output": output}

	if m := regexp.MustCompile(`(\d+)% packet loss`).FindStringSubmatch(output); len(m) > 1 {
		v, _ := strconv.Atoi(m[1])
		result["loss_pct"] = v
	}
	if m := regexp.MustCompile(`([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)`).FindStringSubmatch(output); len(m) > 4 {
		result["rtt_min"], _ = strconv.ParseFloat(m[1], 64)
		result["rtt_avg"], _ = strconv.ParseFloat(m[2], 64)
		result["rtt_max"], _ = strconv.ParseFloat(m[3], 64)
		result["rtt_mdev"], _ = strconv.ParseFloat(m[4], 64)
	}

	writeJSON(w, 200, map[string]interface{}{"ok": true, "result": result})
}

func apiPingStream(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	target := jsonStr(body, "target", "")
	count := jsonInt(body, "count", 0)

	if !validateTargetRE.MatchString(target) {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid target"})
		return
	}

	killStream("ping")
	args := []string{target}
	if count > 0 {
		args = []string{"-c", strconv.Itoa(count), target}
	}

	cmd := exec.Command("ping", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	activeStreamMu.Lock()
	activeStreams["ping"] = cmd
	activeStreamMu.Unlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	cmd.Wait()
	killStream("ping")
}

func apiPingStop(w http.ResponseWriter, r *http.Request) {
	killStream("ping")
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiTracerouteStream(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	target := jsonStr(body, "target", "")

	if !validateTargetRE.MatchString(target) {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid target"})
		return
	}

	killStream("traceroute")

	cmd := exec.Command("bash", "-c",
		`TARGET="$1"
MAC=$(ip neigh show dev bat0 "$TARGET" 2>/dev/null | awk '{print $5}' | head -1)
if [ -n "$MAC" ] && command -v batctl >/dev/null 2>&1; then
  echo "=== Mesh Route (batctl traceroute $MAC) ==="
  batctl traceroute "$MAC" 2>&1 || true
  echo ""
fi
if command -v traceroute >/dev/null 2>&1; then
  echo "=== IP Traceroute ==="
  traceroute -n -w 3 -m 15 "$TARGET" 2>&1 || true
else
  echo "=== IP Route ==="
  ip route get "$TARGET" 2>&1 || true
fi`, "--", target)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	activeStreamMu.Lock()
	activeStreams["traceroute"] = cmd
	activeStreamMu.Unlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	cmd.Wait()
	killStream("traceroute")
}

func apiTracerouteStop(w http.ResponseWriter, r *http.Request) {
	killStream("traceroute")
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// --- Terminal HTTP fallback ---

var blockedCmdRE = regexp.MustCompile(`(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|shutdown|halt|poweroff)\b`)

func apiTerminalExec(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	cmd := strings.TrimSpace(jsonStr(body, "cmd", ""))
	target := jsonStr(body, "target", "")
	user := jsonStr(body, "user", "root")
	password := jsonStr(body, "password", "")

	if cmd == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Empty command"})
		return
	}
	if blockedCmdRE.MatchString(cmd) {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "Command blocked"})
		return
	}

	var shellCmd string
	if target != "" {
		if !validateTargetRE.MatchString(target) {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "Invalid target"})
			return
		}
		sshOpts := "-o StrictHostKeyChecking=no -o ConnectTimeout=5"
		if password != "" {
			shellCmd = fmt.Sprintf("sshpass -p %s ssh %s %s@%s bash -l -c %s",
				shellQuote(password), sshOpts, shellQuote(user), shellQuote(target), shellQuote(cmd))
		} else {
			shellCmd = fmt.Sprintf("ssh %s %s@%s bash -l -c %s",
				sshOpts, shellQuote(user), shellQuote(target), shellQuote(cmd))
		}
	} else {
		shellCmd = fmt.Sprintf("bash -l -c %s", shellQuote(cmd))
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

	proc := exec.Command("bash", "-c", shellCmd)
	proc.Stdout = w
	proc.Stderr = w
	flusher, _ := w.(http.Flusher)
	proc.Run()
	if flusher != nil {
		flusher.Flush()
	}
}

func apiTerminalComplete(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	line := jsonStr(body, "line", "")
	pos := jsonInt(body, "pos", len(line))

	textBefore := line[:pos]
	parts := strings.Fields(textBefore)
	word := ""
	if len(parts) > 0 {
		word = parts[len(parts)-1]
	}
	isFirst := len(parts) <= 1

	compType := "file"
	if isFirst {
		compType = "command"
	}
	compCmd := fmt.Sprintf("compgen -A %s -- %s", compType, shellQuote(word))

	out, _ := runCmdStdout(3*time.Second, "bash", "-l", "-c", compCmd)
	seen := make(map[string]bool)
	var matches []string
	for _, m := range strings.Split(strings.TrimSpace(out), "\n") {
		if m != "" && !seen[m] {
			seen[m] = true
			matches = append(matches, m)
		}
	}

	writeJSON(w, 200, map[string]interface{}{"matches": matches, "word": word})
}

func apiTerminalReboot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "Rebooting..."})
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("systemctl", "reboot").Run()
	}()
}

// --- Auth ---

// isAuthed reports whether the request would pass checkAuth's gate, without
// writing a response on failure. Used by read paths (like the admin status
// endpoint) that need to redact secrets for an unauthenticated caller rather
// than reject the whole request outright.
//
// DEPLOYMENT PREREQUISITE: MANET/provisioning/firstrun.sh.template
// provisions require_auth=y, but nodes provisioned before that change still
// carry require_auth=n. With require_auth=n, this function
// returns true for EVERY caller unconditionally — nothing in this file gets
// redacted and every /api/admin/* + /api/control/* endpoint is wide open to
// anyone who can reach this node's HTTP port. require_auth=y (with a real
// admin_password set) must be part of the cutover to this fleet-crypto
// scheme on every node; it is a fleet configuration precondition, not
// something this code can enforce itself.
func isAuthed(r *http.Request) bool {
	required, authenticated := authState(r)
	return !required || authenticated
}

// authState reports whether this node requires a login and, if so, whether
// the request is authenticated: a live session issued under the current
// password, or an /api/ request relayed by another fleet node's /api/peer
// proxy on behalf of a user logged in there.
func authState(r *http.Request) (required, authenticated bool) {
	required, authenticated = sessionAuthState(r)
	if required && !authenticated && proxiedAPIRequestAuthenticated(r) {
		authenticated = true
	}
	return required, authenticated
}

// proxiedAPIRequestAuthenticated accepts a fleet peer token minted for this
// node under FleetPeerAuthDomainAPI, on /api/ paths only: the domain keeps
// such a token from being replayed against the terminal or logs
// websockets, which verify their own domains. As for those, the Host the
// token is bound to must be one of this node's own addresses.
func proxiedAPIRequestAuthenticated(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	token := r.Header.Get(FleetPeerAuthHeader)
	if token == "" {
		return false
	}
	return hostMatchesLocalAddr(r.Host) && verifyFleetPeerToken(token, r.Host, FleetPeerAuthDomainAPI)
}

// sessionAuthState is authState without the proxied-request path: whether
// the request itself carries a live login session on this node.
func sessionAuthState(r *http.Request) (required, authenticated bool) {
	conf := loadKVFile(MeshConfFile)
	ra := strings.ToLower(conf["require_auth"])
	pw := getProvisionedPassword(conf)
	if pw == "" || (ra != "y" && ra != "yes" && ra != "1") {
		return false, false
	}
	return true, sessions.valid(sessionToken(r), pw)
}

func checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if isAuthed(r) {
		return true
	}
	writeJSON(w, 401, map[string]interface{}{"ok": false, "error": "Authentication required"})
	return false
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		next(w, r)
	}
}

// requireAuthOrPeerToken gates a route on EITHER a valid session cookie
// (same as requireAuth) OR a valid, freshly-minted, target-bound
// X-Manet-Fleet-Peer-Auth header (see verifyFleetPeerToken). It is a small
// factory: each caller passes its own domain string (see
// FleetPeerAuthDomainTerminal / FleetPeerAuthDomainLogs in config.go), so a
// token minted for one route (e.g. /ws/terminal's server-to-server proxy
// hop) cannot be replayed to authenticate a different route (e.g.
// /ws/logs's) even though both derive from the same underlying fleet key.
// It exists solely for server-to-server proxy hops (a node dialing a
// target's own websocket route without a browser session) and must not be
// used to gate any route that isn't itself such a proxy target -- it does
// not set a cookie or otherwise widen session state, it only accepts an
// alternative credential for the one handler it wraps.
//
// r.Host is sender-controlled (it's just the Host header the dialing node
// chose to send) and by itself proves nothing -- so before trusting it as
// the bound target, hostMatchesLocalAddr independently confirms r.Host
// actually names one of THIS node's own addresses via net.InterfaceAddrs().
// Only once that's confirmed does verifyFleetPeerToken's signature check
// against r.Host mean anything.
func requireAuthOrPeerToken(domain string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if token := r.Header.Get(FleetPeerAuthHeader); token != "" {
				if hostMatchesLocalAddr(r.Host) && verifyFleetPeerToken(token, r.Host, domain) {
					next(w, r)
					return
				}
			}
			if !checkAuth(w, r) {
				return
			}
			next(w, r)
		}
	}
}

func apiAuthStatus(w http.ResponseWriter, r *http.Request) {
	required, authenticated := authState(r)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]interface{}{
		"required":      required,
		"authenticated": !required || authenticated,
	})
}

// apiPerfAuth logs in with the admin password and issues a new session
// cookie (see sessions.go), replacing any session the browser already had.
func apiPerfAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "POST required"})
		return
	}
	addr := clientAddr(r)
	if ok, wait := sessions.loginAllowed(addr); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		writeJSON(w, 429, map[string]interface{}{"ok": false, "error": "Too many failed logins, try again shortly"})
		return
	}

	body := readBody(r)
	password := jsonStr(body, "password", "")

	conf := loadKVFile(MeshConfFile)
	expected := getProvisionedPassword(conf)
	if expected == "" || !passwordMatches(password, expected) {
		sessions.recordLoginFailure(addr)
		writeJSON(w, 401, map[string]interface{}{"ok": false, "error": "Invalid password"})
		return
	}

	token, err := sessions.create(expected)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "could not create session"})
		return
	}
	sessions.revoke(sessionToken(r))
	setSessionCookie(w, r, token, int(sessionLifetime.Seconds()))
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// apiLogout ends the session the request presents; other browsers stay
// logged in.
func apiLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "POST required"})
		return
	}
	sessions.revoke(sessionToken(r))
	setSessionCookie(w, r, "", -1)
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     PerfAuthCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

func setHostname(name string) {
	if _, err := runCmd(5*time.Second, "hostnamectl", "set-hostname", name); err == nil {
		return
	}
	os.WriteFile("/etc/hostname", []byte(name+"\n"), 0644)

	if data, err := os.ReadFile("/etc/hosts"); err == nil {
		text := string(data)
		hostRE := regexp.MustCompile(`(?m)^127\.0\.1\.1\s+.*$`)
		if hostRE.MatchString(text) {
			text = hostRE.ReplaceAllLiteralString(text, "127.0.1.1\t"+name)
		} else {
			text += "\n127.0.1.1\t" + name
		}
		os.WriteFile("/etc/hosts", []byte(text), 0644)
	}

	if _, err := runCmd(3*time.Second, "hostname", name); err != nil {
		runCmd(3*time.Second, "hostnamectl", "--transient", "set-hostname", name)
	}
}

var (
	wpaSSIDRE = regexp.MustCompile(`ssid="[^"]*"`)
	// 802.11s mesh mode only supports key_mgmt NONE or SAE — there is no
	// PSK path for a mesh interface, so every wlan*.conf mesh network
	// (2.4GHz, 5GHz, and HaLow's -s1g) uses sae_password, never psk.
	wpaSAERE = regexp.MustCompile(`sae_password="[^"]*"`)
)

// rewriteWPAConf replaces the mesh SSID and (if key is non-empty) SAE
// password in a wpa_supplicant config. The replacements are literal: with
// ReplaceAllString a '$' in the value is expanded as a capture-group
// reference, silently truncating e.g. "abc$def" to "abc". Callers must have
// passed both values through configValueError first — quoting is not
// escaped here because wpa_supplicant's quoted strings have no escapes.
func rewriteWPAConf(text, ssid, key string) string {
	text = wpaSSIDRE.ReplaceAllLiteralString(text, `ssid="`+ssid+`"`)
	if key != "" {
		text = wpaSAERE.ReplaceAllLiteralString(text, `sae_password="`+key+`"`)
	}
	return text
}

func applyWPAConfig(conf map[string]string) {
	ssid := conf["mesh_ssid"]
	key := conf["mesh_key"]
	if ssid == "" {
		return
	}
	// The save/stage/fleet paths validate before persisting, but mesh.conf
	// may still hold a value written before that validation existed or by
	// hand; leave the working supplicant configs alone rather than write a
	// broken one.
	for k, v := range map[string]string{"mesh_ssid": ssid, "mesh_key": key} {
		if err := configValueError(k, v); err != nil {
			log.Printf("wpa config not updated: %v", err)
			return
		}
	}

	wpaDir := "/etc/wpa_supplicant"
	entries, err := os.ReadDir(wpaDir)
	if err != nil {
		return
	}

	restartS1G := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "wpa_supplicant-wlan") || !strings.HasSuffix(name, ".conf") {
			continue
		}

		path := wpaDir + "/" + name
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := rewriteWPAConf(string(data), ssid, key)
		if key != "" && strings.Contains(name, "s1g") {
			restartS1G = true
		}
		if err := writeFileFsync(path, []byte(text)); err != nil {
			log.Printf("wpa config write failed: %s: %v", name, err)
			continue
		}
		log.Printf("wpa config updated: %s", name)
	}

	runCmd(10*time.Second, "bash", "-c", "systemctl restart 'wpa_supplicant@wlan*.service' 2>/dev/null || true")
	if restartS1G {
		runCmd(10*time.Second, "bash", "-c", "systemctl restart 'wpa_supplicant-s1g-wlan*.service' 2>/dev/null || true")
	}
}

func halowBWParams(bw, regDomain string) (opClass, channel, primChwidth, txpowerMBM string) {
	if regDomain == "US" {
		switch bw {
		case "1MHz":
			return "68", "11", "0", "2400"
		case "2MHz":
			return "69", "10", "1", "2400"
		case "4MHz":
			return "70", "24", "1", "2200"
		case "8MHz":
			// op_class 72 / channel 8 is rejected outright by
			// wpa_supplicant_s1g ("error determining S1G operating channel
			// width from operating class") — same bug as radio-setup.sh's
			// table, fixed there but missed here since this is a separate
			// duplicate lookup used when the UI/fleet push updates
			// halow_bw on an already-provisioned node. op_class 71 /
			// channel 12 is the confirmed-working pair for 8MHz.
			return "71", "12", "1", "2000"
		default:
			return "71", "12", "1", "2200"
		}
	}
	if regDomain == "EU" {
		// The compiled wpa_supplicant_s1g op-class table has no
		// 2MHz/4MHz/8MHz entry for the EU band at all — EU is genuinely
		// 1MHz-only on this hardware/firmware (op_class 66, start freq
		// 863000 kHz). apiAdminSave rejects any non-1MHz halow_bw + EU
		// combination at save time, so this branch always returns the
		// single real EU default regardless of what bw was requested.
		return "66", "5", "0", "2400"
	}
	// Non-US/EU domains (JP/KR/SG/AU/NZ/IN/...) are out of scope — no
	// hardware to validate a real per-domain table against, so this
	// generic fallback (unchanged from before this table existed) still
	// applies.
	switch bw {
	case "2MHz":
		return "67", "2", "1", "2400"
	default:
		return "66", "1", "0", "2400"
	}
}

// euHalowCountryCodes mirrors radio-setup.sh's uses_eu_halow_region() list
// (lines 405-416) -- ISO country codes that use the EU HaLow channel plan.
var euHalowCountryCodes = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true,
	"DK": true, "EE": true, "FI": true, "FR": true, "DE": true, "GR": true,
	"HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true,
	"MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true,
	"SI": true, "ES": true, "SE": true, "GB": true, "CH": true, "NO": true,
}

// normalizeRegDomain maps a real ISO country code (e.g. "HR", "NL") to the
// literal "EU" domain used by halowChannelTable/mesh5GHzChannelTable when it
// is in euHalowCountryCodes, leaving any other value (an already-literal
// "US"/"EU", or an out-of-scope code like "JP"/"AU") unchanged. Callers that
// accept a domain from anywhere other than resolveHalowDomain/
// resolveMesh5GHzDomain (e.g. an explicit ?domain= query param) MUST run it
// through this before using it for a channel-table lookup -- otherwise a
// real country code silently falls through to the permissive out-of-scope
// fallback instead of the domain's real restricted list.
func normalizeRegDomain(domain string) string {
	if euHalowCountryCodes[domain] {
		return "EU"
	}
	return domain
}

// resolveHalowDomain is the HaLow plan for the node's country: "EU" for an
// EU-plan country, otherwise the country itself. HaLow always follows
// regulatory_domain, as upstream does: the Morse driver adopts the kernel's
// Wi-Fi country whenever it has its own table for it, so a separate HaLow
// region cannot be relied on. Any halow_regulatory_domain left in an old
// mesh.conf is ignored. radio-setup.sh's resolve_halow_domain applies the
// same rule; keep them in step.
func resolveHalowDomain(conf map[string]string) string {
	return normalizeRegDomain(confGet(conf, "regulatory_domain", "US"))
}

// halowChannelTable is the ground-truth legal HaLow channel list per
// regulatory domain + bandwidth, reverse-engineered from the compiled
// wpa_supplicant_s1g binary's s1g_op_classes table. Domains not present here
// (anything other than "US"/"EU") are out of scope: no hardware to validate
// against, so they keep the existing generic single-default behavior.
var halowChannelTable = map[string]map[string][]int{
	"US": {
		"1MHz": {1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 23, 25, 27, 29, 31, 33, 35, 37, 39, 41, 43, 45, 47, 49, 51},
		"2MHz": {2, 6, 10, 14, 18, 22, 26, 30, 34, 38, 42, 46, 50},
		"4MHz": {8, 16, 24, 32, 40, 48},
		"8MHz": {12, 28, 44},
	},
	"EU": {
		"1MHz": {1, 3, 5, 7, 9},
	},
}

// halowStartFreqKHz is the S1G band start frequency per domain, used with
// the frequency formula: center_freq_MHz = (start_freq_khz + channel*500) / 1000.
var halowStartFreqKHz = map[string]int{"US": 902000, "EU": 863000}

// halowDefaultBW must match radio-setup.sh's halow_bw="${halow_bw:-4MHz}"
// (the HALOW CONFIGURATION section) -- halow_bw is never written to
// /etc/mesh.conf by any provisioning path, so every default-provisioned node
// has it unset and relies on that shell fallback. Without the same fallback
// here, validateHalowChannel/apiHalowChannels would see bw=="" and treat it
// as "this domain has zero legal channels", rejecting unrelated config saves
// and silently no-opping fleet-pushed halow_channel updates on every stock
// node.
const halowDefaultBW = "4MHz"

// effectiveHalowBW resolves the bandwidth actually in effect, applying the
// same default radio-setup.sh applies when halow_bw is unset.
func effectiveHalowBW(conf map[string]string) string {
	return confGet(conf, "halow_bw", halowDefaultBW)
}

// halowDefaultChannel documents the "Auto" default channel per domain/bw —
// must stay in sync with halowBWParams's return values.
var halowDefaultChannel = map[string]map[string]int{
	"US": {"1MHz": 11, "2MHz": 10, "4MHz": 24, "8MHz": 12},
	"EU": {"1MHz": 5},
}

// halowChannelCandidates returns the legal explicit channel list for a
// domain/bw combination, or nil if that combination has no candidate list
// (either the domain is out of scope, or the domain is in scope but this
// particular bandwidth has no legal channel at all, e.g. EU + 2MHz).
func halowChannelCandidates(domain, bw string) []int {
	bwTable, ok := halowChannelTable[domain]
	if !ok {
		return nil
	}
	return bwTable[bw]
}

// resolveMesh5GHzDomain resolves the effective regulatory domain for the
// 5GHz mesh (WiFi) radio from regulatory_domain, defaulting to "US" if unset.
//
// regulatory_domain holds a real ISO country code in provisioned config
// (e.g. "HR", "NL"), not just the literal "EU" the web UI's select emits --
// normalize via euHalowCountryCodes the same way resolveHalowDomain does, or
// every ETSI-country node falls through to the permissive fallback and
// accepts UNII-3 channels unchecked, defeating this validator entirely.
func resolveMesh5GHzDomain(conf map[string]string) string {
	return normalizeRegDomain(confGet(conf, "regulatory_domain", "US"))
}

// mesh5GHzChannelTable is the per-domain guardrail for mesh_5ghz_channel,
// mirroring halowChannelTable's shape. The full channel list (36, 40, 44,
// 48, 149, 153, 157, 161, 165) matches node-manager's ensureStaticChannels/
// scan.go band5Channels -- kept in sync with that list by convention, not by
// import (separate Go modules/binaries).
//
// EU is limited to the non-DFS UNII-1 channels (36/40/44/48); UNII-2/
// UNII-2e (52-144, the DFS-requiring range) is deliberately absent from
// every domain's list here, not just EU's -- this stack has no DFS
// radar-detection handling implemented anywhere in node-manager, so pinning
// a channel in that range would be a live RF violation on any domain, not
// just a config-validation gap.
//
// Domains not explicitly modeled here (JP, AU, ...) keep the full
// permissive list rather than being rejected outright: there is no
// hardware-verified per-domain table for them, and rejecting by default
// would break already-persisted values (e.g. channel 149) on nodes
// provisioned under those domains before this validator existed.
var mesh5GHzChannelTable = map[string][]string{
	"US": {"36", "40", "44", "48", "149", "153", "157", "161", "165"},
	"EU": {"36", "40", "44", "48"},
}

// mesh5GHzChannelCandidatesForDomain returns the legal mesh_5ghz_channel
// values for a resolved regulatory domain, falling back to the full
// permissive list (same as "US") for domains with no explicit table entry.
func mesh5GHzChannelCandidatesForDomain(domain string) []string {
	if list, ok := mesh5GHzChannelTable[domain]; ok {
		return list
	}
	return mesh5GHzChannelTable["US"]
}

// validateMesh5GHzChannel rejects a mesh_5ghz_channel save outright rather
// than silently persisting a value node-manager's desiredMesh5GHzChannel
// would just fall back to the lobby channel on anyway -- matching
// validateHalowChannel's own stated principle just below: an invalid value
// must reject the whole save, not be silently coerced or ignored. Domain is
// resolved by the caller via resolveMesh5GHzDomain (or, on the fleet-apply
// side, per-node -- see applyFleetMesh5GHzChannel in fleet.go).
func validateMesh5GHzChannel(domain, channel string) error {
	for _, c := range mesh5GHzChannelCandidatesForDomain(domain) {
		if c == channel {
			return nil
		}
	}
	return fmt.Errorf("mesh_5ghz_channel %q is not valid for regulatory domain %q (valid: %s)",
		channel, domain, strings.Join(mesh5GHzChannelCandidatesForDomain(domain), ", "))
}

// apiMesh5GHzChannels reports the legal 5GHz mesh channel list for a
// regulatory domain, so the web UI can build a channel picker without
// duplicating mesh5GHzChannelTable in JS. Response shape matches
// apiHalowChannels. Missing domain query param falls back to this node's
// current config via resolveMesh5GHzDomain -- callers editing a remote/fleet
// node should always pass domain= explicitly rather than relying on that
// fallback, since it reflects the serving node's own domain, not
// necessarily the node being edited.
//
// Accepts a bw= query param for URL-shape parity with /api/halow/channels,
// but does not currently filter on it -- unlike HaLow, mesh5GHzChannelTable's
// candidate list doesn't vary by channel width (20/40/80 MHz), only by
// regulatory domain.
func apiMesh5GHzChannels(w http.ResponseWriter, r *http.Request) {
	conf := loadKVFile(MeshConfFile)
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		domain = resolveMesh5GHzDomain(conf)
	} else {
		// resolveMesh5GHzDomain already normalizes for the empty-param
		// fallback above; an explicit ?domain= (e.g. the fleet UI passing a
		// real ISO country code like "HR" straight through) needs the same
		// normalization here, or it falls through to the permissive
		// out-of-scope default instead of the domain's real restricted list
		// -- exactly the gap that let a EU country-code node's picker show
		// unfiltered UNII-3 channels despite the save path already rejecting
		// them correctly.
		domain = normalizeRegDomain(domain)
	}
	candidates := mesh5GHzChannelCandidatesForDomain(domain)
	channels := make([]map[string]interface{}, 0, len(candidates))
	for _, chStr := range candidates {
		ch, err := strconv.Atoi(chStr)
		if err != nil {
			continue
		}
		// "channel" is an int here, matching apiHalowChannels's response
		// shape exactly (that endpoint's candidates are already []int).
		channels = append(channels, map[string]interface{}{"channel": ch, "freq_mhz": 5000 + ch*5})
	}

	defaultChannel := 36
	resp := map[string]interface{}{
		"channels":         channels,
		"default_channel":  defaultChannel,
		"default_freq_mhz": 5000 + defaultChannel*5,
	}
	writeJSON(w, 200, resp)
}

// validateHalowChannel checks whether an explicit (or "Auto"/empty)
// halow_channel is legal for the given domain/bandwidth. An empty channel
// means "Auto" and is only rejected when the domain has an explicit channel
// table (US/EU) but this particular bw has zero legal channels in it (e.g.
// EU + any bw other than 1MHz — a genuine hardware constraint). Domains
// outside the table keep today's existing single-default behavior,
// unchanged.
func validateHalowChannel(domain, bw, channel string) error {
	candidates := halowChannelCandidates(domain, bw)
	if channel == "" {
		if _, tableKnown := halowChannelTable[domain]; tableKnown && len(candidates) == 0 {
			return fmt.Errorf("halow_bw %q has no valid HaLow channel in regulatory domain %q", bw, domain)
		}
		return nil
	}
	chNum, err := strconv.Atoi(channel)
	if err != nil {
		return fmt.Errorf("invalid halow_channel %q: not a number", channel)
	}
	for _, c := range candidates {
		if c == chNum {
			return nil
		}
	}
	return fmt.Errorf("channel %d is not valid for %s/%s", chNum, domain, bw)
}

// apiHalowChannels reports the legal HaLow channel list (and the "Auto"
// default) for a domain/bandwidth pair, so the web UI can build a channel
// picker without duplicating the table in JS. Missing domain/bw query
// params fall back to this node's current config.
func apiHalowChannels(w http.ResponseWriter, r *http.Request) {
	conf := loadKVFile(MeshConfFile)
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		domain = resolveHalowDomain(conf)
	} else {
		// See the matching comment in apiMesh5GHzChannels: an explicit
		// ?domain= needs the same country-code normalization
		// resolveHalowDomain already applies in the empty-param fallback
		// case above, or a real ISO country code silently gets the
		// permissive out-of-scope channel list instead of its real one.
		domain = normalizeRegDomain(domain)
	}
	bw := r.URL.Query().Get("bw")
	if bw == "" {
		bw = effectiveHalowBW(conf)
	}

	candidates := halowChannelCandidates(domain, bw)
	startFreq, hasFreq := halowStartFreqKHz[domain]

	channels := make([]map[string]interface{}, 0, len(candidates))
	for _, ch := range candidates {
		entry := map[string]interface{}{"channel": ch}
		if hasFreq {
			entry["freq_mhz"] = float64(startFreq+ch*500) / 1000.0
		}
		channels = append(channels, entry)
	}

	// halowDefaultChannel is the single source of truth for the "Auto"
	// default within the US/EU table; out-of-scope domains have no table
	// entry, so fall back to whatever halowBWParams already computes for
	// them (today's existing single-default behavior, unchanged).
	defaultChannel, ok := halowDefaultChannel[domain][bw]
	if !ok {
		_, defCh, _, _ := halowBWParams(bw, domain)
		if n, err := strconv.Atoi(defCh); err == nil {
			defaultChannel = n
		}
	}

	resp := map[string]interface{}{
		"channels":        channels,
		"default_channel": defaultChannel,
	}
	if hasFreq {
		resp["default_freq_mhz"] = float64(startFreq+defaultChannel*500) / 1000.0
	}
	writeJSON(w, 200, resp)
}

// applyHalowBW writes the resolved op_class/channel/chwidth/txpower into the
// node's HaLow wpa_supplicant conf(s) and restarts the service if anything
// changed. Returns whether it actually applied a change -- callers must not
// report success unconditionally, since a channel-only update against a
// domain/bw that's already correctly applied is a legitimate no-op.
func applyHalowBW(conf map[string]string) bool {
	bw := effectiveHalowBW(conf)
	regDomain := resolveHalowDomain(conf)
	opClass, ch, chwidth, _ := halowBWParams(bw, regDomain)
	txMBM, _ := halowTxpowerMBM(conf)
	if explicit := conf["halow_channel"]; explicit != "" {
		ch = explicit
	}

	opClassRE := regexp.MustCompile(`op_class=(\d+)`)
	channelRE := regexp.MustCompile(`(^|\s)channel=\d+`)
	channelValRE := regexp.MustCompile(`channel=(\d+)`)
	chwidthRE := regexp.MustCompile(`s1g_prim_chwidth=\d+`)
	countryRE := regexp.MustCompile(`country="([^"]*)"`)
	txpowerRE := regexp.MustCompile(`txpower fixed \d+`)

	wpaDir := "/etc/wpa_supplicant"
	entries, _ := os.ReadDir(wpaDir)
	changed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.Contains(name, "s1g") {
			continue
		}
		path := wpaDir + "/" + name
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)

		// Skip only if op_class, channel, AND country already match the
		// resolved target -- checking op_class alone (as this used to)
		// silently no-ops a pure channel change at the same bandwidth: the
		// op_class match short-circuits before channel/country ever get
		// compared, so the file (and the live radio) never updates even
		// though the API reports success. country is load-bearing too:
		// wpa_supplicant_s1g flat-out rejects an op_class/channel pair that
		// doesn't match its own country field ("Invalid S1G configuration
		// of operating class, country code and channel"), confirmed on
		// live hardware -- a domain switch that only rewrote op_class
		// would take the HaLow interface down.
		opOK := false
		if m := opClassRE.FindStringSubmatch(text); len(m) > 1 && m[1] == opClass {
			opOK = true
		}
		chOK := false
		if m := channelValRE.FindStringSubmatch(text); len(m) > 1 && m[1] == ch {
			chOK = true
		}
		countryOK := false
		if m := countryRE.FindStringSubmatch(text); len(m) > 1 && m[1] == regDomain {
			countryOK = true
		}
		if opOK && chOK && countryOK {
			continue
		}

		text = opClassRE.ReplaceAllString(text, "op_class="+opClass)
		text = channelRE.ReplaceAllStringFunc(text, func(m string) string {
			prefix := ""
			if len(m) > 0 && m[0] != 'c' {
				prefix = string(m[0])
			}
			return prefix + "channel=" + ch
		})
		text = chwidthRE.ReplaceAllString(text, "s1g_prim_chwidth="+chwidth)
		text = countryRE.ReplaceAllString(text, `country="`+regDomain+`"`)
		os.WriteFile(path, []byte(text), 0644)
		log.Printf("halow bw updated: %s -> %s (op_class=%s ch=%s country=%s)", name, bw, opClass, ch, regDomain)
		changed = true
	}

	if !changed {
		return false
	}

	svcDir := "/etc/systemd/system"
	svcEntries, _ := os.ReadDir(svcDir)
	for _, entry := range svcEntries {
		name := entry.Name()
		if !strings.HasPrefix(name, "halow-txpower-") {
			continue
		}
		path := svcDir + "/" + name
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := txpowerRE.ReplaceAllString(string(data), "txpower fixed "+txMBM)
		os.WriteFile(path, []byte(text), 0644)
	}
	runCmd(5*time.Second, "systemctl", "daemon-reload")
	runCmd(10*time.Second, "bash", "-c", "systemctl restart 'wpa_supplicant-s1g-wlan*.service' 2>/dev/null || true")
	return true
}

// eudWantsAP reports whether the given eud= mode requires an AP interface
// (hostapd running), as opposed to wired/none which only use the mesh
// radios and must keep hostapd stopped.
func eudWantsAP(eud string) bool {
	return eud == "wireless" || eud == "both" || eud == "auto"
}

func applyHostapdConfig(conf map[string]string) {
	apf := "/etc/hostapd/hostapd.conf"
	data, err := os.ReadFile(apf)
	if err != nil {
		return
	}
	text := string(data)

	if apSSID := conf["lan_ap_ssid"]; apSSID != "" {
		macSuffix := getMACsuffix()
		fullSSID := apSSID
		if macSuffix != "" {
			fullSSID += "-" + macSuffix
		}
		text = regexp.MustCompile(`(?m)^ssid=.*`).ReplaceAllLiteralString(text, "ssid="+fullSSID)
	}
	if apKey := conf["lan_ap_key"]; apKey != "" {
		text = regexp.MustCompile(`(?m)^wpa_passphrase=.*`).ReplaceAllLiteralString(text, "wpa_passphrase="+apKey)
	}
	if apCh := conf["lan_ap_channel"]; apCh != "" {
		text = regexp.MustCompile(`(?m)^channel=.*`).ReplaceAllLiteralString(text, "channel="+apCh)
	}
	if apBw := conf["lan_ap_bw"]; apBw != "" {
		bwInt, _ := strconv.Atoi(apBw)
		if bwInt >= 40 {
			text = regexp.MustCompile(`(?m)^#?ht_capab=.*`).ReplaceAllString(text, "ht_capab=[HT40+][SHORT-GI-40]")
		} else {
			text = regexp.MustCompile(`(?m)^ht_capab=.*`).ReplaceAllString(text, "#ht_capab=")
		}
		if bwInt >= 80 {
			text = regexp.MustCompile(`(?m)^#?vht_oper_chwidth=.*`).ReplaceAllString(text, "vht_oper_chwidth=1")
		} else if regexp.MustCompile(`(?m)^vht_oper_chwidth=`).MatchString(text) {
			text = regexp.MustCompile(`(?m)^vht_oper_chwidth=.*`).ReplaceAllString(text, "vht_oper_chwidth=0")
		}
	}

	os.WriteFile(apf, []byte(text), 0644)
	log.Printf("hostapd config updated")
}

func applyMulticastMode(mode string) {
	if mode == "optimized" {
		runCmd(3*time.Second, "batctl", "bat0", "multicast_forceflood", "0")
		os.WriteFile("/sys/devices/virtual/net/br0/bridge/multicast_snooping", []byte("1"), 0644)
		os.WriteFile("/sys/devices/virtual/net/br0/bridge/multicast_querier", []byte("1"), 0644)
	} else {
		runCmd(3*time.Second, "batctl", "bat0", "multicast_forceflood", "1")
		os.WriteFile("/sys/devices/virtual/net/br0/bridge/multicast_snooping", []byte("0"), 0644)
		os.WriteFile("/sys/devices/virtual/net/br0/bridge/multicast_querier", []byte("0"), 0644)
	}
}

func applyDNSServers(csv string) {
	servers := strings.Split(csv, ",")
	var lines []string
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s != "" && net.ParseIP(s) != nil {
			lines = append(lines, "nameserver "+s)
		}
	}
	if len(lines) == 0 {
		lines = []string{"nameserver 8.8.8.8", "nameserver 8.8.4.4"}
	}
	os.WriteFile("/etc/resolv.conf", []byte(strings.Join(lines, "\n")+"\n"), 0644)
	runCmd(5*time.Second, "systemctl", "restart", "dnsmasq")
}

func findOpenVLMCard() string {
	matches, _ := filepath.Glob("/proc/asound/card*/usbid")
	target := fmt.Sprintf("%04x:%04x", 0x0D8C, 0x0012)
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(strings.ToLower(string(data))) != target {
			continue
		}
		cardDir := filepath.Base(filepath.Dir(path))
		return strings.TrimPrefix(cardDir, "card")
	}
	return ""
}

func applyVoiceVolume(conf map[string]string) {
	card := findOpenVLMCard()
	if card == "" {
		return
	}
	mic := confGet(conf, "voice_mic_volume", "")
	spk := confGet(conf, "voice_speaker_volume", "")
	if mic != "" {
		v, err := strconv.Atoi(mic)
		if err == nil && v >= 0 && v <= 100 {
			pct := fmt.Sprintf("%d%%", v)
			runCmd(3*time.Second, "amixer", "-c", card, "set", "Mic", pct)
		}
	}
	if spk != "" {
		v, err := strconv.Atoi(spk)
		if err == nil && v >= 0 && v <= 100 {
			pct := fmt.Sprintf("%d%%", v)
			runCmd(3*time.Second, "amixer", "-c", card, "set", "PCM", pct)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func apiATAKPackage(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "MANET"
	}

	uid := "manet-mesh-" + hostname

	pref := `<?xml version='1.0' standalone='yes'?>
<preferences>
    <preference version="1" name="cot_streams">
        <entry key="count" class="class java.lang.Integer">1</entry>
        <entry key="description0" class="class java.lang.String">MANET Mesh</entry>
        <entry key="enabled0" class="class java.lang.Boolean">true</entry>
        <entry key="connectString0" class="class java.lang.String">239.2.3.1:6969:udp</entry>
    </preference>
</preferences>`

	manifest := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MissionPackageManifest version="2">
    <Configuration>
        <Parameter name="uid" value="%s"/>
        <Parameter name="name" value="MANET Mesh CoT"/>
    </Configuration>
    <Contents>
        <Content ignore="false" zipEntry="config/manet-mesh.pref">
            <Parameter name="name" value="MANET Mesh Network Input"/>
        </Content>
    </Contents>
</MissionPackageManifest>`, uid)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	mf, _ := zw.Create("MANIFEST/manifest.xml")
	mf.Write([]byte(manifest))

	pf, _ := zw.Create("config/manet-mesh.pref")
	pf.Write([]byte(pref))

	zw.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="MANET-Mesh-CoT.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Write(buf.Bytes())
}

func applyEUDBandwidth(capMbit string) {
	iface := "br0"
	ifbDev := "ifb0"

	runCmd(3*time.Second, "tc", "qdisc", "del", "dev", iface, "root")
	runCmd(3*time.Second, "tc", "qdisc", "del", "dev", iface, "ingress")
	runCmd(3*time.Second, "tc", "qdisc", "del", "dev", ifbDev, "root")

	if capMbit == "" || capMbit == "0" {
		runCmd(3*time.Second, "ip", "link", "set", ifbDev, "down")
		return
	}

	rate := capMbit + "mbit"
	euds := getEUDs()
	if len(euds) == 0 {
		return
	}

	// Download shaping (br0 egress)
	runCmd(3*time.Second, "tc", "qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "99")
	runCmd(3*time.Second, "tc", "class", "add", "dev", iface, "parent", "1:", "classid", "1:99", "htb", "rate", "1000mbit")

	for i, eud := range euds {
		classID := fmt.Sprintf("1:%d", 10+i)
		handleID := fmt.Sprintf("%d:", 10+i)
		runCmd(3*time.Second, "tc", "class", "add", "dev", iface, "parent", "1:", "classid", classID, "htb", "rate", rate, "ceil", rate)
		runCmd(3*time.Second, "tc", "qdisc", "add", "dev", iface, "parent", classID, "handle", handleID, "sfq", "perturb", "10")
		runCmd(3*time.Second, "tc", "filter", "add", "dev", iface, "parent", "1:", "protocol", "ip", "prio", "1", "u32", "match", "ip", "dst", eud.IP+"/32", "flowid", classID)
	}

	// Upload shaping (br0 ingress via IFB redirect)
	runCmd(3*time.Second, "modprobe", "ifb")
	runCmd(3*time.Second, "ip", "link", "add", ifbDev, "type", "ifb")
	runCmd(3*time.Second, "ip", "link", "set", ifbDev, "up")
	runCmd(3*time.Second, "tc", "qdisc", "add", "dev", iface, "handle", "ffff:", "ingress")
	runCmd(3*time.Second, "tc", "filter", "add", "dev", iface, "parent", "ffff:", "protocol", "ip", "u32",
		"match", "u32", "0", "0", "action", "mirred", "egress", "redirect", "dev", ifbDev)

	runCmd(3*time.Second, "tc", "qdisc", "add", "dev", ifbDev, "root", "handle", "1:", "htb", "default", "99")
	runCmd(3*time.Second, "tc", "class", "add", "dev", ifbDev, "parent", "1:", "classid", "1:99", "htb", "rate", "1000mbit")

	for i, eud := range euds {
		classID := fmt.Sprintf("1:%d", 10+i)
		handleID := fmt.Sprintf("%d:", 10+i)
		runCmd(3*time.Second, "tc", "class", "add", "dev", ifbDev, "parent", "1:", "classid", classID, "htb", "rate", rate, "ceil", rate)
		runCmd(3*time.Second, "tc", "qdisc", "add", "dev", ifbDev, "parent", classID, "handle", handleID, "sfq", "perturb", "10")
		runCmd(3*time.Second, "tc", "filter", "add", "dev", ifbDev, "parent", "1:", "protocol", "ip", "prio", "1", "u32", "match", "ip", "src", eud.IP+"/32", "flowid", classID)
	}
}

func apiDownloadAPK(w http.ResponseWriter, r *http.Request) {
	apkPath := "/usr/local/share/manet/mesh-ctrl.apk"
	info, err := os.Stat(apkPath)
	if err != nil {
		writeJSON(w, 404, map[string]interface{}{"error": "APK not available on this node"})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="mesh-ctrl.apk"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeFile(w, r, apkPath)
}
