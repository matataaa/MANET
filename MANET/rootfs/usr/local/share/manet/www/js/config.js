// Config tab: view/edit node mesh.conf (local or remote via direct fetch)
let configInitialized = false;
let configEditing = false;
let configData = null;
let configVoiceData = null;
let configUpdateStatus = null;
let configTarget = null;

function configBaseUrl() {
  return configTarget ? '/api/peer/' + configTarget : '';
}

function configActivate() {
  const panel = document.getElementById('tab-config');
  if (!configInitialized) {
    panel.innerHTML =
      '<div class="cfg-target-bar">' +
        '<select id="cfg-target"></select>' +
      '</div>' +
      '<div id="cfg-content"><div class="loading-msg">Loading configuration...</div></div>';
    document.getElementById('cfg-target').addEventListener('change', function() {
      configTarget = this.value || null;
      configEditing = false;
      configData = null;
      configVoiceData = null;
      configFetch();
    });
    configInitialized = true;
  }
  configPopulateTargets();
  if (window._pendingConfigTarget) {
    var p = window._pendingConfigTarget;
    window._pendingConfigTarget = null;
    configTarget = p.ip;
    var sel = document.getElementById('cfg-target');
    if (sel) sel.value = p.ip;
  }
  configFetch();
}

function configPopulateTargets() {
  var sel = document.getElementById('cfg-target');
  if (!sel || !DATA || !DATA.nodes) return;
  var current = configTarget || '';
  sel.innerHTML = '<option value="">This Node' + (LOCAL_DATA ? ' (' + (LOCAL_DATA.hostname || LOCAL_DATA.ip || '') + ')' : '') + '</option>';
  DATA.nodes.forEach(function(n) {
    if (n.is_me || !n.ip) return;
    var opt = document.createElement('option');
    opt.value = n.ip;
    opt.textContent = (n.hostname || n.ip) + ' (' + n.ip + ')';
    sel.appendChild(opt);
  });
  sel.value = current;
}

async function configFetch() {
  try {
    var base = configBaseUrl();
    const adminR = await fetch(base + '/api/admin/status');
    configData = await adminR.json();
    try {
      const voiceR = await fetch(base + '/api/voice');
      configVoiceData = await voiceR.json();
    } catch(e) { configVoiceData = null; }
    try {
      const updR = await fetch(base + '/api/admin/update-status');
      configUpdateStatus = await updR.json();
    } catch(e) { configUpdateStatus = null; }
    configRender();
  } catch(e) {
    document.getElementById('cfg-content').innerHTML =
      '<div class="loading-msg">Failed to load config' + (configTarget ? ' from ' + escHtml(configTarget) : '') + '</div>';
  }
}

function configRender() {
  if (!configData) return;
  const panel = document.getElementById('cfg-content');
  const cfg = configData.current_config || {};

  if (configEditing) {
    configRenderEdit(panel, cfg);
  } else {
    configRenderView(panel, cfg);
  }
}

