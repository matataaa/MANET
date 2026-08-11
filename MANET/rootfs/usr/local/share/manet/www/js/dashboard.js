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
        <div class="card">
          <div class="card-header">MESH NODES <span id="dash-node-count"></span></div>
          <div id="dash-node-list"></div>
        </div>
        <div id="dash-daemons-wrap"></div>
        <div id="dash-applets-wrap"></div>
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
  renderDashDaemons();
  renderDashApplets();
}

function renderDashDaemons() {
  var wrap = document.getElementById('dash-daemons-wrap');
  if (!wrap) return;

  fetch('/api/daemons').then(function(r) { return r.json(); }).then(function(d) {
    var items = [];

    var g = d.gps;
    if (g && g.available) {
      var gpsVal = g.has_fix
        ? Number(g.latitude).toFixed(4) + ', ' + Number(g.longitude).toFixed(4)
        : 'No Fix';
      var gpsDot = g.has_fix ? 'on' : 'off';
      items.push('<div class="dash-daemon-row"><span class="voice-dot ' + gpsDot + '"></span>' +
        '<span class="dash-daemon-name">GPS</span>' +
        '<span class="dash-daemon-val">' + gpsVal + '</span></div>');
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
      var cotVal = c.last_error === 'no GPS fix' ? 'Waiting for GPS' : c.unicast_targets + ' EUDs &middot; ' + c.total_sent + ' sent';
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
    if (n.mumble) badges.push('<span class="badge badge-svc">MUMBLE</span>');
    if (n.mediamtx) badges.push('<span class="badge badge-svc">MTX</span>');
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

    return '<div class="' + cls + '">' +
      '<div class="node-name">' + escHtml(n.hostname || n.mac) + ' ' + tqBadge + '</div>' +
      '<div class="node-ip">' + ipLine + (uptimeLine && ipLine ? ' &middot; ' : '') + uptimeLine + '</div>' +
      '<div class="node-meta">' + badges.join('') + offline + '</div>' +
      bar +
      '</div>';
  }).join('');
}
