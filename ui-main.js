// ui-main.js — boot 223CRASH : listeners AVANT connect() pour ne perdre aucune synchro
import { CrashSocket } from './ws.js';
import { initBets } from './bets.js';
import { CrashCanvas } from './ui-canvas.js';
import { initHud } from './ui-hud.js';
import { resolveConnection } from './ui-config.js';
import { initLang, t } from './i18n.js';
import { GameAudio } from './audio.js';

const $ = (id) => document.getElementById(id);

function boot() {
  initLang();
  const { url, token } = resolveConnection();
  if (!token) {
    $('fatal-msg').textContent = t('fatal.noToken');
    $('fatal').hidden = false;
    return;
  }
  const audio = new GameAudio();
  const unlock = () => audio.unlock();
  window.addEventListener('pointerdown', unlock, { once: true });
  window.addEventListener('keydown', unlock, { once: true });

  const socket = new CrashSocket({ url, token });
  const canvas = new CrashCanvas($('stage-canvas'));
  initBets({ socket, panelsEl: $('panels') });
  initHud({ socket, canvas, audio });

  window.addEventListener('pagehide', () => { audio.stopFlight(); socket.close(); });
  window.addEventListener('pageshow', (e) => { if (e.persisted) location.reload(); });
  socket.connect();
}

boot();
