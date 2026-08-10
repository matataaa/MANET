// Dashboard tab: topology + node list sidebar
let dashInitialized = false;

function dashboardActivate() {
  const panel = document.getElementById('tab-dashboard');
  if (!dashInitialized) {
    panel.innerHTML = `
      <div class="topo-panel" id="topo-container">
        <div class="topo-loading" id="topo-loading">LOADING TOPOLOGY...</div>
      </div>
      <div class="dash-side card">
        <div class="card-header">MESH NODES <span id="dash-node-count"></span></div>
        <div id="dash-node-list"></div>
      </div>`;
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
}

function renderDashNodeList(nodes) {
  const el = document.getElementById('dash-node-list');
  if (!el) return;
  const countEl = document.getElementById('dash-node-count');
  if (countEl) countEl.textContent = '(' + nodes.length + ')';

  el.innerHTML = nodes.map(n => {
    const cls = ['node-row', n.is_me ? 'is-me' : '', n.is_gateway ? 'is-gw' : ''].filter(Boolean).join(' ');
    const stale = !n.is_me && DATA.timestamp && (DATA.timestamp - parseInt(n.last_seen || 0)) > 300;

    const badges = [];
    if (n.is_gateway) badges.push('<span class="badge badge-gw">' + (n.is_selected_gw ? '★ GW' : 'GW') + '</span>');
    if (n.is_direct && !n.is_me) badges.push('<span class="badge badge-direct">DIRECT</span>');
    if (n.mumble) badges.push('<span class="badge badge-svc">MUMBLE</span>');
    if (n.mediamtx) badges.push('<span class="badge badge-svc">MTX</span>');
    if (n.ntp) badges.push('<span class="badge badge-svc">NTP</span>');
    if (n.limp) badges.push('<span class="badge badge-limp">LIMP</span>');
    if (n.is_me) badges.push('<span class="self-node-badge">THIS NODE</span>');

    const tqBadge = (n.is_me || stale) ? '' : '<span class="badge ' + tqClass(n.tq) + '">' + tqLabel(n.tq) + '</span>';
    const bar = stale ? '' : '<div class="tq-bar-wrap"><div class="tq-bar" style="width:' + tqPct(n.tq) + '%;background:' + tqColor(n.tq) + '"></div></div>';
    const offline = stale ? '<span class="badge badge-tq-bad" style="opacity:.7">OFFLINE</span> <span style="color:var(--muted);font-size:10px">' + fmtAge(n.last_seen, DATA.timestamp) + '</span>' : '';

    return '<div class="' + cls + '">' +
      '<div class="node-name">' + escHtml(n.hostname) + ' ' + tqBadge + '</div>' +
      '<div class="node-ip">' + escHtml(n.ip || '') + (n.hostname ? ' &middot; ' + escHtml(n.hostname) + '.local' : '') + '</div>' +
      '<div class="node-meta">' + badges.join('') + offline + '</div>' +
      bar +
      '</div>';
  }).join('');
}
