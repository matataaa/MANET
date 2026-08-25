# Packaging

The source repository keeps universal MANET runtime files, while board-specific
files (kernel, modules, firmware) are supplied as an external overlay during CI.

## Inputs

Universal files come from this repository:

- `MANET/scripts/{core,elections,radio,network,system}` -> `/usr/local/bin`
- `MANET/cmd/manet-ctrl/manet-ctrl` -> `/usr/local/bin/manet-ctrl`
- `MANET/www` -> `/usr/local/share/manet/www`
- `MANET/binaries_arm64` -> `/usr/sbin` + `/usr/local/bin`
- `MANET/systemd` -> `/etc/systemd/system`
- `MANET/systemd-network` -> `/etc/systemd/network`
- `MANET/udev/rules.d` -> `/etc/udev/rules.d`
- `MANET/networkd-dispatcher` -> dispatcher hook locations
- `MANET/etc` -> `/etc`
- `MANET/root/regulatory.db` -> `/root/regulatory.db`

Board-specific overlay files should come from a release artifact, not from
committed source. The builder should read them from `SBC_OVERLAY_DIR` when that
environment variable is set.

## RPi5 overlay contract

Expected release asset:

- Release/tag: `rpi5-sbc-overlay-current`
- Asset: `rpi5-sbc-overlay.tar.gz`
- Release URL: `https://github.com/very-srs/MANET/releases/tag/rpi5-sbc-overlay-current`

The archive must be root-relative. It should contain only SBC-specific files,
such as:

- `usr/lib/modules/6.6.78-manet+/extra/morse/dot11ah.ko`
- `usr/lib/modules/6.6.78-manet+/extra/morse/morse.ko`
- `usr/lib/firmware/morse/bcf_*.bin`

It should not contain universal scripts, systemd units, network dispatcher
hooks, dashboard files, or generated config from a live node.

## Output

`build-rpi5-tarball.sh` creates `rpi5-install.tar.gz` as a root-relative
tarball with numeric owner/group `0/0`.

The generated tarball is the artifact that gets attached to CI-created releases.

## Bumping the SBC overlay version

`rootfs/etc/manet_version.txt` is **not** a version we invent — it mirrors the
version stamp that ships inside the externally vendored SBC overlay itself.
`build-tools-tarball.sh` copies it verbatim into the tools tarball; nothing
generates or bumps it automatically. Format is two lines: `<version>` then
`<MM/YYYY>` (e.g. `0.530` / `08/2026`).

This is intentionally separate from the rest of the stack's versioning (see
[`MANET/docs/VERSIONING.md`](../docs/VERSIONING.md)), which is git-tag/
`git describe` driven — the overlay doesn't correspond to our commits, so it
can't be derived from git history.

**Pi4/CM4 is the actively supported board today; RPi5 is a later-stage
target.** Keep this in mind when deciding how much time to spend keeping the
RPi5 section below current.

**CM4**: the overlay is vendored from `https://www.colorado-governor.com/manet/cm4-install.tar.gz`
— an externally-hosted tarball not published in the very-srs/MANET GitHub
repo. Run `packaging/fetch-cm4-overlay.sh` (optionally pointing it at a local
copy of that tarball). It writes a `Bundled version` line into the generated
`kernel-work/packages/cm4-sbc-overlay/VENDORED_FROM.md`. Copy that value
verbatim into `MANET/rootfs/etc/manet_version.txt`, then rebuild the tools
tarball. That file has no lightweight version endpoint of its own (the
version only lives inside the tarball) — to check for an update without
downloading the full ~46MB, compare the current `curl -sI` `Last-Modified`
header against the value already recorded in `VENDORED_FROM.md`.

**RPi5**: the overlay comes from the `rpi5-sbc-overlay-current` GitHub
release asset consumed by `.github/workflows/rpi5-release.yml`. There is no
local fetch/vendoring script for this board today, so the version has to be
obtained from whoever publishes updates to that release asset and copied in
by hand.

## Publishing an overlay OTA update

For what happens on the node side once this is published — settings,
the bandwidth gate, manual/fleet-wide update actions — see
[`MANET/docs/AUTO_UPDATE.md`](../docs/AUTO_UPDATE.md).

Separate from the full-image build above: `node-update`'s opt-in overlay
channel (`auto_update_overlay`, default off — see
[`MANET/docs/VERSIONING.md`](../docs/VERSIONING.md)) pulls a per-board
version file and tarball from `update_url`, decoupled from the SBC overlay
version bump described above (you can bump `manet_version.txt` for the next
full image without publishing an OTA overlay update, and vice versa).

**CM4**:

```sh
packaging/fetch-cm4-overlay.sh
packaging/build-overlay-tarball.sh kernel-work/packages/cm4-sbc-overlay cm4-sbc-overlay.tar.gz
```

Write the `Bundled version` value from the generated `VENDORED_FROM.md` into
a plain-text `cm4-overlay-version.txt`. Host both files at `update_url`.

**RPi5**:

```sh
gh release download rpi5-sbc-overlay-current --pattern rpi5-sbc-overlay.tar.gz
```

The asset is already root-relative and needs no rebuild. Hand-write its
known version into a plain-text `rpi5-overlay-version.txt`. Host both files
at `update_url`.

In both cases the version file must only be bumped once you're satisfied the
paired tarball is good — any node with `auto_update_overlay=y` will pull and
apply it on its next check, and there's no rollback if it fails to boot.
