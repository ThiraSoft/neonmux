// neonmux remote: pairs with daemons, connects over WebTransport, shows one
// terminal at a time. Protocol and checks: internal/remote (conn.go).

import { T, PROTO, b64u, fromB64u, parseQR, parseHashes, validHashes, mergeAddrs, capName, newTermText } from './wire.js';
import { sasCode, createPasskey, regWire, getAssertion, newResumeKey, resumeSig } from './auth.js';
import { open } from './conn.js';
import { signThenSend } from './deliver.js';
import { startScan } from './pair.js';
import { Term, loadFonts } from './term.js';
import { chromeVars, applyChrome } from './chrome.js';
import * as store from './store.js';
import { placeMachine, pairConnUsable, pairReopen, PAIR_TTL, cleanThemes, themeChoice, migrateTheme, chromeFor, machineDetails } from './machines.js';
import { $, show, notice, hideNotice, status, renderMachines, renderSessions, renderThemes, renderDetails, nextSessionKey, prevSessionKey, sortedSessions, agentClass, panePosition, blockedElsewhere, quickReplies, paneJobs, fmtIn, appLabel } from './ui.js';

const RESUME_WINDOW = 5 * 60 * 1000 - 15000; // resume.go: 5 min after the last disconnect
const RESUME_LIFE = 12 * 3600 * 1000 - 60000; // resume.go: 12 h after issue
const TAKE_AFTER = 600; // ms on a terminal reached by a double tap before taking it
const SLIDE_MS = 260; // a bit more than the animation in app.css
const HIDDEN_GRACE = 2000; // hidden right after Face ID: the sheet itself may hide the page
const RETRY = [500, 1000, 2000, 5000, 10000, 20000];
const NEW_TERM_WAIT = 15000; // the daemon waits 5 s for the window, then opens it itself
const HOLD_CTRLC = 500; // ms on ^C before it is sent: it can end an agent
const CTRLC_AGAIN = 3000; // ms after that when a tap sends the second one
const BKSP_DELAY = 400, BKSP_EVERY = 50; // ms: a held backspace, as on a keyboard

const S = {
  screen: 'home',
  machines: [],
  machine: null,
  conn: null,
  live: false,       // the terminal screen wants a connection (reconnect when visible)
  sessions: [],
  themes: [],        // the app's themes (/themes.json), for the pickers
  theme: '',         // the app's theme, "" follows the desktop of the machine connected
  slide: true,       // terminals slide when switching
  takeTimer: 0,      // the Watch that takes a terminal only peeked at
  lastSeen: null,    // the last Theme seen while following the desktop
  setThemeOK: false, // the machine connected knows SetTheme (Authed listed themes)
  newTerm: 0,        // timer of the NewTerm waiting for its answer
  key: null,         // watched session
  pickOnList: false, // open the session list when it arrives
  term: null,
  retry: 0,
  retryTimer: 0,
  attempt: 0,        // bumps on every connect, so stale attempts give up
  veiled: false,     // the terminal is hidden until the next authentication
  authing: 0,        // a WebAuthn call is running: ignore visibility changes
  hiddenTimer: 0,
  pairing: null,
  detail: null,      // machine shown in the details sheet
  lastWidth: 0,
  sizeTimer: 0,
  seenTimer: 0,
  trySkip: () => {},
  online: false,     // authenticated on a connection that is still open
  connText: '',      // what the connection bar says while cut off
  ctrl: false,       // the ctrl key is on for the next letter
  push: false,       // this phone wants notifications
  pushOK: false,     // the machine connected takes PushSub
  bannerKey: null,   // the terminal the banner leads to
  repliesSig: '',    // the quick replies shown
  repliesTimer: 0,
  hintTimer: 0,
  scheduledJobs: [], // the machine's scheduled commands, the next one first
  jobsAt: 0,         // when they came: their in_sec count from there
  jobsTimer: 0,
  limit: null,       // the usage limit the machine saw on the pane (Limit)
  scheduleTimeout: 0,
};

let openScheduleSheet = () => {};

const now = () => Date.now();
const nowSec = () => Math.floor(now() / 1000);

// PairLost: the Pair frame could not be written (connection gone).
class PairLost extends Error {}

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
  S.term = new Term($('band'), $('screen'), {
    onScroll: offset => send(T.Scroll, { offset }),
    onWheel: (steps, x, y) => send(T.Wheel, { steps, x, y }),
    onNextPane: () => stepPane('next'),
    onPrevPane: () => stepPane('prev'),
    onSelect: showSelection,
  });
  S.lastWidth = $('band').clientWidth;
  try { S.machines = await store.listMachines(); } catch { S.machines = []; }
  await loadThemes();
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.addEventListener('message', e => {
      if (e.data && e.data.open) openTarget(e.data.open);
    });
  }
  if (!S.machines.length) { goHome(); return; }
  // Started by a tap on a notification (sw.js).
  const q = new URLSearchParams(location.search);
  if (q.has('device')) {
    history.replaceState(null, '', '/');
    if (openTarget({ key: q.get('key'), device: q.get('device') })) return;
  }
  const last = S.machines.slice().sort((a, b) => (b.used || 0) - (a.used || 0))[0];
  connect(last);
}

// openTarget goes to the terminal a notification is about. device is the id
// the machine gave this phone; false when no machine has it anymore.
function openTarget({ key, device }) {
  const m = typeof device === 'string' && device ? S.machines.find(x => x.deviceId === device) : null;
  if (!m) return false;
  const k = typeof key === 'string' && key ? key : null;
  hideNotice();
  $('sheet').hidden = true;
  $('settings').hidden = true;
  if (S.conn && S.online && S.machine && S.machine.id === m.id) {
    if (k) watch(k);
    return true;
  }
  if (k) m.lastKey = k;
  connect(m);
  return true;
}

// loadThemes reads the themes (cached by the service worker, so it works
// away from home) and the app's choice, and colors the interface before
// any connection.
async function loadThemes() {
  try {
    const r = await fetch('/themes.json');
    if (r.ok) S.themes = cleanThemes(await r.json());
  } catch { S.themes = []; }
  let pref, seen, slide, push;
  try { [pref, seen, slide, push] = await Promise.all([store.getPref('theme'), store.getPref('chrome'), store.getPref('slide'), store.getPref('push')]); } catch { /* defaults */ }
  S.push = push === 'on';
  S.theme = migrateTheme(pref, S.machines);
  S.slide = slide !== 'off';
  if (pref === undefined) store.putPref('theme', S.theme).catch(() => {});
  if (seen === undefined) {
    // Older versions kept the colors with each machine.
    const last = S.machines.slice().sort((a, b) => (b.used || 0) - (a.used || 0))[0];
    seen = last && last.chrome;
  }
  S.lastSeen = chromeVars(seen) ? seen : null;
  showTheme();
}

