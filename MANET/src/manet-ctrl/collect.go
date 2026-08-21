package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Command runner ---

func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func runCmdStdout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// --- batman-adv ---

var origRE = regexp.MustCompile(`(\*)?\s*([0-9a-f:]{17})\s+([\d.]+)(?:ms|s)\s+\(\s*([\d.]+)\)\s+([0-9a-f:]{17})(?:\s+\[\s*(\S+)\s*\])?`)
var neighRE = regexp.MustCompile(`([0-9a-f:]{17})\s+([\d.]+)(?:ms|s)\s+\(\s*([\d.]+)\)\s+\[\s*(\S+)\s*\]`)
var macRE = regexp.MustCompile(`([0-9a-f]{2}(?::[0-9a-f]{2}){5})`)

var batmanV bool
var batmanVOnce sync.Once

func isBatmanV() bool {
	batmanVOnce.Do(func() {
		out, err := runCmdStdout(3*time.Second, "batctl", "routing_algo")
		if err == nil && strings.Contains(out, "BATMAN_V") {
			batmanV = true
		}
	})
	return batmanV
}

// HalowBWMaxMbps is the realistic max link throughput per S1G channel width,
// used as the 100% reference when converting BATMAN_V throughput to TQ. A
// fixed 35 Mbps reference made a full-rate 1 MHz link (~3.3 Mbps) read TQ 23.
var HalowBWMaxMbps = map[string]float64{
	"1MHz": 3.3, "2MHz": 7.2, "4MHz": 16, "8MHz": 32,
}

var (
	meshRefMu   sync.Mutex
	meshRefMbps = 35.0
	meshRefAt   time.Time
)

// meshRefThroughput re-resolves at most once a minute so a live bandwidth
// change (halow_bw apply does not restart manet-ctrl) re-grades TQ without
// paying a driver query per originator line.
func meshRefThroughput() float64 {
	meshRefMu.Lock()
	defer meshRefMu.Unlock()
	if !meshRefAt.IsZero() && time.Since(meshRefAt) < time.Minute {
		return meshRefMbps
	}
	meshRefAt = time.Now()
	meshRefMbps = 35.0
	info := getHalowDriverInfo("wlan2")
	if bw, ok := info["halow_bw"]; ok {
		if max, ok := HalowBWMaxMbps[bw]; ok {
			meshRefMbps = max
		}
	}
	return meshRefMbps
}

// --- Link budget ---

// Approximate S1G receiver decode floors (dBm) per MCS at 1 MHz. Wider
// channels shift the floor up by ~3 dB per doubling (thermal noise scales
// with bandwidth). Values follow docs/halow-range-calc.md / MM6108 typicals.
var s1gFloor1MHz = map[int]float64{
	0: -97, 1: -95, 2: -93, 3: -90, 4: -88,
	5: -85, 6: -83, 7: -81, 8: -76, 9: -73,
}

var s1gBWFloorOffset = map[string]float64{
	"1MHz": 0, "2MHz": 3, "4MHz": 6, "8MHz": 9,
}

type StationLink struct {
	MAC          string
	SignalDBM    int
	SignalAvg    int
	TxMbit       float64
	TxMCS        int
	RxMbit       float64
	RxMCS        int
	TxRetries    int64
	TxPackets    int64
	TxFailed     int64
	ExpectedMbps float64
}

func parseBitrateLine(s string) (float64, int) {
	mbit := 0.0
	fmt.Sscanf(s, "%f MBit/s", &mbit)
	mcs := -1
	if m := regexp.MustCompile(`MCS (\d+)`).FindStringSubmatch(s); len(m) > 1 {
		mcs, _ = strconv.Atoi(m[1])
	}
	return mbit, mcs
}

func runStationDump(iface string) map[string]*StationLink {
	out := map[string]*StationLink{}
	txt, err := runCmdStdout(5*time.Second, "iw", "dev", iface, "station", "dump")
	if err != nil {
		return out
	}
	var cur *StationLink
	for _, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Station ") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				cur = &StationLink{MAC: normMAC(f[1]), TxMCS: -1, RxMCS: -1}
				out[cur.MAC] = cur
			}
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "signal":
			fmt.Sscanf(v, "%d", &cur.SignalDBM)
		case "signal avg":
			fmt.Sscanf(v, "%d", &cur.SignalAvg)
		case "tx bitrate":
			cur.TxMbit, cur.TxMCS = parseBitrateLine(v)
		case "rx bitrate":
			cur.RxMbit, cur.RxMCS = parseBitrateLine(v)
		case "tx retries":
			cur.TxRetries, _ = strconv.ParseInt(v, 10, 64)
		case "tx packets":
			cur.TxPackets, _ = strconv.ParseInt(v, 10, 64)
		case "tx failed":
			cur.TxFailed, _ = strconv.ParseInt(v, 10, 64)
		case "expected throughput":
			fmt.Sscanf(v, "%fMbps", &cur.ExpectedMbps)
		}
	}
	return out
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// buildLinkBudget converts raw station stats into a link-budget view. The
// morse driver presents S1G as a 5 GHz alias: reported bitrates are 20x the
// real over-the-air rate, so divide when the interface is HaLow.
func buildLinkBudget(st *StationLink, iface, halowBW string) map[string]interface{} {
	isHalow := halowBW != "" && iface == "wlan2"
	scale := 1.0
	if isHalow {
		scale = 20.0
	}
	m := map[string]interface{}{
		"signal":     st.SignalDBM,
		"signal_avg": st.SignalAvg,
		"mcs":        st.TxMCS,
		"phy_mbps":   round1(st.TxMbit / scale),
		"rx_mbps":    round1(st.RxMbit / scale),
		"tx_retries": st.TxRetries,
		"tx_failed":  st.TxFailed,
	}
	if st.ExpectedMbps > 0 {
		m["expected_mbps"] = st.ExpectedMbps
	}
	if st.TxPackets > 0 {
		m["retry_pct"] = round1(float64(st.TxRetries) * 100 / float64(st.TxPackets))
	}
	if isHalow && st.TxMCS >= 0 && st.SignalDBM < 0 {
		if base, ok := s1gFloor1MHz[st.TxMCS]; ok {
			floor := base + s1gBWFloorOffset[halowBW]
			m["floor"] = floor
			m["margin"] = round1(float64(st.SignalDBM) - floor)
		}
	}
	return m
}

