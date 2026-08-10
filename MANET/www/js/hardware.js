// Hardware tab — radios, interfaces, GPS, system info
var hwInitialized = false;

function hardwareActivate() {
  var panel = document.getElementById('tab-hardware');
  if (!hwInitialized) {
    panel.innerHTML =
      '<div class="hw-wrap">' +
        '<div class="svc-bar"><button class="cfg-btn" id="hw-refresh">Refresh</button></div>' +
        '<h3 class="svc-cat-title">RADIOS</h3>' +
        '<div id="hw-radios" class="svc-grid"></div>' +
        '<h3 class="svc-cat-title">NETWORK INTERFACES</h3>' +
        '<div id="hw-ifaces" class="svc-grid"></div>' +
        '<h3 class="svc-cat-title">GPS</h3>' +
        '<div id="hw-gps" class="svc-grid"></div>' +
        '<h3 class="svc-cat-title">SYSTEM</h3>' +
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

    var rows = hwRow('Role', hwRoleLabel(r.role));
    if (r.driver) rows += hwRow('Driver', r.driver);
    rows += hwRow('Bus / Type', busType);
    if (r.channel) rows += hwRow('Channel', r.channel + (r.freq_mhz ? ' (' + r.freq_mhz + ' MHz)' : ''));
    if (r.halow_bw) rows += hwRow('Bandwidth', r.halow_bw);
    if (r.txpower_dbm) rows += hwRow('TX Power', r.txpower_dbm + ' dBm' + (r.txpower_cap_dbm ? ' (cap: ' + r.txpower_cap_dbm + ')' : ''));
    if (r.tx_mcs) rows += hwRow('TX MCS', r.tx_mcs);
    if (r.rx_mcs) rows += hwRow('RX MCS', r.rx_mcs);
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
    var stateUp = i.state === 'UP' || i.state === 'UNKNOWN';
    var badge = stateUp ? '<span class="hw-badge hw-badge-up">' + escHtml(i.state) + '</span>' :
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
  var dot = hasFix ? 'dot-ok' : 'dot-warn';
  var badge = hasFix ? '<span class="svc-badge svc-badge-running">FIX</span>' : '<span class="svc-badge svc-badge-stopped">NO FIX</span>';

  var rows = '';
  if (hasFix) {
    rows += hwRow('Latitude', gps.lat || '--');
    rows += hwRow('Longitude', gps.lon || '--');
    rows += hwRow('Altitude', gps.alt ? gps.alt + ' m' : '--');
  } else {
    rows += hwRow('Status', 'No GPS fix or device not connected');
  }

  el.innerHTML = '<div class="svc-card">' +
    '<div class="svc-header"><span class="' + dot + '"></span><span class="svc-name">GPS Receiver</span>' + badge + '</div>' +
    '<div class="svc-desc">Position tracking via serial NMEA</div>' +
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

  el.innerHTML = '<div class="svc-card svc-card-wide">' +
    '<div class="svc-header"><span class="dot-ok"></span><span class="svc-name">Node Info</span></div>' +
    '<div class="hw-details">' + rows + '</div></div>';
}
