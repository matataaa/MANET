// Nodes tab: sortable mesh nodes table
let nodesInitialized = false;
let nodesSortCol = 'hostname';
let nodesSortAsc = true;
let nodesFilter = '';

function nodesActivate() {
  const panel = document.getElementById('tab-nodes');
  if (!nodesInitialized) {
    panel.innerHTML = `
      <div class="card">
        <div class="card-header">ALL MESH NODES <span id="nodes-total"></span></div>
        <div class="nodes-search">
          <input type="text" id="nodes-filter" placeholder="Filter by hostname or IP...">
        </div>
        <div class="nodes-table-wrap">
          <table class="nodes-table">
            <thead>
              <tr>
                <th data-col="hostname">Hostname <span class="sort-arrow"></span></th>
                <th data-col="ip">IP Address <span class="sort-arrow"></span></th>
                <th data-col="dns">DNS Name <span class="sort-arrow"></span></th>
                <th data-col="tq">TQ <span class="sort-arrow"></span></th>
                <th data-col="hop_count">Hops <span class="sort-arrow"></span></th>
                <th data-col="services">Services</th>
                <th data-col="battery">Battery <span class="sort-arrow"></span></th>
                <th data-col="uptime">Uptime</th>
                <th data-col="last_seen">Last Seen <span class="sort-arrow"></span></th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody id="nodes-tbody"></tbody>
          </table>
        </div>
      </div>`;
    panel.querySelectorAll('.nodes-table th[data-col]').forEach(th => {
      th.addEventListener('click', () => {
        const col = th.dataset.col;
        if (col === 'services') return;
        if (nodesSortCol === col) nodesSortAsc = !nodesSortAsc;
        else { nodesSortCol = col; nodesSortAsc = true; }
        nodesRender();
      });
    });
    document.getElementById('nodes-filter').addEventListener('input', (e) => {
      nodesFilter = e.target.value.toLowerCase();
      nodesRender();
    });
    nodesInitialized = true;
  }
  if (DATA) nodesUpdate();
}

function nodesUpdate() {
  if (!DATA || !DATA.nodes) return;
  nodesRender();
}

