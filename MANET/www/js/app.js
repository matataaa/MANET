// SPA tab router and shared state
const POLL_INTERVAL_MS = 15000;
let DATA = null;
let LOCAL_DATA = null;
let activeTab = 'dashboard';
let pollTimer = null;
let booted = false;

// Tab routing
function switchTab(tab) {
  if (activeTab === 'voice' && tab !== 'voice' && typeof voiceDeactivate === 'function') voiceDeactivate();
  activeTab = tab;
  document.querySelectorAll('#tab-nav .tab').forEach(el => {
    el.classList.toggle('active', el.dataset.tab === tab);
  });
  document.querySelectorAll('.tab-panel').forEach(el => {
    el.classList.toggle('active', el.id === 'tab-' + tab);
  });
  onTabActivated(tab);
}

function onTabActivated(tab) {
  if (tab === 'dashboard') dashboardActivate();
  else if (tab === 'nodes') nodesActivate();
  else if (tab === 'config') configActivate();
  else if (tab === 'hardware') hardwareActivate();
  else if (tab === 'voice') voiceActivate();
  else if (tab === 'perf') perfActivate();
  else if (tab === 'services') servicesActivate();
  else if (tab === 'mesh') meshActivate();
  else if (tab === 'terminal') terminalActivate();
  else if (tab === 'applets') appletsActivate();
  else if (tab === 'docs') docsActivate();
}

// Hash routing
function routeFromHash() {
  if (!booted) return;
  const raw = window.location.hash.replace('#', '') || 'dashboard';
  const parts = raw.split('/');
  const tab = parts[0];
  const sub = parts.slice(1).join('/');
  const valid = ['dashboard', 'nodes', 'config', 'hardware', 'voice', 'perf', 'services', 'mesh', 'terminal', 'applets', 'docs'];
  switchTab(valid.includes(tab) ? tab : 'dashboard');
  if (tab === 'applets' && sub) {
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

document.querySelectorAll('#tab-nav .tab').forEach(el => {
  el.addEventListener('click', (e) => {
    e.preventDefault();
    window.location.hash = el.dataset.tab;
  });
});

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
  fetchPeerApplets();
}

let _peerAppletCache = {};
let _peerAppletFetching = {};
function fetchPeerApplets() {
  if (!DATA || !DATA.nodes) return;
  DATA.nodes.forEach(function(n) {
    if (n.is_me || !n.ip || n.applets) return;
    if (_peerAppletCache[n.ip]) {
      n.applets = _peerAppletCache[n.ip];
      return;
    }
    if (_peerAppletFetching[n.ip]) return;
    _peerAppletFetching[n.ip] = true;
    fetch('http://' + n.ip + '/api/applets', {signal: AbortSignal.timeout(3000)})
      .then(function(r) { return r.json(); })
      .then(function(d) {
        var applets = (d.applets || []).map(function(a) {
          return {name: a.name, label: a.label || a.name, status: a.status || 'unknown'};
        });
        _peerAppletCache[n.ip] = applets;
        if (DATA && DATA.nodes) {
          DATA.nodes.forEach(function(nd) {
            if (nd.ip === n.ip && !nd.is_me) nd.applets = applets;
          });
          if (activeTab === 'dashboard') dashboardUpdate();
          else if (activeTab === 'nodes') nodesUpdate();
        }
      })
      .catch(function() {})
      .finally(function() { delete _peerAppletFetching[n.ip]; });
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
    hdr.onclick = (faults.length || warns.length) ? function() { location.hash = '#mesh'; } : null;
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