// showTheme colors the interface and sets both pickers to the choice.
function showTheme() {
  const name = themeChoice(S.theme, S.themes);
  applyChrome(chromeFor(name, S.themes, S.lastSeen));
  renderThemes('sheet-theme', 'theme-row', S.themes, name);
}

function pickSlide(on) {
  S.slide = on;
  store.putPref('slide', on ? 'on' : 'off').catch(() => {});
}

function pickTheme(value) {
  S.theme = themeChoice(value, S.themes);
  store.putPref('theme', S.theme).catch(() => {});
  showTheme();
  if (S.conn && S.setThemeOK) send(T.SetTheme, { name: S.theme });
}

function goHome() {
  closeConn();
  S.live = false;
  S.machine = null;
  S.key = null;
  $('sheet').hidden = true;
  setConn('');
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
    onOpen: m => connect(m),
    onForget: forget,
    onDetails: details,
  });
}

async function forget(m) {
  await store.deleteMachine(m.id);
  await reloadMachines();
  drawMachines();
}

// details opens the details sheet of m: Connect, or Forget once confirmed.
function details(m) {
  renderDetails(m.name, machineDetails(m, now()));
  S.detail = m;
  $('mdetail').hidden = false;
}

// reach records the result of a real connection to m: the list shows it
// as the machine's dot. Nothing probes the machines (a probe session would
// count as a failed handshake on the daemon).
function reach(m, ok, why) {
  m.reach = { ok, at: now() };
  if (why) m.reach.why = String(why).slice(0, 300);
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
  newTermOver();
  clearTimeout(S.retryTimer);
  clearInterval(S.seenTimer);
  S.attempt++;
  S.online = false;
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

// title says which terminal this is, with the state of its agent and its
// place in the order of the double tap. The terminal comes first: on a narrow
// phone it is the machine's name that gets cut off.
function title() {
  const s = S.sessions.find(x => x.key === S.key);
  const machine = S.machine ? S.machine.name : '';
  $('t-title').textContent = s ? (s.name || s.key) + (machine ? ' · ' + machine : '') : machine;
  const cls = agentClass(s && s.agent);
  $('t-dot').className = 'dot ' + cls;
  $('t-dot').hidden = !cls;
  const pos = panePosition(S.sessions, S.key);
  $('t-pos').textContent = pos;
  $('t-pos').hidden = !pos;
}

// ---- what a waiting agent asks for: a banner for the other terminals, quick
// replies for this one

function refreshAgent() {
  clearTimeout(S.repliesTimer);
  S.repliesTimer = 0;
  const b = S.veiled ? null : blockedElsewhere(S.sessions, S.key);
  S.bannerKey = b ? b.key : null;
  $('t-banner').textContent = b ? b.text + ' ›' : '';
  $('t-banner').hidden = !b;

  const s = S.sessions.find(x => x.key === S.key);
  const list = S.veiled || !s ? [] : quickReplies(s.agent, S.term.text());
  const sig = JSON.stringify(list);
  if (sig !== S.repliesSig) {
    S.repliesSig = sig;
    const row = $('t-replies');
    row.replaceChildren(...list.map(r => {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.textContent = r.label;
      // Keep the keyboard up, as the key row does.
      btn.addEventListener('pointerdown', e => e.preventDefault());
      btn.addEventListener('click', () => {
        if (!S.key) return;
        const enter = r.send.endsWith('\n');
        send(T.Text, { text: enter ? r.send.slice(0, -1) : r.send, enter });
      });
      return btn;
    }));
    row.hidden = !list.length;
  }
}

// A menu can appear after the state changed: while this terminal's agent is
// waiting, the screen is looked at again as it changes.
function agentFrame() {
  const s = S.sessions.find(x => x.key === S.key);
  if (S.repliesTimer || !s || !s.agent || s.agent.state !== 'blocked') return;
  S.repliesTimer = setTimeout(refreshAgent, 200);
}

// showLimit fills the sheet with the usage limit the machine saw on the
// pane, or leaves the plain scheduler.
function showLimit() {
  const l = S.limit;
  $('schedule-limit-box').hidden = !l;
  $('schedule-custom-label').hidden = !l;
  if (!l) {
    $('schedule-title').textContent = 'Schedule command';
    return;
  }
  const app = appLabel(l.app);
  $('schedule-title').textContent = `Resume ${app}`;
  $('schedule-limit-app').textContent = `${app} limit`;
  $('schedule-limit-reset').textContent = `resets ${l.reset_at}`;
  $('schedule-limit-text').textContent = l.message || `${app} hit its usage limit.`;
  $('schedule-limit-time').textContent = l.resume_at;
  // The time field gets it too, for a change of command.
  const t = $('schedule-time');
  if (t.value === '+5m') {
    t.value = l.resume_at.replace(/^tomorrow /, '');
    for (const b of document.querySelectorAll('#schedule-sheet .chip')) b.classList.remove('active');
  }
}

function renderScheduled() {
  const elapsed = S.jobsAt ? (now() - S.jobsAt) / 1000 : 0;
  const { mine, others } = paneJobs(S.scheduledJobs, S.key);
  $('t-send').classList.toggle('has-scheduled', mine.length > 0);

  const banner = $('t-scheduled-banner');
  if (mine.length > 0 && !S.veiled) {
    const next = mine[0];
    const txt = document.createElement('div');
    txt.className = 'scheduled-banner-txt';
    const more = mine.length > 1 ? ` · +${mine.length - 1}` : '';
    txt.textContent = `${next.desc} at ${next.run_at} · in ${fmtIn(next.in_sec - elapsed)}${more}`;
    const manage = document.createElement('span');
    manage.className = 'scheduled-banner-btn';
    manage.textContent = 'Manage ›';
    banner.replaceChildren(txt, manage);
    banner.hidden = false;
    banner.onclick = () => openScheduleSheet();
  } else {
    banner.hidden = true;
    banner.onclick = null;
  }
  // The countdown moves while something is to come.
  clearTimeout(S.jobsTimer);
  if (mine.length > 0) S.jobsTimer = setTimeout(renderScheduled, 15000);

  const sec = $('schedule-pending-section');
  const list = $('schedule-pending-list');
  const shown = [...mine, ...others];
  sec.hidden = shown.length === 0;
  list.replaceChildren(...shown.map(j => {
    const li = document.createElement('li');
    li.className = 'schedule-job-item';
    const info = document.createElement('div');
    info.className = 'schedule-job-info';
    const title = document.createElement('div');
    title.className = 'schedule-job-title';
    const time = document.createElement('span');
    time.className = 'schedule-job-time';
    time.textContent = j.run_at;
    const dur = document.createElement('span');
    dur.className = 'schedule-job-in';
    dur.textContent = `in ${fmtIn(j.in_sec - elapsed)}`;
    title.append(time, dur);
    if (j.pane !== S.key) {
      const where = document.createElement('span');
      where.className = 'schedule-job-pane';
      where.textContent = j.pane_name || j.pane;
      title.append(where);
    }
    const cmd = document.createElement('div');
    cmd.className = 'schedule-job-cmd';
    cmd.textContent = j.desc;
    info.append(title, cmd);
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'schedule-job-cancel';
    cancel.textContent = 'Cancel';
    cancel.addEventListener('click', () => {
      cancel.disabled = true;
      cancel.textContent = '…';
      send(T.ScheduleCancel, { id: j.id });
    });
    li.append(info, cancel);
    return li;
  }));
}

// ---- connection state

// setConn says why the terminal is cut off from the machine ('': it is not).
function setConn(text) {
  S.connText = text;
  syncOffline();
}

// syncOffline shows that nothing typed now would reach the machine: the screen
// is greyed out, the keys are off. The text field keeps what is being typed.
function syncOffline() {
  const off = S.live && !S.online;
  $('band').classList.toggle('offline', off);
  $('keys').classList.toggle('offline', off);
  $('t-replies').classList.toggle('offline', off);
  $('t-input').readOnly = off;
  $('t-send').disabled = off;
  $('t-conn-text').textContent = S.connText;
  $('t-conn').hidden = !(off && S.connText);
}

// veil hides what the terminal showed until the phone has proved who it is
// again: gone to the background (the app switcher's picture included), or
// about to ask for Face ID. The list goes too, its names and directories come
// back with the session.
function veil() {
  S.veiled = true;
  $('band').classList.add('veiled');
  $('sheet').hidden = true;
  S.sessions = [];
  title();
  refreshAgent();
  renderScheduled();
}

function unveil() {
  S.veiled = false;
  $('band').classList.remove('veiled');
  renderScheduled();
}

// connect opens m, authenticates (resume, else Face ID) and starts the
// terminal screen. A machine it cannot reach is tried again for as long as the
// app is on screen: a phone waking up, a wifi coming back, a restart.
async function connect(m, { reconnect = false } = {}) {
  closeConn();
  hideNotice();
  const attempt = S.attempt;
  S.machine = m;
  S.live = true;
  if (!reconnect) {
    S.retry = 0;
    S.connText = '';
    S.key = m.lastKey || null;
    S.sessions = [];
    S.setThemeOK = false;
    S.term.reset();
  }
  setScreen('term');
  title();
  syncOffline();
  status('t-status', S.connText ? '' : 'Connecting…');

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
  } catch (e) {
    if (attempt !== S.attempt) return;
    reach(m, false, e && e.why);
    scheduleRetry(`${m.name} is not reachable, trying again…`);
    return;
  }
  if (attempt !== S.attempt) { conn.close(); return; }
  reach(m, true);
  S.conn = conn; // onclose is set once authenticated (startSession)
  try {
    await handshake(conn, m, attempt);
  } catch (e) {
    if (attempt !== S.attempt) return;
    // The handshake may have moved to a new connection (Face ID outlived
    // the first one): close whichever is current.
    for (const c of new Set([conn, S.conn])) {
      if (c) { c.onclose = null; c.close(); }
    }
    S.conn = null;
    handshakeFailed(m, e, { reconnect });
  }
}

