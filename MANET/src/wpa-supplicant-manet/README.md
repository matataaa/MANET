# wpa_supplicant_manet (hostap 2.11 mesh build) — build inputs + binaries

Upstream's (very-srs/MANET) Wi-Fi mesh supplicant, built from
`MANET/binaries_arm64/wpa_supplicant-manet/` there. Opt-in per fleet with
`mesh_supplicant=manet`; the default stays the 2.10 noscan build in
`../wpa-supplicant-mesh/`. HaLow keeps its own S1G supplicant either way.

## Contents

- `.config`, `source.sha256`, `patches/0001`–`0008`, `patches/series` —
  copied unchanged from upstream commit 24c71d9, so upstream's bench and
  regression results apply to the same code. Re-sync from there.
- `patches/0100-accept-noscan.patch` — fork only. Configs written for the
  noscan build carry `noscan=1`; plain 2.11 rejects the whole network block
  and the radio leaves the mesh. 0100 parses the key and ignores it.
- `build.sh` — upstream's recipe plus a cross-compile path.
- `bin/wpa_supplicant_manet-linux-arm64`, `bin/wpa_cli_manet-linux-arm64` —
  stripped arm64 outputs, installed as `/usr/sbin/wpa_supplicant_manet` and
  `/usr/sbin/wpa_cli_manet` next to (never over) the Debian and noscan
  binaries. Needs libc ≥ 2.34, libnl-3, libnl-genl-3, libssl3 only.

## What the patches change

- HE (802.11ax) enabled (`CONFIG_IEEE80211AX`; Debian's 2.10 omits it).
- 0001: an established peer is freed after `mesh_max_inactivity` seconds of
  kernel inactivity (configs get `mesh_max_inactivity=10`).
- 0002/0003: failed SAE attempts are freed after 3–4 s instead of
  `PLINK_BLOCKED`; a station missing from the kernel is replaced.
- 0004: `MESH-SAE … step= state= status=` INFO logging, no secrets.
- 0006: mesh never applies OBSS primary/secondary swaps — the fix the noscan
  build needed `noscan=1` for, now without a config key.
- 0007/0008: DFS hooks, inert unless `MANET_DFS_OWNER 1` is sent (we don't).

## Width keys under manet

node-manager (`meshWidthKeys`), `radio-setup.sh` and
`manet-wlan-reconcile.sh` write upstream's keys, keeping HE/VHT on:

| mesh_5ghz_bw | 5 GHz keys | 2.4 GHz keys |
|---|---|---|
| 20 (default, recommended) | `max_oper_chwidth=0`, `disable_ht40=1` | same |
| 40 | `max_oper_chwidth=0` | `max_oper_chwidth=0`, `disable_ht40=1` |
| 80 | `max_oper_chwidth=1` | same as 40 |

`disable_vht=1` is never written under manet: on 5 GHz, 2.11 enables HE only
together with VHT.

## Bench (EUD4 ↔ EUD3, ch153, 2026-10-19, kernel 6.18.33-manet)

| Build / width | 5 GHz link | TCP Mbit/s (→ / ←) |
|---|---|---|
| 2.10 noscan, 40 MHz | VHT-MCS9 360 | 252 / 274 |
| 2.11, old noscan confs | HE-MCS11 573.5 | 324 / 316 |
| 2.11, 40 MHz keys | HE-MCS11 573.5 | 305 / 338 |
| 2.11, 20 MHz keys | HE-MCS11 286.7 | 211–215 / 223 |
| 2.11 on one node only | HE out / VHT in | 321 / 271 |

2.4 GHz moves from HT MCS15 (144) to HE-MCS11 (286.7) with no key change.
A silently dropped peer is held ~318 s by 2.10 and ~11 s by 2.11 with
`mesh_max_inactivity=10`; rejoin is ~2 s on both. Mixed 2.10/2.11 pairs
peer normally, so a staged rollout is safe. Upstream's
`tests/unit/test_mesh_freq.sh` passes on this patch set.

## Building

```sh
# On a Debian 13 arm64 node (upstream's documented path):
sudo apt install build-essential pkg-config libnl-3-dev libnl-genl-3-dev libssl-dev curl patch
bash build.sh /tmp/manet-wifi

# Cross from x86_64: extract Debian trixie arm64 .debs into a sysroot
# (libc6, libc6-dev, linux-libc-dev, libnl-3-200, libnl-3-dev,
# libnl-genl-3-200, libnl-genl-3-dev, libssl3t64, libssl-dev) with
# `dpkg-deb -x`, then:
CROSS_COMPILE=aarch64-linux-gnu- SYSROOT=/path/to/sysroot bash build.sh /tmp/manet-wifi
cp /tmp/manet-wifi/wpa_supplicant_manet bin/wpa_supplicant_manet-linux-arm64
cp /tmp/manet-wifi/wpa_cli_manet bin/wpa_cli_manet-linux-arm64
```

Rebuild and re-bench after any patch change; a source patch alone does not
change the shipped binary.
