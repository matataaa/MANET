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
  if (window.location.hash !== '#docs/' + id) {
    history.replaceState(null, '', '#docs/' + id);
  }
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
'<tr><td><strong>Voice</strong></td><td>Multi-channel voice PTT. 21 channels with independent TX/RX selection. Web client (browser mic via WebSocket) and hardware PTT (OpenVLM HID). Per-channel activity indicators. Multicast listeners only run on subscribed RX channels.</td></tr>',
'<tr><td><strong>Perf</strong></td><td>Performance testing — iperf3 throughput, ping latency, streaming results.</td></tr>',
'<tr><td><strong>Services</strong></td><td>All systemd services grouped by category. Start/stop/restart controls.</td></tr>',
'<tr><td><strong>Mesh</strong></td><td>Raw batman-adv data — originators, neighbors, gateways, interface membership.</td></tr>',
'<tr><td><strong>Terminal</strong></td><td>Full xterm.js shell (PTY over WebSocket) or live log viewer (journalctl stream). SSH to peer nodes.</td></tr>',
'<tr><td><strong>Applets</strong></td><td>Installable mesh applications. Upload applet tarballs, view status, start/stop/restart, access config and frontend pages. Applets declare DNS records for <code>.mesh</code> hostname access.</td></tr>',
'<tr><td><strong>Fleet</strong></td><td>Fleet-wide configuration management. Stage config changes, push to all nodes, view per-node ACK status. Profile editor for bulk settings.</td></tr>',
'<tr><td><strong>Registry</strong></td><td>Alfred data browser — raw mesh-wide node data as shared via the distributed registry (type 68 JSON records).</td></tr>',
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
'<p>The Config tab exposes these fields: hostname prefix, mesh SSID, mesh key, IPv4 network, regulatory domain, EUD mode, AP SSID, AP key, max EUDs per node, battery monitor, and admin password.</p>',
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
'<pre class="docs-pre">$ mesh config show\nadmin_password                  =\nbattery_monitor                = n\neud                            = wired\nipv4_network                   = 10.30.2.0/24\nlan_ap_channel                 = 36\nlan_ap_ssid                    = MANET-AP\nmesh_key                       = ********\nmesh_ssid                      = MESH\n...</pre>',

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
'<tr><td><code>halow_bw</code></td><td><code>1MHz</code>, <code>2MHz</code>, <code>4MHz</code>, <code>8MHz</code></td><td>Yes</td><td>HaLow primary channel bandwidth. Narrower bandwidth increases range at the cost of throughput. Controls op_class and channel in wpa_supplicant S1G config.</td></tr>',
'<tr><td><code>multicast_mode</code></td><td><code>flood</code>, <code>optimized</code></td><td>Yes</td><td>Multicast delivery mode. <strong>flood</strong> (default): sends all multicast to all peers. <strong>optimized</strong>: enables IGMP snooping on br0 for selective delivery.</td></tr>',
'</tbody></table>',

'<h4>Access Point (EUD)</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>eud</code></td><td><code>wired</code>, <code>wireless</code>, <code>both</code>, <code>auto</code></td><td>Yes</td><td>End-user device connectivity mode. <strong>wired</strong>: EUDs connect via ethernet only, hostapd disabled. <strong>wireless</strong>: hostapd enabled, EUDs connect via AP. <strong>both</strong>: wired and wireless simultaneously. <strong>auto</strong>: ethernet-autodetect manages hostapd based on cable state.</td></tr>',
'<tr><td><code>lan_ap_ssid</code></td><td>string</td><td>Yes</td><td>SSID broadcast by the access point for EUD WiFi connections.</td></tr>',
'<tr><td><code>lan_ap_key</code></td><td>string (8+ chars)</td><td>Yes</td><td>WPA2/WPA3 passphrase for the EUD access point.</td></tr>',
'<tr><td><code>lan_ap_channel</code></td><td>integer</td><td>Yes</td><td>WiFi channel for the access point (e.g. 36 for 5GHz, 6 for 2.4GHz).</td></tr>',
'<tr><td><code>lan_ap_bw</code></td><td><code>20</code>, <code>40</code>, <code>80</code></td><td>Yes</td><td>Channel bandwidth in MHz for the access point.</td></tr>',
'<tr><td><code>max_euds_per_node</code></td><td>integer</td><td>Yes</td><td>Maximum number of EUD clients allowed on this node\'s AP. Controls DHCP pool size in dnsmasq.</td></tr>',
'<tr><td><code>eud_bandwidth</code></td><td>integer (Mbit)</td><td>Yes</td><td>Per-EUD symmetric bandwidth cap in Mbit. Applied via tc htb qdisc on br0 egress and IFB ingress. <code>0</code> or empty = unlimited.</td></tr>',
'</tbody></table>',