func normTQ(raw float64) int {
	if isBatmanV() {
		return int(math.Min(raw*255/meshRefThroughput(), 255))
	}
	if raw > 255 {
		return int(math.Min(raw/1000*255, 255))
	}
	return int(raw)
}

func runBatctlOriginators() (map[string]int, map[string]BatOriginator) {
	tqMap := make(map[string]int)
	origMap := make(map[string]BatOriginator)
	out, err := runCmdStdout(5*time.Second, "batctl", "o", "-n")
	if err != nil {
		return tqMap, origMap
	}
	for _, m := range origRE.FindAllStringSubmatch(out, -1) {
		selected := m[1] == "*"
		orig := normMAC(m[2])
		lastSeen, _ := strconv.ParseFloat(m[3], 64)
		raw, _ := strconv.ParseFloat(m[4], 64)
		tq := normTQ(raw)
		nexthop := normMAC(m[5])
		iface := ""
		if len(m) > 6 {
			iface = m[6]
		}
		var rawTP float64
		if isBatmanV() {
			rawTP = raw
		}
		// Ignore originators not heard from in over 60 seconds
		if lastSeen > 60 {
			continue
		}
		// BATMAN_V's TQ metric saturates at 255 for any link at or above
		// meshRefThroughput, so two real candidates to the same neighbor
		// (e.g. a faster WiFi mesh radio and a slower HaLow radio) can tie
		// on TQ alone — picking between ties via Go's randomized map
		// iteration order silently produced a different "best" interface
		// on every restart, even though batman-adv itself deterministically
		// picks one and marks it with "*" in its own output. Prefer that
		// ground truth outright; only fall back to the highest-TQ heuristic
		// while no entry for this originator is marked selected yet.
		if prev, ok := origMap[orig]; !ok || selected || (!prev.Selected && tq > prev.TQ) {
			origMap[orig] = BatOriginator{TQ: tq, RawTP: rawTP, Nexthop: nexthop, Iface: iface, LastSeen: lastSeen, Selected: selected}
		}
		if tq > tqMap[orig] {
			tqMap[orig] = tq
		}
		if tq > tqMap[nexthop] {
			tqMap[nexthop] = tq
		}
	}
	return tqMap, origMap
}

func runBatctlNeighbors() []BatNeighbor {
	var neighbors []BatNeighbor
	out, err := runCmdStdout(5*time.Second, "batctl", "n", "-n")
	if err != nil {
		return neighbors
	}
	for _, m := range neighRE.FindAllStringSubmatch(out, -1) {
		lastSeen, _ := strconv.ParseFloat(m[2], 64)
		if lastSeen > 60 {
			continue
		}
		raw, _ := strconv.ParseFloat(m[3], 64)
		neighbors = append(neighbors, BatNeighbor{
			Iface: m[4], MAC: normMAC(m[1]), TQ: normTQ(raw), LastSeen: lastSeen,
		})
	}
	return neighbors
}

func runBatctlGateways() []BatGateway {
	var gateways []BatGateway
	out, err := runCmdStdout(5*time.Second, "batctl", "gwl", "-n")
	if err != nil {
		return gateways
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Gateway") || !macRE.MatchString(line) {
			continue
		}
		selected := strings.HasPrefix(line, "=>")
		mac := macRE.FindString(line)
		tq := 0
		if m := regexp.MustCompile(`\(\s*([\d.]+)\s*\)`).FindStringSubmatch(line); len(m) > 1 {
			raw, _ := strconv.ParseFloat(m[1], 64)
			tq = normTQ(raw)
		}
		gateways = append(gateways, BatGateway{
			MAC: normMAC(mac), TQ: tq, Selected: selected,
		})
	}
	return gateways
}

func bestOrigForNode(allMACs []string, origMap map[string]BatOriginator) *BatOriginator {
	var best *BatOriginator
	for _, mac := range allMACs {
		if o, ok := origMap[mac]; ok {
			if best == nil || o.TQ > best.TQ {
				cp := o
				best = &cp
			}
		}
	}
	return best
}

// --- System stats ---

func getSystemStats() *SystemStats {
	s := &SystemStats{}

	// CPU temperature
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err == nil {
		if millideg, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			t := float64(millideg) / 1000.0
			s.CPUTemp = &t
		}
	}

	// Load averages
	data, err = os.ReadFile("/proc/loadavg")
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			s.LoadAvg[0], _ = strconv.ParseFloat(fields[0], 64)
			s.LoadAvg[1], _ = strconv.ParseFloat(fields[1], 64)
			s.LoadAvg[2], _ = strconv.ParseFloat(fields[2], 64)
		}
	}

	// Memory
	data, err = os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				s.MemTotal = val
			case "MemFree:":
				s.MemFree = val
			case "MemAvailable:":
				s.MemAvail = val
			}
		}
	}

	return s
}

// --- System info ---

func getMyMAC() string {
	data, err := os.ReadFile("/sys/class/net/bat0/address")
	if err != nil {
		return ""
	}
	return normMAC(strings.TrimSpace(string(data)))
}

func getMyHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func getBattery() *BatteryInfo {
	data, err := os.ReadFile(BatteryFile)
	if err == nil {
		var b BatteryInfo
		if json.Unmarshal(data, &b) == nil && b.Percentage != nil {
			return &b
		}
	}
	matches, _ := filepath.Glob("/sys/class/power_supply/*/type")
	for _, path := range matches {
		tdata, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(tdata)) != "Battery" {
			continue
		}
		dir := filepath.Dir(path)
		cdata, err := os.ReadFile(filepath.Join(dir, "capacity"))
		if err != nil {
			continue
		}
		pct, _ := strconv.Atoi(strings.TrimSpace(string(cdata)))
		return &BatteryInfo{Percentage: &pct, Status: "unknown"}
	}
	return nil
}

func getNetworkState() *NetworkState {
	conf := loadKVFile(MeshConfFile)
	ns := &NetworkState{
		EUDMode: confGet(conf, "eud", "wired"),
	}

	// Gateway state
	if _, err := os.Stat("/var/run/mesh-gateway.state"); err == nil {
		ns.Gateway = true
	}

	// NTP server
	if _, err := os.Stat("/var/run/mesh-ntp.state"); err == nil {
		ns.NTP = true
	}

	// Upstream interface
	if data, err := os.ReadFile("/var/run/upstream_iface"); err == nil {
		ns.UpstreamIface = strings.TrimSpace(string(data))
	}

	// Ethernet detection state
	if det := loadKVFile("/var/run/ethernet_detection_state"); len(det) > 0 {
		if det["ETH_IP"] != "" {
			ns.GatewayIP = det["ETH_IP"]
		}
		if det["DEFAULT_GW"] != "" && det["DEFAULT_GW"] != "none" {
			ns.DefaultGW = det["DEFAULT_GW"]
		}
	}

	// AP active
	if out, err := runCmdStdout(3*time.Second, "systemctl", "is-active", "hostapd"); err == nil {
		ns.APActive = strings.TrimSpace(out) == "active"
	}

	// EUD active: check if any EUDs are connected (br0 has DHCP leases)
	euds := getEUDs()
	ns.EUDs = euds
	ns.EUDActive = len(euds) > 0
	// Figure out EUD interface
	if ns.APActive {
		ns.EUDIface = "wifi"
	}
	// Check if end0/upstream is bridged to br0 (wired EUD)
	if ns.UpstreamIface != "" && !ns.Gateway {
		if out, err := runCmdStdout(2*time.Second, "ip", "link", "show", ns.UpstreamIface); err == nil {
			if strings.Contains(out, "master br0") {
				ns.EUDIface = ns.UpstreamIface
			}
		}
	}

	// USB tethering: check for USB-backed interfaces
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		name := e.Name()
		if name == "lo" || strings.HasPrefix(name, "bat") || strings.HasPrefix(name, "br") || strings.HasPrefix(name, "wlan") {
			continue
		}
		bus, _ := os.Readlink(filepath.Join("/sys/class/net", name, "device/subsystem"))
		if strings.Contains(bus, "usb") {
			ns.USBTether = true
			ns.USBIface = name
			break
		}
	}

	return ns
}

func getThrottle() *ThrottleInfo {
	out, err := runCmdStdout(3*time.Second, "vcgencmd", "get_throttled")
	if err != nil {
		return nil
	}
	parts := strings.SplitN(strings.TrimSpace(out), "=", 2)
	if len(parts) != 2 {
		return nil
	}
	raw := strings.TrimSpace(parts[1])
	val, err := strconv.ParseUint(strings.TrimPrefix(raw, "0x"), 16, 64)
	if err != nil {
		return nil
	}
	return &ThrottleInfo{
		Raw:           raw,
		Undervoltage:  val&0x1 != 0,
		FreqCapped:    val&0x2 != 0,
		Throttled:     val&0x4 != 0,
		SoftTempLimit: val&0x8 != 0,
		WasUndervolt:  val&0x10000 != 0,
		WasFreqCapped: val&0x20000 != 0,
		WasThrottled:  val&0x40000 != 0,
		WasSoftTemp:   val&0x80000 != 0,
	}
}

func getUptime() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	parts := strings.Fields(string(data))
	if len(parts) == 0 {
		return ""
	}
	secs, _ := strconv.ParseFloat(parts[0], 64)
	return fmtUptime(secs)
}

func fmtUptime(secs float64) string {
	s := int(secs)
	h := s / 3600
	m := (s % 3600) / 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	sec := s % 60
	return fmt.Sprintf("%dm %ds", m, sec)
}

func getEUDs() []EUD {
	var euds []EUD
	now := time.Now().Unix()
	for _, path := range []string{"/var/lib/misc/dnsmasq.leases", "/tmp/dnsmasq.leases", "/run/dnsmasq.leases"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			exp, _ := strconv.ParseInt(fields[0], 10, 64)
			if exp > 0 && exp < now {
				continue
			}
			var expiresIn *int
			if exp > 0 {
				v := int(exp - now)
				expiresIn = &v
			}
			euds = append(euds, EUD{
				MAC: fields[1], IP: fields[2], Hostname: fields[3], ExpiresIn: expiresIn,
			})
		}
		break
	}
	return euds
}

func getRunningServices() map[string]bool {
	checks := map[string][]string{
		"ntp":       {"chrony", "chronyd", "ntp", "ntpd"},
		"tak":       {"tak-server", "takserver"},
	}
	result := make(map[string]bool)
	for svc, units := range checks {
		for _, unit := range units {
			err := exec.Command("systemctl", "is-active", "--quiet", unit).Run()
			if err == nil {
				result[svc] = true
				break
			}
		}
		if !result[svc] {
			result[svc] = false
		}
	}
	return result
}

// --- Network interfaces ---

type iwDev struct {
	Name    string
	Type    string
	SSID    string
	Channel string
	TxPower string
	Freq    string
	Wiphy   string
}

