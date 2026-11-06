// bets.js — panneaux de mise 223CRASH
// Bouton intelligent : MISER -> MISE PLACÉE -> ENCAISSER (gain + multiplicateur en direct)
// + mise en file pour le tour suivant, auto-bet, auto-cashout, ½ / ×2 / MAX, persistance locale.

const SLOTS = ['A', 'B'];
const LS_KEY = '223crash.bets.v1';
const fmt = (n) => Number(n).toFixed(2);
const buzz = (p) => { try { navigator.vibrate && navigator.vibrate(p); } catch (_) {} };

function loadPrefs() {
  try { return JSON.parse(localStorage.getItem(LS_KEY)) || {}; } catch (_) { return {}; }
}
function savePrefs(v) {
  try { localStorage.setItem(LS_KEY, JSON.stringify(v)); } catch (_) {}
}

const tpl = (slot, pf) => `
<div class="bet-panel" data-slot="${slot}" data-active="false" data-queued="false" data-pending="false">
  <div class="panel-head">
    <span class="slot-label">MISE ${slot}</span>
    <span class="result" hidden></span>
    <label class="auto-toggle"><input type="checkbox" class="auto-bet"${pf.autoBet ? ' checked' : ''}><span>AUTO</span></label>
  </div>
  <div class="stake-row">
    <button type="button" class="step" data-act="half">½</button>
    <input type="number" inputmode="decimal" class="amount" min="1" step="1" value="${Number(pf.amount) >= 1 ? Number(pf.amount) : 100}">
    <button type="button" class="step" data-act="double">×2</button>
  </div>
  <div class="quick-stakes">
    <button type="button" data-add="100">+100</button>
    <button type="button" data-add="500">+500</button>
    <button type="button" data-add="1000">+1000</button>
    <button type="button" data-act="max">MAX</button>
  </div>
  <div class="auto-row">
    <span>Auto-cashout</span>
    <input type="number" inputmode="decimal" class="auto-cashout" placeholder="x —" min="1.01" step="0.01" value="${Number(pf.auto) >= 1.01 ? Number(pf.auto) : ''}">
  </div>
  <button type="button" class="action-btn" data-mode="off" disabled><span class="b-main">MISER</span><span class="b-sub"></span></button>
</div>`;

