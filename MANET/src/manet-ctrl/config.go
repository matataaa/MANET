package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	RegistryFile      = "/var/run/mesh_node_registry"
	MeshStateFile     = "/etc/mesh_ipv4_state"
	GPSStatusFile     = "/run/gps_status.json"
	BatteryFile       = "/run/battery_status.json"
	NoMeshIfFile      = "/var/lib/no_mesh_if"
	APInterfaceFile   = "/var/lib/ap_interface"
	UpdateStatusFile  = "/var/run/manet_update_status.json"
	UpdateTriggerFile = "/run/manet-update-trigger"
	// FleetUpdateAckFile records the last slot-71 triggered_at this node has
	// already acted on. Moved off tmpfs (was /var/run/fleet_update_ack_ts) now
	// that slot 71 is authenticated -- a reboot must not lose this record,
	// or a still-gossiping (now-authenticated, so no longer discardable as
	// obviously bogus) old trigger could replay a fleet-wide update.
	FleetUpdateAckFile = "/var/lib/manet_fleet_update_ack_ts"
	// AppliedConfigFile persistently records the pkg_id of every fleet config
	// package this node has actually applied (see fleetCheckActivation /
	// recordPkgIDApplied), independent of AckVersionFile (which only tracks
	// the last version number this node ACKed, lives on tmpfs, and is not
	// itself a security control). Deliberately NOT AckVersionFile's path --
	// mesh-registry/main.go reads that literal path and it is not part of
	// this replay-protection design.
	AppliedConfigFile = "/var/lib/manet_config_applied"
	RefreshMS         = 15000
	PerfAuthCookie    = "manet_perf_auth"
	PerfAuthMaxAge    = 15552000
	// FleetPeerAuthHeader carries a mintFleetPeerToken() value on a
	// server-to-server proxy hop (e.g. handleTerminalProxy -> target's
	// /ws/terminal, handleLogsProxy -> target's /ws/logs). It is checked as
	// an alternative to a session cookie, scoped only to the route it's
	// wrapped on -- see requireAuthOrPeerToken in api.go. The header name is
	// shared across routes, but each route mints/verifies against its own
	// domain string (see FleetPeerAuthDomainTerminal / FleetPeerAuthDomainLogs
	// below), so a token captured for one route is not usable against
	// another even though both derive from the same underlying fleet key.
	// The token itself is a short-lived, target-bound HMAC (see
	// mintFleetPeerToken/verifyFleetPeerToken below), not a static secret,
	// so intercepting one grants at most fleetPeerTokenMaxSkew of replay
	// against the one target and one route it was minted for.
	FleetPeerAuthHeader = "X-Manet-Fleet-Peer-Auth"

	// FleetPeerAuthDomainTerminal / FleetPeerAuthDomainLogs are the
	// domain-separation strings passed to mintFleetPeerToken /
	// verifyFleetPeerToken for each proxy route. Deliberately distinct
	// values (not just distinct call sites) -- the domain string is part of
	// the signed message (see fleetPeerTokenMAC), so a token minted for one
	// route's domain fails verification against another route's domain even
	// if replayed at the same target host within the same freshness window.
	FleetPeerAuthDomainTerminal = "fleet-peer-terminal|v1"
	FleetPeerAuthDomainLogs     = "fleet-peer-logs|v1"
)

var (
	HalowEUChannels      = []int{863500, 864500, 865500, 866500, 867500}
	HalowUIToS1GChannel  = map[int]int{1: 1, 2: 3, 3: 5, 4: 7, 5: 9}
	HalowBWTxPowerCapDBM = map[string]string{"1MHz": "24", "2MHz": "24", "4MHz": "22", "8MHz": "20"}
)

// MeshConfFile, PendingConfFile, AckVersionFile, and FleetPrefsFile are vars
// (not consts) purely so tests can point them at a throwaway temp file
// instead of the real /etc or /var/run path — production code never
// reassigns them, and their default values are unchanged.
// mesh-registry/main.go reads AckVersionFile's literal path from outside
// this package; that string value is untouched.
var (
	MeshConfFile    = "/etc/mesh.conf"
	PendingConfFile = "/var/run/mesh_pending_config.json"
	AckVersionFile  = "/var/run/mesh_config_ack_version"
	FleetPrefsFile  = "/var/run/fleet_preferences.json"
)