func getInterfaces() []Iface {
	conf := loadKVFile(MeshConfFile)
	eudMode := confGet(conf, "eud", "wired")

	// ip -j addr
	type ipAddr struct {
		IfName   string `json:"ifname"`
		OperState string `json:"operstate"`
		AddrInfo []struct {
			Local      string `json:"local"`
			PrefixLen  int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	var ipAddrs []ipAddr
	if out, err := runCmdStdout(5*time.Second, "ip", "-j", "addr"); err == nil {
		json.Unmarshal([]byte(out), &ipAddrs)
	}
	ipMap := make(map[string]ipAddr)
	for _, a := range ipAddrs {
		ipMap[a.IfName] = a
	}

	// iw dev
	iwDevs := parseIWDev()

	// batctl if
	batSlaves := parseBatctlIf()

	// wpa_supplicant detection
	wpaActive := parseWPAActive()

	// no_mesh_if lists interfaces on a genuinely separate non-mesh chip
	// (e.g. onboard Broadcom used only for AP). radio-setup.sh's AP
	// selection has a second path — carving the AP out of a 5GHz-capable
	// interface on a dual-radio mesh card (e.g. MT7916: wlan0 2.4GHz mesh,
	// wlan1 5GHz AP) — which removes that interface from mesh_if and
	// records it in ap_interface instead, without ever touching
	// no_mesh_if. Without also reading ap_interface here, such an
	// interface is neither a batman slave nor in noMeshIfaces, so it falls
	// through every classification case below and gets silently dropped
	// from the interfaces list entirely — confirmed live on a CM4 with an
	// MT7916 card, where a fully working hostapd AP on wlan1 never
	// appeared anywhere in the UI.
	noMeshIfaces := make(map[string]bool)
	if data, err := os.ReadFile(NoMeshIfFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			name := strings.TrimSpace(line)
			if name != "" {
				noMeshIfaces[name] = true
			}
		}
	}
	if data, err := os.ReadFile(APInterfaceFile); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			noMeshIfaces[name] = true
		}
	}

	var ifaces []Iface

	for _, ip := range ipAddrs {
		name := ip.IfName
		if name == "lo" {
			continue
		}
		state := strings.ToUpper(ip.OperState)
		var addrs []string
		for _, a := range ip.AddrInfo {
			if a.Local != "" {
				addrs = append(addrs, fmt.Sprintf("%s/%d", a.Local, a.PrefixLen))
			}
		}

		iface := Iface{
			Name:   name,
			State:  state,
			Addrs:  addrs,
			Health: "ok",
			Faults: []string{},
		}

		if iw, ok := iwDevs[name]; ok {
			iface.Channel = iw.Channel
			iface.FreqMHz = iw.Freq
			iface.TxPowerDBM = iw.TxPower
		}

		// Classify role — AP check must precede batSlaves so no_mesh_if wins
		switch {
		case name == "bat0":
			iface.Role = "bat"
			iface.Health = "info"
			iface.Detail = "batman-adv virtual interface"
			if state == "DOWN" {
				iface.Health = "fault"
				iface.Faults = append(iface.Faults, "Interface is DOWN")
			}
		case name == "br0":
			iface.Role = "bridge"
			iface.Health = "info"
			iface.Detail = "Network bridge"
		case noMeshIfaces[name]:
			iface.Role = "ap"
			iface.Detail = "Access point"
			hostapdUp := isUnitActive("hostapd")
			if state == "DOWN" || state == "UNKNOWN" {
				if hostapdUp {
					iface.Health = "warn"
					iface.Faults = append(iface.Faults, "DOWN but hostapd running")
				} else {
					iface.Health = "info"
					iface.Detail = "Access point (inactive)"
				}
			} else if !hostapdUp {
				iface.Health = "warn"
				iface.Faults = append(iface.Faults, "hostapd not running")
			}
		case batSlaves[name] != "":
			iface.Role = "mesh"
			iface.Detail = "Mesh radio"
			batState := batSlaves[name]
			wpaRestarting := isUnitRestarting("wpa_supplicant@"+name) || isUnitRestarting("wpa_supplicant-s1g-"+name)
			if state == "DOWN" && wpaRestarting {
				iface.Health = "warn"
				iface.Faults = append(iface.Faults, "Restarting")
			} else if state == "DOWN" {
				iface.Health = "fault"
				iface.Faults = append(iface.Faults, "Interface is DOWN")
			} else if batState == "inactive" && wpaRestarting {
				iface.Health = "warn"
				iface.Faults = append(iface.Faults, "Restarting")
			} else if batState == "inactive" {
				iface.Health = "fault"
				iface.Faults = append(iface.Faults, "Inactive in batman-adv")
			} else if !wpaActive[name] {
				iface.Health = "warn"
				iface.Faults = append(iface.Faults, "No wpa_supplicant")
			}
		case strings.HasPrefix(name, "end") || strings.HasPrefix(name, "eth") ||
			strings.HasPrefix(name, "enp") || strings.HasPrefix(name, "ens"):
			hasDefault := false
			if out, err := runCmdStdout(3*time.Second, "ip", "route", "show", "dev", name); err == nil {
				hasDefault = strings.Contains(out, "default")
			}
			if hasDefault {
				iface.Role = "gateway"
				iface.Detail = "Uplink gateway"
			} else if state == "UP" && eudMode == "wired" {
				iface.Role = "eud-bridge"
				iface.Detail = "EUD bridge"
			} else {
				iface.Role = "other"
				iface.Detail = "Ethernet"
			}
		case strings.HasPrefix(name, "wlan") || strings.HasPrefix(name, "halow") || strings.HasPrefix(name, "mlan"):
			if _, inBat := batSlaves[name]; inBat {
				iface.Role = "mesh"
			} else {
				continue
			}
			iface.Detail = "Wireless"
		default:
			iface.Role = "other"
			iface.Detail = name
		}

		// Detect HaLow driver
		if strings.HasPrefix(name, "wlan") || strings.HasPrefix(name, "halow") {
			if out, err := runCmdStdout(3*time.Second, "ethtool", "-i", name); err == nil {
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, "driver:") {
						iface.Driver = strings.TrimSpace(strings.TrimPrefix(line, "driver:"))
					}
				}
			}
		}

		ifaces = append(ifaces, iface)
	}

	sortIfaces(ifaces)
	return ifaces
}

