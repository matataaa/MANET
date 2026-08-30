// Hardware tab — radios, interfaces, GPS, PTT, system info
var hwInitialized = false;
var hwPttTimer = null;
var hwPttData = null;
var hwUpdateStatus = null;

function hardwareActivate() {
  var panel = document.getElementById('tab-hardware');
  if (!hwInitialized) {
    panel.innerHTML =
      '<div class="hw-wrap">' +
        '<div class="svc-bar"><button class="cfg-btn" id="hw-refresh">Refresh</button></div>' +
        '<h3 class="hw-section-title">SYSTEM</h3>' +
        '<div class="svc-grid"><div id="hw-system" style="display:contents"></div><div id="hw-ptt" style="display:contents"></div></div>' +
        '<h3 class="hw-section-title">RADIOS</h3>' +
        '<div id="hw-radios" class="svc-grid"></div>' +
        '<h3 class="hw-section-title">NETWORK INTERFACES</h3>' +
        '<div id="hw-ifaces" class="svc-grid"></div>' +
        '<h3 class="hw-section-title">GPS</h3>' +
        '<div id="hw-gps" class="svc-grid"></div>' +
      '</div>';
    document.getElementById('hw-refresh').addEventListener('click', hardwareUpdate);
    hwInitialized = true;
  }
  hardwareUpdate();
  hwPttFetch();
  hwFetchUpdateStatus();
  hwPttTimer = setInterval(hwPttFetchLive, 1000);
}

async function hwFetchUpdateStatus() {
  try {
    var r = await fetch('/api/admin/update-status');
    hwUpdateStatus = await r.json();
    hwRenderSystem();
  } catch (e) {
    hwUpdateStatus = null;
  }
}

function hardwareDeactivate() {
  if (hwPttTimer) { clearInterval(hwPttTimer); hwPttTimer = null; }
}

function hardwareUpdate() {
  if (!LOCAL_DATA) return;
  hwRenderRadios();
  hwRenderIfaces();
  hwRenderGPS();
  hwRenderSystem();
}

function hwRow(label, value) {
  return '<div class="hw-row"><span class="hw-label">' + label + '</span><span class="hw-value">' + value + '</span></div>';
}

function hwRoleLabel(role) {
  var m = { mesh: 'Mesh Radio', ap: 'Access Point', bat: 'BATMAN Virtual', bridge: 'Bridge', gateway: 'Uplink Gateway', 'eud-bridge': 'EUD Bridge' };
  return m[role] || role;
}

