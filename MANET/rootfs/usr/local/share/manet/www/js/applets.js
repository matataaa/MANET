// Applets tab — list, install, manage, open
var _appletsData = [];
var _appletsLoaded = false;

function appletsActivate() {
  if (!_appletsLoaded) {
    _appletsLoaded = true;
    document.getElementById('tab-applets').innerHTML =
      '<div class="applets-header">' +
        '<h2>Applets</h2>' +
        '<div>' +
          '<input type="file" id="applet-file-input" accept=".tar.gz,.tgz" style="display:none">' +
          '<button class="applet-upload-btn" id="applet-upload-btn">Upload Applet</button>' +
          '<span id="upload-status" class="upload-status"></span>' +
        '</div>' +
      '</div>' +
      '<div id="applet-list" class="applet-list"></div>';

    document.getElementById('applet-upload-btn').onclick = function() {
      document.getElementById('applet-file-input').click();
    };
    document.getElementById('applet-file-input').onchange = uploadApplet;
  }
  fetchApplets();
}

function fetchApplets() {
  fetch('/api/applets').then(function(r) { return r.json(); }).then(function(d) {
    _appletsData = d.applets || [];
    renderApplets();
  }).catch(function() {});
}

function renderApplets() {
  var container = document.getElementById('applet-list');
  if (!container) return;

  if (_appletsData.length === 0) {
    container.innerHTML = '<div class="applet-empty">No applets installed.<br>Upload a .tar.gz applet package to get started.</div>';
    return;
  }

  container.innerHTML = _appletsData.map(function(a) {
    var dotClass = a.status === 'running' ? 'running' : (a.status === 'failed' ? 'failed' : 'stopped');
    var enableLabel = a.enabled ? 'Disable' : 'Enable';
    var enableAction = a.enabled ? 'disable' : 'enable';

    var displayName = a.label || a.name.replace(/[-_]/g, ' ').replace(/\b\w/g, function(c) { return c.toUpperCase(); });
    return '<div class="applet-card" data-applet="' + esc(a.name) + '">' +
      '<div class="applet-card-header">' +
        '<div class="applet-card-info">' +
          '<div class="applet-card-name">' + esc(displayName) + '</div>' +
          '<div class="applet-card-desc">' + esc(a.description) + '</div>' +
          '<div class="applet-card-meta">' +
            '<span>v' + esc(a.version) + '</span>' +
            '<span>' + esc(a.author) + '</span>' +
            '<span>' + esc(a.type) + '</span>' +
            (a.pid ? '<span>PID ' + a.pid + '</span>' : '') +
          '</div>' +
        '</div>' +
        '<div class="applet-card-status">' +
          '<span class="applet-status-dot ' + dotClass + '"></span>' +
          '<span class="applet-status-label">' + esc(a.status) + '</span>' +
        '</div>' +
      '</div>' +
      '<div class="applet-card-actions">' +
        (a.has_frontend ? '<button class="primary" onclick="openApplet(\'' + esc(a.name) + '\')">Open</button>' : '') +
        (a.has_config ? '<button onclick="openAppletConfig(\'' + esc(a.name) + '\')">Config</button>' : '') +
        (a.has_backend ? '<button onclick="appletAction(\'' + esc(a.name) + '\',\'start\')">Start</button>' : '') +
        (a.has_backend ? '<button onclick="appletAction(\'' + esc(a.name) + '\',\'stop\')">Stop</button>' : '') +
        (a.has_backend ? '<button onclick="appletAction(\'' + esc(a.name) + '\',\'restart\')">Restart</button>' : '') +
        (a.has_backend ? '<button onclick="appletAction(\'' + esc(a.name) + '\',\'' + enableAction + '\')">' + enableLabel + '</button>' : '') +
        (a.has_backend ? '<button onclick="toggleAppletLogs(\'' + esc(a.name) + '\')">Logs</button>' : '') +
        '<button class="danger" onclick="uninstallApplet(\'' + esc(a.name) + '\')">Uninstall</button>' +
      '</div>' +
      '<pre class="applet-logs" id="logs-' + esc(a.name) + '"></pre>' +
    '</div>';
  }).join('');
}

function esc(s) {
  if (!s) return '';
  var d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

function appletDisplayName(name) {
  var a = _appletsData.find(function(x) { return x.name === name; });
  if (a && a.label) return a.label;
  return name.replace(/[-_]/g, ' ').replace(/\b\w/g, function(c) { return c.toUpperCase(); });
}

function appletAction(name, action) {
  authFetch('/api/applets/' + encodeURIComponent(name) + '/action', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({action: action})
  }).then(function(r) { return r.json(); }).then(function(d) {
    setTimeout(fetchApplets, 1000);
  }).catch(function() {});
}

