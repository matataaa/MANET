# wpa_supplicant mesh `noscan` patch — research + build plan

Research session: 2026-08-27. Companion to `ACS.md`'s "Open issue: 5GHz
primary channel doesn't reliably match between nodes" — that doc concluded
the coex-scan bug has **no config-level fix in mainline wpa_supplicant** and
chose 20MHz-only mesh as the safe default. This doc exists because a real,
already-written, community-maintained fix for the underlying bug was found
after that conclusion was written — read this before re-deriving any of it,
and before assuming "no fix exists" is still true.

**2026-08-27 update — hardware-validated on EUD3/EUD4, fix confirmed (§9).**
20/20 independent restarts (both VHT80 and HT40-only, 5 restarts × 2 nodes
each) landed on the identical channel/width/center1 on both sides, zero
deviation. Not yet deployed anywhere permanent — `mesh_5ghz_bw` is back to
`20` on the fleet, the live test was fully reverted. §8 is the build log,
§9 is the hardware test. Deployment shape (§6) is still an open decision.

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

**Confirmed on real hardware 2026-08-27, see §9** — yes, `obss_scan=0`
gives both nodes the identical primary channel, not just a stable one
each: 20/20 independent restarts (VHT80 and HT40, 5 each × 2 nodes)
matched exactly on both sides every time.

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

## 8. Build log — 2026-08-27, first successful build

**Environment: this repo's own build host (`meshtasticbuilder`, x86_64
Ubuntu 24.04 Noble), not a Trixie machine.** Chroot/QEMU (debootstrap +
qemu-user-static + binfmt_misc) was tried first and **does not work in
this sandbox** — writing to `/proc/sys/fs/binfmt_misc/register` is denied
even as root, so no arm64 binary can actually be executed here, only
compiled. Pivoted to plain cross-compilation with `aarch64-linux-gnu-gcc`
(already installed on this host, same tool this project's kernel/Morse
builds already use — see `kernel-6.18-morse-port.md`) against a **hand-
assembled sysroot of real Debian Trixie arm64 `.deb`s**, extracted with
`dpkg -x` into a scratch directory rather than registered with the host's
package manager — chosen deliberately over Ubuntu Noble's own arm64
multiarch packages to avoid a libssl/libnl soname/ABI mismatch against
what's actually on the Pi nodes. None of this touched the host's own
`/usr/aarch64-linux-gnu/` cross-sysroot (from Ubuntu's
`libc6-dev-arm64-cross` etc., left untouched and still used for
glibc/libgcc/kernel headers) — only userspace libs (openssl, libnl3,
dbus, pcsclite) came from the hand-assembled one.

**Everything below happened outside the MANET repo**, in
`/root/manet-wpa-noscan-build/` on the build host — not part of this git
tree, not cleaned up automatically. Re-creating it from scratch following
the steps below takes about 15-20 minutes of mostly-unattended downloads
+ one `make` invocation.

### 8.1 Get Debian's exact source (not a generic tag)

Confirmed Trixie's live `Sources` index (not just the cached web search
from §5): `wpa` source package version `2:2.10-24`. Downloaded directly
via `curl` from `deb.debian.org`'s pool (no `apt`/`dpkg` source
registration needed — these are plain files):

```
wpa_2.10-24.dsc
wpa_2.10-24.debian.tar.xz
wpa_2.10.orig.tar.xz
```

Extracted `wpa_2.10.orig.tar.xz`, then the `debian.tar.xz` **into the same
tree** (it lays down a `debian/` subdirectory). Applied Debian's own
20-patch quilt series first, to reproduce the exact source Trixie ships
before adding anything of our own:

```
tar xJf wpa_2.10.orig.tar.xz
tar -C wpa-2.10 -xJf wpa_2.10-24.debian.tar.xz
cd wpa-2.10 && QUILT_PATCHES=debian/patches quilt push -a
```

All 20 applied cleanly (`quilt` needed installing; `apt-get install
quilt`). **`QUILT_PATCHES=debian/patches` is required** — quilt's default
(`patches/`) finds nothing and silently reports "No series file found."

