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

### Mesh Core

| Service | Type | Description |
|---------|------|-------------|
| `wpa_supplicant-s1g-wlan2` | long-running | HaLow 802.11ah mesh authentication using SAE (WPA3). Manages the S1G supplicant for the Morse radio. |
| `batman-enslave` | oneshot | Enslaves mesh interfaces (wlan2) to bat0 and configures the BATMAN-ADV layer-2 mesh. |
| `batman-enslave-watch` | long-running | Watchdog that re-enslaves wlan2 to bat0 if the link drops or the interface is reset. |
| `alfred` | long-running | A.L.F.R.E.D. (Almighty Lightweight Fact Remote Exchange Daemon). Shares mesh-wide data between nodes over bat0 — topology info, node stats, GPS positions. |
| `sae-watchdog` | long-running | Monitors all `wpa_supplicant@wlan*.service` instances and restarts them if mesh authentication stalls. |
| `mesh-boot-lobby` | oneshot | Sets mesh interfaces to a "lobby" channel at boot for initial peer discovery before the mesh fully forms. |
| `node-manager` | long-running | Coordinates IPv4 address assignment across mesh nodes, manages gateway detection, and handles DHCP pool configuration. |

### WiFi Access Point

| Service | Type | Description |
|---------|------|-------------|
| `hostapd` | long-running | Runs the WiFi access point on wlan3. Broadcasts SSID `RADIOx-<mac4>` on 5 GHz channel 36, WPA2-PSK. Bridges clients into br0. |
| `dnsmasq` | long-running | DHCP server and DNS cache for EUD (End User Device) clients connected to the AP. Serves IPs from the 10.30.2.0/24 pool. |
| `ap-interface-setup` | oneshot | Sets wlan3 to managed mode before hostapd starts. |
| `ap-txpower` | oneshot | Sets low TX power on the AP interface to limit range in lab/close-quarters deployments. |

### Networking

| Service | Type | Description |
|---------|------|-------------|
| `systemd-networkd` | long-running | Core network configuration — manages end0, br0, and static routes. |
| `systemd-resolved` | long-running | DNS resolution with local caching. |
| `gateway-route-manager` | long-running | Polls every 10s to detect which mesh node has internet access (via end0) and manages default route across the mesh so all nodes can reach the internet through the gateway node. |
| `mesh-default-route-fix` | oneshot | Sets the correct default route after boot, ensuring traffic routes through the mesh gateway. |
| `radvd` | long-running | IPv6 router advertisement daemon — advertises the mesh prefix to clients. |
| `ebtables-restore` | oneshot | Restores Ethernet bridge firewall rules (layer-2 filtering on br0). |
| `nftables` | oneshot | Loads packet filtering / NAT rules. |
| `mesh-hosts-update` | timer (2min) | Periodically updates `/etc/hosts` with mesh node hostnames discovered via alfred/batman. |
| `mdns-isolate` | oneshot | Isolates mDNS traffic to prevent leaking between mesh and LAN segments. |

### Radio Management

| Service | Type | Description |
|---------|------|-------------|
| `manet-wlan-apply-link-names` | oneshot (early boot) | Renames wireless interfaces to consistent names (wlan0-3) based on MAC addresses and `.link` files. Uses a pre-rename MAC snapshot to handle rename ordering. |
| `wifi-rfkill-unblock` | oneshot | Unblocks all WiFi interfaces via rfkill at boot. |
| `halow-txpower-wlan2` | oneshot | Sets the HaLow radio TX power for wlan2. |
| `manet-txpower` | oneshot | Sets TX power limits across all MANET radios for lab/regulatory compliance. |

### Situational Awareness

| Service | Type | Description |
|---------|------|-------------|
| `gpsd` | long-running | GPS daemon — reads NMEA data from the GPS receiver and serves it on port 2947. |
| `gps-reader` | long-running | Reads GPS position from gpsd and writes it to `/run/gps_status.json` for other services. |
| `cot-emitter` | long-running | Cursor on Target emitter — broadcasts GPS position as CoT XML to ATAK-compatible EUDs over multicast. Enables blue-force tracking. |