function configRenderView(panel, cfg) {
  const sections = [
    { title: 'Node', fields: [
      { label: 'Hostname Prefix', key: 'node_hostname' },
      { label: 'Full Hostname', computed: () => {
        if (configTarget && DATA && DATA.nodes) {
          var node = DATA.nodes.find(function(n) { return n.ip === configTarget; });
          return node ? (node.hostname || '--') : '--';
        }
        return (LOCAL_DATA && LOCAL_DATA.hostname) || '--';
      }},
    ]},
    { title: 'Mesh Settings', fields: [
      { label: 'Mesh SSID', key: 'mesh_ssid' },
      { label: 'Mesh Key', key: 'mesh_key', masked: true },
      { label: 'IPv4 Network', key: 'ipv4_network' },
      { label: 'Regulatory Domain', key: 'regulatory_domain' },
      { label: 'HaLow Regulatory Domain', key: 'halow_regulatory_domain', fmt: function(v) { return v || 'Inherit'; } },
      { label: 'HaLow Bandwidth', key: 'halow_bw' },
      { label: 'HaLow Channel', key: 'halow_channel', fmt: function(v) {
        if (!v) return 'Auto';
        var domain = cfg.halow_regulatory_domain || cfg.regulatory_domain || 'US';
        var startKHz = { US: 902000, EU: 863000 }[domain];
        var ch = parseInt(v, 10);
        if (startKHz && !isNaN(ch)) return v + ' (' + ((startKHz + ch * 500) / 1000) + ' MHz)';
        return v;
      } },
      { label: '5GHz Mesh Channel Mode', key: 'acs', fmt: function(v) { return v === 'y' ? 'Automatic (ACS)' : 'Static (pinned)'; } },
      { label: '5GHz Mesh Width', key: 'mesh_5ghz_bw', fmt: function(v) { return v === '80' ? '80 MHz' : v === '40' ? '40 MHz' : '20 MHz (default)'; } },
      { label: '5GHz Pinned Channel', key: 'mesh_5ghz_channel', fmt: function(v) {
        var chLabel = v || '36 (default)';
        return cfg.acs === 'y' ? chLabel + ' — unused while Automatic (ACS) is active' : chLabel;
      } },
      { label: 'Multicast Mode', key: 'multicast_mode', fmt: function(v) { return v === 'optimized' ? 'Optimized (IGMP)' : 'Flood (default)'; } },
    ]},
    { title: 'Services', fields: [
      { label: 'Battery Monitor', key: 'battery_monitor', yesno: true },
      { label: 'Auto Update', key: 'auto_update', yesno: true },
      { label: 'Update URL', key: 'update_url' },
      { label: 'Auto Update Overlay', key: 'auto_update_overlay', yesno: true },
      { label: 'Auto Update Min Bandwidth', key: 'auto_update_min_mbps', fmt: function(v) { return (v || '10') + ' Mbit'; } },
    ]},
    { title: 'GPS / CoT', fields: [
      { label: 'GPS Enabled', key: 'gps', fmt: function(v) { return (v||'').toLowerCase() === 'n' ? 'Disabled' : 'Enabled'; } },
      { label: 'GPS Source', key: 'gps_source', fmt: function(v) { return v === 'static' ? 'Static (fixed location)' : 'Receiver (hardware GPS)'; },
        showIf: [{key:'gps', equals:'y'}] },
      { label: 'Latitude', key: 'gps_static_lat', showIf: [{key:'gps', equals:'y'}, {key:'gps_source', equals:'static'}] },
      { label: 'Longitude', key: 'gps_static_lon', showIf: [{key:'gps', equals:'y'}, {key:'gps_source', equals:'static'}] },
      { label: 'Altitude', key: 'gps_static_alt', showIf: [{key:'gps', equals:'y'}, {key:'gps_source', equals:'static'}] },
      { label: 'Callsign', key: 'callsign' },
      { label: 'CoT Type', key: 'cot_type', fmt: function(v) { return v || 'a-f-G-E (equipment, default)'; } },
      { label: 'CoT Team', key: 'cot_team', fmt: function(v) { return v || '(none — equipment identity)'; } },
      { label: 'CoT Role', key: 'cot_role', fmt: function(v) { return v || 'Team Member'; }, showIf: [{key:'cot_team', notEmpty:true}] },
      { label: 'CoT Icon', key: 'cot_icon', fmt: function(v) { return v || '(default for type)'; } },
    ]},
    { title: 'Access Point', fields: [
      { label: 'EUD Mode', key: 'eud' },
      { label: 'AP SSID', key: 'lan_ap_ssid' },
      { label: 'AP Key', key: 'lan_ap_key', masked: true },
      { label: 'AP Channel', key: 'lan_ap_channel' },
      { label: 'AP Bandwidth', key: 'lan_ap_bw', fmt: function(v) { return v ? v + ' MHz' : '—'; } },
      { label: 'Max EUDs/Node', key: 'max_euds_per_node' },
      { label: 'EUD Bandwidth Cap', key: 'eud_bandwidth', fmt: function(v) { return (!v || v === '0') ? 'Unlimited' : v + ' Mbit (symmetric)'; } },
    ]},
    { title: 'Gateway', fields: [
      { label: 'Gateway Enabled', key: 'gateway', yesno: true },
      { label: 'NAT Masquerade', key: 'gateway_nat', yesno: true },
      { label: 'MSS Clamping', key: 'gateway_mss_clamp', yesno: true },
      { label: 'Bandwidth Advertisement', key: 'gateway_bandwidth' },
      { label: 'DNS Servers', key: 'dns_servers' },
    ]},
    { title: 'Access', fields: [
      { label: 'Admin Key', key: 'admin_password', masked: true },
      { label: 'Require Auth', key: 'require_auth', fmt: function(v) { return (v||'').toLowerCase() === 'y' ? 'Yes' : 'No'; } },
    ]},
    { title: 'Voice', voice: true, fields: [
      { label: 'Voice Enabled', key: 'voice_enabled', yesno: true },
      { label: 'PTT Mode', voiceKey: 'ptt_mode' },
      { label: 'TX Channel', voiceKey: 'channel', fmt: function(v) { return 'Ch ' + (v || '1'); } },
      { label: 'Mic Volume', key: 'voice_mic_volume', fmt: function(v) { return (v || '80') + '%'; } },
      { label: 'Speaker Volume', key: 'voice_speaker_volume', fmt: function(v) { return (v || '80') + '%'; } },
      { label: 'Port', voiceKey: 'port', fallback: '4370' },
      { label: 'Interface', voiceKey: 'interface', fallback: 'br0' },
      { label: 'Mic Gain', key: 'voice_gain', fmt: function(v) { return (v || '3.0') + 'x'; } },
      { label: 'TX Beep', key: 'voice_beep_tx_start', fmt: function(v) { return (v||'').toLowerCase() === 'n' ? 'Off' : 'On'; } },
      { label: 'RX End Beep', key: 'voice_beep_rx_end', fmt: function(v) { return (v||'').toLowerCase() === 'n' ? 'Off' : 'On'; } },
    ]},
  ];

  let html = '<div>';

  html += configRenderUpdateBanner(cfg);

  sections.forEach(sec => {
    html += '<div class="card cfg-section"><div class="cfg-section-title">' + sec.title + '</div>';
    sec.fields.forEach(f => {
      if (f.showIf && !f.showIf.every(cond => {
        const v = cfg[cond.key] || '';
        return cond.notEmpty ? v !== '' : v === cond.equals;
      })) return;
      let val;
      if (f.voiceKey) {
        val = (configVoiceData && configVoiceData[f.voiceKey]) || f.fallback || '--';
      } else {
        val = f.computed ? f.computed() : (cfg[f.key] || '--');
      }
      let cls = 'cfg-value';
      if (f.masked && val !== '--') { val = '••••••••'; cls += ' masked'; }
      if (f.fmt) val = f.fmt(val);
      else if (f.yesno) val = val === 'y' ? 'Enabled' : 'Disabled';
      html += '<div class="cfg-row"><div class="cfg-label">' + f.label + '</div><div class="' + cls + '">' + escHtml(String(val)) + '</div></div>';
    });
    html += '</div>';
  });

  html += '<div id="qos-card" class="card cfg-section"><div class="card-header">QOS PRIORITY</div><div class="loading-msg" style="padding:12px">Loading...</div></div>';

  var hasPending = configData && configData.pending;
  if (hasPending) {
    html += '<div class="card cfg-section" style="border-color:var(--warn);background:rgba(245,158,11,.06)">' +
      '<div style="padding:12px;font-size:12px;color:var(--warn);font-weight:700">Fleet config is staged — activate or cancel it before editing locally</div></div>';
  }
  html += '<div style="padding:4px 0"><button class="cfg-btn cfg-btn-primary" id="cfg-edit-btn"' +
    (hasPending ? ' disabled style="opacity:.5;cursor:not-allowed"' : '') + '>Edit Configuration</button></div>';
  html += '</div>';
  panel.innerHTML = html;

  document.getElementById('cfg-edit-btn').addEventListener('click', () => {
    configEditing = true;
    configRender();
  });
  configWireUpdateButtons();

  qosFetch();
}

