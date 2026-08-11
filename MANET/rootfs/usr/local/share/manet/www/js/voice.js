let voiceInitialized = false;
let voiceData = null;
let voiceClient = null;
let voicePollTimer = null;
let voiceAudioDevices = { inputs: [], outputs: [] };
let voiceSelectedInput = '';
let voiceSelectedOutput = '';

function voiceActivate() {
  var panel = document.getElementById('tab-voice');
  if (!voiceInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading voice...</div>';
    voiceInitialized = true;
  }
  voiceEnumerateDevices();
  voiceFetch();
  voicePollTimer = setInterval(voiceFetchLive, 1000);
}

async function voiceEnumerateDevices() {
  if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) return;
  try {
    var devices = await navigator.mediaDevices.enumerateDevices();
    voiceAudioDevices.inputs = devices.filter(function(d) { return d.kind === 'audioinput'; });
    voiceAudioDevices.outputs = devices.filter(function(d) { return d.kind === 'audiooutput'; });
    if (voiceAudioDevices.inputs.length && !voiceAudioDevices.inputs[0].label) {
      try {
        var tmp = await navigator.mediaDevices.getUserMedia({ audio: true });
        tmp.getTracks().forEach(function(t) { t.stop(); });
        devices = await navigator.mediaDevices.enumerateDevices();
        voiceAudioDevices.inputs = devices.filter(function(d) { return d.kind === 'audioinput'; });
        voiceAudioDevices.outputs = devices.filter(function(d) { return d.kind === 'audiooutput'; });
      } catch(e) {}
    }
    if (voiceData) voiceRender();
  } catch(e) {}
}

function voiceDeactivate() {
  if (voicePollTimer) { clearInterval(voicePollTimer); voicePollTimer = null; }
}

async function voiceFetch() {
  try {
    var r = await fetch('/api/voice');
    voiceData = await r.json();
    voiceRender();
  } catch(e) {
    document.getElementById('tab-voice').innerHTML = '<div class="loading-msg">Failed to load voice status</div>';
  }
}

async function voiceFetchLive() {
  try {
    var r = await fetch('/api/voice');
    voiceData = await r.json();
    voiceUpdateHWIndicators();
  } catch(e) {}
}

