// SPA tab router and shared state
const POLL_INTERVAL_MS = 15000;
let DATA = null;
let LOCAL_DATA = null;
let activeTab = 'dashboard';
let pollTimer = null;
let booted = false;
let _lastBadgeCounts = {};

// Tab routing
function switchTab(tab) {
  if (activeTab === 'voice' && tab !== 'voice' && typeof voiceDeactivate === 'function') voiceDeactivate();
  if (activeTab === 'hardware' && tab !== 'hardware' && typeof hardwareDeactivate === 'function') hardwareDeactivate();
  if (activeTab === 'fleet' && tab !== 'fleet' && typeof fleetDeactivate === 'function') fleetDeactivate();
  var overlay = document.querySelector('.applet-iframe-overlay');
  if (overlay) overlay.remove();
  activeTab = tab;
  var moreTabs = ['perf', 'terminal', 'applets', 'fleet', 'docs', 'registry'];
  document.querySelectorAll('#tab-nav .tab[data-tab]').forEach(el => {
    el.classList.toggle('active', el.dataset.tab === tab);
  });
  var moreBtn = document.getElementById('nav-more-btn');
  if (moreBtn) moreBtn.classList.toggle('active', moreTabs.indexOf(tab) !== -1);
  document.querySelectorAll('.tab-panel').forEach(el => {
    el.classList.toggle('active', el.id === 'tab-' + tab);
  });
  onTabActivated(tab);
}

function onTabActivated(tab) {
  if (tab === 'dashboard') dashboardActivate();
  else if (tab === 'mesh') meshActivate();
  else if (tab === 'nodes') nodesActivate();
  else if (tab === 'config') configActivate();
  else if (tab === 'hardware') hardwareActivate();
  else if (tab === 'voice') voiceActivate();
  else if (tab === 'perf') perfActivate();
  else if (tab === 'services') servicesActivate();
  else if (tab === 'terminal') terminalActivate();
  else if (tab === 'applets') appletsActivate();
  else if (tab === 'fleet') fleetActivate();
  else if (tab === 'docs') docsActivate();
  else if (tab === 'registry') registryActivate();
}

// Hash routing
function routeFromHash() {
  if (!booted) return;
  const raw = window.location.hash.replace('#', '') || 'dashboard';
  const parts = raw.split('/');
  let tab = parts[0];
  const sub = parts.slice(1).join('/');
  const valid = ['dashboard', 'mesh', 'nodes', 'config', 'hardware', 'voice', 'perf', 'services', 'terminal', 'applets', 'fleet', 'docs', 'registry'];
  switchTab(valid.includes(tab) ? tab : 'dashboard');
  if (tab === 'applets' && sub && !document.querySelector('.applet-iframe-overlay')) {
    var subParts = sub.split('/');
    var appletName = decodeURIComponent(subParts[0]);
    var appletView = subParts[1] || '';
    setTimeout(function() {
      if (appletView === 'config') openAppletConfig(appletName);
      else openApplet(appletName);
    }, 100);
  }
}

window.addEventListener('hashchange', routeFromHash);

document.querySelectorAll('#tab-nav .tab[data-tab]').forEach(el => {
  el.addEventListener('click', (e) => {
    e.preventDefault();
    var menu = document.getElementById('nav-more-menu');
    if (menu) menu.classList.remove('show');
    window.location.hash = el.dataset.tab;
  });
});

// More dropdown
(function() {
  var btn = document.getElementById('nav-more-btn');
  var menu = document.getElementById('nav-more-menu');
  if (!btn || !menu) return;
  btn.addEventListener('click', function(e) {
    e.preventDefault();
    e.stopPropagation();
    var opening = !menu.classList.contains('show');
    menu.classList.toggle('show');
    if (opening) {
      var rect = btn.getBoundingClientRect();
      menu.style.top = (rect.bottom + 4) + 'px';
    }
  });
  document.addEventListener('click', function(e) {
    if (!menu.contains(e.target) && e.target !== btn) menu.classList.remove('show');
  });
})();

// Data polling
async function fetchData() {
  try {
    const [dataResp, localResp] = await Promise.all([
      fetch('/api/data'),
      fetch('/api/local')
    ]);
    DATA = await dataResp.json();
    LOCAL_DATA = await localResp.json();
    updateHeader();
    onDataUpdated();
  } catch(e) {
    console.error('Fetch error:', e);
  }
}

