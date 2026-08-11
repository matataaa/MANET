// Docs tab — tabbed documentation: Overview, Config, API, Services, CLI
var docsInitialized = false;
var docsActiveTab = 'overview';

function docsActivate() {
  if (docsInitialized) return;
  var panel = document.getElementById('tab-docs');
  panel.innerHTML =
    '<div class="docs-wrap">' +
      '<nav class="docs-tabs" id="docs-tabs"></nav>' +
      '<div class="docs-body" id="docs-body"></div>' +
    '</div>';

  var tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'config', label: 'Configuration' },
    { id: 'api', label: 'API Reference' },
    { id: 'services', label: 'Services' },
    { id: 'cli', label: 'MESH CLI' }
  ];

  var nav = document.getElementById('docs-tabs');
  tabs.forEach(function(t) {
    var btn = document.createElement('button');
    btn.className = 'docs-tab-btn' + (t.id === docsActiveTab ? ' active' : '');
    btn.textContent = t.label;
    btn.dataset.tab = t.id;
    btn.addEventListener('click', function() { docsSwitchTab(t.id); });
    nav.appendChild(btn);
  });

  docsInitialized = true;
  docsSwitchTab(docsActiveTab);
}

function docsSwitchTab(id) {
  docsActiveTab = id;
  document.querySelectorAll('.docs-tab-btn').forEach(function(b) {
    b.classList.toggle('active', b.dataset.tab === id);
  });
  var body = document.getElementById('docs-body');
  var content = DOCS_TABS[id] || '';
  body.innerHTML = content;
}

var DOCS_TABS = {};

DOCS_TABS.overview = [
'<h2>MANET//CTRL</h2>',
'<p>Web interface and API server for mesh network nodes. Runs as a single Go binary (<code>manet-ctrl</code>) on port 80, serving the web UI, REST API, and WebSocket terminals.</p>',
'<p>The node runs batman-adv (BATMAN_V) L2 mesh routing over 802.11ah (HaLow) and/or standard WiFi radios on ARM64 SBCs. Node state is shared via Alfred data distribution.</p>',

'<h3>Web Interface Tabs</h3>',
'<table class="docs-table"><thead><tr><th>Tab</th><th>Purpose</th></tr></thead><tbody>',
'<tr><td><strong>Dashboard</strong></td><td>Topology visualization (D3.js force-directed graph), node list with TQ, hop count, last seen. Auto-refreshes every 15s.</td></tr>',
'<tr><td><strong>Nodes</strong></td><td>Full table of all mesh nodes — hostname, IP, DNS (.mesh), TQ, hops, battery, uptime, last seen. Sortable columns.</td></tr>',
'<tr><td><strong>Config</strong></td><td>View and edit node configuration. Hostname, SSID, keys, network, regulatory domain, EUD mode, services.</td></tr>',
'<tr><td><strong>Hardware</strong></td><td>Radio interfaces (driver, channel, TX power, MCS), network interfaces, GPS status/coordinates, system info.</td></tr>',
'<tr><td><strong>Voice</strong></td><td>Mesh voice (PTT) service status — active/inactive, uptime, multicast address.</td></tr>',
'<tr><td><strong>Perf</strong></td><td>Performance testing — iperf3 throughput, ping latency, streaming results.</td></tr>',
'<tr><td><strong>Services</strong></td><td>All systemd services grouped by category. Start/stop/restart controls.</td></tr>',
'<tr><td><strong>Mesh</strong></td><td>Raw batman-adv data — originators, neighbors, gateways, interface membership.</td></tr>',
'<tr><td><strong>Terminal</strong></td><td>Full xterm.js shell (PTY over WebSocket) or live log viewer (journalctl stream). SSH to peer nodes.</td></tr>',
'<tr><td><strong>Docs</strong></td><td>This documentation.</td></tr>',
'</tbody></table>',

'<h3>Architecture</h3>',
'<p>Each mesh node is an ARM64 SBC (CM4, RPi5, Rock3A) running a Morse Micro HaLow radio (802.11ah, SPI) and optionally a standard WiFi radio (SDIO/USB). The mesh uses batman-adv in BATMAN_V mode over the HaLow link. An ethernet bridge (<code>br0</code>) joins <code>bat0</code> with wired interfaces for EUD connectivity.</p>',
'<p>Configuration is stored in <code>/etc/mesh.conf</code> as key=value pairs. The <code>radio-setup.sh</code> script reads this file on boot to configure interfaces, WPA supplicant, hostapd, and all dependent services.</p>',
].join('\n');