Debian's actual build config for the Linux target,
`debian/config/wpasupplicant/linux`, confirmed the relevant flags before
patching further: `CONFIG_MESH=y`, `CONFIG_IEEE80211AC=y`,
`CONFIG_IEEE80211N=y`, `CONFIG_DRIVER_NL80211=y`, `CONFIG_LIBNL32=y`,
`CONFIG_TLS=openssl`, and (missed on the first pass, see 8.2)
`CONFIG_CTRL_IFACE_DBUS_NEW=y`/`CONFIG_CTRL_IFACE_DBUS_INTRO=y` — no
`CONFIG_DBUS` key by that exact name, which is why a first grep for it
came back empty; the actual dbus-enabling keys are the `CTRL_IFACE_DBUS*`
ones. Copied this file to `wpa_supplicant/.config` to build with Debian's
exact feature set, not a guessed one.

### 8.2 Applying the two noscan patches — one hunk landed in the wrong place, verified and fixed by hand

`300-noscan.patch` applied clean (`patch -p1`, only line-number offsets,
no fuzz). `301-mesh-noscan.patch` **partially failed** — 2 of its
`wpa_supplicant.c` hunks rejected outright, because upstream had since
**inlined** the separate `ibss_mesh_select_40mhz()` function this patch
targets directly into `ibss_mesh_setup_freq()`, with `obss_scan` now
declared inline in a combined variable list and the channel table renamed
`ht40plus[]` (5GHz-only, no separate 2.4GHz variant). Ported both by hand
after reading the real 2.10 source directly:

- `int i, chan_idx, ht40 = -1, res, obss_scan = 1;` →
  `..., obss_scan = !(ssid->noscan);`
- `int ht40plus[] = { 36, 44, ... }` → prefixed with `1, 2, 3, 4, 5, 6, 7,`
  (2.4GHz channels, for parity with the original patch's intent — not
  required for the 5GHz fix itself).

**A third hunk "succeeded" via `patch`'s fuzzy matching but landed
completely wrong** — worth internalizing as a general lesson, not just a
one-off: `patch` reported success (with a large offset) for the hunk that
was supposed to add a 2.4GHz-specific call to `ibss_mesh_select_40mhz()`
right after the "Setup higher BW only for 5 GHz" check. Fuzzy context
matching instead spliced it into a **totally unrelated function** ~600
lines away (WPA key-suite-setup code, inside code equivalent to
`wpa_supplicant_associate`), calling a function (`ibss_mesh_select_40mhz`) that
doesn't even exist in this inlined 2.10 source, with variables
(`mode`, `freq`, `obss_scan`, `is_6ghz`, `dfs_enabled`) out of scope at
that point — this would not compile, and worse, doesn't just fail loudly:
had it referenced only in-scope names it could have silently corrupted
unrelated logic. **Caught by reading every hunk's actual landing spot
after applying, not by trusting `patch`'s exit status** — the same
standard this doc already held itself to in §5's build plan, now proven
necessary in practice, not just in theory. Deleted the two bogus lines;
confirmed the surrounding "Setup higher BW only for 5 GHz" gate
(`if (mode->mode != HOSTAPD_MODE_IEEE80211A && !(ssid->noscan)) return;`,
itself successfully patched in the right place) already achieves the same
effect for 2.4GHz in this inlined structure, so nothing needed to replace
the deleted lines.

Also fully traced the one thing §3 flagged as unverified: `mesh.c`'s
`if (conf->noscan) ssid->noscan = 1;` reads a `struct hostapd_config *conf`
that mesh.c builds via `conf = hostapd_config_defaults();` — a generic,
`ssid`-independent default struct. `conf->noscan` is therefore always `0`
for a mesh interface; **this hunk is dead code**, confirmed, not
"unverified." Harmless (compiles, never fires) and left in place — the
real fix works entirely through `ssid->noscan` being set directly by the
config-file parser (§3's `config.c`/`config_ssid.h` hunks), which happens
before `wpa_supplicant_mesh_init()` ever runs.

Verified every other hunk landed in its correct location by reading the
actual diff context after applying (`config.c`, `config_ssid.h`,
`mesh.c` all confirmed correct). `config_file.c`'s hunk (the one that
needed `--fuzz=3`) landed in the right *function*
(`wpa_config_write_network`) but at the very end, past
`#ifdef CONFIG_HE_OVERRIDES`, rather than inside the `#ifdef CONFIG_MESH`
block the original patch targeted. Left as-is: this function only
serializes config back out for `wpa_cli save_config`, which this
project's deployment (writing `wpa_supplicant.conf` directly from
`radio-setup.sh`) never uses — cosmetically misplaced, functionally
irrelevant here.

