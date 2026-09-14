// test-flow.mjs v2 — n'écrit RIEN sur stdout pendant les TICK (juste un compteur en
// mémoire) pour éliminer tout risque de blocage I/O terminal faussant les timings.
// Compare le timestamp CLIENT (Date.now) au timestamp SERVEUR embarqué dans "startsAt".
const token = process.argv[2];
if (!token) { console.error("usage: node test-flow.mjs <token>"); process.exit(1); }

const t0 = Date.now();
function ts() { return `+${((Date.now() - t0) / 1000).toFixed(2)}s`; }

const ws = new WebSocket(`ws://127.0.0.1:3000/?token=${encodeURIComponent(token)}`);
let betPlaced = false;
let lastRoundId = null;
let tickCount = 0;
let roundStartClientTs = null;

ws.addEventListener('open', () => console.log(ts(), '[open]'));

ws.addEventListener('message', (ev) => {
  const msg = JSON.parse(ev.data);

  if (msg.type === 'TICK') { tickCount++; return; } // aucun I/O ici

  console.log(ts(), '[recv]', JSON.stringify(msg));

  if (msg.type === 'ROUND_START' && !betPlaced) {
    lastRoundId = msg.data.roundId;
    roundStartClientTs = Date.now();
    console.log(ts(), '[send] bet A 100 autoCashout=1.05 (round', lastRoundId, ')');
    ws.send(JSON.stringify({ action: 'bet', slot: 'A', amount: 100, autoCashout: 1.05 }));
    betPlaced = true;
  }

  if (msg.type === 'BET_REJECTED') {
    const deltaMs = Date.now() - roundStartClientTs;
    console.log(ts(), `[ANALYSE] rejet ${deltaMs}ms après ROUND_START côté client (ticks reçus avant rejet: ${tickCount})`);
  }

  if (msg.type === 'CASHOUT_ACCEPTED' || msg.type === 'CRASH') {
    setTimeout(() => {
      console.log(ts(), '[fin test] roundId testé =', lastRoundId, 'ticks reçus au total:', tickCount);
      ws.close(1000);
      process.exit(0);
    }, 500);
  }
});

ws.addEventListener('close', (ev) => console.log(ts(), '[close]', ev.code, ev.reason));
ws.addEventListener('error', (ev) => console.log(ts(), '[error]', ev.message || ev));

setTimeout(() => { console.log(ts(), '[timeout global]'); process.exit(1); }, 30000);