DOCS_TABS.config = [
'<h2>Configuration</h2>',
'<p>Node configuration is stored in <code>/etc/mesh.conf</code> as key=value pairs. It can be edited via the web UI Config tab, the <code>mesh</code> CLI, or by editing the file directly. Most changes require a reboot or service restart to take effect.</p>',

'<h3>UI Configuration</h3>',
'<p>The <strong>Config</strong> tab in the web interface provides a form-based editor for all common settings.</p>',
'<table class="docs-table"><thead><tr><th>Action</th><th>How</th></tr></thead><tbody>',
'<tr><td>View current config</td><td>Open the Config tab — settings are grouped into Node, Network, Services, and Access Point sections.</td></tr>',
'<tr><td>Edit config</td><td>Click <strong>Edit Configuration</strong>. All editable fields appear with dropdowns for constrained values (EUD mode, regulatory domain, service toggles).</td></tr>',
'<tr><td>Save changes</td><td>Click <strong>Save</strong>. Changes are written to <code>/etc/mesh.conf</code> and relevant services are restarted (hostname, AP, mesh). A reboot may still be needed for some settings.</td></tr>',
'<tr><td>Cancel</td><td>Click <strong>Cancel</strong> to discard unsaved edits.</td></tr>',
'</tbody></table>',
'<p>The Config tab exposes these fields: hostname prefix, mesh SSID, mesh key, IPv4 network, regulatory domain, EUD mode, AP SSID, AP key, max EUDs per node, ACS, MediaMTX, Mumble, auto update, and admin password.</p>',
'<p>Settings not exposed in the UI (e.g. <code>halow_regulatory_domain</code>) must be set via CLI or direct file edit.</p>',

'<h3>CLI Configuration</h3>',
'<p>The <code>mesh</code> CLI tool and direct file editing provide full access to all settings, including those not available in the UI.</p>',
'<table class="docs-table"><thead><tr><th>Action</th><th>Command</th></tr></thead><tbody>',
'<tr><td>View all settings</td><td><code>mesh config show</code></td></tr>',
'<tr><td>Edit config file</td><td><code>sudo nano /etc/mesh.conf</code></td></tr>',
'<tr><td>Apply changes</td><td>Reboot the node: <code>mesh reboot</code> or <code>sudo reboot</code></td></tr>',
'<tr><td>View a single key</td><td><code>grep "^key=" /etc/mesh.conf</code></td></tr>',
'<tr><td>Set a single key</td><td><code>sudo sed -i "s/^key=.*/key=value/" /etc/mesh.conf</code></td></tr>',
'</tbody></table>',
'<p>Example — change EUD mode to wireless and reboot:</p>',
'<pre class="docs-pre">sudo sed -i "s/^eud=.*/eud=wireless/" /etc/mesh.conf\nmesh reboot</pre>',
'<p>Example — view current config:</p>',
'<pre class="docs-pre">$ mesh config show\nadmin_password                  =\nacs                            = n\nauto_update                    = y\neud                            = wired\nipv4_network                   = 10.30.2.0/24\nlan_ap_channel                 = 36\nlan_ap_ssid                    = MANET-AP\nmesh_key                       = ********\nmesh_ssid                      = MESH\n...</pre>',

'<h3>Settings Reference</h3>',
'<p>Complete list of all <code>/etc/mesh.conf</code> keys.</p>',

'<h4>Node Identity</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>node_hostname</code></td><td>string</td><td>Yes</td><td>Hostname prefix. Full hostname is generated as <code>{prefix}-{mesh_ssid}-{mac_suffix}</code>.</td></tr>',
'</tbody></table>',

'<h4>Mesh Network</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>mesh_ssid</code></td><td>string</td><td>Yes</td><td>SSID for the batman-adv mesh network. All nodes must share the same SSID and key to peer.</td></tr>',
'<tr><td><code>mesh_key</code></td><td>string (8+ chars)</td><td>Yes</td><td>WPA3-SAE passphrase for mesh authentication.</td></tr>',
'<tr><td><code>ipv4_network</code></td><td>CIDR (e.g. <code>10.30.2.0/24</code>)</td><td>Yes</td><td>IPv4 subnet for the mesh. Each node auto-assigns a unique IP within this range via mesh-manager.</td></tr>',
'<tr><td><code>regulatory_domain</code></td><td><code>US</code>, <code>EU</code>, <code>JP</code>, <code>AU</code></td><td>Yes</td><td>WiFi regulatory domain. Sets allowed channels and TX power limits for standard WiFi radios.</td></tr>',
'<tr><td><code>halow_regulatory_domain</code></td><td><code>US</code>, <code>EU</code>, <code>JP</code>, <code>AU</code></td><td>No</td><td>Regulatory domain for 802.11ah HaLow radios. Controls sub-1GHz channel plan and power limits.</td></tr>',
'</tbody></table>',

'<h4>Access Point (EUD)</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>eud</code></td><td><code>wired</code>, <code>wireless</code>, <code>both</code>, <code>auto</code></td><td>Yes</td><td>End-user device connectivity mode. <strong>wired</strong>: EUDs connect via ethernet only, hostapd disabled. <strong>wireless</strong>: hostapd enabled, EUDs connect via AP. <strong>both</strong>: wired and wireless simultaneously. <strong>auto</strong>: ethernet-autodetect manages hostapd based on cable state.</td></tr>',
'<tr><td><code>lan_ap_ssid</code></td><td>string</td><td>Yes</td><td>SSID broadcast by the access point for EUD WiFi connections.</td></tr>',
'<tr><td><code>lan_ap_key</code></td><td>string (8+ chars)</td><td>Yes</td><td>WPA2/WPA3 passphrase for the EUD access point.</td></tr>',
'<tr><td><code>lan_ap_channel</code></td><td>integer</td><td>Yes</td><td>WiFi channel for the access point (e.g. 36 for 5GHz, 6 for 2.4GHz).</td></tr>',
'<tr><td><code>lan_ap_bw</code></td><td><code>20</code>, <code>40</code>, <code>80</code></td><td>Yes</td><td>Channel bandwidth in MHz for the access point.</td></tr>',
'<tr><td><code>max_euds_per_node</code></td><td>integer</td><td>Yes</td><td>Maximum number of EUD clients allowed on this node\'s AP.</td></tr>',
'</tbody></table>',

'<h4>Services</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>mumble</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Enable Mumble voice server election. When enabled, nodes elect a Mumble server via quorum.</td></tr>',
'<tr><td><code>mtx</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Enable MediaMTX (RTSP/WebRTC) server election for video streaming.</td></tr>',
'<tr><td><code>acs</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Auto Channel Selection. When enabled, node-manager periodically scans for interference and coordinates channel changes across the mesh.</td></tr>',
'<tr><td><code>auto_update</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Allow the node to accept over-the-air updates via the mesh.</td></tr>',
'</tbody></table>',

'<h4>Security</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>admin_password</code></td><td>string</td><td>Yes</td><td>Password for admin operations (config staging/activation). Leave empty to disable.</td></tr>',
'</tbody></table>',
].join('\n');

