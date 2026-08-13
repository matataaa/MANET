// Config tab: view/edit node mesh.conf (local or remote via direct fetch)
let configInitialized = false;
let configEditing = false;
let configData = null;
let configVoiceData = null;
let configTarget = null;

function configBaseUrl() {
  return configTarget ? '/api/peer/' + configTarget : '';
}

function configActivate() {
  const panel = document.getElementById('tab-config');
  if (!configInitialized) {
    panel.innerHTML =
      '<div class="cfg-target-bar">' +
        '<select id="cfg-target"></select>' +
      '</div>' +
      '<div id="cfg-content"><div class="loading-msg">Loading configuration...</div></div>';
    document.getElementById('cfg-target').addEventListener('change', function() {
      configTarget = this.value || null;
      configEditing = false;
      configData = null;
      configVoiceData = null;
      configFetch();
    });
    configInitialized = true;
  }
  configPopulateTargets();
  if (window._pendingConfigTarget) {
    var p = window._pendingConfigTarget;
    window._pendingConfigTarget = null;
    configTarget = p.ip;
    var sel = document.getElementById('cfg-target');
    if (sel) sel.value = p.ip;
  }
  configFetch();
}

function configPopulateTargets() {
  var sel = document.getElementById('cfg-target');
  if (!sel || !DATA || !DATA.nodes) return;
  var current = configTarget || '';
  sel.innerHTML = '<option value="">This Node' + (LOCAL_DATA ? ' (' + (LOCAL_DATA.hostname || LOCAL_DATA.ip || '') + ')' : '') + '</option>';
  DATA.nodes.forEach(function(n) {
    if (n.is_me || !n.ip) return;
    var opt = document.createElement('option');
    opt.value = n.ip;
    opt.textContent = (n.hostname || n.ip) + ' (' + n.ip + ')';
    sel.appendChild(opt);
  });
  sel.value = current;
}

async function configFetch() {
  try {
    var base = configBaseUrl();
    const adminR = await fetch(base + '/api/admin/status');
    configData = await adminR.json();
    try {
      const voiceR = await fetch(base + '/api/voice');
      configVoiceData = await voiceR.json();
    } catch(e) { configVoiceData = null; }
    configRender();
  } catch(e) {
    document.getElementById('cfg-content').innerHTML =
      '<div class="loading-msg">Failed to load config' + (configTarget ? ' from ' + escHtml(configTarget) : '') + '</div>';
  }
}

