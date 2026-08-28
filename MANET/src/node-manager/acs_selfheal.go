package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file implements ACS's "verify-after-apply" self-heal — see
// docs/ACS.md, "Open issue: 5GHz primary channel doesn't reliably match
// between nodes", the design validated by manet-architect on 2026-08-27.
// It's a defense-in-depth safety net for the ways a node's live radio state
// can silently diverge from what it believes it configured (a failed join,
// a driver hiccup, a race during some other restart) that a config-file
// compare alone can never see. `noscan` (see docs/wpa-supplicant-mesh-
// noscan.md) fixes the actual root cause of the original 5GHz-mismatch bug
// this was designed against, so this is no longer the primary fix — just a
// bounded, rate-limited backstop for the remaining ways divergence can
// still happen.

// ifaceChannelRE matches `iw dev <iface> info`'s "channel N (FFFF MHz)"
// line specifically. Deliberately not a bare "MHz" substring match — the
// same output also contains lines like "width: 80 MHz" and
// "center1: 5210 MHz" that a naive substring/Contains check against a
// target frequency string could collide with (this was tourguide.go's
// waitForFrequency's actual pre-existing bug; not currently a live bug with
// today's candidate frequencies, but load-bearing the moment anything here
// drives a restart decision).
var ifaceChannelRE = regexp.MustCompile(`(?m)^\s*channel\s+\d+\s+\((\d+)\s*MHz\)`)

// readIfaceFreq is a single-shot read of iface's actual live operating
// frequency, in MHz. This is the one shared primitive both
// waitForFrequency's retry loop (tourguide.go) and acsVerifyAfterApply
// below use — factored out specifically so the decision path here doesn't
// reuse a 10s-budget poller on top of runACSTick's already-tight per-tick
// subprocess/timeout budget (setIfaceFrequency's own 5s post-restart
// sleeps plus up to 10s in performScan, against a 15s tick).
//
// ok=false covers two distinct things callers must not conflate: the radio
// having no channel line at all (it never joined — e.g. the documented
// "mesh join error=-1" case; a restart cannot fix a config-level join
// rejection, so this must never be treated as a correctable mismatch), and
// any command failure/timeout/empty output (USB adapter unplugged, driver
// reload, mesh-radio-state mid-flight) — both must be handled as "nothing
// to act on this cycle", never as evidence of a frequency mismatch.
func readIfaceFreq(iface string) (freqMHz string, ok bool) {
	if iface == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "iw", "dev", iface, "info").Output()
	if err != nil {
		return "", false
	}
	m := ifaceChannelRE.FindStringSubmatch(string(out))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// unitActiveFor reports how long unit has been continuously in systemd's
// "active" state, querying systemd directly rather than tracking restarts
// in an in-process map. An in-process map only sees this process's own
// restart paths (setIfaceFrequency, reconcile5GHzWidth) — at least six
// other things on a live node can restart wpa_supplicant@<iface>, all
// invisible to any tracker node-manager keeps itself: sae-watchdog.sh (the
// exact MESH-SAE-AUTH-BLOCKED symptom class this self-heal targets),
// manet-ctrl's apiControlWifiChannel and radio enable/disable, batman-if-
// setup.sh, radio-setup.sh, and mesh-radio-state's applyIface — which runs
// in the same 15s tick, immediately before runACSTick, from a separate
// process entirely. It doesn't matter *who* restarted the unit; querying
// systemd's own state is correct regardless of the cause.
//
// ok=false whenever the unit isn't currently "active" (activating,
// deactivating, failed, inactive) or the query itself fails/times out —
// callers must skip the divergence check entirely in that case rather than
// interpret a transitional state as a live mismatch.
func unitActiveFor(unit string) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show",
		"-p", "ActiveState", "-p", "ActiveEnterTimestampMonotonic",
		"--value", unit).Output()
	if err != nil {
		return 0, false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return 0, false
	}
	if strings.TrimSpace(lines[0]) != "active" {
		return 0, false
	}
	enterUsec, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil || enterUsec <= 0 {
		return 0, false
	}
	nowUsec, err := monotonicNowUsec()
	if err != nil || nowUsec < enterUsec {
		return 0, false
	}
	return time.Duration(nowUsec-enterUsec) * time.Microsecond, true
}

