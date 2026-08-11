let meshInitialized = false;
let meshData = null;

function meshActivate() {
  const panel = document.getElementById('tab-mesh');
  if (!meshInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading mesh info...</div>';
    meshInitialized = true;
  }
  meshFetch();
}

async function meshFetch() {
  try {
    const r = await fetch('/api/mesh');
    meshData = await r.json();
    meshRender();
  } catch(e) {
    document.getElementById('tab-mesh').innerHTML = '<div class="loading-msg">Failed to load mesh data</div>';
  }
}

function meshNodeLabel(entry) {
  if (entry.hostname) return escHtml(entry.hostname);
  return '<span class="mono">' + escHtml(entry.mac) + '</span>';
}

function meshNodeSub(entry) {
  var parts = [];
  if (entry.hostname && entry.mac) parts.push(entry.mac);
  if (entry.ip) parts.push(entry.ip);
  return parts.length ? '<div class="mesh-node-sub">' + escHtml(parts.join(' / ')) + '</div>' : '';
}

function meshLastSeen(entry) {
  if (!entry.last_seen) return '';
  var ts = parseInt(entry.last_seen, 10);
  if (!ts) return '';
  var ago = Math.floor(Date.now() / 1000) - ts;
  if (ago < 0) ago = 0;
  var label;
  if (ago < 60) label = ago + 's ago';
  else if (ago < 3600) label = Math.floor(ago / 60) + 'm ago';
  else label = Math.floor(ago / 3600) + 'h ago';
  var cls = ago < 30 ? 'mesh-seen-ok' : ago < 120 ? 'mesh-seen-warn' : 'mesh-seen-stale';
  return '<span class="' + cls + '">' + label + '</span>';
}

