// D3.js force-directed mesh topology visualization
var topoSvg = null;
var topoSim = null;
var topoZoom = null;
var topoTooltip = null;
var topoNodeMap = {};
var topoInitialized = false;

function topoInit(container) {
  container.innerHTML = '';

  var hdr = document.createElement('div');
  hdr.className = 'topo-hdr';
  hdr.textContent = 'MESH TOPOLOGY';
  container.appendChild(hdr);

  var tooltip = document.createElement('div');
  tooltip.className = 'topo-tooltip';
  container.appendChild(tooltip);
  topoTooltip = tooltip;

  var legend = document.createElement('div');
  legend.className = 'topo-legend';
  legend.innerHTML =
    '<div class="topo-legend-title">LINK TYPE</div>' +
    '<div class="topo-legend-row"><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke="#ef4444" stroke-width="3"/></svg><span>Direct</span></div>' +
    '<div class="topo-legend-row"><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke="#9aa4b2" stroke-width="1.5" stroke-dasharray="6,3"/></svg><span>Multi-hop</span></div>' +
    '<div class="topo-legend-row"><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke="#9aa4b2" stroke-width="1.5" stroke-dasharray="3,5"/></svg><span>Inferred</span></div>' +
    '<div class="topo-legend-row"><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke="#6e7681" stroke-width="1.5" stroke-dasharray="4,6" opacity="0.4"/></svg><span>Stale</span></div>' +
    '<div class="topo-legend-title" style="margin-top:6px">LINK QUALITY</div>' +
    '<div class="topo-legend-row"><span class="topo-legend-dot" style="background:#22c55e"></span><span>Excellent</span></div>' +
    '<div class="topo-legend-row"><span class="topo-legend-dot" style="background:#eab308"></span><span>Good</span></div>' +
    '<div class="topo-legend-row"><span class="topo-legend-dot" style="background:#f97316"></span><span>Fair</span></div>' +
    '<div class="topo-legend-row"><span class="topo-legend-dot" style="background:#ef4444"></span><span>Poor</span></div>';
  container.appendChild(legend);

  var bar = document.createElement('div');
  bar.className = 'topo-bar';
  bar.innerHTML =
    '<span class="topo-bar-stats" id="topo-bar-stats"></span>' +
    '<span class="topo-bar-live"><span class="topo-bar-dot"></span> LIVE</span>';
  container.appendChild(bar);

  var svg = d3.select(container).append('svg');
  topoSvg = svg;

  var g = svg.append('g').attr('class', 'topo-root');
  g.append('g').attr('class', 'links');
  g.append('g').attr('class', 'link-labels');
  g.append('g').attr('class', 'nodes');

  topoZoom = d3.zoom()
    .scaleExtent([0.3, 4])
    .on('zoom', function(event) { g.attr('transform', event.transform); });
  svg.call(topoZoom);

  topoSim = d3.forceSimulation()
    .force('charge', d3.forceManyBody().strength(-1500))
    .force('link', d3.forceLink().id(function(d) { return d.id; }).distance(function(d) {
      var tq = d.tq != null ? d.tq : 128;
      return 120 + ((255 - tq) / 255) * 180;
    }))
    .force('collision', d3.forceCollide(80))
    .alphaDecay(0.05);

  topoNodeMap = {};
  topoInitialized = false;
}

