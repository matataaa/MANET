// Dashboard tab: topology + node list sidebar
let dashInitialized = false;

function dashboardActivate() {
  const panel = document.getElementById('tab-dashboard');
  if (!dashInitialized) {
    panel.innerHTML = `
      <div class="topo-panel" id="topo-container">
        <div class="topo-loading" id="topo-loading">LOADING TOPOLOGY...</div>
      </div>
      <div class="dash-side">
        <div id="dash-network-wrap"></div>
        <div id="dash-airtime-wrap"></div>
        <div class="card">
          <div class="card-header">MESH NODES <span id="dash-node-count"></span></div>
          <div id="dash-node-list"></div>
        </div>
        <div id="dash-daemons-wrap"></div>
        <div id="dash-applets-wrap"></div>
        <div class="card" style="margin-top:8px">
          <div class="card-header">ANDROID APP</div>
          <div style="padding:10px 14px">
            <a href="/assets/mesh-ctrl.apk" download="mesh-ctrl.apk" style="display:inline-flex;align-items:center;gap:8px;color:var(--accent);text-decoration:none;font-size:13px;font-weight:600">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
              Download APK
            </a>
            <div style="color:var(--muted);font-size:11px;margin-top:4px">MANET//CTRL with EUD mode</div>
          </div>
        </div>
      </div>
`;
    topoInit(document.getElementById('topo-container'));
    dashInitialized = true;
  }
  if (DATA) dashboardUpdate();
}

function dashboardUpdate() {
  if (!DATA || !DATA.nodes || !dashInitialized) return;
  var el = document.getElementById('topo-loading');
  if (el) el.style.display = 'none';
  topoUpdate(DATA);
  renderDashNodeList(DATA.nodes);
  renderThrottleWarning();
  renderDashNetwork();
  renderDashAirtime();
  renderDashDaemons();
  renderDashApplets();
  renderFleetBubble();
}

function fmtBps(bps) {
  if (bps == null || bps < 1) return '0';
  if (bps < 1000) return Math.round(bps) + ' bps';
  if (bps < 1000000) return (bps / 1000).toFixed(1) + ' kbps';
  return (bps / 1000000).toFixed(2) + ' Mbps';
}

function renderDashAirtime() {
  var wrap = document.getElementById('dash-airtime-wrap');
  if (!wrap || !LOCAL_DATA || !LOCAL_DATA.airtime) { if (wrap) wrap.innerHTML = ''; return; }
  var a = LOCAL_DATA.airtime;
  var capBps = (a.capacity_mbps || 0) * 1000000;
  var totalBps = (a.total_tx_bps || 0) + (a.total_rx_bps || 0);
  var pct = capBps > 0 ? Math.min(100, Math.round(totalBps / capBps * 100)) : 0;
  var pctColor = pct < 50 ? 'var(--good)' : pct < 80 ? 'var(--warn)' : 'var(--bad)';

  var html = '<div class="card"><div class="card-header">AIRTIME';
  if (a.capacity_mbps) html += ' <span style="color:var(--muted);font-weight:400">' + pct + '% of ' + a.capacity_mbps + ' Mbps</span>';
  html += '</div><div style="padding:8px 14px 10px">';
  html += '<div class="topo-fleet-ack-bar" style="margin-bottom:8px"><div class="topo-fleet-ack-fill" style="width:' + pct + '%;background:' + pctColor + '"></div></div>';
  html += '<div style="display:flex;justify-content:space-between;font-size:11px;color:var(--muted);margin-bottom:6px">';
  html += '<span>' + escHtml(a.mesh_iface || '') + ' air total</span><span>&uarr; ' + fmtBps(a.total_tx_bps) + ' &nbsp; &darr; ' + fmtBps(a.total_rx_bps) + '</span></div>';
  if (a.wifi_ifaces && a.wifi_ifaces.length) {
    html += '<div style="display:flex;justify-content:space-between;font-size:11px;color:var(--muted);margin-bottom:6px">';
    html += '<span>' + escHtml(a.wifi_ifaces.join('+')) + ' air total</span><span>&uarr; ' + fmtBps(a.wifi_tx_bps) + ' &nbsp; &darr; ' + fmtBps(a.wifi_rx_bps) + '</span></div>';
  }
  if (a.services && a.services.length) {
    a.services.forEach(function(s) {
      var active = (s.tx_bps + s.rx_bps) >= 1;
      html += '<div style="display:flex;justify-content:space-between;font-size:12px;padding:2px 0' + (active ? '' : ';opacity:.45') + '">';
      html += '<span>' + escHtml(s.name) + '</span>';
      html += '<span class="mono" style="font-size:11px">&uarr; ' + fmtBps(s.tx_bps) + ' &nbsp; &darr; ' + fmtBps(s.rx_bps) + '</span></div>';
    });
  } else if (!a.counters_ok) {
    html += '<div style="font-size:11px;color:var(--muted)">Per-service counters unavailable (nft)</div>';
  }
  html += '</div></div>';
  wrap.innerHTML = html;
}

