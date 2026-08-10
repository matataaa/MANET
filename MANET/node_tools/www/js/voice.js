// Voice tab: mesh-voice PTT service status
let voiceInitialized = false;

function voiceActivate() {
  const panel = document.getElementById('tab-voice');
  if (!voiceInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading voice status...</div>';
    voiceInitialized = true;
  }
  voiceFetch();
}

async function voiceFetch() {
  try {
    const r = await fetch('/api/voice');
    const data = await r.json();
    voiceRender(data);
  } catch(e) {
    document.getElementById('tab-voice').innerHTML = '<div class="loading-msg">Failed to load voice status</div>';
  }
}

function voiceRender(data) {
  const panel = document.getElementById('tab-voice');
  const active = data.active;
  const dotCls = active ? 'on' : 'off';
  const statusText = active ? 'Active' : 'Inactive';

  let html = '<div class="voice-grid">';

  html += '<div class="card voice-status-card">';
  html += '<div class="card-header">MESH VOICE SERVICE</div>';
  html += '<div class="status-row"><div class="status-label">Status</div><div class="status-value"><span class="voice-active-dot ' + dotCls + '"></span>' + statusText + '</div></div>';

  if (active) {
    const fields = [
      { label: 'Uptime', value: data.uptime || '--' },
      { label: 'PTT Mode', value: data.ptt_mode || '--' },
      { label: 'Multicast Addr', value: data.mcast_addr || '--' },
      { label: 'Port', value: data.port || '--' },
      { label: 'Interface', value: data.interface || '--' },
    ];
    fields.forEach(f => {
      html += '<div class="status-row"><div class="status-label">' + f.label + '</div><div class="status-value">' + escHtml(f.value) + '</div></div>';
    });
  } else {
    html += '<div class="status-row"><div class="status-value" style="color:var(--muted)">The mesh-voice service is not running. Start it with: systemctl start mesh-voice</div></div>';
  }

  html += '</div>';

  html += '<div class="card voice-status-card">';
  html += '<div class="card-header">ABOUT</div>';
  html += '<div class="status-row"><div class="status-value" style="font-size:11px;color:var(--muted)">' +
    'Mesh Voice provides push-to-talk voice communication over the mesh network using Opus codec over UDP multicast. ' +
    'PTT modes: <strong>always</strong> (always transmitting), <strong>gpio</strong> (hardware button), <strong>hid</strong> (OpenVLM HID device).' +
    '</div></div>';
  html += '</div>';

  html += '</div>';
  panel.innerHTML = html;
}
