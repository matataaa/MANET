# Automatic Channel Selection (ACS)

Decentralized, gossip-based channel selection for the 2.4GHz and 5GHz mesh
radios. Ported from upstream (`very-srs/MANET`, see "Where this came from"
below) and substantially reworked in this fork. This doc exists because the
same ground has been re-explained across several sessions — it's the single
place to check before touching ACS again.

Static-channel provisioning still exists as a fallback (`ensureStaticChannels`,
`node-manager/main.go`) — nodes with `acs=n` in `mesh.conf` permanently park on
the lobby frequencies (`lobbyFreq24`/`lobbyFreq5`) instead of running any of
what's described below.

## Where this came from

Ported from `very-srs/MANET` (the `upstream` git remote) starting 2026-08-19,
decided explicitly because upstream's version worked and this fork's static
channel assignment didn't adapt to RF conditions. Upstream implements it as
bash (`MANET/node_tools/node-manager-acs.sh`, on the old pre-restructure
`node_tools/` layout); this fork rewrote it as Go
(`MANET/src/node-manager/*.go`) — not a port of the bash itself, a
reimplementation of the same design.

**One important negative finding from this rewrite work:** upstream's mesh
network config (`node_tools/radio-setup.sh`) is **byte-for-byte identical**
to this fork's for the dual-band mesh interfaces — just `frequency=${FREQ}`,
nothing about channel width anywhere. Upstream's ACS script has zero
mentions of width/VHT either. So the channel-width gap described below
(the current open bug) is **not a regression introduced by the port** — it's
a pre-existing gap in the original design that neither codebase ever solved.
Worth knowing before assuming "we broke something that used to work."

## Architecture

Every node runs the same deterministic computation independently — there is
no coordinator, no leader election for this purpose. `runACSTick`
(`node-manager/main.go:269`) is the top-level orchestration, called every 15s
from the main loop but internally gated to only actually do ACS work once
per `acsCycleInterval` (180s, matching upstream's scan cadence) via
`lastACSCycle`.

Each tick does, in order:

1. **Scan** (`scan.go`) — `performScan` surveys each mesh radio's candidate
   channels (`band24Channels = [2437, 2462]`, `band5Channels = [5200, 5220,
   5240, 5745, 5765, 5785, 5805, 5825]`) for noise floor and BSS count.
   Lobby frequencies (2412/5180) are deliberately excluded from the
   candidate list — if an election ever landed on the lobby pair, every
   node would flip into lobby state and elections would silently stop.
2. **Publish** (`writeChannelReport`) — the scan result is written to
   `/var/run/mesh_channel_report.json`, which `mesh-registry` picks up and
   gossips to peers via alfred as the `CHANNEL_REPORT_JSON` registry field.
3. **Aggregate** (`channel_election.go`) — `collectFreshReports` merges self
   + any peer reports newer than `reportStaleAfter` (240s) from the
   registry.
4. **Elect** (`electBand`, one call per band) — see "The election algorithm"
   below.
5. **Quorum check** (`quorum.go`) — is this node meaningfully connected to
   the mesh, or should it retreat to the lobby to try to find it again?
6. **Limp mode reconcile** (`limpmode.go`) — mesh-wide consensus on whether
   RF conditions are bad enough to throttle to legacy bitrates.
7. **Tourguide** (`tourguide.go`) — if elected and quorum holds, this node's
   turn (if it's the elected tourguide) to hop to the lobby and check for a
   foreign partition to merge with.

### The election algorithm (`electBand`, `channel_election.go:177`)

For each candidate channel with a fresh aggregated report:

- **Disqualify** if any reporting node (self or peer) saw noise worse than
  `noiseDisqualifyDBM` (-70dBm).
- **Score** survivors: `rawScore = avgNoise + totalBSS*0.1` (lower is
  better — quieter and less contended wins).
- **Vote** (`peerChannelVotes`) — for each *other* active, fresh (within
  `staleNodeThreshold`) peer in the registry, read its self-reported
  `DATA_CHANNEL_2_4`/`DATA_CHANNEL_5_0` field and count it as a vote for
  that channel. Self is excluded from its own vote count.
- **Rank**: if any votes exist at all (`totalVotes > 0`), sort by vote
  count first, score as tiebreak. If literally nobody has voted yet
  (cold start), sort by score alone, with a small `incumbentBiasDB` (4.0)
  nudge toward whatever channel is already current — just enough to damp
  scan-to-scan noise jitter (~2-4dB observed), deliberately far too small
  to ever outweigh a real peer vote once one exists.
- If the winning channel's score is still worse than
  `limpModeScoreThreshold` (-60.0), give up on the band entirely and fall
  back to the lobby frequency, flagging limp mode.

This vote-first design is deliberate and was itself a fix
(`fix/acs-channel-election-convergence`, see "Incident history" below): an
earlier version's 10dB incumbent bias was swamping the ~1.4dB real
noise-score gaps between candidate channels, which meant nodes independently
"agreed" on paper but never actually converged onto the same channel in
practice.

**Where the vote data actually comes from — important, and non-obvious:**
`DATA_CHANNEL_2_4`/`DATA_CHANNEL_5_0` are NOT the node's *elected/intended*
channel — they're written by `mesh-registry`'s `getChannel()`
(`src/mesh-registry/main.go:635`), which runs `iw dev` and reports whatever
channel is **actually live** on an interface in that frequency's band range.
So the vote mechanism is fundamentally about **live-state consensus**, not
intent-consensus — a node effectively "votes" with whatever it's really
running, not what it last decided to run. This matters a lot for the open
bug described below.

### Quorum (`quorum.go`)

`quorumOK` mirrors upstream's three-scenario `quorum-checker.sh`, comparing
batman-adv's data-plane view (`uniqueBatmanOriginators` — who can I actually
reach) against alfred's gossip view (`activeAlfredCount` — who does the mesh
believe exists). The gap between the two distinguishes genuine isolation
from being a small-but-coherent partition:

- **Solo isolation** (0 batman originators, but alfred remembers >2 other
  nodes) → not OK, retreat to lobby.
- **Small functional island** (≥2 originators, but <1/3 of alfred's active
  count) → OK, keep operating independently.
- **Barely connected** (below half of alfred's active count but ≥2
  originators) → degraded-but-real, OK; below that, not OK.

### Limp mode (`limpmode.go`)

Separate from a single node's own RF picture — `limpConsensusRatio`
requires ≥50% of active registry nodes to be self-reporting
`IS_IN_LIMP_MODE=true` before the mesh-wide bitrate throttle
(`reconcileLimpMode`, forces legacy/robust bitrates on both mesh radios)
actually engages. Entry is immediate once consensus crosses the threshold;
exit requires the consensus to have held below threshold for
`limpModeMinDuration` (300s) — asymmetric on purpose, so the mesh doesn't
flap in and out of throttled mode.

### Tourguide (`tourguide.go`)

Partition-healing. `electTourguide` picks one node (deterministic scoring
over the registry) to periodically hop to the lobby frequency and listen
for a foreign partition — a separate group of nodes running ACS
independently that never found this mesh. `analyzeForeignPartitions` +
`applyPartitionMerge` handle actually merging into (or pulling in) the
larger partition if one is found. Only the elected tourguide does this; no
one else's radios get disturbed.

