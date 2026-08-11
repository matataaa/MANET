# x86-64 Gateway with Lunpid mm8108 USB HaLow Dongle

Setup notes for running an x86-64 machine as a MANET gateway node using a
Lunpid mm8108 (Morse Micro MM81xx) USB HaLow dongle.

## Hardware

- Lunpid mm8108 USB HaLow adapter (Morse Micro MM8108 chipset)
- Any x86-64 machine running Debian 13 / kernel 6.12+

## Driver

Driver package: `morse_driver-mm8108-2.0.0.zip` from Morse Micro.

Install the two kernel modules into `/lib/modules/$(uname -r)/updates/`:

```
morse.ko      # Main Morse Micro driver (USB transport)
dot11ah.ko    # S1G <-> HT frame translation
```

Run `depmod -a` after copying.

Firmware and BCF go to `/lib/firmware/morse/`:

```
mm8108b2-rl.bin         # Firmware
bcf_boardtype_0807.bin  # Board config
```

## wpa_supplicant

Use the Morse-patched build: `wpa_supplicant_s1g` (v2.12-morse_micro-rel_1_16_4).
Install to `/usr/sbin/wpa_supplicant_s1g`.

Copy `wpa_supplicant-s1g-wlan0.conf` to `/etc/wpa_supplicant/`.
Copy `wpa_supplicant-s1g-wlan0.service` to `/etc/systemd/system/`.

Update the conf with your mesh SSID and SAE password.

## morse_cli (STUB)

Copy the `morse_cli` stub script to `/usr/local/bin/morse_cli`.

The mm8108 v2.0 driver handles mesh configuration via nl80211 natively.
Sending nl80211 vendor commands (as the real morse_cli binary does) conflicts
with the driver's internal state and breaks Auth frame forwarding. The stub
returns 0 for all commands so wpa_supplicant_s1g thinks they succeeded.

The ARM radio nodes (mm6108, driver v1.16.4) use the real morse_cli binary.
Do NOT use that binary on the mm8108 v2.0 gateway.

## Critical config: MBCA parameters

The wpa_supplicant config MUST include these MBCA parameters:

```
mbca_config=1
mbca_min_beacon_gap_ms=25
mbca_tbtt_adj_interval_sec=60
dot11MeshBeaconTimingReportInterval=10
mbss_start_scan_duration_ms=2048
mesh_beaconless_mode=0
```

Without `mesh_beaconless_mode=0`, the Morse wpa_supplicant defaults to
beaconless mode. The gateway won't transmit mesh beacons, so other nodes
can't discover it via scanning and will drop its SAE Auth frames with
"Mesh peer not yet known."

## Cross-version compatibility

These settings are required for the mm8108 v2.0 gateway to peer with
mm6108 v1.16.4 radio nodes:

| Parameter | Value | Reason |
|-----------|-------|--------|
| `sae_pwe` | `0` | Older firmware doesn't support hunting-and-pecking bypass |
| `s1g_prim_1mhz_chan_index` | `0` | Must match across firmware versions |
| `mesh_dynamic_peering` | `1` | Required for auto peer discovery |
| `ieee80211w` | `2` | PMF required — works fine cross-version |

## batman-adv

Both gateway (v2024.2) and nodes (v2025.4) use compat version 15 and
interoperate. Install `batman-adv` and `batctl` from packages.

## Quick setup

```sh
# Install modules
cp morse.ko dot11ah.ko /lib/modules/$(uname -r)/updates/
depmod -a

# Install firmware
mkdir -p /lib/firmware/morse
cp mm8108b2-rl.bin bcf_boardtype_0807.bin /lib/firmware/morse/

# Install wpa_supplicant
cp wpa_supplicant_s1g /usr/sbin/
cp wpa_supplicant-s1g-wlan0.conf /etc/wpa_supplicant/
cp wpa_supplicant-s1g-wlan0.service /etc/systemd/system/

# Install stub morse_cli
cp morse_cli /usr/local/bin/
chmod +x /usr/local/bin/morse_cli

# Enable and start
modprobe morse
systemctl daemon-reload
systemctl enable --now wpa_supplicant-s1g-wlan0
```
