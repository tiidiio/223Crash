// bets.js — 223CRASH panneaux A/B (Lot 1 : fiabilité financière)
// Règles : solde affiché = BALANCE serveur UNIQUEMENT (aucun débit/crédit optimiste) ;
// issue inconnue = « À VÉRIFIER » (jamais convertie en échec ni en succès) ;
// réponse tardive jamais appliquée à la manche courante ; un auto-bet par manche et par panneau.

const SLOTS = ['A', 'B'];
const LS_KEY = '223crash.bets.v2';
const SYNC_GRACE_MS = 200;
const RESYNC_DEBOUNCE_MS = 400;
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
  const st = { phase: 'idle', mult: 1, balance: null, connected: false, roundSeq: 0, resyncTimer: 0 };
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
      unconfirmed: false, crashFlashed: false, firedSeq: -1,
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

  // sticky = message « À VÉRIFIER » qui reste affiché jusqu'au prochain message.
  function flash(p, text, kind, sticky = false) {
    clearTimeout(p.flashTimer);
    p.resultEl.textContent = text;
    p.resultEl.dataset.kind = kind;
    p.resultEl.hidden = false;
    if (!sticky) p.flashTimer = setTimeout(() => { p.resultEl.hidden = true; }, 4000);
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
    else if (p.unconfirmed) {
      if (st.phase === 'betting') { mode = 'bet'; main = 'RÉESSAYER'; sub = amt + auto; }
      else { mode = 'verify'; main = 'À VÉRIFIER'; sub = 'résultat inconnu'; disabled = true; }
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
    const lock = !c || p.active || p.pending || p.unconfirmed;
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

  // Resynchronisation du solde (le gateway répond à 'join' par BALANCE) — anti-rafale.
  function scheduleResync() {
    if (st.resyncTimer) return;
    st.resyncTimer = setTimeout(() => {
      st.resyncTimer = 0;
      try { socket.join(); } catch (_) {}
    }, RESYNC_DEBOUNCE_MS);
  }

  function placeBet(p, fromQueue) {
    const retry = p.unconfirmed;
    const stake = Math.round(Number(p.amountEl.value) * 100) / 100;
    if (!(stake >= 1)) return fail(p, 'Mise min. 1', fromQueue);
    if (!retry && st.balance != null && stake > st.balance + 1e-9) return fail(p, 'Solde insuffisant', fromQueue);
    const raw = p.autoEl.value.trim();
    const auto = raw === '' ? null : Number(raw);
    if (auto !== null && !(auto >= 1.01)) return fail(p, 'Auto-cashout ≥ 1.01x', fromQueue);

    p.stake = stake;
    p.cashing = false;
    setPending(p, true);
    const res = socket.bet(p.slot, stake, auto); // 'sent' | 'busy' | 'offline'
    if (res === 'sent') return true;
    setPending(p, false);
    if (res === 'busy') return false;
    return fail(p, 'Hors ligne', fromQueue);
  }

  function onAction(p) {
    if (!st.connected || p.pending) return;
    if (p.active) {
      if (st.phase !== 'flying') return;
      p.cashMult = st.mult;
      p.cashing = true;
      setPending(p, true);
      const res = socket.cashout(p.slot);
      if (res !== 'sent') setPending(p, false);
      return;
    }
    if (p.unconfirmed && st.phase !== 'betting') return;
    if (p.queued) { p.queued = false; render(p); return; }
    if (st.phase === 'betting') placeBet(p, false);
    else { p.queued = true; render(p); }
  }

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

  socket.addEventListener('balance', (e) => {
    const b = Number(e.detail && e.detail.balance);
    st.balance = Number.isFinite(b) ? b : null;
    showBalance();
    renderAll();
  });

  socket.addEventListener('status', (e) => setInteractive(e.detail.state === 'connected'));

  socket.addEventListener('actionTimeout', (e) => {
    const d = (e && e.detail) || {};
    const p = bySlot(d.slot);
    if (!p) return;
    const action = d.action || (p.cashing ? 'cashout' : 'bet');
    setPending(p, false);
    if (action === 'bet') {
      p.unconfirmed = true;
      flash(p, 'À VÉRIFIER · mise non confirmée', 'warn', true);
    } else {
      flash(p, 'À VÉRIFIER · encaissement non confirmé', 'warn', true);
    }
    scheduleResync();
    render(p);
  });

  // Pari non résolu au changement de manche : jamais réutilisé, jamais converti en échec.
  socket.addEventListener('staleBet', (e) => {
    const p = bySlot(e.detail && e.detail.slot);
    if (!p) return;
    p.unconfirmed = false;
    p.pending = false;
    flash(p, 'À VÉRIFIER · manche précédente', 'warn', true);
    scheduleResync();
    render(p);
  });

  socket.addEventListener('playerEvent', (e) => {
    const { type, data, indeterminate, stale } = e.detail || {};
    const p = bySlot(data && data.slot);
    if (!p) return;

    if (stale) { // réponse tardive d'une manche précédente : jamais appliquée à la manche courante
      if (type === 'BET_ACCEPTED') flash(p, 'À VÉRIFIER · confirmation tardive', 'warn', true);
      scheduleResync();
      return;
    }

    p.pending = false;
    p.cashing = false;

    if (type === 'BET_ACCEPTED') {
      p.active = true;
      p.unconfirmed = false;
      const amt = Number(data && data.amount);
      if (Number.isFinite(amt) && amt > 0) p.stake = amt;
      buzz(15);
    } else if (type === 'BET_REJECTED') {
      p.active = false;
      if (indeterminate) { // débit en cours / indéterminé : on garde le betId, on ne rembourse rien
        p.unconfirmed = true;
        flash(p, 'À VÉRIFIER · ' + ((data && data.reason) || 'débit en cours'), 'warn', true);
      } else {
        p.unconfirmed = false;
        flash(p, (data && data.reason) || 'Mise refusée', 'lose');
        if (p.autoBetEl.checked) { p.autoBetEl.checked = false; persist(); }
        buzz(80);
      }
      scheduleResync();
    } else if (type === 'CASHOUT_ACCEPTED') {
      const payout = Number(data && data.payout);
      if (Number.isFinite(payout) && payout >= 0) {
        const m = p.stake > 0 ? payout / p.stake : NaN;
        flash(p, `+${fmt(payout)}${Number.isFinite(m) ? ' @ ' + m.toFixed(2) + 'x' : ''}`, 'win');
      } else {
        flash(p, 'Encaissé · montant à vérifier', 'warn', true);
      }
      p.active = false;
      p.unconfirmed = false;
      buzz([30, 40, 30]);
      scheduleResync(); // le solde vient du serveur, pas d'un calcul local
    } else if (type === 'CASHOUT_REJECTED') {
      const reason = data && data.reason;
      if (reason === 'round_crashed') {
        if (!p.crashFlashed) { flash(p, `−${fmt(p.stake)} · crash`, 'lose'); p.crashFlashed = true; }
        p.active = false;
      } else if (reason === 'aucun pari actif') {
        p.active = false;
        flash(p, 'Aucune mise active', 'lose');
      } else {
        flash(p, 'Encaissement refusé', 'lose');
      }
      buzz(80);
      scheduleResync();
    }
    render(p);
  });

  socket.addEventListener('broadcast', (e) => {
    const { type, data, replay } = e.detail || {};
    if (type === 'ROUND_START') {
      st.phase = 'betting';
      st.mult = 1;
      if (!replay) {
        st.roundSeq++;
        panels.forEach((p) => {
          p.active = false; p.pending = false; p.cashing = false;
          p.unconfirmed = false; p.crashFlashed = false;
        });
      }
      const seq = st.roundSeq;
      renderAll();
      setTimeout(() => {
        if (seq !== st.roundSeq || st.phase !== 'betting') return;
        panels.forEach((p) => {
          if (p.active || p.pending || p.unconfirmed || p.firedSeq === seq) return;
          if (p.queued || p.autoBetEl.checked) { p.queued = false; p.firedSeq = seq; placeBet(p, true); }
        });
      }, SYNC_GRACE_MS);
    } else if (type === 'ROUND_RUNNING') {
      st.phase = 'flying'; // paris fermés (événement réel du moteur)
      renderAll();
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
          const ambiguous = ac !== null && Number.isFinite(cp) && cp >= ac; // auto-cashout peut être en vol
          if (!ambiguous) {
            flash(p, `−${fmt(p.stake)} · ${Number.isFinite(cp) ? cp.toFixed(2) : '?'}x`, 'lose');
            p.crashFlashed = true;
          }
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
    if (!enabled) {
      panels.forEach((p) => {
        if (p.pending && !p.cashing) {
          p.unconfirmed = true;
          flash(p, 'À VÉRIFIER · connexion perdue', 'warn', true);
        }
        p.pending = false; p.cashing = false; p.queued = false;
      });
    }
    renderAll();
  }

  renderAll();
  return { setInteractive };
}