// monotonicNowUsec returns the current boot-relative monotonic time in
// microseconds, read from /proc/uptime — the same clock base systemd
// reports ActiveEnterTimestampMonotonic against (CLOCK_MONOTONIC, not wall
// clock), so the two are directly comparable regardless of NTP jumps. This
// project has hit real mesh-wide time-sync bugs before (see the
// "mesh_time_sync" fix history) — using wall-clock ActiveEnterTimestamp
// here instead would make this check vulnerable to exactly that class of
// bug. Pure stdlib (no golang.org/x/sys dependency, which this module
// doesn't otherwise use) at the cost of ~1s granularity from /proc/uptime's
// own precision — more than enough for the ~20s threshold this is used
// for.
func monotonicNowUsec() (int64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, os.ErrInvalid
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(secs * 1e6), nil
}

// acsHealMinUnitUptime is how long wpa_supplicant@<iface> must have been
// continuously "active" before the self-heal trusts a live-frequency read
// enough to act on it — a bit more than one 15s node-manager tick, so a
// restart fired by literally anything (this self-heal's own prior
// corrective restart, reconcile5GHzWidth's no-settle-delay restart,
// sae-watchdog.sh, manet-ctrl's radio API, mesh-radio-state's applyIface in
// the same tick) has had a full cycle to settle before this check trusts
// what `iw` reports.
const acsHealMinUnitUptime = 20 * time.Second

// acsHealTripThreshold bounds how many consecutive cycles a corrective
// restart can be attempted for the same iface+target divergence before the
// self-heal gives up and stops restarting (but keeps the marker updated).
// A restart tears down the mesh plink and flaps batman-adv TQ for every
// peer, not just the local node — "one restart every cycle forever" is a
// permanent recurring outage, not a bound, and a node stuck on the wrong
// channel that keeps restarting also republishes an oscillating
// DATA_CHANNEL vote that can drag peers around (see ACS.md). 3 is picked as
// "clearly more than one transient hiccup" while still giving up before a
// structurally-stuck node disrupts its peers indefinitely; the exact number
// isn't load-bearing since the breaker's job is to guarantee termination,
// not to characterize the failure precisely.
const acsHealTripThreshold = 3

// acsHealState is the ACS self-heal's per-interface circuit-breaker state:
// how many consecutive cycles a live-frequency divergence against target
// has triggered a corrective restart attempt. A new elected target resets
// this — it's a fresh situation, not a continuation of the old failure.
type acsHealState struct {
	target     string
	consecFail int
}

// acsHealStates and acsHoldStreaks are plain (unsynchronized) maps: every
// caller into this file runs from node-manager's single sequential loop
// closure (main.go's loop()), never concurrently, so no mutex is needed —
// matches this codebase's existing convention of package-level state like
// lastACSCycle (main.go).
var acsHealStates = map[string]*acsHealState{}

func acsMarkerPath(iface string) string {
	return "/var/run/mesh_acs_divergence_" + iface
}

// clearAcsDivergence removes iface's divergence marker (if any) and resets
// its circuit-breaker state — called on live-frequency recovery. Matches
// setLimpMode's (main.go) write-on-fault/remove-on-recovery pattern; a
// marker that never clears on recovery is the same failure class as this
// project's own "alfred silently down for 8 days" incident.
func clearAcsDivergence(iface string) {
	os.Remove(acsMarkerPath(iface))
	delete(acsHealStates, iface)
}

// writeAcsDivergence records iface's current mismatch to /var/run, in the
// same KEY=value shape as tourguide_state (tourguide.go) — via
// writeStateFile (registry_read.go), matching that helper's atomic
// write-then-rename, not existence-only content, so a consumer (e.g.
// manet-ctrl/collect.go, surfaced via `mesh radio-info`) can report the
// actual target/actual values rather than just "something's wrong".
func writeAcsDivergence(iface, target, actual string, consecFail int) {
	content := "IFACE=" + iface + "\n" +
		"TARGET_FREQ=" + target + "\n" +
		"ACTUAL_FREQ=" + actual + "\n" +
		"TIMESTAMP=" + strconv.FormatInt(time.Now().Unix(), 10) + "\n" +
		"CONSEC_FAIL=" + strconv.Itoa(consecFail) + "\n"
	if err := writeStateFile(acsMarkerPath(iface), content); err != nil {
		log.Printf("[acs] write divergence marker for %s: %v", iface, err)
	}
}

