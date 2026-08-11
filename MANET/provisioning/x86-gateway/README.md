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

## l2 socket / bat0 enslavement ordering

wpa_supplicant_s1g opens a raw l2 socket on wlan0 to send and receive SAE
authentication frames. If wlan0 is enslaved to bat0 before SAE peering
completes, bat0 intercepts the socket and wpa_supplicant can no longer
receive auth responses. The gateway gets stuck retransmitting SAE commits
with no reply, and the mesh never forms.

**The fix**: never enslave wlan0 to bat0 until at least one peer reaches
`mesh plink: ESTAB` in `iw dev wlan0 station dump`. The boot script
(`halow-mesh-start.sh`) polls for this for up to 60 seconds.

Any restart of wpa_supplicant while wlan0 is enslaved has the same problem.
Use `halow-mesh-restart.sh` which detaches from bat0 first, restarts wpa,
waits for ESTAB, then re-enslaves.

## Mesh watchdog

`halow-mesh-watchdog.service` runs a loop that checks `batctl bat0 n` every
60 seconds. If zero batman neighbors are seen for 3 consecutive checks
(3 minutes), it triggers `halow-mesh-restart.sh` for a clean recovery.
It also restarts the mesh if wpa_supplicant_s1g is not running.

### Gateway scripts

| Script | Purpose |
|--------|---------|
| `/usr/local/bin/halow-mesh-start.sh` | Boot-time setup: load modules, start wpa, wait for ESTAB, enslave to bat0 |
| `/usr/local/bin/halow-mesh-restart.sh` | Clean restart: detach bat0, kill wpa, restart, wait for ESTAB, re-enslave |
| `/usr/local/bin/halow-mesh-watchdog.sh` | Watchdog loop: detect zero neighbors and trigger restart |

### Gateway services

| Service | Type | Description |
|---------|------|-------------|
| `halow-mesh.service` | oneshot | Runs `halow-mesh-start.sh` at boot |
| `halow-mesh-watchdog.service` | simple | Runs the watchdog, auto-restarts on failure |

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

# Install mesh scripts
cp halow-mesh-start.sh halow-mesh-restart.sh halow-mesh-watchdog.sh /usr/local/bin/
chmod +x /usr/local/bin/halow-mesh-*.sh

# Install services
cp halow-mesh.service halow-mesh-watchdog.service /etc/systemd/system/

# Enable and start
modprobe morse
systemctl daemon-reload
systemctl enable --now halow-mesh halow-mesh-watchdog
```
