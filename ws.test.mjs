// ws.test.mjs — tests de ws.js avec un faux WebSocket (aucun réseau, aucune écriture financière)
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

class FakeWS extends EventTarget {
  static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
  static last = null;
  constructor(url) { super(); this.url = url; this.readyState = 0; this.sent = []; FakeWS.last = this; }
  send(d) { this.sent.push(JSON.parse(d)); }
  close(code = 1000) { this.readyState = 3; this.dispatchEvent(Object.assign(new Event('close'), { code })); }
  _open() { this.readyState = 1; this.dispatchEvent(new Event('open')); }
  _rx(o) { this.dispatchEvent(Object.assign(new Event('message'), { data: JSON.stringify(o) })); }
}
globalThis.WebSocket = FakeWS;
globalThis.document = { hidden: false };

const src = readFileSync(new URL('./ws.js', import.meta.url), 'utf8');
const { CrashSocket } = await import('data:text/javascript;base64,' + Buffer.from(src).toString('base64'));

const s = new CrashSocket({ url: 'ws://127.0.0.1:3000', token: 'T' });
const ev = { player: [], broadcast: [], stale: [], proto: [] };
s.addEventListener('playerEvent', (e) => ev.player.push(e.detail));
s.addEventListener('broadcast', (e) => ev.broadcast.push(e.detail));
s.addEventListener('staleBet', (e) => ev.stale.push(e.detail));
s.addEventListener('protocolError', (e) => ev.proto.push(e.detail));

s.connect();
let ws = FakeWS.last;
ws._open();
const lastSent = () => ws.sent[ws.sent.length - 1];
let n = 0;
const ok = (name) => console.log('OK', ++n, name);

// 1. Rejet indéterminé : betId + paramètres d'origine conservés au retry
assert.equal(s.bet('A', 100, null), 'sent');
const id1 = lastSent().betId;
ws._rx({ type: 'BET_REJECTED', data: { slot: 'A', reason: 'débit indéterminé, réessayez' } });
assert.equal(ev.player.at(-1).indeterminate, true);
assert.equal(s.bet('A', 999, 2), 'sent');
assert.equal(lastSent().betId, id1);
assert.equal(lastSent().amount, 100);
assert.equal(lastSent().autoCashout, null);
ok('rejet indéterminé : même betId et mêmes paramètres au retry');

// 2. Réponse définitive : betId libéré
ws._rx({ type: 'BET_ACCEPTED', data: { slot: 'A' } });
assert.equal(s.bet('A', 50, null), 'sent');
assert.notEqual(lastSent().betId, id1);
ws._rx({ type: 'BET_REJECTED', data: { slot: 'A', reason: 'montant invalide' } });
assert.equal(ev.player.at(-1).indeterminate, undefined);
ok('réponse définitive : nouveau betId au pari suivant');

// 3. Anti double envoi
assert.equal(s.bet('B', 10, null), 'sent');
const cnt = ws.sent.length;
assert.equal(s.bet('B', 10, null), 'busy');
assert.equal(ws.sent.length, cnt);
ok('double envoi sur un slot en vol : bloqué');

// 4. Changement de manche avec pari en vol : staleBet + réponse tardive marquée, betId purgé
ws._rx({ type: 'ROUND_START', data: { roundId: 'r1' } });
assert.equal(ev.stale.length, 1);
assert.equal(ev.stale[0].slot, 'B');
assert.equal(ev.broadcast.at(-1).replay, undefined);
ws._rx({ type: 'BET_ACCEPTED', data: { slot: 'B' } });
assert.equal(ev.player.at(-1).stale, true);
ok('changement de manche : pari en vol signalé, réponse tardive marquée stale');

// 5. ROUND_START rejoué (même roundId) = replay
ws._rx({ type: 'ROUND_START', data: { roundId: 'r1' } });
assert.equal(ev.broadcast.at(-1).replay, true);
ok('ROUND_START rejoué marqué replay');

// 6. TICK hors ordre / CRASH dupliqué / TICK après CRASH
ws._rx({ type: 'ROUND_START', data: { roundId: 'r2' } });
const b0 = ev.broadcast.length;
ws._rx({ type: 'TICK', data: { multiplier: 1.5 } });
ws._rx({ type: 'TICK', data: { multiplier: 1.2 } });
ws._rx({ type: 'CRASH', data: { roundId: 'r2', crashPoint: 1.5 } });
ws._rx({ type: 'CRASH', data: { roundId: 'r2', crashPoint: 1.5 } });
ws._rx({ type: 'TICK', data: { multiplier: 1.6 } });
assert.equal(ev.broadcast.length - b0, 2);
ok('TICK hors ordre, CRASH dupliqué et TICK après CRASH ignorés');

// 7. Reconnexion en plein vol : les TICK reprennent
ws.close(1006);
s.connect();
ws = FakeWS.last;
ws._open();
const b1 = ev.broadcast.length;
ws._rx({ type: 'TICK', data: { multiplier: 2.0 } });
assert.equal(ev.broadcast.length - b1, 1);
ok('reconnexion : flux TICK repris');

// 8. Message invalide
ws.dispatchEvent(Object.assign(new Event('message'), { data: '{pas du json' }));
assert.ok(ev.proto.some((p) => p.kind === 'parse'));
ok('message invalide : protocolError émis, pas de crash');

s.close();
console.log('TOUS LES TESTS OK (' + n + ')');
process.exit(0);
