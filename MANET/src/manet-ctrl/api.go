package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request) map[string]interface{} {
	m := make(map[string]interface{})
	body, err := io.ReadAll(r.Body)
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

func peerProxyRequest(w http.ResponseWriter, r *http.Request, peerIP, path string) {
	targetURL := fmt.Sprintf("https://%s%s", peerIP, path)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: peerTLSConfig,
		},
	}

	proxyReq, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		writeJSON(w, 502, map[string]interface{}{"ok": false, "error": "proxy request failed"})
		return
	}
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	proxyReq.ContentLength = r.ContentLength

	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, 502, map[string]interface{}{"ok": false, "error": "peer unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func apiVoice(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		apiVoiceConfig(w, r)
		return
	}
	writeJSON(w, 200, getVoiceStatus())
}

func apiVoiceConfig(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	action := jsonStr(body, "action", "")

	if action == "start" || action == "stop" || action == "restart" {
		ok, errMsg := serviceAction("mesh-voice", action)
		writeJSON(w, 200, map[string]interface{}{"ok": ok, "error": errMsg})
		return
	}

	if action != "configure" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "unknown action"})
		return
	}

	ptt := jsonStr(body, "ptt_mode", "always")
	iface := jsonStr(body, "interface", "br0")
	addr := jsonStr(body, "mcast_addr", "239.69.0.1")
	port := jsonStr(body, "port", "4370")

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
	exec.Command("systemctl", "restart", "mesh-voice").Run()

	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiAdminStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, assembleAdminStatus())
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

func apiMesh(w http.ResponseWriter, r *http.Request) {
	_, origMap := runBatctlOriginators()
	neighbors := runBatctlNeighbors()
	gateways := runBatctlGateways()
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

	var origList []map[string]interface{}
	for mac, o := range origMap {
		entry := enrichMAC(mac)
		entry["tq"] = o.TQ
		entry["nexthop"] = o.Nexthop
		entry["iface"] = o.Iface
		if nhInfo, ok := macInfo[o.Nexthop]; ok {
			entry["nexthop_hostname"] = nhInfo["hostname"]
		}
		origList = append(origList, entry)
	}

	var neighList []map[string]interface{}
	for _, n := range neighbors {
		entry := enrichMAC(n.MAC)
		entry["tq"] = n.TQ
		entry["iface"] = n.Iface
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
	now := time.Now().Unix()
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
		"hostname":          myHostname,
		"mesh_ssid":         conf["mesh_ssid"],
		"network":           confGet(conf, "ipv4_network", "10.30.2.0/24"),
		"originators":       origList,
		"neighbors":         neighList,
		"gateways":          gwList,
		"originator_count":  len(origMap),
		"neighbor_count":    len(neighbors),
		"gateway_count":     len(gateways),
		"dns_records":       dnsRecords,
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
	"ipv4_network": true, "regulatory_domain": true, "halow_bw": true,
	"acs": true, "mtx": true,
	"mumble": true, "battery_monitor": true, "auto_update": true, "admin_password": true,
	"gateway": true, "gateway_nat": true, "gateway_mss_clamp": true, "gateway_bandwidth": true,
	"multicast_mode": true,
}

func apiAdminSave(w http.ResponseWriter, r *http.Request) {
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
	var saved []string
	for k, v := range configMap {
		if saveableKeys[k] {
			updates[k] = fmt.Sprintf("%v", v)
			saved = append(saved, k)
		}
	}

	if len(updates) == 0 {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "No valid keys"})
		return
	}

	if err := saveKVFile(MeshConfFile, updates); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	applied := make(map[string]interface{})
	conf := loadKVFile(MeshConfFile)

	// Apply hostname
	if updates["node_hostname"] != "" || updates["mesh_ssid"] != "" {
		prefix := confGet(conf, "node_hostname", "node")
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

	// Apply AP settings
	if updates["lan_ap_ssid"] != "" || updates["lan_ap_key"] != "" {
		runCmd(10*time.Second, "systemctl", "restart", "hostapd")
		applied["ap_restarted"] = true
	}

	// Apply mesh key/SSID changes to wpa_supplicant configs
	if updates["mesh_ssid"] != "" || updates["mesh_key"] != "" {
		applyWPAConfig(conf)
		applied["mesh_updated"] = true
	}

	// Apply multicast mode
	if updates["multicast_mode"] != "" {
		applyMulticastMode(updates["multicast_mode"])
		applied["multicast_applied"] = true
	}

	writeJSON(w, 200, map[string]interface{}{"ok": true, "saved": saved, "applied": applied})
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

	currentConf := loadKVFile(MeshConfFile)
	strConf := make(map[string]string)
	for k, v := range configMap {
		strConf[k] = fmt.Sprintf("%v", v)
	}

	version := makeConfigVersion(strConf)

	dangerous := strConf["mesh_ssid"] != currentConf["mesh_ssid"] ||
		strConf["mesh_key"] != currentConf["mesh_key"] ||
		strConf["ipv4_network"] != confGet(currentConf, "ipv4_network", "10.30.2.0/24")

	pkg := map[string]interface{}{
		"version":   version,
		"config":    configMap,
		"staged_by": getMyHostname(),
		"staged_at": time.Now().Unix(),
	}

	savePendingConfig(pkg)
	os.WriteFile(AckVersionFile, []byte(version), 0644)
	broadcastConfigPackage(pkg)

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

	if !force {
		version := jsonStr(pkg, "version", "")
		registry := parseRegistry()
		var notAcked []string
		for _, rn := range registry {
			if rn["CONFIG_ACK_VERSION"] != version {
				name := rn["HOSTNAME"]
				if name == "" {
					name = rn["IPV4_ADDRESS"]
				}
				notAcked = append(notAcked, name)
			}
		}
		if len(notAcked) > 0 {
			writeJSON(w, 400, map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("%d nodes have not ACKed: %s", len(notAcked), strings.Join(notAcked, ", ")),
			})
			return
		}
	}

	activateAt := time.Now().Add(60 * time.Second).Unix()
	pkg["activate_at"] = activateAt
	savePendingConfig(pkg)
	broadcastConfigPackage(pkg)

	writeJSON(w, 200, map[string]interface{}{"ok": true, "activate_at": activateAt})
}

