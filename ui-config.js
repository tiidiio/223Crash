// ui-config.js — 223CRASH config réelle + WS sur :3000
export const CONFIG = Object.freeze({
  // Valeur de repli : le serveur n'envoie PAS la durée de la fenêtre de paris (seulement startsAt).
  betWindowMs: 6000,
  historySize: 32,
  defaultWsPath: '',
  defaultWsPort: 3000,
  // Hostnames WS autorisés EN PLUS du hostname de la page. Le JWT n'est jamais envoyé ailleurs.
  allowedWsHosts: Object.freeze([]),
  defaultLang: 'fr',
  langKey: '223crash.lang.v1',
  soundKey: '223crash.sound.v1',
  volumeKey: '223crash.volume.v1',
  tokenKey: '223crash.session.v1',
});

function readStoredToken() {
  try { return sessionStorage.getItem(CONFIG.tokenKey) || ''; } catch (_) { return ''; }
}
function storeToken(t) {
  try { sessionStorage.setItem(CONFIG.tokenKey, t); } catch (_) {}
}

function isAllowedWsUrl(u) {
  if (u.protocol !== 'ws:' && u.protocol !== 'wss:') return false;
  if (u.username || u.password) return false;
  if (location.protocol === 'https:' && u.protocol !== 'wss:') return false;
  return u.hostname === location.hostname || CONFIG.allowedWsHosts.includes(u.hostname);
}

export function resolveConnection() {
  const q = new URLSearchParams(location.search);
  let token = (q.get('token') || '').trim();
  if (token) {
    storeToken(token);
    q.delete('token');
    const rest = q.toString();
    try { history.replaceState(null, '', location.pathname + (rest ? '?' + rest : '') + location.hash); } catch (_) {}
  } else {
    token = readStoredToken();
  }
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  let url = `${proto}//${location.hostname}:${CONFIG.defaultWsPort}`;
  const custom = (q.get('ws') || '').trim();
  if (custom) {
    try {
      const u = new URL(custom, location.href);
      if (isAllowedWsUrl(u)) url = `${u.protocol}//${u.host}${u.pathname === '/' ? '' : u.pathname}`;
    } catch (_) {}
  }
  return { url, token };
}
