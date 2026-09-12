// ws.js — client WebSocket 223CRASH, protocole gateway (JWT HS512 en query param)
export class CrashSocket extends EventTarget {
  constructor({ url, token }) {
    super();
    this.url = url;
    this.token = token;
    this.ws = null;
    this.reconnectAttempts = 0;
    this.maxReconnectDelay = 8000;
    this._manualClose = false;
  }

  connect() {
    this._manualClose = false;
    const wsUrl = `${this.url}?token=${encodeURIComponent(this.token)}`;
    this.ws = new WebSocket(wsUrl);

    this.ws.addEventListener('open', () => {
      this.reconnectAttempts = 0;
      this._emit('status', { state: 'connected' });
      this.join();
    });

    this.ws.addEventListener('message', (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      this._route(msg);
    });

    this.ws.addEventListener('close', () => {
      this._emit('status', { state: 'disconnected' });
      if (!this._manualClose) this._scheduleReconnect();
    });

    this.ws.addEventListener('error', () => {
      this._emit('status', { state: 'error' });
    });
  }

  _scheduleReconnect() {
    const delay = Math.min(500 * 2 ** this.reconnectAttempts, this.maxReconnectDelay);
    this.reconnectAttempts++;
    this._emit('status', { state: 'reconnecting' });
    setTimeout(() => this.connect(), delay);
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
    }
  }

  join() { this._send({ action: 'join' }); }
  bet(slot, amount, autoCashout = null) { this._send({ action: 'bet', slot, amount, autoCashout }); }
  cashout(slot) { this._send({ action: 'cashout', slot }); }

  close() {
    this._manualClose = true;
    this.ws?.close(1000);
  }
}
