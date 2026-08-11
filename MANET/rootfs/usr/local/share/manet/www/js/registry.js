let registryInitialized = false;

function registryActivate() {
  const panel = document.getElementById('tab-registry');
  if (!registryInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading registry...</div>';
    registryInitialized = true;
  }
  registryFetch();
}

async function registryFetch() {
  try {
    const r = await fetch('/api/registry');
    const data = await r.json();
    registryRender(data);
  } catch(e) {
    document.getElementById('tab-registry').innerHTML = '<div class="loading-msg">Failed to load registry</div>';
  }
}

function registryRender(data) {
  const panel = document.getElementById('tab-registry');
  const nodes = data.nodes || [];

  let html = '<div class="mesh-grid">';

  html += '<div class="card mesh-overview">';
  html += '<div class="card-header">ALFRED REGISTRY</div>';
  html += '<div class="mesh-kv">';
  html += meshKV('Nodes in Registry', nodes.length);
  html += meshKV('Last Updated', data.timestamp ? ts(data.timestamp) : '--');
  html += '</div></div>';

  nodes.sort(function(a, b) {
    if (a.is_me) return -1;
    if (b.is_me) return 1;
    var ha = (a.fields.HOSTNAME || '').toLowerCase();
    var hb = (b.fields.HOSTNAME || '').toLowerCase();
    return ha < hb ? -1 : ha > hb ? 1 : 0;
  });

  nodes.forEach(function(node) {
    var f = node.fields;
    var hostname = f.HOSTNAME || node.id;
    var meTag = node.is_me ? ' <span style="color:var(--teal);font-weight:700">(local)</span>' : '';

    html += '<div class="card mesh-table-card mesh-wide">';
    html += '<div class="card-header">' + escHtml(hostname) + meTag + '</div>';
    html += '<table class="mesh-table"><thead><tr><th>Key</th><th>Value</th></tr></thead><tbody>';

    var keys = Object.keys(f).sort();
    keys.forEach(function(k) {
      var v = f[k];
      if (!v) return;
      var cls = '';
      if (k === 'NODE_STATE') cls = v === 'ACTIVE' ? ' style="color:var(--good)"' : ' style="color:var(--bad)"';
      if (k === 'IS_GATEWAY' && v === 'true') cls = ' style="color:var(--teal)"';
      html += '<tr><td class="mono" style="white-space:nowrap;font-weight:600">' + escHtml(k) + '</td>';
      html += '<td class="mono"' + cls + '>' + escHtml(v) + '</td></tr>';
    });

    html += '</tbody></table></div>';
  });

  html += '</div>';
  html += '<div style="padding:8px 0"><button class="cfg-btn" id="registry-refresh">Refresh</button></div>';
  panel.innerHTML = html;
  document.getElementById('registry-refresh').addEventListener('click', registryFetch);
}
