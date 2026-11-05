// Canvas2D renderer of the screen the daemon interprets (screen.go). The
// terminal text only ever reaches the canvas, never the DOM.
//
// The grid is drawn at one device pixel per pixel on an offscreen canvas,
// line by line as frames change them; the visible canvas shows it scaled
// to fit the width, zoomed and panned.

import { applyFrame, fromB64u, newScreen, screenText } from './wire.js';
import { isBoxRune, drawBoxRune } from './boxdraw.js';

export const FONT_PX = 13; // reading size, CSS px
const FAMILY = 'IosevkaTerm';
const FAINT = 0.55;
const LONG_PRESS = 450; // ms on a line before it is selected
const A_BOLD = 1, A_ITALIC = 2, A_UNDER = 4, A_STRIKE = 8, A_FAINT = 16, A_WIDE = 32;

// loadFonts registers the four Iosevka Term faces the page server serves
// (cached by the service worker).
export function loadFonts() {
  const faces = [['Regular', '400', 'normal'], ['Bold', '700', 'normal'], ['Italic', '400', 'italic'], ['BoldItalic', '700', 'italic']];
  return Promise.allSettled(faces.map(([n, weight, style]) => {
    const f = new FontFace(FAMILY, `url(/fonts/IosevkaTerm-${n}.ttf)`, { weight, style });
    document.fonts.add(f);
    return f.load();
  }));
}

// blend mixes fg over bg at alpha a, as 0xRRGGBB.
export function blend(fg, bg, a) {
  let out = 0;
  for (const sh of [16, 8, 0]) out |= Math.round(((fg >> sh) & 255) * a + ((bg >> sh) & 255) * (1 - a)) << sh;
  return out;
}

// MAX_WHEEL is the most steps one Wheel message carries (conn.go
// maxWheelSteps).
const MAX_WHEEL = 10;

// wheelMessages splits steps (positive: toward the history) into Wheel
// messages over cell x, y.
export function wheelMessages(steps, x, y) {
  const out = [];
  while (steps !== 0) {
    const n = Math.max(-MAX_WHEEL, Math.min(MAX_WHEEL, steps));
    out.push({ steps: n, x, y });
    steps -= n;
  }
  return out;
}

// cellUnder is the screen cell under the canvas point px, py (device
// pixels), through the view: canvas offset left/top, pan, scale, cell size.
export function cellUnder(px, py, v) {
  const x = Math.floor((px - v.left - v.panX) / v.scale / v.cw);
  const y = Math.floor((py - v.top - v.panY) / v.scale / v.ch);
  return { x: Math.max(0, Math.min(v.cols - 1, x)), y: Math.max(0, Math.min(v.rows - 1, y)) };
}

// fitPan is the vertical pan (device px, <= 0) that keeps rows top..bottom
// of a content contentH tall inside a view viewH tall, moving pan as little
// as possible. A content that fits is not panned.
export function fitPan(pan, contentH, viewH, top, bottom) {
  if (contentH <= viewH) return 0;
  if (bottom + pan > viewH) pan = viewH - bottom;
  if (top + pan < 0) pan = -top;
  return Math.min(0, Math.max(viewH - contentH, pan));
}

const css = n => '#' + (n & 0xffffff).toString(16).padStart(6, '0');
const rgbInt = a => (Array.isArray(a) && a.length === 3 ? ((a[0] & 255) << 16) | ((a[1] & 255) << 8) | (a[2] & 255) : null);

// selectedLines is the text of rows a..b, in either order, of a screen's
// lines. Rows past the end are empty rows the screen's text left out.
export function selectedLines(lines, a, b) {
  const from = Math.min(a, b), to = Math.max(a, b);
  return lines.slice(from, to + 1).join('\n').replace(/\n+$/, '');
}

