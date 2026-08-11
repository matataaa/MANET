# MANET Mesh Node Architecture

## Hardware

| Component | Details |
|-----------|---------|
| Board | Raspberry Pi Compute Module 4 (Rev 1.1) |
| Kernel | 6.18.33-manet (custom) |
| Onboard WiFi | BCM43455 (brcmfmac) — 2.4/5 GHz, no mesh support |
| HaLow Radio | Morse Micro (SPI) — sub-GHz 802.11ah mesh |
| GPS | GPSD-compatible receiver |
| UPS | Waveshare UPS HAT (E) with battery monitoring |

## Network Interfaces

| Interface | Hardware | Role | Mode | Details |
|-----------|----------|------|------|---------|
| `end0` | Ethernet | Gateway / management | DHCP client | Uplinks to LAN for internet gateway |
| `wlan2` | Morse HaLow (SPI) | Mesh backhaul | 802.11s mesh point | Enslaved to bat0, SAE authenticated |
| `wlan3` | BCM43455 onboard | Client access point | AP (hostapd) | SSID: `RADIOx-<mac4>`, 5 GHz ch36, bridged to br0 |
| `bat0` | Virtual | BATMAN-ADV | L2 mesh routing | Aggregates mesh interfaces, bridged to br0 |
| `br0` | Virtual | Bridge | Bridge | Bridges bat0 + AP, carries 10.30.2.0/24 subnet |

### Interface Naming

Interfaces are renamed at boot by `manet-wlan-apply-link-names.service` using `.link` files. The naming scheme:
- `wlan0` = 2.4 GHz mesh (if present)
- `wlan1` = 5 GHz mesh (if present)
- `wlan2` = HaLow
- `wlan3` = non-mesh AP

Role files track assignments:
- `/var/lib/mesh_if` — mesh backhaul interface(s)
- `/var/lib/ap_interface` — AP interface name
- `/var/lib/halow_interface` — HaLow interface name

## Network Architecture

```
                        ┌──────────────────────────┐
  [EUD clients]         │        Mesh Node          │         [Other mesh nodes]
       │                │                           │                │
       │ WiFi 5GHz      │   ┌───────┐   ┌───────┐  │                │
       └───────────────►│   │ wlan3 │   │ wlan2 │◄─┼────── HaLow ──┘
                        │   │  AP   │   │ mesh  │  │         RF
                        │   └───┬───┘   └───┬───┘  │
                        │       │           │       │
                        │       ▼           ▼       │
                        │   ┌───────────────────┐   │
                        │   │       br0         │   │
                        │   │  10.30.2.0/24     │   │
                        │   └────────┬──────────┘   │
                        │       ┌────┴────┐         │
                        │       │  bat0   │         │
                        │       │ BATMAN  │         │
                        │       └─────────┘         │
                        │                           │
                        │   ┌───────┐               │
                        │   │ end0  │── LAN/GW ─────┼──► Internet
                        │   └───────┘               │
                        └──────────────────────────┘
```

## Services

### Web & API

| Service | Binary | Port | Description |
|---------|--------|------|-------------|
| `manet-ctrl` | Go | 80/443 | Unified web UI and REST API. Serves the SPA dashboard, WebSocket terminal, voice relay, applet management, performance tools (iperf3/ping), and all control endpoints. HTTPS with auto-generated self-signed cert. Supports host-header redirect for applet DNS names (e.g. `chat.mesh` → mesh-chat frontend). |
| `iperf3` | C | 5201 | Network throughput testing server — used by manet-ctrl for mesh link measurements. |

### Mesh Core

| Service | Binary | Description |
|---------|--------|-------------|
| `wpa_supplicant-s1g-wlan2` | Morse fork | HaLow 802.11ah mesh authentication using SAE (WPA3). Manages the S1G supplicant for the Morse radio. |
| `batman-enslave` | oneshot | Enslaves mesh interfaces (wlan2) to bat0 and configures the BATMAN-ADV layer-2 mesh. |
| `batman-enslave-watch` | shell | Watchdog that re-enslaves wlan2 to bat0 if the link drops or the interface is reset. |
| `alfred` | C | A.L.F.R.E.D. (Almighty Lightweight Fact Remote Exchange Daemon). Shares mesh-wide data between nodes over bat0 — topology info, node stats, GPS positions. |
| `sae-watchdog` | shell | Monitors `wpa_supplicant` instances and restarts them if mesh authentication stalls. |
| `mesh-boot-lobby` | oneshot | Sets mesh interfaces to a "lobby" channel at boot for initial peer discovery. |

### Go Daemons