## Incident history

Chronological, each with what broke and what fixed it.

**2026-08-19/20 — initial port + field test.** `feat/acs-port` implemented
and live-tested on 2 nodes: independent channel election converged, WiFi
mesh link re-established automatically. Confirmed working at small scale.

**2026-08-21 — convergence bug found and fixed
(`fix/acs-channel-election-convergence`).** The original 10dB incumbent
bias swamped real noise-score gaps (~1.4dB), blocking convergence — nodes
would each individually "elect" correctly but never actually agree in
practice. Fixed with the vote-first design described above (peer consensus
overrides incumbent bias entirely once any peer has voted).

**2026-08-21 — hostapd/wpa_supplicant race on `eud=` transitions.** Not
ACS's election logic itself, but adjacent: the wired→wireless→wired
round-trip briefly re-peered then dropped 44s later because hostapd was
still bound to the interface wpa_supplicant was trying to join the mesh on.
Fixed in `manet-wlan-reconcile.sh` by having the wired-direction step stop+
disable hostapd itself rather than trusting the caller had already done so.
Re-verified live via the real API, held stable well past the old failure
window.

**2026-08-21 — batman-adv failover/recovery timing measured.** ~4-5 seconds
in both directions (BATMAN_V, OGM-interval-driven), not itself a bug, but
useful baseline data if a future regression needs a "how fast should this
normally be" reference.

**2026-08-25 — alfred silently down breaks ACS convergence entirely
(EUD3).** Not an ACS code bug, but ACS depends completely on the registry
gossip that alfred carries. With alfred `inactive` (cleanly stopped, not
crashed — see `alfred_recurring_clean_stop_bug` memory), EUD3's own
`[acs]` elections logged `votes=0` on every cycle since boot, on both
bands — it kept re-deriving its own locally-scored channel choice with zero
peer input, so it never converged onto what EUD4 had already independently
elected. `systemctl restart alfred` fixed it within a couple of 180s ACS
cycles. **This is the third time alfred has been found silently down on a
live node** (see the alfred memory for the other two) — root cause of why
it stops is still unknown, separately tracked.

**2026-08-25 — 5GHz primary-channel mismatch between EUD3 and EUD4 (OPEN,
see below).** Found while investigating why the two nodes weren't using
their fast 5GHz link after a baseline redeploy + reboot cycle.

## Open issue: 5GHz primary channel doesn't reliably match between nodes

**Symptom:** EUD3 and EUD4 both correctly elect the same channel (both
logged `elected channel 5220`/channel 44, matching votes), but EUD3's
*actual live radio* lands on channel 48 instead — same 80MHz spectrum block
(`center1: 5210MHz` on both), different primary 20MHz sub-channel. Reproduced
twice via clean `wpa_supplicant@wlan1` restarts with completely unchanged
config, so it's deterministic given the environment, not a random race.

**Root cause, traced via wpa_supplicant journal + live `iw dev wlan1 info`
on both nodes:** wpa_supplicant logs `Switch own primary and secondary
channel to get secondary channel with no Beacons from other BSSes` on
restart — its own environment-scan-based primary-channel reselection is
overriding the requested `frequency=5220`. `/etc/wpa_supplicant/
wpa_supplicant-wlan1.conf` is byte-identical on both nodes (just
`frequency=5220`, nothing else) — this isn't a config divergence between
the two nodes, it's non-deterministic driver/wpa_supplicant behavior given
an underspecified config. **Consequence:** EUD3 still detects EUD4
(`new peer notification` for EUD4's wlan1 MAC — plausible since they share
the same 80MHz occupied spectrum) but the SAE handshake then fails
(`MESH-SAE-AUTH-FAILURE`), most likely because EUD3's unicast auth frames go
out on its own (wrong) primary and never land on EUD4's receiver. Only the
2.4GHz (~73-87 Mbit/s) and HaLow (~7 Mbit/s) links carry traffic between
them as a result — down from the ~340 Mbit/s the 5GHz VHT80 link hit
earlier the same day, before this regression appeared. It does **not**
self-heal: `electBand` only re-triggers `setIfaceFrequency` when its own
elected value *changes* — once the conf file already says `frequency=5220`,
ACS considers the job done and never notices the live radio has silently
diverged, no matter how many more 180s cycles pass.

**Why nobody set channel width deliberately (see "Where this came from"
above):** it never has been, in this fork or upstream. Nothing in
`radio-setup.sh`'s mesh network block, `node-manager`'s runtime rewrite, or
any `mesh.conf` key controls mesh-radio channel width — only the AP
interface (`lan_ap_bw`) and HaLow (`halow_bw`) have that lever.
80MHz is simply whatever the mt7915e driver defaults to when nothing
constrains it.

**Why "just pin a static channel" is not what's being proposed:** channel
*width* (how many 20MHz channels get bonded together) and channel
*election* (which candidate channel wins, via the peer-consensus voting
above) are orthogonal. Pinning width doesn't touch election — ACS keeps
freely electing among candidates exactly as it does today, each one is
just narrower. This is not a revert of the convergence-fix work.

### Fix design — validated by manet-architect 2026-08-26

The first pass at this (below, "possible solutions") named `max_oper_chwidth`
as the primary lever. **That was wrong**, caught during design validation
before any code was written — recorded here rather than silently corrected,
since the reasoning matters for whoever implements this.

**1. Deterministic width pinning — corrected lever.** The
`Switch own primary and secondary channel...` log line comes from hostapd's
`ieee80211n_switch_pri_sec()` (`src/ap/hw_features.c` upstream), reached via
the **HT40 20/40 co-existence scan**, which wpa_supplicant's mesh mode pulls
in through the same hostapd AP code `mesh.c` links against. That path is
gated on `iface->conf->noscan`, **not** on VHT width — so `max_oper_chwidth`
alone constrains the VHT field but doesn't stop the scan that's actually
doing the swap. The real fix, in priority order:
- `noscan=1` — stops the coex scan (the actual fix)
- `ht40=1` — explicit secondary-channel offset instead of scan-derived
- `max_oper_chwidth=1` — keeps VHT80 explicit, makes the seg0 computation
  deterministic once the scan is out of the way (still worth keeping)

**Do not use `disable_ht40`/`disable_vht`** — those "fix" the mismatch by
giving up the 80MHz link entirely (trading ~340 Mbit/s for roughly half or
less), which is a regression, not a fix. `fixed_freq` is IBSS-oriented and
doesn't stop the coex scan either.

**Mandatory gate before writing any code:** confirm `noscan` is actually
present in the deployed wpa_supplicant binary (`strings $(command -v
wpa_supplicant) | grep -x -E 'noscan|ht40|max_oper_chwidth'`) — the
original `strings` pass that found `max_oper_chwidth`/`disable_ht40`/etc.
was searching for hostapd-style keys and never checked for `noscan`
specifically. If it's not there, this whole approach needs to be
reconsidered before touching EUD3/EUD4 again.