function hwRenderRadios() {
  var el = document.getElementById('hw-radios');
  var radios = (LOCAL_DATA.interfaces || []).filter(function(i) {
    return i.role === 'mesh' || i.role === 'ap' || (i.driver && i.driver !== '');
  });
  if (!radios.length) { el.innerHTML = '<div class="svc-card"><div class="svc-name">No radios detected</div></div>'; return; }

  el.innerHTML = radios.map(function(r) {
    var dot = r.health === 'ok' ? 'dot-ok' : r.health === 'fault' ? 'dot-bad' : r.health === 'warn' ? 'dot-warn' : 'dot-info';
    var badge = r.state === 'UP' ? '<span class="hw-badge hw-badge-up">UP</span>' :
                r.state === 'DOWN' ? '<span class="hw-badge hw-badge-down">DOWN</span>' :
                '<span class="hw-badge hw-badge-unknown">' + escHtml(r.state || '?') + '</span>';

    var busType = r.driver === 'morse_spi' ? 'SPI (HaLow 802.11ah)' :
                  r.driver === 'morse_usb' ? 'USB (HaLow 802.11ah)' :
                  r.driver === 'brcmfmac' ? 'Onboard WiFi (SDIO)' :
                  r.driver || 'Unknown';

    var isHalow = r.driver === 'morse_spi' || r.driver === 'morse_usb';
    var rows = hwRow('Role', hwRoleLabel(r.role));
    if (r.driver) rows += hwRow('Driver', r.driver);
    rows += hwRow('Bus / Type', busType);
    if (r.channel) {
      var chLabel = r.channel;
      if (r.freq_mhz) {
        var freqVal = parseFloat(r.freq_mhz);
        chLabel += ' (' + (isHalow && freqVal > 10000 ? (freqVal / 1000).toFixed(1) + ' MHz' : r.freq_mhz + ' MHz') + ')';
      }
      rows += hwRow(isHalow ? 'S1G Channel' : 'Channel', chLabel);
    }
    if (r.width_mhz) rows += hwRow('Channel Width', r.width_mhz + ' MHz');
    if (r.halow_bw) rows += hwRow('Primary BW', r.halow_bw);
    if (r.txpower_dbm) {
      var txLabel = r.txpower_dbm + ' dBm';
      if (r.txpower_cap_dbm) txLabel += ' <span style="color:var(--muted)">(cap: ' + r.txpower_cap_dbm + ' dBm)</span>';
      rows += hwRow('TX Power', txLabel);
    }
    if (r.tx_mcs) rows += hwRow('TX MCS', r.tx_mcs);
    if (r.rx_mcs) rows += hwRow('RX MCS', r.rx_mcs);
    if (isHalow && r.halow_source) rows += hwRow('Data Source', r.halow_source === 'morse' ? 'morse_cli (live)' : 'wpa_supplicant config');
    if (r.addrs && r.addrs.length) rows += hwRow('Addresses', r.addrs.map(escHtml).join('<br>'));
    if (r.faults && r.faults.length) rows += hwRow('Faults', '<span style="color:var(--bad)">' + r.faults.map(escHtml).join(', ') + '</span>');

    return '<div class="svc-card">' +
      '<div class="svc-header"><span class="' + dot + '"></span><span class="svc-name">' + escHtml(r.name) + '</span>' + badge + '</div>' +
      '<div class="svc-desc">' + escHtml(r.detail) + '</div>' +
      '<div class="hw-details">' + rows + '</div></div>';
  }).join('');
}

function hwRenderIfaces() {
  var el = document.getElementById('hw-ifaces');
  var ifaces = (LOCAL_DATA.interfaces || []).filter(function(i) {
    return i.role !== 'mesh' && i.role !== 'ap' && (!i.driver || i.driver === '');
  });
  if (!ifaces.length) { el.innerHTML = ''; return; }

  el.innerHTML = ifaces.map(function(i) {
    var dot = i.health === 'ok' ? 'dot-ok' : i.health === 'fault' ? 'dot-bad' : i.health === 'warn' ? 'dot-warn' : 'dot-info';
    var badge = (i.state === 'UP' || i.state === 'ACTIVE') ? '<span class="hw-badge hw-badge-up">' + escHtml(i.state) + '</span>' :
                i.state === 'UNKNOWN' ? '<span class="hw-badge hw-badge-unknown">UNKNOWN</span>' :
                i.state === 'DEGRADED' ? '<span class="hw-badge hw-badge-warn">DEGRADED</span>' :
                '<span class="hw-badge hw-badge-down">' + escHtml(i.state || '?') + '</span>';
    var rows = hwRow('Role', hwRoleLabel(i.role));
    if (i.addrs && i.addrs.length) rows += hwRow('Addresses', i.addrs.map(escHtml).join('<br>'));

    return '<div class="svc-card">' +
      '<div class="svc-header"><span class="' + dot + '"></span><span class="svc-name">' + escHtml(i.name) + '</span>' + badge + '</div>' +
      '<div class="svc-desc">' + escHtml(i.detail) + '</div>' +
      '<div class="hw-details">' + rows + '</div></div>';
  }).join('');
}