| Service | Binary | Description |
|---------|--------|-------------|
| `mesh-registry` | Go | Node discovery registry. Collects local node info (hostname, IP, MAC, GPS, MCS rates, TQ average, Syncthing ID) and publishes to alfred type 68. Other nodes read this to build the mesh registry. |
| `node-manager` | Go | Manages radio state sync (ensures wpa_supplicant frequency matches config), static channel enforcement, gateway reconciliation, and runs election scripts (MTX, mumble). 15s main loop. |
| `mesh-manager` | Go | Consolidated mesh network services: IPv4 address allocation (chunk-based CIDR), `/etc/hosts` mesh hostname updates, `.mesh` DNS records via dnsmasq, gateway route management, default route fix, EUD DHCP pool configuration, and voice QoS setup. 30s periodic loop. |
| `gateway-manager` | Go | Detects internet-connected gateway node, manages NAT/masquerade rules, handles gateway election and failover across mesh. |
| `mesh-voice` | Go (CGO) | Push-to-talk voice over multicast RTP. Opus codec at 48kHz/32kbps. Supports PTT sources: OpenVLM HID USB, GPIO evdev, always-on, VOX. Half-duplex with remote-active detection. Jitter buffer for smooth playback. DSCP EF marking for QoS. |

### Voice QoS

Voice traffic is prioritized at two layers:

**DSCP marking** — `mesh-voice` and `manet-ctrl` (web voice relay) set IP TOS to `0xB8` (DSCP EF / Expedited Forwarding) on all voice UDP packets. batman-adv preserves the TOS field through encapsulation, mapping it to `skb->priority`. The wireless driver then maps this to WMM Access Category Voice (AC_VO), which gets the shortest contention window and highest priority on the air.

**tc qdisc** — `mesh-manager` configures a `prio` qdisc on `br0` at startup with three bands:
- Band 0 (highest): DSCP EF traffic (`match ip tos 0xb8 0xfc`) and voice multicast port 4370
- Band 1: interactive traffic
- Band 2 (lowest): bulk data

This ensures voice packets are dequeued before bulk traffic even under load.

### Applets

| Service | Binary | Description |
|---------|--------|-------------|
| `mesh-chat` | Go | Mesh-wide text chat over multicast. Installed as an applet via manet-ctrl. |

Applets are managed through the manet-ctrl API. Each applet has an `applet.json` manifest that can declare DNS records with scope:
- `local` — resolvable only by EUDs connected to this node's AP (via dnsmasq `address=` on br0)
- `global` — resolvable mesh-wide (published to all nodes via mesh-registry)

### DNS

| Component | Description |
|-----------|-------------|
| `dnsmasq` | Serves DHCP and DNS for EUD clients on br0. Configured with `local=/mesh/` to prevent `.mesh` queries from forwarding upstream. |
| `/etc/dnsmasq.d/mesh-names.conf` | Auto-generated by `mesh-manager`. Contains `address=/<hostname>.mesh/<ip>` entries for all mesh nodes and local-scoped applet DNS records. Updated every 30s; dnsmasq is restarted only when content changes. |
| `radio.mesh` | Always resolves to the local node's br0 IP — the default entry point for EUDs. |
| Applet DNS | e.g. `chat.mesh` — when an EUD browses to an applet hostname, manet-ctrl's host-header redirect sends them to the applet's frontend. |

### WiFi Access Point

| Service | Type | Description |
|---------|------|-------------|
| `hostapd` | long-running | Runs the WiFi access point on wlan3. Broadcasts SSID `RADIOx-<mac4>` on 5 GHz channel 36, WPA2-PSK. Bridges clients into br0. |
| `dnsmasq` | long-running | DHCP server and DNS cache for EUD clients connected to the AP. Serves IPs from the 10.30.2.0/24 pool. |
| `ap-interface-setup` | oneshot | Sets wlan3 to managed mode before hostapd starts. |
| `ap-txpower` | oneshot | Sets low TX power on the AP interface to limit range in lab/close-quarters deployments. |

### Networking

| Service | Type | Description |
|---------|------|-------------|
| `systemd-networkd` | long-running | Core network configuration — manages end0, br0, and static routes. |
| `systemd-resolved` | long-running | DNS resolution with local caching. |
| `radvd` | long-running | IPv6 router advertisement daemon — advertises the mesh prefix to clients. |
| `ebtables-restore` | oneshot | Restores Ethernet bridge firewall rules (layer-2 filtering on br0). |
| `nftables` | oneshot | Loads packet filtering / NAT rules. |

### Radio Management

| Service | Type | Description |
|---------|------|-------------|
| `manet-wlan-apply-link-names` | oneshot (early boot) | Renames wireless interfaces to consistent names (wlan0-3) based on MAC addresses and `.link` files. |
| `wifi-rfkill-unblock` | oneshot | Unblocks all WiFi interfaces via rfkill at boot. |
| `halow-txpower-wlan2` | oneshot | Sets the HaLow radio TX power for wlan2. |
| `manet-txpower` | oneshot | Sets TX power limits across all MANET radios. |

### Situational Awareness

