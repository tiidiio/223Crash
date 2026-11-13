// ws.js — client WebSocket 223CRASH, protocole gateway (JWT HS512 en query param)
// Lot 1 : idempotence stricte (betId + paramètres figés), UNKNOWN jamais converti en échec,
// betId purgé au changement de manche, CRASH dédupliqué, TICK hors ordre / après CRASH ignorés.

// Rejets du gateway qui signifient « issue financière inconnue / en cours » et NON « refusé ».
// Le backend n'expose pas de code structuré : détection par motif (fragile — à remplacer par un code).
const INDETERMINATE_REASON = /ind[ée]termin|en cours de traitement/i;

export class CrashSocket extends EventTarget {
  constructor({ url, token, heartbeatIntervalMs = 5000, heartbeatTimeoutMs = 3000, actionTimeoutMs = 4000 }) {
    super();
    this.url = url;
    this.token = token;
    this.ws = null;
    this.reconnectAttempts = 0;
    this.maxReconnectDelay = 8000;
    this._manualClose = false;
    this._stopped = false;
    this._reconnectTimer = null;

    this.heartbeatIntervalMs = heartbeatIntervalMs;
    this.heartbeatTimeoutMs = heartbeatTimeoutMs;
    this._hbInterval = null;
    this._hbTimeout = null;
    this._lastRx = 0;
    this._lastBeat = 0;
    this._misses = 0;

    // Timeout d'action par slot : sans réponse (BET_*/CASHOUT_*) dans actionTimeoutMs, on émet
    // 'actionTimeout' ({ slot, action, betId }). Un BET sans réponse reste INCONNU (il peut être débité).
    this.actionTimeoutMs = actionTimeoutMs;
    this._pendingBySlot = new Map(); // slot -> { timer, action, betId }

    // Pari non résolu par slot : { betId, amount, autoCashout }. Conservé tant qu'aucune réponse
    // DÉFINITIVE n'est reçue, y compris après timeout et reconnexion. Un retry renvoie EXACTEMENT
    // le même betId et les mêmes paramètres (idempotence serveur). Purgé au changement de manche.
    this._betBySlot = new Map();

    // Slots dont un BET était en vol à un changement de manche : la prochaine réponse est « tardive ».
    this._staleSlots = new Set();

    this._roundId = '';
    this._lastCrashRoundId = '';
    this._crashed = false;
    this._lastTickMult = 0;
  }

  connect() {
    if (this.ws && (this.ws.readyState === WebSocket.CONNECTING || this.ws.readyState === WebSocket.OPEN)) return;

    clearTimeout(this._reconnectTimer);
    this._reconnectTimer = null;
    this._manualClose = false;
    this._stopped = false;

    const wsUrl = `${this.url}?token=${encodeURIComponent(this.token)}`;
    const sock = new WebSocket(wsUrl);
    this.ws = sock;

    sock.addEventListener('open', () => {
      if (this.ws !== sock) return;
      this.reconnectAttempts = 0;
      this._crashed = false;      // reconnexion en plein vol : pas de ROUND_START rejoué
      this._lastTickMult = 0;
      this._emit('status', { state: 'connected' });
      this._startHeartbeat();
      this.join();
    });

    sock.addEventListener('message', (ev) => {
      if (this.ws !== sock) return;
      let msg;
      try { msg = JSON.parse(ev.data); } catch { this._emit('protocolError', { kind: 'parse' }); return; }
      if (!msg || typeof msg !== 'object' || typeof msg.type !== 'string') {
        this._emit('protocolError', { kind: 'shape' });
        return;
      }
      if (msg.type === 'PONG') { this._onPong(); return; }
      this._route(msg);
    });

    sock.addEventListener('close', (ev) => {
      if (this.ws !== sock) return;
      this._stopHeartbeat();
      this._clearAllPending();
      this._staleSlots.clear();
      if (ev.code === 4001) { // session ouverte ailleurs : pas de reconnexion en boucle
        this._stopped = true;
        this._emit('status', { state: 'replaced' });
        return;
      }
      this._emit('status', { state: 'disconnected' });
      if (!this._manualClose) this._scheduleReconnect();
    });

    sock.addEventListener('error', () => {
      if (this.ws !== sock) return;
      this._emit('status', { state: 'error' });
    });
  }