function configRender() {
  if (!configData) return;
  const panel = document.getElementById('cfg-content');
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
      { label: 'Full Hostname', computed: () => {
        if (configTarget && DATA && DATA.nodes) {
          var node = DATA.nodes.find(function(n) { return n.ip === configTarget; });
          return node ? (node.hostname || '--') : '--';
        }
        return (LOCAL_DATA && LOCAL_DATA.hostname) || '--';
      }},
    ]},
    { title: 'Mesh Settings', fields: [
      { label: 'Mesh SSID', key: 'mesh_ssid' },
      { label: 'Mesh Key', key: 'mesh_key', masked: true },
      { label: 'IPv4 Network', key: 'ipv4_network' },
      { label: 'Regulatory Domain', key: 'regulatory_domain' },
      { label: 'HaLow Bandwidth', key: 'halow_bw' },
      { label: 'Multicast Mode', key: 'multicast_mode', fmt: function(v) { return v === 'optimized' ? 'Optimized (IGMP)' : 'Flood (default)'; } },
    ]},
    { title: 'Services', fields: [
      { label: 'Battery Monitor', key: 'battery_monitor', yesno: true },
    ]},
    { title: 'Access Point', fields: [
      { label: 'EUD Mode', key: 'eud' },
      { label: 'AP SSID', key: 'lan_ap_ssid' },
      { label: 'AP Key', key: 'lan_ap_key', masked: true },
      { label: 'AP Channel', key: 'lan_ap_channel' },
      { label: 'AP Bandwidth', key: 'lan_ap_bw' },
      { label: 'Max EUDs/Node', key: 'max_euds_per_node' },
    ]},
    { title: 'Gateway', fields: [
      { label: 'Gateway Enabled', key: 'gateway', yesno: true },
      { label: 'NAT Masquerade', key: 'gateway_nat', yesno: true },
      { label: 'MSS Clamping', key: 'gateway_mss_clamp', yesno: true },
      { label: 'Bandwidth Advertisement', key: 'gateway_bandwidth' },
      { label: 'DNS Servers', key: 'dns_servers' },
    ]},
    { title: 'Access', fields: [
      { label: 'Admin Key', key: 'admin_password', masked: true },
    ]},
    { title: 'Voice', voice: true, fields: [
      { label: 'PTT Mode', voiceKey: 'ptt_mode' },
      { label: 'TX Channel', voiceKey: 'channel', fmt: function(v) { return 'Ch ' + (v || '1'); } },
      { label: 'Mic Volume', key: 'voice_mic_volume', fmt: function(v) { return (v || '80') + '%'; } },
      { label: 'Speaker Volume', key: 'voice_speaker_volume', fmt: function(v) { return (v || '80') + '%'; } },
      { label: 'Port', voiceKey: 'port', fallback: '4370' },
      { label: 'Interface', voiceKey: 'interface', fallback: 'br0' },
    ]},
  ];

  let html = '<div>';

  sections.forEach(sec => {
    html += '<div class="card cfg-section"><div class="cfg-section-title">' + sec.title + '</div>';
    sec.fields.forEach(f => {
      let val;
      if (f.voiceKey) {
        val = (configVoiceData && configVoiceData[f.voiceKey]) || f.fallback || '--';
      } else {
        val = f.computed ? f.computed() : (cfg[f.key] || '--');
      }
      let cls = 'cfg-value';
      if (f.masked && val !== '--') { val = '••••••••'; cls += ' masked'; }
      if (f.fmt) val = f.fmt(val);
      else if (f.yesno) val = val === 'y' ? 'Enabled' : 'Disabled';
      html += '<div class="cfg-row"><div class="cfg-label">' + f.label + '</div><div class="' + cls + '">' + escHtml(String(val)) + '</div></div>';
    });
    html += '</div>';
  });

  html += '<div id="qos-card" class="card cfg-section"><div class="card-header">QOS PRIORITY</div><div class="loading-msg" style="padding:12px">Loading...</div></div>';

  var hasPending = configData && configData.pending;
  if (hasPending) {
    html += '<div class="card cfg-section" style="border-color:var(--warn);background:rgba(245,158,11,.06)">' +
      '<div style="padding:12px;font-size:12px;color:var(--warn);font-weight:700">Fleet config is staged — activate or cancel it before editing locally</div></div>';
  }
  html += '<div style="padding:4px 0"><button class="cfg-btn cfg-btn-primary" id="cfg-edit-btn"' +
    (hasPending ? ' disabled style="opacity:.5;cursor:not-allowed"' : '') + '>Edit Configuration</button></div>';
  html += '</div>';
  panel.innerHTML = html;

  document.getElementById('cfg-edit-btn').addEventListener('click', () => {
    configEditing = true;
    configRender();
  });

  qosFetch();
}

