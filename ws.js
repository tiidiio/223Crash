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

    this.ws.addEventListener('close', () => {
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
    this._hbInterval = setInterval(() => {
      if (this.ws?.readyState !== WebSocket.OPEN) return;
      this.ws.send(JSON.stringify({ action: 'ping' }));
      clearTimeout(this._hbTimeout);
      this._hbTimeout = setTimeout(() => {
        // Pas de pong à temps -> connexion probablement zombie, on force la reconnexion
        this._emit('status', { state: 'stale' });
        this.ws?.close();
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
    const ok = this._send({ action: 'bet', slot, amount, autoCashout });
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
