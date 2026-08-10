// Perf tab: radio control, iperf measurements, ping
let perfInitialized = false;
let perfActiveSection = 'measure';
let perfPingAbort = null;
let perfIperfAbort = null;

function perfActivate() {
  const panel = document.getElementById('tab-perf');
  if (!perfInitialized) {
    panel.innerHTML = `
      <div class="perf-tabs">
        <button class="perf-tab active" data-section="measure">Measure</button>
        <button class="perf-tab" data-section="radio">Radio</button>
        <button class="perf-tab" data-section="ping">Ping</button>
      </div>
      <div class="perf-section active" id="perf-measure">
        <div class="card">
          <div class="card-header">IPERF3 MEASUREMENT</div>
          <div class="measure-form">
            <div><label>Server IP</label><input type="text" id="perf-server-ip" placeholder="10.30.2.x"></div>
            <div><label>Duration (s)</label><input type="number" id="perf-duration" value="30" min="1" max="300"></div>
            <div><label>Test Type</label>
              <select id="perf-test-type">
                <option value="tcp_1stream">TCP (1 stream)</option>
                <option value="udp_throughput">UDP throughput</option>
                <option value="udp_jitter">UDP jitter</option>
              </select>
            </div>
            <div><label>Bitrate (UDP)</label><input type="text" id="perf-bitrate" value="4M"></div>
          </div>
          <div class="measure-actions">
            <button class="cfg-btn cfg-btn-primary" id="perf-run-btn">Run Test</button>
            <button class="cfg-btn cfg-btn-danger" id="perf-stop-btn" style="display:none">Stop</button>
            <button class="cfg-btn" id="perf-server-btn" style="margin-left:8px">Start Server</button>
            <button class="cfg-btn cfg-btn-danger" id="perf-server-stop-btn" style="margin-left:8px">Stop Server</button>
          </div>
          <div class="measure-results" id="perf-results" style="display:none"></div>
        </div>
      </div>
      <div class="perf-section" id="perf-radio">
        <div class="card">
          <div class="card-header">RADIO INTERFACES</div>
          <div class="radio-grid" id="perf-radio-grid"><div class="loading-msg">Loading interfaces...</div></div>
        </div>
      </div>
      <div class="perf-section" id="perf-ping">
        <div class="card">
          <div class="card-header">PING TEST</div>
          <div class="measure-form">
            <div><label>Target IP</label><input type="text" id="ping-target" placeholder="10.30.2.x"></div>
            <div><label>Count</label><input type="number" id="ping-count" value="100" min="1" max="10000"></div>
            <div class="checkbox-row">
              <input type="checkbox" id="ping-continuous"><label for="ping-continuous" style="margin:0;font-size:12px">Continuous</label>
            </div>
          </div>
          <div class="measure-actions">
            <button class="cfg-btn cfg-btn-primary" id="ping-run-btn">Run Ping</button>
            <button class="cfg-btn cfg-btn-danger" id="ping-stop-btn" style="display:none">Stop</button>
          </div>
          <div class="measure-results" id="ping-results" style="display:none"></div>
        </div>
      </div>`;

    panel.querySelectorAll('.perf-tab').forEach(btn => {
      btn.addEventListener('click', () => {
        perfActiveSection = btn.dataset.section;
        panel.querySelectorAll('.perf-tab').forEach(b => b.classList.toggle('active', b === btn));
        panel.querySelectorAll('.perf-section').forEach(s => s.classList.toggle('active', s.id === 'perf-' + perfActiveSection));
        if (perfActiveSection === 'radio') perfLoadRadio();
      });
    });

    document.getElementById('perf-run-btn').addEventListener('click', perfRunIperf);
    document.getElementById('perf-stop-btn').addEventListener('click', perfStopIperf);
    document.getElementById('perf-server-btn').addEventListener('click', () => perfIperfServer('start'));
    document.getElementById('perf-server-stop-btn').addEventListener('click', () => perfIperfServer('stop'));
    document.getElementById('ping-run-btn').addEventListener('click', perfRunPing);
    document.getElementById('ping-stop-btn').addEventListener('click', perfStopPing);
    document.getElementById('ping-continuous').addEventListener('change', (e) => {
      document.getElementById('ping-count').disabled = e.target.checked;
    });

    perfInitialized = true;
  }

  if (perfActiveSection === 'radio') perfLoadRadio();
}