function toggleAppletLogs(name) {
  var el = document.getElementById('logs-' + name);
  if (!el) return;
  if (el.classList.contains('visible')) {
    el.classList.remove('visible');
    return;
  }
  el.textContent = 'Loading...';
  el.classList.add('visible');
  fetch('/api/applets/' + encodeURIComponent(name) + '/logs?lines=80')
    .then(function(r) { return r.json(); })
    .then(function(d) { el.textContent = d.logs || 'No logs available.'; })
    .catch(function() { el.textContent = 'Failed to load logs.'; });
}

function openApplet(name) {
  var existing = document.querySelector('.applet-iframe-overlay');
  if (existing) existing.remove();
  var returnHash = window.location.hash || '#dashboard';
  var display = appletDisplayName(name);
  var overlay = document.createElement('div');
  overlay.className = 'applet-iframe-overlay';
  var isDirect = !!window.__meshApplet;
  overlay.innerHTML =
    (isDirect ? '' :
    '<div class="applet-iframe-bar">' +
      '<h3>' + esc(display) + '</h3>' +
      '<button class="applet-iframe-close" id="close-applet-overlay">Close</button>' +
    '</div>') +
    '<iframe src="/api/applets/' + encodeURIComponent(name) + '/frontend/"></iframe>';
  document.body.appendChild(overlay);
  window.location.hash = 'applets/' + encodeURIComponent(name);
  var closeBtn = document.getElementById('close-applet-overlay');
  if (closeBtn) closeBtn.onclick = function() {
    overlay.remove();
    window.location.hash = returnHash.replace('#', '');
  };
}

function openAppletConfig(name) {
  var existing = document.querySelector('.applet-iframe-overlay');
  if (existing) existing.remove();
  var returnHash = window.location.hash || '#dashboard';
  var display = appletDisplayName(name);
  var overlay = document.createElement('div');
  overlay.className = 'applet-iframe-overlay';
  overlay.innerHTML =
    '<div class="applet-iframe-bar">' +
      '<h3>' + esc(display) + ' — Config</h3>' +
      '<button class="applet-iframe-close" id="close-config-overlay">Close</button>' +
    '</div>' +
    '<iframe src="/api/applets/' + encodeURIComponent(name) + '/config-page"></iframe>';
  document.body.appendChild(overlay);
  window.location.hash = 'applets/' + encodeURIComponent(name) + '/config';
  document.getElementById('close-config-overlay').onclick = function() {
    overlay.remove();
    window.location.hash = returnHash.replace('#', '');
  };
}

function uninstallApplet(name) {
  if (!confirm('Uninstall applet "' + name + '"? This will stop the service and remove all files.')) return;
  authFetch('/api/applets/' + encodeURIComponent(name), {method: 'DELETE'})
    .then(function(r) { return r.json(); })
    .then(function(d) {
      if (d.ok) fetchApplets();
    })
    .catch(function() {});
}

function uploadApplet() {
  if (_authRequired && !_authenticated) {
    authShowLogin(uploadApplet);
    return;
  }
  var fileInput = document.getElementById('applet-file-input');
  var statusEl = document.getElementById('upload-status');
  if (!fileInput.files.length) return;

  var file = fileInput.files[0];
  statusEl.className = 'upload-status';
  statusEl.textContent = 'Uploading ' + file.name + '...';

  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/api/applets/install', true);
  xhr.setRequestHeader('Content-Type', 'application/gzip');

  xhr.onload = function() {
    if (xhr.status === 401) {
      statusEl.textContent = '';
      authShowLogin(uploadApplet);
      return;
    }
    try {
      var resp = JSON.parse(xhr.responseText);
      if (resp.ok) {
        statusEl.className = 'upload-status ok';
        statusEl.textContent = 'Installed: ' + resp.installed + ' v' + resp.version;
        setTimeout(fetchApplets, 1000);
      } else {
        statusEl.className = 'upload-status err';
        statusEl.textContent = resp.error || 'Install failed';
      }
    } catch(e) {
      statusEl.className = 'upload-status err';
      statusEl.textContent = 'Upload failed';
    }
    fileInput.value = '';
  };

  xhr.onerror = function() {
    statusEl.className = 'upload-status err';
    statusEl.textContent = 'Upload error';
    fileInput.value = '';
  };

  var reader = new FileReader();
  reader.onload = function() {
    xhr.send(reader.result);
  };
  reader.readAsArrayBuffer(file);
}
