var termInitialized = false;
var term = null;
var termWs = null;
var termFit = null;
var termSessions = {};
var termCurrentTarget = '';
var termMode = localStorage.getItem('termMode') || 'terminal';
var termReconnectTimer = null;
var TERM_PORT = location.port || (location.protocol === 'https:' ? '443' : '80');

function terminalActivate() {
  var panel = document.getElementById('tab-terminal');
  if (!termInitialized) {
    panel.innerHTML =
      '<div class="term-wrap">' +
        '<div class="term-bar">' +
          '<div class="term-mode-toggle">' +
            '<button class="term-mode-btn active" id="term-mode-shell" data-mode="terminal">Shell</button>' +
            '<button class="term-mode-btn" id="term-mode-logs" data-mode="logs">Logs</button>' +
          '</div>' +
          '<select id="term-target" class="term-select">' +
            '<option value="">This Node</option>' +
          '</select>' +
          '<div id="term-cred-area"></div>' +
          '<div id="term-log-controls" style="display:none">' +
            '<select id="term-log-unit" class="term-select">' +
              '<option value="">All Logs</option>' +
            '</select>' +
          '</div>' +
          '<span id="term-status" class="term-status"></span>' +
          '<button class="cfg-btn cfg-btn-danger" id="term-reconnect" style="margin-left:auto;display:none">Reconnect</button>' +
        '</div>' +
        '<div id="term-container" class="term-container"></div>' +
      '</div>';

    term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'SF Mono','Fira Code','Cascadia Code','Menlo',monospace",
      scrollback: 10000,
      theme: {
        background: '#0d1117',
        foreground: '#c9d1d9',
        cursor: '#c9d1d9',
        selectionBackground: '#264f78',
        black: '#484f58',
        red: '#ff7b72',
        green: '#3fb950',
        yellow: '#d29922',
        blue: '#58a6ff',
        magenta: '#bc8cff',
        cyan: '#39d353',
        white: '#c9d1d9',
        brightBlack: '#6e7681',
        brightRed: '#ffa198',
        brightGreen: '#56d364',
        brightYellow: '#e3b341',
        brightBlue: '#79c0ff',
        brightMagenta: '#d2a8ff',
        brightCyan: '#56d364',
        brightWhite: '#f0f6fc'
      }
    });

    termFit = new FitAddon.FitAddon();
    term.loadAddon(termFit);
    term.open(document.getElementById('term-container'));
    termFit.fit();

    term.onData(function(data) {
      if (termWs && termWs.readyState === 1 && termMode === 'terminal') {
        termWs.send(data);
      }
    });

    term.onResize(function(size) {
      if (termMode === 'terminal') termSendResize(size.cols, size.rows);
    });

    window.addEventListener('resize', function() {
      if (termFit && document.getElementById('tab-terminal').classList.contains('active')) {
        termFit.fit();
      }
    });

    document.getElementById('term-target').addEventListener('change', termOnTargetChange);
    document.getElementById('term-reconnect').addEventListener('click', function() {
      if (termMode === 'logs') termConnectLogs();
      else termConnectWs();
    });

    document.getElementById('term-mode-shell').addEventListener('click', function() { termSetMode('terminal'); });
    document.getElementById('term-mode-logs').addEventListener('click', function() { termSetMode('logs'); });

    document.getElementById('term-log-unit').addEventListener('change', function() {
      localStorage.setItem('termLogUnit', this.value);
      termConnectLogs();
    });

    termInitialized = true;
    termPopulateTargets();
    termSetMode(termMode);
  } else {
    termPopulateTargets();
    termFit.fit();
    term.focus();
  }
}

function termSetMode(mode) {
  termMode = mode;
  localStorage.setItem('termMode', mode);
  if (termReconnectTimer) { clearTimeout(termReconnectTimer); termReconnectTimer = null; }
  document.getElementById('term-mode-shell').classList.toggle('active', mode === 'terminal');
  document.getElementById('term-mode-logs').classList.toggle('active', mode === 'logs');
  document.getElementById('term-target').style.display = mode === 'terminal' ? '' : 'none';
  document.getElementById('term-cred-area').style.display = mode === 'terminal' ? '' : 'none';
  document.getElementById('term-log-controls').style.display = mode === 'logs' ? 'flex' : 'none';

  if (termWs) { termWs.onclose = null; termWs.close(); termWs = null; }
  term.clear();

  if (mode === 'logs') {
    termPopulateLogUnits();
    termConnectLogs();
  } else {
    termConnectWs();
  }
}

function termPopulateLogUnits() {
  var sel = document.getElementById('term-log-unit');
  var current = sel.value || localStorage.getItem('termLogUnit') || '';
  sel.innerHTML = '<option value="">All Logs</option>';
  var units = [
    'manet-ctrl', 'node-manager', 'alfred',
    'gateway-route-manager', 'mesh-boot-lobby',
    'sae-watchdog', 'manet-txpower',
    'hostapd', 'wpa_supplicant', 'dnsmasq', 'avahi-daemon',
    'mesh-voice', 'mumble-server', 'mediamtx', 'syncthing',
    'gps-reader', 'battery-reader', 'cot-emitter', 'chronyd',
    'sshd', 'systemd-networkd'
  ];
  units.forEach(function(u) {
    var opt = document.createElement('option');
    opt.value = u;
    opt.textContent = u;
    sel.appendChild(opt);
  });
  sel.value = current;
}

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
  sel.value = current;
}