export class Term {
  // onScroll(offset) asks the daemon for another history offset;
  // onWheel(steps, x, y) scrolls a full-screen program instead;
  // onNextPane() switches to the next terminal pane;
  // onPrevPane() switches to the previous terminal pane.
  // onSelect(n) tells how many lines are selected, 0 when none are.
  constructor(band, canvas, { onScroll, onWheel, onNextPane, onPrevPane, onSelect }) {
    this.band = band;
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d', { alpha: false });
    this.grid = document.createElement('canvas');
    this.g = this.grid.getContext('2d', { alpha: false });
    this.onScroll = onScroll;
    this.onWheel = onWheel || (() => {});
    this.onNextPane = onNextPane || (() => false);
    this.onPrevPane = onPrevPane || (() => false);
    this.onSelect = onSelect || (() => {});
    this.sel = null; // selected rows, with the text as it was when selecting started
    this.screen = newScreen();
    this.theme = { fg: 0xe8e0ff, bg: 0x0b0a1a, cursor: 0xff2e88 };
    this.key = null;
    this.imgCache = new Map();
    this.zoom = 1;
    this.panX = 0;
    this.panY = 0;
    this.raf = 0;
    this.measure();
    this.resize();
    this.gestures();
  }

  // measure sets the cell size once per font size, in device pixels.
  measure() {
    this.dpr = window.devicePixelRatio || 1;
    this.px = Math.round(FONT_PX * this.dpr);
    this.g.font = `${this.px}px ${FAMILY}`;
    const m = this.g.measureText('M');
    const asc = m.fontBoundingBoxAscent || this.px * 0.8;
    const desc = m.fontBoundingBoxDescent || this.px * 0.25;
    this.cw = Math.max(1, Math.round(m.width));
    this.ch = Math.max(1, Math.round(asc + desc));
    this.base = Math.round(asc);
    this.lineW = Math.max(1, Math.round(this.dpr));
  }

  // resize follows the band's size (rotation, keyboard).
  resize() {
    const w = Math.max(1, Math.round(this.band.clientWidth * this.dpr));
    const h = Math.max(1, Math.round(this.band.clientHeight * this.dpr));
    if (this.canvas.width !== w || this.canvas.height !== h) {
      this.canvas.width = w;
      this.canvas.height = h;
    }
    this.present();
  }

  // fit is the terminal size that fills the band at the reading size.
  fit() {
    return {
      cols: Math.max(2, Math.floor(this.canvas.width / this.cw)),
      rows: Math.max(1, Math.floor(this.canvas.height / this.ch)),
      cw: this.cw,
      ch: this.ch,
    };
  }

  clearImages() {
    if (this.imgCache) this.imgCache.clear();
  }

  setSession(key) {
    if (this.key !== key) {
      this.key = key;
      this.clearImages();
    }
  }

  reset() {
    this.clearSelection();
    this.clearImages();
    this.screen = newScreen();
    this.zoom = 1;
    this.panX = this.panY = 0;
    this.grid.width = this.grid.height = 1;
    this.present();
  }

  setTheme(t) {
    const fg = rgbInt(t.fg), bg = rgbInt(t.bg), cur = rgbInt(t.cursor);
    if (fg !== null) this.theme.fg = fg;
    if (bg !== null) this.theme.bg = bg;
    if (cur !== null) this.theme.cursor = cur;
    for (let y = 0; y < this.screen.rows; y++) this.drawLine(y);
    this.drawImages();
    this.present();
  }

  setFrame(f) {
    const dirty = applyFrame(this.screen, f);
    const gw = this.screen.cols * this.cw, gh = this.screen.rows * this.ch;
    if (this.grid.width !== gw || this.grid.height !== gh) {
      this.grid.width = Math.max(1, gw);
      this.grid.height = Math.max(1, gh);
      for (let y = 0; y < this.screen.rows; y++) dirty.add(y);
    }
    for (const y of dirty) this.drawLine(y);
    this.drawImages();
    this.present();
  }

