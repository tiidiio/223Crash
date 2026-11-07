// ui-canvas.js — courbe du multiplicateur (canvas 2D, nette sur écrans haute densité).

export class CrashCanvas {
  constructor(canvas) {
    this.cv = canvas;
    this.ctx = canvas.getContext('2d');
    this.points = [];
    this.phase = 'idle';
    this.t0 = 0;
    this.mult = 1;
    this.dirty = true;
    this.w = 1; this.h = 1;
    this._loop = this._loop.bind(this);
    this._onResize = () => this.resize();
    window.addEventListener('resize', this._onResize);
    if (typeof ResizeObserver !== 'undefined') {
      this._ro = new ResizeObserver(this._onResize);
      this._ro.observe(canvas.parentElement || canvas);
    }
    this.resize();
    this._raf = requestAnimationFrame(this._loop);
  }

  resize() {
    const r = this.cv.getBoundingClientRect();
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.w = Math.max(1, Math.round(r.width));
    this.h = Math.max(1, Math.round(r.height));
    this.cv.width = this.w * dpr;
    this.cv.height = this.h * dpr;
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.dirty = true;
  }

  reset() { this.points = []; this.phase = 'betting'; this.mult = 1; this.dirty = true; }

  tick(m) {
    const now = performance.now();
    if (this.phase !== 'flying') { this.phase = 'flying'; this.t0 = now; this.points = []; }
    this.mult = m;
    this.points.push({ t: now - this.t0, m });
    if (this.points.length > 3000) this.points = this.points.filter((_, i) => i % 2 === 0);
    this.dirty = true;
  }

  crash(cp) {
    if (Number.isFinite(cp)) {
      this.mult = cp;
      this.points.push({ t: performance.now() - this.t0, m: cp });
    }
    this.phase = 'crashed';
    this.dirty = true;
  }

  _css(name, fallback) {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallback;
  }

  _loop() {
    this._raf = requestAnimationFrame(this._loop);
    if (!this.dirty) return;
    this.dirty = false;
    this._draw();
  }

  _draw() {
    const { ctx, w, h } = this;
    ctx.clearRect(0, 0, w, h);
    const pad = 14;

    ctx.strokeStyle = 'rgba(255,255,255,.06)';
    ctx.lineWidth = 1;
    for (let i = 1; i < 5; i++) {
      const y = pad + ((h - 2 * pad) * i) / 5;
      ctx.beginPath(); ctx.moveTo(pad, y); ctx.lineTo(w - pad, y); ctx.stroke();
    }

    if (this.points.length < 2) return;
    const last = this.points[this.points.length - 1];
    const xMax = Math.max(6000, last.t * 1.05);
    const yMax = Math.max(2, this.mult * 1.2);
    const X = (t) => pad + (t / xMax) * (w - 2 * pad);
    const Y = (m) => h - pad - ((m - 1) / (yMax - 1)) * (h - 2 * pad);

    const color = this.phase === 'crashed' ? this._css('--danger', '#ff5470') : this._css('--accent', '#3ddc97');

    ctx.beginPath();
    ctx.moveTo(X(this.points[0].t), Y(this.points[0].m));
    for (let i = 1; i < this.points.length; i++) ctx.lineTo(X(this.points[i].t), Y(this.points[i].m));

    ctx.lineWidth = 3;
    ctx.lineJoin = 'round';
    ctx.strokeStyle = color;
    ctx.stroke();

    ctx.lineTo(X(last.t), h - pad);
    ctx.lineTo(X(this.points[0].t), h - pad);
    ctx.closePath();
    const g = ctx.createLinearGradient(0, 0, 0, h);
    g.addColorStop(0, color + '55');
    g.addColorStop(1, color + '00');
    ctx.fillStyle = g;
    ctx.fill();

    ctx.beginPath();
    ctx.arc(X(last.t), Y(last.m), 5, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
  }

  destroy() {
    cancelAnimationFrame(this._raf);
    window.removeEventListener('resize', this._onResize);
    if (this._ro) this._ro.disconnect();
  }
}
