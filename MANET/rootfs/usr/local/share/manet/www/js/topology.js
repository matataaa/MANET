// D3.js force-directed mesh topology visualization
let topoSvg = null;
let topoSim = null;
let topoZoom = null;
let topoTooltip = null;
let topoNodeMap = {};
let topoInitialized = false;

function topoInit(container) {
  container.innerHTML = '';
  const tooltip = document.createElement('div');
  tooltip.className = 'topo-tooltip';
  container.appendChild(tooltip);
  topoTooltip = tooltip;

  const svg = d3.select(container).append('svg');
  topoSvg = svg;

  const g = svg.append('g').attr('class', 'topo-root');
  g.append('g').attr('class', 'links');
  g.append('g').attr('class', 'nodes');

  topoZoom = d3.zoom()
    .scaleExtent([0.3, 4])
    .on('zoom', (event) => g.attr('transform', event.transform));
  svg.call(topoZoom);

  topoSim = d3.forceSimulation()
    .force('charge', d3.forceManyBody().strength(-3000))
    .force('link', d3.forceLink().id(d => d.id).distance(d => {
      const tq = d.tq != null ? d.tq : 128;
      return 250 + ((255 - tq) / 255) * 250;
    }))
    .force('collision', d3.forceCollide(120))
    .alphaDecay(0.05);

  topoNodeMap = {};
  topoInitialized = false;
}