  _startHeartbeat() {
    this._stopHeartbeat();
    const sock = this.ws;
    this._lastRx = performance.now();
    this._lastBeat = this._lastRx;
    this._misses = 0;
    sock.addEventListener('message', () => { this._lastRx = performance.now(); this._misses = 0; });
    this._hbInterval = setInterval(() => {
      if (this.ws !== sock || sock.readyState !== WebSocket.OPEN) return;
      const now = performance.now();
      const drift = now - this._lastBeat;
      this._lastBeat = now;
      if (drift > this.heartbeatIntervalMs * 1.8 || document.hidden) { this._lastRx = now; this._misses = 0; return; }
      sock.send(JSON.stringify({ action: 'ping' }));
      clearTimeout(this._hbTimeout);
      this._hbTimeout = setTimeout(() => {
        if (performance.now() - this._lastRx < this.heartbeatTimeoutMs) return;
        if (++this._misses < 2) return;
        this._emit('status', { state: 'stale' });
        sock.close();
      }, this.heartbeatTimeoutMs);
    }, this.heartbeatIntervalMs);
  }

  _onPong() { clearTimeout(this._hbTimeout); }

  _stopHeartbeat() {
    clearInterval(this._hbInterval);
    clearTimeout(this._hbTimeout);
    this._hbInterval = null;
    this._hbTimeout = null;
  }