function voiceRender() {
  var panel = document.getElementById('tab-voice');
  var d = voiceData;
  var active = d.active;
  var cc = voiceClient && voiceClient.connected;

  var html = '<div class="voice-layout">';

  // --- Client card ---
  html += '<div class="card voice-client-card">';
  html += '<div class="card-header">VOICE CLIENT</div>';

  if (location.protocol !== 'https:' && location.hostname !== 'localhost' && location.hostname !== '127.0.0.1') {
    var httpsUrl = 'https://' + location.hostname + (location.port && location.port !== '443' ? '' : '') + location.pathname + location.hash;
    html += '<div class="voice-https-warn">';
    html += '<div class="voice-https-warn-icon">&#9888;</div>';
    html += '<div class="voice-https-warn-text">Voice client requires HTTPS for microphone access.</div>';
    html += '<a class="voice-https-link" href="' + httpsUrl + '">Switch to HTTPS</a>';
    html += '</div>';
  }

  html += '<div class="voice-device-selectors" id="voice-device-selectors">';
  html += '<div class="voice-device-row"><label class="voice-device-label">Microphone</label>';
  html += '<select class="voice-device-select" id="voice-input-select" onchange="voiceSelectedInput=this.value">';
  html += '<option value="">Default</option>';
  voiceAudioDevices.inputs.forEach(function(d) {
    var sel = d.deviceId === voiceSelectedInput ? ' selected' : '';
    html += '<option value="' + escHtml(d.deviceId) + '"' + sel + '>' + escHtml(d.label || 'Mic ' + d.deviceId.slice(0,8)) + '</option>';
  });
  html += '</select></div>';
  html += '<div class="voice-device-row"><label class="voice-device-label">Speaker</label>';
  html += '<select class="voice-device-select" id="voice-output-select" onchange="voiceSelectedOutput=this.value">';
  html += '<option value="">Default</option>';
  voiceAudioDevices.outputs.forEach(function(d) {
    var sel = d.deviceId === voiceSelectedOutput ? ' selected' : '';
    html += '<option value="' + escHtml(d.deviceId) + '"' + sel + '>' + escHtml(d.label || 'Speaker ' + d.deviceId.slice(0,8)) + '</option>';
  });
  html += '</select></div>';
  html += '</div>';

  if (!cc) {
    html += '<div class="voice-connect-wrap">';
    html += '<button class="voice-connect-btn" onclick="voiceClientStart()">Connect to Mesh Voice</button>';
    html += '<div class="voice-connect-hint">Connects your browser mic to the mesh voice channel</div>';
    html += '</div>';
  } else {
    html += '<div class="voice-ptt-area">';
    html += '<button class="voice-ptt-btn' + (voiceClient.transmitting ? ' active' : '') + '" id="voice-ptt-btn">PUSH TO TALK</button>';
    html += '</div>';

    html += '<div class="voice-indicators">';
    html += '<div class="voice-ind"><span class="voice-dot ' + (cc ? 'on' : 'off') + '"></span>Connected</div>';
    html += '<div class="voice-ind"><span class="voice-dot ' + (voiceClient.transmitting ? 'tx' : 'off') + '" id="voice-tx-dot"></span><span id="voice-tx-label">' + (voiceClient.transmitting ? 'TX Web' : 'TX Web Idle') + '</span></div>';
    html += '<div class="voice-ind"><span class="voice-dot ' + (voiceClient.receiving ? 'rx' : 'off') + '" id="voice-rx-dot"></span><span id="voice-rx-label">' + (voiceClient.receiving ? 'RX Web' : 'RX Web Silent') + '</span></div>';
    html += '</div>';

    html += '<div class="voice-disconnect-wrap">';
    html += '<button class="cfg-btn voice-btn-stop" onclick="voiceClientStop()">Disconnect</button>';
    html += '</div>';
  }
  html += '</div>';

  // --- Service + live PTT card ---
  html += '<div class="card voice-status-card">';
  html += '<div class="card-header">HARDWARE PTT</div>';

  // OpenVLM connection status
  var vlmConn = d.ptt_connected;
  html += '<div class="voice-hw-device-row" id="hw-vlm-row">';
  html += '<span class="voice-dot ' + (vlmConn ? 'on' : 'off') + '" id="hw-vlm-dot"></span>';
  html += '<span class="voice-hw-device-label">OpenVLM</span>';
  html += '<span class="voice-hw-device-status ' + (vlmConn ? 'connected' : 'disconnected') + '" id="hw-vlm-status">' + (vlmConn ? 'Connected' : 'Disconnected') + '</span>';
  html += '</div>';

  // PTT state
  html += '<div class="voice-hw-ptt-wrap">';
  html += '<div class="voice-hw-ptt-indicator' + (active && d.ptt_active ? ' active' : '') + '" id="hw-ptt-indicator">';
  html += '<div class="voice-hw-ptt-dot" id="hw-ptt-dot"></div>';
  html += '<div class="voice-hw-ptt-label" id="hw-ptt-label">' + (active ? (d.ptt_active ? 'PTT PRESSED' : 'PTT UNPRESSED') : 'SERVICE OFF') + '</div>';
  html += '</div>';
  html += '</div>';

  // TX/RX indicators
  html += '<div class="voice-indicators" id="hw-txrx">';
  html += '<div class="voice-ind"><span class="voice-dot ' + (d.tx ? 'tx' : 'off') + '" id="hw-tx-dot"></span><span id="hw-tx-label">' + (d.tx ? 'TX PTT' : 'TX PTT Idle') + '</span></div>';
  html += '<div class="voice-ind"><span class="voice-dot ' + (d.rx ? 'rx' : 'off') + '" id="hw-rx-dot"></span><span id="hw-rx-label">' + (d.rx ? 'RX PTT' : 'RX PTT Silent') + '</span></div>';
  html += '</div>';

  html += '<div class="status-row"><div class="status-label">Service</div><div class="status-value">';
  html += '<span class="voice-dot ' + (active ? 'on' : 'off') + '"></span>' + (active ? 'Running' : 'Stopped');
  html += '</div></div>';

  if (active) {
    var fields = [
      { label: 'Uptime', value: d.uptime || '--' },
      { label: 'PTT Mode', value: d.ptt_mode || '--' },
      { label: 'Multicast', value: (d.mcast_addr || '239.69.0.1') + ':' + (d.port || '4370') },
      { label: 'Interface', value: d.interface || 'br0' },
    ];
    fields.forEach(function(f) {
      html += '<div class="status-row"><div class="status-label">' + f.label + '</div><div class="status-value">' + escHtml(String(f.value)) + '</div></div>';
    });
  }

  html += '<div class="status-row voice-svc-controls">';
  if (active) {
    html += '<button class="cfg-btn" onclick="voiceAction(\'restart\')">Restart</button>';
    html += '<button class="cfg-btn voice-btn-stop" onclick="voiceAction(\'stop\')">Stop</button>';
  } else {
    html += '<button class="cfg-btn cfg-btn-primary" onclick="voiceAction(\'start\')">Start</button>';
  }
  html += '</div>';

  html += '<div class="voice-config-hint">Voice settings in <a href="#config">Config</a> tab</div>';
  html += '</div>';

  html += '</div>';
  panel.innerHTML = html;

  voiceBindPTT();
}