| Service | Binary | Description |
|---------|--------|-------------|
| `gpsd` | C | GPS daemon — reads NMEA data from the GPS receiver and serves it on port 2947. |
| `gps-reader` | Go | Reads GPS position from gpsd and writes it to `/run/gps_status.json` for other services to consume. |
| `cot-emitter` | Go | Cursor on Target emitter — broadcasts GPS position as CoT XML to ATAK-compatible EUDs over multicast. Enables blue-force tracking. |

### File Sync

| Service | Binary | Description |
|---------|--------|-------------|
| `syncthing@radio` | Go | Decentralized file sync across mesh nodes. Runs as the radio user. |
| `syncthing-peer-manager` | shell | Manages Syncthing peer discovery across the mesh — adds/removes peers as nodes join/leave. |

### Hardware / Power

| Service | Type | Description |
|---------|------|-------------|
| `cpu-powersave` | oneshot | Sets CPU governor to powersave, caps frequency at 1.0 GHz. Reduces power draw for battery operation. |
| `battery-reader` | Go | Reads Waveshare UPS HAT (E) battery voltage/percentage via I2C. Disabled by default (enable via config). |
| `button-monitor` | shell | Watches for physical button presses on the enclosure and triggers LED info display. |
| `alsa-restore` | oneshot | Restores ALSA mixer state at boot (audio levels for mesh-voice). |

### Provisioning / Maintenance

| Service | Type | Description |
|---------|------|-------------|
| `radio-setup-run-once` | oneshot | First-boot provisioning — detects interfaces, creates all service files, configures mesh parameters. Runs once then disables itself. |
| `mesh-provision` | oneshot | Post-network provisioning tasks that require internet access. |
| `one-shot-time-sync` | oneshot | Forces an NTP time sync at boot (important for SAE authentication which is time-sensitive). |
| `chrony` | long-running | NTP client/server — keeps system clock synchronized. Acts as NTP server for mesh clients. |

## Boot Sequence

```
Power on
  │
  ▼
Boot 1 (first flash only)
  │  firstrun.sh extracts cm4-tools.tar.gz
  │  Creates radio user, installs base packages
  │  Writes /etc/mesh.conf with node configuration
  │  Reboots
  │
  ▼
Boot 2
  │  manet-wlan-apply-link-names (renames interfaces)
  │  wifi-rfkill-unblock
  │  radio-setup-run-once (detects hardware, creates all services)
  │    ├── Creates wpa_supplicant configs
  │    ├── Creates hostapd.conf
  │    ├── Creates batman-enslave.service
  │    ├── Creates node-manager, gateway-manager
  │    ├── Installs web UI and Go binaries
  │    └── Enables all services
  │  Reboots (interface renames take effect)
  │
  ▼
Steady State
  │  All services start normally
  │  HaLow mesh forms (wlan2 → bat0 → br0)
  │  AP broadcasts on wlan3
  │  Gateway detected and routes shared
  │  Alfred shares topology data
  │  mesh-manager allocates IPs, updates DNS, sets QoS
  │  manet-ctrl web UI available on :80/:443
  │  mesh-voice ready for PTT
```

## Configuration Files

| File | Purpose |
|------|---------|
| `/etc/mesh.conf` | Master node configuration — mesh SSID, keys, AP settings, network CIDR, service toggles |
| `/etc/hostapd/hostapd.conf` | WiFi AP configuration |
| `/etc/morse/morse.conf` | Morse HaLow driver config — BCF file, SPI clock speed |
| `/etc/systemd/network/*.link` | Interface rename rules (MAC → wlanN) |
| `/etc/dnsmasq.conf` | DHCP/DNS configuration |
| `/etc/dnsmasq.d/mesh-names.conf` | Auto-generated `.mesh` DNS records |
| `/etc/dnsmasq.d/mesh-eud.conf` | Auto-generated EUD DHCP pool config |
| `/var/lib/mesh_if` | Current mesh interface name(s) |
| `/var/lib/ap_interface` | Current AP interface name |
| `/var/lib/halow_interface` | Current HaLow interface name |
| `/var/run/mesh_node_registry` | Local cache of mesh-wide node registry (from alfred) |
| `/run/gps_status.json` | Current GPS fix data |
| `/run/mesh-voice-ptt.json` | Current PTT/TX/RX state for voice UI |

## Mesh Addressing

- Mesh subnet: `10.30.2.0/24`
- Each node gets a unique IP from the mesh via `mesh-manager` (chunk-based allocation)
- DHCP pool for EUD clients is calculated from `max_euds_per_node` setting
- Hostnames follow pattern: `mesh-<last4mac>` (e.g., `mesh-a773`, `mesh-a84d`)
- AP SSIDs follow pattern: `RADIOx-<last4mac>` (e.g., `RADIO1-a773`, `RADIO2-a84d`)
- `.mesh` TLD resolves via dnsmasq for EUD clients (e.g., `radio.mesh`, `chat.mesh`)
