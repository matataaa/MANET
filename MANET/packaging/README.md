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
