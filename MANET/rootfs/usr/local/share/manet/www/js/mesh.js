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
  var label = entry.hostname ? escHtml(shortHostname(entry.hostname)) : '<span class="mono">' + escHtml(entry.mac) + '</span>';
  if (entry.ip) return '<a href="https://' + encodeURI(entry.ip) + '/" target="_blank" class="node-link">' + label + '</a>';
  return label;
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

// bat0 is a virtual batman-adv routing interface, not a physical link — the
// kernel has no carrier-detect concept for it, so its operstate reports as
// "UNKNOWN" even when the mesh is completely healthy (confirmed: every
// working node in this fleet shows this, never anything else while
// functioning correctly). Showing "Unknown" as-is reads like a problem to
// a user; translate it to what it actually means here.
function bat0StateLabel(state) {
  if (!state) return '--';
  if (state.toUpperCase() === 'UNKNOWN') return 'Active';
  return state;
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
  html += meshKV('Hostname', shortHostname(d.hostname) || '--');
  html += meshKV('State', bat0StateLabel(d.bat0.state));
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

  // Link budget card — per-neighbor RF margin against the current MCS floor
  var lbNeigh = (d.neighbors || []).filter(function(n) { return n.link && n.link.signal < 0; });
  if (lbNeigh.length) {
    html += '<div class="card mesh-table-card mesh-wide">';
    html += '<div class="card-header">LINK BUDGET' + (d.halow_bw ? ' — ' + escHtml(d.halow_bw) + ' CHANNEL' : '') + '</div>';
    html += '<table class="mesh-table"><thead><tr><th>Node</th><th>Signal</th><th>MCS</th><th>PHY Rate</th><th>Real Rate</th><th>Floor</th><th>Margin</th><th>Retry %</th></tr></thead><tbody>';
    lbNeigh.forEach(function(n) {
      var L = n.link;
      var marginHtml = '<span class="badge badge-tq-none">n/a</span>';
      if (L.margin != null) {
        var cls = L.margin >= 15 ? 'badge-tq-great' : L.margin >= 9 ? 'badge-tq-ok' : L.margin >= 4 ? 'badge-tq-warn' : 'badge-tq-bad';
        marginHtml = '<span class="badge ' + cls + '">' + Math.round(L.margin) + ' dB</span>';
      }
      var retryHtml = '--';
      if (L.retry_pct != null) {
        var rc = L.retry_pct < 10 ? 'var(--good)' : L.retry_pct < 30 ? 'var(--warn)' : 'var(--bad)';
        retryHtml = '<span style="color:' + rc + '">' + L.retry_pct.toFixed(0) + '%</span>';
      }
      var radioTag = n.iface ? ' <span class="badge badge-svc">' + escHtml(radioLabel(n.iface)) + '</span>' : '';
      html += '<tr><td>' + meshNodeLabel(n) + radioTag + meshNodeSub(n) + '</td>';
      html += '<td>' + L.signal + ' dBm</td>';
      html += '<td>' + (L.mcs >= 0 ? 'MCS ' + L.mcs : '--') + '</td>';
      html += '<td>' + (L.phy_mbps ? L.phy_mbps.toFixed(1) + ' Mbps' : '--') + '</td>';
      html += '<td>' + (L.expected_mbps ? L.expected_mbps.toFixed(1) + ' Mbps' : '--') + '</td>';
      html += '<td>' + (L.floor != null ? L.floor + ' dBm' : '--') + '</td>';
      html += '<td>' + marginHtml + '</td>';
      html += '<td>' + retryHtml + '</td></tr>';
    });
    html += '</tbody></table>';
    html += '<div style="padding:8px 12px;color:var(--muted);font-size:11px">Margin is signal headroom above the decode floor of the current MCS — every −6 dB roughly halves usable distance. Rate adaptation trades MCS (speed) for floor (reach) as margin shrinks; a narrower channel lowers the floor (~+3 dB per halving).</div>';
    html += '</div>';
  }

  // Neighbors table
  html += '<div class="card mesh-table-card">';
  html += '<div class="card-header">DIRECT NEIGHBORS (' + nCount + ')</div>';
  if (d.neighbors && d.neighbors.length) {
    html += '<table class="mesh-table"><thead><tr><th>Node</th><th>Radio</th><th>TQ</th><th>Last Seen</th></tr></thead><tbody>';
    d.neighbors.forEach(function(n) {
      html += '<tr><td>' + meshNodeLabel(n) + meshNodeSub(n) + '</td>';
      html += '<td>' + escHtml(radioLabel(n.iface) || '') + '</td>';
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
      html += '<td>' + (gw.tq ? '<span class="badge ' + tqClass(gw.tq) + '">' + gw.tq + '</span>' : '--') + '</td>';
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
    html += '<table class="mesh-table"><thead><tr><th>Node</th><th>TQ</th><th>Via</th><th>Radio</th><th>Last Seen</th></tr></thead><tbody>';
    d.originators.forEach(function(o) {
      var via = o.nexthop_hostname ? escHtml(shortHostname(o.nexthop_hostname)) : '<span class="mono">' + escHtml(o.nexthop) + '</span>';
      if (o.nexthop === o.mac) via = '<span style="color:var(--muted)">direct</span>';
      html += '<tr><td>' + meshNodeLabel(o) + meshNodeSub(o) + '</td>';
      html += '<td><span class="badge ' + tqClass(o.tq) + '">' + o.tq + '</span></td>';
      html += '<td>' + via + '</td>';
      html += '<td>' + escHtml(radioLabel(o.iface)) + '</td>';
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
      html += '<td class="mono">' + (r.ip ? '<a href="https://' + encodeURI(r.ip) + '/" target="_blank" class="node-link">' + escHtml(r.name) + '</a>' : escHtml(r.name)) + '</td>';
      html += '<td class="mono">' + (r.ip ? '<a href="https://' + encodeURI(r.ip) + '/" target="_blank" class="node-link">' + escHtml(r.ip) + '</a>' : escHtml(r.ip)) + '</td>';
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

  // Connected EUDs
  var euds = d.euds || [];
  html += '<div class="card mesh-table-card mesh-wide">';
  html += '<div class="card-header">CONNECTED DEVICES (' + euds.length + ')</div>';
  if (euds.length) {
    html += '<table class="mesh-table"><thead><tr><th>Hostname</th><th>IP</th><th>MAC</th><th>Lease</th></tr></thead><tbody>';
    euds.forEach(function(e) {
      var lease = e.expires_in != null ? Math.floor(e.expires_in / 60) + 'm' : 'static';
      html += '<tr>';
      html += '<td>' + escHtml(e.hostname || '*') + '</td>';
      html += '<td class="mono">' + escHtml(e.ip) + '</td>';
      html += '<td class="mono" style="color:var(--muted)">' + escHtml(e.mac) + '</td>';
      html += '<td>' + lease + '</td>';
      html += '</tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No EUD devices connected</div>';
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