### 8.3 Sysroot — what was actually needed, found by iterating real link/compile errors

Built a sysroot at `/root/manet-wpa-noscan-build/sysroot/` by downloading
these exact Trixie arm64 `.deb`s (from `dists/trixie/main/binary-arm64/
Packages.xz`, matched by exact version) and `dpkg -x`-extracting each —
**not** `apt`/`dpkg -i`, so the host's own package database is never
touched:

`libssl-dev` + `libssl3t64`, `libnl-3-dev` + `libnl-3-200`,
`libnl-genl-3-dev` + `libnl-genl-3-200`, `libnl-route-3-dev` +
`libnl-route-3-200` (needed because `CONFIG_DRIVER_NL80211` unconditionally
sets `CONFIG_LIBNL3_ROUTE=y` in `src/drivers/drivers.mak` — easy to miss,
only surfaces as a link error, not a `.config` grep), `libdbus-1-dev` +
`libdbus-1-3` (needed because Debian's config enables
`CONFIG_CTRL_IFACE_DBUS_NEW`, missed on the first `.config` read — see
8.1), `libpcsclite-dev` + `libpcsclite1` (`CONFIG_PCSC=y`).

Each missing piece was found by just running the build and reading the
actual error, not by trying to enumerate dependencies up front — faster
and more reliable than guessing:

1. `Package dbus-1 was not found` / `config.c: fatal error: includes.h` —
   two unrelated problems at once. The real bug: passing `CFLAGS=...` as a
   `make` **command-line argument** creates a make "override" variable
   that **completely replaces** the Makefile's own `CFLAGS += ...` lines
   instead of adding to them — it clobbered the Makefile's own
   `-I../src`/`-I../src/utils` includes entirely. Fixed by using
   `EXTRA_CFLAGS` instead (the Makefile's own designated injection point,
   `CFLAGS += $(EXTRA_CFLAGS)` at the top) and by exporting `LDFLAGS`
   rather than passing it as a `make` argument, since environment
   variables (unlike command-line args) can still be appended to by the
   Makefile's `+=`.
2. `pcsc_funcs.c: fatal error: winscard.h` — the Makefile hardcodes
   `-I/usr/include/PCSC` (a bare host path, not sysroot-relative) when
   `CONFIG_PCSC=y`; our sysroot's copy is invisible to that hardcoded
   flag. Added `-I<sysroot>/usr/include/PCSC` to `EXTRA_CFLAGS` explicitly.
3. `dbus/dbus_dict_helpers.c: fatal error: dbus/dbus.h` — the Makefile
   gets dbus's cflags via `pkg-config --cflags dbus-1`, which was silently
   returning **empty** (not erroring loudly in the make log) because
   `dbus-1.pc` declares `Requires.private: libsystemd >= 209`, and
   `pkg-config --cflags` (unlike `--libs`) refuses to emit anything at all
   if a `Requires.private` package can't be found — confirmed by running
   the exact `pkg-config` invocation by hand outside `make` and seeing
   `exit=1` with the `Package libsystemd was not found` message.
   `PKG_CONFIG_PATH`/`PKG_CONFIG_SYSROOT_DIR`/`PKG_CONFIG_LIBDIR` pointed
   at the sysroot's own `pkgconfig/` dir (needed regardless, for
   dbus-1.pc/libnl's `.pc` files' `libdir`/`includedir` to resolve inside
   the sysroot instead of `/usr`) doesn't fix this by itself, since no
   `libsystemd.pc` exists anywhere in a sysroot that deliberately has no
   systemd package in it at all. Fixed with a **hand-written stub
   `libsystemd.pc`** (empty `Cflags:`/`Libs:`, `Version: 300`) dropped into
   the sysroot's `pkgconfig/` dir — legitimate, not a hack that needs
   later cleanup: we only need to dynamically link against the real
   `libdbus-1.so` (which already carries its own real `libsystemd`
   dependency baked in from how Debian built it), we never call any
   libsystemd symbol ourselves, so nothing about actually satisfying that
   dependency for real is needed at *our* build time.