// Persistent "update available" banner — reuses the same sticky-bar visual
// language fleet.js uses for its staged-config banner, since this needs to
// persist until acted on rather than auto-dismiss like a notify() toast.
function configRenderUpdateBanner(cfg) {
  var st = configUpdateStatus;
  if (!st) return '';
  var swAvail = st.software && st.software.available;
  var ovAvail = st.overlay && st.overlay.available;
  if (!swAvail && !ovAvail) return '';

  var parts = [];
  if (swAvail) parts.push('MANET v' + escHtml(st.software.local) + ' → v' + escHtml(st.software.remote));
  if (ovAvail) parts.push('Kernel/Drivers v' + escHtml(st.overlay.local) + ' → v' + escHtml(st.overlay.remote));

  var html = '<div class="fleet-pending">';
  html += '<div class="fleet-pending-head"><div class="fleet-pending-title">Update Available</div></div>';
  html += '<div class="fleet-pending-meta">' + parts.join(' &middot; ') + '</div>';

  // While an apply is actually in flight (phase != idle), show real
  // progress instead of the buttons — the daemon downloads/extracts
  // synchronously, so this is genuine state, not a guess. This also
  // doubles as the double-click fix: as long as this keeps getting
  // re-rendered from live status, there's no window where the buttons
  // are clickable during an in-progress apply.
  var phase = st.phase || 'idle';
  if (phase !== 'idle') {
    html += '<div class="fleet-pending-meta" id="cfg-update-phase">' + escHtml(configPhaseLabel(st)) + '</div>';
    if (phase === 'rebooting') {
      html += '<div class="fleet-actions" style="justify-content:flex-end">' +
        '<button class="fleet-btn" id="cfg-reboot-now-btn">Reboot Now</button></div>';
    }
  } else {
    html += '<div class="fleet-actions">';
    if (swAvail) html += '<button class="fleet-btn fleet-btn-primary" id="cfg-update-now-sw-btn">Update MANET</button>';
    if (ovAvail) html += '<button class="fleet-btn fleet-btn-danger" id="cfg-update-now-ov-btn">Update Kernel/Drivers</button>';
    html += '</div>';
  }
  html += '</div>';
  return html;
}

// Takes the full status (not just the phase string) so the "rebooting"
// case can compute a live countdown from reboot_at — node-update writes
// the actual jittered reboot time, so this isn't a guess.
function configPhaseLabel(st) {
  var phase = st.phase || 'idle';
  if (phase === 'rebooting' && st.reboot_at) {
    var secs = Math.max(0, Math.round((new Date(st.reboot_at).getTime() - Date.now()) / 1000));
    var m = Math.floor(secs / 60), s = secs % 60;
    return 'Update applied — rebooting in ' + m + 'm ' + (s < 10 ? '0' : '') + s + 's…';
  }
  var labels = {
    'downloading software': 'Downloading MANET update…',
    'extracting software': 'Extracting MANET update…',
    'downloading overlay': 'Downloading Kernel/Drivers update…',
    'extracting overlay': 'Extracting Kernel/Drivers update…',
    'rebooting': 'Update applied — rebooting now…'
  };
  return labels[phase] || (phase.charAt(0).toUpperCase() + phase.slice(1) + '…');
}

// Wires whichever per-channel update buttons the banner actually rendered —
// separate buttons (not one combined action) so overlay, which has no
// rollback, is never applied as a side effect of clicking through a routine
// software update in the field.
function configWireUpdateButtons() {
  var swBtn = document.getElementById('cfg-update-now-sw-btn');
  if (swBtn) swBtn.addEventListener('click', function() { configUpdateNow('software', swBtn); });
  var ovBtn = document.getElementById('cfg-update-now-ov-btn');
  if (ovBtn) ovBtn.addEventListener('click', function() { configUpdateNow('overlay', ovBtn); });
  var rebootBtn = document.getElementById('cfg-reboot-now-btn');
  if (rebootBtn) rebootBtn.addEventListener('click', function() { configRebootNow(rebootBtn); });
}

// Overrides the jittered shutdown node-update already scheduled — reuses
// the existing /api/terminal/reboot endpoint (same one Services uses) to
// reboot immediately rather than waiting out the 1-15 min spread. Safe:
// the update itself already finished applying before this phase, so an
// earlier reboot just reaches the same end state sooner.
function configRebootNow(btn) {
  configConfirm('Reboot now instead of waiting for the scheduled time? You will lose connection until it comes back up.',
    { label: 'Reboot Now', danger: true }, async function() {
      btn.disabled = true;
      try {
        var r = await authFetch(configBaseUrl() + '/api/terminal/reboot', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: '{}'
        });
        if (!r.ok) throw new Error('request failed');
        notify('Update', 'Rebooting now', { type: 'success' });
        configStopUpdatePoll();
      } catch (e) {
        btn.disabled = false;
        notify('Update', 'Failed to trigger reboot', { type: 'error' });
      }
    });
}

var configUpdatePollTimer = null;
var configRebootTickTimer = null;

// Polls update-status every 3s while an apply is in progress and patches
// just the banner in place — not a full configFetch()/re-render, so this
// can safely run while the operator is mid-edit elsewhere on the page.
function configStartUpdatePoll() {
  if (configUpdatePollTimer) return;
  configUpdatePollTimer = setInterval(async function() {
    try {
      var r = await fetch(configBaseUrl() + '/api/admin/update-status');
      var st = await r.json();
      configUpdateStatus = st;
      configRefreshUpdateBanner();
      var phase = st.phase || 'idle';
      if (phase === 'idle') {
        configStopUpdatePoll();
        // Apply finished (success or failure) — refresh the full view so
        // availability reflects the new state: banner disappears on
        // success, or the buttons come back for a retry on failure.
        if (!configEditing) configFetch();
      } else if (phase === 'rebooting') {
        configStartRebootTick();
        // Node is about to disappear — no point chasing connectivity
        // once it does.
        setTimeout(configStopUpdatePoll, 15000);
      }
    } catch (e) {
      // Node likely mid-reboot / unreachable — stop rather than spam
      // failed requests indefinitely.
      configStopUpdatePoll();
    }
  }, 3000);
}

// Ticks the "rebooting in Xm Ys" text every second between 3s status
// polls, computed client-side from the reboot_at timestamp node-update
// already reported — no extra network traffic for the countdown itself.
function configStartRebootTick() {
  if (configRebootTickTimer) return;
  configRebootTickTimer = setInterval(function() {
    var el = document.getElementById('cfg-update-phase');
    if (!el || !configUpdateStatus || configUpdateStatus.phase !== 'rebooting') {
      configStopRebootTick();
      return;
    }
    el.textContent = configPhaseLabel(configUpdateStatus);
  }, 1000);
}

function configStopRebootTick() {
  if (configRebootTickTimer) { clearInterval(configRebootTickTimer); configRebootTickTimer = null; }
}

