// Delivering a Face ID signature. iOS kills the WebTransport session while
// the Face ID or "save passkey" sheet is up: the signature, made over the
// nonce of that dead connection, is then sent on a new one. The daemon
// keeps each nonce for 5 minutes, good for one signature (nonces.go).
// No DOM: node tests it.

import { T, PROTO } from './wire.js';

// sendOrReconnect sends one handshake frame (Pair, Auth) built from a
// signature already obtained. If the connection is gone or the write
// fails, it opens one new connection with reconnect(), reads its
// Challenge, sends Hello and the same frame. It returns the connection
// that carried the frame, or throws.
export async function sendOrReconnect(conn, type, msg, reconnect) {
  if (conn && !conn.closed && await conn.send(type, msg)) return conn;
  if (conn) conn.close();
  const c = await reconnect();
  try {
    const ch = await c.recv();
    if (ch.type !== T.Challenge) throw new Error('no challenge');
    if (!await c.send(T.Hello, { proto: PROTO })) throw new Error('closed');
    if (!await c.send(type, msg)) throw new Error('closed');
    return c;
  } catch (e) {
    c.close();
    throw e;
  }
}

// signThenSend asks for the signature once (sign: Face ID), builds the
// frame from it and delivers it with sendOrReconnect. getConn is read
// after the signature, when the connection may have changed or died.
export async function signThenSend(sign, build, type, getConn, reconnect) {
  const sig = await sign();
  const conn = await sendOrReconnect(getConn(), type, build(sig), reconnect);
  return { sig, conn };
}