function topoUpdate(data) {
  if (!topoSvg || !data || !data.nodes) return;

  const svg = topoSvg;
  const container = svg.node().parentNode;
  const W = container.clientWidth;
  const H = container.clientHeight || 520;
  svg.attr('viewBox', [0, 0, W, H]);

  // Preserve positions from previous frame; mark stale nodes
  const ts = data.timestamp || 0;
  const nodes = data.nodes.map((n, idx) => {
    const old = topoNodeMap[n.id];
    const r = n.is_me ? 18 : (n.is_gateway ? 14 : 11);
    const stale = !n.is_me && n.last_seen && ts && (ts - parseInt(n.last_seen)) > 300;
    const obj = { ...n, r: r, stale: stale };
    if (old) {
      obj.x = old.x;
      obj.y = old.y;
      obj.vx = old.vx;
      obj.vy = old.vy;
      if (old.fx != null) obj.fx = old.fx;
      if (old.fy != null) obj.fy = old.fy;
    } else if (!n.is_me) {
      const angle = idx * 2.4;
      obj.x = W / 2 + Math.cos(angle) * 200;
      obj.y = H / 2 + Math.sin(angle) * 200;
    }
    if (n.is_me) { obj.fx = W / 2; obj.fy = H / 2; }
    return obj;
  });

  // Track whether node set changed
  const oldIds = Object.keys(topoNodeMap).sort().join(',');
  const newIds = nodes.map(n => n.id).sort().join(',');
  const setChanged = oldIds !== newIds;

  // Update map for next round
  topoNodeMap = {};
  nodes.forEach(n => { topoNodeMap[n.id] = n; });

  const nodeIds = new Set(nodes.map(n => n.id));
  const staleIds = new Set(nodes.filter(n => n.stale).map(n => n.id));
  const links = (data.edges || []).filter(e => nodeIds.has(e.source) && nodeIds.has(e.target)).map(e => ({
    source: e.source,
    target: e.target,
    type: e.type,
    tq: e.tq
  }));

  const g = svg.select('.topo-root');

  // Links
  const linkSel = g.select('.links')
    .selectAll('line')
    .data(links, d => (d.source.id || d.source) + '-' + (d.target.id || d.target));

  linkSel.exit().remove();

  const linkEnter = linkSel.enter().append('line');

  const linkAll = linkEnter.merge(linkSel)
    .attr('stroke', d => {
      const sid = d.source.id || d.source;
      const tid = d.target.id || d.target;
      if (staleIds.has(sid) || staleIds.has(tid)) return '#6e7681';
      return tqColor(d.tq);
    })
    .attr('stroke-width', d => d.type === 'direct' ? 3 : 1.5)
    .attr('stroke-dasharray', d => {
      const sid = d.source.id || d.source;
      const tid = d.target.id || d.target;
      if (staleIds.has(sid) || staleIds.has(tid)) return '4,6';
      if (d.type === 'inferred') return '3,5';
      if (d.type === 'multihop') return '6,3';
      if (d.type === 'unknown') return '2,6';
      return null;
    })
    .attr('stroke-opacity', d => {
      const sid = d.source.id || d.source;
      const tid = d.target.id || d.target;
      if (staleIds.has(sid) || staleIds.has(tid)) return 0.2;
      if (d.type === 'direct') return 0.85;
      if (d.type === 'inferred') return 0.35;
      return 0.5;
    });

  // Nodes
  const nodeSel = g.select('.nodes')
    .selectAll('g.node')
    .data(nodes, d => d.id);

  nodeSel.exit().remove();

  const nodeEnter = nodeSel.enter().append('g').attr('class', 'node');

  nodeEnter.append('image')
    .attr('href', '/assets/radio-node.svg')
    .attr('width', 32)
    .attr('height', 52)
    .attr('x', -16)
    .attr('y', -26);

  nodeEnter.append('circle')
    .attr('class', 'topo-status-ring')
    .attr('cy', 6)
    .attr('fill', 'none')
    .attr('stroke-width', 2.5);

  nodeEnter.append('text')
    .attr('text-anchor', 'middle')
    .style('font-size', '11px')
    .style('font-weight', '800')
    .style('font-family', 'Lato, Arial, sans-serif')
    .style('pointer-events', 'none');

  const nodeAll = nodeEnter.merge(nodeSel);

  nodeAll.select('.topo-status-ring')
    .attr('r', d => d.r + 4)
    .attr('stroke', d => {
      if (d.stale) return '#6e7681';
      if (d.is_me) return '#00928f';
      if (d.limp) return '#ef4444';
      if (d.state === 'SHUTTING_DOWN') return '#9aa4b2';
      return tqColor(d.tq);
    })
    .attr('stroke-opacity', d => d.stale ? 0.3 : (d.is_me ? 0.8 : 0.5));

  nodeAll.select('image')
    .attr('opacity', d => d.stale ? 0.3 : 1);

  nodeAll.select('text')
    .text(d => d.hostname || d.id)
    .attr('dy', d => d.r + 16)
    .attr('fill', d => d.stale ? '#6e7681' : (isDarkTheme() ? '#f8f6ef' : '#02000d'));

  nodeAll.style('cursor', d => d.ip ? 'pointer' : 'grab');

  // Tooltip
  const tooltip = topoTooltip;
  nodeAll
    .on('mouseover', (event, d) => {
      let html = '<div class="tt-host">' + escHtml(d.hostname || d.id) + '</div>';
      if (d.ip) html += '<div class="tt-row"><span class="tt-label">IP</span>' + escHtml(d.ip) + '</div>';
      html += '<div class="tt-row"><span class="tt-label">DNS</span>' + escHtml((d.hostname || '') + '.mesh') + '</div>';
      if (!d.is_me && d.tq != null) html += '<div class="tt-row"><span class="tt-label">TQ</span>' + d.tq + ' (' + tqPct(d.tq) + '%)</div>';
      if (d.hop_count) html += '<div class="tt-row"><span class="tt-label">Hops</span>' + d.hop_count + '</div>';
      if (d.is_gateway) html += '<div class="tt-row"><span class="tt-label">Role</span>Gateway</div>';
      if (d.limp) html += '<div class="tt-row"><span class="tt-label">State</span><span style="color:var(--bad)">LIMP MODE</span></div>';
      if (d.battery) html += '<div class="tt-row"><span class="tt-label">Batt</span>' + d.battery.percentage + '%</div>';
      if (d.ip) html += '<div class="tt-row" style="opacity:0.6;font-size:11px;margin-top:4px">Click for options</div>';
      tooltip.innerHTML = html;
      tooltip.classList.add('visible');
    })
    .on('mousemove', (event) => {
      const rect = svg.node().parentNode.getBoundingClientRect();
      tooltip.style.left = (event.clientX - rect.left + 14) + 'px';
      tooltip.style.top = (event.clientY - rect.top - 10) + 'px';
    })
    .on('mouseout', () => {
      tooltip.classList.remove('visible');
    });

  // Drag (track movement to distinguish from click)
  var topoDragMoved = false;
  nodeAll.call(d3.drag()
    .on('start', (event, d) => {
      topoDragMoved = false;
      if (!event.active) topoSim.alphaTarget(0.3).restart();
      d.fx = d.x; d.fy = d.y;
    })
    .on('drag', (event, d) => {
      topoDragMoved = true;
      d.fx = event.x; d.fy = event.y;
    })
    .on('end', (event, d) => {
      if (!event.active) topoSim.alphaTarget(0);
      if (!d.is_me) { d.fx = null; d.fy = null; }
      if (!topoDragMoved && d.ip) {
        topoShowNodeMenu(event.sourceEvent, d);
      }
    })
  );

  topoSim.nodes(nodes);
  topoSim.force('link').links(links);

  // First render: full simulation. Updates: gentle reheat only if topology changed.
  if (!topoInitialized) {
    topoSim.alpha(0.8).restart();
    topoInitialized = true;
  } else if (setChanged) {
    topoSim.alpha(0.3).restart();
  } else {
    topoSim.alpha(0.05).restart();
  }

  const pad = 40;
  topoSim.on('tick', () => {
    nodes.forEach(d => {
      if (d.fx == null) { d.x = Math.max(pad, Math.min(W - pad, d.x)); }
      if (d.fy == null) { d.y = Math.max(pad, Math.min(H - pad, d.y)); }
    });
    linkAll
      .attr('x1', d => d.source.x)
      .attr('y1', d => d.source.y)
      .attr('x2', d => d.target.x)
      .attr('y2', d => d.target.y);
    nodeAll.attr('transform', d => 'translate(' + d.x + ',' + d.y + ')');
  });
}

