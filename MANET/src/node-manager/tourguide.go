package main

import (
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// How long the elected tourguide dwells on the lobby channel listening
	// for a foreign partition's beacon before returning to its data
	// channel — matches upstream's fixed 12s window.
	tourguideDwell = 12 * time.Second
	// A foreign node's registry entry this stale isn't trustworthy
	// evidence of a currently-live mesh on that channel pair.
	helperStaleAfter = 300 * time.Second

	tourguideStateFile = "/var/run/tourguide_state"
	partitionSizeFile  = "/var/run/mesh_partition_size"
)

// tourguideState is this node's own record of its last turn — read back by
// electTourguide (so the rotation is fair) and persisted to
// tourguideStateFile for mesh-registry to gossip (so *peers'* elections can
// see it too, via LAST_TOURGUIDE_TIMESTAMP/LAST_TOURGUIDE_RADIO).
var tourguideState struct {
	lastTimestamp int64
	lastRadio     string
}

// maybeRunTourguide elects a tourguide for this round and, if it's us, hops
// one radio to the lobby channel to look for a foreign partition and merge
// with it if one's found and it's the larger side. Called once per ACS
// cycle (see runACSTick) — upstream runs this on its own 2-minute window
// independent of the 3-minute channel-election window, but folding it into
// the same cycle here is a reasonable simplification: this fork doesn't
// have upstream's separate lobby/data *state machine*, just a per-cycle
// election, so there's no separate clock to synchronize against.
func maybeRunTourguide(registry map[string]map[string]string, selfMAC, iface24, iface5 string) {
	winner := electTourguide(registry, selfMAC)
	if winner != selfMAC {
		return
	}
	if iface24 == "" || iface5 == "" {
		// Tourguide duty alternates radios; with only one mesh radio
		// there's nothing to alternate and no spare to hop while the
		// other keeps carrying data traffic.
		return
	}

	// Capture our real data-channel identity *before* hopping — the hop
	// below overwrites the tourguide radio's own conf file with the lobby
	// frequency, so reading it back afterward would report the lobby
	// channel as "current", not our actual data channel, corrupting the
	// partition comparison below.
	myCh24 := getConfFreq(wpaConfPath(iface24))
	myCh5 := getConfFreq(wpaConfPath(iface5))

	radio := selectTourguideRadio(iface24, iface5)
	is24 := radio == iface24
	confPath := wpaConfPath(radio)
	homeFreq, lobbyFreq := myCh5, lobbyFreq5
	if is24 {
		homeFreq, lobbyFreq = myCh24, lobbyFreq24
	}

	log.Printf("[acs] tourguide: hopping %s to lobby (%s) for %s", radio, lobbyFreq, tourguideDwell)
	hopFrequency(radio, confPath, lobbyFreq, is24, true)

	time.Sleep(tourguideDwell)

	// Default to going straight back home; a merge below overrides this
	// with the foreign (winning) channel for whichever band this radio
	// covers, so the return hop lands on the post-merge frequency instead
	// of silently reverting the merge that was just applied.
	returnFreq := homeFreq
	if foreign := analyzeForeignPartitions(readRegistry(registryFile), selfMAC, myCh24, myCh5); foreign != nil {
		if applyPartitionMerge(foreign, iface24, iface5, myCh24, myCh5) {
			if is24 {
				returnFreq = foreign.dataCh24
			} else {
				returnFreq = foreign.dataCh5
			}
		}
	}

	log.Printf("[acs] tourguide: returning %s to data channel (%s)", radio, returnFreq)
	hopFrequency(radio, confPath, returnFreq, is24, false)

	tourguideState.lastTimestamp = time.Now().Unix()
	tourguideState.lastRadio = radio
	writeStateFile(tourguideStateFile,
		"LAST_TOURGUIDE_TIME="+strconv.FormatInt(tourguideState.lastTimestamp, 10)+"\n"+
			"LAST_TOURGUIDE_RADIO="+radio+"\n")
}

// electTourguide picks who does partition-healing duty this round:
// candidates are self + direct batman neighbors, excluding anyone
// currently hosting a shared service (none exist in this fork yet, but the
// check is here so tourguide duty doesn't fall on a service host once they
// do). Winner is whoever has gone longest without a turn (oldest
// LAST_TOURGUIDE_TIMESTAMP, default 0 for a node never seen taking one),
// tie-broken by lowest MAC — a fairness rotation, not deterministic-by-MAC
// alone, so the duty (and the RF cost of hopping off data channel) spreads
// across the mesh instead of always landing on one node.
func electTourguide(registry map[string]map[string]string, selfMAC string) string {
	candidates := []string{selfMAC}
	for _, mac := range directBatmanNeighbors() {
		if mac != selfMAC {
			candidates = append(candidates, mac)
		}
	}

	type scored struct {
		mac string
		ts  int64
	}
	var eligible []scored
	for _, mac := range candidates {
		if isHostingService(registry, mac) {
			continue
		}
		var ts int64
		if mac == selfMAC {
			ts = tourguideState.lastTimestamp
		} else if fields, ok := registry[mac]; ok {
			ts, _ = strconv.ParseInt(fields["LAST_TOURGUIDE_TIMESTAMP"], 10, 64)
		}
		eligible = append(eligible, scored{mac: mac, ts: ts})
	}
	if len(eligible) == 0 {
		return selfMAC
	}

	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].ts != eligible[j].ts {
			return eligible[i].ts < eligible[j].ts
		}
		return eligible[i].mac < eligible[j].mac
	})
	return eligible[0].mac
}