function configRenderEdit(panel, cfg) {
  const fields = [
    { label: 'Node Hostname', key: 'node_hostname', type: 'text', hint: 'Prefix — full hostname: {this}-{ssid}-{mac}', preview: true },
    { label: 'EUD Mode', key: 'eud', type: 'select', options: ['wired', 'wireless', 'both', 'auto', 'none'] },
    { label: 'AP SSID', key: 'lan_ap_ssid', type: 'text' },
    { label: 'AP Key', key: 'lan_ap_key', type: 'password' },
    { label: 'AP Channel', key: 'lan_ap_channel', type: 'text', hint: 'e.g. 36 for 5GHz, 6 for 2.4GHz' },
    { label: 'AP Bandwidth', key: 'lan_ap_bw', type: 'select', options: ['20', '40', '80'] },
    { label: 'Max EUDs/Node', key: 'max_euds_per_node', type: 'text' },
    { label: 'Mesh SSID', key: 'mesh_ssid', type: 'text' },
    { label: 'Mesh Key', key: 'mesh_key', type: 'password' },
    { label: 'IPv4 Network', key: 'ipv4_network', type: 'text' },
    { label: 'Regulatory Domain', key: 'regulatory_domain', type: 'select', options: ['US', 'EU', 'JP', 'AU'] },
    { label: 'HaLow Bandwidth', key: 'halow_bw', type: 'select', options: [
      {v:'1MHz',l:'1 MHz'},{v:'2MHz',l:'2 MHz'},{v:'4MHz',l:'4 MHz'},{v:'8MHz',l:'8 MHz'}
    ], hint: 'Primary channel width for 802.11ah mesh' },
    { label: 'Multicast Mode', key: 'multicast_mode', type: 'select', options: [
      {v:'flood',l:'Flood (recommended ≤10 nodes)'},
      {v:'optimized',l:'Optimized IGMP (10+ nodes)'}
    ], hint: 'Flood sends all multicast to all peers; IGMP uses snooping for selective delivery' },
    { label: 'Battery Monitor', key: 'battery_monitor', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Admin Key', key: 'admin_password', type: 'password' },
    { section: 'Gateway' },
    { label: 'Gateway Enabled', key: 'gateway', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}], hint: 'Allow this node to act as a mesh gateway' },
    { label: 'NAT Masquerade', key: 'gateway_nat', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'MSS Clamping', key: 'gateway_mss_clamp', type: 'select', options: [{v:'y',l:'Yes'},{v:'n',l:'No'}] },
    { label: 'Bandwidth', key: 'gateway_bandwidth', type: 'select', options: [
      {v:'',l:'Auto (batman default)'},{v:'2M/2M',l:'2M/2M'},{v:'5M/5M',l:'5M/5M'},{v:'10M/10M',l:'10M/10M'},
      {v:'20M/20M',l:'20M/20M'},{v:'50M/50M',l:'50M/50M'},{v:'100M/100M',l:'100M/100M'}
    ] },
    { label: 'DNS Servers', key: 'dns_servers', type: 'text', hint: 'Comma-separated (e.g. 8.8.8.8,8.8.4.4)' },
    { section: 'Voice' },
    { label: 'PTT Mode', key: 'voice_ptt_mode', type: 'select', options: [
      {v:'openvlm',l:'OpenVLM HID'},{v:'gpio',l:'GPIO Button'},{v:'always',l:'Always On'},{v:'vox',l:'VOX (auto)'}
    ], voiceKey: 'ptt_mode', fallback: 'openvlm' },
    { label: 'TX Channel', key: 'voice_channel', type: 'select', options: (function() {
      var opts = []; for (var i = 1; i <= 21; i++) opts.push({v: String(i), l: 'Channel ' + i});
      return opts;
    })() },
    { label: 'Mic Volume', key: 'voice_mic_volume', type: 'range', min: 0, max: 100 },
    { label: 'Speaker Volume', key: 'voice_speaker_volume', type: 'range', min: 0, max: 100 },
    { label: 'Port', key: 'voice_port', type: 'text', voiceKey: 'port', fallback: '4370' },
    { label: 'Interface', key: 'voice_iface', type: 'text', voiceKey: 'interface', fallback: 'br0' },
  ];

  let html = '<div class="card"><div class="cfg-section-title">Edit Configuration' + (configTarget ? ' — ' + escHtml(configTarget) : '') + '</div>';
  fields.forEach(f => {
    if (f.section) {
      html += '</div><div class="card"><div class="cfg-section-title">' + f.section + '</div>';
      return;
    }
    const curVal = f.voiceKey ? ((configVoiceData && configVoiceData[f.voiceKey]) || f.fallback || '') : (cfg[f.key] || '');
    html += '<div class="cfg-row"><div class="cfg-label">' + f.label;
    if (f.hint) html += '<span class="hint">' + f.hint + '</span>';
    html += '</div>';
    if (f.type === 'select') {
      html += '<select class="cfg-input" id="cfg-f-' + f.key + '">';
      f.options.forEach(opt => {
        const val = typeof opt === 'object' ? opt.v : opt;
        const label = typeof opt === 'object' ? opt.l : opt;
        const sel = curVal === val ? ' selected' : '';
        html += '<option value="' + val + '"' + sel + '>' + label + '</option>';
      });
      html += '</select>';
    } else if (f.type === 'range') {
      var rangeVal = curVal || '80';
      html += '<div class="cfg-range-wrap"><input class="cfg-input cfg-range" type="range"' +
        ' min="' + (f.min || 0) + '" max="' + (f.max || 100) + '"' +
        ' id="cfg-f-' + f.key + '" value="' + escHtml(rangeVal) + '"' +
        ' oninput="document.getElementById(\'cfg-rv-' + f.key + '\').textContent=this.value+\'%\'">' +
        '<span class="cfg-range-val" id="cfg-rv-' + f.key + '">' + escHtml(rangeVal) + '%</span></div>';
    } else {
      html += '<input class="cfg-input" type="' + f.type + '" id="cfg-f-' + f.key + '" value="' + escHtml(curVal) + '">';
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
  var macSuffix = '????';
  if (configTarget && DATA && DATA.nodes) {
    var node = DATA.nodes.find(function(n) { return n.ip === configTarget; });
    if (node && node.hostname) macSuffix = node.hostname.split('-').pop();
  } else if (LOCAL_DATA && LOCAL_DATA.hostname) {
    macSuffix = LOCAL_DATA.hostname.split('-').pop();
  }
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
  const meshFields = ['node_hostname','eud','lan_ap_ssid','lan_ap_key','lan_ap_channel','lan_ap_bw','max_euds_per_node','mesh_ssid','mesh_key',
    'ipv4_network','regulatory_domain','halow_bw','multicast_mode','battery_monitor','admin_password',
    'gateway','gateway_nat','gateway_mss_clamp','gateway_bandwidth','dns_servers',
    'voice_mic_volume','voice_speaker_volume','voice_channel'];
  const config = {};
  meshFields.forEach(f => {
    const el = document.getElementById('cfg-f-' + f);
    if (el) config[f] = el.value;
  });

  var chVal = (document.getElementById('cfg-f-voice_channel') || {}).value || '1';
  var chAddr = '239.69.0.' + chVal;
  const voiceCfg = {
    action: 'configure',
    ptt_mode: (document.getElementById('cfg-f-voice_ptt_mode') || {}).value || 'openvlm',
    mcast_addr: chAddr,
    port: (document.getElementById('cfg-f-voice_port') || {}).value || '4370',
    interface: (document.getElementById('cfg-f-voice_iface') || {}).value || 'br0',
    mic_volume: (document.getElementById('cfg-f-voice_mic_volume') || {}).value || '',
    speaker_volume: (document.getElementById('cfg-f-voice_speaker_volume') || {}).value || '',
  };

  var base = configBaseUrl();
  try {
    const [meshR, voiceR] = await Promise.all([
      fetch(base + '/api/admin/save', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({config}) }),
      fetch(base + '/api/voice', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(voiceCfg) })
    ]);
    const meshResult = await meshR.json();
    const voiceResult = await voiceR.json();
    if (meshResult.ok && voiceResult.ok) {
      configEditing = false;
      configFetch();
    } else {
      const errors = [];
      if (!meshResult.ok) errors.push('Mesh: ' + (meshResult.error || 'unknown'));
      if (!voiceResult.ok) errors.push('Voice: ' + (voiceResult.error || 'unknown'));
      alert('Save failed: ' + errors.join(', '));
    }
  } catch(e) { alert('Save failed: ' + e.message); }
}

