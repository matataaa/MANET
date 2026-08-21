package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	gpsStatusPath = "/run/gps_status.json"
	meshConfPath  = "/etc/mesh.conf"
	cotStatusPath = "/run/cot_emitter_status.json"
	pollInterval  = 10 * time.Second
	// Multicast over 802.11 is broadcast at the lowest basic rate with no
	// retries, so frames are lost routinely. At the old 30s interval, four
	// consecutive losses exceeded staleSeconds and the marker vanished from
	// ATAK. 10s gives a margin of eleven packets instead of three.
	mcastInterval = 10 * time.Second
	staleSeconds  = 120
	mcastGroup    = "239.2.3.1"
	mcastPort     = 6969
	// Default CoT identity: a category-level "friendly ground equipment"
	// type with no team affiliation — a mesh node reads as infrastructure,
	// not a teammate, unless explicitly configured otherwise via mesh.conf
	// (cot_type/cot_team/cot_role/cot_icon, see getCoTIdentity).
	defaultCoTType = "a-f-G-E"
	defaultCoTRole = "Team Member"
	controlIface   = "br0"
)

type cotStatus struct {
	Running        bool     `json:"running"`
	GPSFix         bool     `json:"gps_fix"`
	LastSentUTC    string   `json:"last_sent_utc,omitempty"`
	UnicastCount   int      `json:"unicast_targets"`
	EUDIPs         []string `json:"eud_ips"`
	EUDInterfaces  []string `json:"eud_interfaces"`
	McastEnabled   bool     `json:"mcast_enabled"`
	TotalSent      int64    `json:"total_sent"`
	RelayReceived  int64    `json:"relay_received"`
	RelayForwarded int64    `json:"relay_forwarded"`
	RelayError     string   `json:"relay_error,omitempty"`
	LastError      string   `json:"last_error,omitempty"`
	Timestamp      int64    `json:"timestamp"`
}

// Relay counters — written by the relay goroutine, read by the emit loop when
// it writes the status file, so only one goroutine ever touches the file.
var (
	relayReceived  atomic.Int64
	relayForwarded atomic.Int64
	relayErrMu     sync.Mutex
	relayErr       string
)

func setRelayErr(s string) {
	relayErrMu.Lock()
	relayErr = s
	relayErrMu.Unlock()
}

func getRelayErr() string {
	relayErrMu.Lock()
	defer relayErrMu.Unlock()
	return relayErr
}

func writeCotStatus(s cotStatus) {
	data, _ := json.Marshal(s)
	tmp := cotStatusPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, cotStatusPath)
}

var dnsmasqLeasePaths = []string{
	"/var/lib/misc/dnsmasq.leases",
	"/tmp/dnsmasq.leases",
	"/run/dnsmasq.leases",
}

type gpsData struct {
	HasFix    bool    `json:"has_fix"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude"`
	HDOP      float64 `json:"hdop"`
	Timestamp int64   `json:"timestamp"`
}