// acsVerifyAfterApply is the verify-after-apply self-heal's actual decision
// point. It's only meaningful to call when configChanged is false: a true
// return from setIfaceFrequency means the config didn't already match
// targetFreq, so a restart (with its own settle sleep) just happened as a
// direct result of that same call — there's nothing stale to detect this
// cycle. The gap this closes is specifically the case rewriteFrequencyLine
// (main.go) can never see on its own: config already matches, so no
// restart happens no matter how long a live divergence persists, unless
// something like this checks live radio state directly.
//
// Called from both runACSTick (ACS mode, immediately after each band's
// setIfaceFrequency call — see the ordering comment there) and
// ensureStaticIfaceChannel (static/acs=n mode) so both modes get the same
// coverage; see ACS.md point 6 for why that's a deliberate decision, not an
// accident.
func acsVerifyAfterApply(iface, targetFreq, label string, configChanged bool) {
	if iface == "" || configChanged {
		return
	}
	if !radioIfaceEnabled(iface) {
		// Operator deliberately took this radio down. setIfaceFrequency
		// already skips its own restart in this case but still returns
		// false (config already matched) — a downed radio reports no
		// channel line and would otherwise be misread as a join failure
		// worth correcting, against the operator's own intent.
		return
	}

	svc := "wpa_supplicant@" + iface + ".service"
	uptime, active := unitActiveFor(svc)
	if !active || uptime < acsHealMinUnitUptime {
		// Not continuously active long enough to trust yet — could be any
		// of the restart sources described on unitActiveFor, or simply
		// "activating"/"failed" right now. Skip the divergence check
		// entirely this cycle rather than risk a false mismatch against a
		// radio that hasn't settled.
		return
	}

	actual, ok := readIfaceFreq(iface)
	if !ok {
		// No channel line at all: the radio never joined. A restart cannot
		// fix a config-level join rejection — log + marker only, never a
		// corrective restart.
		log.Printf("[acs] %s: %s reports no live channel (unit active %s, target %s MHz) — radio never joined, not correctable by restart",
			label, iface, uptime.Round(time.Second), targetFreq)
		writeAcsDivergence(iface, targetFreq, "none", acsCurrentFailCount(iface, targetFreq))
		return
	}

	if actual == targetFreq {
		clearAcsDivergence(iface)
		return
	}

	// Real divergence. Independent guard before ever restarting on this —
	// belt-and-suspenders on top of whatever activeBand5Channels/electBand
	// (scan.go, channel_election.go) already filtered at election time:
	// confirm the target is actually legal on this phy right now. `iw`
	// commonly still lists supported-but-regdomain-forbidden frequencies
	// even when a survey entry exists, so this guard is independent of and
	// in addition to the election-time filtering, not redundant with it.
	targetInt, err := strconv.Atoi(targetFreq)
	if err != nil {
		log.Printf("[acs] %s: %s target frequency %q not parseable, skipping self-heal this cycle", label, iface, targetFreq)
		return
	}
	legal, err := freqAvailableOnPhy(iface, targetInt)
	if err != nil {
		log.Printf("[acs] %s: %s checking phy legality of %s MHz: %v — not restarting this cycle", label, iface, targetFreq, err)
		return
	}
	if !legal {
		log.Printf("[acs] %s: %s live freq %s MHz != target %s MHz, but %s MHz is not currently legal on this phy — logging only, never restarting toward an illegal target",
			label, iface, actual, targetFreq, targetFreq)
		writeAcsDivergence(iface, targetFreq, actual, acsCurrentFailCount(iface, targetFreq))
		return
	}

	st, exists := acsHealStates[iface]
	if !exists || st.target != targetFreq {
		// First divergence seen for this iface, or the elected target
		// itself changed since the last one — a fresh situation, not a
		// continuation of the old failure, so the breaker resets rather
		// than staying tripped forever against a target nobody's pursuing
		// anymore.
		st = &acsHealState{target: targetFreq}
		acsHealStates[iface] = st
	}

	if st.consecFail >= acsHealTripThreshold {
		log.Printf("[acs] %s: %s stuck at %s MHz (target %s MHz) for %d consecutive cycles — circuit breaker tripped, giving up restarts until this resolves on its own or the elected target changes",
			label, iface, actual, targetFreq, st.consecFail)
		writeAcsDivergence(iface, targetFreq, actual, st.consecFail)
		return
	}

	st.consecFail++
	log.Printf("[acs] %s: %s live freq %s MHz != target %s MHz (unit active %s) — firing corrective restart (%d/%d)",
		label, iface, actual, targetFreq, uptime.Round(time.Second), st.consecFail, acsHealTripThreshold)
	writeAcsDivergence(iface, targetFreq, actual, st.consecFail)
	restartWpaSupplicant(iface)
}

