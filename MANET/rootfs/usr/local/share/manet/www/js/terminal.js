var termInitialized = false;
var term = null;
var termWs = null;
var termFit = null;
var termSessions = {};
var termCurrentTarget = '';
var termMode = localStorage.getItem('termMode') || 'terminal';
var termReconnectTimer = null;

var termMultiPanes = [];
var termMultiSync = true;
var termMultiNextId = 1;

var termInputBuf = '';
var termInputTimer = null;
var termPingInterval = null;
var termLatency = null;
var termReconnectAttempt = 0;
var termLocalEcho = localStorage.getItem('termLocalEcho') === 'true';
var termEchoQueue = [];
var termMultiInputBuf = '';
var termMultiInputTimer = null;

var TERM_PORT = location.port || (location.protocol === 'https:' ? '443' : '80');
var TERM_INPUT_DELAY = 30;
var TERM_PING_INTERVAL = 5000;

var TERM_THEME = {
  background: '#0d1117',
  foreground: '#c9d1d9',
  cursor: '#c9d1d9',
  selectionBackground: '#264f78',
  black: '#484f58', red: '#ff7b72', green: '#3fb950',
  yellow: '#d29922', blue: '#58a6ff', magenta: '#bc8cff',
  cyan: '#39d353', white: '#c9d1d9',
  brightBlack: '#6e7681', brightRed: '#ffa198', brightGreen: '#56d364',
  brightYellow: '#e3b341', brightBlue: '#79c0ff', brightMagenta: '#d2a8ff',
  brightCyan: '#56d364', brightWhite: '#f0f6fc'
};

// ===== LATENCY HELPERS =====

function termHasControl(data) {
  for (var i = 0; i < data.length; i++) {
    var c = data.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return true;
  }
  return false;
}

function termFlushInput(ws) {
  if (termInputBuf && ws && ws.readyState === 1) ws.send(termInputBuf);
  termInputBuf = '';
  termInputTimer = null;
}

function termQueueInput(ws, data) {
  termInputBuf += data;
  if (termHasControl(data) || termInputBuf.length >= 128) {
    if (termInputTimer) { clearTimeout(termInputTimer); termInputTimer = null; }
    termFlushInput(ws);
  } else if (!termInputTimer) {
    var w = ws;
    termInputTimer = setTimeout(function() { termFlushInput(w); }, TERM_INPUT_DELAY);
  }
}

function termStartPing(ws) {
  termStopPing();
  termLatency = null;
  termPingInterval = setInterval(function() {
    if (ws && ws.readyState === 1) {
      var buf = new ArrayBuffer(9);
      var view = new DataView(buf);
      view.setUint8(0, 2);
      view.setFloat64(1, Date.now());
      ws.send(buf);
    }
  }, TERM_PING_INTERVAL);
}

function termStopPing() {
  if (termPingInterval) { clearInterval(termPingInterval); termPingInterval = null; }
  termLatency = null;
}

function termIsPong(data) {
  return data instanceof ArrayBuffer && data.byteLength === 9 && new DataView(data).getUint8(0) === 3;
}

function termDecodePong(data) {
  return Math.round(Date.now() - new DataView(data).getFloat64(1));
}

function termUpdateStatusLatency(el, latency) {
  if (!el) return;
  var text = el.textContent.replace(/ \(\d+ms\)$/, '');
  if (latency !== null && latency !== undefined) text += ' (' + latency + 'ms)';
  el.textContent = text;
}

function termGetReconnectDelay() {
  var delay = Math.min(3000 * Math.pow(2, termReconnectAttempt), 30000);
  termReconnectAttempt++;
  return Math.round(delay);
}

function termCloseWs() {
  if (termWs) {
    termWs.onmessage = null;
    termWs.onclose = null;
    termWs.close();
    termWs = null;
  }
}

function termNodeLabel(ip) {
  if (!ip) return '';
  var label = ip;
  if (DATA && DATA.nodes) {
    DATA.nodes.forEach(function(n) {
      if (n.ip === ip) label = (n.hostname || ip) + ' (' + ip + ')';
    });
  }
  return label;
}

function termLocalEchoWrite(t, data) {
  for (var i = 0; i < data.length; i++) {
    var c = data.charCodeAt(i);
    if (c >= 0x20 && c !== 0x7f) {
      t.write(data[i]);
      termEchoQueue.push(c);
    }
  }
}

