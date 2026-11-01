// heli-sound.js — bruit d'hélicoptère procédural (Web Audio API), zéro asset.
// Usage : unlock() sur geste utilisateur, start() au 1er TICK, setMultiplier(m) à chaque TICK, crash() sur CRASH.
export class HeliSound {
  constructor() {
    this.ctx = null;
    this.muted = false;
    this.running = false;
  }

  async unlock() {
    if (!this.ctx) this._build();
    if (this.ctx.state === 'suspended') await this.ctx.resume();
  }

  _build() {
    const AC = window.AudioContext || window.webkitAudioContext;
    const ctx = (this.ctx = new AC());

    this.master = ctx.createGain();
    this.master.gain.value = 0;
    this.master.connect(ctx.destination);

    // Buffer de bruit blanc (réutilisé pour le chop et l'explosion)
    const len = ctx.sampleRate * 2;
    this.noiseBuf = ctx.createBuffer(1, len, ctx.sampleRate);
    const d = this.noiseBuf.getChannelData(0);
    for (let i = 0; i < len; i++) d[i] = Math.random() * 2 - 1;

    // Chop des pales : bruit passe-bande modulé par un LFO pulsé
    const noise = ctx.createBufferSource();
    noise.buffer = this.noiseBuf;
    noise.loop = true;
    const bp = ctx.createBiquadFilter();
    bp.type = 'bandpass';
    bp.frequency.value = 180;
    bp.Q.value = 0.8;
    this.chop = ctx.createGain();
    this.chop.gain.value = 0.12;
    noise.connect(bp).connect(this.chop).connect(this.master);

    // LFO à forme d'impulsion (harmoniques de même poids => pic net à chaque pale)
    const N = 10;
    const real = new Float32Array(N + 1);
    const imag = new Float32Array(N + 1);
    for (let k = 1; k <= N; k++) real[k] = 1 / Math.sqrt(k);
    this.lfo = ctx.createOscillator();
    this.lfo.setPeriodicWave(ctx.createPeriodicWave(real, imag));
    this.lfo.frequency.value = 9;

    const chopDepth = ctx.createGain();
    chopDepth.gain.value = 0.5;
    this.lfo.connect(chopDepth).connect(this.chop.gain);

    // Thump grave synchronisé sur le même LFO
    const thump = ctx.createOscillator();
    thump.type = 'sine';
    thump.frequency.value = 58;
    const thumpGain = ctx.createGain();
    thumpGain.gain.value = 0.08;
    const thumpDepth = ctx.createGain();
    thumpDepth.gain.value = 0.3;
    thump.connect(thumpGain).connect(this.master);
    this.lfo.connect(thumpDepth).connect(thumpGain.gain);

    // Sifflement turbine (monte avec le multiplicateur)
    this.whine = ctx.createOscillator();
    this.whine.type = 'sawtooth';
    this.whine.frequency.value = 140;
    const lp = ctx.createBiquadFilter();
    lp.type = 'lowpass';
    lp.frequency.value = 700;
    const whineGain = ctx.createGain();
    whineGain.gain.value = 0.025;
    this.whine.connect(lp).connect(whineGain).connect(this.master);

    noise.start();
    this.lfo.start();
    thump.start();
    this.whine.start();
  }

  _vol() {
    return this.muted ? 0 : 0.8;
  }

  start() {
    if (!this.ctx || this.running) return;
    this.running = true;
    const t = this.ctx.currentTime;
    this.master.gain.cancelScheduledValues(t);
    this.master.gain.setTargetAtTime(this._vol(), t, 0.15);
    this.setMultiplier(1);
  }

  setMultiplier(m) {
    if (!this.ctx || !this.running) return;
    const t = this.ctx.currentTime;
    const rate = Math.min(26, 9 + (Math.max(1, m) - 1) * 1.5);
    this.lfo.frequency.setTargetAtTime(rate, t, 0.1);
    this.whine.frequency.setTargetAtTime(140 + Math.min(m, 50) * 9, t, 0.15);
    this.chop.gain.setTargetAtTime(Math.min(0.28, 0.12 + (m - 1) * 0.01), t, 0.2);
  }

  crash() {
    if (!this.ctx || !this.running) return;
    this.running = false;
    const ctx = this.ctx;
    const t = ctx.currentTime;
    this.master.gain.cancelScheduledValues(t);
    this.master.gain.setTargetAtTime(0, t, 0.05);
    if (this.muted) return;
    // Burst d'explosion : bruit passe-bas qui s'effondre
    const src = ctx.createBufferSource();
    src.buffer = this.noiseBuf;
    const lp = ctx.createBiquadFilter();
    lp.type = 'lowpass';
    lp.frequency.setValueAtTime(1400, t);
    lp.frequency.exponentialRampToValueAtTime(70, t + 0.7);
    const g = ctx.createGain();
    g.gain.setValueAtTime(0.9, t);
    g.gain.exponentialRampToValueAtTime(0.001, t + 0.8);
    src.connect(lp).connect(g).connect(ctx.destination);
    src.start(t);
    src.stop(t + 0.85);
  }

  stop() {
    if (!this.ctx) return;
    this.running = false;
    this.master.gain.setTargetAtTime(0, this.ctx.currentTime, 0.1);
  }

  setMuted(flag) {
    this.muted = !!flag;
    if (this.ctx && this.running) {
      this.master.gain.setTargetAtTime(this._vol(), this.ctx.currentTime, 0.05);
    }
  }
}