  drawImages() {
    if (!this.screen.imgs || !this.screen.imgs.length) return;
    const g = this.g, cw = this.cw, ch = this.ch;
    g.save();
    g.beginPath();
    g.rect(0, 0, this.screen.cols * cw, this.screen.rows * ch);
    g.clip();
    for (const img of this.screen.imgs) {
      const bmp = this.imgCache && this.imgCache.get(`${this.key}:${img.id}:${img.gen}`);
      if (!bmp) continue;
      g.drawImage(
        bmp,
        img.sx, img.sy, img.sw, img.sh,
        Math.round(img.x * cw), Math.round(img.y * ch),
        Math.round(img.w * cw), Math.round(img.h * ch),
      );
    }
    g.restore();
  }

  setImage(msg) {
    if (!msg || !msg.key) return;
    if (!this.imgCache) this.imgCache = new Map();
    const cacheKey = `${msg.key}:${msg.id}:${msg.gen}`;
    if (msg.bitmap || msg.bmp) {
      this.imgCache.set(cacheKey, msg.bitmap || msg.bmp);
      if (this.key === msg.key) {
        this.drawImages();
        this.present();
      }
      return;
    }
    if (typeof msg.data === 'string' && typeof createImageBitmap === 'function') {
      try {
        const raw = fromB64u(msg.data);
        const isPNG = raw.length > 4 && raw[0] === 0x89 && raw[1] === 0x50 && raw[2] === 0x4e && raw[3] === 0x47;
        const blob = new Blob([raw], { type: isPNG ? 'image/png' : 'image/jpeg' });
        createImageBitmap(blob).then(bmp => {
          this.imgCache.set(cacheKey, bmp);
          if (this.key === msg.key) {
            this.drawImages();
            this.present();
          }
        }).catch(() => {});
      } catch { /* malformed */ }
    }
  }

  text() { return screenText(this.screen); }

  // ---- selection: a long press picks a line, dragging extends it by lines.

  rowAt(clientX, clientY) {
    const r = this.canvas.getBoundingClientRect();
    const s = this.screen;
    return cellUnder(clientX * this.dpr, clientY * this.dpr, {
      left: r.left * this.dpr, top: r.top * this.dpr, panX: this.panX, panY: this.panY,
      scale: this.baseScale() * this.zoom, cw: this.cw, ch: this.ch, cols: Math.max(1, s.cols), rows: Math.max(1, s.rows),
    }).y;
  }

  startSelection(row) {
    this.sel = { a: row, b: row, lines: this.text().split('\n') };
    this.present();
    this.onSelect(1);
  }

  extendSelection(row) {
    if (!this.sel || this.sel.b === row) return;
    this.sel.b = row;
    this.present();
    this.onSelect(Math.abs(this.sel.b - this.sel.a) + 1);
  }

  clearSelection() {
    if (!this.sel) return;
    this.sel = null;
    this.present();
    this.onSelect(0);
  }

  selectedText() {
    if (!this.sel) return '';
    const { a, b, lines } = this.sel;
    return selectedLines(lines, a, b);
  }

  font(attr) {
    return (attr & A_ITALIC ? 'italic ' : '') + (attr & A_BOLD ? 'bold ' : '') + `${this.px}px ${FAMILY}`;
  }