4. Final link step: `ld` reported missing `libz.so.1`/`libzstd.so.1`
   (transitive deps of `libcrypto.so.3`, which supports compression) and
   `libsystemd.so.0` (transitive dep of the real `libdbus-1.so.3`) —
   **correctly** missing, since this sysroot deliberately doesn't carry
   zlib/zstd/systemd packages. Rather than fetching three more `.deb`s for
   packages the sysroot doesn't otherwise need, added
   `-Wl,--allow-shlib-undefined` to `LDFLAGS`: these are real transitive
   shared-library-to-shared-library dependencies that the actual Trixie
   target already satisfies (zlib1g/libzstd1/libsystemd0 are base-system
   packages on any Debian install, definitely present on the Pi image)
   — the linker doesn't need to verify them at build time, only the
   target's runtime dynamic linker does, and it will.

### 8.4 Result

Build succeeded (`make -C wpa_supplicant wpa_supplicant`, exit 0).
Verified, not just assumed:

- `file`: `ELF 64-bit LSB pie executable, ARM aarch64 ... dynamically
  linked, interpreter /lib/ld-linux-aarch64.so.1` — correct target arch.
- `strings ... | grep -x noscan` — **present** (absent on the original
  binary per the §5 gate check). Two occurrences (the config-key name
  string itself, plus the write-back code path).
- `readelf -d` `NEEDED` entries: `libnl-3.so.200`, `libnl-genl-3.so.200`,
  `libnl-route-3.so.200`, `libssl.so.3`, `libcrypto.so.3`,
  `libdbus-1.so.3`, `libpcsclite.so.1`, plus standard libc/libm — every
  one of these exact sonames matches what a stock Trixie system already
  has installed (confirmed by construction, since the sysroot was built
  from Trixie's own packages) — **no extra runtime packages should be
  needed on the actual nodes**, only the binary itself needs to land.

Binary saved at `/root/manet-wpa-noscan-build/wpa_supplicant-noscan-arm64`
(15.9MB, unstripped/with debug info — deliberately not stripped yet, in
case the first hardware test needs debugging; strip before any real
deployment). **Not yet run on real hardware or even on real arm64
silicon of any kind** — this build environment cannot execute arm64
binaries at all (see the QEMU/binfmt note at the top of this section), so
"it built and links clean, with the right strings and the right library
dependencies" is the strongest verification possible without a real
node. Section 5's bench-test plan (multiple independent restarts on two
real nodes, checking `iw dev wlan1 info` matches) is the next real
verification step and hasn't happened yet.

## 9. Hardware test — 2026-08-27, EUD3/EUD4, fix confirmed

Deployed the §8 binary to the two nodes with a live 5GHz mesh link between
them (EUD4 `192.168.1.183`, EUD3 `10.30.2.186` via a jump through EUD4 —
mesh-only, not directly LAN-reachable). `node-manager` was stopped on both
first, per §5's own warning that `reconcile5GHzWidth()` (15s tick) would
otherwise fight a manual config test by re-adding/removing
`disable_ht40`/`disable_vht` out from under it. Backed up the stock
`/usr/sbin/wpa_supplicant` on both nodes before touching anything.

**Deployment itself worked cleanly**: the binary ran with no missing-
library errors on real Trixie hardware, confirming §8.4's static
dependency analysis held in practice, not just on paper.

**Test A — VHT80** (`noscan=1` + `max_oper_chwidth=1`, `mesh_5ghz_bw=80`):
5 restarts × 2 nodes = **10/10 identical** — `channel 44 (5220 MHz),
width: 80 MHz, center1: 5210 MHz` every single time, both sides. Mesh
plink established, batman route active on wlan1 both directions. Real
iperf3 (EUD3→EUD4, 10s): **374 Mbit/s sender / 373 Mbit/s receiver, 122
retransmits.** Below the old nondeterministic-VHT80 baseline in `ACS.md`
(505 Mbit/s, different session/RF conditions, one sample each side, not a
controlled comparison) but far above the 144 Mbit/s 20MHz-only and 100
Mbit/s mixed-width baselines documented there — and, unlike the 505
Mbit/s number, this one came with a deterministic, repeatable channel
pick behind it rather than a lucky restart.

