// WebTransport to a daemon (listen.go): https://<ip>:<port>/nmx with the
// certificate pinned by hash. The server opens the bidirectional stream
// that carries the wire protocol.

import { encodeFrame, FrameReader, machineURL } from './wire.js';

export class Conn {
  constructor(wt, stream, addr) {
    this.wt = wt;
    this.addr = addr;
    this.writer = stream.writable.getWriter();
    this.queue = [];
    this.waiters = [];
    this.closed = false;
    this.onmessage = null; // once set, messages go there instead of recv
    this.onclose = null;
    this.readLoop(stream.readable.getReader());
    wt.closed.then(() => this.close(), () => this.close());
  }

  async readLoop(reader) {
    const fr = new FrameReader();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        for (const m of fr.push(value)) this.deliver(m);
      }
    } catch { /* closed or bad frame */ }
    this.close();
  }

  deliver(m) {
    if (this.onmessage) { this.onmessage(m); return; }
    const w = this.waiters.shift();
    if (w) w.resolve(m); else this.queue.push(m);
  }

  // recv returns the next message, for the handshake.
  recv(timeoutMs = 15000) {
    if (this.queue.length) return Promise.resolve(this.queue.shift());
    if (this.closed) return Promise.reject(new Error('closed'));
    return new Promise((resolve, reject) => {
      const w = { resolve, reject };
      const timer = setTimeout(() => {
        this.waiters = this.waiters.filter(x => x !== w);
        reject(new Error('timeout'));
      }, timeoutMs);
      w.resolve = m => { clearTimeout(timer); resolve(m); };
      w.reject = e => { clearTimeout(timer); reject(e); };
      this.waiters.push(w);
    });
  }

  send(type, msg) {
    if (this.closed) return;
    let f;
    try { f = encodeFrame(type, msg); } catch { return; }
    this.writer.write(f).catch(() => this.close());
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    for (const w of this.waiters) w.reject(new Error('closed'));
    this.waiters = [];
    try { this.wt.close(); } catch { /* already closed */ }
    if (this.onclose) this.onclose();
  }
}

const timeoutAfter = (ms, what) => new Promise((_, reject) => setTimeout(() => reject(new Error(what)), ms));
const settle = (p, ms) => Promise.race([p.then(() => {}, () => {}), new Promise(r => setTimeout(r, ms))]);

// open tries the addresses one after the other, the preferred one (the
// last that worked) first, and returns the first connection whose stream
// arrives. Each try is closed before the next starts: never two transports
// at once. The daemon counts every session that ends before authenticating
// as a failed handshake (5 a minute, then a ban), so a losing parallel
// attempt would cost one. hashes are 32-byte SHA-256 certificate hashes.
export async function open(addrs, port, hashes, { preferred = null, timeout = 2500 } = {}) {
  if (!hashes.length) throw new Error('no certificate');
  const order = preferred && addrs.includes(preferred) ? [preferred, ...addrs.filter(a => a !== preferred)] : addrs.slice();
  const opts = {
    serverCertificateHashes: hashes.map(h => ({ algorithm: 'sha-256', value: h })),
    allowPooling: false,
  };
  for (const addr of order) {
    let wt;
    try { wt = new WebTransport(machineURL(addr, port), opts); } catch { continue; }
    wt.closed.catch(() => {});
    try {
      await Promise.race([wt.ready, timeoutAfter(timeout, 'timeout')]);
    } catch {
      try { wt.close(); } catch { /* ignore */ }
      await settle(wt.closed, 1000);
      continue;
    }
    // Ready: this is the connection, whatever happens next.
    try {
      const reader = wt.incomingBidirectionalStreams.getReader();
      const { value, done } = await Promise.race([reader.read(), timeoutAfter(10000, 'no stream')]);
      reader.releaseLock();
      if (done || !value) throw new Error('no stream');
      return new Conn(wt, value, addr);
    } catch (e) {
      try { wt.close(); } catch { /* ignore */ }
      throw e;
    }
  }
  throw new Error('unreachable');
}
