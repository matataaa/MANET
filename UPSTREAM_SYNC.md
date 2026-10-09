# Syncing with upstream (very-srs/MANET)

This repo (`mattronix/MANET`) is a real GitHub fork of `very-srs/MANET`,
created 2026-08-11. On that same day the repo went through two structural
rewrites, both git-tracked as renames:

1. `a783d48` — "Restructure node_tools into semantic dirs" —
   `MANET/node_tools/*` → `MANET/scripts/{core,elections,network,radio,system}/*`
2. `5fa0be6` — "Restructure repo to mirror target filesystem layout" —
   `MANET/scripts/*`, `MANET/etc/*`, `MANET/systemd*`, `MANET/udev/*`,
   `MANET/networkd-dispatcher/*`, `MANET/root/*` → `MANET/rootfs/{etc,root,usr}/...`

Separately (not a rename — a rewrite, so git can't bridge it), several
Python/bash components were replaced with compiled Go services under
`MANET/src/`: the web server (`manet-ctrl`), `node-manager`, `node-update`,
`battery-reader`, `gps-reader`, `halow-mcs-summary`, `mesh-radio-state`,
`mesh-registry`, `gateway-manager`, `cot-emitter`, `mesh-voice`. Upstream
still has these as `.py`/`.sh` under `node_tools/` (upstream's voice PTT
daemon is `node_tools/mesh-voice.py`, Python/GStreamer/Lyra — architecturally
unrelated to the fork's compiled Go `mesh-voice`, so upstream voice fixes
need a manual read-and-translate, not a cherry-pick).

Because of this, `origin/main` and `upstream/main` can't be diffed directly
— paths (and for the Go pieces, languages) differ. But since both restructure
commits are proper git renames, **`git log --follow` on a current fork path
still bridges all the way back into the shared upstream history** for
anything that's still a script/config file.

## Checking a specific file

```
./check-upstream-sync.sh MANET/rootfs/usr/local/bin/radio-setup.sh
```

Walks the fork's rename history for that path, finds the newest hop that
still exists on `upstream/main`, and lists upstream commits on that path
since the fork date. Two outcomes:

- **Finds a matching upstream path** → still comparable. Read the listed
  commits, `git show <sha>` to see what changed, `git cherry-pick <sha>`
  to try applying it directly (expect normal merge conflicts where both
  sides evolved the file independently — resolve like any git conflict).
- **No historical path exists on upstream** → either introduced fork-side
  after 2026-08-11, or it's one of the Go rewrites above. No mechanical
  comparison possible — instead, manually check upstream's activity on the
  *old* path it replaced (e.g. `git log upstream/main -- MANET/node_tools/node-manager.sh`
  for `MANET/src/node-manager/`) and port relevant fixes by hand.

## Setup (once per clone)

```
git remote add upstream https://github.com/very-srs/MANET.git
git fetch upstream
```

## Last reviewed

Upstream commit reviewed up to: `90f738b` (2026-10-09) — "rock3a tested
successfully oct-2026" (release 0.567)

**Note (2026-09-26):** upstream force-pushed `main` between the 2026-09-06
pass and this one — `git fetch upstream` showed a *forced-update* in the
reflog, and every SHA referenced below from before this date (`0f9280e`,
`27b0298`, `a1c1f59`, `9824519`, etc.) is no longer an ancestor of
`upstream/main` and won't resolve there anymore. The rewrite goes all the way
back to a 2026-06-12 merge-base — the whole history since then was rebased,
not just the tip. Content-wise it's a clean rebase (verified by diffing the
old tip against the same-dated/same-message commit in the new history: only
difference was `CLAUDE.md`/`AGENTS.md` being deleted — upstream scrubbed its
own AI-agent-tooling files, alongside a broader doc-hygiene pass the
2026-09-26 section below covers). Past review conclusions in this file still
stand; just don't expect the cited SHAs to `git show` against
`upstream/main` anymore.

### 2026-08-21 pass (up to `695ca46`)