function configStopUpdatePoll() {
  if (configUpdatePollTimer) { clearInterval(configUpdatePollTimer); configUpdatePollTimer = null; }
  configStopRebootTick();
}

// Re-renders just the update banner from the current configUpdateStatus,
// in place, without touching the rest of the panel.
function configRefreshUpdateBanner() {
  var container = document.getElementById('cfg-content');
  if (!container) return;
  var old = container.querySelector('.fleet-pending');
  var html = configRenderUpdateBanner();
  if (!html) {
    if (old) old.remove();
    return;
  }
  var wrap = document.createElement('div');
  wrap.innerHTML = html;
  var fresh = wrap.firstElementChild;
  if (old) old.replaceWith(fresh);
  else container.insertBefore(fresh, container.firstChild);
  configWireUpdateButtons();
}

// Minimal confirm-bar, mirroring fleetConfirm() in fleet.js but scoped to
// #cfg-content — kept local rather than depending on fleet.js being loaded.
function configConfirm(msg, opts, onConfirm) {
  var panel = document.getElementById('cfg-content');
  var existing = panel.querySelector('.fleet-confirm-bar');
  if (existing) existing.remove();
  var bar = document.createElement('div');
  bar.className = 'fleet-confirm-bar' + (opts.danger ? ' fleet-confirm-danger' : '');
  bar.innerHTML = '<div class="fleet-confirm-msg">' + msg + '</div>' +
    '<div class="fleet-confirm-actions">' +
    '<button class="fleet-btn ' + (opts.danger ? 'fleet-btn-danger' : 'fleet-btn-primary') + ' fleet-confirm-yes">' +
    escHtml(opts.label || 'Confirm') + '</button>' +
    '<button class="fleet-btn fleet-btn-cancel fleet-confirm-no">Cancel</button>' +
    '</div>';
  panel.insertBefore(bar, panel.firstChild);
  bar.querySelector('.fleet-confirm-yes').onclick = function() { bar.remove(); onConfirm(); };
  bar.querySelector('.fleet-confirm-no').onclick = function() { bar.remove(); };
  bar.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

function configUpdateNow(channel, btn) {
  var st = configUpdateStatus || {};
  var channelLabel = channel === 'overlay' ? 'Kernel/Drivers' : 'MANET';

  var mbps = st.uplink_mbps || 0;
  var uplinkType = st.uplink_type || 'unknown';
  var minMbps = parseFloat((configData && configData.current_config && configData.current_config.auto_update_min_mbps) || '10');
  var belowThreshold = uplinkType !== 'wired' && mbps < minMbps;

  var msg = 'Update ' + channelLabel + ' now? This downloads the update and reboots this node once applied.';
  if (channel === 'overlay') {
    msg += ' The Kernel/Drivers channel updates kernel/firmware — there is no rollback if it fails to boot.';
  }
  if (belowThreshold) {
    msg = '⚠ Current link is ' + mbps.toFixed(1) + ' Mbps (' + uplinkType + '), below the ' + minMbps +
      ' Mbps recommended for auto-update. This may take a long time and could disrupt mesh connectivity. ' +
      'Consider using a higher-bandwidth connection (Ethernet, WiFi mesh, or 8MHz HaLow) if available. ' + msg;
  }

  configConfirm(msg, { label: 'Update ' + channelLabel, danger: belowThreshold || channel === 'overlay' }, async function() {
    // Disable immediately — before the request even resolves — so a
    // second click during the network round-trip can't fire a duplicate
    // trigger. The status poll below takes over showing real progress
    // once the daemon actually starts working.
    if (btn) btn.disabled = true;
    try {
      var r = await fetch(configBaseUrl() + '/api/admin/update-now', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ channel: channel })
      });
      if (!r.ok) throw new Error('request failed');
      notify('Update', 'Update triggered', { type: 'success' });
      configStartUpdatePoll();
    } catch(e) {
      if (btn) btn.disabled = false;
      notify('Update', 'Failed to trigger update', { type: 'error' });
    }
  });
}

