// ui-main.js — point d'entrée : assemble socket, mises, canvas et affichage.
import { CrashSocket } from './ws.js';
import { initBets } from './bets.js';
import { CrashCanvas } from './ui-canvas.js';
import { initHud } from './ui-hud.js';
import { resolveConnection } from './ui-config.js';

const $ = (id) => document.getElementById(id);

function boot() {
  const { url, token } = resolveConnection();
  if (!token) {
    $('fatal-msg').textContent = "Jeton de session manquant. Ouvrez le jeu depuis le site de l'opérateur.";
    $('fatal').hidden = false;
    return;
  }

  const socket = new CrashSocket({ url, token });
  const canvas = new CrashCanvas($('stage-canvas'));

  // Les écouteurs doivent exister AVANT connect() pour ne perdre aucun message de synchro.
  initBets({ socket, panelsEl: $('panels') });
  initHud({
    socket,
    canvas,
    els: {
      stageEl: $('stage'), multEl: $('mult'), phaseEl: $('phase'), barEl: $('count-bar'),
      historyEl: $('history'), statusEl: $('status-chip'), toastEl: $('toast'),
      replacedEl: $('replaced'), replacedReload: $('replaced-reload'),
      soundBtn: $('sound-btn'), fairBtn: $('fair-btn'), fairDlg: $('fair-dlg'),
      fairRound: $('fair-round'), fairHash: $('fair-hash'), fairLast: $('fair-last'),
      fairCopy: $('fair-copy'), fairClose: $('fair-close'),
    },
  });

  window.addEventListener('pagehide', () => socket.close());
  socket.connect();
}

boot();
