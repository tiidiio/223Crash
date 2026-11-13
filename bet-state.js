// bet-state.js — logique pure d'un panneau de pari (sans DOM, testable).

// Raisons du gateway (chaînes libres, pas de code structuré) -> clé i18n. Inconnu : texte brut affiché.
const REASONS = {
  'montant invalide': 'bet.reason.invalidAmount',
  'aucun round actif': 'bet.reason.noRound',
  'autocashout invalide': 'bet.reason.autoInvalid',
  'service indisponible': 'bet.reason.unavailable',
  'débit refusé': 'bet.reason.debitRefused',
  'pari déjà traité': 'bet.reason.alreadyHandled',
  'slot invalide': 'bet.reason.invalidSlot',
  'aucun pari actif': 'bet.reason.noActive',
  'débit indéterminé, réessayez': 'bet.reason.indeterminate',
  'débit en cours de traitement, réessayez': 'bet.reason.inProgress',
};

export function reasonKey(reason) {
  return REASONS[String(reason || '').trim().toLowerCase()] || null;
}

// p : { pending, cashing, active, unconfirmed, queued }  c : { connected, phase }
// phase : 'idle' | 'betting' | 'flying' | 'crashed'
// subKind : 'stake' | 'cashout' | 'amount' | 'cancel' | 'unknown' | 'next' | ''
export function panelView(p, c) {
  if (!c.connected) return { mode: 'off', main: 'bet.main.offline', subKind: '', disabled: true };
  if (p.pending) return { mode: 'wait', main: p.cashing ? 'bet.main.cashing' : 'bet.main.sending', subKind: '', disabled: true };
  if (p.active) {
    if (c.phase === 'flying') return { mode: 'cash', main: 'bet.main.cash', subKind: 'cashout', disabled: false };
    return { mode: 'placed', main: 'bet.main.placed', subKind: 'stake', disabled: true };
  }
  if (p.unconfirmed) {
    if (c.phase === 'betting') return { mode: 'bet', main: 'bet.main.retry', subKind: 'amount', disabled: false };
    return { mode: 'verify', main: 'bet.main.verify', subKind: 'unknown', disabled: true };
  }
  if (p.queued) return { mode: 'queued', main: 'bet.main.queued', subKind: 'cancel', disabled: false };
  if (c.phase === 'betting') return { mode: 'bet', main: 'bet.main.bet', subKind: 'amount', disabled: false };
  return { mode: 'next', main: 'bet.main.bet', subKind: 'next', disabled: false };
}
