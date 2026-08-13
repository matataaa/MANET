// SPA tab router and shared state
const POLL_INTERVAL_MS = 15000;
let DATA = null;
let LOCAL_DATA = null;
let activeTab = 'dashboard';
let pollTimer = null;
let booted = false;
let _lastBadgeCounts = {};

// --- Auth ---
let _authRequired = false;
let _authenticated = false;
let _authLoginVisible = false;

function authCheckStatus() {
  return fetch('/api/auth/status').then(function(r) { return r.json(); }).then(function(d) {
    _authRequired = d.required;
    _authenticated = d.authenticated;
  }).catch(function() {});
}

function authShowLogin(onSuccess) {
  if (_authLoginVisible) return;
  _authLoginVisible = true;
  var overlay = document.createElement('div');
  overlay.id = 'auth-overlay';
  overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:9999;display:flex;align-items:center;justify-content:center';
  var box = document.createElement('div');
  box.style.cssText = 'background:var(--surface);border-radius:10px;padding:28px 24px;min-width:300px;max-width:360px;box-shadow:0 12px 40px rgba(0,0,0,.3)';
  box.innerHTML =
    '<div style="font-size:15px;font-weight:700;margin-bottom:4px;color:var(--text)">Authentication Required</div>' +
    '<div style="font-size:12px;color:var(--muted);margin-bottom:16px">Enter the admin password to continue.</div>' +
    '<input type="password" id="auth-pw" placeholder="Admin password" style="width:100%;padding:8px 10px;border:1px solid var(--border);border-radius:6px;background:var(--panel);color:var(--text);font-size:13px;outline:none;margin-bottom:6px">' +
    '<div id="auth-err" style="color:var(--bad);font-size:12px;min-height:18px;margin-bottom:10px"></div>' +
    '<div style="display:flex;gap:8px;justify-content:flex-end">' +
    '<button id="auth-cancel" style="padding:6px 14px;border:1px solid var(--border);border-radius:6px;background:var(--panel);color:var(--text);cursor:pointer;font-size:13px">Cancel</button>' +
    '<button id="auth-submit" style="padding:6px 14px;border:none;border-radius:6px;background:var(--accent2);color:#fff;cursor:pointer;font-size:13px;font-weight:600">Login</button>' +
    '</div>';
  overlay.appendChild(box);
  document.body.appendChild(overlay);

  var pw = document.getElementById('auth-pw');
  var err = document.getElementById('auth-err');
  pw.focus();

  function doLogin() {
    var val = pw.value;
    if (!val) { err.textContent = 'Password required'; return; }
    err.textContent = '';
    fetch('/api/perf-auth', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: val })
    }).then(function(r) { return r.json(); }).then(function(d) {
      if (d.ok) {
        _authenticated = true;
        overlay.remove();
        _authLoginVisible = false;
        if (onSuccess) onSuccess();
      } else {
        err.textContent = d.error || 'Invalid password';
        pw.value = '';
        pw.focus();
      }
    }).catch(function() { err.textContent = 'Connection error'; });
  }

  document.getElementById('auth-submit').onclick = doLogin;
  pw.onkeydown = function(e) { if (e.key === 'Enter') doLogin(); };
  document.getElementById('auth-cancel').onclick = function() {
    overlay.remove();
    _authLoginVisible = false;
  };
}

function authFetch(url, opts) {
  opts = opts || {};
  return fetch(url, opts).then(function(resp) {
    if (resp.status === 401) {
      return new Promise(function(resolve) {
        authShowLogin(function() {
          fetch(url, opts).then(resolve);
        });
      });
    }
    return resp;
  });
}

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
  var fn = window[tab + 'Activate'] || window[tab.charAt(0).toUpperCase() + tab.slice(1) + 'Activate'];
  if (!fn) {
    var map = {dashboard:'dashboardActivate',mesh:'meshActivate',nodes:'nodesActivate',config:'configActivate',hardware:'hardwareActivate',voice:'voiceActivate',perf:'perfActivate',services:'servicesActivate',terminal:'terminalActivate',applets:'appletsActivate',fleet:'fleetActivate',docs:'docsActivate',registry:'registryActivate'};
    fn = window[map[tab]];
  }
  if (typeof fn === 'function') fn();
}

// Hash routing
function routeFromHash() {
  if (!booted) return;
  const raw = window.location.hash.replace('#', '') || 'dashboard';
  const parts = raw.split('/');
  let tab = parts[0];
  const sub = parts.slice(1).join('/');
  const valid = ['dashboard', 'mesh', 'nodes', 'config', 'hardware', 'voice', 'perf', 'services', 'terminal', 'applets', 'fleet', 'docs', 'registry'];
  if (tab === 'docs' && sub) {
    docsActiveTab = sub;
  }
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
  if (tab === 'docs' && sub && docsInitialized) {
    docsSwitchTab(sub);
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

// More dropdown — move menu to body so mobile overflow-x:auto on #tab-nav can't clip it
(function() {
  var btn = document.getElementById('nav-more-btn');
  var menu = document.getElementById('nav-more-menu');
  if (!btn || !menu) return;
  document.body.appendChild(menu);

  function positionMenu() {
    var rect = btn.getBoundingClientRect();
    menu.style.position = 'fixed';
    menu.style.top = (rect.bottom + 4) + 'px';
    menu.style.left = '';
    var right = window.innerWidth - rect.right;
    if (right < 0) right = 4;
    menu.style.right = right + 'px';
  }

  btn.addEventListener('click', function(e) {
    e.preventDefault();
    e.stopPropagation();
    var opening = !menu.classList.contains('show');
    menu.classList.toggle('show');
    if (opening) positionMenu();
  });

  menu.addEventListener('click', function(e) {
    var link = e.target.closest('.tab[data-tab]');
    if (!link) return;
    e.preventDefault();
    e.stopPropagation();
    menu.classList.remove('show');
    window.location.hash = link.dataset.tab;
  });

  document.addEventListener('click', function(e) {
    if (!menu.contains(e.target) && !btn.contains(e.target)) menu.classList.remove('show');
  });

  window.addEventListener('resize', function() {
    if (menu.classList.contains('show')) positionMenu();
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
  await Promise.all([fetchData(), authCheckStatus()]);
  booted = true;
  const loader = document.getElementById('boot-loader');
  if (loader) loader.remove();
  routeFromHash();
  pollTimer = setInterval(fetchData, POLL_INTERVAL_MS);
})();