function hwRenderGPS() {
  var el = document.getElementById('hw-gps');
  var gps = LOCAL_DATA.gps;
  var hasFix = gps && gps.available;
  var connected = gps && gps.connected;
  var dot = hasFix ? 'dot-ok' : connected ? 'dot-warn' : 'dot-bad';
  var badge = hasFix ? '<span class="hw-badge hw-badge-up">FIX</span>' :
              connected ? '<span class="hw-badge hw-badge-down">NO FIX</span>' :
              '<span class="hw-badge hw-badge-down">OFFLINE</span>';

  var sourceLabel = gps && gps.source === 'static' ? 'Static (fixed position)' :
                     gps && gps.source === 'receiver' ? 'Receiver (gpsd)' : '';

  var rows = '';
  if (sourceLabel) rows += hwRow('Source', sourceLabel);
  if (hasFix) {
    rows += hwRow('Latitude', gps.lat || '--');
    rows += hwRow('Longitude', gps.lon || '--');
    rows += hwRow('Altitude', gps.alt ? gps.alt + ' m' : '--');
  } else if (connected) {
    rows += hwRow('Status', 'Connected — searching for satellites');
  } else {
    rows += hwRow('Status', 'Device not connected');
  }

  el.innerHTML = '<div class="svc-card">' +
    '<div class="svc-header"><span class="' + dot + '"></span><span class="svc-name">GPS Receiver</span>' + badge + '</div>' +
    '<div class="svc-desc">Position tracking via gpsd</div>' +
    '<div class="hw-details">' + rows + '</div></div>';
}