function topoUpdate(data) {
  if (!topoSvg || !data || !data.nodes) return;

  var svg = topoSvg;
  var container = svg.node().parentNode;
  var W = container.clientWidth;
  var H = container.clientHeight || 520;
  svg.attr('viewBox', [0, 0, W, H]);

  var ts = data.timestamp || 0;
  var nodes = data.nodes.map(function(n, idx) {
    var old = topoNodeMap[n.id];
    var r = n.is_me ? 32 : (n.is_gateway ? 26 : 22);
    var stale = !n.is_me && n.last_seen && ts && (ts - parseInt(n.last_seen)) > 300;
    var obj = Object.assign({}, n, { r: r, stale: stale });
    if (old) {
      obj.x = old.x; obj.y = old.y;
      obj.vx = old.vx; obj.vy = old.vy;
      if (old.fx != null) obj.fx = old.fx;
      if (old.fy != null) obj.fy = old.fy;
    } else if (!n.is_me) {
      var angle = idx * 2.4;
      obj.x = W / 2 + Math.cos(angle) * 200;
      obj.y = H / 2 + Math.sin(angle) * 200;
    }
    if (n.is_me) { obj.fx = W / 2; obj.fy = H / 2; }
    return obj;
  });

  var oldIds = Object.keys(topoNodeMap).sort().join(',');
  var newIds = nodes.map(function(n) { return n.id; }).sort().join(',');
  var setChanged = oldIds !== newIds;

  topoNodeMap = {};
  nodes.forEach(function(n) { topoNodeMap[n.id] = n; });

  var nodeIds = new Set(nodes.map(function(n) { return n.id; }));
  var staleIds = new Set(nodes.filter(function(n) { return n.stale; }).map(function(n) { return n.id; }));
  var links = (data.edges || []).filter(function(e) {
    return nodeIds.has(e.source) && nodeIds.has(e.target);
  }).map(function(e) {
    return { source: e.source, target: e.target, type: e.type, tq: e.tq };
  });

  // Status bar
  var statsEl = document.getElementById('topo-bar-stats');
  if (statsEl) {
    var active = nodes.filter(function(n) { return !n.stale; }).length;
    var activeLinks = links.filter(function(l) {
      var s = l.source.id || l.source, t = l.target.id || l.target;
      return !staleIds.has(s) && !staleIds.has(t);
    }).length;
    statsEl.textContent = active + ' mesh · ' + activeLinks + ' links';
  }

  var g = svg.select('.topo-root');

  // --- Links ---
  var linkSel = g.select('.links').selectAll('line')
    .data(links, function(d) { return (d.source.id || d.source) + '-' + (d.target.id || d.target); });
  linkSel.exit().remove();
  var linkEnter = linkSel.enter().append('line');
  var linkAll = linkEnter.merge(linkSel)
    .attr('stroke', function(d) {
      var s = d.source.id || d.source, t = d.target.id || d.target;
      if (staleIds.has(s) || staleIds.has(t)) return '#6e7681';
      return tqColor(d.tq);
    })
    .attr('stroke-width', function(d) { return d.type === 'direct' ? 3 : 2; })
    .attr('stroke-dasharray', function(d) {
      var s = d.source.id || d.source, t = d.target.id || d.target;
      if (staleIds.has(s) || staleIds.has(t)) return '4,6';
      if (d.type === 'inferred') return '3,5';
      if (d.type === 'multihop') return '6,3';
      if (d.type === 'unknown') return '2,6';
      return null;
    })
    .attr('stroke-opacity', function(d) {
      var s = d.source.id || d.source, t = d.target.id || d.target;
      if (staleIds.has(s) || staleIds.has(t)) return 0.35;
      return 1;
    });

  // --- Link labels ---
  var lblSel = g.select('.link-labels').selectAll('text')
    .data(links, function(d) { return (d.source.id || d.source) + '-' + (d.target.id || d.target); });
  lblSel.exit().remove();
  var lblEnter = lblSel.enter().append('text').attr('class', 'topo-link-lbl');
  var lblAll = lblEnter.merge(lblSel)
    .text(function(d) {
      if (d.tq == null) return '';
      var s = d.source.id || d.source, t = d.target.id || d.target;
      if (staleIds.has(s) || staleIds.has(t)) return '';
      return tqPct(d.tq) + '%';
    })
    .attr('fill', function(d) { return tqColor(d.tq); });

  // --- Nodes ---
  var nodeSel = g.select('.nodes').selectAll('g.node')
    .data(nodes, function(d) { return d.id; });
  nodeSel.exit().remove();

  var nodeEnter = nodeSel.enter().append('g').attr('class', 'node');

  nodeEnter.append('image')
    .attr('href', '/assets/radio-node.svg')
    .attr('width', 32).attr('height', 52)
    .attr('x', -16).attr('y', -26);

  nodeEnter.append('circle')
    .attr('class', 'topo-status-ring')
    .attr('cy', 6).attr('fill', 'none').attr('stroke-width', 2.5);

  nodeEnter.append('text')
    .attr('class', 'topo-badge')
    .attr('text-anchor', 'middle').attr('y', -30)
    .style('font-size', '10px').style('font-weight', '900')
    .style('pointer-events', 'none');

  nodeEnter.append('text')
    .attr('class', 'topo-label')
    .attr('text-anchor', 'middle')
    .style('font-size', '11px').style('font-weight', '800')
    .style('font-family', 'Lato, Arial, sans-serif')
    .style('pointer-events', 'none');

  nodeEnter.append('text')
    .attr('class', 'topo-ip')
    .attr('text-anchor', 'middle')
    .style('font-size', '10px').style('font-weight', '600')
    .style('font-family', "'SF Mono','Fira Code',monospace")
    .style('pointer-events', 'none');

  nodeEnter.append('text')
    .attr('class', 'topo-sublabel')
    .attr('text-anchor', 'middle')
    .style('font-size', '10px').style('font-weight', '600')
    .style('pointer-events', 'none');

  var nodeAll = nodeEnter.merge(nodeSel);

  nodeAll.select('.topo-status-ring')
    .attr('r', function(d) { return d.r + 4; })
    .attr('stroke', function(d) {
      if (d.stale) return '#6e7681';
      if (d.is_me) return '#00928f';
      if (d.limp) return '#ef4444';
      return tqColor(d.tq);
    })
    .attr('stroke-opacity', function(d) { return d.stale ? 0.3 : (d.is_me ? 0.8 : 0.5); });

  nodeAll.select('image')
    .attr('opacity', function(d) { return d.stale ? 0.3 : 1; });

  nodeAll.select('.topo-badge')
    .text(function(d) { return d.is_gateway ? 'GW' : (d.is_me ? 'ME' : ''); })
    .attr('fill', function(d) { return d.is_gateway ? '#7c3aed' : '#00928f'; });

  nodeAll.select('.topo-label')
    .text(function(d) { return d.hostname || d.id; })
    .attr('dy', function(d) { return d.r + 36; })
    .attr('fill', '#fff').style('font-size', '11px');

  nodeAll.select('.topo-ip')
    .text(function(d) { return d.ip || ''; })
    .attr('dy', function(d) { return d.r + 50; })
    .attr('fill', '#8b929e');

  nodeAll.select('.topo-sublabel')
    .text(function(d) {
      if (d.is_me) return '';
      if (d.stale) return 'OFFLINE';
      var parts = [];
      if (d.tq != null) parts.push(tqPct(d.tq) + '%');
      if (d.hop_count != null && d.hop_count > 0)
        parts.push(d.hop_count + (d.hop_count === 1 ? ' hop' : ' hops'));
      return parts.join(' · ');
    })
    .attr('dy', function(d) { return d.r + 63; })
    .attr('fill', function(d) {
      if (d.stale) return '#ef4444';
      return d.tq != null ? tqColor(d.tq) : '#8b929e';
    });

  nodeAll.style('cursor', function(d) { return d.ip ? 'pointer' : 'grab'; });

  // Tooltip
  var tooltip = topoTooltip;
  nodeAll
    .on('mouseover', function(event, d) {
      var html = '<div class="tt-host">' + escHtml(d.hostname || d.id) + '</div>';
      if (d.ip) html += '<div class="tt-row"><span class="tt-label">IP</span>' + escHtml(d.ip) + '</div>';
      html += '<div class="tt-row"><span class="tt-label">DNS</span>' + escHtml((d.hostname || '') + '.mesh') + '</div>';
      if (!d.is_me && d.tq != null) html += '<div class="tt-row"><span class="tt-label">TQ</span>' + d.tq + ' (' + tqPct(d.tq) + '%)</div>';
      if (d.hop_count) html += '<div class="tt-row"><span class="tt-label">Hops</span>' + d.hop_count + '</div>';
      if (d.uptime) html += '<div class="tt-row"><span class="tt-label">Up</span>' + d.uptime + '</div>';
      if (d.is_gateway) html += '<div class="tt-row"><span class="tt-label">Role</span>Gateway' + (d.is_selected_gw ? ' (selected)' : '') + '</div>';
      if (d.limp) html += '<div class="tt-row"><span class="tt-label">State</span><span style="color:var(--bad)">LIMP MODE</span></div>';
      if (d.battery) html += '<div class="tt-row"><span class="tt-label">Batt</span>' + d.battery.percentage + '%</div>';
      if (d.ip) html += '<div class="tt-row" style="opacity:0.6;font-size:11px;margin-top:4px">Click for options</div>';
      tooltip.innerHTML = html;
      tooltip.classList.add('visible');
    })
    .on('mousemove', function(event) {
      var rect = svg.node().parentNode.getBoundingClientRect();
      tooltip.style.left = (event.clientX - rect.left + 14) + 'px';
      tooltip.style.top = (event.clientY - rect.top - 10) + 'px';
    })
    .on('mouseout', function() { tooltip.classList.remove('visible'); });

  // Drag
  var topoDragMoved = false;
  nodeAll.call(d3.drag()
    .on('start', function(event, d) {
      topoDragMoved = false;
      if (!event.active) topoSim.alphaTarget(0.3).restart();
      d.fx = d.x; d.fy = d.y;
    })
    .on('drag', function(event, d) {
      topoDragMoved = true;
      d.fx = event.x; d.fy = event.y;
    })
    .on('end', function(event, d) {
      if (!event.active) topoSim.alphaTarget(0);
      if (!d.is_me) { d.fx = null; d.fy = null; }
      if (!topoDragMoved && d.ip) {
        topoShowNodeMenu(event.sourceEvent, d);
      }
    })
  );

  // Simulation
  topoSim.nodes(nodes);
  topoSim.force('link').links(links);
  topoSim.force('x', d3.forceX(W / 2).strength(0.06));
  topoSim.force('y', d3.forceY(H / 2).strength(0.06));

  if (!topoInitialized) {
    topoSim.alpha(0.8).restart();
    topoInitialized = true;
  } else if (setChanged) {
    topoSim.alpha(0.3).restart();
  } else {
    topoSim.alpha(0.05).restart();
  }

  var pad = 40;
  topoSim.on('tick', function() {
    nodes.forEach(function(d) {
      if (d.fx == null) { d.x = Math.max(pad, Math.min(W - pad, d.x)); }
      if (d.fy == null) { d.y = Math.max(pad, Math.min(H - pad, d.y)); }
    });
    linkAll
      .attr('x1', function(d) { return d.source.x; })
      .attr('y1', function(d) { return d.source.y; })
      .attr('x2', function(d) { return d.target.x; })
      .attr('y2', function(d) { return d.target.y; });
    lblAll
      .attr('x', function(d) { return (d.source.x + d.target.x) / 2; })
      .attr('y', function(d) { return (d.source.y + d.target.y) / 2 - 8; });
    nodeAll.attr('transform', function(d) { return 'translate(' + d.x + ',' + d.y + ')'; });
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