async function perfStreamTo(url, body, pre, abortKey) {
  const controller = new AbortController();
  if (abortKey === 'ping') perfPingAbort = controller;
  else perfIperfAbort = controller;

  try {
    const resp = await fetch(url, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(body),
      signal: controller.signal
    });
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      pre.textContent += decoder.decode(value, { stream: true });
      pre.scrollTop = pre.scrollHeight;
    }
  } catch(e) {
    if (e.name !== 'AbortError') {
      pre.textContent += '\nError: ' + e.message;
    }
  } finally {
    if (abortKey === 'ping') perfPingAbort = null;
    else perfIperfAbort = null;
  }
}

async function perfRunIperf() {
  const runBtn = document.getElementById('perf-run-btn');
  const stopBtn = document.getElementById('perf-stop-btn');
  const results = document.getElementById('perf-results');

  runBtn.style.display = 'none';
  stopBtn.style.display = '';
  results.style.display = 'block';
  results.innerHTML = '';
  const pre = document.createElement('pre');
  results.appendChild(pre);

  await perfStreamTo('/api/iperf/client/stream', {
    server_ip: document.getElementById('perf-server-ip').value,
    test_type: document.getElementById('perf-test-type').value,
    duration: parseInt(document.getElementById('perf-duration').value) || 30,
    bitrate: document.getElementById('perf-bitrate').value,
  }, pre, 'iperf');

  runBtn.style.display = '';
  stopBtn.style.display = 'none';
}

async function perfStopIperf() {
  if (perfIperfAbort) perfIperfAbort.abort();
  try { await fetch('/api/iperf/stop', {method: 'POST'}); } catch(e) {}
  document.getElementById('perf-run-btn').style.display = '';
  document.getElementById('perf-stop-btn').style.display = 'none';
}

async function perfIperfServer(action) {
  try {
    await fetch('/api/iperf/server/' + action, {method: 'POST'});
  } catch(e) {
    alert('Failed: ' + e.message);
  }
}

async function perfRunPing() {
  const runBtn = document.getElementById('ping-run-btn');
  const stopBtn = document.getElementById('ping-stop-btn');
  const results = document.getElementById('ping-results');
  const continuous = document.getElementById('ping-continuous').checked;
  const count = continuous ? 0 : (parseInt(document.getElementById('ping-count').value) || 100);

  runBtn.style.display = 'none';
  stopBtn.style.display = '';
  results.style.display = 'block';
  results.innerHTML = '';
  const pre = document.createElement('pre');
  results.appendChild(pre);

  await perfStreamTo('/api/ping/stream', {
    target: document.getElementById('ping-target').value,
    count: count,
    continuous: continuous,
  }, pre, 'ping');

  runBtn.style.display = '';
  stopBtn.style.display = 'none';
}

async function perfStopPing() {
  if (perfPingAbort) perfPingAbort.abort();
  try { await fetch('/api/ping/stop', {method: 'POST'}); } catch(e) {}
  document.getElementById('ping-run-btn').style.display = '';
  document.getElementById('ping-stop-btn').style.display = 'none';
}

async function perfLoadRadio() {
  const grid = document.getElementById('perf-radio-grid');
  if (!LOCAL_DATA || !LOCAL_DATA.interfaces) {
    grid.innerHTML = '<div class="loading-msg">No interface data</div>';
    return;
  }
  const radios = LOCAL_DATA.interfaces.filter(i => i.role === 'mesh' || i.role === 'ap');
  if (!radios.length) {
    grid.innerHTML = '<div class="loading-msg">No radio interfaces found</div>';
    return;
  }
  grid.innerHTML = radios.map(iface => {
    const stateClass = iface.state === 'UP' ? 'iface-state-up' : 'iface-state-down';
    let details = '';
    if (iface.detail) details += '<div class="radio-iface-detail">' + escHtml(iface.detail) + '</div>';
    if (iface.channel) details += '<div class="radio-iface-detail">Channel: ' + escHtml(iface.channel) + (iface.halow_bw ? ' (' + iface.halow_bw + ')' : '') + '</div>';
    if (iface.txpower_dbm) details += '<div class="radio-iface-detail">TX Power: ' + escHtml(iface.txpower_dbm) + ' dBm</div>';
    if (iface.addrs && iface.addrs.length) details += '<div class="radio-iface-detail" style="color:#60b8d4">' + iface.addrs.join(', ') + '</div>';
    iface.faults.forEach(f => { details += '<div style="font-size:10px;color:var(--bad)">⚠ ' + escHtml(f) + '</div>'; });

    return '<div class="radio-iface-card">' +
      '<div class="radio-iface-name">' + escHtml(iface.name) + ' <span class="' + stateClass + '" style="font-size:10px">' + iface.state + '</span></div>' +
      details +
      '</div>';
  }).join('');
}
