// neonmux remote: pairs with daemons, connects over WebTransport, shows one
// terminal at a time. Protocol and checks: internal/remote (conn.go).

import { T, PROTO, b64u, fromB64u, parseQR, parseHashes, validHashes, capName } from './wire.js';
import { sasCode, createPasskey, regWire, getAssertion, newResumeKey, resumeSig } from './auth.js';
import { open } from './conn.js';
import { startScan } from './pair.js';
import { Term, loadFonts } from './term.js';
import * as store from './store.js';
import { placeMachine } from './machines.js';
import { $, show, notice, hideNotice, status, renderMachines, renderSessions } from './ui.js';

const RESUME_WINDOW = 5 * 60 * 1000 - 15000; // resume.go: 5 min after the last disconnect
const RESUME_LIFE = 12 * 3600 * 1000 - 60000; // resume.go: 12 h after issue
const HIDDEN_GRACE = 2000; // hidden right after Face ID: the sheet itself may hide the page
const RETRY = [500, 1000, 2000, 5000, 10000, 20000];

const S = {
  screen: 'home',
  machines: [],
  machine: null,
  conn: null,
  live: false,       // the terminal screen wants a connection (reconnect when visible)
  sessions: [],
  key: null,         // watched session
  pickOnList: false, // open the session list when it arrives
  term: null,
  retry: 0,
  retryTimer: 0,
  attempt: 0,        // bumps on every connect, so stale attempts give up
  authing: 0,        // a WebAuthn call is running: ignore visibility changes
  hiddenTimer: 0,
  pairing: null,
  lastWidth: 0,
  sizeTimer: 0,
  seenTimer: 0,
  trySkip: () => {},
};

const now = () => Date.now();
const nowSec = () => Math.floor(now() / 1000);

class HandshakeError extends Error {
  constructor(code, detail) { super(code); this.code = code; this.detail = detail || {}; }
}

// ---- boot

async function boot() {
  wireUI();
  setupViewport();
  setupSW();
  try { navigator.storage && navigator.storage.persist && navigator.storage.persist(); } catch { /* best effort */ }
  await loadFonts();
  S.term = new Term($('band'), $('screen'), { onScroll: offset => send(T.Scroll, { offset }) });
  S.lastWidth = $('band').clientWidth;
  try { S.machines = await store.listMachines(); } catch { S.machines = []; }
  if (!S.machines.length) { goHome(); return; }
  const last = S.machines.slice().sort((a, b) => (b.used || 0) - (a.used || 0))[0];
  connect(last, { auto: true });
}

function goHome() {
  closeConn();
  S.live = false;
  S.machine = null;
  S.key = null;
  $('sheet').hidden = true;
  setScreen('home');
  drawMachines();
}

function setScreen(name) {
  S.screen = name;
  show(name);
  if (name === 'home') S.trySkip();
  if (name === 'term' && S.term) {
    S.term.resize();
    S.lastWidth = $('band').clientWidth;
  }
}

async function reloadMachines() {
  try { S.machines = await store.listMachines(); } catch { /* keep */ }
}

function drawMachines() {
  const list = S.machines.slice().sort((a, b) => (a.name || '').localeCompare(b.name || ''));
  renderMachines(list, {
    onOpen: m => connect(m, {}),
    onForget: async m => {
      await store.deleteMachine(m.id);
      await reloadMachines();
      drawMachines();
    },
  });
}

// reach records the result of a real connection to m: the list shows it
// as the machine's dot. Nothing probes the machines (a probe session would
// count as a failed handshake on the daemon).
function reach(m, ok) {
  m.reach = { ok, at: now() };
  const listed = S.machines.find(x => x.id === m.id);
  if (listed) listed.reach = m.reach;
  store.putMachine(m).catch(() => {});
}

// withAuth runs a WebAuthn call. The Face ID sheet may fire
// visibilitychange: while it runs the app ignores it, and once it ends a
// page still hidden after a grace period is treated as gone to background.
function withAuth(f) {
  S.authing++;
  let p;
  try { p = Promise.resolve(f()); } catch (e) { p = Promise.reject(e); }
  return p.finally(() => {
    S.authing--;
    if (!S.authing && document.hidden) {
      clearTimeout(S.hiddenTimer);
      S.hiddenTimer = setTimeout(() => { if (document.hidden && !S.authing) toBackground(); }, HIDDEN_GRACE);
    }
  });
}