function termFilterEcho(data) {
  if (termEchoQueue.length === 0) return data;
  if (data instanceof Uint8Array) {
    var i = 0;
    while (i < data.length && termEchoQueue.length > 0) {
      if (data[i] === termEchoQueue[0]) { termEchoQueue.shift(); i++; }
      else { termEchoQueue = []; break; }
    }
    return i < data.length ? data.subarray(i) : null;
  }
  var i = 0;
  while (i < data.length && termEchoQueue.length > 0) {
    if (data.charCodeAt(i) === termEchoQueue[0]) { termEchoQueue.shift(); i++; }
    else { termEchoQueue = []; break; }
  }
  return i < data.length ? data.substring(i) : null;
}

function termMultiFlushInput() {
  var buf = termMultiInputBuf;
  termMultiInputBuf = '';
  termMultiInputTimer = null;
  if (!buf) return;
  termMultiPanes.forEach(function(p) {
    if (p.ws && p.ws.readyState === 1) p.ws.send(buf);
  });
}

function termMultiQueueInput(pane, data) {
  if (termMultiSync) {
    termMultiInputBuf += data;
    if (termHasControl(data) || termMultiInputBuf.length >= 128) {
      if (termMultiInputTimer) { clearTimeout(termMultiInputTimer); termMultiInputTimer = null; }
      termMultiFlushInput();
    } else if (!termMultiInputTimer) {
      termMultiInputTimer = setTimeout(termMultiFlushInput, TERM_INPUT_DELAY);
    }
  } else {
    pane._inputBuf = (pane._inputBuf || '') + data;
    if (termHasControl(data) || pane._inputBuf.length >= 128) {
      if (pane._inputTimer) { clearTimeout(pane._inputTimer); pane._inputTimer = null; }
      if (pane.ws && pane.ws.readyState === 1) pane.ws.send(pane._inputBuf);
      pane._inputBuf = '';
    } else if (!pane._inputTimer) {
      pane._inputTimer = setTimeout(function() {
        pane._inputTimer = null;
        if (pane.ws && pane.ws.readyState === 1) pane.ws.send(pane._inputBuf || '');
        pane._inputBuf = '';
      }, TERM_INPUT_DELAY);
    }
  }
}

function termPaneStartPing(pane) {
  termPaneStopPing(pane);
  pane._pingInterval = setInterval(function() {
    if (pane.ws && pane.ws.readyState === 1) {
      var buf = new ArrayBuffer(9);
      var view = new DataView(buf);
      view.setUint8(0, 2);
      view.setFloat64(1, Date.now());
      pane.ws.send(buf);
    }
  }, TERM_PING_INTERVAL);
}

function termPaneStopPing(pane) {
  if (pane._pingInterval) { clearInterval(pane._pingInterval); pane._pingInterval = null; }
}

// ===== MAIN =====