function nodesRender() {
  if (!DATA || !DATA.nodes) return;

  let nodes = DATA.nodes.slice();

  if (nodesFilter) {
    nodes = nodes.filter(n =>
      (n.hostname || '').toLowerCase().includes(nodesFilter) ||
      (n.ip || '').includes(nodesFilter)
    );
  }

  nodes.sort((a, b) => {
    let va, vb;
    switch (nodesSortCol) {
      case 'hostname': va = a.hostname || ''; vb = b.hostname || ''; break;
      case 'ip': va = a.ip || ''; vb = b.ip || ''; break;
      case 'dns': va = (a.hostname || '') + '.mesh'; vb = (b.hostname || '') + '.mesh'; break;
      case 'tq': va = a.is_me ? 999 : (a.tq || -1); vb = b.is_me ? 999 : (b.tq || -1); break;
      case 'hop_count': va = a.hop_count || 99; vb = b.hop_count || 99; break;
      case 'battery': va = a.battery ? a.battery.percentage : -1; vb = b.battery ? b.battery.percentage : -1; break;
      case 'last_seen': va = parseInt(a.last_seen || 0); vb = parseInt(b.last_seen || 0); break;
      default: va = 0; vb = 0;
    }
    if (typeof va === 'string') {
      const cmp = va.localeCompare(vb);
      return nodesSortAsc ? cmp : -cmp;
    }
    return nodesSortAsc ? va - vb : vb - va;
  });

  document.getElementById('nodes-total').textContent = '(' + nodes.length + ')';

  // Update sort arrows
  document.querySelectorAll('.nodes-table th[data-col]').forEach(th => {
    const arrow = th.querySelector('.sort-arrow');
    if (!arrow) return;
    arrow.textContent = th.dataset.col === nodesSortCol ? (nodesSortAsc ? '▲' : '▼') : '';
  });

  const tbody = document.getElementById('nodes-tbody');
  if (!tbody) return;
  tbody.innerHTML = nodes.map(n => {
    const stale = !n.is_me && n.last_seen && DATA.timestamp && (DATA.timestamp - parseInt(n.last_seen)) > 300;
    const svcs = [];
    if (n.is_gateway) svcs.push('<span class="badge badge-gw">GW</span>');
    if (n.ntp) svcs.push('<span class="badge badge-svc">NTP</span>');
    if (n.applets && n.applets.length) {
      n.applets.forEach(function(a) {
        var cls = a.status === 'running' ? 'badge-applet-on' : 'badge-applet-off';
        svcs.push('<span class="badge ' + cls + '">' + escHtml(a.label || a.name) + '</span>');
      });
    }
    if (n.limp) svcs.push('<span class="badge badge-limp">LIMP</span>');
    if (n.is_me) svcs.push('<span class="self-node-badge">THIS NODE</span>');

    const tqCell = n.is_me ? '--' : (stale ? '<span style="color:var(--muted)">OFFLINE</span>' :
      '<span class="' + tqClass(n.tq) + '" style="padding:2px 6px;border-radius:4px;font-size:11px">' + (n.tq != null ? n.tq : '?') + '</span>');

    const battCell = n.battery ? n.battery.percentage + '%' : '--';
    const lastSeen = n.is_me ? '<span style="color:var(--good)">now</span>' :
      stale ? '<span style="color:var(--bad)">' + fmtAge(n.last_seen, DATA.timestamp) + '</span>' :
      (n.last_seen ? '<span style="color:var(--good)">' + fmtAge(n.last_seen, DATA.timestamp) + '</span>' : '--');

    return '<tr>' +
      '<td class="col-host">' + (n.ip ? '<a href="https://' + encodeURI(n.ip) + '/" target="_blank" class="node-link">' + escHtml(n.hostname || n.id) + '</a>' : escHtml(n.hostname || n.id)) + '</td>' +
      '<td>' + escHtml(n.ip || '--') + '</td>' +
      '<td style="color:var(--muted)">' + escHtml((n.hostname || '') + '.mesh') + '</td>' +
      '<td class="col-tq">' + tqCell + '</td>' +
      '<td>' + (n.is_me ? '0' : (n.hop_count || '--')) + '</td>' +
      '<td>' + (svcs.length ? svcs.join(' ') : '--') + '</td>' +
      '<td>' + battCell + '</td>' +
      '<td>' + escHtml(n.uptime || '--') + '</td>' +
      '<td>' + lastSeen + '</td>' +
      '<td class="col-actions">' +
        (n.ip ? '<button class="node-act-btn" onclick="window.open(\'https://\' + \'' + escHtml(n.ip) + '\' + \'/\', \'_blank\')">Portal</button>' : '') +
        '<button class="node-act-btn" onclick="openNodeConfig(\'' + escHtml(n.ip) + '\',\'' + escHtml(n.hostname || n.ip) + '\')">Config</button>' +
        (n.is_me ? '' :
        '<button class="node-act-btn" onclick="openNodeTerminal(\'' + escHtml(n.ip) + '\',\'' + escHtml(n.hostname || n.ip) + '\',\'terminal\')">Shell</button>' +
        '<button class="node-act-btn" onclick="openNodeTerminal(\'' + escHtml(n.ip) + '\',\'' + escHtml(n.hostname || n.ip) + '\',\'logs\')">Logs</button>' +
        '<button class="node-act-btn" onclick="openNodeStream(\'' + escHtml(n.ip) + '\',\'' + escHtml(n.hostname || n.ip) + '\',\'ping\')">Ping</button>' +
        '<button class="node-act-btn" onclick="openNodeStream(\'' + escHtml(n.ip) + '\',\'' + escHtml(n.hostname || n.ip) + '\',\'traceroute\')">Trace</button>') +
      '</td>' +
      '</tr>';
  }).join('');
}