DOCS_TABS.api = [
'<h2>API Reference</h2>',
'<p>All endpoints return JSON. Unauthenticated unless noted.</p>',

'<h3>Status</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/data</code></td><td>Full mesh topology — all nodes, edges, neighbors, gateways, TQ values</td></tr>',
'<tr><td>GET</td><td><code>/api/local</code></td><td>This node\'s detailed state — hostname, IP, interfaces, GPS, battery, services</td></tr>',
'<tr><td>GET</td><td><code>/api/peer/{ip}</code></td><td>Proxy to a peer node\'s <code>/api/local</code></td></tr>',
'<tr><td>GET</td><td><code>/api/voice</code></td><td>Mesh voice service status</td></tr>',
'<tr><td>GET</td><td><code>/api/admin/status</code></td><td>Config state — current config, pending changes, node ACKs</td></tr>',
'<tr><td>GET</td><td><code>/api/services</code></td><td>All registered services with systemd status, category, and available actions</td></tr>',
'<tr><td>GET</td><td><code>/api/mesh</code></td><td>Raw batman-adv data (bat0 info, gateways, neighbors, originators)</td></tr>',
'</tbody></table>',

'<h3>Control</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/control/interface</code></td><td><code>{"name":"wlan2","action":"up"}</code></td><td>Bring interface up or down</td></tr>',
'<tr><td>POST</td><td><code>/api/control/txpower</code></td><td><code>{"interface":"wlan2","dbm":20}</code></td><td>Set TX power (dBm)</td></tr>',
'<tr><td>POST</td><td><code>/api/control/halow_channel</code></td><td><code>{"channel":36}</code></td><td>Set HaLow channel</td></tr>',
'<tr><td>POST</td><td><code>/api/control/wifi_channel</code></td><td><code>{"interface":"wlan3","channel":6}</code></td><td>Set WiFi channel</td></tr>',
'<tr><td>POST</td><td><code>/api/control/hostname</code></td><td><code>{"hostname":"NODE1"}</code></td><td>Set system hostname</td></tr>',
'</tbody></table>',

'<h3>Admin</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/admin/save</code></td><td>Save config changes to this node (applies hostname, restarts AP/mesh as needed)</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/stage</code></td><td>Stage a config package for deployment to all nodes</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/activate</code></td><td>Activate staged config across the mesh</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/cancel</code></td><td>Cancel pending staged config</td></tr>',
'</tbody></table>',

'<h3>Performance</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/perf/iperf/server</code></td><td>Start iperf3 server on this node</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/iperf/client</code></td><td>Run iperf3 client test to a target</td></tr>',
'<tr><td>GET</td><td><code>/api/perf/iperf/stream</code></td><td>Stream iperf3 output (SSE)</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/iperf/stop</code></td><td>Stop iperf3</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/ping/run</code></td><td>Run ping test to a target</td></tr>',
'<tr><td>GET</td><td><code>/api/perf/ping/stream</code></td><td>Stream ping output (SSE)</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/ping/stop</code></td><td>Stop ping</td></tr>',
'</tbody></table>',

'<h3>Service Actions</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/services/{id}</code></td><td><code>{"action":"restart"}</code></td><td>Start, stop, restart, or reload a systemd service by registry ID</td></tr>',
'</tbody></table>',

'<h3>WebSocket</h3>',
'<table class="docs-table"><thead><tr><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>/ws/terminal</code></td><td>PTY shell session. Binary frames for terminal I/O. Send 5-byte resize: <code>[0x01, cols_hi, cols_lo, rows_hi, rows_lo]</code>. Query params: <code>target</code>, <code>protocol</code>, <code>user</code>, <code>password</code> for SSH to remote nodes.</td></tr>',
'<tr><td><code>/ws/logs</code></td><td>Live journalctl stream. Query params: <code>unit</code> (filter to service), <code>lines</code> (initial backlog, default 200).</td></tr>',
'</tbody></table>',
].join('\n');