export function initBets({ socket, panelsEl }) {
  const st = { phase: 'idle', mult: 1, balance: null, connected: false };
  const prefs = loadPrefs();

  panelsEl.innerHTML = SLOTS.map((s) => tpl(s, prefs[s] || {})).join('');

  const panels = SLOTS.map((slot) => {
    const el = panelsEl.querySelector(`.bet-panel[data-slot="${slot}"]`);
    return {
      slot, el,
      amountEl: el.querySelector('.amount'),
      autoEl: el.querySelector('.auto-cashout'),
      autoBetEl: el.querySelector('.auto-bet'),
      btn: el.querySelector('.action-btn'),
      mainEl: el.querySelector('.b-main'),
      subEl: el.querySelector('.b-sub'),
      resultEl: el.querySelector('.result'),
      stepBtns: [...el.querySelectorAll('[data-act],[data-add]')],
      active: false, pending: false, cashing: false, queued: false,
      stake: 0, cashMult: null, flashTimer: null, key: '',
    };
  });
  const bySlot = (slot) => panels.find((p) => p.slot === slot);

  function persist() {
    const out = {};
    panels.forEach((p) => {
      out[p.slot] = { amount: Number(p.amountEl.value) || 100, auto: p.autoEl.value, autoBet: p.autoBetEl.checked };
    });
    savePrefs(out);
  }

  function flash(p, text, kind) {
    clearTimeout(p.flashTimer);
    p.resultEl.textContent = text;
    p.resultEl.dataset.kind = kind;
    p.resultEl.hidden = false;
    p.flashTimer = setTimeout(() => { p.resultEl.hidden = true; }, 4000);
  }

  const cap = (v) => (st.balance != null && st.balance >= 1 ? Math.min(v, Math.floor(st.balance)) : v);

  function setAmount(p, v) {
    p.amountEl.value = String(Math.max(1, Math.floor(v * 100) / 100));
    persist();
    render(p);
  }

  function render(p) {
    const c = st.connected;
    let mode, main, sub = '', disabled = false;
    const amt = fmt(Number(p.amountEl.value) || 0);
    const auto = p.autoBetEl.checked ? ' · AUTO' : '';

    if (!c) { mode = 'off'; main = 'HORS LIGNE'; disabled = true; }
    else if (p.pending) { mode = 'wait'; main = p.cashing ? 'ENCAISSEMENT…' : 'ENVOI…'; disabled = true; }
    else if (p.active) {
      if (st.phase === 'flying') {
        mode = 'cash'; main = 'ENCAISSER';
        sub = `${fmt(p.stake * st.mult)} · ${st.mult.toFixed(2)}x`;
      } else { mode = 'placed'; main = 'MISE PLACÉE'; sub = fmt(p.stake); disabled = true; }
    }
    else if (p.queued) { mode = 'queued'; main = 'EN ATTENTE'; sub = 'tap pour annuler'; }
    else if (st.phase === 'betting') { mode = 'bet'; main = 'MISER'; sub = amt + auto; }
    else { mode = 'next'; main = 'MISER'; sub = 'tour suivant · ' + amt + auto; }

    const key = [mode, main, sub, disabled].join('|');
    if (key !== p.key) {
      p.key = key;
      p.btn.dataset.mode = mode;
      p.mainEl.textContent = main;
      p.subEl.textContent = sub;
      p.btn.disabled = disabled;
    }
    p.el.dataset.active = p.active ? 'true' : 'false';
    p.el.dataset.queued = p.queued ? 'true' : 'false';
    p.el.dataset.pending = p.pending ? 'true' : 'false';

    const lock = !c || p.active || p.pending;
    p.amountEl.disabled = lock;
    p.autoEl.disabled = lock;
    p.stepBtns.forEach((b) => { b.disabled = lock; });
    p.autoBetEl.disabled = !c;
  }
  const renderAll = () => panels.forEach(render);

  function setPending(p, v) {
    p.pending = v;
    if (!v) p.cashing = false;
    render(p);
  }

  function fail(p, msg, fromQueue) {
    flash(p, msg, 'lose');
    buzz(80);
    if (fromQueue) {
      p.queued = false;
      if (p.autoBetEl.checked) { p.autoBetEl.checked = false; persist(); }
    }
    render(p);
    return false;
  }

  function showBalance() {
    const el = document.getElementById('balance-display');
    if (el && st.balance != null) el.textContent = Number(st.balance).toFixed(2);
  }
  function optimisticDebit(p, stake) {
    if (st.balance == null) return;
    st.balance = Math.round((st.balance - stake) * 100) / 100;
    p.debited = stake;
    if (Date.now() < (st.holdUntil || 0)) st.holdValue = Math.round((st.holdValue - stake) * 100) / 100;
    showBalance();
    renderAll();
  }
  function optimisticRefund(p) {
    if (p.debited && st.balance != null) {
      st.balance = Math.round((st.balance + p.debited) * 100) / 100;
      showBalance();
      renderAll();
    }
    p.debited = 0;
  }

  function placeBet(p, fromQueue) {
    const stake = Math.round(Number(p.amountEl.value) * 100) / 100;
    if (!(stake >= 1)) return fail(p, 'Mise min. 1', fromQueue);
    if (st.balance != null && stake > st.balance + 1e-9) return fail(p, 'Solde insuffisant', fromQueue);
    const raw = p.autoEl.value.trim();
    const auto = raw === '' ? null : Number(raw);
    if (auto !== null && !(auto >= 1.01)) return fail(p, 'Auto-cashout ≥ 1.01x', fromQueue);

    p.stake = stake;
    p.cashing = false;
    setPending(p, true);
    const sent = socket.bet(p.slot, stake, auto);
    if (sent) optimisticDebit(p, stake);
    if (!sent) { setPending(p, false); return fail(p, 'Hors ligne', fromQueue); }
    return true;
  }

  function onAction(p) {
    if (!st.connected || p.pending) return;
    if (p.active) {
      if (st.phase !== 'flying') return;
      p.cashMult = st.mult;
      p.cashing = true;
      setPending(p, true);
      const sent = socket.cashout(p.slot);
      if (!sent) setPending(p, false);
      return;
    }
    if (p.queued) { p.queued = false; render(p); return; }
    if (st.phase === 'betting') placeBet(p, false);
    else { p.queued = true; render(p); }
  }

  // ---- UI ----
  panels.forEach((p) => {
    p.btn.addEventListener('click', () => onAction(p));
    p.amountEl.addEventListener('input', () => { persist(); render(p); });
    p.autoEl.addEventListener('input', persist);
    p.autoBetEl.addEventListener('change', () => { persist(); render(p); });
    p.stepBtns.forEach((b) => b.addEventListener('click', () => {
      const cur = Number(p.amountEl.value) || 0;
      const act = b.dataset.act;
      if (act === 'half') setAmount(p, cur / 2);
      else if (act === 'double') setAmount(p, cap(cur * 2));
      else if (act === 'max') { if (st.balance != null) setAmount(p, Math.floor(st.balance)); }
      else setAmount(p, cap(cur + Number(b.dataset.add)));
    }));
  });

  // ---- événements serveur ----
  socket.addEventListener('balance', (e) => {
    const b = Number(e.detail && e.detail.balance);
    if (Number.isFinite(b) && Date.now() < (st.holdUntil || 0) && b < st.holdValue - 1e-9) return;
    st.balance = Number.isFinite(b) ? b : null;
    showBalance();
    renderAll();
  });

  socket.addEventListener('status', (e) => setInteractive(e.detail.state === 'connected'));

  socket.addEventListener('actionTimeout', (e) => {
    const p = bySlot(e.detail && e.detail.slot);
    if (!p) return;
    setPending(p, false);
    optimisticRefund(p);
    flash(p, 'Pas de réponse serveur', 'lose');
  });

  socket.addEventListener('playerEvent', (e) => {
    const { type, data } = e.detail || {};
    const p = bySlot(data && data.slot);
    if (!p) return;
    p.pending = false;
    p.cashing = false;

    if (type === 'BET_ACCEPTED') {
      p.active = true;
      p.debited = 0;
      buzz(15);
    } else if (type === 'BET_REJECTED') {
      p.active = false;
      optimisticRefund(p);
      flash(p, (data && data.reason) || 'Mise refusée', 'lose');
      if (p.autoBetEl.checked) { p.autoBetEl.checked = false; persist(); }
      buzz(80);
    } else if (type === 'CASHOUT_ACCEPTED') {
      const m = Number(data.multiplier ?? data.cashoutMultiplier ?? p.cashMult ?? st.mult);
      const payout = Number(data.payout ?? data.win ?? data.winAmount ?? p.stake * m);
      if (Number.isFinite(payout) && payout > 0 && st.balance != null) {
        st.balance = Math.round((st.balance + payout) * 100) / 100;
        st.holdValue = st.balance;
        st.holdUntil = Date.now() + 7000;
        showBalance();
        renderAll();
      }
      p.active = false;
      flash(p, `+${fmt(payout)} @ ${m.toFixed(2)}x`, 'win');
      buzz([30, 40, 30]);
    } else if (type === 'CASHOUT_REJECTED') {
      p.active = false;
      flash(p, 'Encaissement refusé', 'lose');
      buzz(80);
    }
    render(p);
  });

  socket.addEventListener('broadcast', (e) => {
    const { type, data } = e.detail || {};
    if (type === 'ROUND_START') {
      st.phase = 'betting';
      st.mult = 1;
      panels.forEach((p) => {
        p.active = false; p.pending = false; p.cashing = false;
        if (p.queued || p.autoBetEl.checked) { p.queued = false; placeBet(p, true); }
        render(p);
      });
    } else if (type === 'TICK') {
      const m = Number(data && data.multiplier);
      if (!Number.isFinite(m)) return;
      st.phase = 'flying';
      st.mult = m;
      renderAll();
    } else if (type === 'CRASH') {
      const cp = Number(data && data.crashPoint);
      st.phase = 'crashed';
      if (Number.isFinite(cp)) st.mult = cp;
      panels.forEach((p) => {
        if (p.active && !p.cashing) {
          const ac = Number(p.autoEl.value) >= 1.01 ? Number(p.autoEl.value) : null;
          if (ac && Number.isFinite(cp) && cp >= ac) flash(p, `+${fmt(p.stake * ac)} @ ${ac.toFixed(2)}x`, 'win');
          else flash(p, `−${fmt(p.stake)} · ${Number.isFinite(cp) ? cp.toFixed(2) : '?'}x`, 'lose');
          p.active = false;
          p.pending = false;
        }
        render(p);
      });
    }
  });

  function setInteractive(enabled) {
    st.connected = !!enabled;
    panelsEl.classList.toggle('panels-disabled', !enabled);
    if (!enabled) panels.forEach((p) => { p.pending = false; p.cashing = false; p.queued = false; });
    renderAll();
  }

  renderAll();
  return { setInteractive };
}
