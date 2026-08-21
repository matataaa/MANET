package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Per-service mesh bandwidth accounting. A dedicated count-only nftables
// table (accept policy, no verdicts) tallies bytes per service port; a
// sampler goroutine turns counter deltas into rates. Totals come from the
// mesh radio's sysfs byte counters, which include relay traffic and
// per-frame overhead that the port counters cannot see.

const airtimeTable = "manet_acct"

// Ports must match the services: mesh-voice RTP 4370-4379 (per-channel),
// cot-emitter 6969, mesh-chat HTTP+multicast 9800, WireGuard 51820.
const airtimeRuleset = `
table inet manet_acct {
	counter voice_in {}
	counter voice_out {}
	counter cot_in {}
	counter cot_out {}
	counter chat_in {}
	counter chat_out {}
	counter wg_in {}
	counter wg_out {}
	chain input {
		type filter hook input priority filter - 1; policy accept;
		udp dport 4370-4379 counter name "voice_in"
		udp dport 6969 counter name "cot_in"
		tcp dport 9800 counter name "chat_in"
		udp dport 9800 counter name "chat_in"
		tcp sport 9800 counter name "chat_in"
		udp dport 51820 counter name "wg_in"
	}
	chain output {
		type filter hook output priority filter - 1; policy accept;
		udp dport 4370-4379 counter name "voice_out"
		udp dport 6969 counter name "cot_out"
		tcp dport 9800 counter name "chat_out"
		udp dport 9800 counter name "chat_out"
		tcp sport 9800 counter name "chat_out"
		udp dport 51820 counter name "wg_out"
	}
}
`

type ServiceRate struct {
	Name  string  `json:"name"`
	TxBps float64 `json:"tx_bps"`
	RxBps float64 `json:"rx_bps"`
}

type AirtimeInfo struct {
	Services     []ServiceRate `json:"services,omitempty"`
	TotalTxBps   float64       `json:"total_tx_bps"`
	TotalRxBps   float64       `json:"total_rx_bps"`
	CapacityMbps float64       `json:"capacity_mbps"`
	MeshIface    string        `json:"mesh_iface"`
	CountersOK   bool          `json:"counters_ok"`
	WifiTxBps    float64       `json:"wifi_tx_bps,omitempty"`
	WifiRxBps    float64       `json:"wifi_rx_bps,omitempty"`
	WifiIfaces   []string      `json:"wifi_ifaces,omitempty"`
}

var (
	airtimeMu   sync.Mutex
	airtimeInfo *AirtimeInfo
)

func currentAirtime() *AirtimeInfo {
	airtimeMu.Lock()
	defer airtimeMu.Unlock()
	return airtimeInfo
}

func nftAvailable() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

