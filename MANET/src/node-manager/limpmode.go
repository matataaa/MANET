package main

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	limpModeConsensus   = 0.5
	limpModeMinDuration = 300 * time.Second
	// Distinct from /var/run/mesh_limp_mode (this node's own per-election
	// signal, written by runACSTick/setLimpMode and gossiped as the
	// IS_IN_LIMP_MODE registry field — see channel_election.go). This file
	// is the separate mesh-wide consensus state that actually gates the
	// bitrate throttle, with its own entry timestamp for exit hysteresis.
	limpConsensusStateFile = "/var/run/mesh_limp_mode.state"
)

// limpConsensusRatio is the fraction of active registry nodes currently
// reporting their own IS_IN_LIMP_MODE signal. A single node's bad RF
// picture shouldn't throttle the whole mesh's bitrates — a majority should.
func limpConsensusRatio(registry map[string]map[string]string) float64 {
	active, limping := 0, 0
	for _, fields := range registry {
		if fields["NODE_STATE"] != "ACTIVE" {
			continue
		}
		active++
		if fields["IS_IN_LIMP_MODE"] == "true" {
			limping++
		}
	}
	if active == 0 {
		return 0
	}
	return float64(limping) / float64(active)
}

// reconcileLimpMode applies or clears the legacy/robust bitrate throttle on
// both mesh radios based on mesh-wide limp consensus. Entry is immediate
// once consensus crosses the threshold; exit requires the consensus to have
// cleared AND limpModeMinDuration to have elapsed since entry, so the mesh
// doesn't thrash in and out of limp mode as the ratio hovers near 50%.
func reconcileLimpMode(registry map[string]map[string]string, iface24, iface5 string) {
	ratio := limpConsensusRatio(registry)
	currentlyLimp := fileExists(limpConsensusStateFile)

	if ratio > limpModeConsensus {
		if !currentlyLimp {
			log.Printf("[acs] limp mode: consensus %.0f%% of active nodes — throttling bitrates", ratio*100)
			setLegacyBitrates(iface24, iface5)
			os.WriteFile(limpConsensusStateFile, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0644)
		}
		return
	}

	if !currentlyLimp {
		return
	}

	entryTS := readLimpEntryTime()
	if entryTS > 0 && time.Since(time.Unix(entryTS, 0)) >= limpModeMinDuration {
		log.Println("[acs] limp mode: consensus cleared and min duration elapsed — restoring bitrates")
		clearBitrateLimit(iface24)
		clearBitrateLimit(iface5)
		os.Remove(limpConsensusStateFile)
	}
}

func readLimpEntryTime() int64 {
	data, err := os.ReadFile(limpConsensusStateFile)
	if err != nil {
		return 0
	}
	ts, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return ts
}

func setLegacyBitrates(iface24, iface5 string) {
	if iface24 != "" {
		exec.Command("iw", "dev", iface24, "set", "bitrates", "legacy-2.4", "1", "2", "5.5", "11").Run()
	}
	if iface5 != "" {
		exec.Command("iw", "dev", iface5, "set", "bitrates", "legacy-5", "6", "9", "12", "18").Run()
	}
}

func clearBitrateLimit(iface string) {
	if iface == "" {
		return
	}
	exec.Command("iw", "dev", iface, "set", "bitrates").Run()
}