func isUnitActive(unit string) bool {
	err := exec.Command("systemctl", "is-active", "--quiet", unit).Run()
	return err == nil
}

func isUnitRestarting(unit string) bool {
	out, _ := runCmdStdout(2*time.Second, "systemctl", "show", "-p", "ActiveState", "--value", unit+".service")
	s := strings.TrimSpace(out)
	return s == "activating" || s == "reloading" || s == "deactivating"
}

func parseIWDev() map[string]iwDev {
	devs := make(map[string]iwDev)
	out, err := runCmdStdout(5*time.Second, "iw", "dev")
	if err != nil {
		return devs
	}
	var current string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Interface ") {
			current = strings.TrimPrefix(trimmed, "Interface ")
			devs[current] = iwDev{Name: current}
		} else if current != "" {
			d := devs[current]
			switch {
			case strings.HasPrefix(trimmed, "ssid "):
				d.SSID = strings.TrimPrefix(trimmed, "ssid ")
			case strings.HasPrefix(trimmed, "channel "):
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 {
					d.Channel = parts[1]
				}
			case strings.HasPrefix(trimmed, "txpower "):
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 {
					d.TxPower = parts[1]
				}
			case strings.HasPrefix(trimmed, "wiphy "):
				d.Wiphy = strings.TrimPrefix(trimmed, "wiphy ")
			}
			if strings.Contains(trimmed, "MHz") {
				if m := regexp.MustCompile(`(\d+)\s*MHz`).FindStringSubmatch(trimmed); len(m) > 1 {
					d.Freq = m[1]
				}
			}
			devs[current] = d
		}
	}
	return devs
}

func parseBatctlIf() map[string]string {
	slaves := make(map[string]string)
	out, err := runCmdStdout(5*time.Second, "batctl", "if")
	if err != nil {
		return slaves
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			name := strings.TrimSpace(parts[0])
			state := strings.TrimSpace(parts[1])
			slaves[name] = state
		}
	}
	return slaves
}

func parseWPAActive() map[string]bool {
	active := make(map[string]bool)
	out, _ := runCmdStdout(5*time.Second, "systemctl", "list-units", "--state=active", "--no-legend",
		"wpa_supplicant*", "wpa_supplicant-s1g*")
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if m := regexp.MustCompile(`wpa_supplicant[@-](\S+?)(?:\.service)?$`).FindStringSubmatch(unit); len(m) > 1 {
			iface := m[1]
			iface = strings.TrimPrefix(iface, "s1g-")
			active[iface] = true
		}
	}
	return active
}

func enrichIfacesWithHalow(ifaces []Iface) []Iface {
	for i, iface := range ifaces {
		// morse_usb boards: morse_cli's NL80211 vendor-command queries fail
		// outright on this transport (confirmed live — every subcommand,
		// not just "channel", errors with "Failed to rcvmsgs"), so
		// getHalowDriverInfo falls through to its wpa_supplicant-config
		// fallback below, which is driver-agnostic.
		if iface.Driver != "morse_spi" && iface.Driver != "morse_usb" {
			continue
		}
		info := getHalowDriverInfo(iface.Name)
		if bw, ok := info["halow_bw"]; ok {
			ifaces[i].HalowBW = bw
		}
		if src, ok := info["halow_source"]; ok {
			ifaces[i].HalowSource = src
		}
		// Overwrite unconditionally, not just when empty: this loop already
		// only reaches morse_spi/morse_usb interfaces, and the generic `iw
		// dev` parse used to pre-populate Channel/FreqMHz reports the
		// driver's internal VHT-mapped representation of the S1G channel
		// (e.g. "36 / 5180MHz", matching neither the real S1G channel nor
		// anything a user configured) — confirmed live on a USB HaLow
		// board. The HaLow-specific source here (morse_cli or the
		// wpa_supplicant-config fallback) is always more accurate for this
		// radio and should win.
		if ch, ok := info["channel"]; ok {
			ifaces[i].Channel = ch
		}
		if freq, ok := info["freq_mhz"]; ok {
			ifaces[i].FreqMHz = freq
		}
		if cap, ok := HalowBWTxPowerCapDBM[ifaces[i].HalowBW]; ok {
			ifaces[i].TxPowerCapDBM = cap
		}
	}
	return ifaces
}

func enrichIfacesWithMCS(ifaces []Iface, regNode RegistryNode) []Iface {
	mcsMap := map[string][2]string{
		"wlan0": {regNode["WIFI_24_TX_MCS"], regNode["WIFI_24_RX_MCS"]},
		"wlan1": {regNode["WIFI_5_TX_MCS"], regNode["WIFI_5_RX_MCS"]},
		"wlan2": {regNode["HALOW_TX_MCS"], regNode["HALOW_RX_MCS"]},
	}
	for i, iface := range ifaces {
		if mcs, ok := mcsMap[iface.Name]; ok {
			ifaces[i].TxMCS = mcs[0]
			ifaces[i].RxMCS = mcs[1]
		}
	}
	return ifaces
}

// --- Radio ---

