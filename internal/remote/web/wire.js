// Wire protocol of internal/remote (wire.go): frame = type u8 | length u32
// LE | JSON payload, at most 1 MiB. No DOM here: node runs this module too
// (selftest.mjs).

export const PROTO = 1;
export const MAX_PAYLOAD = 1 << 20;

export const T = Object.freeze({
  Hello: 1, Pair: 2, Auth: 3, Resume: 4, List: 5, Watch: 6, Size: 7, Scroll: 8,
  Text: 9, Keys: 10, Unwatch: 11,
  Challenge: 12, PairCode: 13, Authed: 14, Sessions: 15, Theme: 16, Frame: 17,
  Exited: 18, Error: 19,
});

const enc = new TextEncoder();
const dec = new TextDecoder('utf-8', { fatal: false });

export const utf8 = s => enc.encode(s);

export function concat(...parts) {
  let n = 0;
  for (const p of parts) n += p.length;
  const out = new Uint8Array(n);
  let o = 0;
  for (const p of parts) { out.set(p, o); o += p.length; }
  return out;
}

export function bytes(b) {
  if (b instanceof Uint8Array) return b;
  if (b instanceof ArrayBuffer) return new Uint8Array(b);
  if (ArrayBuffer.isView(b)) return new Uint8Array(b.buffer, b.byteOffset, b.byteLength);
  throw new TypeError('not bytes');
}

