package main

// Gateway selection for mesh clients (docs/GATEWAY_SELECTION.md), after
// upstream very-srs/MANET 0.559's gateway-route-manager.sh.
//
// batman-adv picks a gateway itself (the "*" in batctl gwl), but that pick
// only steers its DHCP handling, switches on a single reading at a fixed
// 5 Mbit/s margin, and never fails over. Every gateway is scored here as the
// bottleneck of the mesh path to it and the download bandwidth it announces
// (measured by manet-uplink-speed.sh on Ethernet uplinks), and a node moves
// only for a clear, sustained gain, because a switch moves NAT to another
// public address and breaks every open connection.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	switchRatio      = 1.5               // candidate must score this many times the current...
	switchMinGain    = 2.0               // ...and this many Mbit/s more,
	switchSustain    = 60 * time.Second  // for this long,
	switchHold       = 300 * time.Second // and not within this long of the last switch.
	unreachablePolls = 2                 // missed pings before an immediate failover
)

type gateway struct {
	MAC   string
	Score float64 // Mbit/s: min(path throughput, announced download)
}

// One batctl gwl -H -n line, e.g.
//
//   - 0c:bf:74:00:28:d2 (      111.6) 00:0a:52:0f:1b:f7 [     wlan1]: 10.0/2.0 MBit
var gwLineRE = regexp.MustCompile(`^\s*\*?\s*([0-9a-fA-F:]{17})\s+\(\s*([0-9.]+)\)(?:.*\]:\s*([0-9.]+)/)?`)

// parseGateways returns every gateway in batctl gwl -H -n output, best
// first; equal scores sort by MAC so every node orders them the same way.
func parseGateways(out string) []gateway {
	var gws []gateway
	for _, line := range strings.Split(out, "\n") {
		m := gwLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		score, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		if m[3] != "" {
			if bw, err := strconv.ParseFloat(m[3], 64); err == nil && bw < score {
				score = bw
			}
		}
		gws = append(gws, gateway{MAC: strings.ToLower(m[1]), Score: score})
	}
	sort.SliceStable(gws, func(i, j int) bool {
		if gws[i].Score != gws[j].Score {
			return gws[i].Score > gws[j].Score
		}
		return gws[i].MAC < gws[j].MAC
	})
	return gws
}

// selector carries the choice between polls.
type selector struct {
	cur          string // MAC of the gateway the route uses; "" when none
	pending      string // better gateway waiting out switchSustain
	pendingSince time.Time
	lastSwitch   time.Time
	unreachable  int
}

func newSelector(now time.Time) *selector {
	// The first choice is not held back by switchHold.
	return &selector{lastSwitch: now.Add(-switchHold)}
}

func noticeablyBetter(candidate, current float64) bool {
	return candidate >= current*switchRatio && candidate-current >= switchMinGain
}

// plan decides which gateways to try this poll, in order. curReachable is
// whether the current gateway answered a ping (ignored when it is not in
// gws). urgent is non-empty when the move is immediate (no gateway yet, or
// the current one is gone or not answering); then every gateway is a
// candidate, best first, except a current one that stopped answering.
func (s *selector) plan(gws []gateway, curReachable bool, now time.Time) (candidates []string, urgent string) {
	if len(gws) == 0 {
		s.cur, s.pending = "", ""
		return nil, ""
	}
	var curScore float64
	present := false
	for _, g := range gws {
		if g.MAC == s.cur {
			curScore, present = g.Score, true
		}
	}
	all := func(skip string) []string {
		var macs []string
		for _, g := range gws {
			if g.MAC != skip {
				macs = append(macs, g.MAC)
			}
		}
		return macs
	}

	switch {
	case s.cur == "":
		return all(""), "no gateway selected"
	case !present:
		return all(""), "gateway " + s.cur + " no longer announced"
	}

	if curReachable {
		s.unreachable = 0
	} else {
		s.unreachable++
	}
	if s.unreachable >= unreachablePolls {
		return all(s.cur), "gateway " + s.cur + " not answering"
	}

	best := gws[0]
	if best.MAC == s.cur || now.Sub(s.lastSwitch) < switchHold || !noticeablyBetter(best.Score, curScore) {
		s.pending = ""
		return []string{s.cur}, ""
	}
	if s.pending != best.MAC {
		s.pending, s.pendingSince = best.MAC, now
		return []string{s.cur}, ""
	}
	if now.Sub(s.pendingSince) >= switchSustain {
		return []string{best.MAC}, ""
	}
	return []string{s.cur}, ""
}

// switched records that the route now uses mac.
func (s *selector) switched(mac string, now time.Time) {
	if mac == s.cur {
		return
	}
	s.cur, s.lastSwitch, s.unreachable, s.pending = mac, now, 0, ""
}

// bandwidthArg converts a gateway_bandwidth value to batctl's syntax and
// returns its download in Mbit/s. The UI stores values like "10M/10M", which
// batctl rejects; bare numbers are kbit to batctl.
func bandwidthArg(v string) (arg string, downMbit float64, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(v), "/", 2)
	conv := func(p string) (string, float64, bool) {
		p = strings.ToLower(strings.TrimSpace(p))
		unit, mult := "kbit", 0.001
		switch {
		case strings.HasSuffix(p, "mbit"):
			p, unit, mult = strings.TrimSuffix(p, "mbit"), "mbit", 1
		case strings.HasSuffix(p, "kbit"):
			p = strings.TrimSuffix(p, "kbit")
		case strings.HasSuffix(p, "m"):
			p, unit, mult = strings.TrimSuffix(p, "m"), "mbit", 1
		case strings.HasSuffix(p, "k"):
			p = strings.TrimSuffix(p, "k")
		}
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || n <= 0 {
			return "", 0, false
		}
		return p + unit, n * mult, true
	}
	down, mbit, ok := conv(parts[0])
	if !ok {
		return "", 0, false
	}
	if len(parts) == 1 {
		return down, mbit, true
	}
	up, _, ok := conv(parts[1])
	if !ok {
		return "", 0, false
	}
	return down + "/" + up, mbit, true
}

var announcedRE = regexp.MustCompile(`announced bw:\s*([0-9.]+)/`)

// announcedDownMbit reads the download bandwidth from batctl gw output.
func announcedDownMbit(gwOut string) (float64, bool) {
	m := announcedRE.FindStringSubmatch(gwOut)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}