function terminalActivate() {
  var panel = document.getElementById('tab-terminal');
  if (!termInitialized) {
    panel.innerHTML =
      '<div class="term-wrap">' +
        '<div class="term-bar">' +
          '<div class="term-mode-toggle">' +
            '<button class="term-mode-btn active" id="term-mode-shell" data-mode="terminal">Shell</button>' +
            '<button class="term-mode-btn" id="term-mode-multi" data-mode="multi">Multi</button>' +
            '<button class="term-mode-btn" id="term-mode-logs" data-mode="logs">Logs</button>' +
          '</div>' +
          '<div id="term-single-controls" class="term-bar-group">' +
            '<select id="term-target" class="term-select"></select>' +
            '<div id="term-cred-area"></div>' +
            '<div id="term-log-controls" style="display:none">' +
              '<select id="term-log-unit" class="term-select"><option value="">All Logs</option></select>' +
            '</div>' +
          '</div>' +
          '<div id="term-multi-controls" class="term-bar-group" style="display:none">' +
            '<select id="term-multi-node" class="term-select"></select>' +
            '<button class="term-multi-btn" id="term-multi-add-node">+Node</button>' +
            '<button class="term-multi-btn" id="term-multi-add-all">+All</button>' +
            '<span class="term-bar-sep"></span>' +
            '<input type="text" id="term-multi-host" class="term-cred-input" placeholder="Host/IP" style="width:100px">' +
            '<input type="text" id="term-multi-user" class="term-cred-input" placeholder="User" value="radio" style="width:70px">' +
            '<input type="password" id="term-multi-pass" class="term-cred-input" placeholder="Password" value="radio" style="width:70px">' +
            '<button class="term-multi-btn" id="term-multi-add-host">+Host</button>' +
            '<span class="term-bar-sep"></span>' +
            '<button class="term-mode-btn active" id="term-multi-sync-btn">Sync</button>' +
            '<button class="term-multi-btn term-multi-btn-danger" id="term-multi-clear">Clear</button>' +
          '</div>' +
          '<span id="term-status" class="term-status" style="margin-left:auto"></span>' +
          '<button class="term-mode-btn' + (termLocalEcho ? ' active' : '') + '" id="term-echo-btn" title="Local echo for high-latency links">Echo</button>' +
          '<button class="cfg-btn cfg-btn-danger" id="term-reconnect" style="display:none">Reconnect</button>' +
        '</div>' +
        '<div id="term-container" class="term-container"></div>' +
        '<div id="term-multi-grid" class="term-multi-grid" style="display:none"></div>' +
      '</div>';

    term = new Terminal({
      cursorBlink: true, fontSize: 13,
      fontFamily: "'SF Mono','Fira Code','Cascadia Code','Menlo',monospace",
      scrollback: 10000, theme: TERM_THEME
    });
    termFit = new FitAddon.FitAddon();
    term.loadAddon(termFit);
    term.open(document.getElementById('term-container'));
    termFit.fit();

    term.onData(function(data) {
      if (termWs && termWs.readyState === 1 && termMode === 'terminal') {
        if (termLocalEcho) termLocalEchoWrite(term, data);
        termQueueInput(termWs, data);
      }
    });
    term.onResize(function(size) {
      if (termMode === 'terminal') termSendResize(size.cols, size.rows);
    });

    window.addEventListener('resize', function() {
      if (!document.getElementById('tab-terminal').classList.contains('active')) return;
      if (termMode === 'multi') termMultiFitAll();
      else if (termFit) termFit.fit();
    });

    document.getElementById('term-target').addEventListener('change', termOnTargetChange);
    document.getElementById('term-reconnect').addEventListener('click', function() {
      if (termMode === 'logs') termConnectLogs();
      else termConnectWs();
    });
    document.getElementById('term-mode-shell').addEventListener('click', function() { termSetMode('terminal'); });
    document.getElementById('term-mode-multi').addEventListener('click', function() { termSetMode('multi'); });
    document.getElementById('term-mode-logs').addEventListener('click', function() { termSetMode('logs'); });
    document.getElementById('term-log-unit').addEventListener('change', function() {
      localStorage.setItem('termLogUnit', this.value);
      termConnectLogs();
    });
    document.getElementById('term-echo-btn').addEventListener('click', function() {
      termLocalEcho = !termLocalEcho;
      localStorage.setItem('termLocalEcho', termLocalEcho);
      this.classList.toggle('active', termLocalEcho);
      if (!termLocalEcho) termEchoQueue = [];
    });

    document.getElementById('term-multi-add-node').addEventListener('click', termMultiAddSelectedNode);
    document.getElementById('term-multi-add-all').addEventListener('click', termMultiAddAllNodes);
    document.getElementById('term-multi-add-host').addEventListener('click', termMultiAddCustomHost);
    document.getElementById('term-multi-sync-btn').addEventListener('click', function() {
      termMultiSync = !termMultiSync;
      this.classList.toggle('active', termMultiSync);
      document.querySelectorAll('.term-multi-pane').forEach(function(el) {
        el.classList.toggle('sync-on', termMultiSync);
      });
      termMultiUpdateStatus();
    });
    document.getElementById('term-multi-clear').addEventListener('click', termMultiClearAll);
    document.getElementById('term-multi-host').addEventListener('keydown', function(e) { if (e.key === 'Enter') termMultiAddCustomHost(); });
    document.getElementById('term-multi-pass').addEventListener('keydown', function(e) { if (e.key === 'Enter') termMultiAddCustomHost(); });

    termInitialized = true;
    termPopulateTargets();
    termSetMode(termMode);
  } else {
    termPopulateTargets();
    if (termMode === 'multi') {
      termMultiPopulateNodes();
      termMultiFitAll();
    } else {
      termFit.fit();
      term.focus();
    }
  }

  if (window._pendingTermTarget) {
    var p = window._pendingTermTarget;
    window._pendingTermTarget = null;
    var sel = document.getElementById('term-target');
    var targetVal = (LOCAL_DATA && LOCAL_DATA.ip === p.ip) ? '' : p.ip;
    sel.value = targetVal;
    termCurrentTarget = targetVal;
    termSetMode(p.mode === 'logs' ? 'logs' : 'terminal');
  }
}

// ===== MODE =====

