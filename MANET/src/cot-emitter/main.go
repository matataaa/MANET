package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	gpsStatusPath    = "/run/gps_status.json"
	meshConfPath     = "/etc/mesh.conf"
	cotStatusPath    = "/run/cot_emitter_status.json"
	pollInterval     = 10 * time.Second
	mcastInterval    = 30 * time.Second
	staleSeconds     = 120
	unicastPort      = 4349
	mcastGroup       = "239.2.3.1"
	mcastPort        = 6969
	cotType          = "a-f-G-U-C"
)

type cotStatus struct {
	Running       bool   `json:"running"`
	LastSentUTC   string `json:"last_sent_utc,omitempty"`
	UnicastCount  int    `json:"unicast_targets"`
	McastEnabled  bool   `json:"mcast_enabled"`
	TotalSent     int64  `json:"total_sent"`
	LastError     string `json:"last_error,omitempty"`
	Timestamp     int64  `json:"timestamp"`
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

func getCallsign() string {
	if cs := readMeshConf("callsign"); cs != "" {
		return cs
	}
	h, _ := os.Hostname()
	return h
}

func getUID() string {
	h, _ := os.Hostname()
	return "MANET-" + h
}

func readGPS() *gpsData {
	data, err := os.ReadFile(gpsStatusPath)
	if err != nil {
		return nil
	}
	var gps gpsData
	if err := json.Unmarshal(data, &gps); err != nil || !gps.HasFix {
		return nil
	}
	return &gps
}

func isoUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func buildCoTEvent(gps *gpsData, uid, callsign string) []byte {
	now := time.Now()
	stale := now.Add(staleSeconds * time.Second)
	ce := gps.HDOP * 3.0
	if ce < 5.0 {
		ce = 5.0
	}

	xml := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<event version="2.0" uid="%s" type="%s"`+
			` time="%s" start="%s"`+
			` stale="%s" how="m-g">`+
			`<point lat="%f" lon="%f"`+
			` hae="%f" ce="%.1f" le="9999999.0"/>`+
			`<detail>`+
			`<contact callsign="%s"/>`+
			`<__group name="Cyan" role="Team Member"/>`+
			`<precisionlocation altsrc="GPS" geopointsrc="GPS"/>`+
			`<track course="0.0" speed="0.0"/>`+
			`</detail></event>`,
		html.EscapeString(uid), cotType,
		isoUTC(now), isoUTC(now),
		isoUTC(stale),
		gps.Latitude, gps.Longitude,
		gps.Altitude, ce,
		html.EscapeString(callsign),
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

func main() {
	log.SetFlags(0)
	log.SetPrefix("[cot-emitter] ")

	callsign := getCallsign()
	uid := getUID()
	log.Printf("Starting CoT emitter: uid=%s callsign=%s", uid, callsign)

	sock, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		log.Fatalf("Failed to create UDP socket: %v", err)
	}
	defer sock.Close()
	if uc, ok := sock.(*net.UDPConn); ok {
		if rc, err := uc.SyscallConn(); err == nil {
			rc.Control(func(fd uintptr) {
				syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, "br0")
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0x20)
			})
		}
	}

	mcastAddr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", mcastGroup, mcastPort))
	var lastMcast time.Time
	var totalSent int64

	for {
		gps := readGPS()
		if gps == nil {
			writeCotStatus(cotStatus{
				Running:   true,
				LastError: "no GPS fix",
				Timestamp: time.Now().Unix(),
			})
			time.Sleep(pollInterval)
			continue
		}

		event := buildCoTEvent(gps, uid, callsign)
		eudIPs := getEUDIPs()
		var lastErr string

		for _, ip := range eudIPs {
			addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", ip, unicastPort))
			if err != nil {
				continue
			}
			if _, err := sock.WriteTo(event, addr); err != nil {
				lastErr = fmt.Sprintf("unicast to %s: %v", ip, err)
				log.Printf("unicast to %s:%d failed: %v", ip, unicastPort, err)
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
			Running:      true,
			LastSentUTC:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			UnicastCount: len(eudIPs),
			McastEnabled: true,
			TotalSent:    totalSent,
			LastError:    lastErr,
			Timestamp:    time.Now().Unix(),
		})

		time.Sleep(pollInterval)
	}
}
