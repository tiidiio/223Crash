// bet-state.test.mjs — tests de la logique pure des panneaux (aucun DOM, aucun réseau)
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./bet-state.js', import.meta.url), 'utf8');
const { panelView, reasonKey } = await import('data:text/javascript;base64,' + Buffer.from(src).toString('base64'));

const base = { pending: false, cashing: false, active: false, unconfirmed: false, queued: false };
const view = (over, c) => panelView({ ...base, ...over }, { connected: true, phase: 'betting', ...c });
let n = 0;
const ok = (x) => console.log('OK', ++n, x);

assert.deepEqual(panelView(base, { connected: false, phase: 'flying' }), { mode: 'off', main: 'bet.main.offline', subKind: '', disabled: true });
ok('hors ligne : bouton désactivé');

assert.equal(view({}).mode, 'bet');
assert.equal(view({}).disabled, false);
assert.equal(view({}, { phase: 'flying' }).mode, 'next');
assert.equal(view({}, { phase: 'crashed' }).mode, 'next');
ok('mises ouvertes : MISER ; sinon tour suivant');

assert.equal(view({ pending: true }).main, 'bet.main.sending');
assert.equal(view({ pending: true, cashing: true }).main, 'bet.main.cashing');
assert.equal(view({ pending: true }).disabled, true);
ok('requête en vol : bouton désactivé (anti double clic)');

assert.equal(view({ active: true }, { phase: 'betting' }).mode, 'placed');
assert.equal(view({ active: true }, { phase: 'betting' }).disabled, true);
assert.equal(view({ active: true }, { phase: 'flying' }).mode, 'cash');
assert.equal(view({ active: true }, { phase: 'flying' }).disabled, false);
ok('pari actif : ENCAISSER seulement en vol');

assert.equal(view({ unconfirmed: true }, { phase: 'betting' }).main, 'bet.main.retry');
assert.equal(view({ unconfirmed: true }, { phase: 'flying' }).mode, 'verify');
assert.equal(view({ unconfirmed: true }, { phase: 'flying' }).disabled, true);
assert.equal(view({ unconfirmed: true }, { phase: 'crashed' }).main, 'bet.main.verify');
ok('résultat inconnu : RÉESSAYER pendant les mises, sinon À VÉRIFIER (jamais échec ni succès)');

assert.equal(view({ queued: true }, { phase: 'flying' }).mode, 'queued');
assert.equal(view({ queued: true }, { phase: 'flying' }).disabled, false);
ok('pari en file : annulable');

assert.equal(view({ pending: true, active: true }).main, 'bet.main.sending');
assert.equal(view({ unconfirmed: true, queued: true }, { phase: 'betting' }).main, 'bet.main.retry');
ok('priorités : pending > actif > inconnu > file');

assert.equal(reasonKey('Montant invalide'), 'bet.reason.invalidAmount');
assert.equal(reasonKey(' débit indéterminé, réessayez '), 'bet.reason.indeterminate');
assert.equal(reasonKey('aucun pari actif'), 'bet.reason.noActive');
assert.equal(reasonKey('raison inconnue'), null);
assert.equal(reasonKey(undefined), null);
ok('raisons du gateway : connues traduites, inconnues laissées brutes');

console.log('TOUS LES TESTS OK (' + n + ')');