// --- JSON types matching frontend expectations ---

type Node struct {
	ID           string                 `json:"id"`
	Hostname     string                 `json:"hostname"`
	MAC          string                 `json:"mac"`
	IP           string                 `json:"ip"`
	TQ           *int                   `json:"tq"`
	IsMe         bool                   `json:"is_me"`
	IsDirect     bool                   `json:"is_direct"`
	IsGateway    bool                   `json:"is_gateway"`
	IsSelectedGW bool                   `json:"is_selected_gw"`
	GPS          GPS                    `json:"gps"`
	Uptime       string                 `json:"uptime"`
	CPU          string                 `json:"cpu"`
	Battery      *BatteryInfo           `json:"battery"`
	NTP          bool                   `json:"ntp"`
	State        string                 `json:"state"`
	Ch2G         string                 `json:"ch_2g"`
	Ch5G         string                 `json:"ch_5g"`
	Limp         bool                   `json:"limp"`
	AllMACs      []string               `json:"all_macs"`
	BestLink     map[string]interface{} `json:"best_link"`
	HopCount     *int                   `json:"hop_count"`
	LastSeen     string                 `json:"last_seen"`
	Applets      []AppletBrief          `json:"applets,omitempty"`
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
	WidthMHz      string   `json:"width_mhz,omitempty"`
	TxPowerDBM    string   `json:"txpower_dbm"`
	TxPowerCapDBM string   `json:"txpower_cap_dbm"`
	TxPowerOpts   []string `json:"txpower_options_dbm"`
	HalowBW       string   `json:"halow_bw"`
	HalowSource   string   `json:"halow_source"`
	TxMCS         string   `json:"tx_mcs,omitempty"`
	RxMCS         string   `json:"rx_mcs,omitempty"`
	Driver        string   `json:"driver,omitempty"`
	TempC         *float64 `json:"temp_c,omitempty"`
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
	Source    string `json:"source,omitempty"`
	Lat       string `json:"lat"`
	Lon       string `json:"lon"`
	Alt       string `json:"alt"`
}

type StatusData struct {
	Nodes        []Node        `json:"nodes"`
	MyMAC        string        `json:"my_mac"`
	MyHostname   string        `json:"my_hostname"`
	MyIP         string        `json:"my_ip"`
	MeshSSID     string        `json:"mesh_ssid"`
	Network      string        `json:"network"`
	GatewayCount int           `json:"gateway_count"`
	SelectedGW   string        `json:"selected_gw"`
	Neighbors    []BatNeighbor `json:"neighbors"`
	Edges        []Edge        `json:"edges"`
	Timestamp    int64         `json:"timestamp"`
}

type ThrottleInfo struct {
	Raw           string `json:"raw"`
	Undervoltage  bool   `json:"undervoltage"`
	FreqCapped    bool   `json:"freq_capped"`
	Throttled     bool   `json:"throttled"`
	SoftTempLimit bool   `json:"soft_temp_limit"`
	WasUndervolt  bool   `json:"was_undervoltage"`
	WasFreqCapped bool   `json:"was_freq_capped"`
	WasThrottled  bool   `json:"was_throttled"`
	WasSoftTemp   bool   `json:"was_soft_temp_limit"`
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
	Hostname   string          `json:"hostname"`
	IP         string          `json:"ip"`
	MAC        string          `json:"mac"`
	Uptime     string          `json:"uptime"`
	Battery    *BatteryInfo    `json:"battery"`
	GPS        GPS             `json:"gps"`
	Interfaces []Iface         `json:"interfaces"`
	EUDs       []EUD           `json:"euds"`
	Services   map[string]bool `json:"services"`
	EUDMode    string          `json:"eud_mode"`
	APSSID     string          `json:"ap_ssid"`
	MeshSSID   string          `json:"mesh_ssid"`
	Throttle   *ThrottleInfo   `json:"throttle,omitempty"`
	Network    *NetworkState   `json:"network,omitempty"`
	System     *SystemStats    `json:"system,omitempty"`
	Airtime    *AirtimeInfo    `json:"airtime,omitempty"`
	UplinkMbps float64         `json:"uplink_mbps"`
	UplinkType string          `json:"uplink_type"`
	CoreDown   []string        `json:"core_down,omitempty"`
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
	RawTP    float64 `json:"-"`
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
	AckStatus     *AckStatusResult  `json:"ack_status,omitempty"`
}

