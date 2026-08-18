package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	RegistryFile     = "/var/run/mesh_node_registry"
	MeshConfFile     = "/etc/mesh.conf"
	MeshStateFile    = "/etc/mesh_ipv4_state"
	PendingConfFile  = "/var/run/mesh_pending_config.json"
	GPSStatusFile    = "/run/gps_status.json"
	BatteryFile      = "/run/battery_status.json"
	AckVersionFile   = "/var/run/mesh_config_ack_version"
	FleetPrefsFile   = "/var/run/fleet_preferences.json"
	NoMeshIfFile     = "/var/lib/no_mesh_if"
	APInterfaceFile  = "/var/lib/ap_interface"
	RefreshMS        = 15000
	PerfAuthCookie   = "manet_perf_auth"
	PerfAuthMaxAge   = 15552000
)

var (
	HalowEUChannels      = []int{863500, 864500, 865500, 866500, 867500}
	HalowUIToS1GChannel  = map[int]int{1: 1, 2: 3, 3: 5, 4: 7, 5: 9}
	HalowBWTxPowerCapDBM = map[string]string{"1MHz": "24", "2MHz": "24", "4MHz": "22", "8MHz": "20"}
)

// --- JSON types matching frontend expectations ---

type Node struct {
	ID           string      `json:"id"`
	Hostname     string      `json:"hostname"`
	MAC          string      `json:"mac"`
	IP           string      `json:"ip"`
	TQ           *int        `json:"tq"`
	IsMe         bool        `json:"is_me"`
	IsDirect     bool        `json:"is_direct"`
	IsGateway    bool        `json:"is_gateway"`
	IsSelectedGW bool        `json:"is_selected_gw"`
	Uptime       string      `json:"uptime"`
	CPU          string      `json:"cpu"`
	Battery      *BatteryInfo `json:"battery"`
	NTP          bool        `json:"ntp"`
	State        string      `json:"state"`
	Ch2G         string      `json:"ch_2g"`
	Ch5G         string      `json:"ch_5g"`
	Limp         bool        `json:"limp"`
	AllMACs      []string    `json:"all_macs"`
	BestLink     map[string]interface{} `json:"best_link"`
	HopCount     *int        `json:"hop_count"`
	LastSeen     string      `json:"last_seen"`
	Applets      []AppletBrief `json:"applets,omitempty"`
}

type AppletBrief struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type Edge struct {
	Source     string   `json:"source"`
	Target     string   `json:"target"`
	Type       string   `json:"type"`
	Via        string   `json:"via,omitempty"`
	TQ         *int     `json:"tq"`
	Throughput *float64 `json:"throughput,omitempty"`
	GWRoute    bool     `json:"gw_route,omitempty"`
	Iface      string   `json:"iface,omitempty"`
}

type BatteryInfo struct {
	Percentage *int     `json:"percentage"`
	Status     string   `json:"status,omitempty"`
	VoltageV   *float64 `json:"voltage_v"`
	CurrentMA  *float64 `json:"current_ma"`
	PowerW     *float64 `json:"power_w"`
	Charging   *bool    `json:"charging"`
	Timestamp  *string  `json:"timestamp"`
}

type Iface struct {
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	Health        string   `json:"health"`
	Detail        string   `json:"detail"`
	Faults        []string `json:"faults"`
	Addrs         []string `json:"addrs"`
	State         string   `json:"state"`
	Channel       string   `json:"channel"`
	FreqMHz       string   `json:"freq_mhz"`
	TxPowerDBM    string   `json:"txpower_dbm"`
	TxPowerCapDBM string   `json:"txpower_cap_dbm"`
	TxPowerOpts   []string `json:"txpower_options_dbm"`
	HalowBW       string   `json:"halow_bw"`
	HalowSource   string   `json:"halow_source"`
	TxMCS         string   `json:"tx_mcs,omitempty"`
	RxMCS         string   `json:"rx_mcs,omitempty"`
	Driver        string   `json:"driver,omitempty"`
}