// acsCurrentFailCount reads back the current consecutive-failure count for
// iface+targetFreq (0 if none tracked, or if the target has since changed)
// — used only for the marker's CONSEC_FAIL field in the two "never restart"
// branches above (no channel line / target not legal), which intentionally
// don't touch acsHealStates themselves (they never attempt a restart, so
// there's nothing to increment), but should still report a stable count
// rather than always claiming 0 while a real breaker state exists from a
// previous cycle's legal-and-divergent reading.
func acsCurrentFailCount(iface, targetFreq string) int {
	if st, ok := acsHealStates[iface]; ok && st.target == targetFreq {
		return st.consecFail
	}
	return 0
}

// acsHoldEscalateThreshold bounds how many consecutive ACS cycles
// electBand's "hold" result (channel_election.go — zero candidates had ANY
// scan data this cycle) can persist before this escalates from the
// once-per-cycle log line electBand already has to a louder log line plus
// the same marker convention the divergence self-heal uses.
const acsHoldEscalateThreshold = 3

// acsHoldStreaks tracks, per band, how many consecutive cycles electBand
// has returned hold=true. Deliberately a separate small counter from
// acsHealStates rather than folding a "no election data" outage and a "live
// radio disagrees with what was already applied" divergence into one piece
// of shared state: they're genuinely different conditions triggered from
// different points in the tick (this one is band-level and happens whether
// or not setIfaceFrequency even runs; the divergence breaker is iface-level
// and only evaluated after setIfaceFrequency reports no config change), and
// neither condition's resolution has any bearing on the other's counter.
// They do share the same marker file *shape*/write helper convention
// below, so a consumer only needs to understand one KEY=value format.
var acsHoldStreaks = map[string]int{}

// acsTrackHold is called once per band per ACS cycle, right alongside
// acsVerifyAfterApply (see runACSTick). A hold means electBand had nothing
// to act on — there is no restart that could fix "no data", so this only
// ever logs/marks, never restarts.
func acsTrackHold(iface string, result electionResult, band string) {
	holdMarker := acsMarkerPath(iface) + "_hold"
	if !result.hold {
		if acsHoldStreaks[band] != 0 {
			delete(acsHoldStreaks, band)
			os.Remove(holdMarker)
		}
		return
	}

	acsHoldStreaks[band]++
	streak := acsHoldStreaks[band]
	if streak < acsHoldEscalateThreshold {
		return
	}

	log.Printf("[acs] %s: no scan data for any candidate for %d consecutive cycles — still holding %s MHz, this is now a persistent data outage, not a one-off",
		band, streak, result.freq)
	if iface == "" {
		return
	}
	content := "IFACE=" + iface + "\n" +
		"BAND=" + band + "\n" +
		"REASON=persistent-hold\n" +
		"HELD_FREQ=" + result.freq + "\n" +
		"TIMESTAMP=" + strconv.FormatInt(time.Now().Unix(), 10) + "\n" +
		"CONSEC_HOLD=" + strconv.Itoa(streak) + "\n"
	if err := writeStateFile(holdMarker, content); err != nil {
		log.Printf("[acs] write hold marker for %s: %v", iface, err)
	}
}