// AckStatusResult is the one shared acked/total/missing computation used by
// both assembleAdminStatus (what the UI displays) and apiAdminActivate's
// non-force gate (what the server actually enforces) — see ackStatus in
// admin.go. Before this, the two used different logic and could disagree.
// DangerousKeys/OfflineNodes are populated together: DangerousKeys lists
// which of admin_password/mesh_ssid/mesh_key/ipv4_network this pending push
// actually changes, and OfflineNodes lists which registry nodes are not
// ACTIVE right now (both computed regardless of ack count) — a non-force
// Activate must be blocked whenever both are non-empty, since applying one
// of those changes while a node is offline risks silently orphaning it. See
// dangerousKeyChanges/offlineNodeNames in admin.go and apiAdminActivate's
// gate in api.go, which must both use this same pair.
type AckStatusResult struct {
	Version       string   `json:"version"`
	Acked         int      `json:"acked"`
	Total         int      `json:"total"`
	Missing       []string `json:"missing,omitempty"`
	DangerousKeys []string `json:"dangerous_keys,omitempty"`
	OfflineNodes  []string `json:"offline_nodes,omitempty"`
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

// fleetPeerTokenMaxSkew bounds how old (or how far in the future, to allow
// for some clock drift between nodes) a fleet peer token's timestamp may be
// before verifyFleetPeerToken rejects it. Mesh nodes run periodic time sync
// (see MEMORY: "Upstream sync 2026-08-21 / PR #12" -- mesh time sync), so
// 30s is generous relative to expected drift; if a fleet is ever seen with
// worse clock skew than that, the fix is to fix the time sync, not to widen
// this window (a wider window only weakens the replay protection this is
// here for).
const fleetPeerTokenMaxSkew = 30 * time.Second

// fleetPeerTokenKey returns the shared secret behind fleet peer tokens, or
// nil if this node is not eligible to mint/verify them. Gating on
// require_auth here (mirroring isAuthed's own check) is deliberate and
// load-bearing: a node provisioned with a real admin_password but
// require_auth=n (firstrun.sh.template's default) would otherwise mint a
// fully valid token for an unauthenticated caller, turning that one open
// node into a relay that can reach every require_auth=y node in the fleet
// with no password ever entered -- and, since the target of a proxy dial is
// caller-controlled, hand the token itself to an arbitrary third party.
//
// The key is derived from deriveFleetKey (fleetcrypto.go's already
// PBKDF2-hardened, 200000-iteration fleet-crypto key), via one more HMAC
// with its own domain string -- deliberately NOT admin_password|mesh_ssid
// directly. This fleet's peer TLS defaults to InsecureSkipVerify=true
// (main.go), so an on-path mesh member (below admin level -- exactly the
// threat model #36 was built for) can capture a token in transit; deriving
// the peer-token key straight from the password would hand that attacker a
// single unsalted HMAC key to brute-force admin_password at GPU speed,
// undoing #36's PBKDF2 hardening entirely. Going through deriveFleetKey
// means the same 200000-round cost applies here too, and since
// deriveFleetKey caches its result, this costs nothing extra on the hot
// path after the first call. admin_password and mesh_ssid are provisioned
// identically across the fleet (unlike getPerfAuthToken's
// machineTokenSalt, which is deliberately per-machine and therefore can't
// be precomputed cross-node), so every node that IS eligible derives the
// same key independently.
func fleetPeerTokenKey() []byte {
	conf := loadKVFile(MeshConfFile)
	ra := strings.ToLower(conf["require_auth"])
	if ra != "y" && ra != "yes" && ra != "1" {
		return nil
	}
	pw := getProvisionedPassword(conf)
	if pw == "" {
		return nil
	}
	fk, err := deriveFleetKey(pw, conf["mesh_ssid"])
	if err != nil {
		return nil
	}
	m := hmac.New(sha256.New, fk)
	m.Write([]byte("manet-fleet-peer-terminal-key|v1"))
	return m.Sum(nil)
}

// fleetPeerTokenMAC computes the HMAC over a domain+timestamp+target-bound
// message. Binding the target host into the signed message means a token
// minted for one target cannot be replayed against a different one; binding
// the domain string means a token minted for one route (e.g. the terminal
// proxy) cannot be replayed against a different route (e.g. the logs proxy)
// even against the same target and within the same freshness window --
// callers must pass a distinct domain per route (see
// FleetPeerAuthDomainTerminal / FleetPeerAuthDomainLogs in config.go's
// consts).
func fleetPeerTokenMAC(key []byte, ts int64, targetHost, domain string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(fmt.Sprintf("%s|%d|%s", domain, ts, targetHost)))
	return mac.Sum(nil)
}

