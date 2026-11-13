// ui-hud.js — HUD 223CRASH (Lot 2) : multiplicateur, phases, compte à rebours, historique,
// connexion, équité, règles, menu (langue/son/volume/plein écran)
import { CONFIG } from './ui-config.js';
import { t, languages, getLang, setLang, onLangChange } from './i18n.js';

const $ = (id) => document.getElementById(id);
const fmtX = (n) => Number(n).toFixed(2) + 'x';
const STATUS_KIND = { connecting: 'warn', connected: 'on', disconnected: 'off', reconnecting: 'warn', stale: 'warn', error: 'off', replaced: 'off' };

export function initHud({ socket, canvas, audio }) {
  const el = {
    stage: $('stage'), mult: $('mult'), phase: $('phase'), bar: $('count-bar'), history: $('history'),
    status: $('status-chip'), statusText: $('status-text'), toast: $('toast'), banner: $('conn-banner'), sr: $('sr-live'),
    replaced: $('replaced'), replacedReload: $('replaced-reload'),
    soundBtn: $('sound-btn'), menuBtn: $('menu-btn'),
    menuDlg: $('menu-dlg'), menuClose: $('menu-close'), lang: $('lang-select'), setSound: $('set-sound'),
    vol: $('vol'), fsRow: $('fs-row'), fs: $('fs-btn'), openRules: $('open-rules'), openFair: $('open-fair'),
    fairDlg: $('fair-dlg'), fairRound: $('fair-round'), fairHash: $('fair-hash'), fairLast: $('fair-last'),
    fairCopy: $('fair-copy'), fairClose: $('fair-close'), rulesDlg: $('rules-dlg'), rulesClose: $('rules-close'),
  };
  const s = {
    roundId: '', hash: '', last: null, skew: null, t0: 0, timer: 0, toastTimer: 0,
    status: 'connecting', connected: false, everConnected: false,
    phase: 'idle', phaseKey: 'phase.waiting', phaseVars: null, seen: new Set(),
  };

  function toast(text) {
    el.toast.textContent = text;
    el.toast.classList.add('show');
    clearTimeout(s.toastTimer);
    s.toastTimer = setTimeout(() => el.toast.classList.remove('show'), 2800);
  }
  const announce = (text) => { el.sr.textContent = text; };

  function openDlg(d) { if (typeof d.showModal === 'function') { if (!d.open) d.showModal(); } else d.setAttribute('open', ''); }
  function closeDlg(d) { if (typeof d.close === 'function') { if (d.open) d.close(); } else d.removeAttribute('open'); }
  [el.menuDlg, el.fairDlg, el.rulesDlg].forEach((d) => d.addEventListener('click', (ev) => { if (ev.target === d) closeDlg(d); }));

  function renderPhase() { el.phase.textContent = t(s.phaseKey, s.phaseVars); }
  function setPhase(phase, key, vars) {
    s.phase = phase; s.phaseKey = key; s.phaseVars = vars || null;
    if (el.stage.dataset.phase !== phase) el.stage.dataset.phase = phase;
    renderPhase();
  }
  function setMult(text, state) {
    if (el.mult.textContent !== text) el.mult.textContent = text;
    if (el.mult.dataset.state !== state) el.mult.dataset.state = state;
  }

  function renderStatus() {
    const label = t('status.' + s.status);
    el.status.dataset.state = STATUS_KIND[s.status] || 'warn';
    el.statusText.textContent = label;
    el.status.title = label;
    el.status.setAttribute('aria-label', label);
    el.banner.hidden = !(s.everConnected && !s.connected) || s.status === 'replaced';
  }

  // Compte à rebours calé sur startsAt (ms serveur). Horloge client calibrée sur le premier ROUND_START
  // reçu en direct ; sans calibration (rejeu après reconnexion), mode indéterminé : aucune durée inventée.
  function stopCountdown() {
    clearInterval(s.timer); s.timer = 0;
    el.bar.style.width = '0%';
    el.bar.classList.remove('indet');
  }
  function startCountdown(startsAt, replay) {
    stopCountdown();
    const win = CONFIG.betWindowMs;
    const now = Date.now();
    let elapsed = null;
    if (!replay && Number.isFinite(startsAt)) { s.skew = now - startsAt; elapsed = 0; }
    else if (replay && s.skew !== null && Number.isFinite(startsAt)) elapsed = Math.max(0, now - startsAt - s.skew);
    if (elapsed === null) { setPhase('betting', 'phase.betting.open'); el.bar.classList.add('indet'); return; }
    s.t0 = performance.now() - elapsed;
    const step = () => {
      const remain = Math.max(0, win - (performance.now() - s.t0));
      el.bar.style.width = (remain / win) * 100 + '%';
      if (remain > 0) setPhase('betting', 'phase.betting', { s: (remain / 1000).toFixed(1) });
      else { setPhase('betting', 'phase.closing'); clearInterval(s.timer); s.timer = 0; }
    };
    step();
    s.timer = setInterval(step, 100);
  }

  function renderHistoryEmpty() {
    if (el.history.querySelector('.hchip')) return;
    el.history.innerHTML = '';
    const e = document.createElement('span');
    e.className = 'hempty';
    e.textContent = t('history.empty');
    el.history.appendChild(e);
  }
  function addHistory(roundId, cp) {
    if (!Number.isFinite(cp)) return;
    if (roundId) {
      if (s.seen.has(roundId)) return;
      s.seen.add(roundId);
      if (s.seen.size > 200) s.seen.delete(s.seen.values().next().value);
    }
    const empty = el.history.querySelector('.hempty');
    if (empty) empty.remove();
    const chip = document.createElement('span');
    chip.className = 'hchip ' + (cp < 2 ? 'lo' : cp < 10 ? 'mid' : 'hi');
    chip.setAttribute('role', 'listitem');
    if (roundId) chip.title = roundId;
    chip.textContent = fmtX(cp);
    el.history.prepend(chip); // plus récent en premier
    while (el.history.children.length > CONFIG.historySize) el.history.lastElementChild.remove();
  }

  function refreshFair() {
    el.fairRound.textContent = s.roundId || '—';
    el.fairHash.textContent = s.hash || '—';
    el.fairLast.textContent = s.last && Number.isFinite(s.last.crashPoint) ? `${s.last.roundId || '—'} · ${fmtX(s.last.crashPoint)}` : '—';
  }
  el.fairCopy.addEventListener('click', async () => {
    if (!s.hash) { toast(t('toast.noHash')); return; }
    try { await navigator.clipboard.writeText(s.hash); toast(t('toast.hashCopied')); }
    catch (_) { toast(t('toast.copyFail')); }
  });
  el.fairClose.addEventListener('click', () => closeDlg(el.fairDlg));
  el.rulesClose.addEventListener('click', () => closeDlg(el.rulesDlg));
  el.menuClose.addEventListener('click', () => closeDlg(el.menuDlg));
  el.menuBtn.addEventListener('click', () => openDlg(el.menuDlg));
  el.openRules.addEventListener('click', () => { closeDlg(el.menuDlg); openDlg(el.rulesDlg); });
  el.openFair.addEventListener('click', () => { closeDlg(el.menuDlg); refreshFair(); openDlg(el.fairDlg); });

  // Langue
  languages().forEach(({ code, label }) => {
    const o = document.createElement('option');
    o.value = code; o.textContent = label;
    el.lang.appendChild(o);
  });
  el.lang.value = getLang();
  el.lang.addEventListener('change', () => setLang(el.lang.value));

  // Son / volume / plein écran
  function renderSound() {
    el.soundBtn.textContent = audio.on ? '🔊' : '🔇';
    el.soundBtn.setAttribute('aria-pressed', audio.on ? 'true' : 'false');
    el.setSound.textContent = t(audio.on ? 'menu.soundOn' : 'menu.soundOff');
    el.setSound.setAttribute('aria-pressed', audio.on ? 'true' : 'false');
    el.vol.value = String(Math.round(audio.vol * 100));
  }
  function toggleSound() {
    audio.setEnabled(!audio.on);
    renderSound();
    if (audio.on) audio.beep(660, 90);
  }
  el.soundBtn.addEventListener('click', toggleSound);
  el.setSound.addEventListener('click', toggleSound);
  el.vol.addEventListener('input', () => audio.setVolume(Number(el.vol.value) / 100));

  const root = document.documentElement;
  const fsReq = root.requestFullscreen ? () => root.requestFullscreen()
    : root.webkitRequestFullscreen ? () => root.webkitRequestFullscreen() : null;
  if (!fsReq) el.fsRow.hidden = true; // pas de bouton sans action réelle
  el.fs.addEventListener('click', () => {
    try {
      if (document.fullscreenElement || document.webkitFullscreenElement) {
        (document.exitFullscreen || document.webkitExitFullscreen).call(document);
      } else {
        const r = fsReq();
        if (r && r.catch) r.catch(() => toast(t('toast.fsFail')));
      }
    } catch (_) { toast(t('toast.fsFail')); }
  });

  el.replacedReload.addEventListener('click', () => location.reload());

  socket.addEventListener('status', (e) => {
    const st = e.detail && e.detail.state;
    s.status = st || 'error';
    s.connected = st === 'connected';
    if (s.connected) s.everConnected = true;
    renderStatus();
    if (st === 'replaced') el.replaced.hidden = false;
    if (st === 'connected') {
      el.replaced.hidden = true;
      if (s.phase === 'idle') setPhase('idle', 'phase.syncing');
    } else audio.stopFlight();
  });

  socket.addEventListener('broadcast', (e) => {
    const { type, data, replay } = e.detail || {};
    if (type === 'ROUND_START') {
      s.roundId = data && data.roundId != null ? String(data.roundId) : '';
      s.hash = (data && data.serverSeedHash) || '';
      audio.stopFlight();
      if (!replay) { canvas.reset(); setMult('1.00x', 'idle'); }
      startCountdown(Number(data && data.startsAt), !!replay);
      refreshFair();
      announce(t('sr.betting'));
    } else if (type === 'ROUND_RUNNING') { // paris fermés (événement réel du moteur)
      stopCountdown();
      setPhase('flying', 'phase.flying');
      audio.startFlight();
      announce(t('sr.flying'));
    } else if (type === 'TICK') {
      const m = Number(data && data.multiplier);
      if (!Number.isFinite(m)) return;
      if (s.phase !== 'flying') { stopCountdown(); setPhase('flying', 'phase.flying'); announce(t('sr.flying')); }
      setMult(fmtX(m), 'flying');
      canvas.tick(m);
      audio.startFlight();
      audio.setFlightIntensity(m);
    } else if (type === 'CRASH') {
      const cp = Number(data && data.crashPoint);
      const rid = data && data.roundId != null ? String(data.roundId) : '';
      stopCountdown();
      setPhase('crashed', 'phase.crashed');
      setMult(Number.isFinite(cp) ? fmtX(cp) : t('mult.crash'), 'crashed');
      canvas.crash(cp);
      addHistory(rid, cp);
      s.last = { roundId: rid, crashPoint: cp };
      refreshFair();
      audio.crash();
      announce(t('sr.crash', { x: Number.isFinite(cp) ? fmtX(cp) : '?' }));
    }
  });

  socket.addEventListener('playerEvent', (e) => {
    const d = e.detail || {};
    if (d.stale) return;
    if (d.type === 'CASHOUT_ACCEPTED') audio.beep(880, 140);
    else if (d.type === 'BET_ACCEPTED') audio.beep(520, 60);
  });

  socket.addEventListener('serverError', (e) => {
    const reason = e.detail && e.detail.reason;
    if (reason) toast(String(reason));
  });

  onLangChange(() => {
    el.lang.value = getLang();
    renderStatus(); renderPhase(); renderSound(); refreshFair();
    if (!el.history.querySelector('.hchip')) { el.history.innerHTML = ''; renderHistoryEmpty(); }
  });

  renderStatus();
  renderSound();
  setPhase('idle', 'phase.waiting');
  renderHistoryEmpty();
  refreshFair();
  return { toast };
}