function renderDashNetwork() {
  var wrap = document.getElementById('dash-network-wrap');
  if (!wrap || !LOCAL_DATA || !LOCAL_DATA.network) { if (wrap) wrap.innerHTML = ''; return; }
  var n = LOCAL_DATA.network;
  var rows = [];

  // Gateway / Uplink
  var gwDot = n.gateway ? 'on' : 'off';
  var gwVal = n.gateway ? 'Active' : 'Off';
  if (n.gateway && n.gateway_ip) gwVal += ' &middot; ' + escHtml(n.gateway_ip);
  if (n.gateway && n.upstream_iface) gwVal += ' (' + escHtml(n.upstream_iface) + ')';
  rows.push('<div class="dash-daemon-row"><span class="voice-dot ' + gwDot + '"></span>' +
    '<span class="dash-daemon-name">Uplink</span>' +
    '<span class="dash-daemon-val">' + gwVal + '</span></div>');

  // EUD
  var eudDot = n.eud_active ? 'on' : 'off';
  var eudVal = n.eud_mode || 'wired';
  if (n.eud_active) {
    var via = n.eud_iface === 'wifi' ? 'WiFi AP' : (n.eud_iface || 'bridge');
    eudVal += ' &middot; ' + via;
  } else {
    eudVal += ' &middot; no clients';
  }
  rows.push('<div class="dash-daemon-row"><span class="voice-dot ' + eudDot + '"></span>' +
    '<span class="dash-daemon-name">EUD</span>' +
    '<span class="dash-daemon-val">' + eudVal + '</span></div>');

  // AP
  var apDot = n.ap_active ? 'on' : 'off';
  var apVal = n.ap_active ? 'Active' : 'Off';
  if (n.ap_active && LOCAL_DATA.ap_ssid) apVal += ' &middot; ' + escHtml(LOCAL_DATA.ap_ssid);
  rows.push('<div class="dash-daemon-row"><span class="voice-dot ' + apDot + '"></span>' +
    '<span class="dash-daemon-name">AP</span>' +
    '<span class="dash-daemon-val">' + apVal + '</span></div>');

  // USB Tether
  var usbDot = n.usb_tether ? 'on' : 'off';
  var usbVal = n.usb_tether ? 'Connected (' + escHtml(n.usb_iface || 'usb0') + ')' : 'None';
  rows.push('<div class="dash-daemon-row"><span class="voice-dot ' + usbDot + '"></span>' +
    '<span class="dash-daemon-name">USB</span>' +
    '<span class="dash-daemon-val">' + usbVal + '</span></div>');

  // NTP
  if (n.ntp) {
    rows.push('<div class="dash-daemon-row"><span class="voice-dot on"></span>' +
      '<span class="dash-daemon-name">NTP</span>' +
      '<span class="dash-daemon-val">Serving</span></div>');
  }

  wrap.innerHTML = '<div class="card"><div class="card-header">NETWORK</div>' + rows.join('') + '</div>';
}