**2026-08-26 — gate run, FAILED. Implementation did not proceed.** Ran the
gate command on EUD4 (`192.168.1.183`, the only directly-reachable node)
before writing any code:

```
strings $(command -v wpa_supplicant) | grep -x -E 'noscan|ht40|max_oper_chwidth'
# => max_oper_chwidth
```

Only `max_oper_chwidth` matched. Follow-up (non-anchored) greps to rule out
a quoting fluke in the gate command itself:

- `strings ... | grep -i noscan` → **zero matches**, not even as a
  substring anywhere in the binary.
- `strings ... | grep -i ht40` → matches exist, but every one is
  `p2p_go_ht40`, `disable_ht40`, `ht40_intolerant`, or hostapd/ACS log
  strings (`HT40: control channel...`, `nl80211: ACS Params: ... HT40:
  %d ...`) — there is no plain `ht40=%d`/standalone `ht40` network-block
  config key exposed anywhere. The one bare `" ht40"` string found is part
  of a longer flag name, not an assignable key.
- `strings ... | grep -i chwidth` → only `max_oper_chwidth` and
  `vht_oper_chwidth`, both VHT-width fields, neither one a scan-suppression
  lever.

Binary identity: `/usr/sbin/wpa_supplicant`, `wpa_supplicant v2.10`
(2003-2022 build), confirmed present via `command -v` (not a stale PATH
hit).

**Conclusion: the core premise of the "Fix design — validated by
manet-architect 2026-08-26" section above does not hold on this fleet's
actual wpa_supplicant build.** `noscan` is not a recognized config key in
this binary at all — it isn't that the coex-scan-suppression *behavior* is
absent, the *config key* itself was never compiled in. `ht40=1` is
similarly not available as a plain mesh/AP network-block key on this
build. Only `max_oper_chwidth` — the lever the design doc already
identified as *not* actually stopping the coex scan — is present.
Implementation (radio-setup.sh/manet-wlan-reconcile.sh template changes,
`ensureMeshConfDefaults`, `setIfaceFrequency` verify-after-apply,
`waitForFrequency` fix, `scanIface` EU-domain fix) was **not started** as a
result — per this doc's own gate instructions, those all depend on
`noscan` actually working, and none of them were written.

**What needs to happen next, before this can be re-attempted:**
- Determine why `noscan` isn't compiled in — check the OpenWrt/buildroot
  `.config` used to build this `wpa_supplicant` (likely
  `CONFIG_NO_SCAN_PROCESSING` or a stripped mesh/AP feature set) against
  what upstream wpa_supplicant 2.10 actually supports for `noscan` in a
  `mode=5` (mesh) `network={}` block — this repo's binary may simply have
  had that support built out, or `noscan` may never have applied to mesh
  mode in 2.10 regardless of build config and the design's premise (that
  it's gated purely on `iface->conf->noscan` reachable from `mesh.c`) needs
  re-verification against the actual 2.10 source, not just the changelog
  reasoning that produced this design.
- If `noscan` truly isn't available on this build, the width-pinning
  approach needs a different lever than the one in "Fix design" above —
  this has NOT been researched yet, don't assume `max_oper_chwidth` alone
  is good enough just because it's what's left; the design doc already
  explains why that alone doesn't stop the coex scan.
- Rebuilding/patching wpa_supplicant with `noscan` support (if feasible
  given this fleet's toolchain) is one option but hasn't been scoped —
  would need to confirm this doesn't regress `MANET/binaries_arm64/`'s
  other consumers of the same prebuilt binary.
- The parts of this design that are independent of `noscan`'s availability
  (the EU-domain `scanIface` fake-noise-floor fix, `waitForFrequency`'s
  string-match bug) are still valid problems on their own, just not the
  reason this task was opened — worth doing separately if wanted, but they
  don't fix the primary/secondary channel mismatch by themselves.

**2026-08-26, later same day — root cause fully traced against real
wpa_supplicant/hostap source, question above answered.** Downloaded and
grepped the actual source directly (`src/ap/hw_features.c`,
`wpa_supplicant/mesh.c`, `wpa_supplicant/config.c`,
`wpa_supplicant/config_ssid.h` — read myself, not summarized) rather than
continuing to guess. Conclusive findings:

- The scan-and-switch behavior lives in `ieee80211n_check_40mhz()`
  (`src/ap/hw_features.c`): `if (!iface->conf->secondary_channel ||
  iface->conf->no_pri_sec_switch || iface->conf->noscan) return 0;` — so
  `noscan` genuinely is a real gate, exactly as originally claimed. But
  it's a field on `struct hostapd_config` (`iface->conf`) — hostapd's
  **AP-side** config struct — not a field on `struct wpa_ssid`, which is
  what a wpa_supplicant.conf `network={}` block actually populates. This
  is the exact same category of mistake as the original `vht_oper_chwidth`
  confusion, one layer deeper: a real, correctly-identified C struct
  field that is simply not reachable from wpa_supplicant.conf text at all.
- Confirmed via `config.c`'s `ssid_fields[]` table (the actual key-to-field
  parser mapping) that neither `noscan` nor a bare `ht40` exist as
  parseable network-block keys anywhere in wpa_supplicant, in any
  version — not stripped from this build, never existed as text-config
  options in the first place. (`disable_ht40`, `disable_vht`,
  `max_oper_chwidth` are real and do exist — confirmed both in source and
  via `strings` on the live binary — they just don't touch this code
  path.)
- Confirmed via direct grep of `mesh.c`'s full source: **it never sets
  `conf->noscan` or `conf->no_pri_sec_switch` from any `wpa_ssid` field,
  anywhere.** `mesh.c` builds its own internal `struct hostapd_config` via
  `hostapd_config_defaults()` for each mesh interface (there's no real
  hostapd.conf file involved for mesh at all), and both fields are left at
  their zero-initialized default — meaning the scan-based primary/
  secondary reselection is unconditionally active for every 40MHz+-wide
  wpa_supplicant mesh interface, with **no way to configure it off via
  wpa_supplicant.conf, in any version.** This is a genuine, unconditional
  gap in wpa_supplicant's own mesh implementation, not a fleet-specific
  build issue and not something more `strings`-searching would ever have
  found.

**What this means for the fix:** every variant of "add a config key"
(`noscan`, `ht40`, `max_oper_chwidth`, or any combination) was chasing a
lever that structurally cannot exist for mesh mode in mainline
wpa_supplicant. Three real options remain, none yet attempted:

1. **Patch wpa_supplicant/mesh.c itself** to set `conf->noscan = 1`
   unconditionally (or wire a new toggle through from `ssid`), and build/
   vendor a custom binary for this fleet — the same category of solution
   that HaLow already required (`MANET/binaries_arm64/wpa_supplicant_s1g`
   exists specifically because mainline wpa_supplicant has zero 802.11ah
   support at all; this would be the same pattern applied to a smaller,
   more contained gap in mesh mode). Real work: a toolchain to build
   arm64 wpa_supplicant, a patch, and ongoing maintenance of a fork.