func isHostingService(registry map[string]map[string]string, mac string) bool {
	fields, ok := registry[mac]
	if !ok {
		return false
	}
	return fields["IS_MEDIAMTX_SERVER"] == "true" || fields["IS_MUMBLE_SERVER"] == "true"
}

func directBatmanNeighbors() []string {
	out, err := exec.Command("/usr/sbin/batctl", "n").Output()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var macs []string
	for _, line := range strings.Split(string(out), "\n") {
		m := originatorRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mac := strings.ToLower(m[1])
		if !seen[mac] {
			seen[mac] = true
			macs = append(macs, mac)
		}
	}
	return macs
}

func selectTourguideRadio(iface24, iface5 string) string {
	switch tourguideState.lastRadio {
	case iface24:
		return iface5
	default:
		return iface24
	}
}

// hopFrequency rewrites the radio to targetFreq and nudges wpa_supplicant
// via `wpa_cli reconfigure` rather than setIfaceFrequency's full
// systemctl-restart-and-sleep path — tourguide duty is time-boxed by
// tourguideDwell (there and back, twice this cost) and needs the faster
// live-reconfigure upstream's tourguide-manager.sh itself uses for exactly
// this reason. Waits for the interface to actually land on the new
// frequency, then sets bitrates: legacy/robust rates entering the lobby
// (survives on minimal rates against whatever's there), full-rate on the
// way back to data.
func hopFrequency(iface, confPath, targetFreq string, is24, toLobby bool) {
	rewriteFrequencyLine(confPath, targetFreq, "tourguide")
	exec.Command("wpa_cli", "-i", iface, "reconfigure").Run()
	waitForFrequency(iface, targetFreq)
	if toLobby {
		if is24 {
			exec.Command("iw", "dev", iface, "set", "bitrates", "legacy-2.4", "1", "2", "5.5", "11").Run()
		} else {
			exec.Command("iw", "dev", iface, "set", "bitrates", "legacy-5", "6", "9", "12", "18").Run()
		}
	} else {
		clearBitrateLimit(iface)
	}
}

func waitForFrequency(iface, targetFreq string) {
	needle := targetFreq + " MHz"
	for i := 0; i < 20; i++ {
		out, _ := exec.Command("iw", "dev", iface, "info").Output()
		if strings.Contains(string(out), needle) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

type foreignPartition struct {
	mac      string
	dataCh24 string
	dataCh5  string
	size     int
	lastSeen int64
}

// analyzeForeignPartitions looks for a registry entry whose data-channel
// pair differs from ours, is fresh, and isn't already an ACTIVE member of
// our own mesh (which would mean it's a stale pre-migration leftover, not
// a genuinely separate partition). Picks the most-recently-seen candidate
// if several exist.
func analyzeForeignPartitions(registry map[string]map[string]string, selfMAC, myCh24, myCh5 string) *foreignPartition {
	myConfig := myCh24 + "-" + myCh5
	now := time.Now().Unix()

	var best *foreignPartition
	for mac, fields := range registry {
		if mac == selfMAC || fields["NODE_STATE"] == "ACTIVE" {
			continue
		}
		ch24, ch5 := fields["DATA_CHANNEL_2_4"], fields["DATA_CHANNEL_5_0"]
		if ch24 == "" || ch5 == "" || ch24+"-"+ch5 == myConfig {
			continue
		}
		ts, err := strconv.ParseInt(fields["LAST_SEEN_TIMESTAMP"], 10, 64)
		if err != nil || now-ts > int64(helperStaleAfter.Seconds()) {
			continue
		}
		if best == nil || ts > best.lastSeen {
			size, _ := strconv.Atoi(fields["PARTITION_SIZE"])
			best = &foreignPartition{mac: mac, dataCh24: ch24, dataCh5: ch5, size: size, lastSeen: ts}
		}
	}
	return best
}

// applyPartitionMerge migrates this node onto the foreign partition's
// channels if it's the larger side (tie broken by lexicographically lower
// channel-pair string) — the smaller partition migrates to the larger one
// rather than the other way around, so a healing mesh converges on
// whichever side already has more nodes instead of thrashing. Reports
// whether it actually migrated, so the caller's tourguide-radio return hop
// can land on the new (not stale pre-merge) frequency. myCh24/myCh5 must be
// the pre-hop home channels, not read fresh here — during the tourguide's
// dwell window, one radio's conf file currently holds the lobby frequency,
// not its real data channel.
func applyPartitionMerge(foreign *foreignPartition, iface24, iface5, myCh24, myCh5 string) bool {
	myConfig := myCh24 + "-" + myCh5
	mySize := uniqueBatmanOriginators() + 1
	foreignConfig := foreign.dataCh24 + "-" + foreign.dataCh5

	winnerIsForeign := foreign.size > mySize || (foreign.size == mySize && foreignConfig < myConfig)
	if !winnerIsForeign {
		log.Printf("[acs] partition merge: foreign partition %s on %s (size %d) smaller than ours (size %d) — staying put",
			foreign.mac, foreignConfig, foreign.size, mySize)
		return false
	}

	log.Printf("[acs] partition merge: migrating to %s from %s (foreign size %d beats ours %d)",
		foreignConfig, foreign.mac, foreign.size, mySize)
	setIfaceFrequency(iface24, wpaConfPath(iface24), foreign.dataCh24, "2.4 GHz (partition merge)")
	setIfaceFrequency(iface5, wpaConfPath(iface5), foreign.dataCh5, "5 GHz (partition merge)")
	return true
}

func writePartitionSize() {
	size := uniqueBatmanOriginators() + 1
	writeStateFile(partitionSizeFile, strconv.Itoa(size))
}