function topoShowNodeMenu(mouseEvent, d) {
  topoDismissMenu();
  var menu = document.createElement('div');
  menu.className = 'topo-node-menu';
  menu.innerHTML =
    '<div class="topo-menu-title">' + escHtml(d.hostname || d.id) + '</div>' +
    '<button class="topo-menu-btn" data-action="config">Config</button>' +
    '<button class="topo-menu-btn" data-action="shell">Shell</button>' +
    '<button class="topo-menu-btn" data-action="logs">Logs</button>';
  var container = topoSvg.node().parentNode;
  var rect = container.getBoundingClientRect();
  menu.style.left = (mouseEvent.clientX - rect.left + 8) + 'px';
  menu.style.top = (mouseEvent.clientY - rect.top + 8) + 'px';
  container.appendChild(menu);
  menu.querySelectorAll('.topo-menu-btn').forEach(function(btn) {
    btn.addEventListener('click', function() {
      var action = btn.dataset.action;
      topoDismissMenu();
      if (action === 'config') {
        openNodeConfig(d.ip, d.hostname);
      } else {
        openNodeTerminal(d.ip, d.hostname, action === 'logs' ? 'logs' : 'terminal');
      }
    });
  });
  setTimeout(function() {
    document.addEventListener('pointerdown', topoDismissHandler, true);
  }, 10);
}

function topoDismissHandler(e) {
  if (e.target.closest && e.target.closest('.topo-node-menu')) return;
  topoDismissMenu();
}

function topoDismissMenu() {
  document.removeEventListener('pointerdown', topoDismissHandler, true);
  var old = document.querySelector('.topo-node-menu');
  if (old) old.remove();
}

function topoDestroy() {
  if (topoSim) { topoSim.stop(); topoSim = null; }
  topoSvg = null;
  topoTooltip = null;
  topoNodeMap = {};
  topoInitialized = false;
}