// base64url without padding, as Go's base64.RawURLEncoding.
export function b64u(b) {
  b = bytes(b);
  let s = '';
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode.apply(null, b.subarray(i, i + 0x8000));
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// fromB64u accepts padded or unpadded base64url; it throws on anything else.
export function fromB64u(s) {
  if (typeof s !== 'string' || !/^[A-Za-z0-9_-]*={0,2}$/.test(s)) throw new Error('bad base64url');
  s = s.replace(/=+$/, '').replace(/-/g, '+').replace(/_/g, '/');
  if (s.length % 4 === 1) throw new Error('bad base64url');
  const bin = atob(s + '='.repeat((4 - s.length % 4) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function encodeFrame(type, msg) {
  const payload = msg === undefined || msg === null ? new Uint8Array(0) : utf8(JSON.stringify(msg));
  if (payload.length > MAX_PAYLOAD) throw new Error('payload too large');
  const out = new Uint8Array(5 + payload.length);
  out[0] = type;
  new DataView(out.buffer).setUint32(1, payload.length, true);
  out.set(payload, 5);
  return out;
}

// FrameReader reassembles frames from stream chunks. push returns the
// complete frames as {type, msg}; msg is the parsed JSON, or null for an
// empty payload. It throws on an oversized or unparsable frame.
export class FrameReader {
  constructor() { this.buf = new Uint8Array(0); }
  push(chunk) {
    this.buf = this.buf.length ? concat(this.buf, bytes(chunk)) : bytes(chunk).slice();
    const out = [];
    for (;;) {
      if (this.buf.length < 5) break;
      const n = new DataView(this.buf.buffer, this.buf.byteOffset, 5).getUint32(1, true);
      if (n > MAX_PAYLOAD) throw new Error('payload too large');
      if (this.buf.length < 5 + n) break;
      const type = this.buf[0];
      const raw = this.buf.subarray(5, 5 + n);
      out.push({ type, msg: n === 0 ? null : JSON.parse(dec.decode(raw)) });
      this.buf = this.buf.slice(5 + n);
    }
    return out;
  }
}

// ---- Screen model: the PWA keeps the last screen and applies each Frame.

const isInt = v => Number.isInteger(v);

// cleanRun turns a wire run [text, fg, bg, attr] into an object, or null
// when it is malformed.
function cleanRun(r) {
  if (!Array.isArray(r) || r.length !== 4) return null;
  const [text, fg, bg, attr] = r;
  if (typeof text !== 'string' || !isInt(fg) || !isInt(bg) || !isInt(attr)) return null;
  return { text, fg: fg & 0xffffff, bg: bg < 0 ? -1 : bg & 0xffffff, attr: attr & 0xff };
}

export function cleanLine(runs) {
  if (!Array.isArray(runs)) return [];
  const out = [];
  for (const r of runs) {
    const c = cleanRun(r);
    if (c) out.push(c);
  }
  return out;
}

export function newScreen() {
  return { cols: 0, rows: 0, offset: 0, hist: 0, cx: 0, cy: 0, cursor: false, lines: [] };
}

const clampInt = (v, lo, hi, d) => (isInt(v) ? Math.min(hi, Math.max(lo, v)) : d);

// applyFrame updates screen in place from a Frame message and returns the
// set of rows to redraw (every row after a full frame or a size change).
export function applyFrame(screen, f) {
  const cols = clampInt(f.cols, 0, 1000, screen.cols);
  const rows = clampInt(f.rows, 0, 1000, screen.rows);
  const dirty = new Set();
  const resized = cols !== screen.cols || rows !== screen.rows;
  if (f.full || resized) {
    const old = screen.lines;
    screen.lines = new Array(rows);
    for (let y = 0; y < rows; y++) {
      screen.lines[y] = f.full ? [] : (old[y] || []);
      dirty.add(y);
    }
  }
  screen.cols = cols;
  screen.rows = rows;
  const oldCY = screen.cy;
  screen.offset = clampInt(f.offset, 0, 1 << 30, 0);
  screen.hist = clampInt(f.hist, 0, 1 << 30, 0);
  screen.cx = clampInt(f.cx, 0, 1 << 16, 0);
  screen.cy = clampInt(f.cy, 0, 1 << 16, 0);
  screen.cursor = f.cursor === true;
  if (Array.isArray(f.lines)) {
    for (const l of f.lines) {
      if (!Array.isArray(l) || l.length !== 2 || !isInt(l[0])) continue;
      const y = l[0];
      if (y < 0 || y >= rows) continue;
      screen.lines[y] = cleanLine(l[1]);
      dirty.add(y);
    }
  }
  // The cursor is drawn into its line: both its old and new rows change.
  if (oldCY < rows) dirty.add(oldCY);
  if (screen.cy < rows) dirty.add(screen.cy);
  return dirty;
}

// screenText is what "Copy screen" puts in the clipboard.
export function screenText(screen) {
  return screen.lines.map(l => l.map(r => r.text).join('').replace(/\s+$/, '')).join('\n').replace(/\n+$/, '');
}

// ---- Pairing QR code: "nmx1:" + base64url(JSON{n, a, p, h, t}), see
// qrPayload in remote.go.

const ipv4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const ipv6 = /^[0-9a-fA-F:]{2,39}$/;

export function validAddr(a) {
  return typeof a === 'string' && (ipv4.test(a) || (ipv6.test(a) && a.includes(':')));
}

export function parseQR(text) {
  if (typeof text !== 'string' || !text.startsWith('nmx1:')) throw new Error('not a neonmux code');
  const j = JSON.parse(new TextDecoder().decode(fromB64u(text.slice(5))));
  const name = typeof j.n === 'string' ? j.n.slice(0, 64) : '';
  const addrs = Array.isArray(j.a) ? j.a.filter(validAddr) : [];
  const port = j.p;
  const hash = fromB64u(j.h);
  const token = fromB64u(j.t);
  if (!addrs.length) throw new Error('no address');
  if (!isInt(port) || port < 1 || port > 65535) throw new Error('bad port');
  if (hash.length !== 32 || token.length !== 16) throw new Error('bad code');
  return { name, addrs, port, hash, token };
}

export function machineURL(addr, port) {
  if (!validAddr(addr)) throw new Error('bad address');
  return 'https://' + (addr.includes(':') ? '[' + addr + ']' : addr) + ':' + port + '/nmx';
}

// Certificate hashes as Authed sends them: {nb, na, h} (certs.go). Stored
// with h decoded.
export function parseHashes(list) {
  if (!Array.isArray(list)) return [];
  const out = [];
  for (const c of list) {
    try {
      const h = fromB64u(c.h);
      if (h.length === 32 && isInt(c.nb) && isInt(c.na)) out.push({ nb: c.nb, na: c.na, h });
    } catch { /* skip */ }
  }
  return out;
}

// validHashes returns the hashes a connection may pin at nowSec.
export function validHashes(hashes, nowSec) {
  return hashes.filter(c => c.nb <= nowSec && nowSec < c.na).map(c => c.h);
}
