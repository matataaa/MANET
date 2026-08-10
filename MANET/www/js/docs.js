// Docs tab — API reference, UI guide, MESH CLI docs
var docsInitialized = false;

function docsActivate() {
  if (docsInitialized) return;
  var panel = document.getElementById('tab-docs');
  panel.innerHTML = '<div class="docs-wrap">' + DOCS_CONTENT + '</div>';
  docsInitialized = true;

  panel.querySelectorAll('.docs-nav a').forEach(function(a) {
    a.addEventListener('click', function(e) {
      e.preventDefault();
      var target = document.getElementById(a.getAttribute('href').slice(1));
      if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  });
}

var DOCS_CONTENT = [
'<nav class="docs-nav">',
'  <a href="#doc-overview">Overview</a>',
'  <a href="#doc-ui">Web Interface</a>',
'  <a href="#doc-api">API Reference</a>',
'  <a href="#doc-ws">WebSockets</a>',
'  <a href="#doc-services">Services</a>',
'  <a href="#doc-cli">MESH CLI</a>',
'</nav>',
'<div class="docs-body">',

'<h2 id="doc-overview">Overview</h2>',
'<p>MANET//CTRL is the web interface and API server for mesh network nodes. It runs as a single Go binary (<code>manet-ctrl</code>) on port 80, serving the web UI, REST API, and WebSocket terminals.</p>',
'<p>The node runs batman-adv (BATMAN_V) L2 mesh routing over 802.11ah (HaLow) and/or standard WiFi radios on ARM64 SBCs. Node state is shared via Alfred data distribution.</p>',

'<h2 id="doc-ui">Web Interface</h2>',
'<p>The web UI is a single-page application with the following tabs:</p>',
'<table class="docs-table"><thead><tr><th>Tab</th><th>Purpose</th></tr></thead><tbody>',
'<tr><td><strong>Dashboard</strong></td><td>Topology visualization (D3.js force-directed graph), node list with TQ, hop count, last seen. Auto-refreshes every 15s.</td></tr>',
'<tr><td><strong>Nodes</strong></td><td>Full table of all mesh nodes — hostname, IP, DNS (.local), TQ, hops, battery, uptime, last seen. Sortable columns.</td></tr>',
'<tr><td><strong>Config</strong></td><td>View and edit this node\'s mesh configuration (hostname, SSID, keys, network, regulatory domain, services). Stage/activate/cancel flow.</td></tr>',
'<tr><td><strong>Hardware</strong></td><td>Radio interfaces (driver, channel, TX power, MCS), network interfaces (MTU, addresses), GPS status and coordinates, system info.</td></tr>',
'<tr><td><strong>Voice</strong></td><td>Mesh voice (PTT) service status — active/inactive, uptime, multicast address.</td></tr>',
'<tr><td><strong>Perf</strong></td><td>Performance testing — iperf3 throughput, ping latency, streaming results.</td></tr>',
'<tr><td><strong>Services</strong></td><td>All systemd services grouped by category (core, network, application, system). Start/stop/restart controls.</td></tr>',
'<tr><td><strong>Mesh</strong></td><td>Raw batman-adv data — originators, neighbors, gateways, interface membership, algorithm info.</td></tr>',
'<tr><td><strong>Terminal</strong></td><td>Full xterm.js shell (PTY over WebSocket) or log viewer (journalctl stream). Supports remote SSH to peer nodes.</td></tr>',
'</tbody></table>',

'<h2 id="doc-api">API Reference</h2>',
'<p>All API endpoints are unauthenticated unless noted. Responses are JSON.</p>',

'<h3>Status Endpoints</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/data</code></td><td>Full mesh topology — all nodes, edges, neighbors, gateways, TQ values</td></tr>',
'<tr><td>GET</td><td><code>/api/local</code></td><td>This node\'s detailed state — hostname, IP, interfaces, GPS, battery, services</td></tr>',
'<tr><td>GET</td><td><code>/api/peer/{ip}</code></td><td>Proxy to a peer node\'s <code>/api/local</code></td></tr>',
'<tr><td>GET</td><td><code>/api/voice</code></td><td>Mesh voice service status</td></tr>',
'<tr><td>GET</td><td><code>/api/admin/status</code></td><td>Config state — current config, pending changes, node ACKs</td></tr>',
'<tr><td>GET</td><td><code>/api/services</code></td><td>All registered services with systemd status</td></tr>',
'<tr><td>GET</td><td><code>/api/mesh</code></td><td>Raw batman-adv data (bat0 info, gateways, neighbors)</td></tr>',
'</tbody></table>',

'<h3>Control Endpoints</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/control/interface</code></td><td><code>{"name":"wlan2","action":"up"}</code></td><td>Bring interface up/down</td></tr>',
'<tr><td>POST</td><td><code>/api/control/txpower</code></td><td><code>{"interface":"wlan2","dbm":20}</code></td><td>Set TX power</td></tr>',
'<tr><td>POST</td><td><code>/api/control/halow_channel</code></td><td><code>{"channel":36}</code></td><td>Set HaLow channel</td></tr>',
'<tr><td>POST</td><td><code>/api/control/wifi_channel</code></td><td><code>{"interface":"wlan0","channel":6}</code></td><td>Set WiFi channel</td></tr>',
'<tr><td>POST</td><td><code>/api/control/hostname</code></td><td><code>{"hostname":"NODE1"}</code></td><td>Set system hostname</td></tr>',
'</tbody></table>',

'<h3>Admin Endpoints</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/admin/save</code></td><td>Save config changes to this node (applies hostname, restarts AP/mesh as needed)</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/stage</code></td><td>Stage a config package for deployment</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/activate</code></td><td>Activate staged config</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/cancel</code></td><td>Cancel pending config</td></tr>',
'</tbody></table>',

'<h3>Performance Endpoints</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/perf/iperf/server</code></td><td>Start iperf3 server</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/iperf/client</code></td><td>Run iperf3 client test</td></tr>',
'<tr><td>GET</td><td><code>/api/perf/iperf/stream</code></td><td>Stream iperf3 output (SSE)</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/iperf/stop</code></td><td>Stop iperf3</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/ping/run</code></td><td>Run ping test</td></tr>',
'<tr><td>GET</td><td><code>/api/perf/ping/stream</code></td><td>Stream ping output (SSE)</td></tr>',
'<tr><td>POST</td><td><code>/api/perf/ping/stop</code></td><td>Stop ping</td></tr>',
'</tbody></table>',

'<h3>Service Actions</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/service/action</code></td><td><code>{"unit":"hostapd","action":"restart"}</code></td><td>Start, stop, restart, or reload a systemd service</td></tr>',
'</tbody></table>',

'<h2 id="doc-ws">WebSocket Endpoints</h2>',
'<table class="docs-table"><thead><tr><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>/ws/terminal</code></td><td>PTY shell session. Binary frames = terminal data. Send 5-byte resize frame: <code>[0x01, cols_hi, cols_lo, rows_hi, rows_lo]</code>. Optional query params: <code>target</code>, <code>protocol</code>, <code>user</code>, <code>password</code> for SSH to remote nodes.</td></tr>',
'<tr><td><code>/ws/logs</code></td><td>Live journalctl stream. Query params: <code>unit</code> (filter to service), <code>lines</code> (initial backlog, default 200).</td></tr>',
'</tbody></table>',

'<h2 id="doc-services">Node Services</h2>',
'<p>Services running on each mesh node, organized by function:</p>',

'<h3>Core Mesh</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>MANET Controller</td><td><code>manet-ctrl</code></td><td>Web UI, REST API, WebSocket terminal. Single Go binary on port 80.</td></tr>',
'<tr><td>Alfred</td><td><code>alfred</code></td><td>Distributed data store for batman-adv. Shares node state across the mesh.</td></tr>',
'<tr><td>Node Manager</td><td><code>node-manager</code></td><td>Main orchestration loop — publishes status via Alfred, runs IP management, service elections, channel scanning (ACS mode).</td></tr>',
'</tbody></table>',

'<h3>Network</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>WPA Supplicant</td><td><code>wpa_supplicant</code></td><td>Mesh WiFi authentication (SAE/WPA3).</td></tr>',
'<tr><td>hostapd</td><td><code>hostapd</code></td><td>Access point for EUD (end-user device) connections.</td></tr>',
'<tr><td>dnsmasq</td><td><code>dnsmasq</code></td><td>DHCP and DNS for devices connected via the AP.</td></tr>',
'<tr><td>Avahi</td><td><code>avahi-daemon</code></td><td>mDNS service discovery (.local hostnames).</td></tr>',
'<tr><td>Gateway Route Manager</td><td><code>gateway-route-manager</code></td><td>Manages default routes for internet-connected gateway nodes.</td></tr>',
'<tr><td>SAE Watchdog</td><td><code>sae-watchdog</code></td><td>Monitors mesh authentication failures and restarts wpa_supplicant when needed.</td></tr>',
'</tbody></table>',

'<h3>System</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Chrony NTP</td><td><code>chronyd</code></td><td>Network time synchronisation across the mesh.</td></tr>',
'<tr><td>GPS Reader</td><td><code>gps-reader</code></td><td>Reads GPS coordinates from a serial NMEA device.</td></tr>',
'<tr><td>Battery Reader</td><td><code>battery-reader</code></td><td>Monitors battery level via I2C/SPI.</td></tr>',
'<tr><td>CoT Emitter</td><td><code>cot-emitter</code></td><td>Sends Cursor on Target messages for TAK integration.</td></tr>',
'</tbody></table>',

'<h2 id="doc-cli">MESH CLI (Planned)</h2>',
'<p>The <code>mesh</code> CLI tool will provide command-line access to all node functions. It runs directly on the node and talks to the local API or reads system state directly.</p>',

'<h3>Planned Commands</h3>',
'<table class="docs-table"><thead><tr><th>Command</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>mesh status</code></td><td>This node\'s status summary (hostname, IP, uptime, radios, battery)</td></tr>',
'<tr><td><code>mesh nodes</code></td><td>All mesh nodes table (hostname, IP, DNS, TQ, hops, last seen)</td></tr>',
'<tr><td><code>mesh config show</code></td><td>Display current mesh.conf settings</td></tr>',
'<tr><td><code>mesh config set &lt;key&gt; &lt;value&gt;</code></td><td>Update a config value</td></tr>',
'<tr><td><code>mesh radio info</code></td><td>Radio interfaces — driver, channel, TX power, MCS</td></tr>',
'<tr><td><code>mesh radio halow info</code></td><td>HaLow-specific radio details</td></tr>',
'<tr><td><code>mesh radio halow channel &lt;ch&gt;</code></td><td>Set HaLow channel</td></tr>',
'<tr><td><code>mesh radio interface &lt;iface&gt; up|down</code></td><td>Bring an interface up or down</td></tr>',
'<tr><td><code>mesh radio txpower &lt;iface&gt; &lt;dbm&gt;</code></td><td>Set TX power</td></tr>',
'<tr><td><code>mesh voice status</code></td><td>Mesh voice service status</td></tr>',
'<tr><td><code>mesh gps</code></td><td>GPS fix status and coordinates</td></tr>',
'<tr><td><code>mesh perf iperf &lt;target&gt;</code></td><td>Run iperf3 test to a peer</td></tr>',
'<tr><td><code>mesh perf ping &lt;target&gt;</code></td><td>Ping a peer node</td></tr>',
'<tr><td><code>mesh services</code></td><td>List all services and status</td></tr>',
'<tr><td><code>mesh reboot</code></td><td>Reboot the node</td></tr>',
'</tbody></table>',

'</div>'
].join('\n');