Found one actionable, mechanically-portable fix not yet in the fork:
`70dc3c6` "Fix mesh time sync and wire GPS in as a time source" fixed real
bugs also present in the fork's `provision-mesh.sh` / `ethernet-autodetect.sh`
/ `one-shot-time-sync.sh` / `radio-setup.sh` — invalid `offline` directive in
chrony-default.conf, chrony-default.conf written to the wrong path
(`/etc/chrony-default.conf` vs `/etc/chrony/chrony-default.conf`),
`one-shot-time-sync.service` never actually enabled, and
`one-shot-time-sync.sh` bursting a source chronyd was never told about.
Ported (same day, this pass). Deliberately NOT ported: the upstream commit's
GPS-as-explicit-NTP-source marker (`/var/run/mesh-ntp-gps.state`, set from
`node_tools/node-manager.sh`) — the fork's registry publisher
(`src/mesh-registry/main.go`, `serviceActive("chrony")`) works on a
different principle than upstream's state-file marker, so a GPS marker
wouldn't plug in the same way; making a GPS-disciplined node a
preferred/discoverable mesh time source needs its own design pass, not a
port. See the corrected comment above `radio-setup.sh`'s GPS/chrony section
for the current, accurate state.

Everything else in that range (the voice PTT/Lyra feature set — fork has its
own separate Go `mesh-voice`, not upstream's Python one; journald
persistence — already fixed independently in the fork's own
`journald.conf.d/manet.conf`) needed no action.

### 2026-09-01 pass (covers `695ca46..515a3b1`, ~50 commits, plus a
re-check of everything reviewed before)

Found one **live bug in the fork itself**, same class as upstream's
`b0ad1a3` "Fix the auto_update gate that no node has ever satisfied":
`auto_update` is written as `y`/`n` everywhere (`admin.go`, all three
flash-time provisioning scripts, `firstrun.sh.template`), and the Go
`node-update` service's own gate (`isAffirmative`) correctly treats `y` as
on. But three other places still test the dead condition
`grep -qi '^auto_update=1' /etc/mesh.conf`, which no writer has ever
produced:
- `MANET/rootfs/etc/networkd-dispatcher/carrier:24`
- `MANET/packaging/build-rpi5-tarball.sh:144`
- `MANET/packaging/build-x86-tarball.sh:163`

Effect: the carrier-triggered "update immediately when ethernet/internet
comes up" path has never fired on any node. Not a total auto-update
failure (the routine timer-based check still runs `node-update` normally),
just this fast-path trigger. **Fixed 2026-09-01** on branch
`fix/auto-update-carrier-hook-gate` — all three files now use
`grep -qiE '^auto_update=(y|yes|1|true)[[:space:]]*$'`.

Everything else checked in this range turned out to already be independently
covered by the fork's own Go rewrite or prior ports, not because it was
mechanically applied from these specific commits:
- `f755e26` web UI localhost/DHCP firewall — already in via the fork's own
  `e5f4442`.
- `403c968` unauthenticated radio endpoints — N/A, `mesh-status.py` doesn't
  exist here; `manet-ctrl`'s `/api/control/*` routes are already behind
  `requireAuth`.
- `9824519` EU cfg80211 regdom / fatal `sae_anti_clogging_threshold` —
  `radio-setup.sh` already has the fixed logic.
- ACS election/scan-data/solo-node fixes (`fda4e21`, `0d1a31a`, `4080ac0`) —
  the Go `node-manager` already hard-errors on empty/absent scan data and
  handles solo-isolation quorum.
- `batctl o` mean-throughput column-shift parsing (`67ee2dc`, `d234974`) —
  `mesh-registry/main.go`'s `getTQAverage` already parses it correctly.
- Gateway detection on ICMP-filtered uplinks + stale route cleanup
  (`3b467f4`) — `gateway-manager` already tolerates ICMP-only flapping and
  withdraws stale default routes.
- HaLow USB recovery udev rule (`6a483c8`) — byte-identical rule and
  companion script already present.

**Needs a closer look, not cleared either way:** the ~15-commit voice
PTT/Lyra block (`b81dc2a`..`9a3e2bc`). The fork's `mesh-voice` is an
independent Go implementation, but it has zero references to "Lyra" —
codec choice differs or the codec swap was never carried over. Whether
talker-mixing / muted-presence-beacon behavior matches wasn't verified.
Do a feature-by-feature comparison if voice quality/behavior becomes a
priority.