DOCS_TABS.services = [
'<h2>Node Services</h2>',
'<p>Services running on each mesh node, managed via systemd. View and control them from the Services tab.</p>',

'<h3>Core Mesh</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>MANET Controller</td><td><code>manet-ctrl</code></td><td>Web UI (SPA), REST API, WebSocket terminal, voice relay. Single Go binary on port 80/443 (auto TLS). Hosts applet frontends and handles <code>.mesh</code> hostname redirects.</td></tr>',
'<tr><td>Alfred</td><td><code>alfred</code></td><td>Distributed data store for batman-adv. Shares node state across the mesh via type 68 JSON records.</td></tr>',
'<tr><td>Mesh Registry</td><td><code>mesh-registry</code></td><td>Collects local node info (hostname, IP, MAC, GPS, MCS rates, TQ, Syncthing ID) and publishes to Alfred. Reads remote node data to build the mesh-wide registry.</td></tr>',
'<tr><td>Node Manager</td><td><code>node-manager</code></td><td>Radio state sync (ensures wpa_supplicant frequencies match config), static channel enforcement, gateway reconciliation, and service elections (MTX, Mumble). 15s main loop.</td></tr>',
'<tr><td>Mesh Manager</td><td><code>mesh-manager</code></td><td>IPv4 address allocation (chunk-based CIDR), <code>/etc/hosts</code> mesh hostname updates, <code>.mesh</code> DNS records via dnsmasq, gateway route management, default route fix, EUD DHCP pool config, and voice QoS setup. 30s periodic loop.</td></tr>',
'<tr><td>Gateway Manager</td><td><code>gateway-manager</code></td><td>Detects internet-connected gateway node, manages NAT/masquerade rules, handles gateway election and failover across mesh.</td></tr>',
'<tr><td>Boot Lobby</td><td><code>mesh-boot-lobby</code></td><td>Sets mesh interfaces to a lobby channel at boot for initial peer discovery.</td></tr>',
'</tbody></table>',

'<h3>Network</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>WPA Supplicant (S1G)</td><td><code>wpa_supplicant-s1g-wlan2</code></td><td>HaLow 802.11ah mesh authentication using SAE (WPA3). Morse Micro fork of wpa_supplicant for the S1G radio.</td></tr>',
'<tr><td>BATMAN Enslave</td><td><code>batman-enslave</code></td><td>Enslaves mesh interfaces (wlan2) to bat0 and configures the BATMAN-ADV layer-2 mesh.</td></tr>',
'<tr><td>BATMAN Watch</td><td><code>batman-enslave-watch</code></td><td>Watchdog that re-enslaves wlan2 to bat0 if the link drops or the interface is reset.</td></tr>',
'<tr><td>hostapd</td><td><code>hostapd</code></td><td>Access point for EUD connections on wlan3. 5 GHz, WPA2-PSK, bridged to br0.</td></tr>',
'<tr><td>dnsmasq</td><td><code>dnsmasq</code></td><td>DHCP and DNS for EUD clients. Serves <code>.mesh</code> hostnames via auto-generated <code>address=</code> records. <code>local=/mesh/</code> prevents upstream forwarding.</td></tr>',
'<tr><td>SAE Watchdog</td><td><code>sae-watchdog</code></td><td>Monitors mesh authentication health. Restarts wpa_supplicant on persistent SAE failures.</td></tr>',
'</tbody></table>',

'<h3>Voice & QoS</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Mesh Voice</td><td><code>mesh-voice</code></td><td>Push-to-talk voice over multicast RTP. Opus 48kHz/32kbps. Supports OpenVLM HID USB, GPIO evdev, always-on, and VOX PTT modes. Half-duplex with remote-active detection. 60ms jitter buffer for smooth playback.</td></tr>',
'</tbody></table>',
'<p><strong>Voice QoS:</strong> Voice packets are marked with DSCP EF (Expedited Forwarding, TOS 0xB8) on the UDP socket. batman-adv preserves this through encapsulation, mapping to WMM Access Category Voice (AC_VO) on the wireless interface — shortest contention window, highest air-time priority.</p>',
'<p>Additionally, <code>mesh-manager</code> configures a <code>tc prio</code> qdisc on <code>br0</code> at startup that puts DSCP EF traffic and voice multicast (port 4370) in band 0 (highest priority). Bulk data goes to band 2. This ensures voice packets are dequeued before other traffic under load.</p>',

'<h3>Applications & Applets</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Mesh Chat</td><td><code>mesh-chat</code></td><td>Mesh-wide text chat over multicast. Installed as an applet with DNS record <code>chat.mesh</code>.</td></tr>',
'<tr><td>Mumble Server</td><td><code>mumble-server</code></td><td>Full-duplex voice server. Elected via quorum when <code>mumble=y</code>.</td></tr>',
'<tr><td>MediaMTX</td><td><code>mediamtx</code></td><td>RTSP/WebRTC media server for video streaming. Elected when <code>mtx=y</code>.</td></tr>',
'<tr><td>Syncthing</td><td><code>syncthing@radio</code></td><td>Decentralized file synchronization across mesh nodes.</td></tr>',
'</tbody></table>',
'<p><strong>Applet DNS:</strong> Applets declare DNS records in their <code>applet.json</code> manifest with a scope: <code>local</code> (only this node\'s EUDs) or <code>global</code> (mesh-wide). When an EUD browses to an applet hostname (e.g. <code>chat.mesh</code>), manet-ctrl redirects to the applet\'s frontend.</p>',

'<h3>Situational Awareness</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GPSD</td><td><code>gpsd</code></td><td>GPS daemon — reads NMEA data from the GPS receiver, serves on port 2947.</td></tr>',
'<tr><td>GPS Reader</td><td><code>gps-reader</code></td><td>Reads GPS position from gpsd and writes <code>/run/gps_status.json</code> for other services.</td></tr>',
'<tr><td>CoT Emitter</td><td><code>cot-emitter</code></td><td>Cursor on Target emitter — broadcasts GPS position as CoT XML to ATAK-compatible EUDs over multicast. Enables blue-force tracking.</td></tr>',
'</tbody></table>',

'<h3>System</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Chrony NTP</td><td><code>chronyd</code></td><td>Network time synchronization. Uses mesh peers as NTP sources when no external server is reachable.</td></tr>',
'<tr><td>Battery Reader</td><td><code>battery-reader</code></td><td>Monitors Waveshare UPS HAT (E) battery voltage/percentage via I2C. Disabled by default; enable via Config tab.</td></tr>',
'<tr><td>CPU Powersave</td><td><code>cpu-powersave</code></td><td>Sets CPU governor to powersave and caps frequency at 1.0 GHz for battery operation.</td></tr>',
'<tr><td>Button Monitor</td><td><code>button-monitor</code></td><td>Watches physical button presses on the enclosure, triggers LED info display.</td></tr>',
'</tbody></table>',

'<h3>Radio Management</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Interface Names</td><td><code>manet-wlan-apply-link-names</code></td><td>Renames wireless interfaces to consistent names (wlan0-3) based on MAC addresses at early boot.</td></tr>',
'<tr><td>RF Unblock</td><td><code>wifi-rfkill-unblock</code></td><td>Unblocks all WiFi interfaces via rfkill at boot.</td></tr>',
'<tr><td>HaLow TX Power</td><td><code>halow-txpower-wlan2</code></td><td>Sets the HaLow radio TX power for wlan2.</td></tr>',
'<tr><td>MANET TX Power</td><td><code>manet-txpower</code></td><td>Sets TX power limits across all MANET radios.</td></tr>',
'</tbody></table>',
].join('\n');

