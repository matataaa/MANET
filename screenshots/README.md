# MANET//CTRL Screenshots

Screenshots of the MANET//CTRL web interface running on a 3-node HaLow mesh (802.11ah) with dark theme.

## Tabs

| # | Screenshot | Tab | Description |
|---|-----------|-----|-------------|
| 1 | ![Dashboard](01-dashboard-topology.png) | Dashboard | D3.js force-directed topology showing 3 mesh nodes with node list sidebar. Auto-refreshes every 15s. |
| 2 | ![Nodes](02-nodes-table.png) | Nodes | Table of all mesh nodes with hostname, IP, DNS (.local), TQ, hops, services, battery, uptime, last seen. Sortable columns. |
| 3 | ![Loading](03-dashboard-loading.png) | Dashboard | Loading splash shown while connecting to a node's API. |
| 4 | ![Config](04-config-viewer.png) | Config | Node and network configuration viewer. Hostname, SSID, regulatory domain, EUD mode, services. Supports stage/activate/cancel workflow. |
| 5 | ![Hardware](05-hardware-info.png) | Hardware | System info, radio interfaces (driver, channel, TX power, MCS), network interfaces, GPS status/coordinates. |
| 6 | ![Voice](06-voice-ptt.png) | Voice | Mesh voice (PTT) service status. Shows OpenVLM PTT hardware, voice client, service controls. |
| 7 | ![Perf](07-perf-iperf3.png) | Perf | Performance testing with iperf3 throughput, ping latency. Sub-tabs: Measure, Radio, Ping. |
| 8 | ![Services](08-services-overview.png) | Services | All systemd services grouped by category (Core Mesh, Network, Radio, Applications, System). Start/stop/restart controls. 16/16 running. |
| 9 | ![Mesh](09-mesh-batman.png) | Mesh | Raw batman-adv data: bat0 interface state, mesh stats, direct neighbors, gateways, originators table. |
| 10 | ![Terminal](10-terminal-logs.png) | Terminal | Live log viewer (journalctl stream) with Shell and Logs sub-tabs. Shows node-manager, registry, gateway route manager activity. |
| 11 | ![Applets](11-applets-meshchat.png) | Applets | Installable mesh applets. Shows Mesh Chat (v1.0.0, Go) running with open/config/start/stop/restart/disable/logs/uninstall controls. |
| 12 | ![Docs](12-docs-overview.png) | Docs | Built-in documentation with sub-tabs: Overview, Configuration, API Reference, Services, MESH CLI, OpenVLM. Shows architecture and tab reference. |