function hwRenderSystem() {
  var el = document.getElementById('hw-system');
  var rows = '';
  rows += hwRow('Hostname', LOCAL_DATA.hostname || '--');
  if (hwUpdateStatus) {
    var sw = hwUpdateStatus.software, ov = hwUpdateStatus.overlay;
    var updateBadge = '<span class="hw-badge hw-badge-warn">Update available</span>';
    if (sw) rows += hwRow('MANET Version', 'v' + escHtml(sw.local || '--') + (sw.available ? ' ' + updateBadge : ''));
    if (ov) rows += hwRow('Kernel/Drivers Version', escHtml(ov.local || '--') + (ov.available ? ' ' + updateBadge : ''));
  }
  rows += hwRow('IP Address', LOCAL_DATA.ip || '--');
  rows += hwRow('MAC', '<span style="font-family:monospace">' + (LOCAL_DATA.mac || '--') + '</span>');
  rows += hwRow('Uptime', LOCAL_DATA.uptime || '--');
  rows += hwRow('EUD Mode', LOCAL_DATA.eud_mode || '--');
  rows += hwRow('AP SSID', LOCAL_DATA.ap_ssid || '--');
  rows += hwRow('Mesh SSID', LOCAL_DATA.mesh_ssid || '--');
  if (LOCAL_DATA.battery) {
    var pct = LOCAL_DATA.battery.percentage;
    var color = pct > 50 ? 'var(--good)' : pct > 20 ? 'var(--warn)' : 'var(--bad)';
    rows += hwRow('Battery', '<span style="color:' + color + '">' + pct + '%</span>' + (LOCAL_DATA.battery.status ? ' (' + LOCAL_DATA.battery.status + ')' : ''));
  } else {
    rows += hwRow('Battery', 'Not detected');
  }

  var t = LOCAL_DATA.throttle;
  if (t) {
    var flags = [];
    if (t.undervoltage) flags.push('<span style="color:var(--bad);font-weight:700">UNDERVOLTAGE NOW</span>');
    else if (t.was_undervoltage) flags.push('<span style="color:var(--warn)">Undervoltage since boot</span>');
    if (t.throttled) flags.push('<span style="color:var(--bad);font-weight:700">THROTTLED NOW</span>');
    else if (t.was_throttled) flags.push('<span style="color:var(--warn)">Throttled since boot</span>');
    if (t.freq_capped) flags.push('<span style="color:var(--bad)">Freq capped</span>');
    else if (t.was_freq_capped) flags.push('<span style="color:var(--warn)">Freq was capped</span>');
    if (t.soft_temp_limit) flags.push('<span style="color:var(--warn)">Soft temp limit</span>');
    else if (t.was_soft_temp_limit) flags.push('<span style="color:var(--muted)">Soft temp limit occurred</span>');
    if (flags.length) {
      rows += hwRow('Power/Thermal', flags.join(', '));
    } else {
      rows += hwRow('Power/Thermal', '<span style="color:var(--good)">OK</span>');
    }
    rows += hwRow('Throttle Raw', '<span style="font-family:monospace">' + escHtml(t.raw) + '</span>');
  }

  var sys = LOCAL_DATA.system;
  if (sys) {
    if (sys.cpu_temp != null) {
      var tc = sys.cpu_temp;
      var tColor = tc > 80 ? 'var(--bad)' : tc > 65 ? 'var(--warn)' : 'var(--good)';
      rows += hwRow('CPU Temp', '<span style="color:' + tColor + '">' + tc.toFixed(1) + ' &deg;C</span>');
    }
    if (sys.load_avg) {
      rows += hwRow('Load Avg', sys.load_avg[0].toFixed(2) + ' / ' + sys.load_avg[1].toFixed(2) + ' / ' + sys.load_avg[2].toFixed(2) + ' <span style="color:var(--muted)">(1/5/15 min)</span>');
    }
    if (sys.mem_total_kb) {
      var usedKb = sys.mem_total_kb - (sys.mem_avail_kb || sys.mem_free_kb || 0);
      var totalMb = (sys.mem_total_kb / 1024).toFixed(0);
      var usedMb = (usedKb / 1024).toFixed(0);
      var pct = ((usedKb / sys.mem_total_kb) * 100).toFixed(0);
      var mColor = pct > 90 ? 'var(--bad)' : pct > 75 ? 'var(--warn)' : 'var(--good)';
      rows += hwRow('Memory', '<span style="color:' + mColor + '">' + usedMb + ' / ' + totalMb + ' MB (' + pct + '%)</span>');
    }
  }

  var sysDot = (t && (t.undervoltage || t.throttled)) ? 'dot-bad' : (t && (t.was_undervoltage || t.was_throttled)) ? 'dot-warn' : 'dot-ok';
  el.innerHTML = '<div class="svc-card">' +
    '<div class="svc-header"><span class="' + sysDot + '"></span><span class="svc-name">Node Info</span></div>' +
    '<div class="hw-details">' + rows + '</div></div>';
}

// --- PTT hardware tile ---

async function hwPttFetch() {
  try {
    var r = await fetch('/api/voice');
    hwPttData = await r.json();
    hwRenderPtt();
  } catch(e) {
    var el = document.getElementById('hw-ptt');
    if (el) el.innerHTML = '<div class="svc-card"><div class="svc-name">Voice service unavailable</div></div>';
  }
}

async function hwPttFetchLive() {
  try {
    var r = await fetch('/api/voice');
    hwPttData = await r.json();
    hwUpdatePttIndicators();
  } catch(e) {}
}

