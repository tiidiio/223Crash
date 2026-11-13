// ui-canvas.js — Canvas 223CRASH (Lot 2)
// Vérité = multiplicateur serveur (TICK / CRASH). Le canvas ne calcule aucun résultat : il lisse
// seulement l'affichage entre deux ticks (jamais au-delà du dernier tick reçu) et se cale
// exactement sur crashPoint au CRASH.
const TAU = Math.PI * 2;
const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
const rgba = (hex, a) => {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) return hex;
  const n = parseInt(m[1], 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
};

export class CrashCanvas {
  constructor(canvas) {
    this.cv = canvas;
    this.ctx = canvas.getContext('2d');
    this.points = [];
    this.phase = 'idle';
    this.t0 = 0;
    this.mult = 1;
    this.dm = 1;
    this.crashPoint = null;
    this.yMax = 2.2;
    this.particles = [];
    this.flash = 0;
    this.shake = 0;
    this.w = 1; this.h = 1; this.dpr = 1;
    this.dirty = true;
    this._last = 0;
    this._skip = false;
    this.reduced = !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
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

  _readColors() {
    const cs = getComputedStyle(document.documentElement);
    const v = (n, f) => (cs.getPropertyValue(n).trim() || f);
    this.c = { turq: v('--turquoise', '#4FB8AE'), gold: v('--gold', '#D4A54A'), danger: v('--danger', '#ff5470') };
  }

  resize() {
    const r = this.cv.getBoundingClientRect();
    this.dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.w = Math.max(1, Math.round(r.width));
    this.h = Math.max(1, Math.round(r.height));
    this.cv.width = this.w * this.dpr;
    this.cv.height = this.h * this.dpr;
    this.ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    this._readColors();
    const groundY = this.h - Math.max(26, this.h * 0.1);
    this.gradOk = this.ctx.createLinearGradient(0, 0, 0, groundY);
    this.gradOk.addColorStop(0, rgba(this.c.turq, 0.28));
    this.gradOk.addColorStop(1, rgba(this.c.turq, 0));
    this.gradBad = this.ctx.createLinearGradient(0, 0, 0, groundY);
    this.gradBad.addColorStop(0, rgba(this.c.danger, 0.22));
    this.gradBad.addColorStop(1, rgba(this.c.danger, 0));
    this.dirty = true;
  }

  reset() {
    this.points = [];
    this.phase = 'betting';
    this.mult = 1; this.dm = 1;
    this.crashPoint = null;
    this.yMax = 2.2;
    this.particles = [];
    this.flash = 0; this.shake = 0;
    this.t0 = performance.now();
    this.dirty = true;
  }

  tick(m) {
    const now = performance.now();
    if (this.phase !== 'flying') {
      const fromReset = this.phase === 'betting';
      this.phase = 'flying';
      this.t0 = now;
      this.dm = fromReset ? 1 : m; // reconnexion en plein vol : on repart du multiplicateur réel
      this.points = [{ t: 0, m: fromReset ? 1 : m }];
    }
    this.mult = m;
    this.points.push({ t: now - this.t0, m });
    if (this.points.length > 1500) this.points = this.points.filter((_, i) => i % 2 === 0 || i === this.points.length - 1);
    this.dirty = true;
  }

  crash(cp) {
    const now = performance.now();
    if (Number.isFinite(cp)) {
      this.mult = cp; this.dm = cp; this.crashPoint = cp;
      this.points.push({ t: now - this.t0, m: cp });
    }
    this.phase = 'crashed';
    this.dirty = true;
    if (!this.reduced && this.points.length > 1) {
      const last = this.points[this.points.length - 1];
      const g = this._geom(last.t);
      const x = g.X(last.t), y = g.Y(this.dm);
      const cols = [this.c.gold, this.c.turq, '#ffffff', this.c.danger];
      for (let i = 0; i < 26; i++) {
        const max = 700 + Math.random() * 500;
        this.particles.push({
          x, y, vx: (Math.random() - 0.5) * 0.5, vy: -Math.random() * 0.3 - 0.05,
          life: max, max, r: 1.5 + Math.random() * 2.5, c: cols[i % cols.length],
        });
      }
      this.flash = 1; this.shake = 1;
    }
  }

  _geom(tHead) {
    const pad = 14;
    const groundY = this.h - Math.max(26, this.h * 0.1);
    const top = pad + 24;
    const xMax = Math.max(7000, tHead * 1.12);
    const lnY = Math.log(Math.max(1.5, this.yMax));
    return {
      pad, groundY,
      X: (t) => pad + (t / xMax) * (this.w - 2 * pad - 70),
      Y: (m) => groundY - (Math.log(Math.max(1, m)) / lnY) * (groundY - top),
    };
  }

  _loop() {
    this._raf = requestAnimationFrame(this._loop);
    if (document.hidden) return;
    const now = performance.now();
    const dt = Math.min(100, now - (this._last || now));
    this._last = now;
    const animating = this.phase === 'flying' || this.particles.length > 0 || this.flash > 0 || this.shake > 0;
    if (!animating) {
      if (this.phase === 'betting') { this._skip = !this._skip; if (this._skip && !this.dirty) return; }
      else if (!this.dirty) return;
    }
    this.dirty = false;
    this._draw(now, dt);
  }

  _draw(now, dt) {
    const ctx = this.ctx, w = this.w, h = this.h;
    ctx.clearRect(0, 0, w, h);
    ctx.save();
    if (this.shake > 0) {
      const k = this.shake * 7;
      ctx.translate((Math.random() - 0.5) * k, (Math.random() - 0.5) * k);
      this.shake = Math.max(0, this.shake - dt / 380);
    }
    const flying = this.phase === 'flying', crashed = this.phase === 'crashed';
    if (flying) this.dm += (this.mult - this.dm) * (1 - Math.exp(-dt / 70));
    else if (crashed && this.crashPoint != null) this.dm = this.crashPoint;
    const target = Math.max(2.2, this.dm * 1.25);
    this.yMax += (target - this.yMax) * (1 - Math.exp(-dt / 220));
    this.yMax = Math.max(this.yMax, this.dm * 1.08, 1.6);

    const n = this.points.length;
    const tHead = flying ? now - this.t0 : (n ? this.points[n - 1].t : 0);
    const g = this._geom(tHead);

    ctx.strokeStyle = 'rgba(255,255,255,.06)';
    ctx.lineWidth = 1;
    for (let i = 1; i < 5; i++) {
      const y = g.pad + 10 + ((g.groundY - g.pad - 10) * i) / 5;
      ctx.beginPath(); ctx.moveTo(g.pad, y); ctx.lineTo(w - g.pad, y); ctx.stroke();
    }

    if (n < 2) {
      if (!crashed) this._heli(g.pad + 46, g.groundY - 14, 0, this._scale(), now, false);
    } else {
      const col = crashed ? this.c.danger : this.c.turq;
      const hx = g.X(tHead), hy = g.Y(this.dm);
      ctx.beginPath();
      ctx.moveTo(g.X(this.points[0].t), g.Y(this.points[0].m));
      for (let i = 1; i < n; i++) ctx.lineTo(g.X(this.points[i].t), g.Y(this.points[i].m));
      if (flying) ctx.lineTo(hx, hy);
      ctx.lineWidth = 3; ctx.lineJoin = 'round'; ctx.strokeStyle = col;
      ctx.stroke();
      ctx.lineTo(hx, g.groundY);
      ctx.lineTo(g.X(this.points[0].t), g.groundY);
      ctx.closePath();
      ctx.fillStyle = crashed ? this.gradBad : this.gradOk;
      ctx.fill();

      if (flying) {
        const p = this.points[Math.max(0, n - 6)];
        const ang = clamp(Math.atan2(hy - g.Y(p.m), hx - g.X(p.t)) * 0.6, -0.6, 0.2);
        this._heli(hx, hy, ang, this._scale(), now, true);
      } else if (crashed) {
        ctx.strokeStyle = this.c.danger; ctx.lineWidth = 2.5;
        ctx.beginPath(); ctx.arc(hx, hy, 8, 0, TAU); ctx.stroke();
        ctx.beginPath(); ctx.moveTo(hx - 5, hy - 5); ctx.lineTo(hx + 5, hy + 5); ctx.moveTo(hx + 5, hy - 5); ctx.lineTo(hx - 5, hy + 5); ctx.stroke();
      }
    }

    ctx.fillStyle = 'rgba(0,0,0,.25)';
    ctx.fillRect(0, g.groundY, w, h - g.groundY);
    ctx.fillStyle = rgba(this.c.turq, 0.25);
    ctx.fillRect(0, g.groundY, w, 2);

    if (this.particles.length) {
      this.particles = this.particles.filter((p) => (p.life -= dt) > 0);
      for (const p of this.particles) {
        p.x += p.vx * dt; p.y += p.vy * dt; p.vy += 0.0006 * dt;
        ctx.globalAlpha = Math.max(0, p.life / p.max);
        ctx.fillStyle = p.c;
        ctx.beginPath(); ctx.arc(p.x, p.y, p.r, 0, TAU); ctx.fill();
      }
      ctx.globalAlpha = 1;
    }
    if (this.flash > 0) {
      ctx.fillStyle = `rgba(255,255,255,${(this.flash * 0.3).toFixed(3)})`;
      ctx.fillRect(0, 0, w, h);
      this.flash = Math.max(0, this.flash - dt / 300);
    }
    ctx.restore();
  }

  _scale() { return clamp(this.w / 460, 0.7, 1.25); }

  _heli(x, y, ang, s, now, fast) {
    const ctx = this.ctx, c = this.c;
    ctx.save();
    ctx.translate(x, y); ctx.rotate(ang); ctx.scale(s, s);
    ctx.fillStyle = '#22363f';
    ctx.beginPath(); ctx.moveTo(-8, -3); ctx.lineTo(-38, -6); ctx.lineTo(-38, -3); ctx.lineTo(-8, 3); ctx.closePath(); ctx.fill();
    ctx.fillStyle = c.gold;
    ctx.beginPath(); ctx.moveTo(-38, -6); ctx.lineTo(-43, -16); ctx.lineTo(-34, -6); ctx.closePath(); ctx.fill();
    const ty = Math.sin(now / (fast ? 18 : 70)) * 7;
    ctx.strokeStyle = 'rgba(255,255,255,.55)'; ctx.lineWidth = 1.6;
    ctx.beginPath(); ctx.moveTo(-40, -10 - ty); ctx.lineTo(-40, -10 + ty); ctx.stroke();
    ctx.fillStyle = '#2b4650'; ctx.strokeStyle = 'rgba(255,255,255,.18)'; ctx.lineWidth = 1;
    ctx.beginPath(); ctx.ellipse(0, 0, 17, 10, 0, 0, TAU); ctx.fill(); ctx.stroke();
    ctx.strokeStyle = c.gold; ctx.lineWidth = 1.6;
    ctx.beginPath(); ctx.moveTo(-14, 2); ctx.lineTo(10, 2); ctx.stroke();
    ctx.fillStyle = rgba(c.turq, 0.8);
    ctx.beginPath(); ctx.ellipse(8, -1, 8, 6, 0, 0, TAU); ctx.fill();
    ctx.strokeStyle = '#cfd8dc'; ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(0, -10); ctx.lineTo(0, -15); ctx.stroke();
    const bx = Math.cos(now / (fast ? 14 : 90)) * 32;
    ctx.strokeStyle = 'rgba(255,255,255,.22)'; ctx.lineWidth = 4;
    ctx.beginPath(); ctx.ellipse(0, -15, 32, 2.6, 0, 0, TAU); ctx.stroke();
    ctx.strokeStyle = 'rgba(255,255,255,.75)'; ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(-bx, -15); ctx.lineTo(bx, -15); ctx.stroke();
    ctx.strokeStyle = '#9eadb5'; ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(-12, 13); ctx.lineTo(14, 13); ctx.moveTo(-6, 9); ctx.lineTo(-6, 13); ctx.moveTo(8, 9); ctx.lineTo(8, 13); ctx.stroke();
    ctx.restore();
  }

  destroy() {
    cancelAnimationFrame(this._raf);
    window.removeEventListener('resize', this._onResize);
    if (this._ro) this._ro.disconnect();
  }
}
