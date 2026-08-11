package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

var (
	mcsRE  = regexp.MustCompile(`\b(?:VHT-)?MCS\s+(\d+)\b`)
	nssRE  = regexp.MustCompile(`\b(?:VHT-|HE-|EHT-)?NSS\s+(\d+)\b`)
	giRE   = regexp.MustCompile(`\b(?:HE-|EHT-)?GI\s+([0-9.]+)\b`)
	sgiRE  = regexp.MustCompile(`\bshort GI\b`)
	bwRE   = regexp.MustCompile(`\b(\d+)\s*MHz\b`)
	staRE  = regexp.MustCompile(`^Station\s+([0-9a-f:]{17})\s+\(`)
	inacRE = regexp.MustCompile(`inactive time:\s*(\d+)\s*ms`)
	sigRE  = regexp.MustCompile(`signal:\s*(-?\d+)\s*dBm`)
)

type peer struct {
	MAC        string `json:"peer_mac"`
	InactiveMS int    `json:"inactive_ms"`
	Signal     int    `json:"signal_dbm"`
	TxMCS      string `json:"tx_mcs"`
	RxMCS      string `json:"rx_mcs"`
	TxSummary  string `json:"tx_summary"`
	RxSummary  string `json:"rx_summary"`
}

func parseRateSummary(line string) (mcs, summary string) {
	m := mcsRE.FindStringSubmatch(line)
	if m == nil {
		return "", ""
	}
	mcs = "MCS" + m[1]
	parts := []string{mcs}
	if n := nssRE.FindStringSubmatch(line); n != nil {
		parts = append(parts, "N"+n[1])
	}
	if sgiRE.MatchString(line) {
		parts = append(parts, "SGI")
	} else if g := giRE.FindStringSubmatch(line); g != nil {
		parts = append(parts, "GI"+g[1])
	}
	if b := bwRE.FindStringSubmatch(line); b != nil {
		parts = append(parts, b[1]+"M")
	}
	return mcs, strings.Join(parts, " ")
}

func parseStationDump(text string) []peer {
	var peers []peer
	var current *peer
	for _, line := range strings.Split(text, "\n") {
		if m := staRE.FindStringSubmatch(line); m != nil {
			if current != nil {
				peers = append(peers, *current)
			}
			current = &peer{MAC: strings.ToLower(m[1]), InactiveMS: 1e9, Signal: -999}
			continue
		}
		if current == nil {
			continue
		}
		if m := inacRE.FindStringSubmatch(line); m != nil {
			fmt.Sscanf(m[1], "%d", &current.InactiveMS)
		}
		if m := sigRE.FindStringSubmatch(line); m != nil {
			fmt.Sscanf(m[1], "%d", &current.Signal)
		}
		if strings.Contains(line, "tx bitrate:") {
			current.TxMCS, current.TxSummary = parseRateSummary(line)
		}
		if strings.Contains(line, "rx bitrate:") {
			current.RxMCS, current.RxSummary = parseRateSummary(line)
		}
	}
	if current != nil {
		peers = append(peers, *current)
	}
	return peers
}

func main() {
	iface := flag.String("iface", "wlan2", "wireless interface")
	shell := flag.Bool("shell", false, "output shell variables")
	flag.Parse()

	out, _ := exec.Command("/usr/sbin/iw", "dev", *iface, "station", "dump").CombinedOutput()
	peers := parseStationDump(string(out))

	var active []peer
	for _, p := range peers {
		if p.TxMCS != "" || p.RxMCS != "" {
			active = append(active, p)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].InactiveMS != active[j].InactiveMS {
			return active[i].InactiveMS < active[j].InactiveMS
		}
		return active[i].Signal > active[j].Signal
	})

	var best peer
	if len(active) > 0 {
		best = active[0]
	}

	txMCS := best.TxSummary
	if txMCS == "" {
		txMCS = best.TxMCS
	}
	rxMCS := best.RxSummary
	if rxMCS == "" {
		rxMCS = best.RxMCS
	}

	if *shell {
		prefix := strings.ToUpper(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(*iface, "_"))
		fmt.Printf("%s_TX_MCS='%s'\n", prefix, txMCS)
		fmt.Printf("%s_RX_MCS='%s'\n", prefix, rxMCS)
		fmt.Printf("%s_MCS_PEER='%s'\n", prefix, best.MAC)
		fmt.Printf("%s_MCS_SIGNAL_DBM='%d'\n", prefix, best.Signal)
		fmt.Printf("%s_MCS_PEER_COUNT='%d'\n", prefix, len(active))
		return
	}

	result := map[string]interface{}{
		"iface":       *iface,
		"peer_mac":    best.MAC,
		"tx_mcs":      txMCS,
		"rx_mcs":      rxMCS,
		"signal_dbm":  best.Signal,
		"inactive_ms": best.InactiveMS,
		"peer_count":  len(active),
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
