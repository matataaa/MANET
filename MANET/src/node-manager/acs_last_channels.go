package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// acsLastChannelsFile persists the last genuinely-elected frequency per
// band, purely to seed electBand's totalVotes==0 comparator on a
// freshly-booted node whose wpa_supplicant conf has just been reset to the
// lobby frequency by mesh-boot-lobby.service. See docs/ACS.md's persisted
// incumbent-bias design (approved by manet-architect, 2026-08-27/28) for
// the full rationale and the two blocking corrections this implementation
// follows: the value here must never be fed into setIfaceFrequency/
// rewriteFrequencyLine or restored directly into a wpa_supplicant conf —
// it is compared only against activeBand5Channels' live phy-filtered
// candidate set inside electBand's cold-start tiebreak, where a stale or
// cross-regulatory-domain value simply matches nothing and this degrades
// to today's undirected local-noise-score behavior.
const acsLastChannelsFile = "/var/lib/mesh_acs_last_channels"

type acsLastChannels struct {
	freq24 string
	freq5  string
}

// readACSLastChannels parses acsLastChannelsFile. Missing, empty, and
// unparseable content are treated identically — a clean zero value, always
// with a log line so a permission/IO error isn't silently indistinguishable
// from "no file yet" — since writeStateFile's tmp+rename has no fsync and a
// brownout mid-write can leave a zero-length or garbage file; callers
// already fall back to today's getConfFreq-only behavior when a band's
// persisted value is empty.
func readACSLastChannels() acsLastChannels {
	var out acsLastChannels
	data, err := os.ReadFile(acsLastChannelsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[acs] failed to read persisted last-elected channels: %v", err)
		}
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(val); err != nil {
			log.Printf("[acs] persisted last-elected channels: %s=%q unparseable, ignoring", key, val)
			continue
		}
		switch key {
		case "LAST_FREQ_2_4":
			out.freq24 = val
		case "LAST_FREQ_5_0":
			out.freq5 = val
		}
	}
	return out
}

// acsBiasFreq selects the value electBand uses only as a cold-start
// (zero-vote) tiebreak — never returned as the applied frequency itself.
// Prefer the live wpa_supplicant conf value when it's already a real
// candidate: an already-converged node keeps biasing toward itself,
// unaffected by this fix. Fall back to the persisted last-known-good value
// only when the live conf isn't a real candidate — the lobby-reset
// cold-start case this fix targets.
func acsBiasFreq(currentFreq, lastFreq string, candidates []int) string {
	if freqInCandidates(currentFreq, candidates) {
		return currentFreq
	}
	return lastFreq
}

func freqInCandidates(freq string, candidates []int) bool {
	f, err := strconv.Atoi(freq)
	if err != nil {
		return false
	}
	for _, c := range candidates {
		if c == f {
			return true
		}
	}
	return false
}

// updateACSLastChannels writes any band's genuinely-elected frequency this
// cycle to acsLastChannelsFile, write-on-change only (matching this
// codebase's existing convention for /var/lib state — mesh-registry's
// maybeSaveKnownNodes, mesh-manager's savePersistent — rather than the
// unconditional-every-cycle pattern reserved for /var/run tmpfs state).
//
// "Genuinely elected" is quorum true, not a hold, and winnerCh != 0 —
// winnerCh != 0 alone isn't enough, because electBand's hold branch also
// sets winnerCh to the current (possibly still lobby) channel; without the
// explicit !hold check, a node's very first boot with no persisted file
// and a failed first scan would persist the lobby frequency itself as
// "last known good," cementing the exact deadlock this fix exists to
// escape. A band that didn't just genuinely elect keeps whatever value was
// already on disk — never reset to empty.
//
// cur is the caller's already-read snapshot (runACSTick reads it once per
// tick for the bias lookup) rather than re-read here — besides the extra
// file read, re-reading would let the write-on-change comparison drift
// from the exact snapshot this cycle's bias decision was made against.
//
// has24/has5 mean "eligible to persist this band this cycle" — the caller
// passes false not just when the iface doesn't exist, but also when the
// post-apply conf re-read didn't come back as the elected frequency, so a
// failed rewrite/restart can't get persisted as "last known good."
func updateACSLastChannels(cur acsLastChannels, quorum bool, r24, r5 electionResult, has24, has5 bool) {
	next := cur
	changed := false

	if has24 && quorum && !r24.hold && r24.winnerCh != 0 && cur.freq24 != r24.freq {
		next.freq24 = r24.freq
		changed = true
	}
	if has5 && quorum && !r5.hold && r5.winnerCh != 0 && cur.freq5 != r5.freq {
		next.freq5 = r5.freq
		changed = true
	}
	if !changed {
		return
	}

	content := "LAST_FREQ_2_4=" + next.freq24 + "\n" +
		"LAST_FREQ_5_0=" + next.freq5 + "\n" +
		"TIMESTAMP=" + strconv.FormatInt(time.Now().Unix(), 10) + "\n"
	if err := writeStateFile(acsLastChannelsFile, content); err != nil {
		log.Printf("[acs] failed to persist last-elected channels: %v", err)
	}
}