'<h4>Gateway</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>gateway</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Allow this node to act as a mesh gateway, providing internet access to other nodes.</td></tr>',
'<tr><td><code>gateway_nat</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Enable NAT masquerade on the upstream interface so mesh traffic can reach the internet.</td></tr>',
'<tr><td><code>gateway_mss_clamp</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Clamp TCP MSS to prevent fragmentation through the mesh-to-internet path.</td></tr>',
'<tr><td><code>gateway_bandwidth</code></td><td>string (e.g. <code>10M/10M</code>)</td><td>Yes</td><td>Advertised bandwidth for batman-adv gateway selection. Empty = auto.</td></tr>',
'<tr><td><code>dns_servers</code></td><td>comma-separated IPs</td><td>Yes</td><td>Upstream DNS servers written to <code>/etc/resolv.conf</code>. Used by dnsmasq for forwarding EUD queries. Default: <code>8.8.8.8,8.8.4.4</code>. Changing this restarts dnsmasq.</td></tr>',
'</tbody></table>',

'<h4>Services</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>battery_monitor</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Enable battery monitoring via Waveshare UPS HAT (E) I2C interface.</td></tr>',
'</tbody></table>',

'<h4>Updates</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>auto_update</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Trigger OTA update check when ethernet carrier is detected and internet is reachable. Default: <code>n</code>.</td></tr>',
'<tr><td><code>update_url</code></td><td>URL string</td><td>Yes</td><td>Base URL for the OTA update server. The node fetches <code>{url}/manet_version.txt</code> and <code>{url}/{board}-tools.tar.gz</code>. Leave empty to disable OTA entirely.</td></tr>',
'</tbody></table>',

'<h4>Security</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>admin_password</code></td><td>string</td><td>Yes</td><td>Password for admin operations (config staging/activation).</td></tr>',
'<tr><td><code>require_auth</code></td><td>y / n</td><td>Yes</td><td>Require admin password for write operations. Default: n (disabled).</td></tr>',
'</tbody></table>',

'<h4>Voice</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>voice_ptt_mode</code></td><td><code>openvlm</code>, <code>gpio</code>, <code>always</code>, <code>vox</code></td><td>Yes</td><td>Hardware PTT mode. <strong>openvlm</strong>: HID USB button. <strong>gpio</strong>: GPIO evdev button. <strong>always</strong>: always transmitting. <strong>vox</strong>: voice-activated.</td></tr>',
'<tr><td><code>voice_channel</code></td><td>1–21</td><td>Yes</td><td>TX channel. Default: 1. Changing TX restarts mesh-voice daemon.</td></tr>',
'<tr><td><code>voice_rx_channels</code></td><td>comma-separated (e.g. <code>1,4,8</code>)</td><td>Yes</td><td>RX channels to receive. TX channel is always included. Multicast listeners only run on subscribed channels.</td></tr>',
'<tr><td><code>voice_mic_volume</code></td><td>0–100</td><td>Yes</td><td>Microphone input volume. Default: 80.</td></tr>',
'<tr><td><code>voice_speaker_volume</code></td><td>0–100</td><td>Yes</td><td>Speaker output volume. Default: 80.</td></tr>',
'<tr><td><code>voice_gain</code></td><td>1.0–5.0</td><td>Yes</td><td>Software microphone gain multiplier. Default: <code>3.0</code>.</td></tr>',
'<tr><td><code>voice_beep_tx_start</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Play a short beep tone when PTT transmit begins. Default: <code>y</code>.</td></tr>',
'<tr><td><code>voice_beep_rx_end</code></td><td><code>y</code> / <code>n</code></td><td>Yes</td><td>Play a roger-beep when incoming reception ends. Default: <code>y</code>.</td></tr>',
'</tbody></table>',