// readS1GChannelFromConfig returns the conventional S1G channel number
// (e.g. "12") from the generated wpa_supplicant config — the only place
// that number exists; morse_cli's own JSON doesn't carry it.
func readS1GChannelFromConfig() string {
	for _, path := range []string{
		"/etc/wpa_supplicant/wpa_supplicant-wlan2-s1g.conf",
		"/etc/wpa_supplicant/wpa_supplicant_s1g-wlan2.conf",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if m := regexp.MustCompile(`channel=(\d+)`).FindStringSubmatch(string(data)); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func getHalowDriverInfo(iface string) map[string]string {
	if iface == "" {
		iface = "wlan2"
	}
	info := make(map[string]string)

	cmds := [][]string{
		{"/usr/local/bin/morse_cli", "-i", iface, "channel", "-j"},
		{"/usr/local/bin/morse_cli", "-i", iface, "channel", "--json"},
		{"/usr/local/bin/morse_cli", "channel", "-i", iface, "-j"},
		{"/usr/local/bin/morse_cli", "-i", iface, "channel"},
		{"/usr/local/bin/morse_cli", "channel", "-i", iface},
		{"morse_cli", "-i", iface, "channel", "-j"},
		{"morse_cli", "-i", iface, "channel"},
	}

	for _, cmd := range cmds {
		out, err := runCmdStdout(3*time.Second, cmd[0], cmd[1:]...)
		if err != nil {
			continue
		}
		parsed := parseMorseChannelOutput(out)
		if len(parsed) > 0 {
			for k, v := range parsed {
				info[k] = v
			}
			info["halow_source"] = "morse"
			// morse_cli's own JSON has no field for the conventional S1G
			// channel number (confirmed live: channel_frequency,
			// channel_op_bw, channel_primary_bw, channel_index, bw_mhz —
			// channel_index is a different concept, a sub-index that's
			// legitimately 0 here, not the channel number). Only the
			// wpa_supplicant config has that, so pull it in as a
			// supplement even though morse_cli otherwise succeeded.
			if ch := readS1GChannelFromConfig(); ch != "" {
				info["channel"] = ch
			}
			return info
		}
	}

	// Fallback: parse wpa_supplicant config
	for _, path := range []string{
		"/etc/wpa_supplicant/wpa_supplicant-wlan2-s1g.conf",
		"/etc/wpa_supplicant/wpa_supplicant_s1g-wlan2.conf",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		if m := regexp.MustCompile(`channel=(\d+)`).FindStringSubmatch(text); len(m) > 1 {
			info["channel"] = m[1]
		}
		if m := regexp.MustCompile(`op_class=(\d+)`).FindStringSubmatch(text); len(m) > 1 {
			switch m[1] {
			case "66", "68":
				info["halow_bw"] = "1MHz"
			case "67", "69", "70":
				info["halow_bw"] = "2MHz"
			case "71", "72":
				// radio-setup.sh's default case (halow_bw unset or any
				// value other than 1MHz/2MHz/8MHz) and its 8MHz case both
				// resolve to op_class 71 (channel 12) as of the op_class
				// 72/channel 8 fix — confirmed live via wpa_supplicant_s1g's
				// own channel-info log ("Operating BW: 8 MHz"), not 4MHz as
				// previously labeled here.
				info["halow_bw"] = "8MHz"
			}
		}
		info["halow_source"] = "config"
		return info
	}
	return info
}

func parseMorseChannelOutput(text string) map[string]string {
	info := make(map[string]string)
	var data map[string]interface{}
	if json.Unmarshal([]byte(text), &data) == nil {
		freqKeys := []string{"channel_frequency", "frequency", "freq", "freq_khz", "freq_hz", "operating_frequency", "op_chan_freq"}
		bwKeys := []string{"channel_op_bw", "op_bw", "operating_bw", "channel_bw", "bandwidth", "bw", "op_chan_bw"}
		// No "channel_index": confirmed live that's morse_cli's actual JSON
		// schema for the S1G primary-channel sub-index (legitimately 0 in
		// our config's s1g_prim_1mhz_chan_index=0), not a channel number —
		// morse_cli has no field for that at all. getHalowDriverInfo pulls
		// the real channel number from the wpa_supplicant config instead.
		chKeys := []string{"channel", "s1g_channel", "primary_channel"}

		for _, k := range freqKeys {
			if v, ok := data[k]; ok {
				info["freq_mhz"] = fmt.Sprintf("%v", v)
				break
			}
		}
		for _, k := range bwKeys {
			if v, ok := data[k]; ok {
				info["halow_bw"] = formatHalowBW(fmt.Sprintf("%v", v))
				break
			}
		}
		for _, k := range chKeys {
			if v, ok := data[k]; ok {
				info["channel"] = fmt.Sprintf("%v", v)
				break
			}
		}
	}
	return info
}

func formatHalowBW(val string) string {
	val = strings.TrimSpace(val)
	if strings.HasSuffix(strings.ToLower(val), "mhz") {
		return val
	}
	n, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return val
	}
	if n > 100000 {
		n /= 1000000
	} else if n > 100 {
		n /= 1000
	}
	return fmt.Sprintf("%dMHz", int(n))
}

func readIfaceTxPower(iface string) string {
	out, err := runCmdStdout(5*time.Second, "iw", "dev", iface, "info")
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`txpower\s+([\d.]+)\s+dBm`).FindStringSubmatch(out); len(m) > 1 {
		return m[1]
	}
	return ""
}

func getIfaceTxPowerCap(iface string) string {
	if iface == "wlan2" {
		info := getHalowDriverInfo(iface)
		if bw, ok := info["halow_bw"]; ok {
			if cap, ok := HalowBWTxPowerCapDBM[bw]; ok {
				return cap
			}
		}
	}
	out, err := runCmdStdout(5*time.Second, "iw", "phy")
	if err != nil {
		return ""
	}
	maxPower := 0.0
	for _, line := range strings.Split(out, "\n") {
		if m := regexp.MustCompile(`\(([\d.]+)\s+dBm\)`).FindStringSubmatch(line); len(m) > 1 {
			v, _ := strconv.ParseFloat(m[1], 64)
			if v > maxPower {
				maxPower = v
			}
		}
	}
	if maxPower > 0 {
		return fmtDBM(maxPower)
	}
	return ""
}