2. **Work around it post-hoc, outside wpa_supplicant entirely** — after
   `wpa_supplicant@wlan1` starts and joins the mesh, issue a direct `iw
   dev wlan1 set channel <N> <width>` (or equivalent netlink call) from
   `node-manager` to force the actual radio channel, overriding whatever
   primary wpa_supplicant's internal scan picked. Doesn't fix
   wpa_supplicant's own internal bookkeeping/beacon IEs, so needs
   verification that this doesn't create a mismatch between what
   wpa_supplicant *believes* it's running and what the radio actually
   does — untested, but far less work than (1) and fits naturally as an
   extension of the verify-after-apply piece of this design (same
   `setIfaceFrequency` call site could issue the correction directly
   instead of just detecting the mismatch).
3. **Accept 20MHz-only mesh links** via `disable_ht40=1` (the one lever
   that's both real and confirmed present in the deployed binary) — this
   is the option explicitly rejected earlier in this doc for trading away
   the ~340 Mbit/s VHT80 link. Re-surfaced here as the only *zero-new-code*
   option now that (1) and (2) are both known to be real engineering
   effort — worth the user explicitly weighing the tradeoff now that the
   alternatives' actual cost is known, rather than assuming a config-only
   fix was always going to be available.

**2026-08-26, later still — option 3 live-benchmarked on EUD3/EUD4.**
`disable_ht40=1` alone **broke mesh join entirely** on both nodes
(`wlan1: mesh join error=-1`) — VHT80 is built from HT40 segments, so
disabling HT40 while VHT capability stays enabled is an inconsistent
state the driver rejects outright, not a graceful step-down. Adding
`disable_vht=1` alongside it fixed this: both nodes then joined cleanly
(`mesh plink established` within ~2s) and landed on **identical**
`channel 44, width: 20 MHz` — deterministic, unlike the 80MHz case. Real
iperf3 numbers, same link, same session, for a fair comparison:

| Width | Throughput (TCP, 10s) | Retransmits |
|---|---|---|
| 80MHz (VHT80, current default) | 505 Mbit/s | 24 |
| 20MHz (`disable_ht40=1` + `disable_vht=1`) | 144 Mbit/s (142 receiver-side) | 0 |

~28-29% of the 80MHz throughput (close to the naive 1/4-bandwidth
expectation) but zero retransmits over the full window vs. 24 at 80MHz —
some support for the narrower-channel-is-more-reliable theory, though
this is one 10s sample on one link, not a proper characterization. Both
nodes were restored to their original (unpatched, VHT80-default) config
immediately after this test — this was a benchmark to inform the decision
below, not a deployed fix.

**Correction to the option-3 lever named above:** `disable_ht40=1` alone
is **not sufficient and breaks the mesh** — it requires `disable_vht=1`
as well. Both are real, confirmed-present, unconditional config keys (not
gated behind a build flag either — `CONFIG_HT_OVERRIDES`/
`CONFIG_VHT_OVERRIDES` are both compiled into this fleet's binary, per the
successful test above).

None of these have been implemented or tested **as a deployed fix** (the
above was a live benchmark only, reverted immediately). This is a genuine
fork in the road, not a continuation of the previous "just add the right
key" approach — needs a decision on which direction before any more
implementation work.

## Decision: 20MHz-only 5GHz mesh (chosen 2026-08-26)

Option 3 chosen over options 1 (custom wpa_supplicant fork) and 2
(post-hoc `iw` correction). Reasoning, from the user directly: real
deployments are geographically spread out, and 5GHz is the shortest-range
of the three radios (basic RF: higher frequency = more path loss) — so
it's the first link to drop as node separation grows, not the backbone.
2.4GHz has been stable throughout this session's testing with no
reproduction of the primary-channel bug. HaLow (sub-1GHz) is the
long-range fallback by design — this repo's own
[`halow-range-calc.md`](halow-range-calc.md) documents km-scale HaLow
range vs. WiFi's much shorter reach, and explicitly states the general
principle "wider channels increase throughput but reduce range... each
3dB sensitivity loss halves the power margin, cutting range by ~30%" —
the same physics applies to 5GHz, so going 20MHz-only likely *improves*
5GHz's usable range margin, not just fixes the determinism bug. Any two
nodes close enough to use 5GHz at all will route their traffic over it in
preference to HaLow (batman-adv picks the highest-throughput path,
observed directly in this session's own `batctl o` output), which
offloads HaLow — the shared, most range-constrained resource across a
spread-out mesh. Net: reliability and a probable range improvement, in
exchange for a throughput ceiling drop on a link that's already the
least critical/most marginal one in a realistic deployment. Chosen over
options 1/2 specifically because it's a genuine root-cause fix (the
buggy code path structurally cannot run once VHT/HT40 are both off) with
no new runtime state machine, no custom binary to build/maintain, and no
standing correction loop — see the live-benchmark section above for why
options 1 and 2 both carry real ongoing cost that this doesn't.

### Implementation plan

**Scope: 5GHz mesh interface (wlan1-class) only.** Not 2.4GHz (already
stable, doesn't negotiate VHT, no change needed — same template loop,
different band, leave its block untouched). Not the AP interface
(client-facing `hostapd`, already correctly configured with `vht_oper_
chwidth`/`vht_oper_centr_freq_seg0_idx` for its own VHT80 — completely
separate config path from the mesh interfaces, untouched by this). Not
HaLow (separate `-s1g` config path entirely, own driver, own binary).

**Config keys**: `disable_ht40=1` **and** `disable_vht=1` together — the
live benchmark showed `disable_ht40` alone breaks mesh join outright
(`error=-1`), not a graceful step-down; both are required.

**Files to change:** (see "Implementation — 2026-08-26, fleet-wide toggle"
below for what actually shipped — a `mesh_5ghz_bw` mesh.conf toggle rather
than the hard-coded switch sketched here, and a two-way reconciler
instead of item 3's one-way `ensureMeshConfDefaults`.)
1. `MANET/rootfs/usr/local/bin/radio-setup.sh` — the mesh network block
   heredoc (~line 1012-1028, inside the `for WLAN in $(cat
   /var/lib/mesh_if)` loop). `FREQ` is already computed per-interface via
   `iface_mesh_freq "$WLAN"` *before* this heredoc, in plain MHz — add
   `if [[ "$FREQ" -ge 5000 ]]; then` around the two new lines so only the
   5GHz interface gets them, 2.4GHz's block is untouched by the same loop
   iteration.
2. `MANET/rootfs/usr/local/bin/manet-wlan-reconcile.sh` — the mirrored
   mesh lobby template heredoc (~line 319-335), same conditional, kept
   byte-consistent with radio-setup.sh's block (established convention
   from the earlier design work).
3. `MANET/src/node-manager/main.go` — new idempotent
   `ensureMeshConfDefaults(iface)` (same shape/reasoning as the earlier
   design's migration function, but simpler — no rate-limiting or retry
   state needed here, since a mismatch now fails loudly at join time
   rather than silently diverging): inserts the two keys into the
   `network={}` block of **both** the live `wpa_supplicant-<iface>.conf`
   **and** the `-lobby.conf` if missing, gated to 5GHz interfaces only.
   Required for the same reason as before: `mesh-boot-lobby.service`
   re-copies the lobby conf over the live conf on every boot, and
   `manet-wlan-reconcile.sh` only regenerates the lobby template when the
   conf is *missing* — so the shell-template changes alone reach new
   provisions only, not EUD1-4's existing lobby confs. This Go-side
   migration is the only path that's both idempotent and cleanly
   redeployable via a normal software update.

**What's different from the old design, now unnecessary:** no
verify-after-apply/rate-limiting logic in `setIfaceFrequency` — that
existed to catch *silent* divergence between elected and applied channel,
which was specifically the `noscan`-approach's failure mode. This
approach either joins cleanly at the deterministic target channel or
fails loudly (`mesh join error=-1`, visible in the journal, no silent
wrong-channel state possible). `waitForFrequency`'s existing string-match
bug and the EU-regulatory `scan.go` fake-noise-floor issue are still real
and still worth fixing, but are unrelated to this specific change —
optional to bundle, not required by it.

**New failure mode to watch for, not present in the old design:** a
node that fails to join the 20MHz-only mesh for an unrelated local reason
(driver quirk, RF issue) has **no fallback and no silent partial state**
— it's fully isolated on 5GHz until whatever's wrong with that node
specifically is fixed. This is arguably easier to notice (loud journal
error, node genuinely absent from `batctl o`'s wlan1 routes) than the old
bug's silent wrong-channel state, but there's no existing alert for
"wpa_supplicant is running but never joined its mesh group" — worth a
cheap health check (e.g. surfaced in the same `mesh status`/web UI radio
info view) rather than assuming journal-log visibility is enough in
practice.

### Testing checklist for this specific plan

- [ ] EUD3 + EUD4: clean join, identical channel both sides, confirmed
      across **multiple independent restarts** (not just the one live
      benchmark already done).
- [ ] **Cold reboot**, not just service restart — the determinism claim
      depends on the lobby-conf migration actually landing; a boot-time
      test is the only way to catch `mesh-boot-lobby.service` silently
      reverting it, same lesson as the earlier design.
- [ ] Migration test on nodes with pre-existing `-lobby.conf` files (the
      current fleet) via a normal software update, no re-provision.
- [ ] Longer-duration stability run (10+ minutes, several iperf3 samples,
      not the single 10s sample from tonight's benchmark) to build real
      confidence in the 144 Mbit/s number and its consistency.
- [ ] Confirm 2.4GHz and the AP-facing radio are genuinely untouched —
      diff their configs before/after, don't just assume the conditional
      is correct.
- [ ] EUD1/EUD2 (HaLow-only, no 5GHz mesh radio) — confirm the migration
      is a clean no-op for them (`meshIfaces()` should already exclude
      them from having a 5GHz interface to touch at all).
- [ ] Range/reliability claim — if practical, a real distance/obstruction
      test comparing 20MHz vs. 80MHz link margin at the edge of range,
      not just the close-range throughput number from tonight.

### Mixed-width peering — tested live, per-node toggle is not a clean option

User asked whether the old (80MHz, can mismatch) and new (20MHz-only,
deterministic) behavior could coexist as a per-node setting rather than a
fleet-wide switch. Tested directly: EUD3 set to
`disable_ht40=1`+`disable_vht=1`, EUD4 left at default 80MHz, both
restarted.

**They do peer** — `mesh plink established` on both sides, no
capability-mismatch rejection. But each side kept its *own* configured
width independently rather than negotiating down to a common one: EUD3
showed `width: 20 MHz, center1: 5220 MHz`, EUD4 simultaneously showed
`width: 80 MHz, center1: 5210 MHz` — same peering, two different
self-reported widths. They shared channel 44 as primary only because it
happens to fall inside both configurations (EUD4's 80MHz block spans
36-48, EUD3's 20MHz-only channel is 44 itself).

**Real throughput, same link, immediately after peering:** 100 Mbit/s
(98.6 Mbit/s receiver-side), zero retransmits — clean and stable, but
**worse than both-sides-20MHz (144 Mbit/s)**, despite one side running at
80MHz. The narrower side becomes the pairwise bottleneck as expected, but
the mismatch itself appears to cost something extra on top of just "as
slow as the narrower side" — not confirmed why (frame-format overhead
from the wider side, protection/RTS-CTS behavior under a capability
mismatch, and batman's own throughput estimator briefly reporting an even
lower 14.8 Mbit/s before the real number settled are all plausible
partial explanations, none confirmed).

**Conclusion: a per-node toggle is not a clean option.** It technically
works (peers, doesn't break), but: (1) it underperforms the uniform
20MHz configuration on the very link it's meant to protect, so there's no
throughput upside to justify the mixed state; (2) critically, **any node
left at 80MHz is still fully exposed to the original bug** for every one
of *its* other 80MHz peers — a per-node toggle doesn't shrink the bug's
blast radius, it just adds a third, worse-performing link type on top of
the two already measured. If this direction is pursued at all, a
fleet-wide switch (all nodes same mode, chosen deliberately, never mixed
in normal operation) is the only version of "keep both as a setting"
that's actually well-supported by what's been tested.

**2. The fix won't reach already-provisioned nodes as designed — new
finding, not in the original solutions list.** `radio-setup.sh`'s
`mesh-boot-lobby.service` copies `wpa_supplicant-wlanX-lobby.conf` over
`wpa_supplicant-wlanX.conf` on **every boot**. Any key added only to the
live `.conf` (e.g. via `rewriteFrequencyLine`) is erased on the next
reboot — this is *why* "does the fix survive a cold boot" needed to be a
separate test item, not an incidental extra check. Worse:
`manet-wlan-reconcile.sh` only regenerates the lobby template when `.conf`
is *missing*, so a software update alone does not rewrite existing
`-lobby.conf` files on the current fleet — EUD1-4 already have theirs, and
updating only the shell templates fixes new provisions, not these four
nodes. Generated units like `mesh-boot-lobby.service` are also confirmed
not tarball-redeployable (see `upstream_sync_2026-08-21_pr12` memory).
**Required:** an idempotent `ensureMeshConfDefaults(iface)` in
`node-manager` (Go, cleanly redeployable) that inserts the new keys into
the `network={}` block of *both* the live conf and the lobby conf if
they're missing, run once per loop tick per mesh iface. Cheap, no-op after
the first pass on any given node.

**3. Verify-after-apply — bounded, not a blind retry loop.** Put the check
inside `setIfaceFrequency` (`node-manager/main.go:210`), replacing the
blind `time.Sleep(5*time.Second)` with a poll — reuse `waitForFrequency`
(`tourguide.go:232`) rather than writing a second poller, but fix a real
bug in it first: it does `strings.Contains(out, targetFreq+" MHz")` against
full `iw dev info` output, which also contains lines like `width: 80 MHz`
and `center1: 5210 MHz` — parse the `channel N (FFFF MHz)` line specifically
instead, or a future candidate value could collide and produce a false
pass. Two hard constraints, both required:
- **No retry inside `setIfaceFrequency` itself.** It's called synchronously
  from `runACSTick` for both bands and from `applyPartitionMerge` inside
  tourguide's dwell window — a multi-attempt retry there can overrun the
  15s tick and the tourguide dwell timer.
- **Rate-limit the self-heal.** `setIfaceFrequency` currently returns early
  whenever the conf already matches the target — fixing that means
  deciding based on *live* radio state instead, but `ensureStaticIfaceChannel`
  calls this every 15s. A node that can never reach its requested frequency
  (e.g. the EU-regulatory case below) would otherwise restart
  `wpa_supplicant` every 15 seconds forever — strictly worse than today's
  bug. Cap it at one corrective restart per `acsCycleInterval` (180s) per
  interface, and stop + log once after N consecutive failures on the same
  target rather than looping forever.

Convention to follow: **verify and report, don't retry** — matching
`setIfaceTxPower`'s existing poll-then-report shape
(`manet-ctrl/collect.go:1176-1194`, 6×250ms poll, returns requested *and*
actual, no retry), the closest existing precedent in this codebase, even
though that function is itself separately documented as unreliable
(`tx_power_confirmation_unreliable` memory) — the *pattern* is right, that
instance's bug is a different, narrower issue (polling window too short).
On mismatch: log under the existing `[acs] ` sub-prefix, write one marker
via `writeStateFile` under `/var/run/`, matching the existing
`mesh_limp_mode`/`tourguide_state` convention — no gossip schema change,
no retry state machine.

**Tourguide needs a narrower related fix.** `hopFrequency` automatically
inherits the new keys for free (same conf file, no change needed there),
but its two failure modes aren't handled today: a failed hop *to* the
lobby produces a false "no foreign partition found" with no log line, and
a failed hop *back* to the data channel leaves the node stranded off its
own mesh's channel until the next 180s cycle. The return-hop case is worth
a bounded escalation to the full `setIfaceFrequency` restart path on
failure — the outbound lobby-hop case is lower stakes (just a wasted scan
attempt) and can be a log-only fix.

**New risk found during validation, not previously documented: a
regulatory-domain interaction that would turn verify-after-apply into a
restart-loop generator on EU nodes.** `scan.go`'s `band5Channels` hardcodes
`{5200, 5220, 5240, 5745, 5765, 5785, 5805, 5825}` — the 5745-5825 range
(UNII-3) is US-only, illegal under ETSI, and never filtered against what
the phy/regulatory domain actually allows (`radio-setup.sh`'s
`iface_supports_freq` already does this exact check elsewhere and is the
convention to reuse). Separately, `scanIface` synthesizes
`NoiseFloor = -100` for any candidate with no survey entry (e.g. an
unscannable/illegal channel) — better than any real measurement, so
`rawScore` picks it as the winner outright. On an EU node today this just
fails quietly; **once verify-after-apply exists, it becomes a genuine
restart loop** unless the rate-limit above is actually in place. Minimum
required fix alongside this work: `scanIface` should drop missing-survey
results instead of synthesizing a fake winning score, so
`aggregateChannelReports` correctly returns "no data" and skips the
candidate. Filtering `band5Channels` by phy capability up front is the
fuller fix and can land separately.

**Related future work, flagged by the user 2026-08-26, not yet
scoped/implemented:** `regulatory_domain` is currently one combined
mesh.conf key, but WiFi and HaLow hardware have different real-world
constraints — HaLow varies by chip (MM6108 is US-only, MM8108 is
US+EU-capable) while the WiFi cards can likely do both, independent of
which HaLow chip a given node has. A separate `halow_regulatory_domain`
key already exists in `radio-setup.sh` for exactly this, but has a real
bug: `uses_eu_halow_region("$REGULATORY_DOMAIN")` unconditionally forces
HaLow to `EU` whenever the general/WiFi domain is an EU country, even if
`halow_regulatory_domain` was explicitly set to `US` — there's no
hardware-capability check (MM6108 vs MM8108) at all, and no way today to
run WiFi on EU bands while keeping HaLow pinned to US. Full detail in the
`regulatory_domain_wifi_halow_split` memory. Not part of this fix's scope,
but touches the same `radio-setup.sh` regulatory-domain code the EU-domain
fix above lives in — worth doing together if either is picked up.

**Channel-flapping risk, also newly identified:** because
`DATA_CHANNEL_5_0` reports *live* state and `electBand` ranks by votes
first, a node stuck on the wrong primary **votes for its own wrong
channel**. In a two-node case (EUD3/EUD4) each side sees exactly one peer
vote, so it's a tie broken by score/channel-ascending — meaning EUD4 could
in principle get dragged onto EUD3's wrong channel rather than the other
way around, and EUD3's next restart could shift again. This is why the
mismatch marker from part (3) matters even before part (1) is field-proven:
it's the only signal that distinguishes "the mesh genuinely agreed to
move" from "one node's broken state is dragging its peers."

### Files/functions that need to change (per the validation pass)

- `node-manager/main.go` — `setIfaceFrequency` (:210, verify-after-apply +
  rate limit); new `ensureMeshConfDefaults(iface)` (migration, called from
  the loop closure per mesh iface); new package-level per-iface
  mismatch/cooldown state near `lastACSCycle`.
- `node-manager/tourguide.go` — `waitForFrequency` (:232, fix the string-
  match bug, return `bool`); `hopFrequency` (:221, propagate the result);
  the return-to-data hop (:114, escalate on failure).
- `node-manager/scan.go` — `scanIface` (:64-74, stop synthesizing `-100`
  for missing survey data); `band5Channels` (:18, at minimum comment that
  it's US-domain-only, ideally filter by phy capability).
- `rootfs/usr/local/bin/radio-setup.sh` — mesh lobby template heredoc
  (~:1012-1028), add the new keys.
- `rootfs/usr/local/bin/manet-wlan-reconcile.sh` — mesh lobby template
  heredoc (~:319-335), same keys, byte-consistent with radio-setup.sh.
- `manet-ctrl/api.go` — `apiControlWifiChannel` (~:829-873) writes
  `frequency=` and restarts without verifying, worth the same treatment
  for consistency (its sibling `setIfaceTxPower` two lines below already
  verifies-and-reports) — not required for this fix, but flagged as an
  inconsistency worth closing in the same pass.

**Explicitly out of scope, don't touch:** `mesh-boot-lobby.service` itself
(generated, not tarball-redeployable — fix via the Go migration instead);
the HaLow `-s1g` config path and `halow_regulatory_domain` handling —
`setIfaceFrequency` only ever drives `wpa_supplicant@` units, and
`meshIfaces()` already excludes HaLow.

### Open question, not yet resolved — flagging honestly rather than guessing

Live registry dump from EUD4 (2026-08-26) shows `NODE_..._DATA_CHANNEL_5_0`
populated for EUD1, which per the fleet topology docs is a HaLow-only +
onboard-WiFi-AP node with no 5GHz mesh radio at all. `getChannel()` matches
*any* interface in the 5GHz frequency range via a loose `iw dev` scan, not
specifically the mesh-point interface — so if a node's AP-mode radio is
dual-band-capable and happens to be sitting on a 5GHz frequency (e.g. from
AP operation, unrelated to mesh), it could pollute `peerChannelVotes` with
a vote that has nothing to do with actual mesh-radio consensus. This was
noticed while re-verifying the vote counts (`votes=3` in a 4-node mesh where
only 2 nodes are believed to have 5GHz mesh radios doesn't add up under a
strict one-vote-per-mesh-radio reading) but **not chased down** — could be
this AP/mesh radio conflation, could be something else about how
`registry` entries are populated. Worth resolving before trusting vote
counts precisely in future ACS work.

## What needs testing

**Before any code is written:**
- [ ] `strings` gate confirming `noscan` (not just `max_oper_chwidth`) is
      present in the deployed wpa_supplicant binary — see "Fix design"
      above. Do not proceed to implementation if it's missing.

**Once implemented:**
- [ ] EUD3 + EUD4: `iw dev wlan1 info` shows the same `channel N (FFFF MHz)`
      on both sides, matching `width`/`center1`. `batctl o` shows a wlan1
      route again, and throughput recovers toward ~340 Mbit/s — the
      pre-regression number is the pass bar, not just "a route exists."
- [ ] Cold reboot on both nodes, not just `systemctl restart
      wpa_supplicant@wlan1` — specifically to prove `mesh-boot-lobby`
      didn't strip the new keys back out.
- [ ] **Migration test**: deploy to a node that already has old
      `-lobby.conf` files (i.e. the current fleet) via a normal software
      update with no re-provision, and confirm both the live conf and the
      lobby conf actually gained the new keys.
- [ ] Negative test for verify-after-apply: force a mismatch (e.g.
      reproduce tonight's bug, or set a frequency the radio won't honor),
      confirm the log line + `/var/run/` marker appear, and confirm no
      restart storm over a 10-minute window in static mode (`acs=n`,
      ticks 4x more often than ACS) — the rate-limit is the thing most
      likely to be under-tested since it only shows up in a sustained
      failure case.
- [ ] **EU-domain regression** (`regulatory_domain=EU`): confirm ACS does
      *not* elect 5745-5825 and does not enter a restart loop. Flagged as
      the test most likely to get skipped and most likely to actually
      break a real deployment if it is.
- [ ] Tourguide round trip with the new keys: lobby hop lands on the exact
      lobby primary, and a forced failed return-hop escalates to a full
      restart instead of leaving the radio stranded off-channel.
- [ ] The `DATA_CHANNEL_5_0`/AP-radio-conflation open question (see above),
      on a node confirmed to have a dual-band-capable AP radio.
- [ ] Whether the alfred-down failure mode (separately tracked) can be
      reproduced/caught live with logging now in place
      (`fix/alfred-service-action-logging`, in progress in a different
      session as of 2026-08-26), to finally learn whether ACS-breaking
      alfred outages come from the admin API or somewhere else.
- [ ] General tourguide/partition-merge behavior hasn't been re-verified
      since the hostapd race fix and the convergence fix — worth a real
      foreign-partition-merge test now that both of those are settled.
- [ ] Full `eud=` round-trip (wired→wireless→both→auto→none→wired) was
      last fully verified 2026-08-21, before this session's baseline
      redeploy — worth re-confirming on current `release-v0.1.3` code,
      especially since `eud=wired` was re-tested on EUD3 this session and
      the hostapd-disable fix held across reboot (partial re-verification,
      not the full 5-value round trip).

## Implementation — 2026-08-26, fleet-wide toggle (`mesh_5ghz_bw`)

The plan above (`disable_ht40=1`+`disable_vht=1`, unconditional) shipped
with one refinement not in the original decision: a fleet-wide **mesh.conf
toggle**, `mesh_5ghz_bw` (values `20`/`80`, default `20` when the key is
absent), rather than a hard-coded always-on switch. Rationale: the
"Mixed-width peering" finding above already established this can never be
a *per-node* setting (any node left at 80MHz stays fully exposed to the
mismatch bug for all its 80MHz peers) — but a fleet can still legitimately
want a single, deliberate, all-nodes-at-once choice between the two widths
(e.g. rolling back to 80MHz once a real fix for the underlying
wpa_supplicant gap ships, without a re-provision). The default resolves to
`20` (safe/deterministic) whether the key is present-and-set-to-20 or
absent entirely — an existing node's unset key must not silently mean
"stay on legacy 80MHz."

**What actually shipped, file:line:**

- `MANET/rootfs/usr/local/bin/radio-setup.sh`:389-396 — reads
  `mesh_5ghz_bw` from `/etc/mesh.conf` (`grep`+`cut`, same pattern as
  `regulatory_domain`/`halow_regulatory_domain` immediately above it),
  defaults `MESH_5GHZ_BW` to `20`. The mesh network heredoc (originally
  ~1012-1028, now shifted by the added lines) builds a `WIDTH_LINES`
  string gated on `FREQ -ge 5000 && MESH_5GHZ_BW != "80"` and splices it
  into the `network={}` block before the closing brace — 2.4GHz and the
  AP interface's separate hostapd config path are untouched either way.
- `MANET/rootfs/usr/local/bin/manet-wlan-reconcile.sh`:62-67, ~330-345 —
  identical read (`grep '^mesh_5ghz_bw='`) and identical `WIDTH_LINES`
  gating in its own mesh lobby template heredoc, kept byte-consistent
  with radio-setup.sh's block per this doc's established convention.
- `MANET/src/node-manager/main.go` — new functions, all added after
  `wpaConfPath`:
  - `wpaLobbyConfPath(iface)` (:252) — path helper for the `-lobby.conf`
    companion to `wpaConfPath`.
  - `desiredMeshWidth()` (:269) — reads `mesh_5ghz_bw` via the existing
    `loadConf`, returns `"80"` only on an exact match, `"20"` otherwise
    (covers absent, empty, and any unrecognized value as safe-default).
  - `setMeshWidthKeys(path string, want20 bool) bool` (:281) — the
    two-way reconciler: walks the file's `network={}` block, adds
    `disable_ht40=1`/`disable_vht=1` before the closing brace if
    `want20` and they're missing, drops them if `!want20` and they're
    present. Returns whether it actually changed the file; a genuine
    no-op (not just "no restart") when already correct. Verified with a
    throwaway local test (add → idempotent-no-op → remove →
    idempotent-no-op → byte-exact round trip back to the original file)
    during implementation, not committed — this repo has no test suite
    convention to add it to.
  - `reconcile5GHzWidth(iface string)` (:355) — calls
    `setMeshWidthKeys` on both `wpaConfPath(iface)` and
    `wpaLobbyConfPath(iface)`; if either changed, restarts
    `wpa_supplicant@<iface>.service` once (guarded by the existing
    `radioIfaceEnabled` check, same as `setIfaceFrequency`). No
    rate-limiting/retry state — per the original plan's own reasoning,
    this only ever compares config text to a fixed target, there's no
    failure mode to back off from the way `setIfaceFrequency`'s ACS
    self-heal needs.
  - Wired into `main.go`'s `loop` closure (~line 53 area): `if _, iface5
    := meshIfaces(); iface5 != "" { reconcile5GHzWidth(iface5) }`, called
    unconditionally before the `acsEnabled` branch — both ACS and static
    mode get it, matching the plan ("it's about width, not about which
    channel ACS elects").
- `MANET/src/manet-ctrl/api.go` — `mesh_5ghz_bw` added to `saveableKeys`
  (~line 908) and `keyDescriptions` (~line 933). **No explicit
  `apiAdminSave` apply block added** — deliberate, and the one place this
  implementation deviates in shape from a literal reading of the task:
  surveying the existing apply blocks in `apiAdminSave` showed the
  pattern is "add an apply block only when something needs to happen
  *faster* than the relevant reconciler's own next pass" (e.g. `eud`
  triggers `manet-wlan-reconcile.sh` immediately because EUD mode
  changes are user-facing and visible instantly; keys like
  `admin_password`/`battery_monitor`/`require_auth` have no apply block
  at all and just ride on the next natural read). `mesh_5ghz_bw` fits the
  second category: `reconcile5GHzWidth` already runs every 15s
  regardless of ACS/static mode and does a cheap read-and-compare when
  nothing changed, so worst case a saved setting takes one loop tick
  (≤15s) to apply — not worth a special-cased immediate trigger.
- `MANET/rootfs/usr/local/bin/mesh` — `mesh_5ghz_bw` added to the `Config
  keys` help block, next to `halow_bw`.

**Deviations from the original sketch, noted per this doc's own
convention:**
- The task's suggested signature `reconcile5GHzWidth(iface, confPath)`
  wasn't used as-written — the shipped version takes just `iface` and
  derives both `wpaConfPath(iface)` and `wpaLobbyConfPath(iface)`
  internally, since the function always needs both paths together (that
  pairing is the entire point of the two-way, both-files reconcile) and
  passing one in while deriving the other from it would be redundant.
- The original plan's `ensureMeshConfDefaults(iface)` (one-way,
  insert-only migration) is superseded by `setMeshWidthKeys`/
  `reconcile5GHzWidth` (two-way) — the fleet-wide toggle requirement
  didn't exist when `ensureMeshConfDefaults` was scoped, and a one-way
  insert-only function can't support switching back to 80MHz. No
  `ensureMeshConfDefaults` function exists in the shipped code; treat
  every earlier reference to it in this doc as superseded by the above.
- No `/var/run/` marker file was added for a width mismatch, unlike the
  verify-after-apply design for `setIfaceFrequency` elsewhere in this
  doc — not needed here, since this reconcile has no "silently diverged
  and nobody notices" failure mode the way frequency drift did; it's a
  synchronous compare against mesh.conf, always eventually consistent
  within one loop tick.

**Verification status:** `go build ./...` and `go vet ./...` clean on
both `node-manager` and `manet-ctrl`; `gofmt -l` clean on the changed
node-manager files (this session found `manet-ctrl`'s `api.go` and
several sibling files already gofmt-non-canonical on `main` before this
change — pre-existing repo drift, not touched, out of scope);
`golangci-lint run` on both modules shows no new findings introduced by
this change (baseline errcheck/staticcheck findings pre-date it, confirmed
by line-number cross-check); `shellcheck` on all three touched shell
files (`radio-setup.sh`, `manet-wlan-reconcile.sh`, `mesh`) shows no new
findings (diffed against each file's pre-change shellcheck output — only
line-number shifts from added comments).

**2026-08-26, later same day — live hardware verification completed,
committed, and rolled out fleet-wide.** The implementer's live-check
couldn't reach the fleet; done directly in the follow-up session instead.
Found one real gap in the process along the way: the implementer's live
check would have needed to deploy **both** `node-manager` *and*
`manet-ctrl` — `mesh config set mesh_5ghz_bw 80` failed with `"No valid
keys"` after deploying only `node-manager`, because `saveableKeys`/
`keyDescriptions` live in `manet-ctrl`, which was still the old binary.
Cross-compiled and deployed both to EUD3, then it worked. Full round trip
confirmed on real hardware:

- [x] `mesh config set mesh_5ghz_bw 80`: both `wpa_supplicant-wlan1.conf`
      and `-lobby.conf` lost the `disable_` keys, `wpa_supplicant@wlan1`
      restarted once, `iw dev wlan1 info` confirmed `width: 80 MHz`.
- [x] `mesh config set mesh_5ghz_bw 20`: the reverse, both files gained
      the keys back, `iw dev wlan1 info` confirmed `width: 20 MHz`.
- [x] Default (key absent): confirmed independently on both EUD3 and
      EUD4 — `reconcile5GHzWidth` added the keys with zero explicit
      config, both nodes converged to `width: 20 MHz` automatically.

Committed as `8d28272` on `fix/acs-5ghz-channel-width` (5 code files +
this doc; the pre-existing unrelated `.gitignore` change on the working
tree was deliberately excluded from the commit).

**Then rolled out to the full fleet** (cross-compiled binaries + the
three shell scripts, deployed via the same atomic-swap SSH procedure used
throughout this session — not from an official release tarball, a
hand-rolled build from this branch):

- **EUD3, EUD4** (dual-band): both converged to the 20MHz default
  automatically, no manual `mesh config set` needed. Peering confirmed
  healthy post-rollout (`batctl o` showing an active wlan1 route at
  ~51 Mbit/s, consistent with the 144 Mbit/s real throughput measured
  earlier in this doc).
- **EUD1, EUD2** (HaLow + onboard WiFi, no 5GHz mesh radio — see
  `radio info` output: `wlan2` is HaLow/mesh, `wlan3` is the onboard
  brcmfmac radio in **AP role**, not mesh): user explicitly asked whether
  this change could interfere with their onboard WiFi. Verified directly,
  not just architecturally — captured `md5sum /etc/hostapd/hostapd.conf`
  before deployment on both nodes, redeployed, re-hashed: **byte-identical
  before and after on both nodes.** `node-manager`'s journal shows zero
  `[acs]` 5GHz-width log lines on either node post-deploy, confirming
  `reconcile5GHzWidth` correctly no-ops when `meshIfaces()` reports no
  5GHz interface. Onboard WiFi confirmed structurally unreachable by this
  code path, not merely assumed safe.

**Fleet state as of this rollout:** all 4 nodes running this branch's
code (ahead of `release-v0.1.3`, not yet cut as an official release) —
worth a proper release once this is considered final rather than leaving
it as a live-patched state.

## Related docs and memory

- `node-architecture.md` — general node architecture, doesn't currently
  cover ACS (this doc fills that gap).
- `VERSIONING.md` — unrelated to ACS, but referenced by the auto-update doc
  which shares this docs directory.
- Auto-memory (`/root/.claude/projects/-root-MANET/memory/`, not in this
  repo): `acs_port_in_progress`, `combined_test_branch`,
  `eud3_eud4_5ghz_primary_channel_mismatch`,
  `alfred_recurring_clean_stop_bug`, `tx_power_confirmation_unreliable` —
  session-level detail this doc summarizes but doesn't fully replace.