function termSetMode(mode) {
  termMode = mode;
  localStorage.setItem('termMode', mode);
  if (termReconnectTimer) { clearTimeout(termReconnectTimer); termReconnectTimer = null; }

  document.getElementById('term-mode-shell').classList.toggle('active', mode === 'terminal');
  document.getElementById('term-mode-multi').classList.toggle('active', mode === 'multi');
  document.getElementById('term-mode-logs').classList.toggle('active', mode === 'logs');

  var isSingle = mode !== 'multi';
  document.getElementById('term-single-controls').style.display = isSingle ? '' : 'none';
  document.getElementById('term-multi-controls').style.display = mode === 'multi' ? '' : 'none';
  document.getElementById('term-container').style.display = isSingle ? '' : 'none';
  document.getElementById('term-multi-grid').style.display = mode === 'multi' ? '' : 'none';
  document.getElementById('term-reconnect').style.display = 'none';

  if (isSingle) {
    document.getElementById('term-cred-area').style.display = mode === 'terminal' ? '' : 'none';
    document.getElementById('term-log-controls').style.display = mode === 'logs' ? 'flex' : 'none';
    termCloseWs();
    termStopPing();
    term.clear();
    if (mode === 'logs') {
      termPopulateLogUnits();
      termConnectLogs();
    } else {
      termOnTargetChange();
    }
    setTimeout(function() { if (termFit) termFit.fit(); }, 50);
  } else {
    termCloseWs();
    termStopPing();
    termMultiPopulateNodes();
    termMultiUpdatePlaceholder();
    termMultiUpdateStatus();
    setTimeout(termMultiFitAll, 50);
  }
}

// ===== POPULATE =====

function termPopulateTargets() {
  var sel = document.getElementById('term-target');
  if (!DATA || !DATA.nodes) return;
  var current = sel.value;
  sel.innerHTML = '<option value="">This Node</option>';
  DATA.nodes.forEach(function(n) {
    if (n.is_me || !n.ip) return;
    var opt = document.createElement('option');
    opt.value = n.ip;
    opt.textContent = (n.hostname || n.ip) + ' (' + n.ip + ')';
    sel.appendChild(opt);
  });
  var co = document.createElement('option');
  co.value = '__custom__';
  co.textContent = 'Custom Host…';
  sel.appendChild(co);
  sel.value = current;
}

function termPopulateLogUnits() {
  var sel = document.getElementById('term-log-unit');
  var current = sel.value || localStorage.getItem('termLogUnit') || '';
  sel.innerHTML = '<option value="">All Logs</option>';
  var units = [
    'manet-ctrl', 'node-manager', 'alfred',
    'gateway-manager', 'mesh-boot-lobby',
    'sae-watchdog', 'manet-txpower',
    'hostapd', 'wpa_supplicant', 'dnsmasq', 'avahi-daemon',
    'mesh-voice',
    'gps-reader', 'battery-reader', 'cot-emitter', 'chronyd',
    'sshd', 'systemd-networkd'
  ];
  units.forEach(function(u) {
    var opt = document.createElement('option');
    opt.value = u; opt.textContent = u;
    sel.appendChild(opt);
  });
  sel.value = current;
}

function termMultiPopulateNodes() {
  var sel = document.getElementById('term-multi-node');
  if (!sel || !DATA || !DATA.nodes) return;
  sel.innerHTML = '<option value="">Select node…</option>';
  var existing = {};
  termMultiPanes.forEach(function(p) { existing[p.target] = true; });
  if (!existing['__local__']) {
    var lo = document.createElement('option');
    lo.value = '__local__';
    lo.textContent = 'This Node' + (LOCAL_DATA && LOCAL_DATA.hostname ? ' (' + LOCAL_DATA.hostname + ')' : '');
    sel.appendChild(lo);
  }
  DATA.nodes.forEach(function(n) {
    if (n.is_me || !n.ip || existing[n.ip]) return;
    var opt = document.createElement('option');
    opt.value = n.ip;
    opt.textContent = (n.hostname || n.ip) + ' (' + n.ip + ')';
    sel.appendChild(opt);
  });
}

// ===== SINGLE: TARGET CHANGE =====

