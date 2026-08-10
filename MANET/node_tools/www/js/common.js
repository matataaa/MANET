// Theme
const THEME_KEY = 'manetUiTheme';

function preferredTheme() {
  const params = new URLSearchParams(window.location.search);
  const forced = params.get('theme');
  if (forced === 'dark' || forced === 'light') {
    try { localStorage.setItem(THEME_KEY, forced); } catch(e) {}
    return forced;
  }
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === 'dark' || saved === 'light') return saved;
  } catch(e) {}
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  const btn = document.getElementById('theme-toggle');
  if (btn) btn.textContent = theme === 'dark' ? 'Light' : 'Dark';
}

function toggleTheme() {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  try { localStorage.setItem(THEME_KEY, next); } catch(e) {}
  setTheme(next);
}

function isDarkTheme() {
  return document.documentElement.dataset.theme === 'dark';
}

// TQ helpers
function tqClass(tq) {
  if (tq == null) return 'badge-tq-none';
  if (tq >= 200)  return 'badge-tq-great';
  if (tq >= 130)  return 'badge-tq-ok';
  if (tq >= 60)   return 'badge-tq-warn';
  return 'badge-tq-bad';
}

function tqColor(tq) {
  if (tq == null) return '#9aa4b2';
  if (tq >= 200)  return '#22c55e';
  if (tq >= 130)  return '#eab308';
  if (tq >= 60)   return '#f97316';
  return '#ef4444';
}

function tqLabel(tq) {
  if (tq == null) return '?';
  return 'TQ ' + tq;
}

function tqPct(tq) {
  if (tq == null) return 0;
  return Math.round((tq / 255) * 100);
}

function fmtAge(ts, refTime) {
  const secs = (refTime || Math.floor(Date.now() / 1000)) - parseInt(ts || 0);
  if (secs < 60)    return secs + 's ago';
  if (secs < 3600)  return Math.floor(secs/60) + 'm ago';
  if (secs < 86400) return Math.floor(secs/3600) + 'h ago';
  return Math.floor(secs/86400) + 'd ago';
}

function themeColor(light, dark) {
  return isDarkTheme() ? dark : light;
}

function escHtml(str) {
  const d = document.createElement('div');
  d.textContent = str;
  return d.innerHTML;
}

function ts(epoch) {
  const d = new Date(epoch * 1000);
  return d.toLocaleTimeString('en-US', {hour:'2-digit', minute:'2-digit', second:'2-digit', hour12:false});
}

// Init theme
setTheme(preferredTheme());
document.getElementById('theme-toggle').addEventListener('click', toggleTheme);