func apiAdminCancel(w http.ResponseWriter, r *http.Request) {
	clearPendingConfig()
	os.Remove(AckVersionFile)
	writeJSON(w, 200, map[string]interface{}{"ok": true})
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
	activeStreams   = make(map[string]*exec.Cmd)
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

func apiPerfAuth(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	password := jsonStr(body, "password", "")

	conf := loadKVFile(MeshConfFile)
	expected := getProvisionedPassword(conf)
	if expected == "" || password != expected {
		writeJSON(w, 401, map[string]interface{}{"ok": false, "error": "Invalid password"})
		return
	}

	token := getPerfAuthToken()
	http.SetCookie(w, &http.Cookie{
		Name:     PerfAuthCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   PerfAuthMaxAge,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, 200, map[string]interface{}{"ok": true})
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
			text = hostRE.ReplaceAllString(text, "127.0.1.1\t"+name)
		} else {
			text += "\n127.0.1.1\t" + name
		}
		os.WriteFile("/etc/hosts", []byte(text), 0644)
	}

	if _, err := runCmd(3*time.Second, "hostname", name); err != nil {
		runCmd(3*time.Second, "hostnamectl", "--transient", "set-hostname", name)
	}
}

func applyWPAConfig(conf map[string]string) {
	ssid := conf["mesh_ssid"]
	key := conf["mesh_key"]
	if ssid == "" {
		return
	}

	wpaDir := "/etc/wpa_supplicant"
	entries, err := os.ReadDir(wpaDir)
	if err != nil {
		return
	}

	ssidRE := regexp.MustCompile(`ssid="[^"]*"`)
	pskRE := regexp.MustCompile(`psk="[^"]*"`)

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "wpa_supplicant-wlan") || !strings.HasSuffix(name, ".conf") {
			continue
		}
		if strings.Contains(name, "s1g") {
			continue
		}

		path := wpaDir + "/" + name
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		text = ssidRE.ReplaceAllString(text, fmt.Sprintf(`ssid="%s"`, ssid))
		if key != "" {
			text = pskRE.ReplaceAllString(text, fmt.Sprintf(`psk="%s"`, key))
		}
		os.WriteFile(path, []byte(text), 0644)
	}

	// Restart mesh wpa_supplicant instances
	runCmd(10*time.Second, "bash", "-c", "systemctl restart 'wpa_supplicant@wlan*.service' 2>/dev/null || true")
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