function termOnTargetChange() {
  var target = document.getElementById('term-target').value;
  var area = document.getElementById('term-cred-area');

  if (termMode === 'logs') {
    area.innerHTML = '';
    termCurrentTarget = target === '__custom__' ? '' : target;
    termConnectLogs();
    return;
  }

  if (target === '__custom__') {
    termCurrentTarget = '';
    termCloseWs();
    termStopPing();
    term.clear();
    termShowCustomHostForm();
    return;
  }

  termCurrentTarget = target;
  if (!target) {
    area.innerHTML = '';
    termConnectWs();
    return;
  }

  area.innerHTML =
    '<span class="term-proto-badge term-proto-ws">PROXY</span>' +
    '<button class="term-cred-btn term-cred-disconnect" id="term-disconnect">Disconnect</button>';
  document.getElementById('term-disconnect').addEventListener('click', function() {
    termCloseWs();
    termStopPing();
    term.clear();
    area.innerHTML = '';
    document.getElementById('term-target').value = '';
    termCurrentTarget = '';
  });
  termConnectWs(target, { protocol: 'proxy' });
}

function termShowCustomHostForm() {
  var area = document.getElementById('term-cred-area');
  area.innerHTML =
    '<div class="term-cred-form">' +
      '<input type="text" id="term-custom-host" class="term-cred-input" placeholder="Host / IP" style="width:120px">' +
      '<input type="text" id="term-cred-user" class="term-cred-input" placeholder="User" value="radio">' +
      '<input type="password" id="term-cred-pass" class="term-cred-input" placeholder="Password">' +
      '<button class="term-cred-btn term-cred-connect" id="term-cred-go">Connect</button>' +
    '</div>';
  document.getElementById('term-cred-go').addEventListener('click', termDoCustomConnect);
  document.getElementById('term-cred-pass').addEventListener('keydown', function(e) { if (e.key === 'Enter') termDoCustomConnect(); });
  document.getElementById('term-custom-host').addEventListener('keydown', function(e) { if (e.key === 'Enter') termDoCustomConnect(); });
}

function termDoCustomConnect() {
  var host = document.getElementById('term-custom-host').value.trim();
  if (!host) { document.getElementById('term-custom-host').style.borderColor = '#b42318'; return; }
  var user = document.getElementById('term-cred-user').value.trim() || 'radio';
  var pass = document.getElementById('term-cred-pass').value;
  if (!pass) { document.getElementById('term-cred-pass').style.borderColor = '#b42318'; return; }
  termCurrentTarget = host;
  termSessions[host] = { protocol: 'ssh', user: user, password: pass };
  var area = document.getElementById('term-cred-area');
  area.innerHTML =
    '<span class="term-proto-badge term-proto-ssh">SSH</span>' +
    '<span style="color:#8b949e;font-size:11px;font-weight:600;font-family:var(--font)">' + escHtml(host) + '</span>' +
    '<button class="term-cred-btn term-cred-disconnect" id="term-disconnect">Disconnect</button>';
  document.getElementById('term-disconnect').addEventListener('click', function() {
    delete termSessions[host];
    document.getElementById('term-target').value = '__custom__';
    termCloseWs();
    termStopPing();
    term.clear();
    termShowCustomHostForm();
  });
  termConnectWs(host, termSessions[host]);
}

// ===== SINGLE: WEBSOCKET =====