DOCS_TABS.cli = [
'<h2>MESH CLI</h2>',
'<p>The <code>mesh</code> command-line tool is installed at <code>/usr/local/bin/mesh</code>. It talks to the local manet-ctrl API and can be used from the Terminal tab, over SSH, or on the console.</p>',

'<h3>Commands</h3>',
'<table class="docs-table"><thead><tr><th>Command</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>mesh status</code></td><td>Node summary — hostname, IP, uptime, radios, battery</td></tr>',
'<tr><td><code>mesh nodes</code></td><td>All mesh nodes table — hostname, IP, TQ, hops, last seen</td></tr>',
'<tr><td><code>mesh config show</code></td><td>Display all current mesh.conf settings</td></tr>',
'<tr><td><code>mesh radio info</code></td><td>Radio interfaces — driver, channel, TX power, MCS, addresses</td></tr>',
'<tr><td><code>mesh radio txpower &lt;iface&gt; &lt;dbm&gt;</code></td><td>Set TX power on an interface</td></tr>',
'<tr><td><code>mesh radio interface &lt;iface&gt; up|down</code></td><td>Bring a radio interface up or down</td></tr>',
'<tr><td><code>mesh gps</code></td><td>GPS fix status and coordinates</td></tr>',
'<tr><td><code>mesh services</code></td><td>List all services with status, category, enabled state</td></tr>',
'<tr><td><code>mesh perf ping &lt;target&gt;</code></td><td>Ping a peer node (10 packets)</td></tr>',
'<tr><td><code>mesh reboot</code></td><td>Reboot the node</td></tr>',
'<tr><td><code>mesh help</code></td><td>Show usage and available commands</td></tr>',
'</tbody></table>',

'<h3>Examples</h3>',
'<p><code>mesh status</code> — quick check of this node:</p>',
'<pre class="docs-pre">Hostname:  RADIO2-MESH-9ef7a84d\nIP:        10.30.2.76\nMAC:       c6:b3:46:11:3b:b3\nUptime:    2h 14m\nRadio:     wlan2 [UP] morse_spi ch36 (5180 MHz) tx=27.00 dBm</pre>',

'<p><code>mesh nodes</code> — see all mesh peers:</p>',
'<pre class="docs-pre">HOSTNAME                 IP               TQ       HOPS   LAST SEEN\n--------------------------------------------------------------------\nRADIO1-MESH-a1b2c3d4     10.30.2.75       245      1      3s\nRADIO2-MESH-9ef7a84d (me 10.30.2.76       --       --     --</pre>',

'<p><code>mesh services</code> — list running services:</p>',
'<pre class="docs-pre">SERVICE              STATUS       CATEGORY     ENABLED\n--------------------------------------------------------\nMANET Controller     running      core         yes\nAlfred               running      core         yes\nNode Manager         running      core         yes\n...</pre>',
].join('\n');
