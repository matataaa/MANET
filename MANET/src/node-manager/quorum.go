package main

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Registry entries older than this are excluded from the active count
	// — a node that's gone quiet shouldn't count against quorum.
	staleNodeThreshold = 600 * time.Second
	quorumThreshold    = 0.5
)

var originatorRE = regexp.MustCompile(`^\s*\*?\s*([0-9a-fA-F:]{17})`)

// uniqueBatmanOriginators counts distinct originator MACs in `batctl o` —
// how many nodes batman-adv itself can currently reach, independent of
// what alfred's gossip layer still remembers.
func uniqueBatmanOriginators() int {
	out, err := exec.Command("/usr/sbin/batctl", "o").Output()
	if err != nil {
		return 0
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		m := originatorRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen[strings.ToLower(m[1])] = true
	}
	return len(seen)
}

func activeAlfredCount(registry map[string]map[string]string) int {
	now := time.Now().Unix()
	count := 0
	for _, fields := range registry {
		if fields["NODE_STATE"] != "ACTIVE" {
			continue
		}
		ts, err := strconv.ParseInt(fields["LAST_SEEN_TIMESTAMP"], 10, 64)
		if err != nil || now-ts > int64(staleNodeThreshold.Seconds()) {
			continue
		}
		count++
	}
	return count
}

// quorumOK mirrors upstream's three-scenario quorum-checker.sh: batman-adv
// (data plane, "who can I actually reach") vs alfred (gossip, "who does the
// mesh believe exists") can disagree, and the gap tells you whether this
// node is genuinely isolated or just a functional minority partition.
func quorumOK(registry map[string]map[string]string) bool {
	originators := uniqueBatmanOriginators()
	active := activeAlfredCount(registry)

	// Scenario 1 — solo isolation: alfred still remembers other nodes but
	// batman-adv sees none of them. Fall back to the lobby to try to find
	// the mesh again.
	if originators == 0 && active > 2 {
		return false
	}

	// Scenario 2 — small functional island: a minority partition that's
	// still internally coherent is fine to keep operating on its own.
	if originators >= 2 && originators < active/3 {
		return true
	}

	// Scenario 3 — barely connected: below half of the expected active
	// nodes but still ≥2 originators is a degraded-but-real mesh; below
	// that, treat it as isolated.
	if active > 3 {
		quorumMin := int(float64(active) * quorumThreshold)
		if originators < quorumMin {
			return originators >= 2
		}
	}

	return true
}