function configRenderEdit(panel, cfg) {
  const fields = [
    { section: 'Node' },
    { label: 'Node Hostname', key: 'node_hostname', type: 'text', hint: 'Prefix — full hostname: {this}-{ssid}-{mac}', preview: true },
    { section: 'Mesh Settings' },
    { label: 'Mesh SSID', key: 'mesh_ssid', type: 'text' },
    { label: 'Mesh Key', key: 'mesh_key', type: 'password' },
    { label: 'IPv4 Network', key: 'ipv4_network', type: 'text' },
    { label: 'Regulatory Domain', key: 'regulatory_domain', type: 'select', options: ['US', 'EU', 'JP', 'AU'] },
    { label: 'HaLow Regulatory Domain', key: 'halow_regulatory_domain', type: 'select', options: [
      {v:'',l:'Inherit from Regulatory Domain'},'US','EU','JP','AU'
    ], hint: 'Overrides the regulatory domain for the HaLow radio specifically, independent of the WiFi Regulatory Domain above (e.g. an MM8108 unit can run HaLow on a different domain than its 2.4/5GHz radios). Empty = inherit.' },
    { label: 'HaLow Bandwidth', key: 'halow_bw', type: 'select', options: [
      {v:'1MHz',l:'1 MHz'},{v:'2MHz',l:'2 MHz'},{v:'4MHz',l:'4 MHz'},{v:'8MHz',l:'8 MHz'}
    ], hint: 'Primary channel width for 802.11ah mesh. EU supports 1MHz only. Narrower = longer range.' },
    { label: 'HaLow Channel', key: 'halow_channel', type: 'select', options: [{v:'',l:'Auto'}],
      hint: 'Explicit HaLow channel for the current regulatory domain/bandwidth. Auto (default) picks the standard channel for that combination.' },
    { label: '5GHz Mesh Channel Mode', key: 'acs', type: 'select', options: [
      {v:'n',l:'Static (pinned channel)'},{v:'y',l:'Automatic (ACS)'}
    ], hint: 'Static pins the 5GHz (and 2.4GHz) mesh to a fixed channel — deterministic, recommended. Automatic elects a channel via scanning/consensus across the fleet. Live — applies within one 15s tick, no restart needed.' },
    { label: '5GHz Mesh Width', key: 'mesh_5ghz_bw', type: 'select', options: [
      {v:'20',l:'20 MHz (default)'},{v:'40',l:'40 MHz'},{v:'80',l:'80 MHz'}
    ], hint: 'Fleet-wide — never mix widths across nodes. 40MHz requires the patched wpa_supplicant and silently falls back to 20MHz without it. 80MHz without the patch can mismatch primary channel between peers.' },
    { label: '5GHz Pinned Channel', key: 'mesh_5ghz_channel', type: 'select', options: [
      {v:'',l:'36 / 5180 MHz (default)'},
      {v:'40',l:'40 / 5200 MHz'},{v:'44',l:'44 / 5220 MHz'},{v:'48',l:'48 / 5240 MHz'},
      {v:'149',l:'149 / 5745 MHz'},{v:'153',l:'153 / 5765 MHz'},{v:'157',l:'157 / 5785 MHz'},{v:'161',l:'161 / 5805 MHz'},{v:'165',l:'165 / 5825 MHz'}
    ], hint: 'Only used when 5GHz Mesh Channel Mode above is Static. Ignored under Automatic (ACS), which elects its own channel.',
      showIf: [{key:'acs', equals:'n'}] },
    { label: 'Multicast Mode', key: 'multicast_mode', type: 'select', options: [
      {v:'flood',l:'Flood (recommended ≤10 nodes)'},
      {v:'optimized',l:'Optimized IGMP (10+ nodes)'}
    ], hint: 'Flood sends all multicast to all peers; IGMP uses snooping for selective delivery' },
    { section: 'GPS / CoT' },
    { label: 'GPS Enabled', key: 'gps', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}],
      hint: 'No on hardware with no GPS module — stops gpsd/gps-reader instead of leaving them polling for a fix that will never come.' },
    { label: 'GPS Source', key: 'gps_source', type: 'select', options: [
        {v:'receiver',l:'Receiver (hardware GPS)'},{v:'static',l:'Static (fixed location)'}
      ], hint: 'Static reports a fixed position with no GPS module needed — e.g. a stationary gateway node.',
      showIf: [{key:'gps', equals:'y'}] },
    { label: 'Latitude', key: 'gps_static_lat', type: 'text', hint: 'Decimal degrees, e.g. 52.859337',
      showIf: [{key:'gps', equals:'y'}, {key:'gps_source', equals:'static'}] },
    { label: 'Longitude', key: 'gps_static_lon', type: 'text', hint: 'Decimal degrees, e.g. 6.513487',
      showIf: [{key:'gps', equals:'y'}, {key:'gps_source', equals:'static'}] },
    { label: 'Altitude', key: 'gps_static_alt', type: 'text', hint: 'Meters above sea level',
      showIf: [{key:'gps', equals:'y'}, {key:'gps_source', equals:'static'}] },
    { label: 'Callsign', key: 'callsign', type: 'text', hint: 'Blank = hostname' },
    { label: 'CoT Type', key: 'cot_type', type: 'text', hint: 'Blank = a-f-G-E (equipment, no team). CoT/2525 type code.' },
    { label: 'CoT Team', key: 'cot_team', type: 'select', options: [
        {v:'',l:'No affiliation'},
        {v:'White',l:'White'},{v:'Yellow',l:'Yellow'},{v:'Orange',l:'Orange'},{v:'Magenta',l:'Magenta'},
        {v:'Red',l:'Red'},{v:'Maroon',l:'Maroon'},{v:'Purple',l:'Purple'},{v:'Dark Blue',l:'Dark Blue'},
        {v:'Blue',l:'Blue'},{v:'Cyan',l:'Cyan'},{v:'Teal',l:'Teal'},{v:'Green',l:'Green'},
        {v:'Dark Green',l:'Dark Green'},{v:'Brown',l:'Brown'}
      ], hint: 'ATAK team-color affiliation. No affiliation = shown as equipment, no team.' },
    { label: 'CoT Role', key: 'cot_role', type: 'text', hint: 'Blank = Team Member.',
      showIf: [{key:'cot_team', notEmpty:true}] },
    { label: 'CoT Icon', key: 'cot_icon', type: 'text', hint: 'Blank = default icon for the type. Optional iconset path override.' },
    { section: 'Access Point' },
    { label: 'EUD Mode', key: 'eud', type: 'select', options: ['wired', 'wireless', 'both', 'auto', 'none'] },
    { label: 'AP SSID', key: 'lan_ap_ssid', type: 'text' },
    { label: 'AP Key', key: 'lan_ap_key', type: 'password' },
    { label: 'AP Channel', key: 'lan_ap_channel', type: 'select', options: [
      {v:'',l:'Auto'},
      {v:'1',l:'1 (2.4 GHz)'},{v:'6',l:'6 (2.4 GHz)'},{v:'11',l:'11 (2.4 GHz)'},
      {v:'36',l:'36 (5 GHz)'},{v:'40',l:'40 (5 GHz)'},{v:'44',l:'44 (5 GHz)'},{v:'48',l:'48 (5 GHz)'},
      {v:'149',l:'149 (5 GHz)'},{v:'153',l:'153 (5 GHz)'},{v:'157',l:'157 (5 GHz)'},{v:'161',l:'161 (5 GHz)'}
    ] },
    { label: 'AP Bandwidth', key: 'lan_ap_bw', type: 'select', options: [
      {v:'20',l:'20 MHz'},{v:'40',l:'40 MHz'},{v:'80',l:'80 MHz'}
    ] },
    { label: 'Max EUDs/Node', key: 'max_euds_per_node', type: 'text' },
    { label: 'EUD Bandwidth Cap', key: 'eud_bandwidth', type: 'select', options: [
      {v:'0',l:'Unlimited'},{v:'0.25',l:'0.25 Mbit'},{v:'0.5',l:'0.5 Mbit'},{v:'1',l:'1 Mbit'},{v:'2',l:'2 Mbit'},{v:'5',l:'5 Mbit'},
      {v:'10',l:'10 Mbit'},{v:'20',l:'20 Mbit'},{v:'50',l:'50 Mbit'},{v:'100',l:'100 Mbit'}
    ], hint: 'Per-device symmetric bandwidth limit for connected EUDs' },
    { section: 'Services' },
    { label: 'Battery Monitor', key: 'battery_monitor', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Auto Update', key: 'auto_update', type: 'select', options: [{v:'n',l:'No'},{v:'y',l:'Yes'}], hint: 'Checks for a new release every 6h, and immediately when this setting is saved' },
    { label: 'Update URL', key: 'update_url', type: 'text', hint: 'Base URL for OTA tarball server (blank = disabled)' },
    { label: 'Auto Update Overlay (kernel/firmware)', key: 'auto_update_overlay', type: 'select', options: [{v:'n',l:'No'},{v:'y',l:'Yes'}], hint: 'Updates the kernel/modules/firmware. No rollback if a bad overlay fails to boot — test on one node before enabling fleet-wide. Off by default.' },
    { label: 'Auto Update Min Bandwidth (Mbit)', key: 'auto_update_min_mbps', type: 'text', hint: 'Automatic apply is skipped below this link speed. Manual "Update Now" and fleet-wide force update ignore it (with a warning).' },
    { section: 'Gateway' },
    { label: 'Gateway Enabled', key: 'gateway', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}], hint: 'Allow this node to act as a mesh gateway' },
    { label: 'NAT Masquerade', key: 'gateway_nat', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'MSS Clamping', key: 'gateway_mss_clamp', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Bandwidth', key: 'gateway_bandwidth', type: 'select', options: [
      {v:'',l:'Auto (batman default)'},{v:'2M/2M',l:'2M/2M'},{v:'5M/5M',l:'5M/5M'},{v:'10M/10M',l:'10M/10M'},
      {v:'20M/20M',l:'20M/20M'},{v:'50M/50M',l:'50M/50M'},{v:'100M/100M',l:'100M/100M'}
    ] },
    { label: 'DNS Servers', key: 'dns_servers', type: 'text', hint: 'Comma-separated (e.g. 8.8.8.8,8.8.4.4)' },
    { section: 'Access' },
    { label: 'Admin Key', key: 'admin_password', type: 'password' },
    { label: 'Require Auth', key: 'require_auth', type: 'select', options: [{v:'n',l:'No'},{v:'y',l:'Yes'}], hint: 'Require admin password for write operations' },
    { section: 'Voice' },
    { label: 'Voice Enabled', key: 'voice_enabled', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}],
      hint: 'Off stops this node\'s local mic/speaker and physical PTT button. Browser-based web PTT is unaffected either way.' },
    { label: 'PTT Mode', key: 'voice_ptt_mode', type: 'select', options: [
      {v:'openvlm',l:'OpenVLM HID'},{v:'gpio',l:'GPIO Button'},{v:'always',l:'Always On'},{v:'vox',l:'VOX (auto)'}
    ], voiceKey: 'ptt_mode', fallback: 'openvlm' },
    { label: 'TX Channel', key: 'voice_channel', type: 'select', options: (function() {
      var opts = []; for (var i = 1; i <= 21; i++) opts.push({v: String(i), l: 'Channel ' + i});
      return opts;
    })() },
    { label: 'Mic Volume', key: 'voice_mic_volume', type: 'range', min: 0, max: 100 },
    { label: 'Speaker Volume', key: 'voice_speaker_volume', type: 'range', min: 0, max: 100 },
    { label: 'Port', key: 'voice_port', type: 'text', voiceKey: 'port', fallback: '4370' },
    { label: 'Interface', key: 'voice_iface', type: 'text', voiceKey: 'interface', fallback: 'br0' },
    { label: 'Mic Gain', key: 'voice_gain', type: 'select', options: [
      {v:'1.0',l:'1x (low)'},{v:'2.0',l:'2x'},{v:'3.0',l:'3x (default)'},{v:'4.0',l:'4x'},{v:'5.0',l:'5x (loud)'}
    ] },
    { label: 'TX Beep', key: 'voice_beep_tx_start', type: 'select', options: [{v:'y',l:'On'},{v:'n',l:'Off'}] },
    { label: 'RX End Beep', key: 'voice_beep_rx_end', type: 'select', options: [{v:'y',l:'On'},{v:'n',l:'Off'}] },
  ];

  let html = configRenderUpdateBanner(cfg) +
    (configTarget ? '<div class="cfg-target-label">Editing: ' + escHtml(configTarget) + '</div>' : '') + '<div class="card">';
  fields.forEach(f => {
    if (f.section) {
      html += '</div><div class="card cfg-section"><div class="cfg-section-title">' + f.section + '</div>';
      return;
    }
    const curVal = f.voiceKey ? ((configVoiceData && configVoiceData[f.voiceKey]) || f.fallback || '') : (cfg[f.key] || '');
    html += '<div class="cfg-row" id="cfg-row-' + f.key + '"><div class="cfg-label">' + f.label;
    if (f.hint) html += '<span class="hint">' + f.hint + '</span>';
    html += '</div>';
    if (f.type === 'select') {
      html += '<select class="cfg-input" id="cfg-f-' + f.key + '">';
      f.options.forEach(opt => {
        const val = typeof opt === 'object' ? opt.v : opt;
        const label = typeof opt === 'object' ? opt.l : opt;
        const sel = curVal === val ? ' selected' : '';
        html += '<option value="' + val + '"' + sel + '>' + label + '</option>';
      });
      html += '</select>';
    } else if (f.type === 'range') {
      var rangeVal = curVal || '80';
      html += '<div class="cfg-range-wrap"><input class="cfg-input cfg-range" type="range"' +
        ' min="' + (f.min || 0) + '" max="' + (f.max || 100) + '"' +
        ' id="cfg-f-' + f.key + '" value="' + escHtml(rangeVal) + '"' +
        ' oninput="document.getElementById(\'cfg-rv-' + f.key + '\').textContent=this.value+\'%\'">' +
        '<span class="cfg-range-val" id="cfg-rv-' + f.key + '">' + escHtml(rangeVal) + '%</span></div>';
    } else {
      html += '<input class="cfg-input" type="' + f.type + '" id="cfg-f-' + f.key + '" value="' + escHtml(curVal) + '">';
      if (f.preview) html += '<div class="cfg-hostname-preview" id="cfg-hostname-preview"></div>';
    }
    html += '</div>';
  });
  html += '<div class="cfg-actions">';
  html += '<button class="cfg-btn cfg-btn-primary" id="cfg-save-btn">Save</button>';
  html += '<button class="cfg-btn" id="cfg-back-btn" style="margin-left:auto">Cancel</button>';
  html += '</div></div>';
  panel.innerHTML = html;

  // Dynamic show/hide: a field with `showIf` (an array of conditions,
  // ANDed together — each either {key, equals} for an exact match or
  // {key, notEmpty:true} for "has any value") only shows its row once
  // every referenced control currently satisfies its condition.
  // Re-evaluated whenever any controlling field changes, so e.g. picking
  // GPS Source = Static reveals the latitude/longitude/altitude rows, or
  // picking a CoT Team reveals CoT Role, without a page reload.
  function configApplyShowIf() {
    fields.forEach(f => {
      if (!f.showIf) return;
      const row = document.getElementById('cfg-row-' + f.key);
      if (!row) return;
      const visible = f.showIf.every(cond => {
        const el = document.getElementById('cfg-f-' + cond.key);
        if (!el) return false;
        if (cond.notEmpty) return el.value !== '';
        return el.value === cond.equals;
      });
      row.style.display = visible ? '' : 'none';
    });
  }
  const showIfControllers = new Set();
  fields.forEach(f => { if (f.showIf) f.showIf.forEach(cond => showIfControllers.add(cond.key)); });
  showIfControllers.forEach(key => {
    const el = document.getElementById('cfg-f-' + key);
    if (el) el.addEventListener('change', configApplyShowIf);
  });
  configApplyShowIf();

  // HaLow Bandwidth's own options aren't fully static either — EU's real
  // driver-compiled channel plan only has a 1MHz entry (see the field's
  // hint), so offering 2/4/8MHz there would let the user pick a combination
  // the save rejects outright. Domains not listed here keep the full set.
  const HALOW_BW_OPTIONS = [
    {v:'1MHz',l:'1 MHz'},{v:'2MHz',l:'2 MHz'},{v:'4MHz',l:'4 MHz'},{v:'8MHz',l:'8 MHz'}
  ];
  const HALOW_BW_BY_DOMAIN = { EU: ['1MHz'] };

  // HaLow Channel's option list isn't static like other selects — the legal
  // channels (and what "Auto" actually resolves to) depend on the current
  // Regulatory Domain + HaLow Bandwidth, so it's rebuilt from
  // /api/halow/channels on load and whenever either of those two fields
  // changes.
  async function configRefreshHalowChannels() {
    const chEl = document.getElementById('cfg-f-halow_channel');
    const domainEl = document.getElementById('cfg-f-regulatory_domain');
    const halowRegDomainEl = document.getElementById('cfg-f-halow_regulatory_domain');
    const bwEl = document.getElementById('cfg-f-halow_bw');
    if (!chEl || !domainEl || !bwEl) return;

    // halow_regulatory_domain overrides regulatory_domain when set, mirroring
    // resolveHalowDomain's server-side precedence — this only decides what
    // the picker narrows against client-side; the server still validates
    // for real on save.
    const resolvedDomain = (halowRegDomainEl && halowRegDomainEl.value) ? halowRegDomainEl.value : domainEl.value;

    const allowedBw = HALOW_BW_BY_DOMAIN[resolvedDomain] || HALOW_BW_OPTIONS.map(o => o.v);
    const currentBw = bwEl.value;
    bwEl.innerHTML = HALOW_BW_OPTIONS.filter(o => allowedBw.includes(o.v))
      .map(o => '<option value="' + o.v + '">' + o.l + '</option>').join('');
    bwEl.value = allowedBw.includes(currentBw) ? currentBw : allowedBw[0];

    const current = chEl.value;
    try {
      const url = configBaseUrl() + '/api/halow/channels?domain=' + encodeURIComponent(resolvedDomain) +
        '&bw=' + encodeURIComponent(bwEl.value);
      const r = await fetch(url);
      const d = await r.json();
      let opts = '<option value="">Auto (Channel ' + d.default_channel +
        (d.default_freq_mhz ? ', ' + d.default_freq_mhz + ' MHz' : '') + ')</option>';
      (d.channels || []).forEach(c => {
        opts += '<option value="' + c.channel + '">' + c.channel +
          (c.freq_mhz ? ' (' + c.freq_mhz + ' MHz)' : '') + '</option>';
      });
      chEl.innerHTML = opts;
      // Keep the previously-selected explicit channel if it is still legal
      // for the new domain/bw; otherwise fall back to Auto rather than
      // silently submitting a channel that no longer applies.
      chEl.value = Array.from(chEl.options).some(o => o.value === current) ? current : '';
    } catch (e) {
      // Transient fetch failure — leave whatever options are already
      // rendered rather than wiping the picker.
    }
  }
  const halowDomainEl = document.getElementById('cfg-f-regulatory_domain');
  const halowRegDomainSelectEl = document.getElementById('cfg-f-halow_regulatory_domain');
  const halowBwEl = document.getElementById('cfg-f-halow_bw');
  if (halowDomainEl) halowDomainEl.addEventListener('change', configRefreshHalowChannels);
  if (halowRegDomainSelectEl) halowRegDomainSelectEl.addEventListener('change', configRefreshHalowChannels);
  if (halowBwEl) halowBwEl.addEventListener('change', configRefreshHalowChannels);
  configRefreshHalowChannels();

  document.getElementById('cfg-back-btn').addEventListener('click', () => {
    configEditing = false;
    configFetch();
  });
  document.getElementById('cfg-save-btn').addEventListener('click', configSave);
  configWireUpdateButtons();

  // Live hostname preview
  const hostnameInput = document.getElementById('cfg-f-node_hostname');
  const ssidInput = document.getElementById('cfg-f-mesh_ssid');
  const preview = document.getElementById('cfg-hostname-preview');
  var macSuffix = '????';
  if (configTarget && DATA && DATA.nodes) {
    var node = DATA.nodes.find(function(n) { return n.ip === configTarget; });
    if (node && node.hostname) macSuffix = node.hostname.split('-').pop();
  } else if (LOCAL_DATA && LOCAL_DATA.hostname) {
    macSuffix = LOCAL_DATA.hostname.split('-').pop();
  }
  function updateHostnamePreview() {
    const prefix = hostnameInput.value || '???';
    const ssid = ssidInput.value || '???';
    preview.textContent = prefix + '-' + ssid + '-' + macSuffix;
  }
  hostnameInput.addEventListener('input', updateHostnamePreview);
  ssidInput.addEventListener('input', updateHostnamePreview);
  updateHostnamePreview();
}