function meshRender() {
  if (!meshData) return;
  const panel = document.getElementById('tab-mesh');
  const d = meshData;

  let html = '<div class="mesh-grid">';

  // bat0 overview card
  html += '<div class="card mesh-overview">';
  html += '<div class="card-header">LOCAL NODE</div>';
  html += '<div class="mesh-kv">';
  html += meshKV('Hostname', d.hostname || '--');
  html += meshKV('State', d.bat0.state || '--');
  html += meshKV('Address', (d.bat0.addrs || []).join(', ') || '--');
  html += meshKV('Algorithm', d.bat0.algo || '--');
  html += meshKV('Gateway Mode', d.bat0.gw_mode || '--');
  html += meshKV('Mesh SSID', d.mesh_ssid || '--');
  html += meshKV('Network', d.network || '--');
  html += '</div></div>';

  // Stats card
  html += '<div class="card mesh-overview">';
  html += '<div class="card-header">MESH HEALTH</div>';
  html += '<div class="mesh-kv">';
  var nCount = d.neighbor_count || 0;
  var oCount = d.originator_count || 0;
  var gCount = d.gateway_count || 0;
  var selGW = (d.gateways || []).find(function(g) { return g.selected; });
  html += meshKV('Direct Neighbors', nCount > 0 ? '<span style="color:var(--good)">' + nCount + '</span>' : '<span style="color:var(--bad)">0</span>');
  html += meshKV('Reachable Nodes', oCount);
  html += meshKV('Gateways Available', gCount > 0 ? '<span style="color:var(--good)">' + gCount + '</span>' : '<span style="color:var(--warn)">0</span>');
  html += meshKV('Selected Gateway', selGW ? meshNodeLabel(selGW) : '<span style="color:var(--muted)">None</span>');
  html += '</div></div>';

  // Neighbors table
  html += '<div class="card mesh-table-card">';
  html += '<div class="card-header">DIRECT NEIGHBORS (' + nCount + ')</div>';
  if (d.neighbors && d.neighbors.length) {
    html += '<table class="mesh-table"><thead><tr><th>Node</th><th>Interface</th><th>TQ</th><th>Last Seen</th></tr></thead><tbody>';
    d.neighbors.forEach(function(n) {
      html += '<tr><td>' + meshNodeLabel(n) + meshNodeSub(n) + '</td>';
      html += '<td>' + escHtml(n.iface || '') + '</td>';
      html += '<td><span class="badge ' + tqClass(n.tq) + '">' + (n.tq != null ? n.tq : '?') + '</span></td>';
      html += '<td>' + meshLastSeen(n) + '</td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No direct neighbors — this node is isolated</div>';
  }
  html += '</div>';

  // Gateways table
  html += '<div class="card mesh-table-card">';
  html += '<div class="card-header">GATEWAYS (' + gCount + ')</div>';
  if (d.gateways && d.gateways.length) {
    html += '<table class="mesh-table"><thead><tr><th>Node</th><th>TQ</th><th>Status</th></tr></thead><tbody>';
    d.gateways.forEach(function(gw) {
      var sel = gw.selected ? '<span style="color:var(--teal);font-weight:700">Active</span>' : '<span style="color:var(--muted)">Standby</span>';
      html += '<tr><td>' + meshNodeLabel(gw) + meshNodeSub(gw) + '</td>';
      html += '<td><span class="badge ' + tqClass(gw.tq) + '">' + (gw.tq != null ? gw.tq : '?') + '</span></td>';
      html += '<td>' + sel + '</td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No gateways — no internet uplink available</div>';
  }
  html += '</div>';

  // Originators table
  html += '<div class="card mesh-table-card mesh-wide">';
  html += '<div class="card-header">REACHABLE NODES (' + oCount + ')</div>';
  if (d.originators && d.originators.length) {
    html += '<table class="mesh-table"><thead><tr><th>Node</th><th>TQ</th><th>Via</th><th>Interface</th><th>Last Seen</th></tr></thead><tbody>';
    d.originators.forEach(function(o) {
      var via = o.nexthop_hostname ? escHtml(o.nexthop_hostname) : '<span class="mono">' + escHtml(o.nexthop) + '</span>';
      if (o.nexthop === o.mac) via = '<span style="color:var(--muted)">direct</span>';
      html += '<tr><td>' + meshNodeLabel(o) + meshNodeSub(o) + '</td>';
      html += '<td><span class="badge ' + tqClass(o.tq) + '">' + o.tq + '</span></td>';
      html += '<td>' + via + '</td>';
      html += '<td>' + escHtml(o.iface) + '</td>';
      html += '<td>' + meshLastSeen(o) + '</td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No reachable nodes</div>';
  }
  html += '</div>';

  // DNS Records
  var dns = d.dns_records || [];
  html += '<div class="card mesh-table-card mesh-wide">';
  html += '<div class="card-header">DNS RECORDS (' + dns.length + ')</div>';
  if (dns.length) {
    html += '<table class="mesh-table"><thead><tr><th>Name</th><th>Resolves To</th><th>Type</th><th>Source</th><th>Status</th></tr></thead><tbody>';
    dns.forEach(function(r) {
      var statusCls = r.stale ? 'mesh-seen-stale' : 'mesh-seen-ok';
      var statusLabel = r.stale ? 'STALE' : 'Active';
      html += '<tr' + (r.stale ? ' style="opacity:.5"' : '') + '>';
      html += '<td class="mono">' + escHtml(r.name) + '</td>';
      html += '<td class="mono">' + escHtml(r.ip) + '</td>';
      html += '<td><span class="badge badge-' + (r.type === 'service' ? 'applet-on' : r.type === 'global' ? 'tq-high' : 'tq-mid') + '">' + escHtml(r.type) + '</span></td>';
      html += '<td>' + escHtml(r.source) + '</td>';
      html += '<td><span class="' + statusCls + '">' + statusLabel + '</span></td>';
      html += '</tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No DNS records</div>';
  }
  html += '</div>';

  html += '</div>';

  html += '<div style="padding:8px 0"><button class="cfg-btn" id="mesh-refresh">Refresh</button></div>';
  panel.innerHTML = html;
  document.getElementById('mesh-refresh').addEventListener('click', meshFetch);
}

function meshKV(label, value) {
  return '<div class="mesh-kv-row"><span class="mesh-kv-label">' + label + '</span><span class="mesh-kv-value">' + value + '</span></div>';
}

