# MANET Feature List

## Mesh Networking

- **batman-adv BATMAN_V** layer-2 mesh routing with ELP/OGMv2
- **802.11ah HaLow** sub-GHz mesh backhaul via Morse Micro radios (SPI) — long range, low power
- **802.11s mesh** with SAE (WPA3) authentication
- **Multi-radio support** — HaLow + 2.4 GHz + 5 GHz mesh interfaces simultaneously
- **Self-forming mesh** — nodes discover peers automatically on a lobby channel at boot
- **Self-healing** — batman-adv reroutes around failed links, SAE watchdog restarts stalled auth
- **batman-enslave watchdog** — re-enslaves interfaces to bat0 if the link drops

## Zero-Configuration Networking

- **Distributed IPv4 allocation** — chunk-based CIDR addressing, no central DHCP, conflict-free
- **Automatic gateway detection** — mesh nodes elect and route through the internet-connected gateway
- **NAT/masquerade** — gateway node provides internet access to the entire mesh
- **Default route management** — automatic failover when gateway changes
- **Mesh DNS** — `.mesh` TLD resolves via dnsmasq for all EUD clients (e.g. `radio.mesh`, `chat.mesh`)
- **Hostname resolution** — mesh-wide `/etc/hosts` updated every 30s from the node registry

## End User Device (EUD) Support

- **WiFi access point** — 5 GHz WPA2-PSK AP on a separate radio, bridged to the mesh
- **Wired Ethernet** — plug-and-play bridge mode for wired EUDs
- **DHCP** — automatic IP assignment for connected clients via dnsmasq
- **DNS for EUDs** — `.mesh` hostnames resolvable by connected devices, upstream queries forwarded
- **Applet DNS** — applets declare hostnames (e.g. `chat.mesh`) accessible from EUDs

## Web Interface (manet-ctrl)

- **Single-page application** — tabbed UI served from a single Go binary on port 80/443
- **Auto TLS** — self-signed HTTPS certificate generated on first run
- **Dashboard** — D3.js force-directed topology graph, node list with TQ/hops/last-seen, auto-refresh
- **Nodes** — sortable table of all mesh nodes with hostname, IP, DNS, TQ, hops, battery, uptime
- **Config** — view/edit node configuration (hostname, SSID, keys, network, services) with save/apply
- **Hardware** — radio interfaces (driver, channel, TX power, MCS rates), GPS, system info
- **Mesh** — batman-adv originators, neighbors, gateways with hostname resolution and last-seen timestamps, DNS records table
- **Voice** — PTT controls (web and hardware), TX/RX indicators, OpenVLM connection status, service management
- **Performance** — iperf3 throughput and ping latency testing with streaming results
- **Services** — systemd service management grouped by category, start/stop/restart controls
- **Terminal** — full xterm.js PTY shell over WebSocket, SSH to peer nodes, live journalctl log viewer
- **Applets** — install/uninstall applets, each gets its own sub-path and optional DNS hostname
- **Docs** — built-in documentation: overview, configuration, API reference, services, CLI

## Voice Communications

- **Push-to-talk** over multicast RTP with Opus codec (48 kHz, 32 kbps)
- **Hardware PTT** — OpenVLM USB HID device with GPIO3 button, GPIO evdev, always-on, VOX modes
- **Web PTT** — browser-based push-to-talk via WebSocket relay (requires HTTPS for mic access)
- **Half-duplex** — automatic remote-active detection prevents talking over incoming audio
- **Jitter buffer** — 60ms pre-buffer for smooth playback over mesh links
- **ALSA auto-detect** — finds OpenVLM sound card automatically

## Voice QoS

- **DSCP EF marking** — voice UDP packets tagged with TOS 0xB8 (Expedited Forwarding)
- **WMM prioritization** — DSCP maps through batman-adv to 802.11 Access Category Voice (AC_VO), shortest contention window
- **tc prio qdisc** — 3-band priority queue on br0: voice in band 0, bulk in band 2
- **Port-based filter** — multicast port 4370 matched to high-priority band as fallback

## Applet System

- **Installable applets** — Go or static frontend apps managed through the API
- **Manifest-driven** — `applet.json` declares binary, port, frontend, config, DNS records
- **DNS integration** — applets declare `.mesh` hostnames with local or global scope
- **Host-header redirect** — browsing to `chat.mesh` auto-redirects to the applet frontend
- **Lifecycle management** — install, uninstall, start, stop via API; DNS updated on change

## Mesh Registry

- **Alfred type 68** — JSON node info distributed mesh-wide via the Alfred DHT
- **Per-node data** — hostname, IP, MAC addresses, GPS coordinates, MCS rates, TQ average, Syncthing ID, uptime
- **MAC resolution** — mesh tab resolves raw batman-adv MACs to hostnames and IPs
- **Last-seen tracking** — timestamps with color-coded freshness (green/yellow/red)

## Situational Awareness

- **GPS integration** — gpsd reads NMEA, gps-reader publishes to `/run/gps_status.json`
- **Cursor on Target (CoT)** — broadcasts GPS position as CoT XML for ATAK/TAK blue-force tracking
- **Position in registry** — GPS coordinates shared mesh-wide via the node registry

## File Synchronization

- **Syncthing** — decentralized file sync across mesh nodes
- **Peer management** — automatic discovery and add/remove of Syncthing peers as nodes join/leave

## CLI Tool

- `mesh status` — node summary (hostname, IP, uptime, radios, battery)
- `mesh nodes` — mesh-wide node table (hostname, IP, TQ, hops, last seen)
- `mesh config show` — display all mesh.conf settings
- `mesh radio info` — radio interfaces with driver, channel, TX power, MCS
- `mesh services` — systemd service listing with status and category
- `mesh perf ping <target>` — ping a peer node
- `mesh reboot` — reboot the node

## Hardware Support

| Hardware | Status | Notes |
|----------|--------|-------|
| Raspberry Pi CM4 | Primary target | 802.11ax + HaLow |
| Raspberry Pi 5 | Functional | 802.11ax + HaLow |
| Radxa Rock 3A | Functional | 802.11ax + HaLow |

## Power Management

- **CPU powersave** — governor set to powersave, frequency capped at 1.0 GHz
- **Battery monitoring** — Waveshare UPS HAT (E) voltage/percentage via I2C (optional, disabled by default)
- **Low-power mesh** — HaLow radio operates at sub-GHz with configurable TX power

## Security

- **SAE (WPA3)** — mesh authentication with pre-shared key
- **TLS** — HTTPS with auto-generated certificates for web UI
- **Isolated mDNS** — prevents mDNS leaking between mesh and LAN segments
- **ebtables/nftables** — layer-2 and layer-3 firewall rules on the bridge
