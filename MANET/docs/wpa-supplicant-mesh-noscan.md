# wpa_supplicant mesh `noscan` patch — research + build plan

Research session: 2026-08-27. Companion to `ACS.md`'s "Open issue: 5GHz
primary channel doesn't reliably match between nodes" — that doc concluded
the coex-scan bug has **no config-level fix in mainline wpa_supplicant** and
chose 20MHz-only mesh as the safe default. This doc exists because a real,
already-written, community-maintained fix for the underlying bug was found
after that conclusion was written — read this before re-deriving any of it,
and before assuming "no fix exists" is still true.

**Nothing in this doc has been built or deployed yet.** This is findings +
a plan, not a shipped change. `mesh_5ghz_bw` stays `20`/`80`-only until this
plan is executed and tested.

## 1. Why this exists: AW7916/AW7915, and does 40MHz dodge the bug?

Asked directly: does the AsiaRF **AW7916-AED** (MT7916AN, the chip on this
fleet's 5GHz mesh radio) behave differently from its predecessor **AW7915**,
and can plain 40MHz (HT40, no VHT) sidestep the 80MHz primary-channel
mismatch bug `ACS.md` documents?

- **Hardware:** AW7916-AED is Wi-Fi 6E, MT7916AN, DBDC, max 160MHz on 5/6GHz,
  **max 40MHz on 2.4GHz** (AsiaRF's own spec page). mt76 has no separate
  "mt7916" driver — MT7916 is handled by the same `mt7915` kernel driver
  already in use on this fleet (`radio-setup.sh` references `mt7915`
  directly). AW7915→AW7916 is not expected to change any of the behavior
  below; the bug is 100% in userspace (wpa_supplicant), not the driver or
  firmware.
- **40MHz does not dodge the bug.** The nondeterministic primary/secondary
  reselection is the same code path for HT40 and VHT80 — VHT80 is built
  from two adjacent HT40 blocks. Going 40MHz-only shrinks the number of
  possible wrong primaries from 4 (an 80MHz block's four 20MHz slots) down
  to 2 (a 40MHz block's two 20MHz slots), but it's the same nondeterminism,
  not a different, safer mechanism. There is no width between 20 and 80
  that avoids this for free.

## 2. Corrected root cause — verified against actual upstream source, not paraphrase

`ACS.md`'s original trace (2026-08-26) attributed the `Switch own primary
and secondary channel to get secondary channel with no Beacons from other
BSSes` log line to hostapd's `ieee80211n_check_40mhz()`
(`src/ap/hw_features.c`), reached via `mesh.c`'s internal
`hostapd_config`. That conclusion (`noscan` cannot be wired through from
`wpa_supplicant.conf` in mainline) is still correct, but the **specific
function actually responsible for the log line and the reselection itself
is a different one**, confirmed 2026-08-27 by fetching and reading the
patch that fixes it (see §3) against real upstream source:

- `wpa_supplicant/wpa_supplicant.c`, `ibss_mesh_setup_freq()` — the actual
  mesh/IBSS frequency+HT/VHT setup function wpa_supplicant runs for a mesh
  interface. It unconditionally sets `int obss_scan = 1;` and, for 5GHz
  (`HOSTAPD_MODE_IEEE80211A`), calls `ibss_mesh_select_40mhz(...,
  obss_scan, ...)`.
- `ibss_mesh_select_40mhz()` (same file) — does the actual coexistence
  scan and picks the primary/secondary channel when `obss_scan` is true.
  This is what nondeterministically reselects the primary depending on
  which beacons it happens to hear at restart time — not
  `ieee80211n_check_40mhz()`, which is hostapd's **AP**-mode equivalent and
  is never reached by wpa_supplicant's own mesh code path at all (`mesh.c`
  never calls it — this much of the original trace was right).
- `ibss_mesh_can_use_vht()` (same file) — separately gates whether VHT is
  even considered, only relevant to the 80MHz case.

Practical effect on this fork's earlier conclusion: unchanged. Mainline
wpa_supplicant 2.10 (confirmed via the `strings` gate already run against
this fleet's real binary — see `ACS.md`) has no config key that reaches
`obss_scan` in `ibss_mesh_setup_freq()`. The fix is the same shape as
`ACS.md`'s Option 1 (patch wpa_supplicant, vendor a custom binary) — the new
finding is that the patch already exists, is small, and is maintained by
someone else.

## 3. The fix: OpenWrt's `300-noscan.patch` + `301-mesh-noscan.patch`

Archived verbatim in
`patches/wpa_supplicant-mesh-noscan/{300-noscan.patch,301-mesh-noscan.patch}`
(fetched 2026-08-27 from `openwrt/openwrt`'s `master` branch — see that
directory's `README.md` for upstream URLs and provenance). **Apply `300`
before `301`** — `301`'s `mesh.c` hunk reads `conf->noscan`, a
`struct hostapd_config` field `300` adds; without `300` first, `301` won't
even compile.

What each one actually does, read from the real diff (not an AI summary of
it — see §7 for where an AI paraphrase of this same patch nearly produced a
wrong claim in this session):

- **`300-noscan.patch`** (Felix Fietkau, 2010): adds `int noscan;` and
  `int no_ht_coex;` to `struct hostapd_config`
  (`src/ap/ap_config.h`), a `noscan=`/`ht_coex=` key to hostapd.conf
  parsing (`hostapd/config_file.c` — **hostapd's own AP-mode conf file
  only, not relevant to mesh by itself**), and gates
  `ieee80211n_check_40mhz()` (`src/ap/hw_features.c`) and two
  20/40-coexistence-frame handlers (`src/ap/ieee802_11_ht.c`) on the new
  field. This alone does nothing for mesh — mesh.c builds its
  `hostapd_config` programmatically and never parses a hostapd.conf file,
  confirmed in the earlier `ACS.md` source trace. It exists purely so `301`
  has a field to read.
- **`301-mesh-noscan.patch`** (Daniel Golle, 2018) — the actual mesh fix:
  - `wpa_supplicant/config.c`, `config_ssid.h`: adds `noscan` as a real,
    parseable field on `struct wpa_ssid` — i.e. a plain `noscan=1` line
    inside a `wpa_supplicant.conf` `network={}` block now does something,
    which is exactly the gap `ACS.md`'s source trace found didn't exist in
    any version.
  - `wpa_supplicant/mesh.c`: `if (conf->noscan) ssid->noscan = 1;` inside
    `wpa_supplicant_mesh_init()`. **Not fully traced against full source
    this session** — `conf` here is the mesh's internal
    `struct hostapd_config` (`300`'s field), and this line's exact
    purpose/necessity relative to the `wpa_supplicant.c` hunks below isn't
    100% pinned down. Don't skip it without checking — verify against a
    full source read of `mesh_config_create()` before assuming it's dead
    code, same standard `ACS.md` already holds itself to.
  - **`wpa_supplicant/wpa_supplicant.c` — this is the part that actually
    matters for the 5GHz bug**, all reading `ssid->noscan` directly
    (independent of the `mesh.c` hunk above):
    - `ibss_mesh_setup_freq()`: `int obss_scan = !(ssid->noscan);`
      (previously always `1`). This is the literal fix — the coex scan
      simply doesn't run when `noscan=1` is set on the network block.
    - `ibss_mesh_select_40mhz()`: extends the "which channels get a `+`
      HT40 offset" table to include 2.4GHz channels 1-7 — this half of the
      patch is about enabling 2.4GHz HT40, not our 5GHz problem; harmless
      to carry, not required for the 5GHz fix.
    - `ibss_mesh_can_use_vht()`: loosens a mode check to also allow VHT
      consideration when `noscan` is set — mostly relevant to non-5GHz
      cases; for our already-5GHz interface this branch was already true
      before the patch (`mode->mode == HOSTAPD_MODE_IEEE80211A`), so this
      hunk is a no-op for the 5GHz link specifically.

**Recommended config once patched**, mirroring `ACS.md`'s own
already-correct reasoning for VHT80 determinism: `noscan=1` (stops the
scan) **plus** `max_oper_chwidth=1` (keeps the VHT80 segment-0 computation
explicit instead of scan-derived) for the 80MHz case. For a 40MHz-only
case: `noscan=1` with `disable_vht=1` (keep HT40, drop VHT) — this
configuration doesn't exist as a `mesh_5ghz_bw` value today (see §6).

**Not yet verified, flag before relying on it:** whether `obss_scan=0`
alone is sufficient for **both** nodes to independently compute the
*same* primary channel (not just *a* stable one each) — i.e., whether the
fallback path `ibss_mesh_select_40mhz` takes when it skips scanning is
itself deterministic and identical given the same `frequency=` config on
both sides. This needs the live two-node test in §5, not just "it stopped
scanning" — a stable-per-node-but-still-different pick would be a smaller
bug than today's but not actually a fix.

## 4. Correction to an assumption from earlier in this research

Earlier in this session, before this doc was written, the comparison was
made that building a patched wpa_supplicant would be "the same category of
effort" as the existing `MANET/binaries_arm64/wpa_supplicant_s1g` binary
already vendored for HaLow. **That comparison is wrong and worth
correcting before scoping effort**: per `MANET/binaries_arm64/README.md`,
`wpa_supplicant_s1g` is **Morse Micro's own pre-built binary** ("compiled
from Morse Micro's halow enabled hostapd sources") — MANET vendors it
as-is, nobody on this project has actually set up a wpa_supplicant/hostap
cross-compile toolchain or carried a patch series themselves. This is
genuinely new capability for this project, not a repeat of prior work —
scope the toolchain setup in §5 as real, first-time effort, not "redo what
we did for HaLow."

## 5. Build plan

**Target:** Raspberry Pi OS Trixie (Debian 13) arm64 — confirmed the base
image (`provisioning/build-image-linux.sh`:30-32,
`raspios-trixie-arm64-lite`). Debian trixie's `wpasupplicant` package is
version **2.10-24**, source package `wpa`
(`wpa_2.10.orig.tar.xz` + `wpa_2.10-24.debian.tar.xz` —
packages.debian.org, checked 2026-08-27). This matches the `wpa_supplicant
v2.10` string the earlier `ACS.md` `strings` gate found on the actual
deployed binary — high confidence this is the exact source to patch, not a
generic upstream tag that might carry different build flags.

1. **Get matching source, not a generic tag.** On a Trixie arm64 build
   host (or a Trixie arm64 chroot/container on any host — needed either
   way for step 3's native arm64 build unless cross-compiling): enable
   `deb-src` in `/etc/apt/sources.list`, then
   `apt-get source wpasupplicant`. This pulls `wpa_2.10.orig.tar.xz` +
   Debian's own `.debian.tar.xz` patch series and unpacks to a ready
   `dpkg-buildpackage` source tree — preserves Debian's own build flags
   (`CONFIG_MESH`, `CONFIG_IEEE80211AC`, systemd/dbus integration, etc.)
   instead of guessing them from a bare hostap.git checkout.
   **Verify:** `dpkg-buildpackage -b -uc -us` on the *unpatched* tree
   first, confirm it produces a `wpa_supplicant` binary, and diff
   `strings <built binary> | grep -c .` against the real deployed one as a
   sanity check that the rebuild pipeline itself isn't already diverging
   before any patch is added.
2. **Apply the patches, in order.** `patch -p1 < 300-noscan.patch` then
   `patch -p1 < 301-mesh-noscan.patch` from the source tree root. Both are
   old (2010/2018) and targeted at whatever hostap tree OpenWrt currently
   tracks, not Debian's exact tree — **expect fuzz/context mismatches**,
   don't assume a clean apply. If `patch` fails outright, fall back to
   manually porting each hunk by locating the same functions in Debian's
   tree (all four touched files — `config.c`, `config_file.c`,
   `config_ssid.h`, `mesh.c`, `wpa_supplicant.c` — are named consistently
   across versions, this is expected to be mechanical even if line numbers
   shifted).
   **Verify:** `patch` reports "succeeded" (possibly "with fuzz N") for
   every hunk in both files, zero `.rej` files. Read the resulting diffs
   against the pre-patch tree afterward and confirm they match the intent
   in §3 (not just "it applied") — a fuzzy-matched hunk can land in a
   subtly wrong spot.
3. **Rebuild.** `dpkg-buildpackage -b -uc -us` again (or `make` directly
   inside `wpa_supplicant/` for a faster iteration loop while testing,
   final artifact should still come from a full package build for
   consistency). Cross-compiling for arm64 from an amd64 host is an
   alternative to a native Trixie-arm64 build host/chroot if that's faster
   in this project's existing CI/build setup — not scoped further here
   since no such setup currently exists for this binary (see §4).
   **Verify:** `strings <new binary> | grep -x noscan` now matches
   (it didn't before, per the earlier `ACS.md` gate) — the same kind of
   gate `ACS.md` already used, now checking for presence instead of
   absence.
4. **Bench-test off-fleet first**, same convention as the disable_ht40/
   disable_vht live benchmark in `ACS.md`: two nodes (or two of anything
   running this radio), swap only the mesh-radio `wpa_supplicant` binary,
   leave everything else stock. Test order, smallest change first:
   - `noscan=1` alone, default width (should still be 80MHz/VHT — confirms
     the patch doesn't silently change default behavior for anyone who
     doesn't opt in).
   - `noscan=1` + `disable_vht=1` (40MHz-only) on both nodes: restart
     `wpa_supplicant@wlan1` **multiple independent times** on both sides,
     confirm `iw dev wlan1 info` shows identical channel/width both sides
     every time — this is the determinism claim, and per §3 it needs
     multiple restarts, not one, to actually test.
   - `noscan=1` + `max_oper_chwidth=1` (80MHz) on both nodes, same
     multiple-restart determinism test, plus a real iperf3 number for
     comparison against `ACS.md`'s existing 505 Mbit/s (current
     nondeterministic VHT80) and 144 Mbit/s (20MHz-only) baselines.
   - Reproduce the original bug's trigger conditions if known/practical
     (the doc doesn't record exactly what made EUD3 diverge from EUD4,
     only that two clean restarts reproduced it) — without a known
     trigger, "many restarts, always matching" is the best available
     confidence signal.
5. **Only after (4) passes** does this belong anywhere near the fleet —
   see §6 for what "belong" means here (this is not a `binaries_arm64/`
   drop-in).

## 6. Deployment shape — not decided, options only

Not scoped to a decision yet; flagging the real choice rather than picking
one, since it affects update/rollback story and blast radius:

- **Replace the system `wpa_supplicant` package fleet-wide.** Both new
  patched fields (`noscan`, and `301`'s 2.4GHz HT40 table extension) are
  strictly opt-in — a node whose config never sets `noscan=1` gets
  byte-identical behavior to today's stock binary. This means replacing
  the system package is lower-risk than it sounds: nothing that doesn't
  explicitly opt in (any non-mesh wpa_supplicant use — e.g. whatever
  `manet-uplink-dispatch.sh` does for client-mode WiFi, if anything) is
  affected. Simpler ongoing story: one binary, normal `apt`-shaped
  packaging.
- **Vendor a separate binary + a systemd `ExecStart=` override for just
  the mesh radio's `wpa_supplicant@wlan1.service`,** left untouched
  everywhere else — smaller blast radius, more moving parts (unit
  override, separate binary path, its own update path independent of the
  system package).

Either way: **do not add this binary to `MANET/binaries_arm64/`** — per
`CLAUDE.md`, that directory is real vendor-supplied prebuilt binaries only
(Morse Micro's HaLow build, upstream alfred/batctl) and is explicitly
"do not modify." A new patched wpa_supplicant is *this project's own*
build artifact, not a vendor drop — give it its own location (e.g. a new
`binaries_arm64_custom/` or fold it into the packaging pipeline directly,
whichever this project's existing conventions in `packaging/*.sh` /
`provisioning/build-image-linux.sh` fit better — not decided here).

**Must land in the provisioning/packaging pipeline, not a live SSH swap.**
Per the `auto_update_v0.1.2_live_test` memory: manual node hotfixes get
reverted by the next software update. Whatever this becomes needs to be
baked into `build-image-linux.sh`/the tarball packaging scripts the same
way `wpa_supplicant_s1g` is, or it silently regresses on the fleet's next
update.

**New `mesh_5ghz_bw` value, once a patched binary exists:** today
`desiredMeshWidth()` (`node-manager/main.go:301`) and the matching
`radio-setup.sh`/`manet-wlan-reconcile.sh` `WIDTH_LINES` heredocs only
know `20` and `80`. A third value (`40`?) would need: `noscan=1` written
unconditionally whenever a patched binary is present, `disable_vht=1` for
the 40-only case, and — per the AP interface's existing precedent
(`radio-setup.sh`:1170-1183, `ht_capab=[HT40+]`/`[HT40-]` keyed off channel
number parity) — the same explicit secondary-channel-offset direction
logic, since `noscan` stops the scan that used to pick that automatically.
Not designed further here — this is follow-on work once §5 proves the
patch itself works, tracked so it isn't dropped.

## 7. No live channel-switch (CSA) exists in wpa_supplicant mesh — separate from the above

Checked directly against `wpa_supplicant/mesh.c` source (2026-08-27): it
defines no CSA/channel-switch function at all — 802.11s's mesh channel
switch element is never implemented for `mode=5` in wpa_supplicant, in any
version. This means "force two already-diverged nodes back in sync without
dropping the link" **cannot** be done via any live wpa_supplicant
mechanism today, patched or not — a resync is always a drop-and-rejoin
(`systemctl restart wpa_supplicant@<iface>`, what `setIfaceFrequency`
already does). The `noscan` patch above fixes the *nondeterminism* (a
restart reliably reconverges instead of coin-flipping), it doesn't add a
graceful in-place resync — those are two different problems, both raised
in the original ask, and this doc's fix only addresses the first one.

The other lever from `ACS.md`'s own "Open issue" section — implementing
the never-built verify-after-apply self-heal in `node-manager` (detect
live-channel divergence from the elected value even when the elected value
itself hasn't changed, then rate-limited forced restart) — is still the
right tool for actually noticing and recovering from a stuck pair,
independent of whether the `noscan` patch ships. That design is already
fully spec'd in `ACS.md` and doesn't depend on anything in this doc.

**Caution logged for future research sessions:** a mid-session AI
fetch-and-summarize pass on `301-mesh-noscan.patch` (before the verbatim
patch shown in §3 was pulled and read directly) also claimed
`ibss_mesh_can_use_vht()`'s patched condition "meaningfully changes
behavior on 5GHz" — the actual diff shows that branch is a no-op for an
interface already in `HOSTAPD_MODE_IEEE80211A` (our case). The paraphrase
wasn't fabricated, just imprecise about which hunks matter. Treat any
AI-summarized patch content as a lead to verify against the raw file, not
a citable fact — this doc's §3 only avoided repeating that imprecision
because the raw patch was fetched and read directly before writing it.

## Related docs

- `ACS.md` — the full ACS design, election algorithm, and the original
  (still valid) trace of why no vanilla-wpa_supplicant config fix exists.
  Read that first if this doc's context doesn't make sense standalone.
- `patches/wpa_supplicant-mesh-noscan/` — the two patches this doc is
  about, archived verbatim.