  drawLine(y) {
    const g = this.g, cw = this.cw, ch = this.ch, top = y * ch, s = this.screen;
    if (y >= s.rows) return;
    g.globalAlpha = 1;
    g.fillStyle = css(this.theme.bg);
    g.fillRect(0, top, s.cols * cw, ch);
    g.textBaseline = 'alphabetic';
    const curX = s.cursor && s.offset === 0 && s.cy === y ? s.cx : -1;
    let under = null; // the rune under the cursor and its style
    let x = 0;
    for (const run of s.lines[y] || []) {
      const chars = Array.from(run.text);
      const n = run.attr & A_WIDE ? 2 : 1;
      const span = chars.length * n;
      if (run.bg >= 0) {
        g.fillStyle = css(run.bg);
        g.fillRect(x * cw, top, span * cw, ch);
      }
      g.fillStyle = css(run.fg);
      g.globalAlpha = run.attr & A_FAINT ? FAINT : 1;
      g.font = this.font(run.attr);
      // Faint box runes: overlapping arms at partial alpha would darken the
      // joins, so they are drawn opaque in the color faint text blends to.
      const boxColor = run.attr & A_FAINT ? css(blend(run.fg, run.bg >= 0 ? run.bg : this.theme.bg, FAINT)) : null;
      for (let i = 0; i < chars.length; i++) {
        const cx = x + i * n;
        if (curX >= cx && curX < cx + n) under = { c: chars[i], cx, n, attr: run.attr };
        this.glyph(chars[i], cx * cw, top, n * cw, boxColor);
      }
      if (run.attr & A_UNDER) g.fillRect(x * cw, top + Math.min(ch - this.lineW, this.base + this.lineW), span * cw, this.lineW);
      if (run.attr & A_STRIKE) g.fillRect(x * cw, top + Math.round(ch / 2), span * cw, this.lineW);
      g.globalAlpha = 1;
      x += span;
    }
    if (curX >= 0) {
      const cx = under ? under.cx : curX, n = under ? under.n : 1;
      g.fillStyle = css(this.theme.cursor);
      g.fillRect(cx * cw, top, n * cw, ch);
      if (under) {
        g.fillStyle = css(this.theme.bg);
        g.font = this.font(under.attr);
        this.glyph(under.c, cx * cw, top, n * cw);
      }
    }
  }

  // glyph draws one rune; boxColor, when set, draws box runes opaque in
  // that color instead of the current fill and alpha.
  glyph(c, px, top, w, boxColor = null) {
    if (c === ' ') return;
    const cp = c.codePointAt(0);
    const g = this.g;
    if (isBoxRune(cp)) {
      if (!boxColor) { drawBoxRune(g, cp, px, top, w, this.ch); return; }
      const fill = g.fillStyle, alpha = g.globalAlpha;
      g.fillStyle = boxColor;
      g.globalAlpha = 1;
      drawBoxRune(g, cp, px, top, w, this.ch);
      g.fillStyle = fill;
      g.globalAlpha = alpha;
      return;
    }
    g.fillText(c, px, top + this.base);
  }

  // ---- view: scale to fit the width, zoom 1× to 3×, pan

  baseScale() {
    return this.grid.width > 1 ? Math.min(1, this.canvas.width / this.grid.width) : 1;
  }

  clampPan() {
    const s = this.baseScale() * this.zoom;
    const cw = this.grid.width * s, ch = this.grid.height * s;
    const W = this.canvas.width, H = this.canvas.height;
    this.panX = cw <= W ? 0 : Math.min(0, Math.max(W - cw, this.panX));
    this.panY = ch <= H ? 0 : Math.min(0, Math.max(H - ch, this.panY));
    // Not zoomed, the grid is only taller than the band when the keyboard
    // is up (its size follows the width alone): keep the cursor row, or the
    // last row in the history, in view.
    if (this.zoom === 1 && this.screen.rows) {
      const sc = this.screen;
      const row = sc.offset === 0 ? Math.min(sc.cy, sc.rows - 1) : sc.rows - 1;
      const rh = this.ch * s;
      this.panY = fitPan(this.panY, ch, H, row * rh, (row + 1) * rh);
    }
  }

