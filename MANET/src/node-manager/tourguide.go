package main

import (
	"log"
	"os"
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

// readTourguideState reads this node's own record of its last turn back
// from disk — deliberately not cached in memory (matching limpmode.go's
// readLimpEntryTime pattern): node-manager can restart (crash, systemd
// restart, deploy) without a reboot, and /var/run survives that. Caching
// in a package var would reset to zero on every such restart, making
// electTourguide think this node has never taken a turn and immediately
// re-electing it — the exact failure mode a persistent on-disk record
// exists to avoid. Also persisted to tourguideStateFile for mesh-registry
// to gossip (so *peers'* elections can see it too).
func readTourguideState() (lastTimestamp int64, lastRadio string) {
	data, err := os.ReadFile(tourguideStateFile)
	if err != nil {
		return 0, ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "LAST_TOURGUIDE_TIME":
			lastTimestamp, _ = strconv.ParseInt(v, 10, 64)
		case "LAST_TOURGUIDE_RADIO":
			lastRadio = v
		}
	}
	return lastTimestamp, lastRadio
}

// maybeRunTourguide elects a tourguide for this round and, if it's us, hops
// one radio to the lobby channel to look for a foreign partition and merge
// with it if one's found and it's the larger side. Called once per ACS
// cycle (see runACSTick) — upstream runs this on its own 2-minute window
// independent of the 3-minute channel-election window, but folding it into
// the same cycle here is a reasonable simplification: this fork doesn't
// have upstream's separate lobby/data *state machine*, just a per-cycle
// election, so there's no separate clock to synchronize against.
func maybeRunTourguide(registry map[string]map[string]string, selfMAC, iface24, iface5 string, mySize int) {
	lastTS, _ := readTourguideState()

	winner := electTourguide(registry, selfMAC, lastTS)
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
		if applyPartitionMerge(foreign, selfMAC, iface24, iface5, myCh24, myCh5, mySize) {
			if is24 {
				returnFreq = foreign.dataCh24
			} else {
				returnFreq = foreign.dataCh5
			}
		}
	}

	log.Printf("[acs] tourguide: returning %s to data channel (%s)", radio, returnFreq)
	hopFrequency(radio, confPath, returnFreq, is24, false)

	writeStateFile(tourguideStateFile,
		"LAST_TOURGUIDE_TIME="+strconv.FormatInt(time.Now().Unix(), 10)+"\n"+
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
func electTourguide(registry map[string]map[string]string, selfMAC string, selfLastTS int64) string {
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
			ts = selfLastTS
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

// directBatmanNeighbors returns colon-stripped lowercase MACs — matching
// myRegistryMAC() and the registry's own NODE_<mac>_... key format
// (mesh-registry writes keys via the same strip). batctl's own output is
// colon-separated; normalizing here means every caller (electTourguide's
// registry/service-host lookups) can compare directly without each having
// to remember to strip separators itself.
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
		mac := strings.ReplaceAll(strings.ToLower(m[1]), ":", "")
		if !seen[mac] {
			seen[mac] = true
			macs = append(macs, mac)
		}
	}
	return macs
}

// selectTourguideRadio picks which radio hops to the lobby this round.
// Derived from a global window index over wall-clock time — (now /
// acsCycleInterval) % 2 — rather than each node's own last-used radio.
// Per-node "last radio" state can go out of phase between two healing
// partitions: one missed turn (a restart, a failed hop, a future
// service-hosting exclusion) and each side's tourguide starts alternating
// to the opposite band from the other, so their lobby dwells never
// overlap on the same band again and partition healing silently stops
// working forever. A window computed identically from the same clock on
// every node can't drift the way independent per-node state can — nodes
// disagreeing here would be a clock-sync problem, not an alternation bug.
func selectTourguideRadio(iface24, iface5 string) string {
	if (time.Now().Unix()/int64(acsCycleInterval/time.Second))%2 == 0 {
		return iface24
	}
	return iface5
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
	if !waitForFrequency(iface, targetFreq) {
		// Not escalated further here — a radio stranded off its intended
		// frequency after a failed hop (lobby-bound or the return-to-data
		// hop) is exactly the live-vs-elected divergence
		// acsVerifyAfterApply already catches on the next ACS cycle;
		// building a second escalation path here would be redundant (see
		// ACS.md's validated design, point 2).
		log.Printf("[acs] tourguide: %s did not reach %s MHz within the poll window", iface, targetFreq)
	}
	if toLobby {
		setLegacyBitrate(iface, is24)
	} else {
		clearBitrateLimit(iface)
	}
}