function handshakeFailed(m, e, { reconnect }) {
  if (S.machine !== m || S.screen !== 'term') return; // left meanwhile
  S.live = false;
  syncOffline();
  status('t-status', '');
  const code = e instanceof HandshakeError ? e.code : 'closed';
  if (code === 'resume') { S.live = true; connect(m, { reconnect: true }); return; }
  if (code === 'proto') {
    notice(protoText(e.detail, m.name), [{ label: 'Machines', fn: goHome, primary: true }]);
    return;
  }
  if (code === 'busy') {
    notice(`${m.name} has too many phones connected`, [
      { label: 'Retry', fn: () => connect(m), primary: true },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (code === 'auth') {
    notice(`${m.name} did not accept this phone`, [
      { label: 'Retry', fn: () => connect(m), primary: true },
      { label: 'Pair a machine', fn: startPair },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (code === 'cancel') {
    notice(`Unlock ${m.name}`, [
      { label: 'Unlock with Face ID', fn: () => connect(m), primary: true },
      { label: 'Machines', fn: goHome },
    ]);
    return;
  }
  if (reconnect) { S.live = true; scheduleRetry(); return; }
  notice(`Lost the connection to ${m.name}`, [
    { label: 'Retry', fn: () => connect(m), primary: true },
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
    conn.send(T.Resume, { id: r.resumeId, sig: b64u(sig), nonce: ch.msg.nonce });
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
  // WebAuthn inside the tap, on this same connection. iOS may also kill the
  // connection while the Face ID sheet is up: the assertion then goes on a
  // new connection (the daemon still takes this nonce, once).
  // Past the resume window: what was on screen is erased, not only hidden.
  veil();
  S.term.reset();
  const key = await newResumeKey();
  status('t-status', '');
  const sign = async () => {
    try {
      return await withAuth(() => getAssertion(nonce, m.credId));
    } catch {
      if (attempt !== S.attempt || conn.closed) throw new HandshakeError('cancel');
      return tapForFaceID(m, conn, () => withAuth(() => getAssertion(nonce, m.credId)));
    }
  };
  const reopen = async () => {
    if (attempt !== S.attempt) throw new HandshakeError('closed');
    const c = await open(m.addrs, m.port, validHashes(m.hashes || [], nowSec()), { preferred: m.lastAddr });
    if (attempt !== S.attempt) { c.close(); throw new HandshakeError('closed'); }
    S.conn = c;
    return c;
  };
  let sent;
  try {
    sent = await signThenSend(sign, assertion => {
      status('t-status', 'Connecting…');
      return { assertion, session_pub: b64u(key.spki) };
    }, T.Auth, () => conn, reopen);
  } catch (e) {
    throw e instanceof HandshakeError ? e : new HandshakeError('closed');
  }
  const c = sent.conn;
  if (attempt !== S.attempt) throw new HandshakeError('closed');
  S.conn = c;
  const reply = await c.recv();
  if (reply.type === T.Error) throw errorFrom(reply);
  if (reply.type !== T.Authed) throw new HandshakeError('closed');
  await authed(c, m, reply.msg, key);
}

// tapForFaceID shows a button that runs get inside the tap (iOS may want
// a gesture). A failed or cancelled Face ID shows the button again, on the
// same connection, until the daemon closes it (60 s handshake deadline):
// every new connection that ends unauthenticated counts against the phone.
// A connection lost while Face ID runs does not matter: the assertion is
// delivered on a new one.
function tapForFaceID(m, conn, get) {
  return new Promise((resolve, reject) => {
    const was = conn.onclose;
    let over = false, signing = false;
    const finish = () => { over = true; conn.onclose = was; };
    conn.onclose = () => {
      if (over || signing) return;
      finish();
      hideNotice();
      reject(new HandshakeError('cancel'));
    };
    const ask = () => {
      if (over) return;
      notice(`Unlock ${m.name}`, [
        {
          label: 'Unlock with Face ID', primary: true,
          fn: () => {
            signing = true;
            get().then(a => {
              signing = false;
              if (!over) { finish(); resolve(a); }
            }, () => {
              signing = false;
              if (over) return;
              if (!conn.closed) { ask(); return; }
              finish();
              reject(new HandshakeError('cancel'));
            });
          },
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
  m.addrs = mergeAddrs(a && a.addrs, m.addrs);
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
  S.setThemeOK = !!(a && Array.isArray(a.themes) && a.themes.length);
  S.pushOK = !!(a && a.push);
  startSession(conn);
}

function startSession(conn) {
  unveil();
  S.conn = conn;
  S.live = true;
  S.online = true;
  setConn('');
  conn.onclose = () => onClosed(conn);
  conn.onmessage = onMessage;
  // Messages that came in with Authed, before onmessage was set.
  for (const m of conn.queue.splice(0)) onMessage(m);
  setScreen('term');
  status('t-status', '');
  title();
  clearInterval(S.seenTimer);
  S.seenTimer = setInterval(() => markSeen(S.machine), 60000);
  // The phone's theme, before the first Watch so its first frame has it.
  // A daemon that lists no themes does not know SetTheme.
  if (S.setThemeOK) send(T.SetTheme, { name: themeChoice(S.theme, S.themes) });
  send(T.List);
  if (S.key) watch(S.key);
  else S.pickOnList = true;
  sendPush().catch(() => {});
}

function onClosed(conn) {
  if (conn !== S.conn) return;
  S.conn = null;
  S.online = false;
  syncOffline();
  newTermOver();
  clearInterval(S.seenTimer);
  markSeen(S.machine);
  if (document.hidden || !S.live || !S.machine) return;
  scheduleRetry();
}

// scheduleRetry reconnects with a growing delay (the daemon was restarted,
// the wifi dropped). Each try is one connection: the daemon bans an address
// after 5 failed handshakes in a minute.
function scheduleRetry(text) {
  clearTimeout(S.retryTimer);
  const m = S.machine;
  if (!m) return;
  const d = RETRY[Math.min(S.retry, RETRY.length - 1)];
  S.retry++;
  status('t-status', '');
  setConn(text || `Reconnecting to ${m.name}…`);
  S.retryTimer = setTimeout(() => {
    if (!document.hidden && S.live && S.machine === m) connect(m, { reconnect: true });
  }, d);
}

function onMessage({ type, msg }) {
  switch (type) {
    case T.Sessions:
      S.sessions = msg && Array.isArray(msg.list) ? msg.list.filter(s => s && typeof s.key === 'string') : [];
      title();
      refreshAgent();
      if (!$('sheet').hidden) drawSessions();
      if (S.pickOnList) { S.pickOnList = false; openSheet(); }
      break;
    case T.Theme:
      if (msg) {
        S.term.setTheme(msg);
        themeChrome(msg);
      }
      break;
    case T.Frame:
      if (msg && S.key) {
        S.term.setFrame(msg);
        agentFrame();
      }
      break;
    case T.Image:
      if (msg) S.term.setImage(msg);
      break;
    case T.Exited:
      S.key = null;
      S.term.reset();
      title();
      refreshAgent();
      openSheet();
      break;
    case T.NewTermDone:
      if (!S.newTerm) break;
      newTermOver();
      if (msg && typeof msg.key === 'string' && msg.key) {
        $('sheet').hidden = true;
        watch(msg.key);
      }
      break;
    case T.Error:
      if (msg && typeof msg.msg === 'string' && msg.msg.startsWith('push: ')) {
        pushHint(`This machine refused the subscription (${msg.msg.slice(6)})`);
        break;
      }
      if (msg && newTermText(msg.msg) && S.newTerm) {
        newTermOver();
        notice(newTermText(msg.msg), [{ label: 'OK', fn: () => {}, primary: true }]);
        break;
      }
      if (msg && msg.msg === 'frame too large') {
        notice('This terminal is too large to show on the phone', [{ label: 'OK', fn: () => {}, primary: true }]);
        break;
      }
      if (msg && msg.msg === 'session not found') {
        S.key = null;
        S.term.reset();
        title();
        openSheet();
      }
      break;
    case T.ScheduleDone: {
      if ($('schedule-sheet').hidden) break;
      clearTimeout(S.scheduleTimeout);
      const st = $('schedule-status');
      if (msg.error) {
        st.textContent = msg.cancelled ? msg.error : `Error: ${msg.error}`;
        st.className = 'hint schedule-status error';
        $('schedule-submit').disabled = false;
        $('schedule-limit-apply').disabled = false;
        break;
      }
      if (msg.cancelled) {
        st.textContent = '✓ Cancelled';
        st.className = 'hint schedule-status success';
        setTimeout(() => { if (st.textContent === '✓ Cancelled') st.textContent = ''; }, 2000);
        break;
      }
      st.textContent = `✓ ${msg.desc} at ${msg.run_at}`;
      st.className = 'hint schedule-status success';
      setTimeout(() => {
        $('schedule-sheet').hidden = true;
        $('schedule-submit').disabled = false;
        $('schedule-limit-apply').disabled = false;
        // What was in the field is now scheduled.
        const field = $('t-input');
        if (field.value.trim() === msg.desc) {
          field.value = '';
          field.dispatchEvent(new Event('input'));
        }
      }, 700);
      break;
    }
    case T.ScheduleJobs:
      S.scheduledJobs = Array.isArray(msg.jobs) ? msg.jobs : [];
      S.jobsAt = now();
      renderScheduled();
      break;
    case T.Limit:
      if (msg.pane !== S.key || $('schedule-sheet').hidden) break;
      S.limit = msg.found ? msg : null;
      showLimit();
      break;
  }
}

// themeChrome follows the Theme the machine sends. With a theme chosen, it
// is that theme: the interface already has it. Following the desktop, the
// colors are kept for the next start (home screen, pairing).
function themeChrome(msg) {
  if (themeChoice(S.theme, S.themes)) return;
  const v = chromeVars(msg);
  if (!v) return;
  applyChrome(v);
  const c = { fg: msg.fg, bg: msg.bg, cursor: msg.cursor, accent: msg.accent, accent2: msg.accent2, ansi: Array.isArray(msg.ansi) ? msg.ansi.slice(0, 16) : undefined };
  if (JSON.stringify(c) === JSON.stringify(S.lastSeen)) return;
  S.lastSeen = c;
  store.putPref('chrome', c).catch(() => {});
}

// watch selects a terminal, which takes it at the phone's size. With peek it
// is only shown, and taken once the user stays on it: going through the panes
// does not resize each one of them.
function watch(key, { peek = false } = {}) {
  clearTimeout(S.takeTimer);
  S.key = key;
  S.term.setSession(key);
  if (S.machine) {
    S.machine.lastKey = key;
    store.putMachine(S.machine).catch(() => {});
  }
  S.term.reset();
  title();
  refreshAgent();
  if (S.conn) send(T.ScheduleList, {});
  renderScheduled();
  if (peek) {
    send(T.Watch, { key, peek: true });
    S.takeTimer = setTimeout(() => {
      if (S.key !== key) return;
      send(T.Watch, { key });
      sendSize();
    }, TAKE_AFTER);
    return;
  }
  send(T.Watch, { key });
  sendSize();
}

let slideTimer = 0;

// slide copies the screen on the canvas above it; both leave in direction
// dir ('next' or 'prev') while the next terminal draws on the screen.
function slide(dir) {
  const band = $('band'), screen = $('screen'), old = $('screen-old');
  slideEnd();
  old.width = screen.width;
  old.height = screen.height;
  old.getContext('2d').drawImage(screen, 0, 0);
  old.hidden = false;
  void band.offsetWidth; // restarts the animation of a slide still running
  band.classList.add('fx-' + dir);
  slideTimer = setTimeout(slideEnd, SLIDE_MS);
}

function slideEnd() {
  clearTimeout(slideTimer);
  $('band').classList.remove('fx-next', 'fx-prev');
  const old = $('screen-old');
  old.hidden = true;
  old.width = old.height = 1;
}

// stepPane goes to the next or previous terminal; false when there is none.
function stepPane(dir) {
  if (!S.conn || S.screen !== 'term') return false;
  const key = (dir === 'next' ? nextSessionKey : prevSessionKey)(S.sessions, S.key);
  if (!key || key === S.key) return false;
  $('sheet').hidden = true;
  if (S.slide) slide(dir);
  watch(key, { peek: true });
  return true;
}

function sendSize() {
  if (!S.key) return;
  S.sent = S.term.fit();
  send(T.Size, S.sent);
}

// refit lays the app out again on the visible viewport and, the keyboard
// down, gives the terminal the band's full size when it does not have it:
// iOS sometimes skips the resize event when the keyboard goes away, and a
// size sent while it was up leaves the terminal short.
function refit(force) {
  S.fitViewport?.();
  if (!S.term || !S.key) return;
  S.term.resize();
  if (force) S.term.resetView();
  if (!force && document.activeElement === $('t-input')) return;
  const f = S.term.fit();
  clearTimeout(S.sizeTimer);
  if (force) {
    // One row less, then the right size: the program gets two SIGWINCH
    // and draws itself again, even when the size did not change.
    send(T.Size, { ...f, rows: Math.max(1, f.rows - 1) });
    S.sizeTimer = setTimeout(sendSize, 150);
  } else if (!S.sent || f.cols !== S.sent.cols || f.rows !== S.sent.rows) {
    sendSize();
  }
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
    if (key !== S.key) {
      const list = sortedSessions(S.sessions);
      const curIdx = list.findIndex(s => s && s.key === S.key);
      const newIdx = list.findIndex(s => s && s.key === key);
      if (S.slide) slide(curIdx >= 0 && newIdx >= 0 && newIdx < curIdx ? 'prev' : 'next');
      watch(key);
    }
  }, S.conn ? newTerm : null, !!S.newTerm);
}

// newTerm asks the machine for a new terminal at this phone's size; the
// answer (NewTermDone, then the phone watches it) comes in onMessage.
function newTerm() {
  if (!S.conn || S.newTerm) return;
  S.newTerm = setTimeout(() => {
    newTermOver();
    notice('Could not open a terminal', [{ label: 'OK', fn: () => {}, primary: true }]);
  }, NEW_TERM_WAIT);
  drawSessions();
  send(T.Size, S.term.fit());
  send(T.NewTerm);
}

// ---- settings

function openSettings() {
  renderThemes('sheet-theme', 'theme-row', S.themes, themeChoice(S.theme, S.themes));
  $('sheet-slide').checked = S.slide;
  $('settings-push').checked = S.push && pushSupported();
  $('settings-push').disabled = !pushSupported();
  pushHint();
  $('settings').hidden = false;
}

// ---- notifications: the daemon sends them (push.go), the service worker
// shows them. A subscription is bound to one VAPID key and a phone holds one
// subscription whatever the machines paired, so the key is made here and each
// machine gets it over its authenticated connection.

const pushSupported = () => 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;

function pushHint(text) {
  let t = text;
  if (t === undefined) {
    if (!pushSupported()) t = 'Open neonmux from the home screen to get notifications.';
    else if (S.machine && S.online && !S.pushOK) t = `Update neonmux on ${S.machine.name} to get its notifications.`;
    else if (S.push) t = 'Encrypted for this phone, carried by Apple\'s push service. Each machine starts once you have connected to it.';
    else t = '';
  }
  $('settings-push-hint').textContent = t;
  $('settings-push-hint').hidden = !t;
}

async function vapidKeys() {
  const saved = await store.getPref('vapid');
  if (saved && saved.pub && saved.priv) return saved;
  const kp = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign']);
  const raw = await crypto.subtle.exportKey('raw', kp.publicKey);
  const jwk = await crypto.subtle.exportKey('jwk', kp.privateKey);
  const v = { pub: b64u(raw), priv: jwk.d };
  await store.putPref('vapid', v);
  return v;
}

async function pushSubscription(create) {
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  if (sub || !create) return sub;
  const v = await vapidKeys();
  return reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromB64u(v.pub) });
}

// sendPush tells the machine connected what this phone wants, on every
// connection: that is how another machine learns it, and a new subscription
// reaches them all.
async function sendPush() {
  if (!S.conn || !S.pushOK) return;
  if (!S.push || !pushSupported()) {
    send(T.PushSub, { endpoint: '' });
    return;
  }
  const sub = await pushSubscription(Notification.permission === 'granted');
  if (!sub || !S.conn) return;
  const v = await vapidKeys();
  send(T.PushSub, {
    endpoint: sub.endpoint, p256dh: b64u(sub.getKey('p256dh')), auth: b64u(sub.getKey('auth')),
    vapid_pub: v.pub, vapid_priv: v.priv,
  });
}

// pushToggle runs inside the tap: iOS asks for the permission only then.
async function pushToggle(on) {
  const box = $('settings-push');
  if (on) {
    let perm = 'denied';
    try { perm = await Notification.requestPermission(); } catch { /* denied */ }
    if (perm !== 'granted') {
      box.checked = false;
      pushHint('Notifications are off for this app: iOS Settings, Notifications, neonmux.');
      return;
    }
    try {
      await pushSubscription(true);
    } catch (e) {
      box.checked = false;
      pushHint('Could not subscribe: ' + (e && e.message ? e.message : 'unknown error'));
      return;
    }
  } else {
    try {
      const sub = await pushSubscription(false);
      if (sub) await sub.unsubscribe();
    } catch { /* the machine is told below anyway */ }
  }
  S.push = on;
  store.putPref('push', on ? 'on' : 'off').catch(() => {});
  pushHint();
  sendPush().catch(() => {});
}

// ---- selection: a long press on the terminal picks lines (term.js)

function showSelection(n) {
  $('t-sel-n').textContent = n === 1 ? '1 line' : n + ' lines';
  $('t-sel').hidden = n === 0;
}

// hint says something for a moment, over the terminal: the band keeps its size.
function hint(text) {
  clearTimeout(S.hintTimer);
  $('t-hint').textContent = text;
  $('t-hint').hidden = false;
  S.hintTimer = setTimeout(() => { $('t-hint').hidden = true; }, 1500);
}

function newTermOver() {
  if (!S.newTerm) return;
  clearTimeout(S.newTerm);
  S.newTerm = 0;
  if (!$('sheet').hidden) drawSessions();
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
    p.scannedAt = now();
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
    // The daemon cuts it after 60 s without Pair: a slow name gets a new
    // one, so the tap on Continue still finds an open connection.
    conn.done.then(() => {
      if (S.pairing === p && pairReopen(p, conn, p.scannedAt, now())) pairConnect(p);
    });
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
  // A connection that can only pair lives until the token expires; a
  // closed one (the daemon gave up, the wifi dropped) is opened again first.
  if (p.state !== 'ready' || !pairConnUsable(p.conn, p.scannedAt, now())) {
    if (p.conn) p.conn.close();
    pairConnect(p);
    return;
  }
  p.state = 'creating';
  let conn = p.conn;
  // p.ch.name: a daemon older than the one that stopped sending it.
  const machineName = capName(p.ch.name) || p.q.name || 'machine';
  // iOS may kill the connection while the passkey sheets are up: the Pair
  // then goes on a new connection, with the passkey already created (the
  // daemon still takes its nonce, once). createPasskey runs inside the tap.
  const reopen = async () => {
    const c = await open(p.q.addrs, p.q.port, [p.q.hash]);
    if (p.cancelled) { c.close(); throw new Error('cancelled'); }
    p.conn = c;
    return c;
  };
  let reg = null;
  signThenSend(
    () => withAuth(() => createPasskey(p.nonce, machineName, name)),
    r => { reg = r; return { token: b64u(p.q.token), reg: regWire(r, name), session_pub: b64u(p.key.spki) }; },
    T.Pair, () => conn, reopen,
  ).catch(e => {
    // A passkey was made but could not be delivered: say so at once.
    throw reg && !(e instanceof HandshakeError) ? new PairLost() : e;
  }).then(async sent => {
    conn = sent.conn;
    $('pair-name').hidden = true;
    const code = await sasCode(p.q.token, p.q.hash, reg.pubKey);
    $('pair-sas').textContent = code;
    $('pair-code').hidden = false;
    const gone = conn.done.then(() => { throw new Error('closed'); });
    gone.catch(() => {});
    for (;;) {
      const m = await Promise.race([conn.recv(PAIR_TTL + 30000), gone]);
      if (m.type === T.PairCode) continue;
      if (m.type === T.Error) throw errorOf(m);
      if (m.type !== T.Authed) throw new Error('closed');
      await pairDone(p, conn, reg, m.msg, machineName);
      return;
    }
  }).catch(e => {
    if (p.cancelled || S.pairing !== p) return;
    conn.close();
    if (p.conn) p.conn.close();
    $('pair-code').hidden = true;
    S.pairing = null;
    const refused = e instanceof HandshakeError;
    const text = e instanceof PairLost ? `Lost the connection to ${machineName}`
      : refused && e.code === 'proto' ? protoText({ ...e.detail, challenge: p.ch.proto }, machineName)
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
    addrs: mergeAddrs(a && a.addrs, p.q.addrs),
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
  S.setThemeOK = !!(a && Array.isArray(a.themes) && a.themes.length);
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

const KEYS = { enter: 'enter', esc: 'escape', tab: 'tab', ctrlc: 'ctrl+c', left: 'left', down: 'down', up: 'up', right: 'right', bksp: 'backspace' };

function wireUI() {
  $('home-settings').addEventListener('click', openSettings);
  $('sheet-settings').addEventListener('click', openSettings);
  $('settings-close').addEventListener('click', () => { $('settings').hidden = true; });
  $('settings-push').addEventListener('change', e => pushToggle(e.target.checked));
  $('t-conn-home').addEventListener('click', goHome);
  $('t-banner').addEventListener('click', () => {
    if (!S.bannerKey) return;
    if (S.slide) slide('next');
    watch(S.bannerKey);
  });
  $('t-sel-cancel').addEventListener('click', () => S.term.clearSelection());
  $('t-sel-copy').addEventListener('click', () => {
    if (!S.veiled && navigator.clipboard) navigator.clipboard.writeText(S.term.selectedText()).catch(() => {});
    S.term.clearSelection();
  });
  setInterval(() => { if (!$('sheet').hidden) drawSessions(); }, 30000); // how long agents have waited
  $('pair-btn').addEventListener('click', startPair);
  $('pair-cancel').addEventListener('click', cancelPair);
  $('pair-name').addEventListener('submit', pairContinue);
  $('t-list').addEventListener('click', openSheet);
  $('t-refit').addEventListener('click', () => refit(true));
  $('t-copy').addEventListener('click', () => {
    if (!S.veiled && navigator.clipboard) navigator.clipboard.writeText(S.term.text()).catch(() => {});
  });
  $('sheet-close').addEventListener('click', () => { $('sheet').hidden = true; });
  $('mdetail-close').addEventListener('click', () => { $('mdetail').hidden = true; });
  $('mdetail-open').addEventListener('click', () => {
    $('mdetail').hidden = true;
    if (S.detail) connect(S.detail);
  });
  $('mdetail-forget').addEventListener('click', () => {
    const m = S.detail;
    if (!m) return;
    notice(`Forget ${m.name}? Pair it again to use it.`, [
      { label: 'Cancel', fn: () => {} },
      { label: 'Forget', fn: () => { $('mdetail').hidden = true; forget(m); }, primary: true },
    ]);
  });
  $('sheet-machines').addEventListener('click', goHome);
  $('sheet-theme').addEventListener('change', e => pickTheme(e.target.value));
  $('sheet-slide').addEventListener('change', e => pickSlide(e.target.checked));
  const input = $('t-input');
  // The field takes the height of its text, up to the CSS's max-height.
  const fitInput = () => {
    input.style.height = 'auto';
    input.style.height = (input.scrollHeight + input.offsetHeight - input.clientHeight) + 'px';
  };
  input.addEventListener('input', fitInput);
  const submit = () => {
    if (!S.key || !S.conn) return;
    send(T.Text, { text: input.value.replace(/\r\n?/g, '\n'), enter: true });
    input.value = '';
    fitInput();
  };
  // ctrl, shift and alt hold for the next key.
  const mods = {};
  const clearMods = () => {
    for (const m of ['ctrl', 'shift', 'alt']) {
      mods[m] = false;
      $('k-' + m).classList.remove('on');
    }
  };
  const combo = name => ['ctrl', 'alt', 'shift'].filter(m => mods[m]).map(m => m + '+').join('') + name;
  const anyMod = () => mods.ctrl || mods.shift || mods.alt;
  for (const m of ['ctrl', 'shift', 'alt']) {
    $('k-' + m).addEventListener('click', () => {
      mods[m] = !mods[m];
      $('k-' + m).classList.toggle('on', mods[m]);
    });
  }
  // Return sends the field; with a modifier armed it is a key for the
  // terminal (shift+enter is a new line in many programs).
  const enterKey = () => {
    if (input.value && !anyMod()) { submit(); return; }
    const name = combo('enter');
    clearMods();
    if (S.key) send(T.Keys, { names: [name] });
  };
  for (const b of [...$('keys').querySelectorAll('button'), $('t-send')]) {
    // Keep the keyboard up: the buttons never take the focus.
    b.addEventListener('pointerdown', e => e.preventDefault());
    b.addEventListener('contextmenu', e => e.preventDefault());
  }
  let sendHoldTimer = 0;
  let didSendHold = false;
  const sendBtn = $('t-send');

  const updateScheduleChips = val => {
    for (const b of document.querySelectorAll('#schedule-sheet .chip')) {
      b.classList.toggle('active', b.dataset.time === val);
    }
  };

  const scheduleBusy = busy => {
    $('schedule-submit').disabled = busy;
    $('schedule-limit-apply').disabled = busy;
  };

  const executeSchedule = (at, text) => {
    at = (at || '').trim();
    text = (text || '').trim();
    const status = $('schedule-status');
    if (!at || !text) {
      status.textContent = !at ? 'When? +5m, 17:21…' : 'What to type?';
      status.className = 'hint schedule-status error';
      return;
    }
    if (!S.conn) return;
    status.className = 'hint schedule-status';
    status.textContent = 'Scheduling…';
    scheduleBusy(true);
    send(T.Schedule, { at, text, pane: S.key });
    clearTimeout(S.scheduleTimeout);
    S.scheduleTimeout = setTimeout(() => {
      if (!$('schedule-sheet').hidden) {
        status.textContent = 'No answer from the machine';
        status.className = 'hint schedule-status error';
        scheduleBusy(false);
      }
    }, 6000);
  };

  openScheduleSheet = () => {
    const status = $('schedule-status');
    clearTimeout(S.scheduleTimeout);
    status.textContent = '';
    status.className = 'hint schedule-status';
    scheduleBusy(false);
    S.limit = null;
    showLimit();
    $('schedule-time').value = '+5m';
    $('schedule-cmd').value = input.value.trim() || 'continue';
    updateScheduleChips('+5m');
    $('schedule-sheet').hidden = false;
    // The machine reads the screen: one parser, and its own clock.
    if (S.conn && S.key) {
      send(T.LimitQuery, { pane: S.key });
      send(T.ScheduleList, {});
    }
    renderScheduled();
  };

  $('schedule-limit-apply').addEventListener('click', () => {
    if (S.limit) executeSchedule(S.limit.resume_iso, $('schedule-cmd').value.trim() || S.limit.command || 'continue');
  });

  const closeScheduleSheet = () => {
    $('schedule-sheet').hidden = true;
    clearTimeout(S.scheduleTimeout);
    scheduleBusy(false);
  };

  sendBtn.addEventListener('pointerdown', () => {
    didSendHold = false;
    clearTimeout(sendHoldTimer);
    sendHoldTimer = setTimeout(() => {
      didSendHold = true;
      openScheduleSheet();
    }, 500);
  });
  const cancelSendHold = () => { clearTimeout(sendHoldTimer); };
  sendBtn.addEventListener('pointerup', cancelSendHold);
  sendBtn.addEventListener('pointerleave', cancelSendHold);
  sendBtn.addEventListener('pointercancel', cancelSendHold);
  // A long press is the scheduler, not the system's callout.
  sendBtn.addEventListener('contextmenu', e => e.preventDefault());

  $('schedule-close').addEventListener('click', closeScheduleSheet);
  $('schedule-cancel').addEventListener('click', closeScheduleSheet);
  $('schedule-sheet').addEventListener('click', e => {
    if (e.target === $('schedule-sheet')) closeScheduleSheet();
  });

  for (const b of document.querySelectorAll('#schedule-sheet .chip')) {
    b.addEventListener('click', () => {
      $('schedule-time').value = b.dataset.time;
      updateScheduleChips(b.dataset.time);
    });
  }
  $('schedule-time').addEventListener('input', e => {
    updateScheduleChips(e.target.value.trim());
  });

  const submitSchedule = () => executeSchedule($('schedule-time').value, $('schedule-cmd').value);
  $('schedule-submit').addEventListener('click', submitSchedule);
  for (const id of ['schedule-time', 'schedule-cmd']) {
    $(id).addEventListener('keydown', e => {
      if (e.key === 'Enter' && !e.isComposing) {
        e.preventDefault();
        submitSchedule();
      }
    });
  }

  $('t-send').addEventListener('click', e => {
    if (didSendHold) {
      didSendHold = false;
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    enterKey();
  });
  // Every key of the row goes to the terminal; the field only gets what comes
  // from the phone's keyboard.
  for (const b of $('keys').querySelectorAll('button[data-key]')) {
    if (b.dataset.key === 'ctrlc' || b.dataset.key === 'bksp') continue;
    b.addEventListener('click', () => {
      const name = combo(KEYS[b.dataset.key]);
      clearMods();
      if (S.key) send(T.Keys, { names: [name] });
    });
  }
  // Backspace repeats while it is held.
  const bk = $('keys').querySelector('[data-key="bksp"]');
  let bkTimer = 0, bkRepeat = 0;
  const bkOff = () => {
    if (!bkTimer && !bkRepeat) return;
    clearTimeout(bkTimer);
    clearInterval(bkRepeat);
    bkTimer = bkRepeat = 0;
    clearMods();
  };
  bk.addEventListener('pointerdown', () => {
    bkOff();
    const name = combo(KEYS.bksp);
    const fire = () => {
      // A press whose release never comes (the app left the screen) stops here.
      if (document.hidden || !S.conn || !S.key) { bkOff(); return; }
      send(T.Keys, { names: [name] });
    };
    fire();
    bkTimer = setTimeout(() => { bkRepeat = setInterval(fire, BKSP_EVERY); }, BKSP_DELAY);
  });
  for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) bk.addEventListener(ev, bkOff);
  // ^C needs a long press: one stray tap should not end an agent. Programs
  // that ask for a second one in a short time get it with a tap.
  const cc = $('keys').querySelector('[data-key="ctrlc"]');
  let ccTimer = 0, ccSent = false, ccAgain = 0;
  const ccOff = () => { clearTimeout(ccTimer); ccTimer = 0; cc.classList.remove('arming'); };
  const ccAgainOff = () => { clearTimeout(ccAgain); ccAgain = 0; cc.classList.remove('on'); };
  cc.addEventListener('pointerdown', () => {
    ccSent = false;
    if (ccAgain) return;
    cc.classList.add('arming');
    ccTimer = setTimeout(() => {
      ccOff();
      ccSent = true;
      if (navigator.vibrate) navigator.vibrate(20);
      clearMods();
      if (S.key) send(T.Keys, { names: [KEYS.ctrlc] });
      cc.classList.add('on');
      ccAgain = setTimeout(ccAgainOff, CTRLC_AGAIN);
    }, HOLD_CTRLC);
  });
  for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) cc.addEventListener(ev, ccOff);
  cc.addEventListener('click', () => {
    if (ccSent) return;
    if (!ccAgain) { hint('Hold ^C to send it'); return; }
    ccAgainOff();
    clearMods();
    if (S.key) send(T.Keys, { names: [KEYS.ctrlc] });
  });
  // A modifier, then a character from the keyboard.
  input.addEventListener('beforeinput', e => {
    if (!anyMod() || typeof e.data !== 'string') return;
    if ((mods.ctrl || mods.alt) && /^[a-zA-Z0-9]$/.test(e.data)) {
      e.preventDefault();
      if (S.key) send(T.Keys, { names: [combo(e.data.toLowerCase())] });
    } else if (mods.shift && /^[a-z]$/.test(e.data)) {
      e.preventDefault();
      input.setRangeText(e.data.toUpperCase(), input.selectionStart, input.selectionEnd, 'end');
    }
    clearMods();
  });
  // The keyboard's return is a new line in the field, ⏎ sends. With a modifier
  // armed it is that key for the terminal.
  input.addEventListener('keydown', e => {
    if (e.key !== 'Enter' || e.isComposing || !anyMod()) return;
    e.preventDefault();
    enterKey();
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
  syncOffline();
}

// toBackground closes the transport: iOS cuts it in the background anyway,
// and closing now starts the resume window now. S.live stays: back in
// front, the app reconnects (resume, no Face ID within 5 minutes).
function toBackground() {
  closeConn();
  veil();
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
    // iOS may keep the visual viewport panned down (the focused field
    // scrolled into view): the app then sits at its top, not at the page's.
    app.style.top = (vv ? Math.max(0, vv.offsetTop) : 0) + 'px';
    // The sheets sit on the same visible part, above the keyboard.
    document.documentElement.style.setProperty('--vv-h', h + 'px');
    document.documentElement.style.setProperty('--vv-top', app.style.top);
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
    vv.addEventListener('scroll', fit);
  }
  window.addEventListener('resize', fit);
  S.fitViewport = fit;
  // The keyboard goes away: once it is gone, the terminal gets its height
  // back, even when iOS says nothing.
  $('t-input').addEventListener('blur', () => {
    clearTimeout(S.blurTimer);
    S.blurTimer = setTimeout(() => {
      refit(false);
      S.blurTimer = setTimeout(() => refit(false), 600);
    }, 350);
  });
  // The bars above and below the terminal come and go: the canvas follows.
  new ResizeObserver(() => { if (S.term) S.term.resize(); }).observe($('band'));
  // A field of a sheet stays in sight once the keyboard is up.
  document.addEventListener('focusin', e => {
    const f = e.target;
    if (!f.matches?.('.sheet input')) return;
    setTimeout(() => f.scrollIntoView({ block: 'nearest' }), 350);
  });
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
  '/auth.js', '/conn.js', '/deliver.js', '/pair.js', '/term.js', '/boxdraw.js', '/chrome.js', '/themes.json', '/sw.js'];

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