function gpsSourceTag(source) {
  if (source === 'static') return '<span class="dash-daemon-tag" title="Fixed position from mesh.conf (gps_source=static)">ST</span>';
  if (source === 'receiver') return '<span class="dash-daemon-tag" title="Live hardware GPS receiver via gpsd">RC</span>';
  return '';
}

function renderDashDaemons() {
  var wrap = document.getElementById('dash-daemons-wrap');
  if (!wrap) return;

  fetch('/api/daemons').then(function(r) { return r.json(); }).then(function(d) {
    var items = [];

    var g = d.gps;
    var gpsSrc = gpsSourceTag(g && g.source);
    if (g && g.available) {
      var gpsVal = g.has_fix
        ? Number(g.latitude).toFixed(4) + ', ' + Number(g.longitude).toFixed(4)
        : 'No Fix';
      var gpsDot = g.has_fix ? 'on' : 'off';
      items.push('<div class="dash-daemon-row"><span class="voice-dot ' + gpsDot + '"></span>' +
        '<span class="dash-daemon-name">GPS</span>' +
        '<span class="dash-daemon-val">' + gpsVal + gpsSrc + '</span></div>');
    }

    var b = d.battery;
    if (b && b.available && b.status !== 'unknown') {
      var pct = b.percentage != null ? b.percentage + '%' : '--';
      var bDot = b.charging ? 'on' : (b.percentage != null && b.percentage < 20 ? 'off' : 'on');
      var bExtra = b.voltage_v != null ? ' &middot; ' + Number(b.voltage_v).toFixed(2) + 'V' : '';
      items.push('<div class="dash-daemon-row"><span class="voice-dot ' + bDot + '"></span>' +
        '<span class="dash-daemon-name">Battery</span>' +
        '<span class="dash-daemon-val">' + pct + bExtra + '</span></div>');
    }

    var c = d.cot_emitter;
    if (c && c.available && c.running) {
      var gpsPart = c.gps_fix ? 'GPS OK' + gpsSrc : 'No GPS';
      var eudPart = c.unicast_targets + ' EUD' + (c.unicast_targets !== 1 ? 's' : '');
      var relayPart = c.relay_received > 0 ? ' &middot; ' + c.relay_forwarded + ' relayed' : '';
      var sentPart = c.total_sent > 0 ? ' &middot; ' + c.total_sent + ' sent' : '';
      var cotVal = gpsPart + ' &middot; ' + eudPart + relayPart + sentPart;
      var cDot = c.last_error && c.last_error !== 'no GPS fix' ? 'off' : 'on';
      items.push('<div class="dash-daemon-row"><span class="voice-dot ' + cDot + '"></span>' +
        '<span class="dash-daemon-name">CoT</span>' +
        '<span class="dash-daemon-val">' + cotVal + '</span></div>');
    }

    if (!items.length) { wrap.innerHTML = ''; return; }
    wrap.innerHTML = '<div class="card"><div class="card-header">DAEMONS</div>' + items.join('') + '</div>';
  }).catch(function() {});
}

function renderDashApplets() {
  var wrap = document.getElementById('dash-applets-wrap');
  if (!wrap || wrap.innerHTML) return;
  fetch('/api/applets').then(function(r) { return r.json(); }).then(function(d) {
    var applets = (d.applets || []).filter(function(a) { return a.has_frontend; });
    if (!applets.length) return;
    wrap.innerHTML = '<div class="card">' +
      '<div class="card-header">APPLETS</div>' +
      applets.map(function(a) {
        var dot = a.status === 'running' ? 'on' : 'off';
        return '<div class="dash-applet-row" onclick="openApplet(\'' + escHtml(a.name) + '\')" data-applet-badge="' + escHtml(a.name) + '">' +
          '<span class="voice-dot ' + dot + '"></span>' +
          '<span class="dash-applet-name">' + escHtml(a.label || a.name) + '</span>' +
          '<span class="dash-applet-launch">Open</span>' +
        '</div>';
      }).join('') + '</div>';
    pollAppletBadges();
  }).catch(function() {});
}

