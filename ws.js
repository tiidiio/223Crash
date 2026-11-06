// ws.js — client WebSocket 223CRASH, protocole gateway (JWT HS512 en query param)
export class CrashSocket extends EventTarget {
  constructor({ url, token, heartbeatIntervalMs = 5000, heartbeatTimeoutMs = 3000, actionTimeoutMs = 4000 }) {
    super();
    this.url = url;
    this.token = token;
    this.ws = null;
    this.reconnectAttempts = 0;
    this.maxReconnectDelay = 8000;
    this._manualClose = false;

    this.heartbeatIntervalMs = heartbeatIntervalMs;
    this.heartbeatTimeoutMs = heartbeatTimeoutMs;
    this._hbInterval = null;
    this._hbTimeout = null;

    // Timeout d'action par slot : si aucune réponse (BET_ACCEPTED/REJECTED,
    // CASHOUT_ACCEPTED/REJECTED) n'arrive dans actionTimeoutMs, on émet
    // 'actionTimeout' pour que l'UI débloque le bouton au lieu de rester figée.
    this.actionTimeoutMs = actionTimeoutMs;
    this._pendingBySlot = new Map(); // slot -> timeoutId

    // betId actif par slot : conservé tant qu'aucune réponse DÉFINITIVE (BET_ACCEPTED/REJECTED)
    // n'est reçue pour ce slot, y compris après un actionTimeout (qui réactive le bouton côté UI
    // pendant que la requête d'origine est peut-être encore en cours de traitement serveur) et à
    // travers une reconnexion (cette Map vit sur l'instance CrashSocket, pas sur la connexion WS
    // sous-jacente). Réutiliser le même betId sur un retry est ce qui permet au serveur de
    // garantir qu'une même tentative logique de pari ne produit jamais deux opérations
    // financières — voir cmd/server/gateway.go.handleBet.
    this._betIdBySlot = new Map();
  }

  connect() {
    this._manualClose = false;
    const wsUrl = `${this.url}?token=${encodeURIComponent(this.token)}`;
    this.ws = new WebSocket(wsUrl);

    this.ws.addEventListener('open', () => {
      this.reconnectAttempts = 0;
      this._emit('status', { state: 'connected' });
      this._startHeartbeat();
      this.join();
    });

    this.ws.addEventListener('message', (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      if (msg.type === 'PONG') {
        this._onPong();
        return;
      }
      this._route(msg);
    });

    this.ws.addEventListener('close', (ev) => {
      if (ev.code === 4001) { // session ouverte ailleurs : pas de reconnexion en boucle
        this._stopped = true;
        this._stopHeartbeat();
        this._clearAllPending();
        this._emit('status', { state: 'replaced' });
        return;
      }
      this._stopHeartbeat();
      this._clearAllPending();
      this._emit('status', { state: 'disconnected' });
      if (!this._manualClose) this._scheduleReconnect();
    });

    this.ws.addEventListener('error', () => {
      this._emit('status', { state: 'error' });
    });
  }

  _startHeartbeat() {
    this._stopHeartbeat();
    const sock = this.ws;
    this._lastRx = performance.now();
    this._lastBeat = this._lastRx;
    this._misses = 0;
    // Tout message reçu (tick compris) prouve que la connexion est vivante.
    sock.addEventListener('message', () => { this._lastRx = performance.now(); this._misses = 0; });
    this._hbInterval = setInterval(() => {
      if (this.ws !== sock || sock.readyState !== WebSocket.OPEN) return;
      const now = performance.now();
      const drift = now - this._lastBeat;
      this._lastBeat = now;
      // Timers gelés/throttlés (écran éteint, onglet caché) : on ne juge pas la connexion.
      if (drift > this.heartbeatIntervalMs * 1.8 || document.hidden) { this._lastRx = now; this._misses = 0; return; }
      sock.send(JSON.stringify({ action: 'ping' }));
      clearTimeout(this._hbTimeout);
      this._hbTimeout = setTimeout(() => {
        if (performance.now() - this._lastRx < this.heartbeatTimeoutMs) return; // du trafic est arrivé entre-temps
        if (++this._misses < 2) return; // 2 silences consécutifs avant verdict
        this._emit('status', { state: 'stale' });
        sock.close();
      }, this.heartbeatTimeoutMs);
    }, this.heartbeatIntervalMs);
  }