function termOnTargetChange() {
  var target = document.getElementById('term-target').value;
  var area = document.getElementById('term-cred-area');
  termCurrentTarget = target;

  if (!target) {
    area.innerHTML = '';
    termConnectWs();
    return;
  }

  var session = termSessions[target];
  if (session) {
    area.innerHTML =
      '<span class="term-proto-badge term-proto-' + session.protocol + '">' +
        session.protocol.toUpperCase() + '</span>' +
      '<button class="term-cred-btn term-cred-disconnect" id="term-disconnect">Disconnect</button>';
    document.getElementById('term-disconnect').addEventListener('click', function() {
      delete termSessions[target];
      termOnTargetChange();
    });
    termConnectWs(target, session);
  } else {
    termShowConnectForm(target);
  }
}

function termShowConnectForm(target) {
  var area = document.getElementById('term-cred-area');
  area.innerHTML =
    '<div class="term-cred-form">' +
      '<div class="term-proto-toggle">' +
        '<button class="term-proto-opt active" id="term-proto-ssh" data-proto="ssh">SSH</button>' +
      '</div>' +
      '<div id="term-ssh-fields">' +
        '<input type="text" id="term-cred-user" class="term-cred-input" placeholder="User" value="root">' +
        '<input type="password" id="term-cred-pass" class="term-cred-input" placeholder="Password">' +
      '</div>' +
      '<button class="term-cred-btn term-cred-connect" id="term-cred-go">Connect</button>' +
    '</div>';

  document.getElementById('term-cred-go').addEventListener('click', function() { termDoConnect(target); });
  document.getElementById('term-cred-pass').addEventListener('keydown', function(e) {
    if (e.key === 'Enter') termDoConnect(target);
  });
}

function termDoConnect(target) {
  var user = document.getElementById('term-cred-user').value.trim() || 'root';
  var pass = document.getElementById('term-cred-pass').value;
  if (!pass) {
    document.getElementById('term-cred-pass').style.borderColor = '#b42318';
    return;
  }
  termSessions[target] = {protocol: 'ssh', user: user, password: pass};
  termOnTargetChange();
}

function termConnectWs(target, session) {
  if (termWs) { termWs.onclose = null; termWs.close(); termWs = null; }

  var reconnBtn = document.getElementById('term-reconnect');
  var statusEl = document.getElementById('term-status');

  var params = new URLSearchParams();
  if (target && session) {
    params.set('target', target);
    params.set('protocol', session.protocol);
    if (session.user) params.set('user', session.user);
    if (session.password) params.set('password', session.password);
  }

  var wsUrl = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.hostname + ':' + TERM_PORT + '/ws/terminal';
  if (params.toString()) wsUrl += '?' + params;

  statusEl.textContent = 'Connecting...';
  statusEl.className = 'term-status term-status-connecting';
  reconnBtn.style.display = 'none';

  term.clear();
  termWs = new WebSocket(wsUrl);
  termWs.binaryType = 'arraybuffer';

  termWs.onopen = function() {
    statusEl.textContent = 'Connected';
    statusEl.className = 'term-status term-status-ok';
    reconnBtn.style.display = 'none';
    termSendResize(term.cols, term.rows);
    term.focus();
  };

  termWs.onmessage = function(event) {
    if (event.data instanceof ArrayBuffer) {
      term.write(new Uint8Array(event.data));
    } else {
      term.write(event.data);
    }
  };

  termWs.onclose = function() {
    term.writeln('\r\n\x1b[31m[Connection closed — reconnecting in 3s...]\x1b[0m');
    statusEl.textContent = 'Reconnecting...';
    statusEl.className = 'term-status term-status-off';
    reconnBtn.style.display = '';
    termWs = null;
    termReconnectTimer = setTimeout(function() {
      if (termMode === 'terminal') termConnectWs(target, session);
    }, 3000);
  };

  termWs.onerror = function() {
    statusEl.textContent = 'Error';
    statusEl.className = 'term-status term-status-off';
    reconnBtn.style.display = '';
  };
}

function termConnectLogs() {
  if (termWs) { termWs.onclose = null; termWs.close(); termWs = null; }

  var reconnBtn = document.getElementById('term-reconnect');
  var statusEl = document.getElementById('term-status');
  var unit = document.getElementById('term-log-unit').value;

  var params = new URLSearchParams();
  if (unit) params.set('unit', unit);
  params.set('lines', '200');

  var wsUrl = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.hostname + ':' + TERM_PORT + '/ws/logs';
  if (params.toString()) wsUrl += '?' + params;

  statusEl.textContent = 'Connecting...';
  statusEl.className = 'term-status term-status-connecting';
  reconnBtn.style.display = 'none';

  term.clear();
  termWs = new WebSocket(wsUrl);

  termWs.onopen = function() {
    statusEl.textContent = 'Streaming' + (unit ? ' — ' + unit : '');
    statusEl.className = 'term-status term-status-ok';
    reconnBtn.style.display = 'none';
  };

  termWs.onmessage = function(event) {
    term.write(event.data);
  };

  termWs.onclose = function() {
    term.writeln('\r\n\x1b[31m[Stream ended — reconnecting in 3s...]\x1b[0m');
    statusEl.textContent = 'Reconnecting...';
    statusEl.className = 'term-status term-status-off';
    reconnBtn.style.display = '';
    termWs = null;
    termReconnectTimer = setTimeout(function() {
      if (termMode === 'logs') termConnectLogs();
    }, 3000);
  };

  termWs.onerror = function() {
    statusEl.textContent = 'Error';
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