func fmtDBM(v float64) string {
	if math.Abs(v-math.Round(v)) < 0.05 {
		return strconv.Itoa(int(math.Round(v)))
	}
	return fmt.Sprintf("%.1f", v)
}

func setIfaceTxPower(iface string, dbm float64) (string, string, error) {
	mbm := int(dbm * 100)
	_, err := runCmd(5*time.Second, "iw", "dev", iface, "set", "txpower", "fixed", strconv.Itoa(mbm))
	if err != nil {
		return "", "", err
	}
	for i := 0; i < 6; i++ {
		time.Sleep(250 * time.Millisecond)
		actual := readIfaceTxPower(iface)
		if actual != "" {
			av, _ := strconv.ParseFloat(actual, 64)
			if math.Abs(av-dbm) < 0.5 {
				return fmtDBM(dbm), actual, nil
			}
		}
	}
	actual := readIfaceTxPower(iface)
	return fmtDBM(dbm), actual, nil
}

func wifiChannelToFreq(iface string, ch int) int {
	if iface == "wlan0" || iface == "" {
		if ch >= 1 && ch <= 13 {
			return 2407 + ch*5
		}
	}
	if ch == 14 {
		return 2484
	}
	if ch >= 32 && ch <= 177 {
		return 5000 + ch*5
	}
	return 0
}

// --- Voice ---

func getVoiceStatus() VoiceStatus {
	vs := VoiceStatus{}
	err := exec.Command("systemctl", "is-active", "--quiet", "mesh-voice").Run()
	vs.Active = err == nil
	if !vs.Active {
		return vs
	}
	if out, err := runCmdStdout(3*time.Second, "systemctl", "show", "mesh-voice",
		"--property=ExecMainStartTimestamp,ExecStart"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "ExecStart=") {
				args := line
				if i := strings.Index(args, "-addr"); i >= 0 {
					if m := regexp.MustCompile(`-addr\s+(\S+)`).FindStringSubmatch(args[i:]); len(m) > 1 {
						vs.McastAddr = m[1]
					}
				}
				if i := strings.Index(args, "-port"); i >= 0 {
					if m := regexp.MustCompile(`-port\s+(\S+)`).FindStringSubmatch(args[i:]); len(m) > 1 {
						vs.Port = m[1]
					}
				}
				if i := strings.Index(args, "-ptt"); i >= 0 {
					if m := regexp.MustCompile(`-ptt\s+(\S+)`).FindStringSubmatch(args[i:]); len(m) > 1 {
						vs.PTTMode = m[1]
					}
				}
				if i := strings.Index(args, "-iface"); i >= 0 {
					if m := regexp.MustCompile(`-iface\s+(\S+)`).FindStringSubmatch(args[i:]); len(m) > 1 {
						vs.Interface = m[1]
					}
				}
			}
		}
	}
	if out, err := runCmdStdout(3*time.Second, "systemctl", "status", "mesh-voice"); err == nil {
		if m := regexp.MustCompile(`Active:.*;\s+(.+?)\s+ago`).FindStringSubmatch(out); len(m) > 1 {
			vs.Uptime = m[1]
		}
	}
	if data, err := os.ReadFile("/run/mesh-voice-ptt.json"); err == nil {
		var ps struct {
			Active    bool   `json:"ptt_active"`
			Connected bool   `json:"ptt_connected"`
			Device    string `json:"ptt_device"`
			TX        bool   `json:"tx"`
			RX        bool   `json:"rx"`
		}
		if json.Unmarshal(data, &ps) == nil {
			vs.PTTActive = ps.Active
			vs.PTTConnected = ps.Connected
			vs.PTTDevice = ps.Device
			vs.TX = ps.TX
			vs.RX = ps.RX
		}
	} else if vs.PTTMode == "always" {
		vs.PTTActive = true
		vs.PTTDevice = "always"
	}

	conf := loadKVFile(MeshConfFile)
	vs.MicVolume = confGet(conf, "voice_mic_volume", "80")
	vs.SpeakerVolume = confGet(conf, "voice_speaker_volume", "80")
	vs.Channel = int(voiceTxCh.Load())
	vs.RxChannels = voiceGetRxChannels()
	return vs
}

// --- Services ---

type serviceEntry struct {
	ID          string
	Name        string
	Units       []string
	Category    string
	Actions     []string
	Description string
}