**Structural, not directly portable** (old `node_tools/` python/bash surface
now Go — architecture differs enough that these need a manual read-and-port
if a matching symptom shows up, not a blind cherry-pick): alfred
identity/telemetry payload split (`c601555`), the `/manage`-prefixed
config apply/ACK/rollback pipeline (`323b922`, `3f80ed0`, `2f2fb2f`),
provisioning-completion tracking (`809b658`, `9fea978`), OTA-reverts-
ACS-node-to-static protection (`3355b40`, `c1d35a2`), USB-WiFi-as-uplink
prep (`ec5346c`, not reviewed in depth).

**Skipped as low value:** Windows/rpi-imager fixes, the removed RPi5
release workflow (RPi5 is a later-stage target per project priorities),
decorative UI tweaks, and ~12 version-bump-only commits.

### 2026-09-06 pass (covers `515a3b1..0f9280e`, ~40 commits)

Found one **real hardware bug, fixed this pass**: `27b0298` "Put USB back on
the DWC2 host controller instead of the 2711 XHCI" (later corrected for a
section-detection bug by `a1c1f59`). The stock CM4 image runs USB off the
2711 built-in XHCI controller (`otg_mode=1` in `[cm4]`), which upstream found
does not provide a working USB host controller on their hardware — no root
hub, so a USB MM81xx HaLow card never enumerates, so no mesh. The fix
comments out `otg_mode=1` and inserts `dtoverlay=dwc2,dr_mode=host` in the
same `[cm4]` section (section-aware, since the stock config also carries an
identical dwc2 line under `[cm5]` that a file-wide grep would false-match).

This directly applies to the fork: `firstrun.sh.template` already detects a
USB MM81xx HaLow card via sysfs (`_halow_on_spi=0` path,
`MANET/provisioning/firstrun.sh.template:283-300`) and skips the SPI-hat
`config.txt` plumbing for it — but never had *any* CM4 DWC2/otg_mode
handling, on either side of the fork's 2026-08-11 split. `27b0298` landed
2026-08-30, inside the range the 2026-09-01 pass already covered, but wasn't
individually caught — it fell into that pass's "Windows/rpi-imager, skipped
as low value" bucket by association with the surrounding flasher-rewrite
commits, when it's actually a `firstrun.sh.template` provisioning fix
unrelated to the flasher GUI. **Ported 2026-09-06** (final, section-aware
form) directly onto `MANET/provisioning/firstrun.sh.template`, right after
the existing `[cm4]` `pcie-32bit-dma` block. Not yet hardware-verified on a
CM4 + USB MM81xx board — our fleet's HaLow boards have all been SPI/Seeed-HAT
so far (see `[[halow_spi_clock_speed_fix]]`), so this path has had no live
coverage either upstream's way or ours.

Also checked, no action needed:
- `06e462b` "Document how a node discovers claimed IP chunks via Alfred" —
  docs-only on upstream's old `node_tools/README.md`, describing the
  claimed-chunk allocation invariants (random pick, 300s stale timeout,
  persisted state overrides live registry data to avoid false-conflict
  churn). Cross-checked against the fork's Go `mesh-manager`
  (`MANET/src/mesh-manager/main.go`, `ipManager.run()` ~line 453-535): the Go
  implementation already matches this invariant exactly — a valid persisted
  chunk (`im.pValid`) is reasserted directly without consulting
  `claimed_chunks.txt` (`usePersistent` gate at line 503). Confirms the
  design is correct; no gap.
- `92ae222`/`45faf87`/`bfcbb34`/`8d60d0b` (openvlm/Lyra voice pipeline
  work, ~5 commits) — Python/GStreamer/Lyra, architecturally unrelated to
  the fork's own Go `mesh-voice` (Opus-based). `45faf87` is a real bug
  pattern worth knowing about even though it's not portable code: adapted
  loss-recovery state (frames-per-packet) was being silently reset to the
  configured default whenever the GStreamer pipeline rebuilt on a SIGHUP
  retune, and cumulative loss counters weren't reset alongside it, producing
  a bogus negative delta right after retune. Worth checking for an analogous
  "adaptive state clobbered by pipeline/session rebuild" bug in the fork's
  Go mesh-voice *if* a feature-by-feature comparison is ever prioritized
  (still an open item from the 2026-09-01 pass, not resolved this pass
  either).
- `build-cm4-tarball.sh` / `build-r3a-tarball.sh` / `build-rpi5-tarball.sh` —
  one-line `openvlm` binary packaging additions, N/A (same Lyra-voice
  architecture boundary as above).

