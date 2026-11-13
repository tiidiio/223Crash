// audio.js — sons procéduraux Web Audio (aucun asset) : bips, rotor d'hélicoptère, crash
import { CONFIG } from './ui-config.js';

const read = (k) => { try { return localStorage.getItem(k); } catch (_) { return null; } };
const write = (k, v) => { try { localStorage.setItem(k, v); } catch (_) {} };

export class GameAudio {
  constructor() {
    this.on = read(CONFIG.soundKey) === '1';
    const v = parseFloat(read(CONFIG.volumeKey));
    this.vol = Number.isFinite(v) ? Math.min(1, Math.max(0, v)) : 0.6;
    this.ctx = null;
    this.master = null;
    this.flight = null;
  }

  _ensure() {
    if (this.ctx) return this.ctx;
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return null;
    try {
      this.ctx = new AC();
      this.master = this.ctx.createGain();
      this.master.gain.value = this.on ? this.vol : 0;
      this.master.connect(this.ctx.destination);
    } catch (_) { this.ctx = null; }
    return this.ctx;
  }

  unlock() {
    const c = this._ensure();
    if (c && c.state === 'suspended') c.resume().catch(() => {});
  }

  _apply() {
    if (this.ctx && this.master) this.master.gain.setTargetAtTime(this.on ? this.vol : 0, this.ctx.currentTime, 0.02);
  }

  setEnabled(v) {
    this.on = !!v;
    write(CONFIG.soundKey, this.on ? '1' : '0');
    this.unlock();
    this._apply();
    if (!this.on) this.stopFlight();
  }

  setVolume(v) {
    this.vol = Math.min(1, Math.max(0, Number(v) || 0));
    write(CONFIG.volumeKey, String(this.vol));
    this._apply();
  }

  beep(freq, ms, type = 'sine', gain = 0.25) {
    if (!this.on) return;
    const c = this._ensure();
    if (!c) return;
    try {
      const o = c.createOscillator();
      const g = c.createGain();
      const t0 = c.currentTime;
      o.type = type;
      o.frequency.value = freq;
      g.gain.setValueAtTime(0.0001, t0);
      g.gain.exponentialRampToValueAtTime(gain, t0 + 0.01);
      g.gain.exponentialRampToValueAtTime(0.0001, t0 + ms / 1000);
      o.connect(g); g.connect(this.master);
      o.start(t0); o.stop(t0 + ms / 1000 + 0.02);
    } catch (_) {}
  }

  noiseBurst(sec, gain) {
    if (!this.on) return;
    const c = this._ensure();
    if (!c) return;
    try {
      const n = Math.max(1, Math.floor(c.sampleRate * sec));
      const buf = c.createBuffer(1, n, c.sampleRate);
      const d = buf.getChannelData(0);
      for (let i = 0; i < n; i++) d[i] = (Math.random() * 2 - 1) * (1 - i / n);
      const src = c.createBufferSource();
      const g = c.createGain();
      g.gain.value = gain;
      src.buffer = buf;
      src.connect(g); g.connect(this.master);
      src.start();
    } catch (_) {}
  }

  // Rotor : bruit filtré modulé par un LFO ; idempotent.
  startFlight() {
    if (!this.on || this.flight) return;
    const c = this._ensure();
    if (!c) return;
    try {
      const n = c.sampleRate;
      const buf = c.createBuffer(1, n, c.sampleRate);
      const d = buf.getChannelData(0);
      for (let i = 0; i < n; i++) d[i] = Math.random() * 2 - 1;
      const src = c.createBufferSource();
      src.buffer = buf; src.loop = true;
      const lp = c.createBiquadFilter();
      lp.type = 'lowpass'; lp.frequency.value = 500;
      const amp = c.createGain();
      amp.gain.value = 0.5;
      const lfo = c.createOscillator();
      lfo.frequency.value = 13;
      const depth = c.createGain();
      depth.gain.value = 0.45;
      lfo.connect(depth); depth.connect(amp.gain);
      const out = c.createGain();
      out.gain.value = 0.35;
      src.connect(lp); lp.connect(amp); amp.connect(out); out.connect(this.master);
      src.start(); lfo.start();
      this.flight = { src, lfo, lp, out };
    } catch (_) { this.flight = null; }
  }

  setFlightIntensity(mult) {
    const f = this.flight;
    if (!f || !this.ctx) return;
    const k = Math.log2(Math.min(Math.max(mult, 1), 20));
    const t0 = this.ctx.currentTime;
    try {
      f.lfo.frequency.setTargetAtTime(13 + k * 4, t0, 0.1);
      f.lp.frequency.setTargetAtTime(500 + k * 150, t0, 0.1);
    } catch (_) {}
  }

  stopFlight() {
    const f = this.flight;
    if (!f) return;
    this.flight = null;
    try {
      f.out.gain.setTargetAtTime(0, this.ctx.currentTime, 0.05);
      setTimeout(() => { try { f.src.stop(); f.lfo.stop(); } catch (_) {} }, 300);
    } catch (_) {}
  }

  crash() {
    this.stopFlight();
    this.beep(180, 320, 'sawtooth', 0.4);
    this.noiseBurst(0.35, 0.4);
  }
}