  present() {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      const c = this.ctx;
      this.clampPan();
      const s = this.baseScale() * this.zoom;
      c.setTransform(1, 0, 0, 1, 0, 0);
      c.fillStyle = css(this.theme.bg);
      c.fillRect(0, 0, this.canvas.width, this.canvas.height);
      if (this.screen.rows) {
        c.imageSmoothingEnabled = s !== 1;
        c.drawImage(this.grid, Math.round(this.panX), Math.round(this.panY), this.grid.width * s, this.grid.height * s);
        if (this.sel) {
          const from = Math.min(this.sel.a, this.sel.b), to = Math.max(this.sel.a, this.sel.b);
          c.globalAlpha = 0.35;
          c.fillStyle = css(this.theme.cursor);
          c.fillRect(Math.round(this.panX), Math.round(this.panY + from * this.ch * s), this.grid.width * s, (to - from + 1) * this.ch * s);
          c.globalAlpha = 1;
        }
      }
    });
  }

  // appScroll: the screen belongs to a full-screen program, scrolling goes
  // to it (Wheel) rather than to the history.
  appScroll() { return this.screen.alt || this.screen.mouse; }

  // wheel sends steps over the cell under the client point (CSS px).
  wheel(steps, clientX, clientY) {
    if (!steps) return;
    const r = this.canvas.getBoundingClientRect();
    const s = this.screen;
    const c = cellUnder(clientX * this.dpr, clientY * this.dpr, {
      left: r.left * this.dpr, top: r.top * this.dpr, panX: this.panX, panY: this.panY,
      scale: this.baseScale() * this.zoom, cw: this.cw, ch: this.ch, cols: Math.max(1, s.cols), rows: Math.max(1, s.rows),
    });
    for (const m of wheelMessages(steps, c.x, c.y)) this.onWheel(m.steps, m.x, m.y);
  }

  resetView() {
    this.zoom = 1;
    this.panX = this.panY = 0;
    this.present();
  }

  // ---- gestures: one finger drags the history (or pans when zoomed), two
  // fingers zoom, a double tap goes to the next pane on the right half of the
  // band and to the previous one on the left; without another pane it resets
  // the view.

  gestures() {
    const el = this.band;
    let mode = null, start = null, lastTap = 0, lastTapX = 0, lastTapY = 0, moved = false, wantOffset = 0, sent = 0;
    let wantLines = 0, sentLines = 0; // wheel steps of a drag over a full-screen program
    let lastTouchDouble = 0;
    const pt = t => ({ x: t.clientX * this.dpr, y: t.clientY * this.dpr });
    const mid = (a, b) => ({ x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 });
    const dist = (a, b) => Math.hypot(a.x - b.x, a.y - b.y);
    const rect = () => {
      const r = this.canvas.getBoundingClientRect();
      return { x: r.left * this.dpr, y: r.top * this.dpr };
    };
    const handleDoubleTap = clientX => {
      const r = this.band.getBoundingClientRect();
      const switched = clientX >= r.left + r.width / 2 ? this.onNextPane() : this.onPrevPane();
      if (!switched) this.resetView();
    };
    const begin = ts => {
      if (ts.length >= 2) {
        const a = pt(ts[0]), b = pt(ts[1]), o = rect();
        const m = mid(a, b);
        mode = 'pinch';
        start = { d: Math.max(1, dist(a, b)), zoom: this.zoom, m: { x: m.x - o.x, y: m.y - o.y }, panX: this.panX, panY: this.panY };
      } else if (ts.length === 1) {
        mode = 'drag';
        start = { p: pt(ts[0]), c: { x: ts[0].clientX, y: ts[0].clientY }, panX: this.panX, panY: this.panY, offset: this.screen.offset };
        wantOffset = this.screen.offset;
        wantLines = sentLines = 0;
      }
    };
    const sendScroll = () => {
      if (this.appScroll()) {
        if (mode !== 'drag' || !start.c) return;
        const d = wantLines - sentLines;
        sentLines = wantLines;
        this.wheel(d, start.c.x, start.c.y);
        return;
      }
      if (sent === wantOffset) return;
      sent = wantOffset;
      this.onScroll(wantOffset);
    };
    let scrollRaf = 0;
    let pressTimer = 0;
    const noPress = () => { clearTimeout(pressTimer); pressTimer = 0; };
    el.addEventListener('touchstart', e => {
      e.preventDefault();
      noPress();
      moved = e.touches.length > 1;
      begin(e.touches);
      sent = this.screen.offset;
      // A new touch leaves a selection, unless it is the one being made.
      if (mode !== 'select') this.clearSelection();
      if (e.touches.length === 1 && this.screen.rows) {
        const t = e.touches[0], x = t.clientX, y = t.clientY;
        pressTimer = setTimeout(() => {
          pressTimer = 0;
          if (moved) return;
          mode = 'select';
          if (navigator.vibrate) navigator.vibrate(15);
          this.startSelection(this.rowAt(x, y));
        }, LONG_PRESS);
      }
    }, { passive: false });
    el.addEventListener('touchmove', e => {
      e.preventDefault();
      if (mode === 'select' && e.touches.length === 1) {
        this.extendSelection(this.rowAt(e.touches[0].clientX, e.touches[0].clientY));
      } else if (mode === 'pinch' && e.touches.length >= 2) {
        const a = pt(e.touches[0]), b = pt(e.touches[1]), o = rect();
        const m = mid(a, b);
        const base = this.baseScale();
        const z = Math.min(3, Math.max(1, start.zoom * dist(a, b) / start.d));
        // Keep the content under the fingers where it was.
        const cx = (start.m.x - start.panX) / (base * start.zoom);
        const cy = (start.m.y - start.panY) / (base * start.zoom);
        this.zoom = z;
        this.panX = m.x - o.x - cx * base * z;
        this.panY = m.y - o.y - cy * base * z;
        this.present();
      } else if (mode === 'drag' && e.touches.length === 1) {
        const p = pt(e.touches[0]);
        const dx = p.x - start.p.x, dy = p.y - start.p.y;
        if (Math.abs(dx) + Math.abs(dy) > 10 * this.dpr) {
          moved = true;
          noPress();
        }
        if (this.zoom > 1) {
          this.panX = start.panX + dx;
          this.panY = start.panY + dy;
          this.present();
        } else {
          const lines = Math.round(dy / (this.ch * this.baseScale()));
          wantLines = lines;
          wantOffset = Math.min(this.screen.hist, Math.max(0, start.offset + lines));
          if (!scrollRaf) scrollRaf = requestAnimationFrame(() => { scrollRaf = 0; sendScroll(); });
        }
      }
    }, { passive: false });
    const end = e => {
      e.preventDefault();
      noPress();
      if (mode === 'select') {
        if (!e.touches.length) mode = null;
        return;
      }
      if (e.touches.length) { begin(e.touches); return; }
      if (mode === 'drag' && !moved && e.changedTouches.length) {
        const t = e.changedTouches[0];
        const now = performance.now();
        if (now - lastTap < 300 && Math.hypot(t.clientX - lastTapX, t.clientY - lastTapY) < 30) {
          lastTap = 0;
          lastTouchDouble = now;
          handleDoubleTap(t.clientX);
        } else {
          lastTap = now;
          lastTapX = t.clientX;
          lastTapY = t.clientY;
        }
      }
      // The last lines of the drag, before the finger lifted.
      if (scrollRaf) { cancelAnimationFrame(scrollRaf); scrollRaf = 0; sendScroll(); }
      mode = null;
    };
    el.addEventListener('touchend', end, { passive: false });
    el.addEventListener('touchcancel', end, { passive: false });
    el.addEventListener('dblclick', e => {
      if (performance.now() - lastTouchDouble < 500) return;
      handleDoubleTap(e.clientX);
    });
    // Desktop browsers (selftest, debugging): the wheel scrolls the history,
    // or the full-screen program.
    el.addEventListener('wheel', e => {
      e.preventDefault();
      if (this.appScroll()) { this.wheel(-Math.sign(e.deltaY), e.clientX, e.clientY); return; }
      wantOffset = Math.min(this.screen.hist, Math.max(0, this.screen.offset - Math.sign(e.deltaY) * 3));
      sendScroll();
    }, { passive: false });
  }
}
