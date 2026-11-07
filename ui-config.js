// ui-config.js — constantes et résolution de la connexion (jeton + adresse WebSocket).

export const CONFIG = Object.freeze({
  betWindowMs: 5000,            // doit correspondre à engine.DefaultConfig().BettingDuration
  historySize: 24,
  defaultWsPath: '/ws',         // chemin WebSocket du gateway (à ajuster si différent)
  soundKey: '223crash.sound.v1',
  tokenKey: '223crash.session.v1',
});

function readStoredToken() {
  try { return sessionStorage.getItem(CONFIG.tokenKey) || ''; } catch (_) { return ''; }
}
function storeToken(t) {
  try { sessionStorage.setItem(CONFIG.tokenKey, t); } catch (_) {}
}

// Jeton : ?token=... dans l'URL (fourni par l'opérateur), conservé pour la session de l'onglet
// puis retiré de la barre d'adresse (évite fuite par historique ou en-tête Referer).
// Adresse WS : ?ws=... acceptée uniquement sur le même hôte que la page — sinon un lien piégé
// enverrait le jeton vers un serveur tiers.
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
  let url = `${proto}//${location.host}${CONFIG.defaultWsPath}`;
  const custom = (q.get('ws') || '').trim();
  if (custom) {
    try {
      const u = new URL(custom, location.href);
      if ((u.protocol === 'ws:' || u.protocol === 'wss:') && u.host === location.host) url = u.href;
    } catch (_) {}
  }
  return { url, token };
}