func ensureAirtimeTable() bool {
	if !nftAvailable() {
		return false
	}
	if _, err := runCmdStdout(5*time.Second, "nft", "list", "table", "inet", airtimeTable); err == nil {
		return true
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(airtimeRuleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("airtime: nft table setup failed: %v (%s)", err, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// readAirtimeCounters returns cumulative bytes per named counter.
func readAirtimeCounters() map[string]int64 {
	out, err := runCmdStdout(5*time.Second, "nft", "-j", "list", "counters", "table", "inet", airtimeTable)
	if err != nil {
		return nil
	}
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name  string `json:"name"`
				Bytes int64  `json:"bytes"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if json.Unmarshal([]byte(out), &doc) != nil {
		return nil
	}
	m := make(map[string]int64)
	for _, e := range doc.Nftables {
		if e.Counter != nil {
			m[e.Counter.Name] = e.Counter.Bytes
		}
	}
	return m
}

func readIfaceBytes(iface string) (rx, tx int64, ok bool) {
	rxb, err1 := os.ReadFile("/sys/class/net/" + iface + "/statistics/rx_bytes")
	txb, err2 := os.ReadFile("/sys/class/net/" + iface + "/statistics/tx_bytes")
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	rx = parseInt64(strings.TrimSpace(string(rxb)))
	tx = parseInt64(strings.TrimSpace(string(txb)))
	return rx, tx, true
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func meshAirIface() string {
	if _, err := os.Stat("/sys/class/net/wlan2"); err == nil {
		return "wlan2"
	}
	return "bat0"
}

// wifiMeshIfaces returns whichever of the standard-WiFi mesh radios are
// present on this node. Interface naming is pinned fleet-wide by
// radio-setup.sh's .link rules: wlan0/wlan1 are always WiFi mesh, never AP
// or HaLow, so their combined byte counters are safe to sum as one total.
func wifiMeshIfaces() []string {
	var out []string
	for _, i := range []string{"wlan0", "wlan1"} {
		if _, err := os.Stat("/sys/class/net/" + i); err == nil {
			out = append(out, i)
		}
	}
	return out
}

// readIfacesBytes sums byte counters across multiple interfaces, e.g. to
// get a combined wlan0+wlan1 total. Fails closed (ok=false) if any listed
// interface can't be read, so a partial sum is never reported as real.
func readIfacesBytes(ifaces []string) (rx, tx int64, ok bool) {
	if len(ifaces) == 0 {
		return 0, 0, false
	}
	for _, i := range ifaces {
		r, t, iok := readIfaceBytes(i)
		if !iok {
			return 0, 0, false
		}
		rx += r
		tx += t
	}
	return rx, tx, true
}

func airtimeLoop() {
	countersOK := ensureAirtimeTable()
	iface := meshAirIface()
	wifiIfaces := wifiMeshIfaces()

	services := []struct{ name, in, out string }{
		{"Voice", "voice_in", "voice_out"},
		{"CoT", "cot_in", "cot_out"},
		{"Chat", "chat_in", "chat_out"},
		{"WireGuard", "wg_in", "wg_out"},
	}

	var prevCounters map[string]int64
	var prevRx, prevTx int64
	var prevWifiRx, prevWifiTx int64
	var prevAt time.Time

	const interval = 5 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		now := time.Now()
		counters := map[string]int64{}
		if countersOK {
			counters = readAirtimeCounters()
			if counters == nil {
				// Table can vanish on nft flushes (e.g. firewall reload) — recreate.
				countersOK = ensureAirtimeTable()
			}
		}
		rx, tx, ifOK := readIfaceBytes(iface)
		wifiRx, wifiTx, wifiOK := readIfacesBytes(wifiIfaces)

		if !prevAt.IsZero() {
			dt := now.Sub(prevAt).Seconds()
			if dt > 0 {
				info := &AirtimeInfo{
					CapacityMbps: meshRefThroughput(),
					MeshIface:    iface,
					CountersOK:   countersOK && counters != nil && prevCounters != nil,
					WifiIfaces:   wifiIfaces,
				}
				if info.CountersOK {
					for _, s := range services {
						txd := float64(counters[s.out]-prevCounters[s.out]) * 8 / dt
						rxd := float64(counters[s.in]-prevCounters[s.in]) * 8 / dt
						if txd < 0 {
							txd = 0
						}
						if rxd < 0 {
							rxd = 0
						}
						info.Services = append(info.Services, ServiceRate{Name: s.name, TxBps: txd, RxBps: rxd})
					}
				}
				if ifOK && prevRx > 0 {
					info.TotalRxBps = float64(rx-prevRx) * 8 / dt
					info.TotalTxBps = float64(tx-prevTx) * 8 / dt
					if info.TotalRxBps < 0 {
						info.TotalRxBps = 0
					}
					if info.TotalTxBps < 0 {
						info.TotalTxBps = 0
					}
				}
				if wifiOK && prevWifiRx > 0 {
					info.WifiRxBps = float64(wifiRx-prevWifiRx) * 8 / dt
					info.WifiTxBps = float64(wifiTx-prevWifiTx) * 8 / dt
					if info.WifiRxBps < 0 {
						info.WifiRxBps = 0
					}
					if info.WifiTxBps < 0 {
						info.WifiTxBps = 0
					}
				}
				airtimeMu.Lock()
				airtimeInfo = info
				airtimeMu.Unlock()
			}
		}

		if counters != nil {
			prevCounters = counters
		}
		prevRx, prevTx, prevAt = rx, tx, now
		prevWifiRx, prevWifiTx = wifiRx, wifiTx

		<-ticker.C
	}
}
