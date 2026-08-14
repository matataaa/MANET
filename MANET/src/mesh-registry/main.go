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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	alfredBin    = "/usr/sbin/alfred"
	batctlBin    = "/usr/sbin/batctl"
	alfredType   = "68"
	registryFile = "/var/run/mesh_node_registry"
	stateFile    = "/var/lib/manet/state.json"
	nodesFile    = "/var/lib/manet/known_nodes.json"
	confFile     = "/etc/mesh.conf"
	appletsDir   = "/usr/local/share/manet/applets"
	interval     = 15 * time.Second
)

type NodeInfo struct {
	Hostname     string `json:"hostname"`
	MAC          string `json:"mac"`
	MACAddresses string `json:"mac_addresses"`
	IPv4         string `json:"ipv4"`
	IPv4Chunk    string `json:"ipv4_chunk"`
	Uptime       string `json:"uptime_seconds"`
	Battery      string `json:"battery_percentage"`
	CPULoad      string `json:"cpu_load"`
	IsGateway    string `json:"is_gateway"`
	GatewayIface string `json:"gateway_iface"`
	IsNTP        string `json:"is_ntp"`
	GPSLat       string `json:"gps_lat"`
	GPSLon       string `json:"gps_lon"`
	GPSAlt       string `json:"gps_alt"`
	Ch2G         string `json:"ch_2g"`
	Ch5G         string `json:"ch_5g"`
	IsLimp       string `json:"is_limp"`
	Timestamp    string `json:"timestamp"`
	Applets      string `json:"applets,omitempty"`
	HalowTxMCS   string `json:"halow_tx_mcs,omitempty"`
	HalowRxMCS   string `json:"halow_rx_mcs,omitempty"`
	HalowMCSPeer string `json:"halow_mcs_peer,omitempty"`
	Wifi24TxMCS  string `json:"wifi_24_tx_mcs,omitempty"`
	Wifi24RxMCS  string `json:"wifi_24_rx_mcs,omitempty"`
	Wifi5TxMCS   string `json:"wifi_5_tx_mcs,omitempty"`
	Wifi5RxMCS   string `json:"wifi_5_rx_mcs,omitempty"`
	TQAverage    string `json:"tq_average,omitempty"`
	Neighbors    string `json:"neighbors,omitempty"`
	ConfigAck    string `json:"config_ack,omitempty"`
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.Println("mesh-registry starting")

	loadKnownNodes()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	tick := time.NewTicker(interval)
	defer tick.Stop()

	run()
	for {
		select {
		case <-tick.C:
			run()
		case <-sig:
			log.Println("shutting down")
			return
		}
	}
}

func run() {
	info := collectLocal()
	publish(info)
	peers := readPeers()
	writeRegistry(info, peers)
}

func collectLocal() NodeInfo {
	hostname, _ := os.Hostname()
	mac := getMyMAC()
	allMACs := getAllMACs()
	ip := getIPv4()
	uptime := getUptimeSeconds()
	battery := getBatteryPct()
	cpu := getCPULoad()
	isGW, gwIface := getGatewayInfo()
	gpsLat, gpsLon, gpsAlt := getGPS()

	mcs := collectMCS()

	return NodeInfo{
		Hostname:     hostname,
		MAC:          mac,
		MACAddresses: allMACs,
		IPv4:         ip,
		Uptime:       uptime,
		Battery:      battery,
		CPULoad:      cpu,
		IsGateway:    isGW,
		GatewayIface: gwIface,
		IsNTP:        boolStr(serviceActive("ntp") || serviceActive("chrony") || serviceActive("systemd-timesyncd")),
		GPSLat:       gpsLat,
		GPSLon:       gpsLon,
		GPSAlt:       gpsAlt,
		Ch2G:         getChannel("2.4"),
		Ch5G:         getChannel("5"),
		IsLimp:       boolStr(fileExists("/var/run/mesh_limp_mode")),
		Timestamp:    fmt.Sprintf("%d", time.Now().Unix()),
		Applets:      scanApplets(),
		HalowTxMCS:   mcs["WLAN2_TX_MCS"],
		HalowRxMCS:   mcs["WLAN2_RX_MCS"],
		HalowMCSPeer: mcs["WLAN2_MCS_PEER"],
		Wifi24TxMCS:  mcs["WLAN0_TX_MCS"],
		Wifi24RxMCS:  mcs["WLAN0_RX_MCS"],
		Wifi5TxMCS:   mcs["WLAN1_TX_MCS"],
		Wifi5RxMCS:   mcs["WLAN1_RX_MCS"],
		TQAverage:    getTQAverage(),
		Neighbors:    getDirectNeighbors(),
		ConfigAck:    readFileStr("/var/run/mesh_config_ack_version"),
	}
}