function onDataUpdated() {
  if (activeTab === 'dashboard') dashboardUpdate();
  else if (activeTab === 'nodes') nodesUpdate();
  pollAppletBadges();
}

function pollAppletBadges() {
  var controller = new AbortController();
  var timeoutId = setTimeout(function() { controller.abort(); }, 3000);
  fetch('/api/applets/mesh-chat/proxy/unread', { signal: controller.signal })
    .then(function(resp) {
      clearTimeout(timeoutId);
      return resp.json();
    })
    .then(function(data) {
      var count = data.count || 0;
      if (typeof notifyBadge === 'function') notifyBadge('mesh-chat', count);
      var prev = _lastBadgeCounts['mesh-chat'] || 0;
      if (count > prev && typeof notify === 'function') {
        notify('Mesh Chat', count + ' unread message(s)', {
          type: 'info',
          onClick: function() { window.location.hash = '#applets/mesh-chat'; }
        });
      }
      _lastBadgeCounts['mesh-chat'] = count;
    })
    .catch(function() {
      clearTimeout(timeoutId);
      // Silently ignore errors
    });
}

function updateHeader() {
  if (!DATA || !LOCAL_DATA) return;

  document.getElementById('hdr-hostname').textContent = LOCAL_DATA.hostname || '--';
  if (LOCAL_DATA.hostname) document.title = 'Mesh: ' + LOCAL_DATA.hostname;
  document.getElementById('hdr-ip').textContent = LOCAL_DATA.ip || '--';
  var totalNodes = DATA.nodes ? DATA.nodes.length : 0;
  var onlineNodes = DATA.nodes ? DATA.nodes.filter(function(n) {
    if (n.is_me) return true;
    if (!n.last_seen || !DATA.timestamp) return false;
    return (DATA.timestamp - parseInt(n.last_seen)) <= 300;
  }).length : 0;
  document.getElementById('hdr-nodes').textContent = onlineNodes + '/' + totalNodes + ' nodes';
  document.getElementById('hdr-time').textContent = DATA.timestamp ? ts(DATA.timestamp) : '--';

  // Health
  const hdr = document.getElementById('hdr-health');
  const dot = document.getElementById('hdr-health-dot');
  const label = document.getElementById('hdr-health-label');
  if (LOCAL_DATA.interfaces) {
    const faults = LOCAL_DATA.interfaces.filter(i => i.health === 'fault');
    const warns = LOCAL_DATA.interfaces.filter(i => i.health === 'warn');
    let cls, dotColor, text;
    if (faults.length > 0) {
      cls = 'health-fault';
      dotColor = 'var(--bad)';
      text = faults.length === 1 ? '⚠ ' + faults[0].name + ' FAULT' : '⚠ ' + faults.length + ' FAULTS';
    } else if (warns.length > 0) {
      cls = 'health-warn';
      dotColor = 'var(--warn)';
      text = warns.length === 1 ? '⚠ ' + warns[0].name + ' WARN' : '⚠ ' + warns.length + ' WARNS';
    } else {
      cls = 'health-ok';
      dotColor = 'var(--good)';
      text = 'ALL OK';
    }
    hdr.className = cls;
    dot.style.background = dotColor;
    label.textContent = text;
    label.style.color = dotColor;
    hdr.style.cursor = (faults.length || warns.length) ? 'pointer' : '';
    hdr.title = (faults.length || warns.length)
      ? faults.concat(warns).map(i => i.name + ': ' + (i.faults||[]).join(', ')).join('\n')
      : '';
    hdr.onclick = (faults.length || warns.length) ? function() { location.hash = '#dashboard'; } : null;
  }
}

// Open terminal tab targeting a remote node
function openNodeTerminal(ip, hostname, mode) {
  window._pendingTermTarget = { ip: ip, hostname: hostname || ip, mode: mode || 'terminal' };
  window.location.hash = 'terminal';
}

// Open config tab targeting a specific node (local or remote)
function openNodeConfig(ip, hostname) {
  if (LOCAL_DATA && LOCAL_DATA.ip === ip) {
    window._pendingConfigTarget = null;
  } else {
    window._pendingConfigTarget = { ip: ip, hostname: hostname || ip };
  }
  window.location.hash = 'config';
}

// Boot — fetch data before routing so tabs have data on first render
(async () => {
  await fetchData();
  booted = true;
  const loader = document.getElementById('boot-loader');
  if (loader) loader.remove();
  routeFromHash();
  pollTimer = setInterval(fetchData, POLL_INTERVAL_MS);
})();