function termConnectWs(target, session) {
  if (_authRequired && !_authenticated) {
    authShowLogin(function() { termConnectWs(target, session); });
    return;
  }
  termCloseWs();
  termStopPing();
  termEchoQueue = [];
  var reconnBtn = document.getElementById('term-reconnect');
  var statusEl = document.getElementById('term-status');
  var params = new URLSearchParams();
  var isRemote = !!(target && session);
  if (isRemote) {
    params.set('target', target);
    params.set('protocol', session.protocol);
    if (session.user) params.set('user', session.user);
    if (session.password) params.set('password', session.password);
  }
  var wsUrl = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.hostname + ':' + TERM_PORT + '/ws/terminal';
  if (params.toString()) wsUrl += '?' + params;

  var connectLabel = isRemote ? termNodeLabel(target) : 'local shell';
  statusEl.textContent = 'Connecting…';
  statusEl.className = 'term-status term-status-connecting';
  reconnBtn.style.display = 'none';
  term.clear();
  if (isRemote) {
    term.writeln('\r\n\x1b[33m  Connecting to ' + connectLabel + '…\x1b[0m\r\n');
  }

  var openedAt = 0;
  var gotFirstData = false;
  termWs = new WebSocket(wsUrl);
  termWs.binaryType = 'arraybuffer';

  termWs.onopen = function() {
    openedAt = Date.now();
    termReconnectAttempt = 0;
    statusEl.textContent = isRemote ? 'Connected to ' + target : 'Connected';
    statusEl.className = 'term-status term-status-ok';
    reconnBtn.style.display = 'none';
    termSendResize(term.cols, term.rows);
    termStartPing(termWs);
    term.focus();
  };
  termWs.onmessage = function(event) {
    if (event.data instanceof ArrayBuffer) {
      if (termIsPong(event.data)) {
        termLatency = termDecodePong(event.data);
        termUpdateStatusLatency(statusEl, termLatency);
        return;
      }
      if (!gotFirstData) { term.clear(); gotFirstData = true; }
      var bytes = new Uint8Array(event.data);
      if (termLocalEcho) {
        var filtered = termFilterEcho(bytes);
        if (filtered && filtered.length > 0) term.write(filtered);
      } else {
        term.write(bytes);
      }
    } else {
      if (!gotFirstData) { term.clear(); gotFirstData = true; }
      if (termLocalEcho) {
        var filtered = termFilterEcho(event.data);
        if (filtered && filtered.length > 0) term.write(filtered);
      } else {
        term.write(event.data);
      }
    }
  };
  termWs.onclose = function() {
    termWs = null;
    termStopPing();
    var quickClose = openedAt && (Date.now() - openedAt) < 3000;
    if (isRemote && quickClose) {
      statusEl.textContent = 'Node unreachable';
      statusEl.className = 'term-status term-status-off';
      reconnBtn.style.display = '';
    } else if (isRemote) {
      term.writeln('\r\n\x1b[31m[Remote node disconnected]\x1b[0m');
      statusEl.textContent = 'Disconnected';
      statusEl.className = 'term-status term-status-off';
      reconnBtn.style.display = '';
    } else {
      var delay = termGetReconnectDelay();
      term.writeln('\r\n\x1b[31m[Connection closed — reconnecting in ' + (delay / 1000).toFixed(0) + 's…]\x1b[0m');
      statusEl.textContent = 'Reconnecting…';
      statusEl.className = 'term-status term-status-off';
      reconnBtn.style.display = '';
      termReconnectTimer = setTimeout(function() {
        if (termMode === 'terminal') termConnectWs(target, session);
      }, delay);
    }
  };
  termWs.onerror = function() {
    if (isRemote) {
      statusEl.textContent = 'Failed to reach ' + target;
    } else {
      statusEl.textContent = 'Error';
    }
    statusEl.className = 'term-status term-status-off';
    reconnBtn.style.display = '';
  };
}

// ===== SINGLE: LOGS =====

function termConnectLogs() {
  termCloseWs();
  termStopPing();
  var reconnBtn = document.getElementById('term-reconnect');
  var statusEl = document.getElementById('term-status');
  var unit = document.getElementById('term-log-unit').value;
  var target = document.getElementById('term-target').value;
  if (target === '__custom__') target = '';
  var isRemote = !!target;
  var params = new URLSearchParams();
  if (unit) params.set('unit', unit);
  if (target) params.set('target', target);
  params.set('lines', '200');

  var wsUrl = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.hostname + ':' + TERM_PORT + '/ws/logs';
  if (params.toString()) wsUrl += '?' + params;

  statusEl.textContent = 'Connecting…';
  statusEl.className = 'term-status term-status-connecting';
  reconnBtn.style.display = 'none';
  term.clear();
  if (isRemote) {
    term.writeln('\r\n\x1b[33m  Connecting to ' + termNodeLabel(target) + '…\x1b[0m\r\n');
  }

  var openedAt = 0;
  termWs = new WebSocket(wsUrl);
  termWs.onopen = function() {
    openedAt = Date.now();
    termReconnectAttempt = 0;
    term.clear();
    var label = 'Streaming' + (unit ? ' — ' + unit : '');
    if (isRemote) label += ' on ' + target;
    statusEl.textContent = label;
    statusEl.className = 'term-status term-status-ok';
    reconnBtn.style.display = 'none';
  };
  termWs.onmessage = function(event) { term.write(event.data); };
  termWs.onclose = function() {
    termWs = null;
    var quickClose = openedAt && (Date.now() - openedAt) < 3000;
    if (isRemote && quickClose) {
      statusEl.textContent = 'Node unreachable';
      statusEl.className = 'term-status term-status-off';
      reconnBtn.style.display = '';
    } else if (isRemote) {
      term.writeln('\r\n\x1b[31m[Remote node disconnected]\x1b[0m');
      statusEl.textContent = 'Disconnected';
      statusEl.className = 'term-status term-status-off';
      reconnBtn.style.display = '';
    } else {
      var delay = termGetReconnectDelay();
      term.writeln('\r\n\x1b[31m[Stream ended — reconnecting in ' + (delay / 1000).toFixed(0) + 's…]\x1b[0m');
      statusEl.textContent = 'Reconnecting…';
      statusEl.className = 'term-status term-status-off';
      reconnBtn.style.display = '';
      termReconnectTimer = setTimeout(function() {
        if (termMode === 'logs') termConnectLogs();
      }, delay);
    }
  };
  termWs.onerror = function() {
    if (isRemote) {
      statusEl.textContent = 'Failed to reach ' + target;
    } else {
      statusEl.textContent = 'Error';
    }
    statusEl.className = 'term-status term-status-off';
    reconnBtn.style.display = '';
  };
}