### Web Interfaces

| Service | Port | Description |
|---------|------|-------------|
| `mesh-status` | 80 | Node management web UI — shows interface status, allows enable/disable of radios, TX power control. |
| `perf-dashboard` | 8081 | MANET//PERF dashboard — topology map, radio config, performance measurement (iperf3), session management. Reads data from alfred. |
| `iperf3` | 5201 | Network throughput testing server — used by perf-dashboard for mesh link measurements. |

### Hardware / Power

| Service | Type | Description |
|---------|------|-------------|
| `cpu-powersave` | oneshot | Sets CPU governor to powersave, caps frequency at 1.0 GHz. Optionally disables CPU cores 2-3 if hotplug is available. Reduces power draw for battery operation. |
| `battery-reader` | long-running | Reads Waveshare UPS HAT (E) battery voltage/percentage via I2C and exposes it for monitoring. |
| `button-monitor` | long-running | Watches for physical button presses on the enclosure and triggers LED info display. |

### Provisioning / Maintenance

| Service | Type | Description |
|---------|------|-------------|
| `radio-setup-run-once` | oneshot | First-boot provisioning — detects interfaces, creates all service files, configures mesh parameters, installs packages. Runs once then disables itself. |
| `mesh-provision` | oneshot | Post-network provisioning tasks that require internet access (package downloads, updates). |
| `mesh-clone-identity` | oneshot | Clones mesh identity (keys, hostname) during initial setup. |
| `syncthing-peer-manager` | long-running | Manages Syncthing peer discovery across the mesh for file synchronization between nodes. |
| `one-shot-time-sync` | oneshot | Forces an NTP time sync at boot (important for SAE authentication which is time-sensitive). |
| `chrony` | long-running | NTP client/server — keeps system clock synchronized. Acts as NTP server for mesh clients. |
| `ssh-recovery` | oneshot | Ensures SSH remains accessible even if other services fail — safety net for remote recovery. |

### System (standard)

| Service | Description |
|---------|-------------|
| `systemd-journald` | System logging |
| `systemd-udevd` | Device event manager |
| `dbus` | System message bus |
| `polkit` | Authorization manager |
| `avahi-daemon` | mDNS/DNS-SD (Bonjour) |
| `cron` | Scheduled tasks |
| `ssh` | OpenSSH server |
| `ModemManager` | Cellular modem management (if attached) |
| `bluetooth` | Bluetooth stack |

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
  │    ├── Creates node-manager, gateway-route-manager
  │    ├── Installs web dashboards
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
  │  Web UIs available on :80 and :8081
```

## Configuration Files

| File | Purpose |
|------|---------|
| `/etc/mesh.conf` | Master node configuration — mesh SSID, keys, AP settings, network CIDR |
| `/etc/hostapd/hostapd.conf` | WiFi AP configuration |
| `/etc/morse/morse.conf` | Morse HaLow driver config — BCF file, SPI clock speed |
| `/etc/systemd/network/*.link` | Interface rename rules (MAC → wlanN) |
| `/etc/dnsmasq.conf` | DHCP/DNS configuration |
| `/var/lib/mesh_if` | Current mesh interface name(s) |
| `/var/lib/ap_interface` | Current AP interface name |
| `/var/lib/halow_interface` | Current HaLow interface name |

## Mesh Addressing

- Mesh subnet: `10.30.2.0/24`
- Each node gets a unique IP from the mesh via `node-manager`
- DHCP pool for EUD clients is calculated from `max_euds_per_node` setting
- Hostnames follow pattern: `mesh-<last4mac>` (e.g., `mesh-a773`, `mesh-a84d`)
- AP SSIDs follow pattern: `RADIOx-<last4mac>` (e.g., `RADIO1-a773`, `RADIO2-a84d`)