// waitForFrequency polls iface (via readIfaceFreq, acs_selfheal.go) until it
// reports targetFreq or the poll budget is exhausted. A thin retry loop over
// that single-shot reader rather than an independent poller — this used to
// do its own `strings.Contains(out, targetFreq+" MHz")` against the full
// `iw dev info` output, which also matches unrelated lines like
// "width: 80 MHz" or "center1: 5210 MHz" and could produce a false pass if a
// future candidate frequency happened to collide with one of those values.
// Returns whether targetFreq was actually observed, so a caller that cares
// can tell a genuine join from a timeout — hopFrequency below doesn't act on
// this today (a failed hop is left to the next ACS cycle's own
// verify-after-apply self-heal to catch, see ACS.md, rather than building a
// second escalation path here), but logs on timeout for visibility.
func waitForFrequency(iface, targetFreq string) bool {
	for i := 0; i < 20; i++ {
		if freq, ok := readIfaceFreq(iface); ok && freq == targetFreq {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

type foreignPartition struct {
	mac      string
	dataCh24 string
	dataCh5  string
	size     int
	lastSeen int64
}

// wifiChannelFreq translates a WiFi channel *number* (as gossiped in
// DATA_CHANNEL_2_4/DATA_CHANNEL_5_0 — mesh-registry's getChannel() reports
// the human-readable channel number, e.g. "6", not MHz, because manet-ctrl's
// UI displays it that way like any WiFi tool would) to the MHz frequency
// this file otherwise deals in throughout (wpa_supplicant conf, iw, our own
// candidate channel lists). Only needs to cover our own candidate channels
// — a healthy peer running the same election would never legitimately
// report a channel outside that set.
func wifiChannelFreq(channelNum string) (string, bool) {
	for _, freq := range band24Channels {
		if strconv.Itoa(wifiFreqToChannelNum(freq)) == channelNum {
			return strconv.Itoa(freq), true
		}
	}
	for _, freq := range band5Channels {
		if strconv.Itoa(wifiFreqToChannelNum(freq)) == channelNum {
			return strconv.Itoa(freq), true
		}
	}
	return "", false
}

func wifiFreqToChannelNum(freq int) int {
	if freq >= 2412 && freq <= 2472 {
		return (freq - 2407) / 5
	}
	if freq == 2484 {
		return 14
	}
	return (freq - 5000) / 5
}

// analyzeForeignPartitions looks for a registry entry whose data-channel
// pair differs from ours, is fresh, and isn't already an ACTIVE member of
// our own mesh (which would mean it's a stale pre-migration leftover, not
// a genuinely separate partition). Picks the most-recently-seen candidate
// if several exist. myCh24/myCh5 are MHz (getConfFreq); DATA_CHANNEL_2_4/
// DATA_CHANNEL_5_0 from the registry are channel numbers — translated to
// MHz here before any comparison, so both sides are the same unit. Skips
// a peer whose channel number doesn't resolve to one of our own candidate
// channels rather than risk comparing/migrating onto a bogus value.
func analyzeForeignPartitions(registry map[string]map[string]string, selfMAC, myCh24, myCh5 string) *foreignPartition {
	myConfig := myCh24 + "-" + myCh5
	now := time.Now().Unix()

	var best *foreignPartition
	for mac, fields := range registry {
		if mac == selfMAC || fields["NODE_STATE"] == "ACTIVE" {
			continue
		}
		ch24Num, ch5Num := fields["DATA_CHANNEL_2_4"], fields["DATA_CHANNEL_5_0"]
		if ch24Num == "" || ch5Num == "" {
			continue
		}
		ch24, ok24 := wifiChannelFreq(ch24Num)
		ch5, ok5 := wifiChannelFreq(ch5Num)
		if !ok24 || !ok5 {
			continue
		}
		if ch24+"-"+ch5 == myConfig {
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
// channels if it's the larger side (tie broken by lowest MAC, which stays
// put) — the smaller partition migrates to the larger one rather than the
// other way around, so a healing mesh converges on whichever side already
// has more nodes instead of thrashing. Reports whether it actually
// migrated, so the caller's tourguide-radio return hop can land on the new
// (not stale pre-merge) frequency. myCh24/myCh5 must be the pre-hop home
// channels, not read fresh here — during the tourguide's dwell window, one
// radio's conf file currently holds the lobby frequency, not its real data
// channel. mySize is likewise the caller's single per-cycle originator
// count taken before the hop, not recomputed here — measuring it now would
// undercount whatever this partition can only reach through the radio
// that's currently sitting in the lobby.
//
// The tie is broken on MAC rather than on the channel-pair string: an
// equal-size merge used to keep whichever side had the lexicographically
// lower "ch24-ch5" string, which silently biases every equal-size merge
// toward the numerically lower channel pair — a node that fled a jammed
// low channel would get pulled straight back onto it. MAC is neutral with
// respect to channel number, and the normal election re-optimizes away
// from a bad channel on the next cycle regardless of which side won here.
func applyPartitionMerge(foreign *foreignPartition, selfMAC, iface24, iface5, myCh24, myCh5 string, mySize int) bool {
	foreignConfig := foreign.dataCh24 + "-" + foreign.dataCh5

	winnerIsForeign := foreign.size > mySize || (foreign.size == mySize && foreign.mac < selfMAC)
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

func writePartitionSize(originators int) {
	writeStateFile(partitionSizeFile, strconv.Itoa(originators+1))
}