function renderThrottleWarning() {
  var existing = document.getElementById('dash-throttle');
  if (existing) existing.remove();
  if (!LOCAL_DATA || !LOCAL_DATA.throttle) return;
  var t = LOCAL_DATA.throttle;
  var warnings = [];
  if (t.undervoltage) warnings.push('UNDERVOLTAGE DETECTED — power supply insufficient');
  else if (t.was_undervoltage) warnings.push('Undervoltage occurred since boot — check power supply');
  if (t.throttled) warnings.push('CPU THROTTLED — reduce load or improve cooling/power');
  else if (t.was_throttled) warnings.push('CPU was throttled since boot');
  if (t.freq_capped) warnings.push('CPU frequency capped — thermal or power limit');
  else if (t.was_freq_capped) warnings.push('CPU frequency was capped since boot');
  if (t.soft_temp_limit) warnings.push('Soft temperature limit active');
  else if (t.was_soft_temp_limit) warnings.push('Soft temperature limit occurred since boot');
  if (!warnings.length) return;
  var active = t.undervoltage || t.throttled || t.freq_capped || t.soft_temp_limit;
  var cls = active ? 'throttle-banner throttle-active' : 'throttle-banner throttle-past';
  var panel = document.getElementById('tab-dashboard');
  var banner = document.createElement('div');
  banner.id = 'dash-throttle';
  banner.className = cls;
  banner.innerHTML = warnings.map(function(w) { return '<div>' + escHtml(w) + '</div>'; }).join('');
  panel.insertBefore(banner, panel.firstChild);
}

function renderFleetBubble() {
  var container = document.getElementById('topo-container');
  if (!container) return;
  var existing = container.querySelector('.topo-fleet-bubble');

  fetch('/api/admin/status').then(function(r) { return r.json(); }).then(function(d) {
    var pending = d.pending ? (typeof d.pending === 'string' ? JSON.parse(d.pending) : d.pending) : null;
    if (!pending) {
      if (existing) existing.remove();
      return;
    }

    var config = pending.config || {};
    var current = d.current_config || {};
    var diffs = [];
    var sensitive = ['admin_password', 'mesh_key', 'lan_ap_key'];
    for (var k in config) {
      if (config[k] !== current[k]) {
        diffs.push({ key: k, val: sensitive.indexOf(k) !== -1 ? '******' : config[k] });
      }
    }
    if (!diffs.length) {
      if (existing) existing.remove();
      return;
    }

    var nodes = d.nodes || [];
    var version = pending.version || '';
    var acked = nodes.filter(function(n) { return n.ack === version; }).length;
    var total = nodes.length;
    var pct = total > 0 ? Math.round(acked / total * 100) : 0;
    var activating = !!pending.activate_at;

    var html = '<div class="topo-fleet-title">';
    html += '<span class="voice-dot ' + (activating ? 'on' : 'off') + '"></span>';
    html += activating ? 'ACTIVATING' : 'FLEET CONFIG STAGED';
    html += '</div>';
    html += '<div class="topo-fleet-diff">';
    diffs.slice(0, 4).forEach(function(d) {
      html += '<div class="topo-fleet-diff-row">';
      html += '<span class="topo-fleet-diff-key">' + escHtml(d.key) + '</span>';
      html += '<span class="topo-fleet-diff-val">' + escHtml(d.val) + '</span>';
      html += '</div>';
    });
    if (diffs.length > 4) html += '<div style="color:var(--muted)">+' + (diffs.length - 4) + ' more</div>';
    html += '</div>';
    html += '<div class="topo-fleet-ack">' + acked + '/' + total;
    html += '<div class="topo-fleet-ack-bar"><div class="topo-fleet-ack-fill" style="width:' + pct + '%"></div></div>';
    html += '</div>';

    if (existing) {
      existing.innerHTML = html;
    } else {
      var bubble = document.createElement('div');
      bubble.className = 'topo-fleet-bubble';
      bubble.innerHTML = html;
      bubble.addEventListener('click', function() { window.location.hash = 'fleet'; });
      container.appendChild(bubble);
    }
  }).catch(function() {
    if (existing) existing.remove();
  });
}

