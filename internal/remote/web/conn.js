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

// open tries the addresses in parallel and keeps the first transport that
// is ready; the others are closed. The preferred address (the last one that
// worked) gets a short head start so that, at home, one connection is
// usually all the daemon sees. hashes are 32-byte SHA-256 certificate hashes.
export function open(addrs, port, hashes, { preferred = null, timeout = 8000, stagger = 300 } = {}) {
  if (!hashes.length) return Promise.reject(new Error('no certificate'));
  const order = preferred && addrs.includes(preferred) ? [preferred, ...addrs.filter(a => a !== preferred)] : addrs.slice();
  const opts = {
    serverCertificateHashes: hashes.map(h => ({ algorithm: 'sha-256', value: h })),
    allowPooling: false,
  };
  return new Promise((resolve, reject) => {
    const all = [];
    const timers = [];
    const started = new Set();
    let won = false, failed = 0;
    const lose = () => {
      if (++failed === order.length && !won) { won = true; reject(new Error('unreachable')); }
      // A failure ends the head start of the preferred address.
      timers.forEach(clearTimeout);
      order.forEach(start);
    };
    const start = addr => {
      if (won || started.has(addr)) return;
      started.add(addr);
      let wt;
      try { wt = new WebTransport(machineURL(addr, port), opts); } catch { lose(); return; }
      all.push(wt);
      wt.closed.catch(() => {});
      Promise.race([wt.ready, timeoutAfter(timeout, 'timeout')]).then(async () => {
        if (won) { wt.close(); return; }
        won = true;
        timers.forEach(clearTimeout);
        for (const o of all) if (o !== wt) { try { o.close(); } catch { /* ignore */ } }
        try {
          const reader = wt.incomingBidirectionalStreams.getReader();
          const { value, done } = await Promise.race([reader.read(), timeoutAfter(timeout, 'no stream')]);
          reader.releaseLock();
          if (done || !value) throw new Error('no stream');
          resolve(new Conn(wt, value, addr));
        } catch (e) {
          try { wt.close(); } catch { /* ignore */ }
          reject(e);
        }
      }, () => {
        try { wt.close(); } catch { /* ignore */ }
        lose();
      });
    };
    order.forEach((a, i) => {
      if (i === 0) start(a);
      else timers.push(setTimeout(() => start(a), stagger));
    });
  });
}