function hwRenderPtt() {
  var el = document.getElementById('hw-ptt');
  if (!el || !hwPttData) return;
  var d = hwPttData;
  var active = d.active;
  var dot = active ? 'dot-ok' : 'dot-bad';
  var badge = active ? '<span class="hw-badge hw-badge-up">RUNNING</span>' :
              '<span class="hw-badge hw-badge-down">STOPPED</span>';

  var html = '<div class="svc-card">';
  html += '<div class="svc-header"><span class="' + dot + '"></span><span class="svc-name">Hardware PTT</span>' + badge + '</div>';

  // OpenVLM connection
  html += '<div class="voice-hw-device-row" id="hwp-vlm-row">';
  html += '<span class="voice-dot ' + (d.ptt_connected ? 'on' : 'off') + '" id="hwp-vlm-dot"></span>';
  html += '<span class="voice-hw-device-label">OpenVLM</span>';
  html += '<span class="voice-hw-device-status ' + (d.ptt_connected ? 'connected' : 'disconnected') + '" id="hwp-vlm-status">' + (d.ptt_connected ? 'Connected' : 'Disconnected') + '</span>';
  html += '</div>';

  // PTT state
  html += '<div class="voice-hw-ptt-wrap">';
  html += '<div class="voice-hw-ptt-indicator' + (active && d.ptt_active ? ' active' : '') + '" id="hwp-ptt-indicator">';
  html += '<div class="voice-hw-ptt-dot" id="hwp-ptt-dot"></div>';
  html += '<div class="voice-hw-ptt-label" id="hwp-ptt-label">' + (active ? (d.ptt_active ? 'PTT PRESSED' : 'PTT UNPRESSED') : 'SERVICE OFF') + '</div>';
  html += '</div>';
  html += '</div>';

  // TX/RX indicators
  html += '<div class="voice-indicators" id="hwp-txrx">';
  html += '<div class="voice-ind"><span class="voice-dot ' + (d.tx ? 'tx' : 'off') + '" id="hwp-tx-dot"></span><span id="hwp-tx-label">' + (d.tx ? 'TX' : 'TX Idle') + '</span></div>';
  html += '<div class="voice-ind"><span class="voice-dot ' + (d.rx ? 'rx' : 'off') + '" id="hwp-rx-dot"></span><span id="hwp-rx-label">' + (d.rx ? 'RX' : 'RX Silent') + '</span></div>';
  html += '</div>';

  // Service details
  if (active) {
    var details = '<div class="hw-details">';
    details += hwRow('PTT Mode', d.ptt_mode || '--');
    details += hwRow('Interface', d.interface || 'br0');
    details += hwRow('Uptime', d.uptime || '--');
    details += '</div>';
    html += details;
  }

  html += '</div>';
  el.innerHTML = html;
}

function hwUpdatePttIndicators() {
  if (!hwPttData) return;
  var d = hwPttData;

  // OpenVLM
  var vlmDot = document.getElementById('hwp-vlm-dot');
  var vlmStatus = document.getElementById('hwp-vlm-status');
  if (vlmDot) vlmDot.className = 'voice-dot ' + (d.ptt_connected ? 'on' : 'off');
  if (vlmStatus) {
    vlmStatus.textContent = d.ptt_connected ? 'Connected' : 'Disconnected';
    vlmStatus.className = 'voice-hw-device-status ' + (d.ptt_connected ? 'connected' : 'disconnected');
  }

  // PTT state
  var indicator = document.getElementById('hwp-ptt-indicator');
  var label = document.getElementById('hwp-ptt-label');
  if (indicator) {
    if (d.active && d.ptt_active) {
      indicator.className = 'voice-hw-ptt-indicator active';
      if (label) label.textContent = 'PTT PRESSED';
    } else if (d.active) {
      indicator.className = 'voice-hw-ptt-indicator';
      if (label) label.textContent = 'PTT UNPRESSED';
    } else {
      indicator.className = 'voice-hw-ptt-indicator';
      if (label) label.textContent = 'SERVICE OFF';
    }
  }

  // TX/RX
  var txDot = document.getElementById('hwp-tx-dot');
  var txLabel = document.getElementById('hwp-tx-label');
  var rxDot = document.getElementById('hwp-rx-dot');
  var rxLabel = document.getElementById('hwp-rx-label');
  if (txDot) txDot.className = 'voice-dot ' + (d.tx ? 'tx' : 'off');
  if (txLabel) txLabel.textContent = d.tx ? 'TX' : 'TX Idle';
  if (rxDot) rxDot.className = 'voice-dot ' + (d.rx ? 'rx' : 'off');
  if (rxLabel) rxLabel.textContent = d.rx ? 'RX' : 'RX Silent';
}