// QOS Priority
var qosData = null;

function qosFetch() {
  var base = configBaseUrl();
  fetch(base + '/api/qos').then(function(r) { return r.json(); }).then(function(d) {
    qosData = d;
    qosRender();
  }).catch(function() {});
}

function qosFmt(n) {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
  return '' + n;
}

function qosRender() {
  var card = document.getElementById('qos-card');
  if (!card || !qosData) return;

  var d = qosData;
  var bandColors = ['var(--good)', 'var(--warn)', 'var(--muted)'];
  var bandNames = d.band_names || ['High', 'Normal', 'Low'];

  var html = '<div class="card-header">QOS PRIORITY</div>';
  html += '<div class="qos-toggle-row">';
  html += '<span class="qos-toggle-label">Traffic Prioritization</span>';
  html += '<button class="qos-toggle' + (d.enabled ? ' on' : '') + '" id="qos-toggle"></button>';
  html += '</div>';

  html += '<div id="qos-body"' + (d.enabled ? '' : ' class="qos-disabled"') + '>';

  for (var b = 0; b < 3; b++) {
    var rules = (d.rules || []).filter(function(r) { return r.band === b; });
    var totalPkts = 0, totalBytes = 0;
    if (d.stats) {
      Object.keys(d.stats).forEach(function(iface) {
        var s = d.stats[iface];
        if (s && s[b]) {
          totalPkts += s[b].packets;
          totalBytes += s[b].bytes;
        }
      });
    }

    html += '<div class="qos-band-section">';
    html += '<div class="qos-band-header">';
    html += '<span class="qos-band-dot" style="background:' + bandColors[b] + '"></span>';
    html += '<span class="qos-band-name">' + bandNames[b] + '</span>';
    html += '<span class="qos-band-stats">' + qosFmt(totalPkts) + ' pkts / ' + qosFmt(totalBytes) + ' bytes</span>';
    html += '</div>';

    if (rules.length) {
      rules.forEach(function(r) {
        html += '<div class="qos-rule-row">';
        html += '<span class="qos-rule-name">' + escHtml(r.name) + '</span>';
        html += '<span class="qos-rule-port">:' + r.port + '</span>';
        html += '<select class="qos-rule-band" data-svc="' + escHtml(r.name) + '">';
        for (var i = 0; i < 3; i++) {
          html += '<option value="' + i + '"' + (r.band === i ? ' selected' : '') + '>' + bandNames[i] + '</option>';
        }
        html += '</select>';
        html += '</div>';
      });
    } else {
      html += '<div style="font-size:11px;color:var(--muted);padding:4px 8px">No assigned services</div>';
    }
    html += '</div>';
  }

  html += '</div>';
  card.innerHTML = html;

  var base = configBaseUrl();
  document.getElementById('qos-toggle').addEventListener('click', function() {
    fetch(base + '/api/qos', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({enabled: !d.enabled})
    }).then(function() { qosFetch(); });
  });

  card.querySelectorAll('.qos-rule-band').forEach(function(sel) {
    sel.addEventListener('change', function() {
      fetch(base + '/api/qos', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({service: sel.dataset.svc, band: parseInt(sel.value)})
      }).then(function() { qosFetch(); });
    });
  });
}