type EUD struct {
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	Hostname  string `json:"hostname"`
	ExpiresIn *int   `json:"expires_in"`
}

type GPS struct {
	Available bool   `json:"available"`
	Connected bool   `json:"connected"`
	Lat       string `json:"lat"`
	Lon       string `json:"lon"`
	Alt       string `json:"alt"`
}

type StatusData struct {
	Nodes        []Node            `json:"nodes"`
	MyMAC        string            `json:"my_mac"`
	MyHostname   string            `json:"my_hostname"`
	MyIP         string            `json:"my_ip"`
	MeshSSID     string            `json:"mesh_ssid"`
	Network      string            `json:"network"`
	GatewayCount int               `json:"gateway_count"`
	SelectedGW   string            `json:"selected_gw"`
	Neighbors    []BatNeighbor     `json:"neighbors"`
	Edges        []Edge            `json:"edges"`
	Timestamp    int64             `json:"timestamp"`
}

type ThrottleInfo struct {
	Raw            string `json:"raw"`
	Undervoltage   bool   `json:"undervoltage"`
	FreqCapped     bool   `json:"freq_capped"`
	Throttled      bool   `json:"throttled"`
	SoftTempLimit  bool   `json:"soft_temp_limit"`
	WasUndervolt   bool   `json:"was_undervoltage"`
	WasFreqCapped  bool   `json:"was_freq_capped"`
	WasThrottled   bool   `json:"was_throttled"`
	WasSoftTemp    bool   `json:"was_soft_temp_limit"`
}

type NetworkState struct {
	Gateway       bool   `json:"gateway"`
	GatewayIP     string `json:"gateway_ip,omitempty"`
	DefaultGW     string `json:"default_gw,omitempty"`
	UpstreamIface string `json:"upstream_iface,omitempty"`
	EUDMode       string `json:"eud_mode"`
	EUDActive     bool   `json:"eud_active"`
	EUDs          []EUD  `json:"euds"`
	EUDIface      string `json:"eud_iface,omitempty"`
	APActive      bool   `json:"ap_active"`
	USBTether     bool   `json:"usb_tether"`
	USBIface      string `json:"usb_iface,omitempty"`
	NTP           bool   `json:"ntp"`
}

type SystemStats struct {
	CPUTemp  *float64   `json:"cpu_temp,omitempty"`
	LoadAvg  [3]float64 `json:"load_avg"`
	MemTotal int64      `json:"mem_total_kb"`
	MemFree  int64      `json:"mem_free_kb"`
	MemAvail int64      `json:"mem_avail_kb"`
}

type LocalData struct {
	Hostname   string            `json:"hostname"`
	IP         string            `json:"ip"`
	MAC        string            `json:"mac"`
	Uptime     string            `json:"uptime"`
	Battery    *BatteryInfo      `json:"battery"`
	GPS        GPS               `json:"gps"`
	Interfaces []Iface           `json:"interfaces"`
	EUDs       []EUD             `json:"euds"`
	Services   map[string]bool   `json:"services"`
	EUDMode    string            `json:"eud_mode"`
	APSSID     string            `json:"ap_ssid"`
	MeshSSID   string            `json:"mesh_ssid"`
	Throttle   *ThrottleInfo     `json:"throttle,omitempty"`
	Network    *NetworkState     `json:"network,omitempty"`
	System     *SystemStats      `json:"system,omitempty"`
	Airtime    *AirtimeInfo      `json:"airtime,omitempty"`
}

type BatOriginator struct {
	TQ       int
	RawTP    float64
	Nexthop  string
	Iface    string
	LastSeen float64
	Selected bool
}

type BatNeighbor struct {
	Iface    string  `json:"iface"`
	MAC      string  `json:"mac"`
	TQ       int     `json:"tq"`
	LastSeen float64 `json:"-"`
}

type BatGateway struct {
	MAC      string `json:"mac"`
	TQ       int    `json:"tq"`
	Selected bool   `json:"selected"`
}

type ServiceInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Unit        string   `json:"unit"`
	Status      string   `json:"status"`
	SubState    string   `json:"sub_state"`
	Enabled     bool     `json:"enabled"`
	Installed   bool     `json:"installed"`
	PID         *int     `json:"pid"`
	StartedAt   string   `json:"started_at"`
	Actions     []string `json:"actions"`
}

type VoiceStatus struct {
	Active        bool   `json:"active"`
	Uptime        string `json:"uptime"`
	PTTMode       string `json:"ptt_mode"`
	McastAddr     string `json:"mcast_addr"`
	Port          string `json:"port"`
	Interface     string `json:"interface"`
	PTTActive     bool   `json:"ptt_active"`
	PTTConnected  bool   `json:"ptt_connected"`
	PTTDevice     string `json:"ptt_device"`
	TX            bool   `json:"tx"`
	RX            bool   `json:"rx"`
	MicVolume     string `json:"mic_volume"`
	SpeakerVolume string `json:"speaker_volume"`
	Channel       int    `json:"channel"`
	RxChannels    []int  `json:"rx_channels"`
}

type AdminStatus struct {
	CurrentConfig map[string]string `json:"current_config"`
	Pending       json.RawMessage   `json:"pending"`
	Nodes         []AdminNode       `json:"nodes"`
	TotalNodes    int               `json:"total_nodes"`
	ActiveNodes   int               `json:"active_nodes"`
	MyHostname    string            `json:"my_hostname"`
	Preferences   FleetPreferences  `json:"preferences"`
}

type AdminNode struct {
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Ack       string `json:"ack"`
	LastSeen  string `json:"last_seen"`
	NodeState string `json:"node_state"`
	Profile   string `json:"profile"`
}

type FleetProfile struct {
	Name   string            `json:"name"`
	Config map[string]string `json:"config"`
}

type FleetPreferences struct {
	Profiles     map[string]FleetProfile `json:"profiles"`
	NodeProfiles map[string]string       `json:"node_profiles"`
	MeshConfig   map[string]string       `json:"mesh_config"`
}

// --- Config file utilities ---

func loadKVFile(path string) map[string]string {
	m := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, "'\"")
		m[key] = val
	}
	return m
}

// saveKVFileMu serializes every saveKVFile call across all callers in this
// process. Without it, two API handlers racing to update mesh.conf at
// nearly the same time (e.g. a voice-settings save and a fleet hostname
// push) can each os.ReadFile the same pre-update content, then both
// os.WriteFile back a version missing the other's keys — or, worse, land
// their writes byte-interleaved if the timing is close enough, corrupting
// the file outright (observed live: a merged "voice_speaker_volume=80"
// + "regulatory_domain=US" line, and mesh_ssid/mesh_key dropped entirely).
// The mutex fixes the read-modify-write race; the temp-file+rename below
// fixes visibility (no reader or concurrent writer ever sees a partially
// written file, unlike the previous direct os.WriteFile which truncates
// in place).
var saveKVFileMu sync.Mutex

