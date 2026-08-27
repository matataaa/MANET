let fleetInitialized = false;
let fleetData = null;
let fleetPollTimer = null;
let fleetEditing = false;
let fleetUpdateSummaryData = null;
let fleetUpdatePollTimer = null;

const QOS_BAND_OPTS = [
  {v:'0',l:'High (Voice)'},{v:'1',l:'Normal'},{v:'2',l:'Low (Bulk)'}
];

const MESH_FIELDS = [
  { key: 'mesh_ssid', label: 'Mesh SSID', dangerous: true },
  { key: 'mesh_key', label: 'Mesh Key', dangerous: true, type: 'password' },
  { key: 'ipv4_network', label: 'IPv4 Network', dangerous: true },
  { key: 'halow_bw', label: 'HaLow Bandwidth', type: 'select', options: ['1MHz','2MHz','4MHz','8MHz'] },
  { key: 'halow_channel', label: 'HaLow Channel', hint: 'Blank = Auto. Validated per-node against its own regulatory domain on apply, since a fleet push can span nodes on different domains.' },
  { key: 'acs', label: '5GHz Mesh Channel Mode', dangerous: true, type: 'select', options: [
    {v:'n',l:'Static (pinned channel)'},{v:'y',l:'Automatic (ACS)'}
  ], hint: 'Must match across the fleet — nodes on different modes won\'t elect/scan together.' },
  { key: 'mesh_5ghz_bw', label: '5GHz Mesh Width', dangerous: true, type: 'select', options: [
    {v:'20',l:'20 MHz (default)'},{v:'80',l:'80 MHz'}
  ], hint: 'Never mix widths across nodes. 80MHz + Automatic mode is the combination known to sometimes mismatch primary channel between peers.' },
  { key: 'mesh_5ghz_channel', label: '5GHz Pinned Channel', dangerous: true, hint: 'Only used when 5GHz Mesh Channel Mode is Static. Blank = default 36. Must match across the fleet.' },
  { key: 'multicast_mode', label: 'Multicast Mode', type: 'select', options: [
    {v:'flood',l:'Flood (recommended ≤10 nodes)'},{v:'optimized',l:'Optimized IGMP (10+ nodes)'}
  ] },
  { key: 'regulatory_domain', label: 'Reg Domain' },
  { key: 'halow_regulatory_domain', label: 'HaLow Reg Domain', hint: 'Blank = inherit Reg Domain. Overrides it for HaLow specifically (e.g. MM8108 nodes running HaLow on a different domain than their WiFi radios).' },
  { key: 'dns_servers', label: 'DNS Servers', hint: 'Comma-separated (e.g. 8.8.8.8,8.8.4.4)' },
  { key: 'admin_password', label: 'Admin Password', type: 'password' },
  { key: 'require_auth', label: 'Require Auth', type: 'select', options: [{v:'n',l:'No'},{v:'y',l:'Yes'}], hint: 'Require admin password for write operations' },
  { section: 'QoS' },
  { key: 'qos_enabled', label: 'QoS Enabled', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
  { key: 'qos_voice_band', label: 'Voice Priority', type: 'select', options: QOS_BAND_OPTS },
  { key: 'qos_cot_band', label: 'CoT Priority', type: 'select', options: QOS_BAND_OPTS },
  { key: 'qos_chat_band', label: 'Chat Priority', type: 'select', options: QOS_BAND_OPTS },
  { section: 'Updates' },
  { key: 'auto_update', label: 'Auto Update', type: 'select', options: [{v:'n',l:'No'},{v:'y',l:'Yes'}] },
  { key: 'update_url', label: 'Update URL', hint: 'Base URL for OTA tarball server (blank = disabled)' },
  { key: 'auto_update_overlay', label: 'Auto Update Overlay', type: 'select', options: [{v:'n',l:'No'},{v:'y',l:'Yes'}], hint: 'Kernel/firmware — no rollback if it fails to boot' },
  { key: 'auto_update_min_mbps', label: 'Auto Update Min Bandwidth (Mbit)', hint: 'Automatic apply is skipped below this — manual/force update ignores it' },
];

const PROFILE_SECTIONS = [
  { id: 'node', cat: 'Node', fields: [
    { key: 'node_hostname', label: 'Hostname Prefix', hint: 'Prefix — full: {this}-{ssid}-{mac}. Use {{hostname}} in other fields for per-node substitution' },
    { key: 'battery_monitor', label: 'Battery Monitor', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
  ]},
  { id: 'gateway', cat: 'Gateway', fields: [
    { key: 'gateway', label: 'Gateway Enabled', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { key: 'gateway_nat', label: 'NAT Masquerade', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { key: 'gateway_mss_clamp', label: 'MSS Clamping', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { key: 'gateway_bandwidth', label: 'Bandwidth Advertisement', type: 'select', options: [
      {v:'',l:'Auto (batman default)'},{v:'2M/2M',l:'2 Mbit'},{v:'5M/5M',l:'5 Mbit'},{v:'10M/10M',l:'10 Mbit'},
      {v:'20M/20M',l:'20 Mbit'},{v:'50M/50M',l:'50 Mbit'},{v:'100M/100M',l:'100 Mbit'},
      {v:'200M/200M',l:'200 Mbit'},{v:'300M/300M',l:'300 Mbit'},{v:'500M/500M',l:'500 Mbit'},{v:'1000M/1000M',l:'1 Gbit'},
    ] },
  ]},
  { id: 'eud', cat: 'EUD', fields: [
    { key: 'eud', label: 'EUD Mode', type: 'select', options: ['wired','wifi','both','auto'] },
    { key: 'max_euds_per_node', label: 'Max EUDs (0=unlimited)' },
    { key: 'eud_bandwidth', label: 'Bandwidth Cap (Mbit)', type: 'select', options: [
      {v:'0',l:'Unlimited'},{v:'0.25',l:'0.25 Mbit'},{v:'0.5',l:'0.5 Mbit'},{v:'1',l:'1 Mbit'},{v:'2',l:'2 Mbit'},{v:'5',l:'5 Mbit'},
      {v:'10',l:'10 Mbit'},{v:'20',l:'20 Mbit'},{v:'50',l:'50 Mbit'},{v:'100',l:'100 Mbit'}
    ] },
  ]},
  { id: 'ap', cat: 'Access Point', fields: [
    { key: 'lan_ap_ssid', label: 'AP SSID', hint: 'Use {{hostname}} for per-node name' },
    { key: 'lan_ap_key', label: 'AP Key', type: 'password' },
    { key: 'lan_ap_channel', label: 'AP Channel' },
    { key: 'lan_ap_bw', label: 'AP Bandwidth', type: 'select', options: [
      {v:'20',l:'20 MHz'},{v:'40',l:'40 MHz'},{v:'80',l:'80 MHz'}
    ] },
  ]},
  { id: 'gps', cat: 'GPS', fields: [
    { key: 'gps', label: 'GPS Enabled', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { key: 'gps_source', label: 'GPS Source', type: 'select', options: [
      {v:'receiver',l:'Receiver (hardware GPS)'},{v:'static',l:'Static (fixed location)'}
    ], hint: 'Static coordinates are set per-node in that node\'s own Config, not here' },
  ]},
  { id: 'voice', cat: 'Voice', fields: [
    { key: 'voice_enabled', label: 'Voice Enabled', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { key: 'voice_ptt_mode', label: 'PTT Mode', type: 'select', options: [
      {v:'openvlm',l:'OpenVLM HID'},{v:'gpio',l:'GPIO Button'},{v:'always',l:'Always On'},{v:'vox',l:'VOX (auto)'}
    ] },
    { key: 'voice_gain', label: 'Mic Gain', type: 'select', options: [
      {v:'1.0',l:'1x (low)'},{v:'2.0',l:'2x'},{v:'3.0',l:'3x (default)'},{v:'4.0',l:'4x'},{v:'5.0',l:'5x (loud)'}
    ] },
    { key: 'voice_beep_tx_start', label: 'TX Beep', type: 'select', options: [{v:'y',l:'On'},{v:'n',l:'Off'}] },
    { key: 'voice_beep_rx_end', label: 'RX End Beep', type: 'select', options: [{v:'y',l:'On'},{v:'n',l:'Off'}] },
  ]},
];

function fleetOptLabel(f, val) {
  if (f.type === 'select' && f.options && f.options.length && typeof f.options[0] === 'object') {
    var m = f.options.find(function(o) { return o.v === val; });
    if (m) return m.l;
  }
  return val;
}

function fleetActivate() {
  var panel = document.getElementById('tab-fleet');
  if (!fleetInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading fleet status...</div>';
    fleetInitialized = true;
  }
  fleetFetch();
  fleetPollTimer = setInterval(fleetFetch, 5000);
  fleetFetchUpdateSummary();
  fleetUpdatePollTimer = setInterval(fleetFetchUpdateSummary, 30000);
}

function fleetDeactivate() {
  if (fleetPollTimer) { clearInterval(fleetPollTimer); fleetPollTimer = null; }
  if (fleetUpdatePollTimer) { clearInterval(fleetUpdatePollTimer); fleetUpdatePollTimer = null; }
}

async function fleetFetch() {
  try {
    var r = await fetch('/api/admin/status');
    fleetData = await r.json();
    fleetRender();
  } catch(e) {
    document.getElementById('tab-fleet').innerHTML =
      '<div class="loading-msg">Failed to load fleet status</div>';
  }
}

// Fanned out to every node (with a per-node timeout), so this runs on its
// own slower interval rather than alongside the 5s config-status poll.
async function fleetFetchUpdateSummary() {
  try {
    var r = await fetch('/api/admin/update-summary');
    fleetUpdateSummaryData = await r.json();
    fleetRender();
  } catch(e) {
    // Leave the last-known summary in place rather than blanking the banner
    // over one failed poll.
  }
}

// --- Inline notifications (no browser dialogs) ---

function fleetToast(msg, type) {
  if (typeof notify === 'function') {
    notify('Fleet', msg, { type: type || 'info', duration: 4000 });
  }
}

function fleetConfirm(msg, opts, onConfirm) {
  var existing = document.querySelector('.fleet-confirm-bar');
  if (existing) existing.remove();
  var bar = document.createElement('div');
  bar.className = 'fleet-confirm-bar' + (opts.danger ? ' fleet-confirm-danger' : '');
  bar.innerHTML = '<div class="fleet-confirm-msg">' + msg + '</div>' +
    '<div class="fleet-confirm-actions">' +
    '<button class="fleet-btn ' + (opts.danger ? 'fleet-btn-danger' : 'fleet-btn-primary') + ' fleet-confirm-yes">' +
    escHtml(opts.label || 'Confirm') + '</button>' +
    '<button class="fleet-btn fleet-btn-cancel fleet-confirm-no">Cancel</button>' +
    '</div>';
  var panel = document.getElementById('tab-fleet');
  panel.insertBefore(bar, panel.firstChild);
  bar.querySelector('.fleet-confirm-yes').onclick = function() { bar.remove(); onConfirm(); };
  bar.querySelector('.fleet-confirm-no').onclick = function() { bar.remove(); };
  bar.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

// --- View mode ---

function fleetRender() {
  if (!fleetData || fleetEditing) return;
  var panel = document.getElementById('tab-fleet');
  var d = fleetData;
  var prefs = d.preferences || {};
  var profiles = prefs.profiles || {};
  var pending = d.pending ? (typeof d.pending === 'string' ? JSON.parse(d.pending) : d.pending) : null;
  var html = '';

  html += '<div class="fleet-status-bar">';
  html += '<div class="fleet-sync">';
  var syncOk = !pending;
  html += '<span class="voice-dot ' + (syncOk ? 'on' : 'off') + '"></span>';
  html += syncOk ? 'All nodes in sync' : 'Config change pending';
  html += '</div>';
  html += '<span style="font-size:12px;color:var(--muted)">' + d.active_nodes + '/' + d.total_nodes + ' active';
  var pCount = Object.keys(profiles).length;
  if (pCount > 1) html += ' &middot; ' + pCount + ' profiles';
  html += '</span>';
  html += '<div class="fleet-actions">';
  if (!pending) html += '<button class="fleet-btn fleet-btn-primary" id="fleet-edit-btn">Edit Fleet Config</button>';
  html += '</div></div>';

  if (pending) html += fleetRenderPending(pending, d);
  html += fleetRenderUpdateBanner();

  // Network config
  var meshCfg = prefs.mesh_config || {};
  var curCfg = d.current_config || {};
  html += '<div class="fleet-section-header">Network Config <span class="fleet-section-sub">shared across all nodes in the mesh</span></div>';
  html += '<div class="fleet-card fleet-card-network">';
  MESH_FIELDS.forEach(function(f) {
    if (f.section) {
      html += '<div style="font-size:10px;font-weight:700;color:var(--accent2);margin:10px 0 4px;text-transform:uppercase;letter-spacing:.3px">' + escHtml(f.section) + '</div>';
      return;
    }
    var val = meshCfg[f.key] || curCfg[f.key] || '';
    var display = f.type === 'password' && val ? '••••••' : fleetOptLabel(f, val);
    html += '<div class="fleet-field-inline"><span class="fleet-field-label">' + escHtml(f.label) + '</span>';
    html += '<span>' + escHtml(display || '—') + '</span></div>';
  });
  html += '</div>';

  // Profiles
  var pNames = Object.keys(profiles);
  html += '<div class="fleet-section-header">Node Profiles <span class="fleet-section-sub">' + pNames.length + ' profile' + (pNames.length !== 1 ? 's' : '') + ' — per-profile settings override network config for assigned nodes</span></div>';
  html += '<div class="fleet-profiles-view">';
  Object.keys(profiles).forEach(function(pid) {
    var p = profiles[pid];
    var nodes = (d.nodes || []).filter(function(n) { return n.profile === pid; });
    html += '<div class="fleet-card">';
    html += '<div class="fleet-card-title">' + escHtml(p.name) + ' <span style="font-size:10px;font-weight:400;color:var(--muted);text-transform:none;letter-spacing:0">(' + nodes.length + ' node' + (nodes.length !== 1 ? 's' : '') + ')</span></div>';
    PROFILE_SECTIONS.forEach(function(sec) {
      html += '<div style="font-size:10px;font-weight:700;color:var(--accent2);margin:6px 0 2px;text-transform:uppercase">' + sec.cat + '</div>';
      sec.fields.forEach(function(f) {
        var val = (p.config || {})[f.key] || curCfg[f.key] || '';
        var display = f.type === 'password' && val ? '••••••' : fleetOptLabel(f, val);
        html += '<div class="fleet-field-inline"><span class="fleet-field-label">' + escHtml(f.label) + '</span>';
        html += '<span>' + escHtml(display || '—') + '</span>';
        if (f.hint) html += '<span style="font-size:10px;color:var(--muted);margin-left:6px">' + escHtml(f.hint) + '</span>';
        html += '</div>';
      });
    });
    if (nodes.length) {
      html += '<div style="margin-top:8px;display:flex;flex-wrap:wrap;gap:4px">';
      nodes.forEach(function(n) {
        html += '<span class="fleet-node-chip">' + escHtml(n.hostname || n.mac) + '</span>';
      });
      html += '</div>';
    }
    html += '</div>';
  });
  html += '</div>';

  // Node table
  html += fleetRenderNodes(d.nodes, pending);

  panel.innerHTML = html;
  var editBtn = document.getElementById('fleet-edit-btn');
  if (editBtn) editBtn.addEventListener('click', fleetStartEdit);
  var activateBtn = document.getElementById('fleet-activate-btn');
  if (activateBtn) activateBtn.addEventListener('click', function() { fleetActivateConfig(false); });
  var forceBtn = document.getElementById('fleet-force-btn');
  if (forceBtn) forceBtn.addEventListener('click', function() { fleetActivateConfig(true); });
  var cancelBtn = document.getElementById('fleet-cancel-btn');
  if (cancelBtn) cancelBtn.addEventListener('click', fleetCancelConfig);
  var forceUpdateSwBtn = document.getElementById('fleet-force-update-sw-btn');
  if (forceUpdateSwBtn) forceUpdateSwBtn.addEventListener('click', function() { fleetForceUpdate('software', forceUpdateSwBtn); });
  var forceUpdateOvBtn = document.getElementById('fleet-force-update-ov-btn');
  if (forceUpdateOvBtn) forceUpdateOvBtn.addEventListener('click', function() { fleetForceUpdate('overlay', forceUpdateOvBtn); });
}

// Renders a sticky "N of M nodes have an update available" banner, same
// visual language as the staged-config banner above it, when any node's
// own node-update status (polled via fleetFetchUpdateSummary) reports a
// software or overlay update available.
function fleetRenderUpdateBanner() {
  if (!fleetUpdateSummaryData || !fleetUpdateSummaryData.nodes) return '';
  var nodes = fleetUpdateSummaryData.nodes;
  var withUpdate = nodes.filter(function(n) {
    var s = n.status;
    return s && ((s.software && s.software.available) || (s.overlay && s.overlay.available));
  });
  if (!withUpdate.length) return '';

  var swNodes = nodes.filter(function(n) { return n.status && n.status.software && n.status.software.available; });
  var ovNodes = nodes.filter(function(n) { return n.status && n.status.overlay && n.status.overlay.available; });
  function belowCount(list) {
    return list.filter(function(n) {
      var s = n.status;
      return s.uplink_type && s.uplink_type !== 'wired' && (s.uplink_mbps || 0) < 10;
    }).length;
  }
  var swBelow = belowCount(swNodes);
  var ovBelow = belowCount(ovNodes);

  // Hide a channel's button while any of its nodes are actively applying
  // (picked up on the existing 30s update-summary poll) — same idea as the
  // per-node banner: don't leave a button clickable mid-operation.
  function inProgressCount(list) {
    return list.filter(function(n) {
      var p = n.status && n.status.phase;
      return p && p !== 'idle';
    }).length;
  }
  var swApplying = inProgressCount(swNodes);
  var ovApplying = inProgressCount(ovNodes);

  var html = '<div class="fleet-pending">';
  html += '<div class="fleet-pending-head"><div class="fleet-pending-title">Update Available</div></div>';
  html += '<div class="fleet-pending-meta">' + withUpdate.length + ' of ' + nodes.length +
    ' node' + (nodes.length !== 1 ? 's' : '') + ' have an update available';
  var parts = [];
  if (swNodes.length) parts.push(swNodes.length + ' MANET');
  if (ovNodes.length) parts.push(ovNodes.length + ' Kernel/Drivers');
  if (parts.length) html += ' (' + parts.join(', ') + ')';
  html += '</div>';
  html += '<div class="fleet-actions">';
  if (swApplying) {
    html += '<div class="fleet-pending-meta">' + swApplying + ' node' + (swApplying !== 1 ? 's' : '') + ' applying MANET update…</div>';
  } else if (swNodes.length) {
    html += '<button class="fleet-btn fleet-btn-primary" id="fleet-force-update-sw-btn" data-below="' +
      swBelow + '" data-total="' + swNodes.length + '">Force Update MANET (' + swNodes.length + ')</button>';
  }
  if (ovApplying) {
    html += '<div class="fleet-pending-meta">' + ovApplying + ' node' + (ovApplying !== 1 ? 's' : '') + ' applying Kernel/Drivers update…</div>';
  } else if (ovNodes.length) {
    html += '<button class="fleet-btn fleet-btn-danger" id="fleet-force-update-ov-btn" data-below="' +
      ovBelow + '" data-total="' + ovNodes.length + '">Force Update Kernel/Drivers (' + ovNodes.length + ')</button>';
  }
  html += '</div></div>';
  return html;
}

// channel is explicit ('software' or 'overlay') — separate buttons rather
// than one combined action, so overlay (no rollback) is never triggered as
// a side effect of a routine fleet-wide software push.
function fleetForceUpdate(channel, btn) {
  var below = parseInt(btn.getAttribute('data-below') || '0', 10);
  var total = parseInt(btn.getAttribute('data-total') || '0', 10);
  var channelLabel = channel === 'overlay' ? 'Kernel/Drivers' : 'MANET';

  var msg = 'Force ' + channelLabel + ' update on all ' + total + ' node' + (total !== 1 ? 's' : '') +
    ' with an update available now?';
  if (channel === 'overlay') {
    msg += ' The Kernel/Drivers channel updates kernel/firmware — there is no rollback if it fails to boot.';
  }
  if (below > 0) {
    msg = '⚠ ' + below + ' of ' + total + ' node' + (total !== 1 ? 's' : '') +
      (below === 1 ? ' is' : ' are') +
      ' below the recommended bandwidth and may take a long time to update, ' +
      'disrupting mesh connectivity during the download and reboot. ' + msg;
  }
  fleetConfirm(msg, { label: 'Force ' + channelLabel + ' Update', danger: below > 0 || channel === 'overlay' }, async function() {
    // Disable immediately — before the request resolves — so a second
    // click during the network round-trip can't fire a duplicate
    // broadcast. fleetFetchUpdateSummary() below picks up real per-node
    // progress (via status.phase) as soon as it lands.
    btn.disabled = true;
    try {
      var r = await fetch('/api/admin/force-update', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ channel: channel })
      });
      if (!r.ok) throw new Error('request failed');
      fleetToast(channelLabel + ' update triggered on all nodes', 'success');
      fleetFetchUpdateSummary();
    } catch(e) {
      btn.disabled = false;
      fleetToast('Failed to trigger update', 'error');
    }
  });
}

function fleetRenderPending(pkg, status) {
  var version = pkg.version || '?';
  var stagedBy = pkg.staged_by || '?';
  var activateAt = pkg.activate_at;
  var config = pkg.config || {};
  var html = '<div class="fleet-pending">';
  html += '<div class="fleet-pending-head"><div class="fleet-pending-title">Staged Configuration</div>';
  html += '<span class="fleet-pending-ver">v' + escHtml(version) + '</span></div>';
  html += '<div class="fleet-pending-meta">Staged by ' + escHtml(stagedBy) + '</div>';

  var current = status.current_config || {};
  var diffs = [];
  var hasDangerous = false;
  for (var k in config) {
    var cv = current[k] || '';
    var nv = config[k] || '';
    if (cv !== nv) {
      diffs.push({ key: k, old: cv, new: nv });
      if (['mesh_ssid','mesh_key','ipv4_network'].indexOf(k) !== -1) hasDangerous = true;
    }
  }
  if (hasDangerous) {
    html += '<div class="fleet-dangerous">Includes mesh SSID, key, or network changes — nodes may lose connectivity</div>';
  }
  var sensitiveKeys = ['admin_password', 'mesh_key', 'lan_ap_key'];
  if (diffs.length) {
    html += '<div class="fleet-diff">';
    diffs.forEach(function(d) {
      var masked = sensitiveKeys.indexOf(d.key) !== -1;
      html += '<div class="fleet-diff-row"><span class="fleet-diff-key">' + escHtml(d.key) + '</span>';
      html += '<span class="fleet-diff-old">' + (masked ? '••••••' : escHtml(d.old)) + '</span>';
      html += '<span class="fleet-diff-new">' + (masked ? '••••••' : escHtml(d.new)) + '</span></div>';
    });
    html += '</div>';
  }

  var nodes = status.nodes || [];
  var acked = nodes.filter(function(n) { return n.ack === version; }).length;
  var total = nodes.length;
  var pct = total > 0 ? Math.round(acked / total * 100) : 0;
  html += '<div class="fleet-ack-bar"><div class="fleet-ack-fill" style="width:' + pct + '%"></div></div>';
  html += '<div class="fleet-ack-label">' + acked + '/' + total + ' nodes acknowledged</div>';

  if (activateAt) {
    var remaining = activateAt - Math.floor(Date.now() / 1000);
    html += '<div class="fleet-countdown">' + (remaining > 0 ? 'Activating in ' + remaining + 's' : 'Activation in progress...') + '</div>';
  }

  html += '<div class="fleet-actions">';
  if (!activateAt) {
    var allAcked = acked === total && total > 0;
    html += '<button class="fleet-btn ' + (allAcked ? 'fleet-btn-go' : '') + '" id="fleet-activate-btn"' +
      (!allAcked ? ' disabled' : '') + '>Activate</button>';
    if (acked < total) html += '<button class="fleet-btn fleet-btn-danger" id="fleet-force-btn">Force Activate</button>';
  }
  html += '<button class="fleet-btn fleet-btn-danger" id="fleet-cancel-btn">Cancel</button>';
  html += '</div></div>';
  return html;
}

function fleetRenderNodes(nodes, pending) {
  if (!nodes || !nodes.length) return '';
  var version = pending ? (pending.version || '') : '';
  var html = '<div class="fleet-card" style="margin-top:20px">';
  html += '<div class="fleet-card-title">All Nodes</div>';
  html += '<table class="fleet-nodes-table"><tr><th>Node</th><th>IP</th><th>Profile</th><th>State</th>';
  if (version) html += '<th>ACK</th>';
  html += '</tr>';
  nodes.forEach(function(n) {
    html += '<tr><td>' + (n.ip ? '<a href="https://' + encodeURI(n.ip) + '/" target="_blank" class="node-link">' + escHtml(n.hostname || '—') + '</a>' : escHtml(n.hostname || '—')) + '</td>';
    html += '<td>' + escHtml(n.ip || '—') + '</td>';
    html += '<td>' + escHtml(n.profile || 'default') + '</td>';
    var stateClass = (n.node_state === 'OFFLINE') ? ' class="fleet-state-offline"' : '';
    html += '<td' + stateClass + '>' + escHtml(n.node_state || '—') + '</td>';
    if (version) {
      var acked = n.ack === version;
      html += '<td class="' + (acked ? 'fleet-ack-yes' : 'fleet-ack-no') + '">' + (acked ? 'Yes' : 'No') + '</td>';
    }
    html += '</tr>';
  });
  html += '</table></div>';
  return html;
}

// --- Edit mode ---

function fleetStartEdit() {
  fleetEditing = true;
  var panel = document.getElementById('tab-fleet');
  var curCfg = fleetData ? fleetData.current_config : {};
  var prefs = (fleetData && fleetData.preferences) ? fleetData.preferences : {};
  var meshCfg = prefs.mesh_config || {};
  var profiles = prefs.profiles || { default: { name: 'Default', config: {} } };
  var nodeProfiles = prefs.node_profiles || {};
  var nodes = fleetData ? fleetData.nodes : [];
  var html = '';

  html += '<div class="fleet-status-bar">';
  html += '<div class="fleet-sync" style="color:var(--accent2)">Editing Fleet Configuration</div>';
  html += '<div class="fleet-actions">';
  html += '<button class="fleet-btn fleet-btn-primary" id="fleet-stage-btn">Stage for Deployment</button>';
  html += '<button class="fleet-btn fleet-btn-cancel" id="fleet-edit-cancel">Cancel</button>';
  html += '</div></div>';

  // Network-wide config
  html += '<div class="fleet-card fleet-card-network">';
  html += '<div class="fleet-card-title">Network Config <span style="font-size:10px;font-weight:400;color:var(--muted);text-transform:none;letter-spacing:0">(applied to all nodes — no exceptions)</span></div>';
  MESH_FIELDS.forEach(function(f) {
    if (f.section) {
      html += '<div style="font-size:10px;font-weight:700;color:var(--accent2);margin:10px 0 4px;text-transform:uppercase;letter-spacing:.3px">' + escHtml(f.section) + '</div>';
      return;
    }
    var val = meshCfg[f.key] || curCfg[f.key] || '';
    html += fleetRenderField(f, val, 'mesh-');
  });
  html += '</div>';

  // Profiles
  html += '<div class="fleet-profiles-edit" id="fleet-profiles-edit">';
  html += '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px">';
  html += '<div style="font-size:14px;font-weight:700">Profiles</div>';
  html += '<button class="fleet-btn" id="fleet-add-profile">+ New Profile</button>';
  html += '</div>';

  Object.keys(profiles).forEach(function(pid) {
    html += fleetRenderProfileCard(pid, profiles[pid], curCfg, nodes, nodeProfiles);
  });
  html += '</div>';

  panel.innerHTML = html;

  document.getElementById('fleet-stage-btn').addEventListener('click', fleetStageConfig);
  document.getElementById('fleet-edit-cancel').addEventListener('click', function() { fleetEditing = false; fleetRender(); });
  document.getElementById('fleet-add-profile').addEventListener('click', fleetAddProfile);
  fleetBindProfileEvents();
}

function fleetRenderField(f, val, prefix) {
  var cls = f.dangerous ? ' fleet-field-danger' : '';
  var html = '<div class="fleet-field' + cls + '">';
  html += '<label>' + escHtml(f.label) + '</label>';
  if (f.type === 'select' && f.options) {
    html += '<select id="fleet-f-' + prefix + f.key + '">';
    f.options.forEach(function(opt) {
      var ov = typeof opt === 'object' ? opt.v : opt;
      var ol = typeof opt === 'object' ? opt.l : opt;
      html += '<option value="' + escHtml(ov) + '"' + (val === ov ? ' selected' : '') + '>' + escHtml(ol) + '</option>';
    });
    html += '</select>';
  } else {
    var inputType = f.type === 'password' ? 'password' : 'text';
    html += '<input type="' + inputType + '" id="fleet-f-' + prefix + f.key + '" value="' + escHtml(val) + '"' +
      (f.hint ? ' placeholder="' + escHtml(f.hint) + '"' : '') + '>';
  }
  if (f.dangerous) html += '<div class="fleet-field-hint">Changing this may disconnect nodes</div>';
  else if (f.hint) html += '<div style="font-size:10px;color:var(--muted);margin-top:2px">' + escHtml(f.hint) + '</div>';
  html += '</div>';
  return html;
}

function fleetRenderProfileCard(pid, profile, curCfg, nodes, nodeProfiles) {
  var cfg = profile.config || {};
  var isDefault = pid === 'default';
  var assignedMACs = [];
  for (var mac in nodeProfiles) { if (nodeProfiles[mac] === pid) assignedMACs.push(mac); }
  var unassignedNodes = nodes.filter(function(n) {
    var np = nodeProfiles[n.mac] || 'default';
    return np !== pid;
  });
  // Nodes in this profile: explicitly assigned + (for default) unassigned ones
  var profileNodes = nodes.filter(function(n) {
    var np = nodeProfiles[n.mac] || 'default';
    return np === pid;
  });

  var html = '<div class="fleet-profile-card" data-profile="' + escHtml(pid) + '">';
  html += '<div class="fleet-profile-head">';
  html += '<input type="text" class="fleet-profile-name" data-pid="' + escHtml(pid) + '" value="' + escHtml(profile.name) + '"' + (isDefault ? ' readonly' : '') + '>';
  html += '<span style="font-size:11px;color:var(--muted)">' + profileNodes.length + ' node' + (profileNodes.length !== 1 ? 's' : '') + '</span>';
  if (!isDefault) html += '<button class="fleet-btn fleet-btn-cancel fleet-del-profile" data-pid="' + escHtml(pid) + '" style="padding:3px 8px;font-size:11px">Delete</button>';
  html += '</div>';

  PROFILE_SECTIONS.forEach(function(sec) {
    html += '<div style="font-size:10px;font-weight:700;color:var(--accent2);margin:8px 0 4px;text-transform:uppercase">' + sec.cat + '</div>';
    sec.fields.forEach(function(f) {
      var val = cfg[f.key] || curCfg[f.key] || '';
      html += fleetRenderField(f, val, 'p-' + pid + '-');
    });
  });

  // Node assignment
  html += '<div style="margin-top:10px"><label style="font-size:11px;font-weight:600;color:var(--muted);text-transform:uppercase;letter-spacing:.3px">Assigned Nodes</label></div>';
  html += '<div class="fleet-profile-nodes" data-pid="' + escHtml(pid) + '">';
  profileNodes.forEach(function(n) {
    html += '<span class="fleet-node-chip fleet-node-removable" data-mac="' + escHtml(n.mac) + '" data-pid="' + escHtml(pid) + '">';
    html += escHtml(n.hostname || n.mac) + ' <span class="fleet-chip-x">&times;</span></span>';
  });
  if (unassignedNodes.length || !isDefault) {
    html += '<select class="fleet-node-add" data-pid="' + escHtml(pid) + '">';
    html += '<option value="">+ Add node</option>';
    unassignedNodes.forEach(function(n) {
      html += '<option value="' + escHtml(n.mac) + '">' + escHtml(n.hostname || n.mac) + '</option>';
    });
    html += '</select>';
  }
  html += '</div>';
  html += '</div>';
  return html;
}

function fleetBindProfileEvents() {
  document.querySelectorAll('.fleet-del-profile').forEach(function(btn) {
    btn.addEventListener('click', function() { fleetDeleteProfile(btn.dataset.pid); });
  });
  document.querySelectorAll('.fleet-node-removable .fleet-chip-x').forEach(function(x) {
    x.addEventListener('click', function(e) {
      e.stopPropagation();
      var chip = x.parentElement;
      fleetMoveNode(chip.dataset.mac, chip.dataset.pid, 'default');
    });
  });
  document.querySelectorAll('.fleet-node-add').forEach(function(sel) {
    sel.addEventListener('change', function() {
      if (sel.value) fleetMoveNode(sel.value, null, sel.dataset.pid);
    });
  });
}

function fleetMoveNode(mac, fromPid, toPid) {
  // Collect current edit state then re-render profiles
  var state = fleetCollectEditState();
  state.nodeProfiles[mac] = toPid;
  fleetRedrawProfiles(state);
}

function fleetAddProfile() {
  var state = fleetCollectEditState();
  var id = 'profile-' + Date.now().toString(36);
  state.profiles[id] = { name: 'New Profile', config: {} };
  fleetRedrawProfiles(state);
  setTimeout(function() {
    var nameInput = document.querySelector('.fleet-profile-name[data-pid="' + id + '"]');
    if (nameInput) { nameInput.focus(); nameInput.select(); }
  }, 50);
}

function fleetDeleteProfile(pid) {
  var state = fleetCollectEditState();
  delete state.profiles[pid];
  for (var mac in state.nodeProfiles) {
    if (state.nodeProfiles[mac] === pid) state.nodeProfiles[mac] = 'default';
  }
  fleetRedrawProfiles(state);
}

function fleetCollectEditState() {
  var meshCfg = {};
  MESH_FIELDS.forEach(function(f) {
    var el = document.getElementById('fleet-f-mesh-' + f.key);
    if (el) meshCfg[f.key] = el.value;
  });

  var profiles = {};
  var nodeProfiles = {};
  document.querySelectorAll('.fleet-profile-card').forEach(function(card) {
    var pid = card.dataset.profile;
    var nameInput = card.querySelector('.fleet-profile-name');
    var name = nameInput ? nameInput.value : pid;
    var cfg = {};
    PROFILE_SECTIONS.forEach(function(sec) {
      sec.fields.forEach(function(f) {
        var el = document.getElementById('fleet-f-p-' + pid + '-' + f.key);
        if (el) cfg[f.key] = el.value;
      });
    });
    profiles[pid] = { name: name, config: cfg };
  });

  document.querySelectorAll('.fleet-node-removable').forEach(function(chip) {
    nodeProfiles[chip.dataset.mac] = chip.dataset.pid;
  });

  return { meshCfg: meshCfg, profiles: profiles, nodeProfiles: nodeProfiles };
}

function fleetRedrawProfiles(state) {
  var curCfg = fleetData ? fleetData.current_config : {};
  var nodes = fleetData ? fleetData.nodes : [];
  var container = document.getElementById('fleet-profiles-edit');
  var html = '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px">';
  html += '<div style="font-size:14px;font-weight:700">Profiles</div>';
  html += '<button class="fleet-btn" id="fleet-add-profile">+ New Profile</button>';
  html += '</div>';
  Object.keys(state.profiles).forEach(function(pid) {
    html += fleetRenderProfileCard(pid, state.profiles[pid], curCfg, nodes, state.nodeProfiles);
  });
  container.innerHTML = html;
  document.getElementById('fleet-add-profile').addEventListener('click', fleetAddProfile);
  fleetBindProfileEvents();
}

// --- Stage / Activate / Cancel ---

async function fleetStageConfig() {
  var state = fleetCollectEditState();
  var btn = document.getElementById('fleet-stage-btn');
  btn.disabled = true;
  btn.textContent = 'Staging...';

  var curCfg = fleetData ? fleetData.current_config : {};
  var hasDangerous = ['mesh_ssid','mesh_key','ipv4_network'].some(function(k) {
    return state.meshCfg[k] && state.meshCfg[k] !== curCfg[k];
  });

  function doStage() {
    fleetSaveAndStage(state, btn);
  }

  if (hasDangerous) {
    btn.disabled = false;
    btn.textContent = 'Stage for Deployment';
    fleetConfirm(
      'This change includes mesh SSID, key, or network changes. Nodes may lose connectivity.',
      { label: 'Stage Anyway', danger: true },
      doStage
    );
  } else {
    doStage();
  }
}

async function fleetSaveAndStage(state, btn) {
  if (btn) { btn.disabled = true; btn.textContent = 'Staging...'; }
  try {
    await authFetch('/api/admin/preferences', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        mesh_config: state.meshCfg,
        profiles: state.profiles,
        node_profiles: state.nodeProfiles,
      }),
    });

    var r = await authFetch('/api/admin/stage', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ config: state.meshCfg }),
    });
    var result = await r.json();
    if (!result.ok) {
      fleetToast('Stage failed: ' + (result.error || 'unknown'), 'error');
      if (btn) { btn.disabled = false; btn.textContent = 'Stage for Deployment'; }
      return;
    }
    fleetToast('Configuration staged (v' + result.version + ')', 'success');
    fleetEditing = false;
    fleetFetch();
  } catch(e) {
    fleetToast('Stage failed: ' + e.message, 'error');
    if (btn) { btn.disabled = false; btn.textContent = 'Stage for Deployment'; }
  }
}

