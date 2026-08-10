// Config tab: view/edit this node's mesh.conf
let configInitialized = false;
let configEditing = false;
let configData = null;

function configActivate() {
  const panel = document.getElementById('tab-config');
  if (!configInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading configuration...</div>';
    configInitialized = true;
  }
  configFetch();
}

async function configFetch() {
  try {
    const r = await fetch('/api/admin/status');
    configData = await r.json();
    configRender();
  } catch(e) {
    document.getElementById('tab-config').innerHTML = '<div class="loading-msg">Failed to load config</div>';
  }
}

function configRender() {
  if (!configData) return;
  const panel = document.getElementById('tab-config');
  const cfg = configData.current_config || {};

  if (configEditing) {
    configRenderEdit(panel, cfg);
  } else {
    configRenderView(panel, cfg);
  }
}

function configRenderView(panel, cfg) {
  const sections = [
    { title: 'Node', fields: [
      { label: 'Hostname Prefix', key: 'node_hostname' },
      { label: 'Full Hostname', computed: () => (LOCAL_DATA && LOCAL_DATA.hostname) || '--' },
    ]},
    { title: 'Network', fields: [
      { label: 'Mesh SSID', key: 'mesh_ssid' },
      { label: 'Mesh Key', key: 'mesh_key', masked: true },
      { label: 'IPv4 Network', key: 'ipv4_network' },
      { label: 'Regulatory Domain', key: 'regulatory_domain' },
    ]},
    { title: 'Services', fields: [
      { label: 'ACS (Auto Channel)', key: 'acs', yesno: true },
      { label: 'MediaMTX', key: 'mtx', yesno: true },
      { label: 'Mumble Voice', key: 'mumble', yesno: true },
      { label: 'Auto Update', key: 'auto_update', yesno: true },
    ]},
    { title: 'Access Point', fields: [
      { label: 'EUD Mode', key: 'eud' },
      { label: 'AP SSID', key: 'lan_ap_ssid' },
      { label: 'AP Key', key: 'lan_ap_key', masked: true },
      { label: 'Max EUDs/Node', key: 'max_euds_per_node' },
    ]},
  ];

  let html = '<div>';

  sections.forEach(sec => {
    html += '<div class="card cfg-section"><div class="cfg-section-title">' + sec.title + '</div>';
    sec.fields.forEach(f => {
      let val = f.computed ? f.computed() : (cfg[f.key] || '--');
      let cls = 'cfg-value';
      if (f.masked && val !== '--') { val = '••••••••'; cls += ' masked'; }
      if (f.yesno) val = val === 'y' ? 'Enabled' : 'Disabled';
      html += '<div class="cfg-row"><div class="cfg-label">' + f.label + '</div><div class="' + cls + '">' + escHtml(val) + '</div></div>';
    });
    html += '</div>';
  });

  html += '<div style="padding:4px 0"><button class="cfg-btn cfg-btn-primary" id="cfg-edit-btn">Edit Configuration</button></div>';
  html += '</div>';
  panel.innerHTML = html;

  document.getElementById('cfg-edit-btn').addEventListener('click', () => {
    configEditing = true;
    configRender();
  });
}

function configRenderEdit(panel, cfg) {
  const fields = [
    { label: 'Node Hostname', key: 'node_hostname', type: 'text', hint: 'Prefix — full hostname: {this}-{ssid}-{mac}', preview: true },
    { label: 'EUD Mode', key: 'eud', type: 'select', options: ['wired', 'wireless', 'both', 'auto'] },
    { label: 'AP SSID', key: 'lan_ap_ssid', type: 'text' },
    { label: 'AP Key', key: 'lan_ap_key', type: 'password' },
    { label: 'Max EUDs/Node', key: 'max_euds_per_node', type: 'text' },
    { label: 'Mesh SSID', key: 'mesh_ssid', type: 'text' },
    { label: 'Mesh Key', key: 'mesh_key', type: 'password' },
    { label: 'IPv4 Network', key: 'ipv4_network', type: 'text' },
    { label: 'Regulatory Domain', key: 'regulatory_domain', type: 'select', options: ['US', 'EU', 'JP', 'AU'] },
    { label: 'ACS', key: 'acs', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'MediaMTX', key: 'mtx', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Mumble', key: 'mumble', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Auto Update', key: 'auto_update', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Admin Password', key: 'admin_password', type: 'password' },
  ];

  let html = '<div class="card"><div class="cfg-section-title">Edit Configuration</div>';
  fields.forEach(f => {
    html += '<div class="cfg-row"><div class="cfg-label">' + f.label;
    if (f.hint) html += '<span class="hint">' + f.hint + '</span>';
    html += '</div>';
    if (f.type === 'select') {
      html += '<select class="cfg-input" id="cfg-f-' + f.key + '">';
      f.options.forEach(opt => {
        const val = typeof opt === 'object' ? opt.v : opt;
        const label = typeof opt === 'object' ? opt.l : opt;
        const sel = cfg[f.key] === val ? ' selected' : '';
        html += '<option value="' + val + '"' + sel + '>' + label + '</option>';
      });
      html += '</select>';
    } else {
      html += '<input class="cfg-input" type="' + f.type + '" id="cfg-f-' + f.key + '" value="' + escHtml(cfg[f.key] || '') + '">';
      if (f.preview) html += '<div class="cfg-hostname-preview" id="cfg-hostname-preview"></div>';
    }
    html += '</div>';
  });
  html += '<div class="cfg-actions">';
  html += '<button class="cfg-btn cfg-btn-primary" id="cfg-save-btn">Save</button>';
  html += '<button class="cfg-btn" id="cfg-back-btn" style="margin-left:auto">Cancel</button>';
  html += '</div></div>';
  panel.innerHTML = html;

  document.getElementById('cfg-back-btn').addEventListener('click', () => {
    configEditing = false;
    configFetch();
  });
  document.getElementById('cfg-save-btn').addEventListener('click', configSave);

  // Live hostname preview
  const hostnameInput = document.getElementById('cfg-f-node_hostname');
  const ssidInput = document.getElementById('cfg-f-mesh_ssid');
  const preview = document.getElementById('cfg-hostname-preview');
  const macSuffix = (LOCAL_DATA && LOCAL_DATA.hostname) ? LOCAL_DATA.hostname.split('-').pop() : '????';
  function updateHostnamePreview() {
    const prefix = hostnameInput.value || '???';
    const ssid = ssidInput.value || '???';
    preview.textContent = prefix + '-' + ssid + '-' + macSuffix;
  }
  hostnameInput.addEventListener('input', updateHostnamePreview);
  ssidInput.addEventListener('input', updateHostnamePreview);
  updateHostnamePreview();
}

async function configSave() {
  const fields = ['node_hostname','eud','lan_ap_ssid','lan_ap_key','max_euds_per_node','mesh_ssid','mesh_key',
    'ipv4_network','regulatory_domain','acs','mtx','mumble','auto_update','admin_password'];
  const config = {};
  fields.forEach(f => {
    const el = document.getElementById('cfg-f-' + f);
    if (el) config[f] = el.value;
  });
  try {
    const r = await fetch('/api/admin/save', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({config})
    });
    const result = await r.json();
    if (result.ok) { configEditing = false; configFetch(); }
    else alert('Save failed: ' + (result.error || 'unknown'));
  } catch(e) { alert('Save failed: ' + e.message); }
}