**New idea, not implemented — needs a product decision, not a port:** the
Windows flasher gained an "additional scripts" feature (new
`provisioning/additional-scripts/` dir + GUI checks: syntax-check scripts
before running them, measure real script size instead of trusting the
directory entry, auto-correct CRLF-mangled Windows-authored scripts instead
of rejecting them, size list columns to content). The fork has no equivalent
— nothing under `MANET/provisioning/` runs arbitrary user-supplied setup
scripts during provisioning today. This is a genuinely new capability
(let an installer bundle custom node setup steps), not a bugfix, so it
wasn't ported; flag if there's a use case for user-extensible provisioning.

**Skipped as low value, consistent with prior passes:** the rest of the
Windows GUI flasher rewrite (~30 commits: DPI scaling, rpiboot visibility,
progress bars, quoting fixes, build-stamp display, settings-page defaults) —
the fork's own `windows.ps1`/`linux.sh`/`mac.sh` are independent
implementations, not upstream's rpi-imager-wrapper GUI. US-spelling and
version-bump-only commits.

### 2026-09-26 pass (24 genuinely-new commits, content-identified across the
force-push — see note above; SHAs below are the *new* upstream hashes)

**Found a real, unfixed vulnerability class in our own fork, surfaced by
upstream's `01dea5d` "Authenticate mesh admin commands":** upstream's mesh
config apply/ACK/rollback pipeline used to broadcast admin/config changes
over Alfred in plaintext with no authentication — any mesh member could
forge a config push. `01dea5d` fixes it with AES-GCM + scrypt (new
`manet_admin.py`, `manet-admin-setup.sh` installing `python3-cryptography`),
encrypting/authenticating control packets with the shared `admin_password`,
and notes older versions "included the admin password in readable config
broadcasts."