// mintFleetPeerTokenAt builds a "<unix-ts>|<hex-hmac>" token for targetHost
// under the given domain, at the given timestamp, or "" if this node isn't
// eligible to mint one (see fleetPeerTokenKey). Split out from
// mintFleetPeerToken so tests can mint an already-expired, but otherwise
// validly-signed, token to exercise verifyFleetPeerToken's expiry check for
// real.
func mintFleetPeerTokenAt(ts int64, targetHost, domain string) string {
	key := fleetPeerTokenKey()
	if len(key) == 0 {
		return ""
	}
	mac := fleetPeerTokenMAC(key, ts, targetHost, domain)
	return fmt.Sprintf("%d|%x", ts, mac)
}

// mintFleetPeerToken mints a fleet peer token for targetHost under domain,
// timestamped now. Used only to authenticate a server-to-server proxy hop
// (e.g. handleTerminalProxy, handleLogsProxy) -- not a general auth token.
// domain must match the one the receiving end verifies against (see
// requireAuthOrPeerToken).
func mintFleetPeerToken(targetHost, domain string) string {
	return mintFleetPeerTokenAt(time.Now().Unix(), targetHost, domain)
}

// verifyFleetPeerToken checks a token received on the receiving end of a
// proxy hop (requireAuthOrPeerToken) against targetHost -- which must be the
// *receiving* node's own address as the client addressed it (r.Host), since
// that's what the token was bound to at mint time -- and against domain,
// which must match the route's own domain string (a token minted for a
// different route's domain will not verify here even for the same
// targetHost). Note: this function only checks the signature and freshness
// of the token against the CLAIMED targetHost -- it does not itself verify
// that targetHost is actually this node's address. That check is
// hostMatchesLocalAddr, which the caller (requireAuthOrPeerToken) must run
// first: the Host header is sender-controlled, so without that separate
// check, "binding" to it would be a comment claiming protection that
// doesn't exist.
func verifyFleetPeerToken(token, targetHost, domain string) bool {
	key := fleetPeerTokenKey()
	if len(key) == 0 || token == "" {
		return false
	}
	tsStr, macHex, ok := strings.Cut(token, "|")
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	maxSkew := int64(fleetPeerTokenMaxSkew / time.Second)
	if ts < now-maxSkew || ts > now+maxSkew {
		return false
	}
	got, err := hex.DecodeString(macHex)
	if err != nil {
		return false
	}
	want := fleetPeerTokenMAC(key, ts, targetHost, domain)
	return hmac.Equal(got, want)
}

// hostMatchesLocalAddr reports whether hostport (typically a request's
// r.Host, e.g. "10.30.2.181", "10.30.2.181:8443", or "[fe80::1]:8443")
// names one of this node's own network addresses, per
// net.InterfaceAddrs(). This is what actually enforces the fleet peer
// token's target binding -- the sender's Host header is otherwise
// self-reported and unverified.
func hostMatchesLocalAddr(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // strip IPv6 zone, if present
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || !ipNet.IP.Equal(ip) {
			continue
		}
		return true
	}
	return false
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