'<h4>QoS</h4>',
'<table class="docs-table"><thead><tr><th>Key</th><th>Values</th><th>UI</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>qos_enabled</code></td><td><code>y</code> / <code>n</code></td><td>No</td><td>Enable tc prio qdisc traffic prioritization on mesh interfaces.</td></tr>',
'<tr><td><code>qos_voice_band</code></td><td>0, 1, 2</td><td>No</td><td>Priority band for voice traffic (port 4370). 0=high, 1=normal, 2=low.</td></tr>',
'<tr><td><code>qos_cot_band</code></td><td>0, 1, 2</td><td>No</td><td>Priority band for CoT traffic (port 6969).</td></tr>',
'<tr><td><code>qos_chat_band</code></td><td>0, 1, 2</td><td>No</td><td>Priority band for mesh chat traffic (port 9800).</td></tr>',
'</tbody></table>',
].join('\n');

DOCS_TABS.api = [
'<h2>API Reference</h2>',
'<p>All endpoints return JSON. Endpoints marked <strong>Auth</strong> require <code>admin_password</code> when <code>require_auth=y</code>.</p>',

'<h3>Status</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/data</code></td><td>Full mesh topology — all nodes, edges, neighbors, gateways, TQ values</td></tr>',
'<tr><td>GET</td><td><code>/api/local</code></td><td>This node\'s detailed state — hostname, IP, interfaces, GPS, battery, services</td></tr>',
'<tr><td>GET</td><td><code>/api/peer/{ip}</code></td><td>Proxy to a peer node\'s <code>/api/local</code></td></tr>',
'<tr><td>GET</td><td><code>/api/daemons</code></td><td>Live status of GPS, battery, and CoT emitter daemons from <code>/run/*.json</code> files</td></tr>',
'<tr><td>GET</td><td><code>/api/registry</code></td><td>Full Alfred registry — all mesh-wide node data (type 68 JSON records)</td></tr>',
'<tr><td>GET</td><td><code>/api/services</code></td><td>All registered services with systemd status, category, and available actions</td></tr>',
'<tr><td>GET</td><td><code>/api/mesh</code></td><td>Raw batman-adv data (bat0 info, gateways, neighbors, originators)</td></tr>',
'<tr><td>GET</td><td><code>/api/version</code></td><td>Software version string for manet-ctrl</td></tr>',
'</tbody></table>',

'<h3>Voice</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/voice</code></td><td></td><td>Mesh voice service status — active, uptime, PTT mode, multicast address, TX/RX state</td></tr>',
'<tr><td>GET</td><td><code>/api/voice/channels</code></td><td></td><td>Voice channel state — 21 channels with tx, rx (subscribed), and active (traffic in last 500ms) flags</td></tr>',
'<tr><td>POST</td><td><code>/api/voice</code></td><td><code>{"action":"start|stop|restart"}</code></td><td>Control the hardware PTT voice service (mesh-voice daemon)</td></tr>',
'<tr><td>POST</td><td><code>/api/voice/channels</code></td><td><code>{"tx":4,"rx":[1,4,8]}</code></td><td>Set TX channel and/or RX channel list. TX channel always included in RX. Persisted to mesh.conf. Changing TX restarts mesh-voice.</td></tr>',
'</tbody></table>',

'<h3>QoS</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/qos</code></td><td></td><td>QoS config — rules, enabled state, per-interface tc stats, band names</td></tr>',
'<tr><td>POST</td><td><code>/api/qos</code></td><td><code>{"enabled":true}</code> or <code>{"service":"voice","band":0}</code></td><td>Toggle QoS or reassign a service to a priority band (0=high, 1=normal, 2=low)</td></tr>',
'</tbody></table>',

'<h3>Control</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/control/interface</code></td><td><code>{"name":"wlan2","action":"up"}</code></td><td>Auth. Bring interface up or down</td></tr>',
'<tr><td>POST</td><td><code>/api/control/txpower</code></td><td><code>{"interface":"wlan2","dbm":20}</code></td><td>Auth. Set TX power (dBm)</td></tr>',
'<tr><td>POST</td><td><code>/api/control/halow_channel</code></td><td><code>{"channel":36}</code></td><td>Auth. Set HaLow channel</td></tr>',
'<tr><td>POST</td><td><code>/api/control/wifi_channel</code></td><td><code>{"interface":"wlan3","channel":6}</code></td><td>Auth. Set WiFi channel</td></tr>',
'<tr><td>POST</td><td><code>/api/control/hostname</code></td><td><code>{"hostname":"NODE1"}</code></td><td>Auth. Set system hostname</td></tr>',
'</tbody></table>',

'<h3>Admin & Fleet</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/admin/status</code></td><td>Config state — current config, pending changes, node ACKs</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/save</code></td><td>Auth. Save config changes to this node. Applies AP, hostname, EUD mode, DHCP, gateway settings and restarts affected services. Emits <code>config-change</code> event hook.</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/stage</code></td><td>Auth. Stage a config package for deployment to all nodes</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/activate</code></td><td>Auth. Activate staged config across the mesh</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/cancel</code></td><td>Auth. Cancel pending staged config</td></tr>',
'<tr><td>POST</td><td><code>/api/admin/delete-node</code></td><td>Auth. Delete a node from the local registry by MAC-based ID</td></tr>',
'<tr><td>GET/POST</td><td><code>/api/admin/preferences</code></td><td>Auth. GET returns fleet preferences (profiles, node assignments, mesh config). POST updates them.</td></tr>',
'</tbody></table>',

'<h3>Authentication</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/auth/status</code></td><td>Whether auth is required and whether the current session is authenticated</td></tr>',
'<tr><td>POST</td><td><code>/api/perf-auth</code></td><td>Submit admin password, receive session cookie for authenticated access</td></tr>',
'</tbody></table>',

'<h3>Performance Testing</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/iperf/server/start</code></td><td>Auth. Start iperf3 server on this node</td></tr>',
'<tr><td>POST</td><td><code>/api/iperf/server/stop</code></td><td>Auth. Stop iperf3 server</td></tr>',
'<tr><td>POST</td><td><code>/api/iperf/client/run</code></td><td>Auth. Run iperf3 client test to a target</td></tr>',
'<tr><td>POST</td><td><code>/api/iperf/client/stream</code></td><td>Auth. Stream iperf3 output (SSE)</td></tr>',
'<tr><td>POST</td><td><code>/api/iperf/stop</code></td><td>Auth. Stop iperf3 client</td></tr>',
'<tr><td>POST</td><td><code>/api/ping/run</code></td><td>Auth. Run ping test to a target</td></tr>',
'<tr><td>GET</td><td><code>/api/ping/stream</code></td><td>Auth. Stream ping output (SSE)</td></tr>',
'<tr><td>POST</td><td><code>/api/ping/stop</code></td><td>Auth. Stop ping</td></tr>',
'<tr><td>POST</td><td><code>/api/traceroute/stream</code></td><td>Auth. Stream combined batctl traceroute and IP traceroute output for a target</td></tr>',
'<tr><td>POST</td><td><code>/api/traceroute/stop</code></td><td>Auth. Stop active traceroute stream</td></tr>',
'</tbody></table>',

'<h3>Terminal</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/terminal/exec</code></td><td>Auth. Execute a shell command via HTTP (not WebSocket). Optionally SSH to a remote target. Streams output.</td></tr>',
'<tr><td>POST</td><td><code>/api/terminal/complete</code></td><td>Auth. Bash tab-completion — given partial input line, returns matching commands and files</td></tr>',
'<tr><td>POST</td><td><code>/api/terminal/reboot</code></td><td>Auth. Trigger system reboot via <code>systemctl reboot</code></td></tr>',
'</tbody></table>',

'<h3>Service Actions</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Body</th><th>Description</th></tr></thead><tbody>',
'<tr><td>POST</td><td><code>/api/services/{id}</code></td><td><code>{"action":"restart"}</code></td><td>Auth. Start, stop, restart, or reload a systemd service by registry ID</td></tr>',
'</tbody></table>',

'<h3>Applets</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/applets</code></td><td>List all installed applets with status, version, backend/frontend flags</td></tr>',
'<tr><td>POST</td><td><code>/api/applets/install</code></td><td>Auth. Upload a <code>.tar.gz</code> applet tarball. Extracts, validates <code>applet.json</code>, runs pre-install hook, installs systemd unit, registers event hooks.</td></tr>',
'<tr><td>GET</td><td><code>/api/applets/{name}</code></td><td>Detailed info for a single applet — status, PID, version, author, started_at</td></tr>',
'<tr><td>POST</td><td><code>/api/applets/{name}/action</code></td><td>Auth. Body: <code>{"action":"start|stop|restart|enable|disable"}</code>. Control the applet\'s systemd service.</td></tr>',
'<tr><td>GET</td><td><code>/api/applets/{name}/logs</code></td><td>Recent journalctl logs for the applet\'s service unit. Query: <code>?lines=100</code></td></tr>',
'<tr><td>GET</td><td><code>/api/applets/{name}/config</code></td><td>Proxy GET to the applet backend\'s <code>/config</code> endpoint</td></tr>',
'<tr><td>POST</td><td><code>/api/applets/{name}/config</code></td><td>Auth. Write key=value pairs to the applet\'s declared config file</td></tr>',
'<tr><td>GET</td><td><code>/api/applets/{name}/config-page</code></td><td>Serve the applet\'s config HTML page</td></tr>',
'<tr><td>GET</td><td><code>/api/applets/{name}/frontend[/path]</code></td><td>Serve applet frontend static files (index.html, JS, CSS)</td></tr>',
'<tr><td>*</td><td><code>/api/applets/{name}/proxy[/path]</code></td><td>Reverse proxy to the applet backend (HTTP and WebSocket). Supports GET, POST, DELETE.</td></tr>',
'<tr><td>DELETE</td><td><code>/api/applets/{name}</code></td><td>Auth. Uninstall — stops service, runs post-remove hook, removes event hooks and files, reloads systemd</td></tr>',
'</tbody></table>',

'<h3>Downloads</h3>',
'<table class="docs-table"><thead><tr><th>Method</th><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td>GET</td><td><code>/api/mesh-ctrl.apk</code></td><td>Download the Android companion app APK. Returns 404 if not installed on this node.</td></tr>',
'<tr><td>GET</td><td><code>/api/atak-package</code></td><td>Download ATAK data package (ZIP) pre-configured with this mesh\'s CoT multicast stream (239.2.3.1:6969/udp). Import into ATAK to receive blue-force tracking from mesh nodes.</td></tr>',
'</tbody></table>',

'<h3>WebSocket</h3>',
'<table class="docs-table"><thead><tr><th>Endpoint</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>/ws/terminal</code></td><td>Auth. PTY shell session. Binary frames for terminal I/O. Send 5-byte resize: <code>[0x01, cols_hi, cols_lo, rows_hi, rows_lo]</code>. Query params: <code>target</code>, <code>protocol</code>, <code>user</code>, <code>password</code> for SSH to remote nodes.</td></tr>',
'<tr><td><code>/ws/logs</code></td><td>Live journalctl stream. Query params: <code>unit</code> (filter to service), <code>lines</code> (initial backlog, default 200), <code>file</code> (tail an arbitrary file instead of journalctl), <code>target</code> (proxy log stream from a remote peer node).</td></tr>',
'<tr><td><code>/ws/voice</code></td><td>Browser voice client. Send raw Opus frames as binary; receive RTP packets (12-byte header + Opus payload). Each connection gets a unique SSRC. Loopback is suppressed — you won\'t hear your own audio.</td></tr>',
'</tbody></table>',
].join('\n');