  _onPong() {
    clearTimeout(this._hbTimeout);
  }

  _stopHeartbeat() {
    clearInterval(this._hbInterval);
    clearTimeout(this._hbTimeout);
    this._hbInterval = null;
    this._hbTimeout = null;
  }

  _scheduleReconnect() {
    if (this._stopped) return;
    const base = Math.min(500 * 2 ** this.reconnectAttempts, this.maxReconnectDelay);
    const jitter = base * (0.5 + Math.random() * 0.5); // 50%-100% du délai, évite le thundering herd
    this.reconnectAttempts++;
    this._emit('status', { state: 'reconnecting' });
    setTimeout(() => this.connect(), jitter);
  }

  _armActionTimeout(slot) {
    this._clearActionTimeout(slot);
    const t = setTimeout(() => {
      this._pendingBySlot.delete(slot);
      this._emit('actionTimeout', { slot });
    }, this.actionTimeoutMs);
    this._pendingBySlot.set(slot, t);
  }

  _clearActionTimeout(slot) {
    const t = this._pendingBySlot.get(slot);
    if (t) clearTimeout(t);
    this._pendingBySlot.delete(slot);
  }

  _clearAllPending() {
    for (const t of this._pendingBySlot.values()) clearTimeout(t);
    this._pendingBySlot.clear();
  }

  // Génère un identifiant de pari opaque et suffisamment unique pour servir de clé
  // d'idempotence côté serveur. crypto.randomUUID() est utilisé quand disponible (contexte
  // sécurisé — https/wss ou localhost) ; le repli n'est pas cryptographique, seule l'unicité
  // pratique est requise ici (la protection contre le rejeu est assurée côté serveur par
  // l'index unique MongoDB, pas par une propriété cryptographique du betId lui-même).
  _genBetId() {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
      return crypto.randomUUID();
    }
    return 'bet-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 12);
  }

  _route(msg) {
    switch (msg.type) {
      case 'CONNECTED':
        this._emit('connected', msg);
        break;
      case 'BALANCE':
        this._emit('balance', msg);
        break;
      case 'ERROR':
        this._emit('serverError', msg);
        break;
      case 'TICK':
      case 'CRASH':
      case 'ROUND_START':
        this._emit('broadcast', msg);
        break;
      case 'BET_ACCEPTED':
      case 'BET_REJECTED':
        // Réponse définitive pour ce betId : on libère le slot pour qu'un PROCHAIN pari (pas un
        // retry de celui-ci) obtienne un nouveau betId.
        if (msg.data?.slot !== undefined) {
          this._clearActionTimeout(msg.data.slot);
          this._betIdBySlot.delete(msg.data.slot);
        }
        this._emit('playerEvent', msg);
        break;
      case 'CASHOUT_ACCEPTED':
      case 'CASHOUT_REJECTED':
        if (msg.data?.slot !== undefined) this._clearActionTimeout(msg.data.slot);
        this._emit('playerEvent', msg);
        break;
      default:
        this._emit('unknown', msg);
    }
  }

  _emit(name, detail) {
    this.dispatchEvent(new CustomEvent(name, { detail }));
  }

  _send(payload) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(payload));
      return true;
    }
    // Pas connecté : on prévient explicitement au lieu de dropper en silence.
    this._emit('sendFailed', { payload });
    return false;
  }

  join() { this._send({ action: 'join' }); }

  bet(slot, amount, autoCashout = null) {
    // Réutilise le betId déjà en vol pour ce slot s'il y en a un (retry après actionTimeout,
    // double-clic ayant échappé au garde-fou UI, etc.) — sinon en génère un nouveau. C'est cette
    // réutilisation, combinée à l'idempotency côté serveur, qui garantit qu'une même tentative
    // logique de pari ne produit jamais deux débits même en cas de retry.
    let betId = this._betIdBySlot.get(slot);
    if (!betId) {
      betId = this._genBetId();
      this._betIdBySlot.set(slot, betId);
    }
    const ok = this._send({ action: 'bet', betId, slot, amount, autoCashout });
    if (ok) this._armActionTimeout(slot);
    return ok;
  }

  cashout(slot) {
    const ok = this._send({ action: 'cashout', slot });
    if (ok) this._armActionTimeout(slot);
    return ok;
  }

  close() {
    this._manualClose = true;
    this._stopHeartbeat();
    this._clearAllPending();
    this.ws?.close(1000);
  }
}