function voiceUpdateHWIndicators() {
  if (!voiceData) return;
  var d = voiceData;

  // OpenVLM connection
  var vlmDot = document.getElementById('hw-vlm-dot');
  var vlmStatus = document.getElementById('hw-vlm-status');
  if (vlmDot) {
    vlmDot.className = 'voice-dot ' + (d.ptt_connected ? 'on' : 'off');
  }
  if (vlmStatus) {
    vlmStatus.textContent = d.ptt_connected ? 'Connected' : 'Disconnected';
    vlmStatus.className = 'voice-hw-device-status ' + (d.ptt_connected ? 'connected' : 'disconnected');
  }

  // PTT state
  var indicator = document.getElementById('hw-ptt-indicator');
  var label = document.getElementById('hw-ptt-label');
  if (!indicator) return;

  if (d.active && d.ptt_active) {
    indicator.className = 'voice-hw-ptt-indicator active';
    label.textContent = 'PTT PRESSED';
  } else if (d.active) {
    indicator.className = 'voice-hw-ptt-indicator';
    label.textContent = 'PTT UNPRESSED';
  } else {
    indicator.className = 'voice-hw-ptt-indicator';
    label.textContent = 'SERVICE OFF';
  }

  // TX/RX
  var txDot = document.getElementById('hw-tx-dot');
  var txLabel = document.getElementById('hw-tx-label');
  var rxDot = document.getElementById('hw-rx-dot');
  var rxLabel = document.getElementById('hw-rx-label');
  if (txDot) txDot.className = 'voice-dot ' + (d.tx ? 'tx' : 'off');
  if (txLabel) txLabel.textContent = d.tx ? 'TX PTT' : 'TX PTT Idle';
  if (rxDot) rxDot.className = 'voice-dot ' + (d.rx ? 'rx' : 'off');
  if (rxLabel) rxLabel.textContent = d.rx ? 'RX PTT' : 'RX PTT Silent';
}

function voiceBindPTT() {
  var btn = document.getElementById('voice-ptt-btn');
  if (!btn) return;
  btn.addEventListener('mousedown', function(e) { e.preventDefault(); voiceClientPTT(true); });
  btn.addEventListener('mouseup', function(e) { e.preventDefault(); voiceClientPTT(false); });
  btn.addEventListener('mouseleave', function() { voiceClientPTT(false); });
  btn.addEventListener('touchstart', function(e) { e.preventDefault(); voiceClientPTT(true); });
  btn.addEventListener('touchend', function(e) { e.preventDefault(); voiceClientPTT(false); });
  btn.addEventListener('touchcancel', function() { voiceClientPTT(false); });
}