var serviceRegistry = []serviceEntry{
	{"manet-ctrl", "MANET Controller", []string{"manet-ctrl"}, "core", []string{"restart"}, "Web UI, API, and terminal"},
	{"alfred", "Alfred", []string{"alfred"}, "core", []string{"start", "stop", "restart"}, "Mesh data distribution daemon"},
	{"mesh-registry", "Mesh Registry", []string{"mesh-registry"}, "core", []string{"start", "stop", "restart"}, "Node discovery and registry sync"},
	{"wpa-supplicant", "WPA Supplicant", []string{"wpa_supplicant-s1g-wlan2", "wpa_supplicant@wlan0", "wpa_supplicant@wlan1"}, "network", []string{"start", "stop", "restart"}, "Mesh WiFi authentication"},
	{"hostapd", "hostapd", []string{"hostapd"}, "network", []string{"start", "stop", "restart"}, "Access point daemon"},
	{"dnsmasq", "dnsmasq", []string{"dnsmasq"}, "network", []string{"start", "stop", "restart", "reload"}, "DHCP and DNS for EUDs"},
	{"avahi", "Avahi", []string{"avahi-daemon"}, "network", []string{"start", "stop", "restart", "reload"}, "mDNS / service discovery"},
	{"mesh-voice", "Mesh Voice", []string{"mesh-voice"}, "application", []string{"start", "stop", "restart"}, "PTT voice over mesh"},
	{"chronyd", "Chrony NTP", []string{"chronyd", "chrony"}, "system", []string{"start", "stop", "restart"}, "Network time synchronisation"},
	{"gps-reader", "GPS Reader", []string{"gps-reader"}, "system", []string{"start", "stop", "restart"}, "GPS position tracking"},
	{"gateway-manager", "Gateway Manager", []string{"gateway-manager"}, "network", []string{"restart", "reload"}, "Gateway routing, NAT, and bandwidth control"},
	{"sae-watchdog", "SAE Watchdog", []string{"sae-watchdog"}, "network", []string{"start", "stop", "restart"}, "Mesh auth health monitor"},
	{"battery-reader", "Battery Reader", []string{"battery-reader"}, "system", []string{"start", "stop", "restart"}, "Battery level monitor"},
	{"cot-emitter", "CoT Emitter", []string{"cot-emitter"}, "system", []string{"start", "stop", "restart"}, "Cursor-on-Target position broadcast"},
	{"manet-txpower", "TX Power Manager", []string{"manet-txpower"}, "radio", []string{"start", "stop", "restart"}, "Transmit power management"},
	{"mesh-boot-lobby", "Boot Lobby", []string{"mesh-boot-lobby"}, "core", []string{"start", "stop", "restart"}, "Mesh boot coordination"},
}

func unitStatus(unit string) map[string]string {
	fields := make(map[string]string)
	out, err := runCmdStdout(5*time.Second, "systemctl", "show", unit,
		"--property=ActiveState,SubState,MainPID,LoadState,ActiveEnterTimestamp,Description,UnitFileState")
	if err != nil {
		return fields
	}
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.IndexByte(line, '='); idx > 0 {
			fields[line[:idx]] = line[idx+1:]
		}
	}
	return fields
}

func findActiveUnit(units []string) (string, map[string]string) {
	for _, u := range units {
		if strings.Contains(u, "*") {
			continue
		}
		props := unitStatus(u)
		if props["LoadState"] != "not-found" {
			return u, props
		}
	}
	if len(units) > 0 {
		return units[0], map[string]string{}
	}
	return "", map[string]string{}
}

func getAllServices() []ServiceInfo {
	var results []ServiceInfo
	for _, svc := range serviceRegistry {
		unit, props := findActiveUnit(svc.Units)
		activeState := props["ActiveState"]
		if activeState == "" {
			activeState = "unknown"
		}
		status := activeState
		switch activeState {
		case "active":
			status = "running"
		case "inactive":
			status = "stopped"
		case "failed":
			status = "failed"
		}
		installed := props["LoadState"] != "not-found" && props["LoadState"] != ""
		enabled := props["UnitFileState"] == "enabled" || props["UnitFileState"] == "enabled-runtime"
		var pid *int
		if p, _ := strconv.Atoi(props["MainPID"]); p > 0 {
			pid = &p
		}
		results = append(results, ServiceInfo{
			ID: svc.ID, Name: svc.Name, Description: svc.Description,
			Category: svc.Category, Unit: unit, Status: status,
			SubState: props["SubState"], Enabled: enabled, Installed: installed,
			PID: pid, StartedAt: props["ActiveEnterTimestamp"], Actions: svc.Actions,
		})
	}
	return results
}

func serviceAction(serviceID, action string) (bool, string) {
	for _, svc := range serviceRegistry {
		if svc.ID != serviceID {
			continue
		}
		allowed := false
		for _, a := range svc.Actions {
			if a == action {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, fmt.Sprintf("action %s not allowed for %s", action, serviceID)
		}
		unit, _ := findActiveUnit(svc.Units)
		if unit == "" {
			return false, "no unit found"
		}
		out, err := runCmd(10*time.Second, "systemctl", action, unit)
		if err != nil {
			return false, strings.TrimSpace(out)
		}
		return true, ""
	}
	return false, "unknown service"
}

// --- GPS ---

func getGPS(regNode RegistryNode) GPS {
	// connected lives outside the fix-availability branches below: gpsd can
	// be up and reporting (connected) with no fix yet, and that state must
	// survive into the registry-fallback return too, not just the have-fix one.
	connected := false
	data, err := os.ReadFile(GPSStatusFile)
	if err == nil {
		var g struct {
			HasFix    bool    `json:"has_fix"`
			Lat       float64 `json:"latitude"`
			Lon       float64 `json:"longitude"`
			Alt       float64 `json:"altitude"`
			Timestamp int64   `json:"timestamp"`
		}
		if json.Unmarshal(data, &g) == nil {
			if g.Timestamp > 0 && time.Now().Unix()-g.Timestamp < 30 {
				connected = true
			}
			if g.HasFix {
				return GPS{
					Available: true,
					Connected: connected,
					Lat:       fmt.Sprintf("%f", g.Lat),
					Lon:       fmt.Sprintf("%f", g.Lon),
					Alt:       fmt.Sprintf("%f", g.Alt),
				}
			}
		}
	}
	gps := registryGPS(regNode)
	gps.Connected = connected
	return gps
}

// registryGPS reads a node's position from its gossiped registry entry only,
// never the local gpsd status file. getGPS() is only correct for the node
// manet-ctrl is running on — its local-file branch would otherwise report
// this node's own fix as if it belonged to whichever peer regNode is for.
func registryGPS(regNode RegistryNode) GPS {
	gps := GPS{}
	if lat := regNode["GPS_LATITUDE"]; lat != "" && lat != "0" {
		gps.Available = true
		gps.Lat = lat
		gps.Lon = regNode["GPS_LONGITUDE"]
		gps.Alt = regNode["GPS_ALTITUDE"]
	}
	return gps
}