function renderDashNodeList(nodes) {
  const el = document.getElementById('dash-node-list');
  if (!el) return;
  const countEl = document.getElementById('dash-node-count');
  if (countEl) countEl.textContent = '(' + nodes.length + ')';

  el.innerHTML = nodes.map(n => {
    const cls = ['node-row', n.is_me ? 'is-me' : '', n.is_gateway ? 'is-gw' : ''].filter(Boolean).join(' ');
    const stale = !n.is_me && n.last_seen && DATA.timestamp && (DATA.timestamp - parseInt(n.last_seen)) > 300;

    const badges = [];
    if (n.is_gateway) badges.push('<span class="badge badge-gw">' + (n.is_selected_gw ? '★ GW' : 'GW') + '</span>');
    if (n.is_direct && !n.is_me) badges.push('<span class="badge badge-direct">DIRECT</span>');
    if (n.hop_count && n.hop_count > 1) badges.push('<span class="badge badge-hop">' + n.hop_count + ' hops</span>');
    if (n.ntp) badges.push('<span class="badge badge-svc">NTP</span>');
    if (n.applets && n.applets.length) {
      n.applets.forEach(function(a) {
        var cls = a.status === 'running' ? 'badge-applet-on' : 'badge-applet-off';
        badges.push('<span class="badge ' + cls + '">' + escHtml(a.label || a.name) + '</span>');
      });
    }
    if (n.limp) badges.push('<span class="badge badge-limp">LIMP</span>');
    if (n.is_me) badges.push('<span class="self-node-badge">THIS NODE</span>');

    const tqBadge = (n.is_me || stale) ? '' : '<span class="badge ' + tqClass(n.tq) + '">' + tqLabel(n.tq) + '</span>';
    const bar = (n.is_me || stale) ? '' : '<div class="tq-bar-wrap"><div class="tq-bar" style="width:' + tqPct(n.tq) + '%;background:' + tqColor(n.tq) + '"></div></div>';
    const offline = stale ? '<span class="badge badge-tq-bad" style="opacity:.7">OFFLINE</span> <span style="color:var(--muted);font-size:10px">' + fmtAge(n.last_seen, DATA.timestamp) + '</span>' : '';

    const ipLine = n.ip ? '<span class="node-ip-addr">' + escHtml(n.ip) + '</span>' : '';
    const uptimeLine = n.uptime ? '<span class="node-uptime">' + escHtml(n.uptime) + '</span>' : '';

    const delBtn = (!n.is_me && n.id) ? '<button class="node-del-btn" data-node-id="' + escHtml(n.id) + '" title="Remove from registry">&times;</button>' : '';

    const shortName = shortHostname(n.hostname) || n.mac;

    return '<div class="' + cls + '">' +
      '<div class="node-name">' + (n.ip ? '<a href="https://' + encodeURI(n.ip) + '/" target="_blank" class="node-link">' + escHtml(shortName) + '</a>' : escHtml(shortName)) + ' ' + tqBadge + delBtn + '</div>' +
      '<div class="node-ip">' + ipLine + (uptimeLine && ipLine ? ' &middot; ' : '') + uptimeLine + '</div>' +
      '<div class="node-meta">' + badges.join('') + offline + '</div>' +
      bar +
      '</div>';
  }).join('');

  el.querySelectorAll('.node-del-btn').forEach(function(btn) {
    btn.addEventListener('click', function(e) {
      e.stopPropagation();
      var id = this.dataset.nodeId;
      var row = this.closest('.node-row');
      var name = row ? row.querySelector('.node-name').textContent.trim() : id;
      if (!confirm('Remove ' + name + ' from registry?\nIt will reappear if still active on the mesh.')) return;
      fetch('/api/admin/delete-node', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: id})
      }).then(function(r) { return r.json(); }).then(function(d) {
        if (d.ok && row) row.remove();
      });
    });
  });
}