// --- Service control ---

async function voiceAction(action) {
  try {
    var r = await fetch('/api/voice', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ action: action })
    });
    var result = await r.json();
    if (!result.ok && result.error) alert('Voice ' + action + ' failed: ' + result.error);
    setTimeout(voiceFetch, 1000);
  } catch(e) {
    alert('Voice ' + action + ' failed: ' + e.message);
  }
}

// --- Web voice client ---

function voiceGetUserMedia(constraints) {
  if (navigator.mediaDevices && navigator.mediaDevices.getUserMedia) {
    return navigator.mediaDevices.getUserMedia(constraints);
  }
  var legacy = navigator.getUserMedia || navigator.webkitGetUserMedia || navigator.mozGetUserMedia;
  if (!legacy) return Promise.reject(new Error('getUserMedia not available — HTTPS may be required'));
  return new Promise(function(resolve, reject) {
    legacy.call(navigator, constraints, resolve, reject);
  });
}

var VOICE_SAMPLE_RATE = 48000;
var VOICE_FRAME_SIZE = 1024;

async function voiceClientStart() {
  if (voiceClient && voiceClient.connected) return;

  try {
    var audioConstraints = { sampleRate: VOICE_SAMPLE_RATE, channelCount: 1, echoCancellation: true, noiseSuppression: true };
    if (voiceSelectedInput) audioConstraints.deviceId = { exact: voiceSelectedInput };
    var stream = await voiceGetUserMedia({ audio: audioConstraints });

    var ctxOpts = { sampleRate: VOICE_SAMPLE_RATE };
    if (voiceSelectedOutput) ctxOpts.sinkId = voiceSelectedOutput;
    var ctx = new AudioContext(ctxOpts);

    var encoder = new AudioEncoder({
      output: function(chunk) {
        if (!voiceClient || !voiceClient.ws || !voiceClient.transmitting) return;
        var buf = new ArrayBuffer(chunk.byteLength);
        chunk.copyTo(buf);
        voiceClient.ws.send(buf);
      },
      error: function(e) { console.error('voice encoder:', e); }
    });
    encoder.configure({ codec: 'opus', sampleRate: VOICE_SAMPLE_RATE, numberOfChannels: 1, bitrate: 32000 });

    var decoder = new AudioDecoder({
      output: function(frame) {
        if (!voiceClient) { frame.close(); return; }
        var samples = new Float32Array(frame.numberOfFrames);
        frame.copyTo(samples, { planeIndex: 0 });
        frame.close();
        voiceClientPlaySamples(samples);
      },
      error: function(e) { console.error('voice decoder:', e); }
    });
    decoder.configure({ codec: 'opus', sampleRate: VOICE_SAMPLE_RATE, numberOfChannels: 1 });

    var playBufSize = VOICE_SAMPLE_RATE;
    var playBuf = new Float32Array(playBufSize);
    var playWritePos = 0;
    var playReadPos = 0;

    var playNode = ctx.createScriptProcessor(VOICE_FRAME_SIZE, 0, 1);
    playNode.onaudioprocess = function(e) {
      var out = e.outputBuffer.getChannelData(0);
      for (var i = 0; i < out.length; i++) {
        if (playReadPos !== playWritePos) {
          out[i] = playBuf[playReadPos % playBufSize];
          playReadPos++;
        } else {
          out[i] = 0;
        }
      }
    };
    playNode.connect(ctx.destination);

    var source = ctx.createMediaStreamSource(stream);
    var captureNode = ctx.createScriptProcessor(VOICE_FRAME_SIZE, 1, 1);
    var capturing = false;

    captureNode.onaudioprocess = function(e) {
      if (!capturing) return;
      var input = e.inputBuffer.getChannelData(0);
      var data = new Float32Array(input.length);
      data.set(input);
      var audioData = new AudioData({
        format: 'f32-planar',
        sampleRate: VOICE_SAMPLE_RATE,
        numberOfFrames: data.length,
        numberOfChannels: 1,
        timestamp: performance.now() * 1000,
        data: data
      });
      try { encoder.encode(audioData); } catch(ex) {}
      audioData.close();
    };
    source.connect(captureNode);
    captureNode.connect(ctx.destination);

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(proto + '//' + location.host + '/ws/voice');
    ws.binaryType = 'arraybuffer';

    var rxTimeout = null;

    ws.onmessage = function(evt) {
      if (!(evt.data instanceof ArrayBuffer) || evt.data.byteLength < 12) return;
      var payload = new Uint8Array(evt.data, 12);
      if (payload.length === 0) return;

      var chunk = new EncodedAudioChunk({ type: 'key', timestamp: performance.now() * 1000, data: payload });
      try { decoder.decode(chunk); } catch(ex) {}

      if (voiceClient) {
        voiceClient.receiving = true;
        voiceUpdateClientIndicators();
        clearTimeout(rxTimeout);
        rxTimeout = setTimeout(function() {
          if (voiceClient) { voiceClient.receiving = false; voiceUpdateClientIndicators(); }
        }, 300);
      }
    };

    ws.onclose = function() { voiceClientCleanup(); if (activeTab === 'voice') voiceRender(); };
    ws.onerror = function() { voiceClientCleanup(); if (activeTab === 'voice') voiceRender(); };

    voiceClient = {
      ws: ws, ctx: ctx, stream: stream, encoder: encoder, decoder: decoder,
      captureNode: captureNode, playNode: playNode, source: source,
      connected: false, transmitting: false, receiving: false,
      setCaptureActive: function(active) { capturing = active; }
    };

    window.voiceClientPlaySamples = function(samples) {
      if (!voiceClient) return;
      for (var i = 0; i < samples.length; i++) {
        playBuf[playWritePos % playBufSize] = samples[i];
        playWritePos++;
      }
    };

    ws.onopen = function() {
      voiceClient.connected = true;
      if (activeTab === 'voice') voiceRender();
    };

  } catch(e) {
    alert('Voice client error: ' + e.message);
    voiceClientCleanup();
  }
}

