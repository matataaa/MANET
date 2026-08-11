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

var origRE = regexp.MustCompile(`[\s*]+([0-9a-f:]{17})\s+[\d.]+(?:ms|s)\s+\(\s*([\d.]+)\)\s+([0-9a-f:]{17})(?:\s+\[\s*(\S+)\s*\])?`)
var neighRE = regexp.MustCompile(`([0-9a-f:]{17})\s+[\d.]+(?:ms|s)\s+\(\s*([\d.]+)\)\s+\[\s*(\S+)\s*\]`)
var macRE = regexp.MustCompile(`([0-9a-f]{2}(?::[0-9a-f]{2}){5})`)

func normTQ(raw float64) int {
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
		orig := normMAC(m[1])
		raw, _ := strconv.ParseFloat(m[2], 64)
		tq := normTQ(raw)
		nexthop := normMAC(m[3])
		iface := ""
		if len(m) > 4 {
			iface = m[4]
		}
		if prev, ok := origMap[orig]; !ok || tq > prev.TQ {
			origMap[orig] = BatOriginator{TQ: tq, Nexthop: nexthop, Iface: iface}
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
		raw, _ := strconv.ParseFloat(m[2], 64)
		neighbors = append(neighbors, BatNeighbor{
			Iface: m[3], MAC: normMAC(m[1]), TQ: normTQ(raw),
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
		"mumble":    {"mumble-server", "murmur", "mumble"},
		"mediamtx":  {"mediamtx", "rtsp-server"},
		"ntp":       {"chrony", "chronyd", "ntp", "ntpd"},
		"syncthing": {"syncthing"},
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

	// no_mesh_if
	noMeshIfaces := make(map[string]bool)
	if data, err := os.ReadFile(NoMeshIfFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			name := strings.TrimSpace(line)
			if name != "" {
				noMeshIfaces[name] = true
			}
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
			if state == "DOWN" {
				iface.Health = "fault"
				iface.Faults = append(iface.Faults, "Interface is DOWN")
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
		if m := regexp.MustCompile(`s1g_prim_chwidth=(\d+)`).FindStringSubmatch(text); len(m) > 1 {
			switch m[1] {
			case "0":
				info["halow_bw"] = "1MHz"
			case "1":
				info["halow_bw"] = "2MHz"
			case "2":
				info["halow_bw"] = "4MHz"
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
		chKeys := []string{"channel_index", "channel", "primary_channel", "s1g_channel"}

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
		vs.PTTConnected = true
		vs.PTTDevice = "always"
	}
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
	{"mumble", "Mumble Server", []string{"mumble-server", "murmur"}, "application", []string{"start", "stop", "restart"}, "Voice comms server"},
	{"mediamtx", "MediaMTX", []string{"mediamtx"}, "application", []string{"start", "stop", "restart"}, "RTSP/WebRTC media server"},
	{"chronyd", "Chrony NTP", []string{"chronyd", "chrony"}, "system", []string{"start", "stop", "restart"}, "Network time synchronisation"},
	{"syncthing", "Syncthing", []string{"syncthing"}, "application", []string{"start", "stop", "restart"}, "File synchronisation"},
	{"gps-reader", "GPS Reader", []string{"gps-reader"}, "system", []string{"start", "stop", "restart"}, "GPS position tracking"},
	{"gateway-route", "Gateway Route Manager", []string{"gateway-route-manager"}, "network", []string{"restart"}, "Mesh gateway routing"},
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
	gps := GPS{}
	data, err := os.ReadFile(GPSStatusFile)
	if err == nil {
		var g struct {
			HasFix bool    `json:"has_fix"`
			Lat    float64 `json:"latitude"`
			Lon    float64 `json:"longitude"`
			Alt    float64 `json:"altitude"`
		}
		if json.Unmarshal(data, &g) == nil && g.HasFix {
			gps.Available = true
			gps.Lat = fmt.Sprintf("%f", g.Lat)
			gps.Lon = fmt.Sprintf("%f", g.Lon)
			gps.Alt = fmt.Sprintf("%f", g.Alt)
			return gps
		}
	}
	if lat := regNode["GPS_LATITUDE"]; lat != "" && lat != "0" {
		gps.Available = true
		gps.Lat = lat
		gps.Lon = regNode["GPS_LONGITUDE"]
		gps.Alt = regNode["GPS_ALTITUDE"]
	}
	return gps
}