func readMeshConf(key string) string {
	f, err := os.Open(meshConfPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	prefix := key + "="
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// eudPort returns the UDP port for unicast CoT delivery to EUDs.
// Defaults to mcastPort (6969) so ATAK receives it on the same socket as
// its SA multicast input. Overridable via mesh.conf cot_eud_port.
func eudPort() int {
	if v := readMeshConf("cot_eud_port"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return mcastPort
}

// eudInterfaces returns bridge members of br0 that face end-user devices,
// excluding bat0 (mesh) and the active uplink interface (gateway ethernet).
func eudInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net/br0/brif")
	if err != nil {
		return nil
	}
	var uplinkIface string
	if data, err := os.ReadFile("/var/run/upstream_iface"); err == nil {
		uplinkIface = strings.TrimSpace(string(data))
	}
	var eud []string
	for _, e := range entries {
		name := e.Name()
		if name == "bat0" || name == uplinkIface {
			continue
		}
		eud = append(eud, name)
	}
	return eud
}

func getCallsign() string {
	if cs := readMeshConf("callsign"); cs != "" {
		return cs
	}
	h, _ := os.Hostname()
	return h
}

// cotIdentity controls how this node's own position marker presents on the
// map. Defaults to an equipment identity (no team); an operator can
// reconfigure a node to read as a team member instead — e.g. when its
// carrier has no CoT-capable EUD of their own — by setting cot_team.
type cotIdentity struct {
	Type string
	Team string
	Role string
	Icon string
}

func getCoTIdentity() cotIdentity {
	id := cotIdentity{Type: defaultCoTType, Role: defaultCoTRole}
	if t := readMeshConf("cot_type"); t != "" {
		id.Type = t
	}
	id.Team = readMeshConf("cot_team")
	if r := readMeshConf("cot_role"); r != "" {
		id.Role = r
	}
	id.Icon = readMeshConf("cot_icon")
	return id
}

func getUID() string {
	h, _ := os.Hostname()
	return "MANET-" + h
}

var lastGPSStale time.Time

func readGPS() *gpsData {
	data, err := os.ReadFile(gpsStatusPath)
	if err != nil {
		return nil
	}
	var gps gpsData
	if err := json.Unmarshal(data, &gps); err != nil || !gps.HasFix {
		return nil
	}
	if gps.Timestamp > 0 && time.Now().Unix()-gps.Timestamp > 30 {
		if lastGPSStale.IsZero() {
			lastGPSStale = time.Now()
			log.Printf("GPS file stale (%ds old), ignoring", time.Now().Unix()-gps.Timestamp)
		}
		if time.Since(lastGPSStale) > 2*time.Minute {
			if readMeshConf("gps") == "n" {
				// GPS deliberately disabled on this hardware — don't nag a
				// service the operator turned off on purpose.
				lastGPSStale = time.Now()
			} else {
				log.Printf("GPS stale for >2m, restarting gps-reader")
				exec.Command("systemctl", "restart", "gps-reader").Run()
				lastGPSStale = time.Now()
			}
		}
		return nil
	}
	if !lastGPSStale.IsZero() {
		log.Printf("GPS fix recovered")
		lastGPSStale = time.Time{}
	}
	return &gps
}

func isoUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func buildCoTEvent(gps *gpsData, uid, callsign string, identity cotIdentity) []byte {
	now := time.Now()
	stale := now.Add(staleSeconds * time.Second)
	ce := gps.HDOP * 3.0
	if ce < 5.0 {
		ce = 5.0
	}

	var detail strings.Builder
	fmt.Fprintf(&detail, `<contact callsign="%s"/>`, html.EscapeString(callsign))
	if identity.Team != "" {
		fmt.Fprintf(&detail, `<__group name="%s" role="%s"/>`, html.EscapeString(identity.Team), html.EscapeString(identity.Role))
	}
	detail.WriteString(`<precisionlocation altsrc="GPS" geopointsrc="GPS"/>`)
	detail.WriteString(`<track course="0.0" speed="0.0"/>`)
	if identity.Icon != "" {
		fmt.Fprintf(&detail, `<usericon iconsetpath="%s"/>`, html.EscapeString(identity.Icon))
	}

	xml := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<event version="2.0" uid="%s" type="%s"`+
			` time="%s" start="%s"`+
			` stale="%s" how="m-g">`+
			`<point lat="%f" lon="%f"`+
			` hae="%f" ce="%.1f" le="9999999.0"/>`+
			`<detail>%s</detail></event>`,
		html.EscapeString(uid), html.EscapeString(identity.Type),
		isoUTC(now), isoUTC(now),
		isoUTC(stale),
		gps.Latitude, gps.Longitude,
		gps.Altitude, ce,
		detail.String(),
	)
	return []byte(xml)
}

func getEUDIPs() []string {
	now := time.Now().Unix()
	for _, path := range dnsmasqLeasePaths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var ips []string
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) < 4 {
				continue
			}
			expiry, err := strconv.ParseInt(parts[0], 10, 64)
			if err == nil && expiry > 0 && expiry < now {
				continue
			}
			ips = append(ips, parts[2])
		}
		f.Close()
		return ips
	}
	return nil
}

// localIPv4s returns every IPv4 address on this host, used to drop our own
// multicast when it loops back to the relay socket.
func localIPv4s() map[string]bool {
	out := make(map[string]bool)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			out[ipnet.IP.String()] = true
		}
	}
	return out
}

// relayLoop joins the CoT multicast group and forwards everything it hears
// from *other* nodes to this node's own EUDs as unicast.
//
// Without this, a tablet attached to node B can only learn node A's position
// from the raw multicast, which has to survive the 802.11 hop as an unretried
// broadcast frame — Android drops those in Wi-Fi power save. Unicast to the
// DHCP lease is retried by the AP and gets through.
//
// It also runs independently of GPS: a node with no fix still relays its
// peers' positions, which is exactly the case that used to go dark.
func relayLoop(uid string) {
	group, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", mcastGroup, mcastPort))
	if err != nil {
		setRelayErr("resolve: " + err.Error())
		log.Printf("relay: resolve failed: %v", err)
		return
	}

	var conn *net.UDPConn
	for {
		iface, ierr := net.InterfaceByName(controlIface)
		if ierr == nil {
			conn, err = net.ListenMulticastUDP("udp4", iface, group)
			if err == nil {
				break
			}
		} else {
			err = ierr
		}
		// br0 may not exist yet at boot; keep trying rather than giving up.
		setRelayErr("join: " + err.Error())
		log.Printf("relay: cannot join %s on %s (%v), retrying in 15s", mcastGroup, controlIface, err)
		time.Sleep(15 * time.Second)
	}
	defer conn.Close()
	setRelayErr("")
	log.Printf("Relay listening on %s:%d via %s", mcastGroup, mcastPort, controlIface)

	port := eudPort()

	sender, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		setRelayErr("sender socket: " + err.Error())
		log.Printf("relay: sender socket failed: %v", err)
		return
	}
	defer sender.Close()
	if uc, ok := sender.(*net.UDPConn); ok {
		if rc, err := uc.SyscallConn(); err == nil {
			rc.Control(func(fd uintptr) {
				syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, controlIface)
			})
		}
	}

	selfIPs := localIPv4s()
	var lastRefresh time.Time
	var lastEUDLog time.Time
	buf := make([]byte, 8192)

	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			setRelayErr("read: " + err.Error())
			log.Printf("relay: read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		if time.Since(lastRefresh) > time.Minute {
			selfIPs = localIPv4s()
			lastRefresh = time.Now()
		}

		if selfIPs[src.IP.String()] {
			continue
		}
		payload := buf[:n]
		if strings.Contains(string(payload), `uid="`+uid+`"`) {
			continue
		}

		relayReceived.Add(1)

		eudIfs := eudInterfaces()
		if len(eudIfs) == 0 {
			if time.Since(lastEUDLog) > 5*time.Minute {
				log.Printf("relay: no EUD interfaces on br0, skipping forward")
				lastEUDLog = time.Now()
			}
			continue
		}

		for _, ip := range getEUDIPs() {
			addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", ip, port))
			if err != nil {
				continue
			}
			if _, err := sender.WriteTo(payload, addr); err != nil {
				setRelayErr(fmt.Sprintf("forward to %s: %v", ip, err))
				continue
			}
			relayForwarded.Add(1)
		}
	}
}

var Version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(Version)
		return
	}

	log.SetFlags(0)
	log.SetPrefix("[cot-emitter] ")
	log.Printf("starting (version %s)", Version)

	callsign := getCallsign()
	uid := getUID()
	identity := getCoTIdentity()
	log.Printf("Starting CoT emitter: uid=%s callsign=%s type=%s team=%q", uid, callsign, identity.Type, identity.Team)

	// Relay peers' CoT to our own EUDs. Runs regardless of local GPS state.
	go relayLoop(uid)

	sock, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		log.Fatalf("Failed to create UDP socket: %v", err)
	}
	defer sock.Close()
	if uc, ok := sock.(*net.UDPConn); ok {
		if rc, err := uc.SyscallConn(); err == nil {
			rc.Control(func(fd uintptr) {
				syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, controlIface)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0x20)
				if iface, err := net.InterfaceByName(controlIface); err == nil {
					mreq := &syscall.IPMreqn{Ifindex: int32(iface.Index)}
					syscall.SetsockoptIPMreqn(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, mreq)
				}
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 5)
			})
		}
	}

	mcastAddr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", mcastGroup, mcastPort))
	var lastMcast time.Time
	var totalSent int64
	port := eudPort()
	log.Printf("EUD unicast port: %d", port)

	for {
		eudIPs := getEUDIPs()
		eudIfs := eudInterfaces()
		gps := readGPS()
		if gps == nil {
			writeCotStatus(cotStatus{
				Running:        true,
				GPSFix:         false,
				UnicastCount:   len(eudIPs),
				EUDIPs:         eudIPs,
				EUDInterfaces:  eudIfs,
				LastError:      "no GPS fix",
				RelayReceived:  relayReceived.Load(),
				RelayForwarded: relayForwarded.Load(),
				RelayError:     getRelayErr(),
				Timestamp:      time.Now().Unix(),
			})
			time.Sleep(pollInterval)
			continue
		}

		event := buildCoTEvent(gps, uid, callsign, identity)
		var lastErr string

		for _, ip := range eudIPs {
			addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", ip, port))
			if err != nil {
				continue
			}
			if _, err := sock.WriteTo(event, addr); err != nil {
				lastErr = fmt.Sprintf("unicast to %s: %v", ip, err)
				log.Printf("unicast to %s:%d failed: %v", ip, port, err)
			} else {
				totalSent++
			}
		}

		if time.Since(lastMcast) >= mcastInterval {
			if _, err := sock.WriteTo(event, mcastAddr); err != nil {
				lastErr = fmt.Sprintf("multicast: %v", err)
				log.Printf("multicast failed: %v", err)
			} else {
				totalSent++
			}
			lastMcast = time.Now()
		}

		writeCotStatus(cotStatus{
			Running:        true,
			GPSFix:         true,
			LastSentUTC:    time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			UnicastCount:   len(eudIPs),
			EUDIPs:         eudIPs,
			EUDInterfaces:  eudIfs,
			McastEnabled:   true,
			TotalSent:      totalSent,
			RelayReceived:  relayReceived.Load(),
			RelayForwarded: relayForwarded.Load(),
			RelayError:     getRelayErr(),
			LastError:      lastErr,
			Timestamp:      time.Now().Unix(),
		})

		time.Sleep(pollInterval)
	}
}