Checked our Go equivalent and **the fork has the same class of hole, not yet
fixed**: `broadcastConfigPackage` (`MANET/src/manet-ctrl/admin.go:49-58`)
JSON-marshals the config package straight to `alfred -s 70`, no signing, no
encryption. On the receive side, `fleetPollAlfred`
(`MANET/src/manet-ctrl/fleet.go:286-294`) reads `alfred -r 70`, picks the
newest-`staged_at` entry, and feeds it straight to `fleetApplyConfig`
(`fleet.go:83-229`) with **no origin check at all**. `fleetApplyConfig` will
happily write anything in `saveableKeys` (`api.go:904-923`) to
`/etc/mesh.conf` — which includes `admin_password` and `require_auth`
themselves. So today, any device that joins the batman-adv mesh (which only
needs `mesh_ssid`/`mesh_key`, not the admin password) can broadcast a forged
type-70 Alfred payload and rewrite the admin password / disable auth /
change gateway or radio config on every node. `requireAuth` only gates the
HTTP staging side (a legitimate admin's own node); it does nothing for what
other nodes accept over the mesh. **Not fixed this pass — flagging for a
decision**, since porting upstream's approach means picking a crypto/KDF
scheme and a rollout story for existing fleets (all nodes need the new
dependency before anyone can push a change, per upstream's own upgrade note).

**Other new commits, categorized:**

- `cff714e` "Stop electing the lobby when jammed, score on occupancy" —
  substantial rewrite of upstream's `channel-election.sh` ACS scoring:
  switched from a single dBm noise threshold (never actually reached in the
  field per the commit's own comment) to `occupancy% + capped noise penalty +
  mean BSS weight`, added a disqualification quorum (`DISQUALIFY_QUORUM`,
  ceil of ~34% of reporters must agree — one bad radio/connector can no
  longer take a channel away from the whole mesh), switched max→median noise
  aggregation, and replaced a per-channel `bc` shell-out with one `jq` pass
  (previous version died in the flock subshell on one malformed report).
  **Worth a real look**: our Go `node-manager`'s ACS was previously confirmed
  to hard-error on absent scan data and handle solo-isolation quorum, but
  this occupancy/quorum-based scoring is a different, more robust algorithm
  than what we ported before — and we have an open live issue
  ([[eud3_eud4_5ghz_primary_channel_mismatch]]) in the same subsystem. Not
  yet checked against our Go scoring logic.
- `253c008` "Fix chunk zero allocation tracking" — upstream's
  `node-manager.sh` used to always coerce an unallocated IPv4 chunk to `0`
  (indistinguishable from a legitimate chunk-0 allocation) when publishing
  identity; fixed to read the chunk file fresh after IP management runs and
  omit the address entirely when unallocated. Check whether the Go
  `mesh-manager`'s identity-publish path has the same 0-vs-unallocated
  ambiguity.
- `9da0b7c` "Bound IPv4 startup discovery" — new `mesh-ip-startup.py`: at
  boot, waits for `br0` link-local IPv6 + Alfred + this node's own
  identity/telemetry publish, then observes BATMAN peers for 10s, extending
  to 20s total if peers are present but haven't published identity yet,
  before allocating. A remembered chunk in `/etc/mesh_ipv4_state` is reused
  only if no peer has since claimed it. Worth checking whether the Go
  `ipManager.run()` has an equivalent bounded startup wait or can race a
  chunk claim immediately on boot before peers have had a chance to publish.
- `75fcd5d` "Fix config rollback peer detection" — 108-line rewrite of
  `mesh-config-rollback.sh` plus a 337-line new test file; peer-detection
  logic for the safety-net rollback timer was buggy. Same architecture
  boundary as the `/manage` apply/ACK/rollback pipeline noted in the
  2026-09-01 pass (structural, Python vs our Go `manet-ctrl`/`fleet.go`) —
  needs a manual read-and-port only if a matching rollback symptom shows up
  live; not blindly portable.
- `93785b0` + `4de7c29` "Drive the onboard LEDs from the recorded
  provisioning verdict" — new `manet-led-status.sh` (98 lines) + a systemd
  unit, genuinely new and small, not tied to the Python/Go rewrite boundary
  (it's a small bash script + unit, the same shape as our own
  `rootfs/usr/local/bin/*` + `rootfs/etc/systemd/system/*`). Plausibly
  portable as-is if we want boot-time LED provisioning feedback; not
  reviewed for exact GPIO/LED assumptions against our board support yet.
- `4db070b` "low level voice bug fixes" — 1174-line rewrite of upstream's
  `mesh-voice.py`. Same Python/GStreamer/Lyra vs our Go Opus `mesh-voice`
  boundary as every prior pass — not portable, no action, consistent with
  the still-open "worth a feature-by-feature comparison someday" item from
  the 2026-09-01/09-06 passes.

**Skipped as low value, consistent with prior passes:** the rest of this
range is the Windows/Linux flasher continuing its rewrite (renamed to
`flash-a-radio.sh`, self-bootstrapping, cached templates, "additional
scripts" syntax-checking) and a large documentation restructuring — `README`
rewrites "for users," a new `docs/node-tools-internals.md` /
`docs/provisioning-internals.md` split, "prose tells" and "conversational
voice" removed, US spelling, and `packaging/` dropped from git tracking
entirely as "developer tooling." None of this is fork-relevant; our
`provisioning/`, `packaging/`, and doc layout are independent and already
serve the same purpose.

### 2026-10-01 pass (covers `1d66e0b..299757f`, releases 0.550–0.556)

Fast-forward, no force-push. Commit bodies are empty, so upstream's
`docs/node-tools-internals.md` diff served as the changelog. Four real gaps
in the fork, all since handled:
1. `manet-ctrl` session cookie was a deterministic hash of the password
   (no expiry, no logout, no rate limit). Fixed: mattronix/MANET#43.
2. No HTTP body/header limits. Fixed: mattronix/MANET#44.
3. `ethernet-autodetect.sh` treated "no DHCP lease in 20s" as a wired EUD,
   bridging the mesh into a foreign LAN with rogue DHCP. Superseded by the
   Ethernet port ownership redesign (mattronix/MANET#49).
4. `applyWPAConfig` expanded `$` in SSID/key replacements. Fixed:
   mattronix/MANET#42.

Found alongside: tcp/443 UI reachable mesh-wide (fixed, `ui_uplink_access`)
and `/api/peer` as an open proxy (fixed, mattronix/MANET#48).

### 2026-10-03 pass (covers `299757f..fcd02c5`, releases 0.557–0.559)

Fast-forward, no force-push. Three gaps in the fork:
1. **Secrets in trace logs (fixed, branch `fix/trace-secrets-sae-watchdog`).**
   `radio-setup.sh` traces (`set -x`) into `/var/log/radio-setup.log`; its
   `mesh.conf` loop exported every value under trace (mesh key, admin and
   user passwords, LAN AP key) and it echoed the SAE key outright.
   `firstrun.sh.template` traced the radio password and the `mesh.conf`
   writes into `/boot/firmware/{firstrun,provision}.log`, readable by anyone
   holding the card. Same fix as upstream 0.558: tracing off around those
   blocks.
2. **`sae-watchdog.sh` restart loop (fixed, same branch).** With no enabled
   mesh interface it ran `exit 0`; `Restart=always` + `RestartSec=5` reran it
   every 5 s and each start pulled in `batman-enslave` (`Wants=`). Now waits
   and rechecks every 15 s, as upstream 0.558 does.
3. **Gateway choice follows batman's pick (open, needs design).**
   `gateway-manager`'s `pollClient` routes to batman's `*` gateway, which
   switches on one reading at a 5 Mbit/s margin, never falls back when that
   gateway stops answering, and sees every gateway at the default 10/2
   unless `gateway_bandwidth` is set. Upstream 0.559 scores
   min(path throughput, announced bandwidth), switches only for ≥1.5× and
   +2 Mbit/s sustained 60 s with a 5-minute hold, fails over after two
   missed pings, and measures Ethernet uplinks with a 5 MB HTTPS download
   (which also rejects captive portals). Worth porting to the Go
   gateway-manager, but as its own feature.

**Already covered or N/A:** EU HaLow 1 MHz only (fork's `config.js`
already); runtime region apply (fork's own PR #50); `node-manager.sh`
symlink selector (fork's Go `node-manager` re-reads `acs`); the shared
`mesh-service-election.py` (fork has no MediaMTX/Mumble elections); 30 dBm
mesh power (relies on upstream's kernel 6.18 driver ceiling removal);
Python→jq cleanups; flasher automount hold (desktop-only, low priority).

### 2026-10-09 pass (covers `fcd02c5..90f738b`, releases 0.560–0.567)

Fast-forward, no force-push. 37 commits; most of the volume is new upstream
features (ATAK phone service, ranging/positioning, MT7916 timing patches and
firmware tooling, GPS reader rewrite).

**Ported:**
1. **Radio power on the PHY (mattronix/MANET#68).** Upstream 956232b: mt76
   honours only `iw phy <phy> set txpower`. Verified on EUD4 that morse
   (USB and SPI) ignores `iw dev` too, so our UI/API power, the boot unit
   and `halow_txpower_dbm` had never applied. Adopted upstream's model:
   request 30 dBm per PHY on every mesh radio, AP and shared-PHY guarded;
   `halow_txpower_dbm` and the generated `halow-txpower-*` units removed.
   Measured: HaLow unchanged (firmware limits SPI 27 / USB 24 dBm); MT7916
   runs at its EEPROM per-rate targets (2.4 GHz OFDM 16 dBm, 5 GHz 22 dBm)
   whatever is requested above that.
2. **Discovery isolation at bat0 (mattronix/MANET#69).** Upstream 70aaf19's
   `dhcp-isolation.nft` unchanged. The fork's `mdns-isolate.service` had
   never been enabled, so EUD mDNS/SSDP/LLMNR crossed the mesh (verified
   5/5/5 before, 0/0/0 after).

**Rejected:** 031e6b8 (`ConditionPathExists=/dev/i2c-1` on battery-reader):
the fork's Go reader already waits for the bus, and the condition would
skip it on first boot before `i2c-dev` loads.

**Open, worth porting later:** 3cf7031 `dpkg --configure -a` and
non-interactive installs in `radio-setup.sh`; 94d6520 offline-resumable
updates and logrotate for `/var/log/*.log`; 454a75a NTS-only internet time
on the gateway.

**Skipped / N/A:** ATAK, positioning, GPS reader rewrite, MT7916 timing
patches and firmware tooling (features; note the upstream CM4 overlay from
0.563 on ships `mt7915e` with `manet_timing=1`, inert without debugfs
writes, so bench-test the next overlay bump on EUD4); 1d9e095 boot-clock
timers (Go timers are monotonic); bc42934 `/etc/hosts` only on change
(mesh-manager already does); ee355be shorter BATMAN interface waits (Morse
HaLow bring-up can exceed 10 s here); 922de07/4ca08df voice (Python
service, fork's is Go); systemd sandboxing (f098985), Rock 3A, version
bumps.

Update this line after each review pass so `git log upstream/main --oneline
<last-reviewed-sha>..upstream/main` shows only what's new.