function termSendResize(cols, rows) {
  if (termWs && termWs.readyState === 1) {
    var buf = new ArrayBuffer(5);
    var view = new DataView(buf);
    view.setUint8(0, 1);
    view.setUint16(1, cols);
    view.setUint16(3, rows);
    termWs.send(buf);
  }
}

// ===== MULTI PANE =====

function termMultiAddPane(target, label, user, password) {
  if (_authRequired && !_authenticated) {
    authShowLogin(function() { termMultiAddPane(target, label, user, password); });
    return;
  }
  var id = termMultiNextId++;
  var paneEl = document.createElement('div');
  paneEl.className = 'term-multi-pane' + (termMultiSync ? ' sync-on' : '');
  paneEl.id = 'term-pane-wrap-' + id;
  paneEl.innerHTML =
    '<div class="term-multi-pane-bar">' +
      '<span class="term-multi-pane-label">' + escHtml(label) + '</span>' +
      '<span class="term-multi-pane-latency"></span>' +
      '<span class="term-multi-pane-status term-status-connecting">●</span>' +
      '<button class="term-multi-pane-close" data-pane="' + id + '">×</button>' +
    '</div>' +
    '<div class="term-multi-pane-term" id="term-pane-' + id + '"></div>';

  var grid = document.getElementById('term-multi-grid');
  var placeholder = document.getElementById('term-multi-placeholder');
  if (placeholder) placeholder.remove();
  grid.appendChild(paneEl);

  var t = new Terminal({
    cursorBlink: true, fontSize: 12,
    fontFamily: "'SF Mono','Fira Code','Cascadia Code','Menlo',monospace",
    scrollback: 5000, theme: TERM_THEME
  });
  var f = new FitAddon.FitAddon();
  t.loadAddon(f);
  t.open(document.getElementById('term-pane-' + id));
  f.fit();

  var pane = { id: id, target: target, label: label, term: t, fit: f, ws: null, el: paneEl };
  termMultiPanes.push(pane);

  var wsUrl = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.hostname + ':' + TERM_PORT + '/ws/terminal';
  if (target !== '__local__') {
    var params = new URLSearchParams();
    params.set('target', target);
    params.set('protocol', 'proxy');
    wsUrl += '?' + params;
  }
  var ws = new WebSocket(wsUrl);
  ws.binaryType = 'arraybuffer';
  pane.ws = ws;

  var statusEl = paneEl.querySelector('.term-multi-pane-status');
  var latencyEl = paneEl.querySelector('.term-multi-pane-latency');

  ws.onopen = function() {
    statusEl.className = 'term-multi-pane-status term-status-ok';
    var buf = new ArrayBuffer(5);
    var view = new DataView(buf);
    view.setUint8(0, 1);
    view.setUint16(1, t.cols);
    view.setUint16(3, t.rows);
    ws.send(buf);
    termPaneStartPing(pane);
    t.focus();
  };
  ws.onmessage = function(event) {
    if (event.data instanceof ArrayBuffer) {
      if (termIsPong(event.data)) {
        pane.latency = termDecodePong(event.data);
        if (latencyEl) latencyEl.textContent = pane.latency + 'ms';
        return;
      }
      t.write(new Uint8Array(event.data));
    } else {
      t.write(event.data);
    }
  };
  ws.onclose = function() {
    statusEl.className = 'term-multi-pane-status term-status-off';
    if (latencyEl) latencyEl.textContent = '';
    termPaneStopPing(pane);
    t.writeln('\r\n\x1b[31m[Disconnected]\x1b[0m');
  };
  ws.onerror = function() {
    statusEl.className = 'term-multi-pane-status term-status-off';
  };

  t.onData(function(data) {
    termMultiQueueInput(pane, data);
  });

  t.onResize(function(size) {
    if (pane.ws && pane.ws.readyState === 1) {
      var buf = new ArrayBuffer(5);
      var view = new DataView(buf);
      view.setUint8(0, 1);
      view.setUint16(1, size.cols);
      view.setUint16(3, size.rows);
      pane.ws.send(buf);
    }
  });

  paneEl.querySelector('.term-multi-pane-close').addEventListener('click', function() {
    termMultiRemovePane(id);
  });

  termMultiPopulateNodes();
  termMultiUpdateStatus();
  return pane;
}

