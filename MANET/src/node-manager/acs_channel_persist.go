package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// lastElectedChannelsFile persists the last frequency (per band) this node
// actually elected and ran as a real data channel — never a lobby/limp
// fallback, never just a config-parse artifact. It exists solely to seed
// electBand's cold-start incumbent-bias tiebreak (biasFreq,
// channel_election.go) across a reboot: mesh-boot-lobby.service resets the
// live wpa_supplicant conf's frequency= to the lobby value before
// wpa_supplicant even starts, so getConfFreq can't tell node-manager
// anything useful about "the channel this node was actually on" during the
// first ACS tick after a cold boot. See ACS.md's "cold-boot 5GHz election
// races ahead of peer gossip" section for the incident this fixes.
const lastElectedChannelsFile = "/var/lib/mesh_acs_last_channels"

// readLastElectedFreq returns the last frequency this node persisted for
// band ("2.4GHz" or "5GHz"), or "" if there is none — covering a missing
// file, an empty file, and an unparseable value identically. writeStateFile
// does tmp-write-then-rename with no fsync, so a brownout mid-write can
// leave a zero-length file on this hardware; that's a documented failure
// mode here, not an edge case to special-case or panic on.
//
// HARD INVARIANT: the value this returns must NEVER be passed to
// setIfaceFrequency or rewriteFrequencyLine, and must NEVER be written into
// a wpa_supplicant conf file directly, by this function or any caller. It
// exists solely to feed electBand's cold-start biasFreq comparator. That
// comparator only ever compares this value against a live, phy-filtered
// candidate list (activeBand5Channels/band24Channels) — a stale or
// cross-regulatory-domain value simply matches nothing there and is
// silently ignored, degrading to today's unbiased behavior. Do not let a
// future "restore last channel at boot" shortcut bypass that comparison and
// write this value straight into a conf — that would reintroduce a
// regulatory-domain violation path (e.g. an EU node restoring a persisted
// UNII-3 frequency).
func readLastElectedFreq(band string) string {
	key := "LAST_FREQ_2_4"
	if band == "5GHz" {
		key = "LAST_FREQ_5_0"
	}

	data, err := os.ReadFile(lastElectedChannelsFile)
	if err != nil {
		log.Printf("[acs] %s: no persisted last-elected channel (%v), cold-start bias falls back to live conf", band, err)
		return ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			break
		}
		if _, err := strconv.Atoi(v); err != nil {
			log.Printf("[acs] %s: persisted %s value %q unparseable, cold-start bias falls back to live conf", band, key, v)
			return ""
		}
		return v
	}

	log.Printf("[acs] %s: no persisted last-elected channel, cold-start bias falls back to live conf", band)
	return ""
}

// selectBiasFreq picks electBand's cold-start incumbent-bias input for band:
// the live-conf frequency when it's actually a member of candidates
// (today's existing, common-case behavior — steady state, or any tick where
// mesh-boot-lobby.service hasn't just reset the conf to the lobby
// frequency); otherwise the persisted last-elected value for band if one
// exists (the lobby-reset cold-start case this fix targets); otherwise the
// live-conf value anyway (a true first-ever boot with nothing to fall back
// on — unbiased-toward-nothing, same as today, which is correct since
// there's nothing better to bias toward).
func selectBiasFreq(confFreq string, candidates []int, band string) string {
	if confCh, err := strconv.Atoi(confFreq); err == nil {
		for _, c := range candidates {
			if c == confCh {
				return confFreq
			}
		}
	}
	if persisted := readLastElectedFreq(band); persisted != "" {
		return persisted
	}
	return confFreq
}

// maybeWriteLastElectedFreq persists freq24/freq5 as the frequencies this
// node last actually ran as real data channels. Pass "" for a band that
// shouldn't be updated this cycle (runACSTick only ever passes a non-empty
// value when quorum && !result.hold && result.winnerCh != 0 for that band —
// see the blocking-correction-2 write gate documented there); an empty
// value here leaves that band's previously persisted value untouched rather
// than clearing it.
//
// Write-on-change only: reads back what's currently on disk and skips the
// write entirely if neither band's value actually changed, matching this
// codebase's existing /var/lib write-cadence convention (mesh-manager's
// savePersistent(), mesh-registry's maybeSaveKnownNodes) rather than an
// unconditional per-tick write.
func maybeWriteLastElectedFreq(freq24, freq5 string) {
	cur24 := readLastElectedFreq("2.4GHz")
	cur5 := readLastElectedFreq("5GHz")

	next24, next5 := cur24, cur5
	if freq24 != "" {
		next24 = freq24
	}
	if freq5 != "" {
		next5 = freq5
	}

	if next24 == cur24 && next5 == cur5 {
		return
	}

	content := "LAST_FREQ_2_4=" + next24 + "\n" +
		"LAST_FREQ_5_0=" + next5 + "\n" +
		"TIMESTAMP=" + strconv.FormatInt(time.Now().Unix(), 10) + "\n"
	if err := writeStateFile(lastElectedChannelsFile, content); err != nil {
		log.Printf("[acs] write last-elected channel state: %v", err)
	}
}
