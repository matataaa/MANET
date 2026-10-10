// Offline updates, shared by the Config and Fleet Control tabs. Every node
// keeps a package store (/api/offline-update): the last package it
// installed on each channel, or one uploaded here. A node installs from its
// own store, or from another node's over the mesh, without the update server.

// Upload state lives here, not in the DOM: Fleet Control re-renders its
// panel on every poll, which would wipe a progress message mid-upload.
var offlineUploadState = { busy: false, msg: '', error: false };

function offlineChannelLabel(channel) {
  return channel === 'overlay' ? 'Kernel/Drivers' : 'MANET';
}

// > 0 when version a is newer than b on channel (X.Y.Z for MANET, a plain
// decimal for Kernel/Drivers), the same comparison node-update makes.
function offlineCompare(channel, a, b) {
  if (channel === 'overlay') return (parseFloat(a) || 0) - (parseFloat(b) || 0);
  var pa = String(a || '').split('.').map(Number), pb = String(b || '').split('.').map(Number);
  for (var i = 0; i < 3; i++) {
    var d = (pa[i] || 0) - (pb[i] || 0);
    if (d) return d;
  }
  return 0;
}

function offlinePackageText(channel, p) {
  return offlineChannelLabel(channel) + ' v' + escHtml(p.version) +
    ' <span style="color:var(--muted)">(' + escHtml(p.file) + ', ' + (p.size / 1048576).toFixed(1) + ' MB' +
    (p.signed ? '' : ', <span style="color:var(--warn)">unsigned</span>') + ')</span>';
}

// File picker + status line. Picking files starts the upload straight away
// (one dialog, several files) so no re-render can lose the selection.
function offlineRenderUpload(prefix, board) {
  board = board || 'cm4';
  var st = offlineUploadState;
  var html = '<div class="fleet-actions" style="align-items:center">';
  html += '<label class="fleet-btn"' + (st.busy ? ' style="opacity:.5;pointer-events:none"' : '') + '>Upload Package…' +
    '<input type="file" id="' + prefix + '-offline-file" multiple accept=".zip,.gz,.sig,.txt" style="display:none"' +
    (st.busy ? ' disabled' : '') + '></label>';
  if (st.msg) {
    html += '<span style="font-size:12px;color:' + (st.error ? 'var(--bad)' : 'var(--muted)') + '">' + escHtml(st.msg) + '</span>';
  }
  html += '</div>';
  html += '<div style="font-size:11px;color:var(--muted);margin-top:6px">The release .zip, or these files from the release folder: ' +
    '<code>' + board + '-tools.tar.gz</code> + <code>.sig</code> + <code>manet_release_version.txt</code> (MANET), and/or ' +
    '<code>' + board + '-sbc-overlay.tar.gz</code> + <code>.sig</code> + <code>' + board + '-overlay-version.txt</code> (Kernel/Drivers). ' +
    'Leave the .img out.</div>';
  return html;
}

// rerender is called whenever the upload state changes (progress, result),
// so the caller can redraw from offlineUploadState.
function offlineWireUpload(prefix, rerender) {
  var input = document.getElementById(prefix + '-offline-file');
  if (!input) return;
  input.addEventListener('change', function() {
    if (!input.files || !input.files.length) return;
    offlineUpload(Array.from(input.files), rerender);
  });
}

function offlineUpload(files, rerender) {
  var st = offlineUploadState;
  st.busy = true; st.error = false; st.msg = 'Uploading…';
  rerender();

  function send() {
    var form = new FormData();
    files.forEach(function(f) { form.append('files', f, f.name); });
    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/offline-update');
    xhr.upload.onprogress = function(e) {
      if (!e.lengthComputable) return;
      var pct = Math.round(e.loaded / e.total * 100);
      st.msg = pct < 100 ? 'Uploading… ' + pct + '%' : 'Checking the signature…';
      rerender();
    };
    xhr.onload = function() {
      if (xhr.status === 401) {
        authShowLogin(send, function() { finish(true, 'Login required to upload'); });
        return;
      }
      var resp = {};
      try { resp = JSON.parse(xhr.responseText); } catch (e) {}
      if (xhr.status !== 200 || !resp.ok) {
        finish(true, resp.error || ('Upload failed (HTTP ' + xhr.status + ')'));
        return;
      }
      var stored = (resp.stored || []).map(offlineChannelLabel).join(' + ');
      finish(false, 'Stored ' + stored + ' on this node');
    };
    xhr.onerror = function() { finish(true, 'Upload failed: connection lost'); };
    xhr.send(form);
  }
  function finish(error, msg) {
    st.busy = false; st.error = error; st.msg = msg;
    rerender();
  }
  send();
}