function termMultiRemovePane(id) {
  var idx = -1;
  for (var i = 0; i < termMultiPanes.length; i++) {
    if (termMultiPanes[i].id === id) { idx = i; break; }
  }
  if (idx === -1) return;
  var pane = termMultiPanes[idx];
  termPaneStopPing(pane);
  if (pane._inputTimer) clearTimeout(pane._inputTimer);
  if (pane.ws) { pane.ws.onclose = null; pane.ws.close(); }
  pane.term.dispose();
  pane.el.remove();
  termMultiPanes.splice(idx, 1);
  termMultiPopulateNodes();
  termMultiUpdatePlaceholder();
  termMultiUpdateStatus();
}

function termMultiClearAll() {
  while (termMultiPanes.length) {
    var pane = termMultiPanes[0];
    termPaneStopPing(pane);
    if (pane._inputTimer) clearTimeout(pane._inputTimer);
    if (pane.ws) { pane.ws.onclose = null; pane.ws.close(); }
    pane.term.dispose();
    pane.el.remove();
    termMultiPanes.shift();
  }
  termMultiPopulateNodes();
  termMultiUpdatePlaceholder();
  termMultiUpdateStatus();
}

function termMultiFitAll() {
  termMultiPanes.forEach(function(p) {
    try { p.fit.fit(); } catch(e) {}
  });
}

function termMultiUpdatePlaceholder() {
  var grid = document.getElementById('term-multi-grid');
  if (!grid) return;
  var placeholder = document.getElementById('term-multi-placeholder');
  if (termMultiPanes.length === 0) {
    if (!placeholder) {
      var el = document.createElement('div');
      el.id = 'term-multi-placeholder';
      el.className = 'term-multi-placeholder';
      el.textContent = 'Select nodes or enter a host above to open terminal sessions. When Sync is active, keyboard input is broadcast to all terminals.';
      grid.appendChild(el);
    }
  } else {
    if (placeholder) placeholder.remove();
  }
}

function termMultiUpdateStatus() {
  var statusEl = document.getElementById('term-status');
  if (termMode !== 'multi') return;
  var n = termMultiPanes.length;
  if (n === 0) {
    statusEl.textContent = '';
    statusEl.className = 'term-status';
  } else {
    statusEl.textContent = n + ' pane' + (n > 1 ? 's' : '') + (termMultiSync ? ' · synced' : '');
    statusEl.className = 'term-status term-status-ok';
  }
}

function termMultiAddSelectedNode() {
  var sel = document.getElementById('term-multi-node');
  var ip = sel.value;
  if (!ip) return;
  if (ip === '__local__') {
    var label = (LOCAL_DATA && LOCAL_DATA.hostname) || 'This Node';
    termMultiAddPane('__local__', label);
    return;
  }
  var label = ip;
  if (DATA && DATA.nodes) {
    DATA.nodes.forEach(function(n) { if (n.ip === ip) label = (n.hostname || ip); });
  }
  termMultiAddPane(ip, label);
}

function termMultiAddAllNodes() {
  if (!DATA || !DATA.nodes) return;
  var existing = {};
  termMultiPanes.forEach(function(p) { existing[p.target] = true; });
  if (!existing['__local__']) {
    var label = (LOCAL_DATA && LOCAL_DATA.hostname) || 'This Node';
    termMultiAddPane('__local__', label);
  }
  DATA.nodes.forEach(function(n) {
    if (n.is_me || !n.ip || existing[n.ip]) return;
    termMultiAddPane(n.ip, n.hostname || n.ip);
  });
}

function termMultiAddCustomHost() {
  var hostEl = document.getElementById('term-multi-host');
  var host = hostEl.value.trim();
  if (!host) { hostEl.style.borderColor = '#b42318'; return; }
  var user = document.getElementById('term-multi-user').value.trim() || 'root';
  var pass = document.getElementById('term-multi-pass').value;
  if (!pass) { document.getElementById('term-multi-pass').style.borderColor = '#b42318'; return; }
  hostEl.style.borderColor = '';
  termMultiAddPane(host, host, user, pass);
  hostEl.value = '';
}