async function configSave() {
  const panel = document.getElementById('cfg-content');
  const saveBtn = document.getElementById('cfg-save-btn');
  const backBtn = document.getElementById('cfg-back-btn');
  const savedBtnText = saveBtn.textContent;
  saveBtn.disabled = true;
  saveBtn.textContent = 'Saving…';
  if (backBtn) backBtn.disabled = true;
  panel.querySelectorAll('.cfg-input').forEach(el => { el.disabled = true; });

  function restoreEditable() {
    saveBtn.disabled = false;
    saveBtn.textContent = savedBtnText;
    if (backBtn) backBtn.disabled = false;
    panel.querySelectorAll('.cfg-input').forEach(el => { el.disabled = false; });
  }

  const meshFields = ['node_hostname','eud','lan_ap_ssid','lan_ap_key','lan_ap_channel','lan_ap_bw','max_euds_per_node','eud_bandwidth','mesh_ssid','mesh_key',
    'ipv4_network','regulatory_domain','halow_regulatory_domain','halow_bw','halow_channel','acs','mesh_5ghz_bw','mesh_5ghz_channel','multicast_mode','battery_monitor','admin_password','require_auth',
    'gateway','gateway_nat','gateway_mss_clamp','gateway_bandwidth','dns_servers',
    'auto_update','update_url','auto_update_overlay','auto_update_min_mbps',
    'gps','gps_source','gps_static_lat','gps_static_lon','gps_static_alt','callsign','cot_type','cot_team','cot_role','cot_icon',
    'voice_mic_volume','voice_speaker_volume','voice_channel',
    'voice_beep_tx_start','voice_beep_rx_end','voice_gain','voice_enabled'];
  const config = {};
  meshFields.forEach(f => {
    const el = document.getElementById('cfg-f-' + f);
    if (el) config[f] = el.value;
  });

  var chVal = (document.getElementById('cfg-f-voice_channel') || {}).value || '1';
  var chAddr = '239.69.0.' + chVal;
  const voiceCfg = {
    action: 'configure',
    ptt_mode: (document.getElementById('cfg-f-voice_ptt_mode') || {}).value || 'openvlm',
    mcast_addr: chAddr,
    port: (document.getElementById('cfg-f-voice_port') || {}).value || '4370',
    interface: (document.getElementById('cfg-f-voice_iface') || {}).value || 'br0',
    mic_volume: (document.getElementById('cfg-f-voice_mic_volume') || {}).value || '',
    speaker_volume: (document.getElementById('cfg-f-voice_speaker_volume') || {}).value || '',
  };

  var base = configBaseUrl();
  try {
    const [meshR, voiceR] = await Promise.all([
      authFetch(base + '/api/admin/save', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({config}) }),
      authFetch(base + '/api/voice', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(voiceCfg) })
    ]);
    const meshResult = await meshR.json();
    const voiceResult = await voiceR.json();
    if (meshResult.ok && voiceResult.ok) {
      configEditing = false;
      configFetch();
    } else {
      const errors = [];
      if (!meshResult.ok) errors.push('Mesh: ' + (meshResult.error || 'unknown'));
      if (!voiceResult.ok) errors.push('Voice: ' + (voiceResult.error || 'unknown'));
      notify('Save Failed', errors.join(', '), {type:'error'});
      restoreEditable();
    }
  } catch(e) {
    notify('Save Failed', e.message, {type:'error'});
    restoreEditable();
  }
}