function voiceClientStop() {
  voiceClientCleanup();
  voiceRender();
}

function voiceClientCleanup() {
  if (!voiceClient) return;
  try { voiceClient.ws.close(); } catch(e) {}
  try { voiceClient.encoder.close(); } catch(e) {}
  try { voiceClient.decoder.close(); } catch(e) {}
  try { voiceClient.captureNode.disconnect(); } catch(e) {}
  try { voiceClient.playNode.disconnect(); } catch(e) {}
  try { voiceClient.source.disconnect(); } catch(e) {}
  try { voiceClient.ctx.close(); } catch(e) {}
  try { voiceClient.stream.getTracks().forEach(function(t) { t.stop(); }); } catch(e) {}
  voiceClient = null;
}

function voiceClientPTT(down) {
  if (!voiceClient || !voiceClient.connected) return;
  voiceClient.transmitting = down;
  voiceClient.setCaptureActive(down);
  voiceUpdateClientIndicators();
}

function voiceUpdateClientIndicators() {
  if (!voiceClient) return;
  var txDot = document.getElementById('voice-tx-dot');
  var txLabel = document.getElementById('voice-tx-label');
  var rxDot = document.getElementById('voice-rx-dot');
  var rxLabel = document.getElementById('voice-rx-label');
  var pttBtn = document.getElementById('voice-ptt-btn');

  if (txDot) txDot.className = 'voice-dot ' + (voiceClient.transmitting ? 'tx' : 'off');
  if (txLabel) txLabel.textContent = voiceClient.transmitting ? 'TX Web' : 'TX Web Idle';
  if (rxDot) rxDot.className = 'voice-dot ' + (voiceClient.receiving ? 'rx' : 'off');
  if (rxLabel) rxLabel.textContent = voiceClient.receiving ? 'RX Web' : 'RX Web Silent';
  if (pttBtn) pttBtn.classList.toggle('active', voiceClient.transmitting);
}
