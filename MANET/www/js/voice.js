let voiceInitialized = false;
let voiceData = null;
let voiceClient = null;
let voicePollTimer = null;

function voiceActivate() {
  var panel = document.getElementById('tab-voice');
  if (!voiceInitialized) {
    panel.innerHTML = '<div class="loading-msg">Loading voice...</div>';
    voiceInitialized = true;
  }
  voiceFetch();
  voicePollTimer = setInterval(voiceFetch, 5000);
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

function voiceRender() {
  var panel = document.getElementById('tab-voice');
  var d = voiceData;
  var active = d.active;
  var cc = voiceClient && voiceClient.connected;

  var html = '<div class="voice-layout">';

  // --- Client card ---
  html += '<div class="card voice-client-card">';
  html += '<div class="card-header">VOICE CLIENT</div>';

  if (!window.AudioEncoder) {
    html += '<div class="voice-unsupported">WebCodecs not supported. Use Chrome or Edge.</div>';
  } else if (!cc) {
    html += '<div class="voice-connect-wrap">';
    html += '<button class="voice-connect-btn" onclick="voiceClientStart()">Connect to Mesh Voice</button>';
    html += '<div class="voice-connect-hint">Connects your browser mic to the mesh multicast channel</div>';
    html += '</div>';
  } else {
    // PTT area
    html += '<div class="voice-ptt-area">';
    html += '<button class="voice-ptt-btn' + (voiceClient.transmitting ? ' active' : '') + '" id="voice-ptt-btn">PUSH TO TALK</button>';
    html += '</div>';

    // Live indicators
    html += '<div class="voice-indicators">';
    html += '<div class="voice-ind"><span class="voice-dot ' + (cc ? 'on' : 'off') + '"></span>Connected</div>';
    html += '<div class="voice-ind"><span class="voice-dot ' + (voiceClient.transmitting ? 'tx' : 'off') + '" id="voice-tx-dot"></span><span id="voice-tx-label">' + (voiceClient.transmitting ? 'Transmitting' : 'TX Idle') + '</span></div>';
    html += '<div class="voice-ind"><span class="voice-dot ' + (voiceClient.receiving ? 'rx' : 'off') + '" id="voice-rx-dot"></span><span id="voice-rx-label">' + (voiceClient.receiving ? 'Receiving' : 'RX Silent') + '</span></div>';
    html += '</div>';

    html += '<div class="voice-disconnect-wrap">';
    html += '<button class="cfg-btn voice-btn-stop" onclick="voiceClientStop()">Disconnect</button>';
    html += '</div>';
  }
  html += '</div>';

  // --- Current settings card (read-only) ---
  html += '<div class="card voice-status-card">';
  html += '<div class="card-header">SERVICE STATUS</div>';

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

  html += '<div class="voice-config-hint">Voice settings can be changed in the <a href="#config">Config</a> tab</div>';
  html += '</div>';

  html += '</div>';
  panel.innerHTML = html;

  voiceBindPTT();
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

var VOICE_SAMPLE_RATE = 48000;
var VOICE_FRAME_SIZE = 960;

async function voiceClientStart() {
  if (voiceClient && voiceClient.connected) return;

  try {
    var stream = await navigator.mediaDevices.getUserMedia({
      audio: { sampleRate: VOICE_SAMPLE_RATE, channelCount: 1, echoCancellation: true, noiseSuppression: true }
    });

    var ctx = new AudioContext({ sampleRate: VOICE_SAMPLE_RATE });

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
        voiceUpdateLiveIndicators();
        clearTimeout(rxTimeout);
        rxTimeout = setTimeout(function() {
          if (voiceClient) { voiceClient.receiving = false; voiceUpdateLiveIndicators(); }
        }, 300);
      }
    };

    ws.onclose = function() { voiceClientCleanup(); voiceRender(); };
    ws.onerror = function() { voiceClientCleanup(); voiceRender(); };

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
      voiceRender();
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
  voiceUpdateLiveIndicators();
}

function voiceUpdateLiveIndicators() {
  if (!voiceClient) return;
  var txDot = document.getElementById('voice-tx-dot');
  var txLabel = document.getElementById('voice-tx-label');
  var rxDot = document.getElementById('voice-rx-dot');
  var rxLabel = document.getElementById('voice-rx-label');
  var pttBtn = document.getElementById('voice-ptt-btn');

  if (txDot) txDot.className = 'voice-dot ' + (voiceClient.transmitting ? 'tx' : 'off');
  if (txLabel) txLabel.textContent = voiceClient.transmitting ? 'Transmitting' : 'TX Idle';
  if (rxDot) rxDot.className = 'voice-dot ' + (voiceClient.receiving ? 'rx' : 'off');
  if (rxLabel) rxLabel.textContent = voiceClient.receiving ? 'Receiving' : 'RX Silent';
  if (pttBtn) pttBtn.classList.toggle('active', voiceClient.transmitting);
}
