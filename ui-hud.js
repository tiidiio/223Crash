// ui-hud.js — affichage autour des panneaux de mise : multiplicateur, phase, compte à rebours,
// historique, état de connexion, son, fenêtre d'équité. Ne touche jamais au solde ni aux mises
// (c'est le rôle de bets.js).
import { CONFIG } from './ui-config.js';

const STATUS = {
  connected: ['En ligne', 'on'],
  disconnected: ['Déconnecté', 'off'],
  reconnecting: ['Reconnexion…', 'warn'],
  stale: ['Connexion instable', 'warn'],
  error: ['Erreur réseau', 'off'],
  replaced: ['Session remplacée', 'off'],
};

const fmtX = (n) => Number(n).toFixed(2) + 'x';

function loadSound() { try { return localStorage.getItem(CONFIG.soundKey) === '1'; } catch (_) { return false; } }
function saveSound(v) { try { localStorage.setItem(CONFIG.soundKey, v ? '1' : '0'); } catch (_) {} }

export function initHud({ socket, canvas, els }) {
  const s = { roundId: '', hash: '', last: null, betT0: 0, timer: 0, sound: loadSound(), audio: null, toastTimer: 0 };

  // ---- utilitaires ----
  function toast(text) {
    els.toastEl.textContent = text;
    els.toastEl.classList.add('show');
    clearTimeout(s.toastTimer);
    s.toastTimer = setTimeout(() => els.toastEl.classList.remove('show'), 2800);
  }

  function beep(freq, ms, type = 'sine', gain = 0.06) {
    if (!s.sound) return;
    try {
      if (!s.audio) {
        const AC = window.AudioContext || window.webkitAudioContext;
        if (!AC) return;
        s.audio = new AC();
      }
      const a = s.audio;
      if (a.state === 'suspended') a.resume();
      const o = a.createOscillator();
      const g = a.createGain();
      o.type = type; o.frequency.value = freq; g.gain.value = gain;
      o.connect(g); g.connect(a.destination);
      o.start(); o.stop(a.currentTime + ms / 1000);
    } catch (_) {}
  }

  function setPhase(phase, label) {
    els.stageEl.dataset.phase = phase;
    els.phaseEl.textContent = label;
  }

  function setMult(text, state) {
    els.multEl.textContent = text;
    els.multEl.dataset.state = state;
  }

  // ---- compte à rebours (fenêtre de mise) ----
  function stopCountdown() {
    clearInterval(s.timer);
    s.timer = 0;
    els.barEl.style.width = '0%';
  }
  function startCountdown() {
    stopCountdown();
    // Horloge locale : on part du moment où ROUND_START est reçu (approximation à la latence près).
    s.betT0 = performance.now();
    const step = () => {
      const remain = Math.max(0, CONFIG.betWindowMs - (performance.now() - s.betT0));
      els.barEl.style.width = (remain / CONFIG.betWindowMs) * 100 + '%';
      if (remain > 0) els.phaseEl.textContent = `Mises ouvertes · ${(remain / 1000).toFixed(1)}s`;
      else { els.phaseEl.textContent = 'Fermeture des mises…'; clearInterval(s.timer); s.timer = 0; }
    };
    step();
    s.timer = setInterval(step, 100);
  }

  // ---- historique ----
  function addHistory(cp) {
    if (!Number.isFinite(cp)) return;
    const chip = document.createElement('span');
    chip.className = 'hchip ' + (cp < 2 ? 'lo' : cp < 10 ? 'mid' : 'hi');
    chip.textContent = fmtX(cp);
    els.historyEl.prepend(chip);
    while (els.historyEl.children.length > CONFIG.historySize) els.historyEl.lastElementChild.remove();
  }

  // ---- équité ----
  function refreshFair() {
    els.fairRound.textContent = s.roundId || '—';
    els.fairHash.textContent = s.hash || '—';
    els.fairLast.textContent = s.last ? `${s.last.roundId} · ${fmtX(s.last.crashPoint)}` : '—';
  }
  els.fairBtn.addEventListener('click', () => {
    refreshFair();
    if (typeof els.fairDlg.showModal === 'function') els.fairDlg.showModal();
    else els.fairDlg.setAttribute('open', '');
  });
  els.fairClose.addEventListener('click', () => {
    if (typeof els.fairDlg.close === 'function') els.fairDlg.close();
    else els.fairDlg.removeAttribute('open');
  });
  els.fairCopy.addEventListener('click', async () => {
    if (!s.hash) { toast('Aucun hash disponible'); return; }
    try { await navigator.clipboard.writeText(s.hash); toast('Hash copié'); }
    catch (_) { toast('Copie impossible'); }
  });

  // ---- son ----
  function renderSoundBtn() {
    els.soundBtn.textContent = s.sound ? '🔊' : '🔇';
    els.soundBtn.setAttribute('aria-pressed', s.sound ? 'true' : 'false');
    els.soundBtn.setAttribute('aria-label', s.sound ? 'Couper le son' : 'Activer le son');
  }
  els.soundBtn.addEventListener('click', () => {
    s.sound = !s.sound;
    saveSound(s.sound);
    renderSoundBtn();
    beep(660, 90);
  });
  renderSoundBtn();

  // ---- session remplacée ----
  els.replacedReload.addEventListener('click', () => location.reload());

  // ---- événements serveur ----
  socket.addEventListener('status', (e) => {
    const state = e.detail && e.detail.state;
    const [label, kind] = STATUS[state] || [String(state || '—'), 'warn'];
    els.statusEl.textContent = label;
    els.statusEl.dataset.state = kind;
    if (state === 'replaced') els.replacedEl.hidden = false;
    if (state === 'connected') els.replacedEl.hidden = true;
  });

  socket.addEventListener('broadcast', (e) => {
    const { type, data } = e.detail || {};
    if (type === 'ROUND_START') {
      s.roundId = (data && data.roundId) || '';
      s.hash = (data && data.serverSeedHash) || '';
      canvas.reset();
      setMult('1.00x', 'idle');
      startCountdown();
      refreshFair();
    } else if (type === 'TICK') {
      const m = Number(data && data.multiplier);
      if (!Number.isFinite(m)) return;
      if (els.stageEl.dataset.phase !== 'flying') { stopCountdown(); setPhase('flying', 'En vol'); }
      setMult(fmtX(m), 'flying');
      canvas.tick(m);
    } else if (type === 'CRASH') {
      const cp = Number(data && data.crashPoint);
      stopCountdown();
      setPhase('crashed', 'Crash');
      if (Number.isFinite(cp)) setMult(fmtX(cp), 'crashed');
      else setMult('CRASH', 'crashed');
      canvas.crash(cp);
      addHistory(cp);
      s.last = { roundId: (data && data.roundId) || '', crashPoint: cp };
      refreshFair();
      beep(180, 260, 'sawtooth', 0.05);
    }
  });

  socket.addEventListener('playerEvent', (e) => {
    const type = e.detail && e.detail.type;
    if (type === 'CASHOUT_ACCEPTED') beep(880, 140);
    else if (type === 'BET_ACCEPTED') beep(520, 60);
  });

  socket.addEventListener('serverError', (e) => {
    const reason = e.detail && e.detail.reason;
    if (reason) toast(String(reason));
  });

  return { toast };
}
