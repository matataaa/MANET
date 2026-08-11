// Hardware tab — radios, interfaces, GPS, system info
var hwInitialized = false;

function hardwareActivate() {
  var panel = document.getElementById('tab-hardware');
  if (!hwInitialized) {
    panel.innerHTML =
      '<div class="hw-wrap">' +
        '<div class="svc-bar"><button class="cfg-btn" id="hw-refresh">Refresh</button></div>' +
        '<h3 class="hw-section-title">RADIOS</h3>' +
        '<div id="hw-radios" class="svc-grid"></div>' +
        '<h3 class="hw-section-title">NETWORK INTERFACES</h3>' +
        '<div id="hw-ifaces" class="svc-grid"></div>' +
        '<h3 class="hw-section-title">GPS</h3>' +
        '<div id="hw-gps" class="svc-grid"></div>' +
        '<h3 class="hw-section-title">SYSTEM</h3>' +
        '<div id="hw-system" class="svc-grid"></div>' +
      '</div>';
    document.getElementById('hw-refresh').addEventListener('click', hardwareUpdate);
    hwInitialized = true;
  }
  hardwareUpdate();
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
                  r.driver === 'brcmfmac' ? 'Onboard WiFi (SDIO)' :
                  r.driver || 'Unknown';

    var isHalow = r.driver === 'morse_spi';
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
    var badge = i.state === 'UP' ? '<span class="hw-badge hw-badge-up">UP</span>' :
                i.state === 'UNKNOWN' ? '<span class="hw-badge hw-badge-unknown">UNKNOWN</span>' :
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

  var rows = '';
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

  var sysDot = (t && (t.undervoltage || t.throttled)) ? 'dot-bad' : (t && (t.was_undervoltage || t.was_throttled)) ? 'dot-warn' : 'dot-ok';
  el.innerHTML = '<div class="svc-card svc-card-wide">' +
    '<div class="svc-header"><span class="' + sysDot + '"></span><span class="svc-name">Node Info</span></div>' +
    '<div class="hw-details">' + rows + '</div></div>';
}
