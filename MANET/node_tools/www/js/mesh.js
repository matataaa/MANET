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

function meshRender() {
  if (!meshData) return;
  const panel = document.getElementById('tab-mesh');
  const d = meshData;

  let html = '<div class="mesh-grid">';

  // bat0 overview card
  html += '<div class="card mesh-overview">';
  html += '<div class="card-header">BAT0 INTERFACE</div>';
  html += '<div class="mesh-kv">';
  html += meshKV('State', d.bat0.state || '--');
  html += meshKV('Address', (d.bat0.addrs || []).join(', ') || '--');
  html += meshKV('Algorithm', d.bat0.algo || '--');
  html += meshKV('Gateway Mode', d.bat0.gw_mode || '--');
  html += meshKV('Mesh SSID', d.mesh_ssid || '--');
  html += meshKV('Network', d.network || '--');
  html += '</div></div>';

  // Stats card
  html += '<div class="card mesh-overview">';
  html += '<div class="card-header">MESH STATS</div>';
  html += '<div class="mesh-kv">';
  html += meshKV('Originators', d.originator_count);
  html += meshKV('Direct Neighbors', d.neighbor_count);
  html += meshKV('Gateways', d.gateway_count);
  html += '</div></div>';

  // Neighbors table
  html += '<div class="card mesh-table-card">';
  html += '<div class="card-header">DIRECT NEIGHBORS (' + d.neighbor_count + ')</div>';
  if (d.neighbors.length) {
    html += '<table class="mesh-table"><thead><tr><th>Interface</th><th>MAC</th><th>TQ</th></tr></thead><tbody>';
    d.neighbors.forEach(n => {
      html += '<tr><td>' + escHtml(n.iface || '') + '</td><td class="mono">' + escHtml(n.mac || '') + '</td>';
      html += '<td><span class="badge ' + tqClass(n.tq) + '">' + (n.tq != null ? n.tq : '?') + '</span></td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No direct neighbors</div>';
  }
  html += '</div>';

  // Gateways table
  html += '<div class="card mesh-table-card">';
  html += '<div class="card-header">GATEWAYS (' + d.gateway_count + ')</div>';
  if (d.gateways.length) {
    html += '<table class="mesh-table"><thead><tr><th>MAC</th><th>TQ</th><th>Selected</th></tr></thead><tbody>';
    d.gateways.forEach(gw => {
      const sel = gw.selected ? '<span style="color:var(--teal);font-weight:700">★ Yes</span>' : 'No';
      html += '<tr><td class="mono">' + escHtml(gw.mac || '') + '</td>';
      html += '<td><span class="badge ' + tqClass(gw.tq) + '">' + (gw.tq != null ? gw.tq : '?') + '</span></td>';
      html += '<td>' + sel + '</td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No gateways</div>';
  }
  html += '</div>';

  // Originators table
  html += '<div class="card mesh-table-card mesh-wide">';
  html += '<div class="card-header">ORIGINATORS (' + d.originator_count + ')</div>';
  if (d.originators.length) {
    html += '<table class="mesh-table"><thead><tr><th>MAC</th><th>TQ</th><th>Next Hop</th><th>Interface</th></tr></thead><tbody>';
    d.originators.forEach(o => {
      html += '<tr><td class="mono">' + escHtml(o.mac) + '</td>';
      html += '<td><span class="badge ' + tqClass(o.tq) + '">' + o.tq + '</span></td>';
      html += '<td class="mono">' + escHtml(o.nexthop) + '</td>';
      html += '<td>' + escHtml(o.iface) + '</td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="mesh-empty">No originators</div>';
  }
  html += '</div>';

  html += '</div>';

  html += '<div style="padding:8px 0"><button class="cfg-btn" id="mesh-refresh">Refresh</button></div>';
  panel.innerHTML = html;
  document.getElementById('mesh-refresh').addEventListener('click', meshFetch);
}

function meshKV(label, value) {
  return '<div class="mesh-kv-row"><span class="mesh-kv-label">' + label + '</span><span class="mesh-kv-value">' + escHtml(String(value)) + '</span></div>';
}
