// i18n.js — FR + EN au lancement. Langues prévues (bm, wo, ha, yo) : registerLanguage(code, label, messages).
// Repli : langue demandée -> en -> fr -> clé brute.
import { CONFIG } from './ui-config.js';

const DICT = {
  fr: {
    'balance.label': 'SOLDE',
    'aria.sound': 'Activer ou couper le son',
    'aria.menu': 'Menu',
    'aria.history': 'Derniers multiplicateurs',
    'phase.waiting': 'En attente du serveur…',
    'phase.syncing': 'Synchronisation…',
    'phase.betting': 'Mises ouvertes · {s}s',
    'phase.betting.open': 'Mises ouvertes',
    'phase.closing': 'Fermeture des mises…',
    'phase.flying': 'En vol',
    'phase.crashed': 'Crash',
    'mult.crash': 'CRASH',
    'status.connecting': 'Connexion…',
    'status.connected': 'En ligne',
    'status.disconnected': 'Déconnecté',
    'status.reconnecting': 'Reconnexion…',
    'status.stale': 'Connexion instable',
    'status.error': 'Erreur réseau',
    'status.replaced': 'Session remplacée',
    'conn.lost': 'Connexion perdue — vos actions sont suspendues',
    'history.empty': 'Aucune manche reçue pour le moment',
    'sr.betting': 'Mises ouvertes',
    'sr.flying': 'En vol',
    'sr.crash': 'Crash à {x}',
    'menu.title': 'Menu',
    'menu.sound': 'Son',
    'menu.soundOn': 'Son activé',
    'menu.soundOff': 'Son coupé',
    'menu.volume': 'Volume',
    'menu.language': 'Langue',
    'menu.fullscreen': 'Plein écran',
    'menu.rules': 'Règles du jeu',
    'menu.fair': 'Équité (Provably Fair)',
    'menu.close': 'Fermer',
    'fair.title': 'Équité du jeu — Provably Fair',
    'fair.round': 'Manche en cours',
    'fair.hash': 'Hash de la graine serveur (engagé avant les mises)',
    'fair.last': 'Dernier crash',
    'fair.note': "Le hash est engagé avant l'ouverture des mises : il ne peut plus changer. Le crash est calculé côté serveur. La graine serveur n'est pas transmise à cette interface.",
    'fair.copy': 'Copier le hash',
    'fair.close': 'Fermer',
    'toast.hashCopied': 'Hash copié',
    'toast.noHash': 'Aucun hash disponible',
    'toast.copyFail': 'Copie impossible',
    'toast.fsFail': 'Plein écran indisponible',
    'rules.title': 'Règles du jeu',
    'rules.p1': 'Placez votre mise pendant la phase « Mises ouvertes ». Deux mises indépendantes (A et B) sont possibles.',
    'rules.p2': "Une fois le vol lancé, le multiplicateur augmente jusqu'au crash. Encaissez avant le crash : le gain est la mise multipliée par le multiplicateur au moment de l'encaissement.",
    'rules.p3': "Si le crash survient avant votre encaissement, la mise est perdue. Auto-cashout : encaissement automatique au multiplicateur choisi (minimum 1.01x).",
    'rules.p4': "Le résultat officiel est toujours celui du serveur. Le statut « À VÉRIFIER » signifie que la confirmation n'est pas encore reçue.",
    'replaced.title': 'Session ouverte ailleurs',
    'replaced.body': 'Ce compte est connecté sur un autre appareil ou onglet.',
    'replaced.btn': 'Reprendre ici',
    'fatal.title': 'Accès impossible',
    'fatal.noToken': "Jeton manquant. Ouvrez le jeu depuis l'opérateur.",
  },
  en: {
    'balance.label': 'BALANCE',
    'aria.sound': 'Toggle sound',
    'aria.menu': 'Menu',
    'aria.history': 'Latest multipliers',
    'phase.waiting': 'Waiting for the server…',
    'phase.syncing': 'Syncing…',
    'phase.betting': 'Betting open · {s}s',
    'phase.betting.open': 'Betting open',
    'phase.closing': 'Betting closing…',
    'phase.flying': 'In flight',
    'phase.crashed': 'Crashed',
    'mult.crash': 'CRASH',
    'status.connecting': 'Connecting…',
    'status.connected': 'Online',
    'status.disconnected': 'Disconnected',
    'status.reconnecting': 'Reconnecting…',
    'status.stale': 'Unstable connection',
    'status.error': 'Network error',
    'status.replaced': 'Session replaced',
    'conn.lost': 'Connection lost — your actions are suspended',
    'history.empty': 'No round received yet',
    'sr.betting': 'Betting open',
    'sr.flying': 'In flight',
    'sr.crash': 'Crashed at {x}',
    'menu.title': 'Menu',
    'menu.sound': 'Sound',
    'menu.soundOn': 'Sound on',
    'menu.soundOff': 'Sound off',
    'menu.volume': 'Volume',
    'menu.language': 'Language',
    'menu.fullscreen': 'Fullscreen',
    'menu.rules': 'Game rules',
    'menu.fair': 'Fairness (Provably Fair)',
    'menu.close': 'Close',
    'fair.title': 'Game fairness — Provably Fair',
    'fair.round': 'Current round',
    'fair.hash': 'Server seed hash (committed before bets)',
    'fair.last': 'Last crash',
    'fair.note': 'The hash is committed before betting opens: it can no longer change. The crash is computed server-side. The server seed is not sent to this interface.',
    'fair.copy': 'Copy hash',
    'fair.close': 'Close',
    'toast.hashCopied': 'Hash copied',
    'toast.noHash': 'No hash available',
    'toast.copyFail': 'Copy failed',
    'toast.fsFail': 'Fullscreen unavailable',
    'rules.title': 'Game rules',
    'rules.p1': 'Place your bet during the "Betting open" phase. Two independent bets (A and B) are possible.',
    'rules.p2': 'Once the flight starts, the multiplier rises until the crash. Cash out before the crash: your win is your bet times the multiplier at the moment of cash-out.',
    'rules.p3': 'If the crash happens before you cash out, the bet is lost. Auto-cashout cashes out automatically at the chosen multiplier (minimum 1.01x).',
    'rules.p4': 'The official result is always the server\'s. The "TO VERIFY" status means confirmation has not been received yet.',
    'replaced.title': 'Session opened elsewhere',
    'replaced.body': 'This account is connected on another device or tab.',
    'replaced.btn': 'Resume here',
    'fatal.title': 'Access impossible',
    'fatal.noToken': 'Missing token. Open the game from the operator.',
  },
};
const LABELS = { fr: 'Français', en: 'English' };
let lang = CONFIG.defaultLang;
const listeners = new Set();

