let svcInitialized = false;
let svcData = null;

function servicesActivate() {
  const panel = document.getElementById('tab-services');
  if (!svcInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading services...</div>';
    svcInitialized = true;
  }
  svcFetch();
}

function servicesDeactivate() {
}

async function svcFetch() {
  try {
    const [svcR, appR] = await Promise.all([fetch('/api/services'), fetch('/api/applets')]);
    svcData = await svcR.json();
    var appData = await appR.json();
    svcData.applets = (appData.applets || []).map(function(a) {
      return {
        id: 'applet-' + a.name, name: a.label || a.name, description: a.description || '',
        category: 'applet', unit: a.service || '', status: a.status || 'stopped',
        installed: a.installed, enabled: a.enabled, pid: a.pid || null,
        started_at: a.started_at || '', actions: ['start', 'stop', 'restart'],
        _applet: a.name
      };
    });
    svcRender();
  } catch(e) {
    document.getElementById('tab-services').innerHTML =
      '<div class="loading-msg">Failed to load services</div>';
  }
}

function svcRender() {
  if (!svcData || !svcData.services) return;
  const panel = document.getElementById('tab-services');
  const services = svcData.services;

  const running = services.filter(s => s.status === 'running').length;
  const installed = services.filter(s => s.installed).length;

  const allServices = services.concat(svcData.applets || []);

  const categories = [
    { id: 'core', label: 'Core Mesh' },
    { id: 'network', label: 'Network' },
    { id: 'radio', label: 'Radio' },
    { id: 'application', label: 'Applications' },
    { id: 'system', label: 'System' },
    { id: 'applet', label: 'Applets' },
  ];

  let html = '<div class="svc-refresh-bar">';
  html += '<button class="svc-refresh-btn" id="svc-refresh">Refresh</button>';
  html += '<span class="svc-count">' + running + '/' + installed + ' running</span>';
  html += '<button class="svc-reboot-btn" id="svc-reboot">Reboot Node</button>';
  html += '</div>';
  html += '<div class="svc-grid">';

  categories.forEach(cat => {
    const catServices = allServices.filter(s => s.category === cat.id);
    if (!catServices.length) return;

    html += '<div class="svc-category-title">' + cat.label + '</div>';
    catServices.forEach(svc => {
      const dotCls = 'svc-dot-' + svc.status;
      const statusCls = 'svc-status-' + svc.status;
      const cardCls = svc.installed ? '' : ' not-installed';

      html += '<div class="svc-card' + cardCls + '">';
      html += '<div class="svc-card-head">';
      html += '<div class="svc-dot ' + dotCls + '"></div>';
      html += '<div class="svc-name">' + escHtml(svc.name) + '</div>';
      html += '<span class="svc-status-pill ' + statusCls + '">' + svc.status + '</span>';
      html += '</div>';
      html += '<div class="svc-desc">' + escHtml(svc.description) + '</div>';

      const meta = [];
      if (svc.unit) meta.push('unit: ' + svc.unit);
      if (svc.pid) meta.push('pid: ' + svc.pid);
      if (svc.enabled) meta.push('enabled');
      if (!svc.installed) meta.push('not installed');
      if (svc.started_at && svc.status === 'running') meta.push('since ' + svc.started_at.replace(/^.*? /, ''));
      if (meta.length) {
        html += '<div class="svc-meta">' + meta.map(m => '<span>' + escHtml(m) + '</span>').join('') + '</div>';
      }

      if (svc.installed && svc.actions.length) {
        html += '<div class="svc-actions">';
        var appletAttr = svc._applet ? ' data-applet="' + escHtml(svc._applet) + '"' : '';
        svc.actions.forEach(action => {
          let btnCls = 'svc-btn';
          if (action === 'restart' || action === 'reload') btnCls += ' svc-btn-restart';
          else if (action === 'stop') btnCls += ' svc-btn-stop';
          else if (action === 'start') btnCls += ' svc-btn-start';
          const label = action.charAt(0).toUpperCase() + action.slice(1);
          html += '<button class="' + btnCls + '" data-svc="' + svc.id + '" data-action="' + action + '"' + appletAttr + '>' + label + '</button>';
        });
        html += '</div>';
      }

      html += '</div>';
    });
  });

  html += '</div>';
  panel.innerHTML = html;

  document.getElementById('svc-refresh').addEventListener('click', svcFetch);
  document.getElementById('svc-reboot').addEventListener('click', svcReboot);
  panel.querySelectorAll('.svc-btn[data-svc]').forEach(btn => {
    btn.addEventListener('click', () => svcDoAction(btn));
  });
}

async function svcReboot() {
  if (!confirm('Reboot this node? You will lose connection until it comes back up.')) return;
  try {
    await authFetch('/api/terminal/reboot', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
    document.getElementById('tab-services').innerHTML =
      '<div class="loading-msg" style="padding:40px;text-align:center">' +
      '<div style="font-size:16px;font-weight:700;margin-bottom:8px">Rebooting...</div>' +
      '<div style="color:var(--muted)">The node is restarting. This page will reconnect automatically.</div></div>';
    svcInitialized = false;
    setTimeout(function poll() {
      fetch('/api/data').then(function() { location.reload(); }).catch(function() { setTimeout(poll, 3000); });
    }, 8000);
  } catch(e) { notify('Reboot Failed', e.message, {type:'error'}); }
}

async function svcDoAction(btn) {
  const svcId = btn.dataset.svc;
  const action = btn.dataset.action;
  const applet = btn.dataset.applet;
  btn.disabled = true;
  btn.textContent = action + 'ing...';
  try {
    const url = applet ? '/api/applets/' + applet : '/api/services/' + svcId;
    const r = await authFetch(url, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({action}),
    });
    const result = await r.json();
    if (!result.ok) notify('Action Failed', action + ': ' + (result.error || 'unknown'), {type:'error'});
  } catch(e) {
    notify('Action Failed', action + ': ' + e.message, {type:'error'});
  }
  setTimeout(svcFetch, 800);
}