func saveKVFile(path string, updates map[string]string) error {
	saveKVFileMu.Lock()
	defer saveKVFileMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	written := make(map[string]bool)
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			idx := strings.IndexByte(trimmed, '=')
			if idx > 0 {
				key := strings.TrimSpace(trimmed[:idx])
				if val, ok := updates[key]; ok {
					out = append(out, fmt.Sprintf("%s=%s", key, val))
					written[key] = true
					continue
				}
			}
		}
		out = append(out, line)
	}
	for key, val := range updates {
		if !written[key] {
			out = append(out, fmt.Sprintf("%s=%s", key, val))
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func confGet(conf map[string]string, key, def string) string {
	if v, ok := conf[key]; ok && v != "" {
		return v
	}
	return def
}

// --- Registry parsing ---

var registryRE = regexp.MustCompile(`NODE_([A-Fa-f0-9]+)_([A-Z0-9_]+)='([^']*)'`)

type RegistryNode map[string]string

var (
	registryCacheMu sync.Mutex
	registryCache   = make(map[string]RegistryNode)
)

// updateRegistryCache mirrors the current registry contents. It must not
// merge into the old cache: append-only behavior kept resurrecting nodes
// that mesh-registry had already purged, showing ghost duplicates in the UI
// until manet-ctrl restarted.
func updateRegistryCache(nodes map[string]RegistryNode) {
	fresh := make(map[string]RegistryNode)
	for _, rn := range nodes {
		if rn["HOSTNAME"] == "" || rn["IPV4_ADDRESS"] == "" {
			continue
		}
		mac := normMAC(rn["MAC_ADDRESS"])
		if mac != "" {
			fresh[mac] = rn
		}
		for _, m := range strings.Split(rn["MAC_ADDRESSES"], ",") {
			m = normMAC(m)
			if m != "" {
				fresh[m] = rn
			}
		}
	}
	registryCacheMu.Lock()
	registryCache = fresh
	registryCacheMu.Unlock()
}

func getCachedRegistryNode(mac string) (RegistryNode, bool) {
	registryCacheMu.Lock()
	defer registryCacheMu.Unlock()
	rn, ok := registryCache[normMAC(mac)]
	return rn, ok
}

func parseRegistry() map[string]RegistryNode {
	nodes := make(map[string]RegistryNode)
	data, err := os.ReadFile(RegistryFile)
	if err != nil {
		return nodes
	}
	for _, m := range registryRE.FindAllStringSubmatch(string(data), -1) {
		id, field, val := m[1], m[2], m[3]
		if _, ok := nodes[id]; !ok {
			nodes[id] = RegistryNode{"id": id}
		}
		nodes[id][field] = val
	}
	updateRegistryCache(nodes)
	return nodes
}

func normMAC(mac string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(mac), "-", ":"))
}

func parseAppletsBrief(s string) []AppletBrief {
	if s == "" {
		return nil
	}
	var out []AppletBrief
	for _, entry := range strings.Split(s, ",") {
		parts := strings.SplitN(entry, "|", 3)
		if len(parts) < 2 {
			continue
		}
		ab := AppletBrief{Name: parts[0], Label: parts[1]}
		if len(parts) >= 3 {
			ab.Status = parts[2]
		}
		out = append(out, ab)
	}
	return out
}

// --- Auth helpers ---

func machineTokenSalt() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		data, err := os.ReadFile(path)
		if err == nil {
			s := strings.TrimSpace(string(data))
			if s != "" {
				return s
			}
		}
	}
	h, _ := os.Hostname()
	return h
}

func getProvisionedPassword(conf map[string]string) string {
	return conf["admin_password"]
}

func getPerfAuthToken() string {
	conf := loadKVFile(MeshConfFile)
	pw := getProvisionedPassword(conf)
	if pw == "" {
		return ""
	}
	salt := machineTokenSalt()
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|perf-local|v1|%s", pw, salt)))
	return fmt.Sprintf("%x", h)
}

// --- Network helpers ---

func isAllowedIP(clientIP string, conf map[string]string) bool {
	if clientIP == "127.0.0.1" || clientIP == "::1" {
		return true
	}
	network := confGet(conf, "ipv4_network", "10.30.2.0/24")
	_, cidr, err := net.ParseCIDR(network)
	if err != nil {
		return false
	}
	ip := net.ParseIP(clientIP)
	return ip != nil && cidr.Contains(ip)
}

// --- Sort helpers ---

var rolePriority = map[string]int{
	"bat": 0, "mesh": 1, "ap": 2, "gateway": 3,
	"eud-bridge": 4, "bridge": 5, "other": 6,
}

var healthPriority = map[string]int{
	"fault": 0, "warn": 1, "ok": 2, "info": 3,
}

func sortIfaces(ifaces []Iface) {
	sort.SliceStable(ifaces, func(i, j int) bool {
		ri := rolePriority[ifaces[i].Role]
		rj := rolePriority[ifaces[j].Role]
		if ri != rj {
			return ri < rj
		}
		hi := healthPriority[ifaces[i].Health]
		hj := healthPriority[ifaces[j].Health]
		return hi < hj
	})
}