**Test B — HT40-only** (`noscan=1` + `disable_vht=1`, `mesh_5ghz_bw=80`
so node-manager's reconciler wouldn't strip the lone `disable_vht`): 5
restarts × 2 nodes = **10/10 identical** — `channel 44 (5220 MHz),
width: 40 MHz, center1: 5230 MHz` every time. Mesh plink established,
batman route active. This is the concrete, hardware-confirmed answer to
§1's original question: 40MHz doesn't need the width-limiting workaround
either once `noscan` is in place — same fix, same determinism, at
whichever width is chosen. No throughput sample taken for this case.

**Zero deviation across all 20 restarts, either test.** This is the
strongest evidence so far that `obss_scan=0` doesn't just stop the scan
but produces a genuinely deterministic, config-derived pick — the thing
§3 flagged as unconfirmed is now confirmed.

**Full revert verified, not just attempted**: original binary restored
(md5-matched against the pre-test backup), both conf files' `network={}`
blocks restored to the stock `disable_ht40=1`+`disable_vht=1` content,
`mesh_5ghz_bw` reset to `20` in `/etc/mesh.conf` on both nodes,
`wpa_supplicant@wlan1` restarted and confirmed back to `channel 44, width:
20 MHz` on both. `node-manager` restarted on both (matching its prior
running state), came back up clean, ACS re-elected `5220` within one
cycle on both, `NRestarts=0` on the wpa_supplicant unit (confirms the
reconciler found the restored config already correct and took no action),
batman route back to the ~49-58 Mbit/s baseline consistent with 20MHz.
Both nodes left exactly as found.

**What this changes about the state of this doc**: the fix is no longer
"a promising patch that builds clean" — it's hardware-confirmed to solve
the actual symptom (two nodes landing on different primaries) for both
40MHz and 80MHz, repeatably. What's left is entirely the §6 deployment
question (which was never a technical unknown, just an undecided rollout
shape) plus the follow-on work §6 already scoped: a `mesh_5ghz_bw=40`
value, and landing the binary in the actual packaging/provisioning
pipeline rather than a live SSH swap (this test's binary and config
changes were fully reverted — nothing about this test is deployed
anywhere persistent).

### 9.1 Follow-up — VHT80 confirmed clean with ACS actually running

§9 above was run with `node-manager` **stopped** on both nodes, so it
answered "is the primary-channel pick deterministic" but not "does this
coexist with ACS's own reconcile loop." Re-ran the VHT80 case
(`noscan=1`+`max_oper_chwidth=1`, `mesh_5ghz_bw=80`) on the same two
nodes with `node-manager`/ACS left running throughout, watching ~11
minutes (~4 full 180s ACS cycles) rather than a single restart:

- Both nodes landed on `channel 44 (5220 MHz), width: 80 MHz, center1:
  5210 MHz` on the first restart and **never deviated** across 7 samples
  taken every ~90s.
- `NRestarts=0` on `wpa_supplicant@wlan1` on both nodes for the entire
  window — no restart storm.
- Every one of the 4 observed `[acs]` election cycles logged `elected
  channel 5220`, matching live radio state throughout.
- **`reconcile5GHzWidth` never logged a single line** — confirmed in
  practice, not just by reading the code, that it correctly saw
  `mesh_5ghz_bw=80` already meant no `disable_ht40`/`disable_vht` to add,
  and never touches `noscan`/`max_oper_chwidth` at all.
- Mesh plink `ESTAB` and the wlan1 batman route stayed active and stable
  the entire window.

Fully reverted afterward, same as §9 (binaries/configs md5-verified back
to stock, `mesh_5ghz_bw=20`, `node-manager`/`acs=y` left exactly as
found). **The HT40-only case still cannot be tested with ACS running** —
unchanged from §6's existing gap: `reconcile5GHzWidth` manages
`disable_ht40`/`disable_vht` as a pair keyed off `mesh_5ghz_bw`, and
without a `mesh_5ghz_bw=40` value, leaving ACS on would strip a manually
set `disable_vht=1` within one 15s tick. This is exactly the follow-on
work already named in §6.

## Related docs

- `ACS.md` — the full ACS design, election algorithm, and the original
  (still valid) trace of why no vanilla-wpa_supplicant config fix exists.
  Read that first if this doc's context doesn't make sense standalone.
- `patches/wpa_supplicant-mesh-noscan/` — the two patches this doc is
  about, archived verbatim.