// ---- connection to a paired machine

function closeConn() {
  clearTimeout(S.retryTimer);
  clearInterval(S.seenTimer);
  S.attempt++;
  if (S.conn) {
    const c = S.conn;
    S.conn = null;
    c.onclose = null;
    c.close();
    markSeen(S.machine);
  }
}

function markSeen(m) {
  if (!m) return;
  store.getResume(m.id).then(r => { if (r) { r.seen = now(); return store.putResume(r); } }).catch(() => {});
}

function send(type, msg) { if (S.conn) S.conn.send(type, msg); }

function title() {
  const s = S.sessions.find(x => x.key === S.key);
  $('t-title').textContent = (S.machine ? S.machine.name : '') + (s ? ' · ' + (s.name || s.key) : '');
}

// connect opens m, authenticates (resume, else Face ID) and starts the
// terminal screen. auto: from the app's start, falls back to the list.
async function connect(m, { auto = false, reconnect = false } = {}) {
  closeConn();
  hideNotice();
  const attempt = S.attempt;
  S.machine = m;
  S.live = true;
  if (!reconnect) {
    S.key = m.lastKey || null;
    S.sessions = [];
    S.term.reset();
  }
  setScreen('term');
  title();
  status('t-status', 'Connecting…');

  const hashes = validHashes(m.hashes || [], nowSec());
  if (!hashes.length) {
    S.live = false;
    notice('Pair this machine again', [
      { label: 'Pair a machine', fn: startPair, primary: true },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  let conn;
  try {
    conn = await open(m.addrs, m.port, hashes, { preferred: m.lastAddr });
  } catch {
    if (attempt !== S.attempt) return;
    reach(m, false);
    if (auto) { goHome(); return; }
    if (reconnect) { scheduleRetry(); return; }
    S.live = false;
    notice(`${m.name} is not reachable`, [
      { label: 'Retry', fn: () => connect(m, {}), primary: true },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (attempt !== S.attempt) { conn.close(); return; }
  reach(m, true);
  S.conn = conn; // onclose is set once authenticated (startSession)
  try {
    await handshake(conn, m, attempt);
  } catch (e) {
    if (attempt !== S.attempt) return;
    conn.onclose = null;
    conn.close();
    S.conn = null;
    handshakeFailed(m, e, { auto, reconnect });
  }
}

function handshakeFailed(m, e, { reconnect }) {
  if (S.machine !== m || S.screen !== 'term') return; // left meanwhile
  S.live = false;
  status('t-status', '');
  const code = e instanceof HandshakeError ? e.code : 'closed';
  if (code === 'resume') { S.live = true; connect(m, { reconnect: true }); return; }
  if (code === 'proto') {
    notice(protoText(e.detail, m.name), [{ label: 'Machines', fn: goHome, primary: true }]);
    return;
  }
  if (code === 'busy') {
    notice(`${m.name} has too many phones connected`, [
      { label: 'Retry', fn: () => connect(m, {}), primary: true },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (code === 'auth') {
    notice(`${m.name} did not accept this phone`, [
      { label: 'Retry', fn: () => connect(m, {}), primary: true },
      { label: 'Pair a machine', fn: startPair },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (code === 'cancel') {
    notice(`Unlock ${m.name}`, [
      { label: 'Unlock with Face ID', fn: () => connect(m, {}), primary: true },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (reconnect) { S.live = true; scheduleRetry(); return; }
  notice(`Lost the connection to ${m.name}`, [
    { label: 'Retry', fn: () => connect(m, {}), primary: true },
    { label: 'Machines', fn: goHome },
  ]);
}

// protoText says which side to update, from Error{proto, min, max} or,
// without min and max, the version the daemon's Challenge announced.
function protoText(d, name) {
  const daemonOlder = d.max !== undefined ? d.max < PROTO : d.challenge !== undefined && d.challenge < PROTO;
  return daemonOlder ? `Update neonmux on ${name}` : 'Open the app at home to update it';
}

function errorOf(msg) {
  const d = msg.msg || {};
  return new HandshakeError(d.msg === 'proto' || d.msg === 'busy' ? d.msg : 'auth', d);
}

async function handshake(conn, m, attempt) {
  const ch = await conn.recv();
  if (ch.type === T.Error) throw errorOf(ch);
  if (ch.type !== T.Challenge || !ch.msg) throw new HandshakeError('closed');
  // The daemon answers Error{proto, min, max} to a version it does not take.
  conn.send(T.Hello, { proto: PROTO });
  const nonce = fromB64u(ch.msg.nonce);
  const errorFrom = reply => {
    const e = errorOf(reply);
    if (e.code === 'proto') e.detail = { ...e.detail, challenge: ch.msg.proto };
    return e;
  };

  const r = await store.getResume(m.id).catch(() => null);
  if (r && r.resumeId && r.key && now() - r.issued < RESUME_LIFE && now() - (r.seen || 0) < RESUME_WINDOW) {
    const sig = await resumeSig(r.key, nonce);
    conn.send(T.Resume, { id: r.resumeId, sig: b64u(sig) });
    const reply = await conn.recv();
    if (reply.type === T.Authed) {
      r.seen = now();
      await store.putResume(r).catch(() => {});
      await authed(conn, m, reply.msg, null);
      return;
    }
    await store.deleteResume(m.id).catch(() => {});
    if (reply.type === T.Error) {
      const e = errorFrom(reply);
      // An expired or forgotten resume key: start again with Face ID.
      throw e.code === 'auth' ? new HandshakeError('resume') : e;
    }
    throw new HandshakeError('closed');
  }

  // Face ID. iOS may want a tap first: then a button asks for it and calls
  // WebAuthn inside the tap, on this same connection.
  const key = await newResumeKey();
  status('t-status', '');
  let assertion;
  try {
    assertion = await withAuth(() => getAssertion(nonce, m.credId));
  } catch {
    if (attempt !== S.attempt || conn.closed) throw new HandshakeError('cancel');
    assertion = await tapForFaceID(m, conn, () => withAuth(() => getAssertion(nonce, m.credId)));
  }
  if (attempt !== S.attempt || conn.closed) throw new HandshakeError('closed');
  status('t-status', 'Connecting…');
  conn.send(T.Auth, { assertion, session_pub: b64u(key.spki) });
  const reply = await conn.recv();
  if (reply.type === T.Error) throw errorFrom(reply);
  if (reply.type !== T.Authed) throw new HandshakeError('closed');
  await authed(conn, m, reply.msg, key);
}

// tapForFaceID shows a button that runs get inside the tap (iOS may want
// a gesture). A failed or cancelled Face ID shows the button again, on the
// same connection, until the daemon closes it (60 s handshake deadline):
// every new connection that ends unauthenticated counts against the phone.
function tapForFaceID(m, conn, get) {
  return new Promise((resolve, reject) => {
    const was = conn.onclose;
    let over = false;
    const finish = () => { over = true; conn.onclose = was; };
    conn.onclose = () => {
      if (over) return;
      finish();
      hideNotice();
      reject(new HandshakeError('cancel'));
    };
    const ask = () => {
      if (over) return;
      notice(`Unlock ${m.name}`, [
        {
          label: 'Unlock with Face ID', primary: true,
          fn: () => get().then(a => { if (!over) { finish(); resolve(a); } }, () => { if (!conn.closed) ask(); }),
        },
        { label: 'Machines', fn: () => { finish(); reject(new HandshakeError('closed')); goHome(); } },
      ]);
    };
    ask();
  });
}

// authed stores what Authed brought (fresh certificate hashes, resume id)
// and starts the session.
async function authed(conn, m, a, key) {
  const hashes = parseHashes(a && a.hashes);
  if (hashes.length) m.hashes = hashes;
  if (a && typeof a.device_id === 'string' && a.device_id) m.deviceId = a.device_id;
  m.lastAddr = conn.addr;
  m.used = now();
  m.reach = { ok: true, at: now() };
  await store.putMachine(m).catch(() => {});
  if (key) {
    if (a && a.resume_id) {
      await store.putResume({ machineId: m.id, resumeId: a.resume_id, key: key.key, issued: now(), seen: now() }).catch(() => {});
    } else {
      await store.deleteResume(m.id).catch(() => {});
    }
  }
  await reloadMachines();
  S.retry = 0;
  startSession(conn);
}

function startSession(conn) {
  S.conn = conn;
  S.live = true;
  conn.onclose = () => onClosed(conn);
  conn.onmessage = onMessage;
  // Messages that came in with Authed, before onmessage was set.
  for (const m of conn.queue.splice(0)) onMessage(m);
  setScreen('term');
  status('t-status', '');
  title();
  clearInterval(S.seenTimer);
  S.seenTimer = setInterval(() => markSeen(S.machine), 60000);
  send(T.List);
  if (S.key) watch(S.key);
  else S.pickOnList = true;
}

function onClosed(conn) {
  if (conn !== S.conn) return;
  S.conn = null;
  clearInterval(S.seenTimer);
  markSeen(S.machine);
  if (document.hidden || !S.live || !S.machine) return;
  scheduleRetry();
}

// scheduleRetry reconnects with a growing delay (the daemon was restarted,
// the wifi dropped). Each try is one connection: the daemon bans an address
// after 5 failed handshakes in a minute.
function scheduleRetry() {
  clearTimeout(S.retryTimer);
  const m = S.machine;
  if (!m) return;
  const d = RETRY[Math.min(S.retry, RETRY.length - 1)];
  S.retry++;
  status('t-status', 'Reconnecting…');
  S.retryTimer = setTimeout(() => {
    if (!document.hidden && S.live && S.machine === m) connect(m, { reconnect: true });
  }, d);
}

function onMessage({ type, msg }) {
  switch (type) {
    case T.Sessions:
      S.sessions = msg && Array.isArray(msg.list) ? msg.list.filter(s => s && typeof s.key === 'string') : [];
      title();
      if (!$('sheet').hidden) drawSessions();
      if (S.pickOnList) { S.pickOnList = false; openSheet(); }
      break;
    case T.Theme:
      if (msg) S.term.setTheme(msg);
      break;
    case T.Frame:
      if (msg && S.key) S.term.setFrame(msg);
      break;
    case T.Exited:
      S.key = null;
      S.term.reset();
      title();
      openSheet();
      break;
    case T.Error:
      if (msg && msg.msg === 'session not found') {
        S.key = null;
        S.term.reset();
        title();
        openSheet();
      }
      break;
  }
}

function watch(key) {
  S.key = key;
  if (S.machine) {
    S.machine.lastKey = key;
    store.putMachine(S.machine).catch(() => {});
  }
  S.term.reset();
  title();
  send(T.Watch, { key });
  sendSize();
}

function sendSize() {
  if (S.key) send(T.Size, S.term.fit());
}

// ---- session sheet

function openSheet() {
  $('sheet-title').textContent = S.machine ? S.machine.name : '';
  drawSessions();
  $('sheet').hidden = false;
  if (S.conn) send(T.List);
}

function drawSessions() {
  renderSessions(S.sessions, S.key, key => {
    $('sheet').hidden = true;
    if (key !== S.key) watch(key);
  });
}

// ---- pairing

function startPair() {
  closeConn();
  S.live = false;
  S.key = null;
  hideNotice();
  $('sheet').hidden = true;
  setScreen('pair');
  $('pair-name').hidden = true;
  $('pair-code').hidden = true;
  $('pair-scan').hidden = false;
  status('pair-status', '');
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    status('pair-status', 'No camera available');
    return;
  }
  const scan = startScan($('pair-video'));
  const p = { scan, conn: null, q: null, ch: null, key: null, state: 'scan', cancelled: false };
  S.pairing = p;
  scan.promise.then(text => {
    if (p.cancelled) return;
    let q;
    try { q = parseQR(text); } catch {
      status('pair-status', 'This is not a neonmux pairing code');
      setTimeout(() => { if (S.pairing === p) startPair(); }, 1500);
      return;
    }
    p.q = q;
    $('pair-scan').hidden = true;
    $('pair-name').hidden = false;
    pairConnect(p);
  }, e => {
    if (!p.cancelled) status('pair-status', 'Camera: ' + (e && e.message ? e.message : 'unavailable'));
  });
}

// pairConnect opens the connection while the name is typed, so the tap on
// Continue can start the passkey creation at once (iOS wants the gesture).
function pairConnect(p) {
  status('pair-status', 'Connecting…');
  p.state = 'connecting';
  p.conn = null;
  p.nonce = null;
  (async () => {
    const conn = await open(p.q.addrs, p.q.port, [p.q.hash]);
    if (p.cancelled) { conn.close(); throw new Error('cancelled'); }
    p.conn = conn;
    const ch = await conn.recv();
    if (ch.type === T.Error) throw errorOf(ch);
    if (ch.type !== T.Challenge || !ch.msg) throw new Error('closed');
    conn.send(T.Hello, { proto: PROTO });
    p.ch = ch.msg;
    p.nonce = fromB64u(ch.msg.nonce);
    p.at = now();
    p.key = await newResumeKey();
    p.state = 'ready';
    status('pair-status', '');
  })().catch(e => {
    p.state = 'failed';
    if (p.cancelled || S.pairing !== p) return;
    if (e instanceof HandshakeError && e.code === 'proto') {
      status('pair-status', protoText(e.detail, p.q.name));
      return;
    }
    status('pair-status', `${p.q.name || 'The machine'} is not reachable`);
  });
}

function pairContinue(ev) {
  ev.preventDefault();
  const p = S.pairing;
  if (!p || !p.q) return;
  const name = $('pair-device').value.trim().slice(0, 64) || 'iPhone';
  if (p.state === 'connecting') return;
  // The daemon closes a connection that has not authenticated within 60 s.
  if (p.state !== 'ready' || p.conn.closed || now() - p.at > 50000) {
    if (p.conn) p.conn.close();
    pairConnect(p);
    return;
  }
  p.state = 'creating';
  const conn = p.conn;
  const machineName = capName(p.ch.name) || p.q.name || 'machine';
  withAuth(() => createPasskey(p.nonce, machineName, name)).then(async reg => {
    $('pair-name').hidden = true;
    const code = await sasCode(p.q.token, p.q.hash, reg.pubKey);
    conn.send(T.Pair, { token: b64u(p.q.token), reg: regWire(reg, name), session_pub: b64u(p.key.spki) });
    $('pair-sas').textContent = code;
    $('pair-code').hidden = false;
    for (;;) {
      const m = await conn.recv(150000);
      if (m.type === T.PairCode) continue;
      if (m.type === T.Error) throw errorOf(m);
      if (m.type !== T.Authed) throw new Error('closed');
      await pairDone(p, conn, reg, m.msg, machineName);
      return;
    }
  }).catch(e => {
    if (p.cancelled || S.pairing !== p) return;
    conn.close();
    $('pair-code').hidden = true;
    S.pairing = null;
    const refused = e instanceof HandshakeError;
    const text = refused && e.code === 'proto' ? protoText({ ...e.detail, challenge: p.ch.proto }, machineName)
      : refused ? 'The computer refused the pairing' : 'Pairing cancelled';
    notice(text, [
      { label: 'Pair a machine', fn: startPair, primary: true },
      { label: 'Machines', fn: goHome },
    ]);
  });
}

async function pairDone(p, conn, reg, a, machineName) {
  await reloadMachines();
  const place = placeMachine(S.machines, machineName, p.q.addrs);
  let hashes = parseHashes(a && a.hashes);
  if (!hashes.length) hashes = [{ nb: nowSec() - 60, na: nowSec() + 13 * 86400, h: p.q.hash }];
  const m = {
    id: place.id || crypto.randomUUID(),
    name: place.name,
    addrs: p.q.addrs,
    port: p.q.port,
    hashes,
    credId: reg.credId,
    deviceId: a && a.device_id ? a.device_id : '',
    lastAddr: conn.addr,
    lastKey: null,
    used: now(),
    reach: { ok: true, at: now() },
  };
  await store.putMachine(m);
  if (a && a.resume_id) await store.putResume({ machineId: m.id, resumeId: a.resume_id, key: p.key.key, issued: now(), seen: now() });
  else await store.deleteResume(m.id);
  await reloadMachines();
  S.pairing = null;
  S.machine = m;
  S.key = null;
  S.sessions = [];
  S.term.reset();
  S.retry = 0;
  startSession(conn);
}

function cancelPair() {
  const p = S.pairing;
  S.pairing = null;
  if (p) {
    p.cancelled = true;
    p.scan.stop();
    if (p.conn) p.conn.close();
  }
  goHome();
}

// ---- terminal screen controls

const KEYS = { esc: 'escape', tab: 'tab', ctrlc: 'ctrl+c', left: 'left', down: 'down', up: 'up', right: 'right' };

function wireUI() {
  $('pair-btn').addEventListener('click', startPair);
  $('pair-cancel').addEventListener('click', cancelPair);
  $('pair-name').addEventListener('submit', pairContinue);
  $('t-list').addEventListener('click', openSheet);
  $('t-copy').addEventListener('click', () => {
    if (navigator.clipboard) navigator.clipboard.writeText(S.term.text()).catch(() => {});
  });
  $('sheet-close').addEventListener('click', () => { $('sheet').hidden = true; });
  $('sheet-machines').addEventListener('click', goHome);
  for (const b of $('keys').querySelectorAll('button[data-key]')) {
    // Keep the keyboard up: the buttons never take the focus.
    b.addEventListener('pointerdown', e => e.preventDefault());
    b.addEventListener('click', () => {
      const name = KEYS[b.dataset.key];
      if (name && S.key) send(T.Keys, { names: [name] });
    });
  }
  const input = $('t-input');
  input.addEventListener('keydown', e => {
    if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return;
    e.preventDefault();
    if (!S.key) return;
    send(T.Text, { text: input.value.replace(/\r\n?/g, '\n'), enter: true });
    input.value = '';
  });
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      if (!S.authing) toBackground();
      return;
    }
    clearTimeout(S.hiddenTimer);
    if (S.live && S.machine && !S.conn && !S.authing) {
      S.retry = 0;
      connect(S.machine, { reconnect: true });
    }
  });
}

// toBackground closes the transport: iOS cuts it in the background anyway,
// and closing now starts the resume window now. S.live stays: back in
// front, the app reconnects (resume, no Face ID within 5 minutes).
function toBackground() {
  closeConn();
}

// The app takes the visible viewport's height, so the key row and the
// text field stay above the iOS keyboard.
function setupViewport() {
  const app = $('app');
  const vv = window.visualViewport;
  const fit = () => {
    const h = vv ? vv.height : window.innerHeight;
    app.style.height = h + 'px';
    window.scrollTo(0, 0);
    if (S.term) {
      S.term.resize();
      // Rotation changes the width: the terminal takes the new size. The
      // keyboard only changes the height and leaves the size alone.
      const w = $('band').clientWidth;
      if (w !== S.lastWidth) {
        S.lastWidth = w;
        clearTimeout(S.sizeTimer);
        S.sizeTimer = setTimeout(sendSize, 200);
      }
    }
  };
  if (vv) {
    vv.addEventListener('resize', fit);
    vv.addEventListener('scroll', () => window.scrollTo(0, 0));
  }
  window.addEventListener('resize', fit);
  document.addEventListener('gesturestart', e => e.preventDefault());
  fit();
}

// ---- service worker: the cache is named after /version.json's version
// (sw.js?v=), so a new neonmux installs a new worker. It takes over only
// when no terminal is open.

// A development build ("devel", or a "+dirty" pseudo-version) keeps its
// version across rebuilds: the cache name then also carries a hash of the
// app's own files (fonts and jsQR left out), fetched past the cache. Keep in
// step with FILES in sw.js (TestWebDevFilesInSW).
const DEV_FILES = ['/', '/app.css', '/manifest.json', '/app.js', '/ui.js', '/store.js', '/machines.js', '/wire.js',
  '/auth.js', '/conn.js', '/pair.js', '/term.js', '/boxdraw.js', '/sw.js'];

async function buildID(v) {
  if (!/devel|dirty/.test(v)) return v;
  const parts = await Promise.all(DEV_FILES.map(f => fetch(f + '?fresh=1', { cache: 'no-store' }).then(r => r.arrayBuffer())));
  const all = new Uint8Array(parts.reduce((n, b) => n + b.byteLength, 0));
  let o = 0;
  for (const b of parts) { all.set(new Uint8Array(b), o); o += b.byteLength; }
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', all));
  return v + '-' + b64u(sum.subarray(0, 6));
}

async function setupSW() {
  if (!('serviceWorker' in navigator)) return;
  const hadController = !!navigator.serviceWorker.controller;
  let reg = null;
  try {
    const r = await fetch('/version.json', { cache: 'no-store' });
    if (r.ok) {
      const v = (await r.json()).version;
      if (typeof v === 'string' && v) reg = await navigator.serviceWorker.register('/sw.js?v=' + encodeURIComponent(await buildID(v)));
    }
  } catch { /* offline, away from home: keep the installed worker */ }
  if (!reg) reg = await navigator.serviceWorker.getRegistration().catch(() => null);
  if (!reg) return;
  S.trySkip = () => {
    if (reg.waiting && S.screen === 'home' && !S.conn && !S.pairing) reg.waiting.postMessage('skipWaiting');
  };
  reg.addEventListener('updatefound', () => {
    const w = reg.installing;
    if (w) w.addEventListener('statechange', () => { if (w.state === 'installed') S.trySkip(); });
  });
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (hadController && S.screen === 'home') location.reload();
  });
  S.trySkip();
}

boot();
