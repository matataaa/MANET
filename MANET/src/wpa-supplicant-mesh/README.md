# wpa_supplicant (mesh `noscan` patch) — build inputs + binary

This directory holds the build inputs and resulting binary for a patched
`wpa_supplicant` that fixes wpa_supplicant mesh mode's nondeterministic
HT40/VHT80 primary-channel reselection. See
`../../docs/wpa-supplicant-mesh-noscan.md` for the full research/root-cause
writeup and `../../docs/ACS.md` for the original bug report this fixes, and
why. Read those before touching anything here.

This is **not** a vendor-supplied prebuilt binary (unlike
`MANET/binaries_arm64/`, which is Morse Micro's/upstream's own builds and is
explicitly "do not modify" — see `CLAUDE.md`) — it's this project's own
build artifact, following the same "checked in next to what builds it"
precedent as `MANET/src/mesh-voice/bin/` (CGO-linked, can't be produced by
the normal `GOOS=linux GOARCH=arm64 go build` cross-compile loop the other
Go services use).

## Contents

- `bin/wpa_supplicant_mesh-linux-arm64` — the built, **stripped** arm64
  binary. Installed on-device as `/usr/sbin/wpa_supplicant_mesh` — a new
  filename, deliberately not overwriting the system-package-owned
  `/usr/sbin/wpa_supplicant` (an apt/unattended-upgrade of the
  `wpasupplicant` package would silently revert a same-name overwrite).
- `300-noscan.patch`, `301-mesh-noscan.patch` — the two OpenWrt patches this
  binary is built from (moved here from
  `docs/patches/wpa_supplicant-mesh-noscan/` — they're build inputs, not
  documentation). Archived verbatim, not summarized, on 2026-08-27:
  - `300-noscan.patch` — https://github.com/openwrt/openwrt/blob/master/package/network/services/hostapd/patches/300-noscan.patch
    (Felix Fietkau, 2010-01-20, "Add noscan, no_ht_coex config options")
  - `301-mesh-noscan.patch` — https://github.com/openwrt/openwrt/blob/master/package/network/services/hostapd/patches/301-mesh-noscan.patch
    (Daniel Golle, 2018-04-20, "Allow HT40 also on 2.4GHz if noscan option is
    set, which also skips secondary channel scan just like noscan works in
    AP mode")

  **Apply order matters — 301 depends on 300.** `301`'s `mesh.c` hunk reads
  `conf->noscan`, a field on `struct hostapd_config` that only exists
  because `300` adds it (`src/ap/ap_config.h`). Apply `300` first, then
  `301`, against the same source tree. Neither applies cleanly against
  Debian Trixie's actual 2.10-24 source — see `build.sh` and
  `../../docs/wpa-supplicant-mesh-noscan.md` §8.2 for the by-hand porting
  this actually took.
- `build.sh` — the recorded build recipe (Debian source + patches +
  cross-compile), for reproducing `bin/wpa_supplicant_mesh-linux-arm64`
  from scratch. Documents the steps; **not currently a turnkey script** —
  see its own header for what's automated vs. manual.

## On-device install path

`/usr/sbin/wpa_supplicant_mesh` — wired up via a systemd drop-in
(`/etc/systemd/system/wpa_supplicant@.service.d/20-mesh-binary.conf`)
generated and kept in sync at runtime by `node-manager`
(`ensureNoscanDropIn`, `src/node-manager/main.go`), not shipped as a
static rootfs file — see `docs/wpa-supplicant-mesh-noscan.md` §6 for why
a static file was tried and reverted (it can't survive an OTA update on
an already-provisioned node). Every key the patch adds is opt-in
per-`network={}`-block (`noscan=1`, etc.), so pointing every
`wpa_supplicant@<iface>` instance fleet-wide at this binary is safe by
construction — a conf file that never sets `noscan=1` gets byte-identical
behavior to the stock binary.

**Never write `noscan=1` (or any patch-added key) into a conf file without
first confirming this binary is actually present and executable
(`/usr/sbin/wpa_supplicant_mesh`)** — the stock system `wpa_supplicant`
fails to parse the entire `network={}` block on an unrecognized key and
exits (`status=255`), dropping that radio out of the mesh. See
`../../docs/wpa-supplicant-mesh-noscan.md` for the full incident writeup.