function fleetActivateConfig(force) {
  var label = force ? 'Force activate' : 'Activate';
  fleetConfirm(
    label + ' fleet configuration? All nodes will apply changes in 60 seconds.',
    { label: label, danger: force },
    async function() {
      try {
        var r = await authFetch('/api/admin/activate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ force: force }),
        });
        var result = await r.json();
        if (!result.ok) {
          fleetToast('Activate failed: ' + (result.error || 'unknown'), 'error');
          return;
        }
        fleetToast('Activation scheduled — applying in 60s', 'success');
        fleetFetch();
      } catch(e) {
        fleetToast('Activate failed: ' + e.message, 'error');
      }
    }
  );
}

function fleetCancelConfig() {
  fleetConfirm('Cancel the staged configuration?', { label: 'Cancel Staged Config' }, async function() {
    try {
      await authFetch('/api/admin/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      // Reset preferences mesh_config back to the running config
      var curCfg = fleetData ? fleetData.current_config || {} : {};
      var prefs = (fleetData && fleetData.preferences) ? fleetData.preferences : {};
      await authFetch('/api/admin/preferences', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          mesh_config: curCfg,
          profiles: prefs.profiles || {},
          node_profiles: prefs.node_profiles || {},
        }),
      });
      fleetToast('Staged configuration cancelled', 'info');
      fleetEditing = false;
      fleetFetch();
    } catch(e) {
      fleetToast('Cancel failed: ' + e.message, 'error');
    }
  });
}
