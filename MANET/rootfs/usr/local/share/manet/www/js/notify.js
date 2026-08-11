// Toast notification system and applet badge manager
(function() {
  'use strict';

  var TOAST_MAX = 5;
  var DEFAULT_DURATION = 5000;
  var container = null;
  var toasts = [];

  var TYPE_COLORS = {
    info:    'var(--info)',
    success: 'var(--good)',
    warning: 'var(--warn)',
    error:   'var(--bad)'
  };

  function getContainer() {
    if (!container) {
      container = document.getElementById('toast-container');
    }
    return container;
  }

  // --- Toast notifications ---

  function notify(title, body, opts) {
    opts = opts || {};
    var type = opts.type || 'info';
    var duration = opts.duration != null ? opts.duration : DEFAULT_DURATION;
    var onClick = opts.onClick || null;
    var icon = opts.icon || null;

    var el = document.createElement('div');
    el.className = 'toast toast-' + type;
    el.style.borderLeftColor = TYPE_COLORS[type] || TYPE_COLORS.info;

    var html = '<div class="toast-content">';
    if (icon) {
      html += '<span class="toast-icon">' + icon + '</span>';
    }
    html += '<div class="toast-text">';
    html += '<div class="toast-title">' + escapeHtml(title) + '</div>';
    if (body) {
      html += '<div class="toast-body">' + escapeHtml(body) + '</div>';
    }
    html += '</div>';
    html += '<button class="toast-close" type="button" aria-label="Close">&times;</button>';
    html += '</div>';
    el.innerHTML = html;

    if (onClick) {
      el.style.cursor = 'pointer';
      el.addEventListener('click', function(e) {
        if (!e.target.classList.contains('toast-close')) {
          onClick(e);
        }
      });
    }

    el.querySelector('.toast-close').addEventListener('click', function() {
      dismiss(handle);
    });

    // Add to DOM
    var c = getContainer();
    if (c) {
      c.appendChild(el);
    }

    // Trigger enter animation
    requestAnimationFrame(function() {
      el.classList.add('toast-enter');
    });

    var handle = {
      el: el,
      timer: null,
      dismissed: false,
      dismiss: function() { dismiss(handle); }
    };

    toasts.push(handle);

    // Enforce max visible
    while (toasts.length > TOAST_MAX) {
      dismiss(toasts[0]);
    }

    // Auto-dismiss
    if (duration > 0) {
      handle.timer = setTimeout(function() {
        dismiss(handle);
      }, duration);
    }

    // Browser notification when page hidden
    sendBrowserNotification(title, body);

    return handle;
  }

  function dismiss(handle) {
    if (handle.dismissed) return;
    handle.dismissed = true;

    if (handle.timer) {
      clearTimeout(handle.timer);
      handle.timer = null;
    }

    var el = handle.el;
    el.classList.remove('toast-enter');
    el.classList.add('toast-exit');

    el.addEventListener('animationend', function() {
      if (el.parentNode) el.parentNode.removeChild(el);
    });

    // Fallback removal in case animationend doesn't fire
    setTimeout(function() {
      if (el.parentNode) el.parentNode.removeChild(el);
    }, 400);

    var idx = toasts.indexOf(handle);
    if (idx !== -1) toasts.splice(idx, 1);
  }

  // --- Applet badge management ---

  function notifyBadge(appletName, count) {
    var targets = document.querySelectorAll('[data-applet-badge="' + appletName + '"]');
    targets.forEach(function(el) {
      // Find or create badge element
      var badge = el.querySelector('.applet-badge');
      if (count > 0) {
        if (!badge) {
          badge = document.createElement('span');
          badge.className = 'applet-badge';
          var launch = el.querySelector('.dash-applet-launch');
          if (launch) el.insertBefore(badge, launch);
          else el.appendChild(badge);
        }
        badge.textContent = count > 99 ? '99+' : count;
      } else {
        if (badge) {
          badge.parentNode.removeChild(badge);
        }
      }
    });
  }

  // --- Browser Notification API ---

  function notifyRequestPermission() {
    if (!('Notification' in window)) return Promise.resolve('unsupported');
    if (Notification.permission === 'granted') return Promise.resolve('granted');
    if (Notification.permission === 'denied') return Promise.resolve('denied');
    return Notification.requestPermission();
  }

  function sendBrowserNotification(title, body) {
    if (!('Notification' in window)) return;
    if (Notification.permission !== 'granted') return;
    if (!document.hidden) return;
    try {
      new Notification(title, { body: body || '' });
    } catch(e) {
      // Silently ignore - some environments block Notification constructor
    }
  }

  // --- Utility ---

  function escapeHtml(str) {
    if (!str) return '';
    // Use the global escHtml if available (from common.js), otherwise basic escape
    if (typeof window.escHtml === 'function') return window.escHtml(str);
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
  }

  // --- Expose globals ---

  window.notify = notify;
  window.notifyBadge = notifyBadge;
  window.notifyRequestPermission = notifyRequestPermission;

})();