export function registerMessages(code, messages) {
  DICT[code] = { ...(DICT[code] || {}), ...messages };
}
export function registerLanguage(code, label, messages) {
  registerMessages(code, messages);
  LABELS[code] = label || LABELS[code] || code;
}
export function languages() {
  return Object.keys(DICT).map((code) => ({ code, label: LABELS[code] || code }));
}
export function getLang() { return lang; }

export function t(key, vars) {
  let s = (DICT[lang] && DICT[lang][key]) ?? (DICT.en && DICT.en[key]) ?? (DICT.fr && DICT.fr[key]) ?? key;
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
  return s;
}

export function applyDom(root = document) {
  root.querySelectorAll('[data-i18n]').forEach((n) => { n.textContent = t(n.getAttribute('data-i18n')); });
  root.querySelectorAll('[data-i18n-aria]').forEach((n) => { n.setAttribute('aria-label', t(n.getAttribute('data-i18n-aria'))); });
  root.querySelectorAll('[data-i18n-title]').forEach((n) => { n.title = t(n.getAttribute('data-i18n-title')); });
}

export function onLangChange(cb) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function setLang(code, { persist = true } = {}) {
  if (!DICT[code]) return false;
  lang = code;
  try { document.documentElement.lang = code; } catch (_) {}
  if (persist) { try { localStorage.setItem(CONFIG.langKey, code); } catch (_) {} }
  applyDom();
  listeners.forEach((cb) => { try { cb(code); } catch (_) {} });
  return true;
}

// Priorité : ?lang= (paramètre opérateur, non persisté) > choix mémorisé > défaut. Pas de navigator.language.
export function initLang() {
  let code = '';
  try { code = (new URLSearchParams(location.search).get('lang') || '').toLowerCase().split('-')[0]; } catch (_) {}
  if (!(code && DICT[code])) {
    code = '';
    try { code = localStorage.getItem(CONFIG.langKey) || ''; } catch (_) {}
    if (!DICT[code]) code = CONFIG.defaultLang;
  }
  setLang(code, { persist: false });
  return code;
}