// QOS Priority
var qosData = null;

function qosFetch() {
  var base = configBaseUrl();
  fetch(base + '/api/qos').then(function(r) { return r.json(); }).then(function(d) {
    qosData = d;
    qosRender();
  }).catch(function() {});
}

function qosFmt(n) {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
  return '' + n;
}

function qosRender() {
  var card = document.getElementById('qos-card');
  if (!card || !qosData) return;

  var d = qosData;
  var bandColors = ['var(--good)', 'var(--warn)', 'var(--muted)'];
  var bandNames = d.band_names || ['High', 'Normal', 'Low'];

  var html = '<div class="card-header">QOS PRIORITY</div>';
  html += '<div class="qos-toggle-row">';
  html += '<span class="qos-toggle-label">Traffic Prioritization</span>';
  html += '<button class="qos-toggle' + (d.enabled ? ' on' : '') + '" id="qos-toggle"></button>';
  html += '</div>';

  html += '<div id="qos-body"' + (d.enabled ? '' : ' class="qos-disabled"') + '>';

  for (var b = 0; b < 3; b++) {
    var rules = (d.rules || []).filter(function(r) { return r.band === b; });
    var totalPkts = 0, totalBytes = 0;
    if (d.stats) {
      Object.keys(d.stats).forEach(function(iface) {
        var s = d.stats[iface];
        if (s && s[b]) {
          totalPkts += s[b].packets;
          totalBytes += s[b].bytes;
        }
      });
    }

    html += '<div class="qos-band-section">';
    html += '<div class="qos-band-header">';
    html += '<span class="qos-band-dot" style="background:' + bandColors[b] + '"></span>';
    html += '<span class="qos-band-name">' + bandNames[b] + '</span>';
    html += '<span class="qos-band-stats">' + qosFmt(totalPkts) + ' pkts / ' + qosFmt(totalBytes) + ' bytes</span>';
    html += '</div>';

    if (rules.length) {
      rules.forEach(function(r) {
        html += '<div class="qos-rule-row">';
        html += '<span class="qos-rule-name">' + escHtml(r.name) + '</span>';
        html += '<span class="qos-rule-port">:' + r.port + '</span>';
        html += '<select class="qos-rule-band" data-svc="' + escHtml(r.name) + '">';
        for (var i = 0; i < 3; i++) {
          html += '<option value="' + i + '"' + (r.band === i ? ' selected' : '') + '>' + bandNames[i] + '</option>';
        }
        html += '</select>';
        html += '</div>';
      });
    } else {
      html += '<div style="font-size:11px;color:var(--muted);padding:4px 8px">No assigned services</div>';
    }
    html += '</div>';
  }

  html += '</div>';
  card.innerHTML = html;

  var base = configBaseUrl();
  document.getElementById('qos-toggle').addEventListener('click', function() {
    authFetch(base + '/api/qos', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({enabled: !d.enabled})
    }).then(function() { qosFetch(); });
  });

  card.querySelectorAll('.qos-rule-band').forEach(function(sel) {
    sel.addEventListener('change', function() {
      authFetch(base + '/api/qos', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({service: sel.dataset.svc, band: parseInt(sel.value)})
      }).then(function() { qosFetch(); });
    });
  });
}