  _scheduleReconnect() {
    if (this._stopped || this._manualClose) return;
    clearTimeout(this._reconnectTimer);
    const base = Math.min(500 * 2 ** this.reconnectAttempts, this.maxReconnectDelay);
    const jitter = base * (0.5 + Math.random() * 0.5);
    this.reconnectAttempts++;
    this._emit('status', { state: 'reconnecting' });
    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null;
      this.connect();
    }, jitter);
  }

  _armActionTimeout(slot, action, betId = null) {
    this._clearActionTimeout(slot);
    const timer = setTimeout(() => {
      this._pendingBySlot.delete(slot);
      this._emit('actionTimeout', { slot, action, betId });
    }, this.actionTimeoutMs);
    this._pendingBySlot.set(slot, { timer, action, betId });
  }

  _clearActionTimeout(slot) {
    const entry = this._pendingBySlot.get(slot);
    if (entry) clearTimeout(entry.timer);
    this._pendingBySlot.delete(slot);
  }

  _clearAllPending() {
    for (const entry of this._pendingBySlot.values()) clearTimeout(entry.timer);
    this._pendingBySlot.clear();
  }

  pendingAction(slot) {
    const entry = this._pendingBySlot.get(slot);
    return entry ? entry.action : null;
  }

  _genBetId() {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
    return 'bet-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 12);
  }

  // Changement de manche (ROUND_START d'une nouvelle manche) : tout pari non résolu de la manche
  // précédente est signalé 'staleBet' (À VÉRIFIER) et son betId est purgé — il ne doit JAMAIS être
  // réutilisé pour la manche suivante.
  _onNewRound(roundId) {
    this._roundId = roundId;
    this._crashed = false;
    this._lastTickMult = 0;
    for (const [slot, entry] of this._pendingBySlot) {
      if (entry.action === 'bet') this._staleSlots.add(slot);
    }
    for (const [slot, rec] of this._betBySlot) this._emit('staleBet', { slot, betId: rec.betId });
    this._betBySlot.clear();
    this._clearAllPending();
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

      case 'ROUND_START': {
        const rid = msg.data && msg.data.roundId != null ? String(msg.data.roundId) : '';
        const replay = rid !== '' && rid === this._roundId; // rejoué par le gateway après (re)join
        if (!replay) this._onNewRound(rid);
        this._emit('broadcast', replay ? { ...msg, replay: true } : msg);
        break;
      }
      case 'ROUND_RUNNING':
        this._crashed = false;
        this._lastTickMult = 0;
        this._emit('broadcast', msg);
        break;
      case 'TICK': {
        const m = Number(msg.data && msg.data.multiplier);
        if (!Number.isFinite(m)) { this._emit('protocolError', { kind: 'tick' }); break; }
        if (this._crashed) break;                       // retardataire après CRASH
        if (m < this._lastTickMult - 1e-9) break;       // hors ordre
        this._lastTickMult = m;
        this._emit('broadcast', msg);
        break;
      }
      case 'CRASH': {
        const rid = msg.data && msg.data.roundId != null ? String(msg.data.roundId) : '';
        if (rid && rid === this._lastCrashRoundId) break; // doublon
        if (rid) this._lastCrashRoundId = rid;
        this._crashed = true;
        this._lastTickMult = 0;
        this._emit('broadcast', msg);
        break;
      }

      case 'BET_ACCEPTED':
      case 'BET_REJECTED': {
        const slot = msg.data && msg.data.slot;
        if (slot === undefined) { this._emit('protocolError', { kind: 'bet-no-slot' }); break; }
        this._clearActionTimeout(slot);
        if (this._staleSlots.delete(slot)) {            // réponse tardive d'une manche précédente
          this._emit('playerEvent', { ...msg, stale: true });
          break;
        }
        const reason = msg.data && msg.data.reason;
        if (msg.type === 'BET_REJECTED' && INDETERMINATE_REASON.test(String(reason || ''))) {
          this._emit('playerEvent', { ...msg, indeterminate: true }); // betId CONSERVÉ pour le retry
          break;
        }
        this._betBySlot.delete(slot);                   // réponse définitive : betId libéré
        this._emit('playerEvent', msg);
        break;
      }
      case 'CASHOUT_ACCEPTED':
      case 'CASHOUT_REJECTED': {
        const slot = msg.data && msg.data.slot;
        if (slot === undefined) { this._emit('protocolError', { kind: 'cashout-no-slot' }); break; }
        this._clearActionTimeout(slot);
        this._emit('playerEvent', msg);
        break;
      }
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
    this._emit('sendFailed', { action: payload && payload.action });
    return false;
  }

  join() { return this._send({ action: 'join' }); }

  // Retourne 'sent' | 'busy' (déjà une action en vol sur ce slot) | 'offline'.
  bet(slot, amount, autoCashout = null) {
    if (this._pendingBySlot.has(slot)) return 'busy';
    let rec = this._betBySlot.get(slot);
    const isNew = !rec;
    if (isNew) {
      rec = { betId: this._genBetId(), amount, autoCashout };
      this._betBySlot.set(slot, rec);
    }
    // Retry : mêmes betId ET mêmes paramètres que l'envoi d'origine (sinon « paramètres différents »).
    const ok = this._send({ action: 'bet', betId: rec.betId, slot, amount: rec.amount, autoCashout: rec.autoCashout });
    if (!ok) {
      if (isNew) this._betBySlot.delete(slot);
      return 'offline';
    }
    this._armActionTimeout(slot, 'bet', rec.betId);
    return 'sent';
  }

  cashout(slot) {
    if (this._pendingBySlot.has(slot)) return 'busy';
    if (!this._send({ action: 'cashout', slot })) return 'offline';
    this._armActionTimeout(slot, 'cashout');
    return 'sent';
  }

  close() {
    this._manualClose = true;
    clearTimeout(this._reconnectTimer);
    this._reconnectTimer = null;
    this._stopHeartbeat();
    this._clearAllPending();
    this.ws?.close(1000);
  }
}