DOCS_TABS.services = [
'<h2>Node Services</h2>',
'<p>Services running on each mesh node, managed via systemd. View and control them from the Services tab.</p>',

'<h3>Core Mesh</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>MANET Controller</td><td><code>manet-ctrl</code></td><td>Web UI (SPA), REST API, WebSocket terminal, voice relay. Single Go binary on port 80/443 (auto TLS). Hosts applet frontends and handles <code>.mesh</code> hostname redirects.</td></tr>',
'<tr><td>Alfred</td><td><code>alfred</code></td><td>Distributed data store for batman-adv. Shares node state across the mesh via type 68 JSON records.</td></tr>',
'<tr><td>Mesh Registry</td><td><code>mesh-registry</code></td><td>Collects local node info (hostname, IP, MAC, GPS, MCS rates, TQ) and publishes to Alfred. Reads remote node data to build the mesh-wide registry.</td></tr>',
'<tr><td>Node Manager</td><td><code>node-manager</code></td><td>Radio state sync (ensures wpa_supplicant frequencies match config), static channel enforcement, and gateway reconciliation. 15s main loop.</td></tr>',
'<tr><td>Mesh Manager</td><td><code>mesh-manager</code></td><td>IPv4 address allocation (chunk-based CIDR), <code>/etc/hosts</code> mesh hostname updates, <code>.mesh</code> DNS records via dnsmasq, gateway route management, default route fix, EUD DHCP pool config, and voice QoS setup. 30s periodic loop.</td></tr>',
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
'<tr><td>Gateway Manager</td><td><code>gateway-manager</code></td><td>Detects internet-connected interfaces, manages NAT/masquerade rules, handles gateway election and failover. Emits <code>gateway-up</code>/<code>gateway-down</code> event hooks.</td></tr>',
'<tr><td>Avahi</td><td><code>avahi-daemon</code></td><td>mDNS / Zeroconf service discovery daemon.</td></tr>',
'</tbody></table>',

'<h3>Voice & QoS</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Mesh Voice</td><td><code>mesh-voice</code></td><td>Push-to-talk voice over multicast RTP. Opus 48kHz/32kbps. Supports OpenVLM HID USB, GPIO evdev, always-on, and VOX PTT modes. Half-duplex with remote-active detection. 60ms jitter buffer for smooth playback.</td></tr>',
'</tbody></table>',

'<h4>Multi-Channel Voice</h4>',
'<p>21 voice channels available (1–21). Each channel maps to a separate multicast group (<code>239.69.0.{ch}:4370</code>). A node has one TX channel and one or more RX channels.</p>',
'<table class="docs-table"><thead><tr><th>Setting</th><th>mesh.conf key</th><th>Description</th></tr></thead><tbody>',
'<tr><td>TX Channel</td><td><code>voice_channel</code></td><td>Channel to transmit on. Default: 1. Changing TX restarts the mesh-voice daemon.</td></tr>',
'<tr><td>RX Channels</td><td><code>voice_rx_channels</code></td><td>Comma-separated list of channels to receive. TX channel is always included. Multicast UDP listeners only run on subscribed channels.</td></tr>',
'<tr><td>Mic Volume</td><td><code>voice_mic_volume</code></td><td>Microphone input volume (0–100). Default: 80.</td></tr>',
'<tr><td>Speaker Volume</td><td><code>voice_speaker_volume</code></td><td>Speaker output volume (0–100). Default: 80.</td></tr>',
'<tr><td>Mic Gain</td><td><code>voice_gain</code></td><td>Software microphone gain multiplier (1x–5x). Boosts captured audio before encoding. Default: 3x (<code>3.0</code>). Changes restart mesh-voice.</td></tr>',
'<tr><td>TX Beep</td><td><code>voice_beep_tx_start</code></td><td>Play a short beep tone when PTT transmit begins (web and hardware). Default: On (<code>y</code>).</td></tr>',
'<tr><td>RX End Beep</td><td><code>voice_beep_rx_end</code></td><td>Play a roger-beep when incoming reception ends (web and hardware). Default: On (<code>y</code>).</td></tr>',
'</tbody></table>',
'<p>The Voice tab shows all 21 channels with <strong>Listen</strong> (RX subscribe, solid green when active) and <strong>TX</strong> (transmit channel select, hollow red when active) buttons. A blue activity dot pulses when traffic is detected on a subscribed channel within the last 500ms.</p>',

'<h4>Web Voice Client</h4>',
'<p>The Voice tab includes a browser-based voice client that connects your device mic to the mesh voice channel via WebSocket (<code>/ws/voice</code>). Requires HTTPS for microphone access. Uses the WebCodecs API (AudioEncoder/AudioDecoder) for Opus encoding at 48kHz mono, 32kbps. Push-to-talk only — hold the PTT button to transmit.</p>',

'<h4>Voice QoS</h4>',
'<p>Voice packets are marked with DSCP EF (Expedited Forwarding, TOS 0xB8) on the UDP socket. batman-adv preserves this through encapsulation, mapping to WMM Access Category Voice (AC_VO) on the wireless interface — shortest contention window, highest air-time priority.</p>',
'<p>Additionally, <code>mesh-manager</code> configures a <code>tc prio</code> qdisc on <code>br0</code> at startup that puts DSCP EF traffic and voice multicast (port 4370) in band 0 (highest priority). Bulk data goes to band 2. This ensures voice packets are dequeued before other traffic under load.</p>',

'<h4>Android App (APK)</h4>',
'<p>The MANET Mesh Ctrl companion app for Android is available for download from any node at <code>/api/mesh-ctrl.apk</code>. An EUD connected to a node\'s WiFi can browse to <code>http://&lt;node-ip&gt;/api/mesh-ctrl.apk</code> to install it. The APK is pre-installed at <code>/usr/local/share/manet/mesh-ctrl.apk</code> on nodes that include it in their image.</p>',

'<h4>ATAK Integration</h4>',
'<p>Download the ATAK data package from <code>/api/atak-package</code> and import it into ATAK. The package pre-configures the CoT multicast stream (<code>239.2.3.1:6969/udp</code>) so ATAK receives blue-force tracking from mesh nodes running <code>cot-emitter</code>. Each node broadcasts its GPS position as Cursor on Target XML.</p>',

'<h3>Applications & Applets</h3>',
'<table class="docs-table"><thead><tr><th>Service</th><th>Unit</th><th>Description</th></tr></thead><tbody>',
'<tr><td>Mesh Chat</td><td><code>mesh-chat</code></td><td>Mesh-wide text chat over multicast. Installed as an applet with DNS record <code>chat.mesh</code>.</td></tr>',
'</tbody></table>',
'<p><strong>Applet DNS:</strong> Applets declare DNS records in their <code>applet.json</code> manifest with a scope: <code>local</code> (only this node\'s EUDs) or <code>global</code> (mesh-wide). When an EUD browses to an applet hostname (e.g. <code>chat.mesh</code>), manet-ctrl redirects to the applet\'s frontend.</p>',
'<p><strong>Event Hooks:</strong> Applets can subscribe to system events by declaring an <code>events</code> array in <code>applet.json</code>. Each entry specifies an <code>event</code> name and a <code>script</code> to run. Scripts receive environment variables: <code>MESH_EVENT</code> (the event name) plus event-specific vars.</p>',
'<table class="docs-table"><thead><tr><th>Event</th><th>Variables</th><th>Description</th></tr></thead><tbody>',
'<tr><td><code>gateway-up</code></td><td><code>MESH_IFACE</code></td><td>Internet gateway detected, NAT applied</td></tr>',
'<tr><td><code>gateway-down</code></td><td></td><td>Internet gateway lost, NAT cleared</td></tr>',
'<tr><td><code>peer-join</code></td><td><code>MESH_MAC</code>, <code>MESH_IP</code>, <code>MESH_HOSTNAME</code></td><td>New mesh peer appeared in registry</td></tr>',
'<tr><td><code>peer-leave</code></td><td><code>MESH_MAC</code>, <code>MESH_IP</code>, <code>MESH_HOSTNAME</code></td><td>Mesh peer went offline</td></tr>',
'<tr><td><code>ip-change</code></td><td><code>MESH_IP</code>, <code>MESH_GATEWAY</code></td><td>Node IP address changed</td></tr>',
'<tr><td><code>config-change</code></td><td><code>MESH_KEY</code> (repeated)</td><td>mesh.conf was modified via API</td></tr>',
'<tr><td><code>ap-client-join</code></td><td><code>MESH_MAC</code>, <code>MESH_IP</code></td><td>EUD connected to local AP</td></tr>',
'<tr><td><code>ap-client-leave</code></td><td><code>MESH_MAC</code></td><td>EUD disconnected from local AP</td></tr>',
'<tr><td><code>limp-enter</code></td><td></td><td>Node entered limp mode (radio failure)</td></tr>',
'<tr><td><code>limp-exit</code></td><td></td><td>Node exited limp mode</td></tr>',
'</tbody></table>',

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
'<tr><td><code>mesh config get &lt;key&gt;</code></td><td>Get a single config value</td></tr>',
'<tr><td><code>mesh config set &lt;key&gt; &lt;value&gt;</code></td><td>Set a config value (applied immediately — restarts affected services)</td></tr>',
'<tr><td><code>mesh config keys</code></td><td>List all configurable key names with descriptions</td></tr>',
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