func publish(info NodeInfo) {
	data, err := json.Marshal(info)
	if err != nil {
		log.Printf("marshal: %v", err)
		return
	}
	cmd := exec.Command(alfredBin, "-s", alfredType)
	cmd.Stdin = strings.NewReader(string(data))
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("alfred publish: %v: %s", err, out)
	}
}

var alfredRE = regexp.MustCompile(`\{\s*"([^"]+)",\s*"((?:[^"\\]|\\.)*)"\s*\}`)

func readPeers() map[string]NodeInfo {
	peers := make(map[string]NodeInfo)
	out, err := exec.Command(alfredBin, "-r", alfredType).Output()
	if err != nil {
		return peers
	}
	for _, m := range alfredRE.FindAllStringSubmatch(string(out), -1) {
		mac := m[1]
		payload := unescapeAlfred(m[2])
		var info NodeInfo
		if json.Unmarshal([]byte(payload), &info) == nil {
			peers[mac] = info
		}
	}
	return peers
}

func unescapeAlfred(s string) string {
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\x0a`, "\n")
	s = strings.ReplaceAll(s, `\x09`, "\t")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

var knownNodes = make(map[string]NodeInfo)
var prevLive = make(map[string]bool)

func loadKnownNodes() {
	data, err := os.ReadFile(nodesFile)
	if err != nil {
		return
	}
	var nodes map[string]NodeInfo
	if json.Unmarshal(data, &nodes) == nil {
		knownNodes = nodes
		log.Printf("loaded %d known nodes from disk", len(nodes))
	}
}

func saveKnownNodes() {
	data, err := json.Marshal(knownNodes)
	if err != nil {
		log.Printf("marshal known nodes: %v", err)
		return
	}
	os.MkdirAll(filepath.Dir(nodesFile), 0755)
	tmp := nodesFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("write known nodes: %v", err)
		return
	}
	os.Rename(tmp, nodesFile)
}

func writeRegistry(self NodeInfo, peers map[string]NodeInfo) {
	// Build set of live MACs from current alfred data
	liveMACs := make(map[string]bool)
	liveMACs[self.MAC] = true
	for _, p := range peers {
		liveMACs[p.MAC] = true
	}

	// Merge current peers into known nodes, always keyed by NodeInfo.MAC.
	// Dedup by IP: if a live node has the same IP as a stale entry under
	// a different MAC (e.g. bat0 MAC changed after reboot), drop the stale one.
	knownNodes[self.MAC] = self
	for _, p := range peers {
		if p.MAC != self.MAC {
			knownNodes[p.MAC] = p
		}
	}
	for mac := range knownNodes {
		if liveMACs[mac] {
			continue
		}
		stale := knownNodes[mac]
		if stale.IPv4 == "" {
			continue
		}
		for liveMac := range liveMACs {
			if live, ok := knownNodes[liveMac]; ok && live.IPv4 == stale.IPv4 && liveMac != mac {
				delete(knownNodes, mac)
				break
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Mesh Node Registry - Generated %s\n", time.Now().Format(time.RFC1123))
	fmt.Fprintln(&b, "# Sourced by other scripts to get network state.")
	fmt.Fprintln(&b)

	for mac, n := range knownNodes {
		isLive := liveMACs[mac]
		if !isLive {
			n.IsLimp = "false"
		}
		writeNodeWithState(&b, n, isLive)
	}

	tmp := registryFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		log.Printf("write registry: %v", err)
		return
	}
	os.Rename(tmp, registryFile)
	saveKnownNodes()

	// Emit peer-join / peer-leave events
	for mac := range liveMACs {
		if mac == self.MAC {
			continue
		}
		if !prevLive[mac] {
			n := knownNodes[mac]
			go meshHook("peer-join", "MAC="+mac, "IP="+n.IPv4, "HOSTNAME="+n.Hostname)
		}
	}
	for mac := range prevLive {
		if !liveMACs[mac] {
			n := knownNodes[mac]
			go meshHook("peer-leave", "MAC="+mac, "IP="+n.IPv4, "HOSTNAME="+n.Hostname)
		}
	}
	prevLive = make(map[string]bool)
	for mac := range liveMACs {
		if mac != self.MAC {
			prevLive[mac] = true
		}
	}
}

func writeNodeWithState(b *strings.Builder, n NodeInfo, isLive bool) {
	if n.MAC == "" {
		return
	}
	prefix := "NODE_" + strings.ReplaceAll(strings.ReplaceAll(n.MAC, ":", ""), "-", "")
	w := func(field, val string) {
		fmt.Fprintf(b, "%s_%s='%s'\n", prefix, field, val)
	}
	w("HOSTNAME", n.Hostname)
	w("MAC_ADDRESS", n.MAC)
	w("MAC_ADDRESSES", n.MACAddresses)
	w("IPV4_ADDRESS", n.IPv4)
	w("IPV4_CHUNK", n.IPv4Chunk)
	w("UPTIME_SECONDS", n.Uptime)
	w("BATTERY_PERCENTAGE", n.Battery)
	w("CPU_LOAD_AVERAGE", n.CPULoad)
	w("IS_GATEWAY", n.IsGateway)
	w("GATEWAY_IFACE", n.GatewayIface)
	w("IS_NTP_SERVER", n.IsNTP)
	w("GPS_LATITUDE", n.GPSLat)
	w("GPS_LONGITUDE", n.GPSLon)
	w("GPS_ALTITUDE", n.GPSAlt)
	w("DATA_CHANNEL_2_4", n.Ch2G)
	w("DATA_CHANNEL_5_0", n.Ch5G)
	w("IS_IN_LIMP_MODE", n.IsLimp)
	w("LAST_SEEN_TIMESTAMP", n.Timestamp)
	state := "ACTIVE"
	if !isLive {
		state = "OFFLINE"
	}
	w("NODE_STATE", state)
	w("APPLETS", n.Applets)
	w("HALOW_TX_MCS", n.HalowTxMCS)
	w("HALOW_RX_MCS", n.HalowRxMCS)
	w("HALOW_MCS_PEER", n.HalowMCSPeer)
	w("WIFI_24_TX_MCS", n.Wifi24TxMCS)
	w("WIFI_24_RX_MCS", n.Wifi24RxMCS)
	w("WIFI_5_TX_MCS", n.Wifi5TxMCS)
	w("WIFI_5_RX_MCS", n.Wifi5RxMCS)
	w("TQ_AVERAGE", n.TQAverage)
	w("DIRECT_NEIGHBORS", n.Neighbors)
	w("CONFIG_ACK_VERSION", n.ConfigAck)
	fmt.Fprintln(b)
}

// --- System data collection ---

func meshHook(event string, args ...string) {
	cmdArgs := append([]string{event}, args...)
	exec.Command("/usr/local/bin/mesh-hook", cmdArgs...).Run()
}

func getMyMAC() string {
	for _, name := range []string{"bat0", "br0"} {
		iface, err := net.InterfaceByName(name)
		if err == nil && len(iface.HardwareAddr) > 0 {
			return iface.HardwareAddr.String()
		}
	}
	return ""
}

func getAllMACs() string {
	var macs []string
	for _, name := range []string{"bat0", "br0", "wlan0", "wlan1", "wlan2", "wlan3"} {
		iface, err := net.InterfaceByName(name)
		if err == nil && len(iface.HardwareAddr) > 0 {
			macs = append(macs, iface.HardwareAddr.String())
		}
	}
	return strings.Join(macs, ",")
}

func getIPv4() string {
	// Try state file first
	if data, err := os.ReadFile(stateFile); err == nil {
		var state map[string]string
		if json.Unmarshal(data, &state) == nil {
			if ip := state["CURRENT_IPV4"]; ip != "" {
				return ip
			}
			if ip := state["PERSISTENT_IPV4"]; ip != "" {
				return ip
			}
		}
	}
	// Try reading state file as KV
	if kv := loadKV(stateFile); kv["CURRENT_IPV4"] != "" {
		return kv["CURRENT_IPV4"]
	}
	// Fall back to br0 address
	iface, err := net.InterfaceByName("br0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

func getUptimeSeconds() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func getBatteryPct() string {
	data, err := os.ReadFile("/sys/class/power_supply/BAT0/capacity")
	if err != nil {
		data, err = os.ReadFile("/sys/class/power_supply/battery/capacity")
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(data))
}

func getCPULoad() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func getGatewayInfo() (string, string) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "false", ""
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "00000000" {
			iface := fields[0]
			if iface == "br0" || iface == "bat0" {
				continue
			}
			return "true", iface
		}
	}
	return "false", ""
}

func getGPS() (string, string, string) {
	out, err := exec.Command("gpspipe", "-w", "-n", "5").Output()
	if err != nil {
		return "", "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, `"class":"TPV"`) {
			continue
		}
		var tpv map[string]interface{}
		if json.Unmarshal([]byte(line), &tpv) != nil {
			continue
		}
		mode, _ := tpv["mode"].(float64)
		if mode < 2 {
			continue
		}
		lat := fmt.Sprintf("%f", tpv["lat"])
		lon := fmt.Sprintf("%f", tpv["lon"])
		alt := ""
		if a, ok := tpv["altMSL"].(float64); ok {
			alt = fmt.Sprintf("%.1f", a)
		} else if a, ok := tpv["alt"].(float64); ok {
			alt = fmt.Sprintf("%.1f", a)
		}
		return lat, lon, alt
	}
	return "", "", ""
}

func getChannel(band string) string {
	out, err := exec.Command("iw", "dev").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if strings.Contains(line, "channel") {
			ch := strings.TrimSpace(line)
			freq := 0
			if parts := strings.Fields(ch); len(parts) >= 2 {
				if f, err := strconv.Atoi(parts[1]); err == nil {
					freq = f
				}
				// Try extracting from parenthetical
				for _, p := range parts {
					p = strings.Trim(p, "()")
					if f, err := strconv.Atoi(p); err == nil && f > 100 {
						freq = f
					}
				}
			}
			if band == "2.4" && freq >= 2400 && freq <= 2500 {
				return extractChannelNum(lines, i)
			}
			if band == "5" && freq >= 5000 && freq <= 6000 {
				return extractChannelNum(lines, i)
			}
		}
	}
	return ""
}

func extractChannelNum(lines []string, idx int) string {
	line := strings.TrimSpace(lines[idx])
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

func serviceActive(name string) bool {
	err := exec.Command("systemctl", "is-active", "--quiet", name).Run()
	return err == nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFileStr(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func collectMCS() map[string]string {
	result := make(map[string]string)
	for _, iface := range []string{"wlan0", "wlan1", "wlan2"} {
		if !fileExists("/sys/class/net/" + iface) {
			continue
		}
		out, err := exec.Command("/usr/local/bin/halow-mcs-summary", "--iface", iface, "--shell").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				result[k] = strings.Trim(v, "'")
			}
		}
	}
	return result
}

func getDirectNeighbors() string {
	out, err := exec.Command(batctlBin, "n").Output()
	if err != nil {
		return ""
	}
	var entries []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.Contains(fields[0], ":") {
			entry := fields[0]
			if len(fields) >= 4 {
				speed := strings.Trim(fields[3], "()")
				if _, err := strconv.ParseFloat(speed, 64); err == nil {
					entry += "=" + speed
				}
			}
			entries = append(entries, entry)
		}
	}
	return strings.Join(entries, ",")
}

func getTQAverage() string {
	out, err := exec.Command("/usr/sbin/batctl", "o").Output()
	if err != nil {
		return "0"
	}
	var sum, count float64
	for _, line := range strings.Split(string(out), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if v, err := strconv.ParseFloat(strings.Trim(fields[2], "()"), 64); err == nil {
				sum += v
				count++
			}
		}
	}
	if count > 0 {
		return fmt.Sprintf("%.2f", sum/count)
	}
	return "0"
}

func loadKV(path string) map[string]string {
	m := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			v = strings.Trim(v, "'\"")
			m[k] = v
		}
	}
	return m
}

func scanApplets() string {
	entries, err := os.ReadDir(appletsDir)
	if err != nil {
		return ""
	}
	var parts []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(appletsDir, e.Name(), "applet.json"))
		if err != nil {
			continue
		}
		var m struct {
			Name    string `json:"name"`
			Label   string `json:"label"`
			Backend struct {
				Service string `json:"service"`
			} `json:"backend"`
		}
		if json.Unmarshal(data, &m) != nil || m.Name == "" {
			continue
		}
		label := m.Label
		if label == "" {
			label = m.Name
		}
		svc := m.Backend.Service
		if svc == "" {
			svc = m.Name + ".service"
		}
		status := "unknown"
		if exec.Command("systemctl", "is-active", "--quiet", svc).Run() == nil {
			status = "running"
		} else {
			status = "stopped"
		}
		parts = append(parts, m.Name+"|"+label+"|"+status)
	}
	return strings.Join(parts, ",")
}
