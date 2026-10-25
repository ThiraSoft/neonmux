// Checks of the pure modules, shared by selftest.html (browser) and
// selftest.mjs (node: `node internal/remote/web/selftest.mjs`).

import {
  T, PROTO, MAX_PAYLOAD, encodeFrame, FrameReader, b64u, fromB64u, utf8, concat,
  newScreen, applyFrame, screenText, parseQR, machineURL, parseHashes, validHashes, newTermText,
} from './wire.js';
import { sasCode, newResumeKey, resumeSig } from './auth.js';
import { drawBoxRune, isBoxRune } from './boxdraw.js';
import { open, Conn } from './conn.js';
import { signThenSend } from './deliver.js';
import { placeMachine, pairConnUsable, pairReopen, PAIR_TTL, cleanThemes, themeChoice, migrateTheme, chromeFor } from './machines.js';
import { Term, blend, wheelMessages, cellUnder, fitPan } from './term.js';
import { chromeVars, contrast, CHROME_DEFAULTS } from './chrome.js';

const hex = b => Array.from(b, x => x.toString(16).padStart(2, '0')).join('');
const range = (a, n) => Uint8Array.from({ length: n }, (_, i) => a + i);
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

export async function run(log) {
  let pass = 0, fail = 0;
  const check = (name, ok, detail) => {
    if (ok) pass++; else fail++;
    log(ok, name + (ok || detail === undefined ? '' : ': ' + detail));
  };
  const throws = f => { try { f(); return false; } catch { return true; } };

  // ---- frames
  const samples = {
    [T.Hello]: { proto: PROTO },
    [T.Pair]: { token: b64u(range(0, 16)), reg: { cred_id: 'AQ', pub_key: 'Ag', auth_data: 'Aw', client_data: 'BA', name: 'iPhone' }, session_pub: 'BQ' },
    [T.Auth]: { assertion: { cred_id: 'AQ', auth_data: 'Ag', client_data: 'Aw', sig: 'BA' }, session_pub: 'BQ' },
    [T.Resume]: { id: '00ff', sig: b64u(range(1, 64)) },
    [T.List]: null,
    [T.Watch]: { key: 's1' },
    [T.Size]: { cols: 80, rows: 24 },
    [T.Scroll]: { offset: 5 },
    [T.Text]: { text: 'héllo 🎉\nworld', enter: true },
    [T.Keys]: { names: ['escape', 'ctrl+c'] },
    [T.Unwatch]: null,
    [T.Challenge]: { proto: 1, nonce: b64u(range(0, 32)), name: 'arch' },
    [T.PairCode]: { code: '483657' },
    [T.Authed]: { device_id: 'd', resume_id: 'r', hashes: [{ nb: 1, na: 2, h: b64u(range(0x20, 32)) }] },
    [T.Sessions]: { list: [{ key: 'k', name: '<b>x</b>', cwd: '/', garage: true, agent: { state: 'blocked', kind: 'question', msg: 'ok?', since: '2026-10-23T00:00:00Z' } }] },
    [T.Theme]: { fg: [1, 2, 3], bg: [4, 5, 6], cursor: [7, 8, 9], ansi: Array.from({ length: 16 }, (_, i) => [i, i, i]) },
    [T.Frame]: { cols: 4, rows: 2, offset: 0, hist: 0, cx: 0, cy: 0, cursor: true, full: true, lines: [[0, [['ab', 0xffffff, -1, 1]]], [1, null]] },
    [T.Exited]: {},
    [T.Error]: { msg: 'proto', min: 1, max: 1 },
    [T.Wheel]: { steps: -3, x: 4, y: 7 },
    [T.SetTheme]: { name: 'synthwave' },
    [T.NewTerm]: null,
    [T.NewTermDone]: { key: '0123456789abcdef' },
  };
  for (const [type, msg] of Object.entries(samples)) {
    const f = encodeFrame(+type, msg);
    const r = new FrameReader();
    const got = [];
    for (let i = 0; i < f.length; i += 3) got.push(...r.push(f.subarray(i, i + 3)));
    check(`frame round trip type ${type}`, got.length === 1 && got[0].type === +type && same(got[0].msg, msg),
      JSON.stringify(got));
  }
  {
    const f = encodeFrame(T.Size, { cols: 80, rows: 24 });
    const body = utf8('{"cols":80,"rows":24}');
    check('frame header is type u8 | length u32 LE', hex(f.subarray(0, 5)) === '07' + hex([body.length, 0, 0, 0]) && hex(f.subarray(5)) === hex(body));
    const two = concat(encodeFrame(T.List), encodeFrame(T.Watch, { key: 'a' }));
    const got = new FrameReader().push(two);
    check('two frames in one chunk', got.length === 2 && got[0].type === T.List && got[0].msg === null && got[1].msg.key === 'a');
    const big = new Uint8Array(5); big[0] = T.Frame; new DataView(big.buffer).setUint32(1, MAX_PAYLOAD + 1, true);
    check('payload over 1 MiB refused', throws(() => new FrameReader().push(big)));
    // A full-size frame in small chunks: linear, not one copy per chunk.
    const huge = encodeFrame(T.Frame, { pad: 'x'.repeat(MAX_PAYLOAD - 20) });
    const fr = new FrameReader();
    const t0 = Date.now();
    let n = 0;
    for (let i = 0; i < huge.length; i += 8) n += fr.push(huge.subarray(i, i + 8)).length;
    const ms = Date.now() - t0;
    check('1 MiB frame in 8-byte chunks reassembled fast', n === 1 && ms < 500, ms + ' ms');
    const mixed = concat(encodeFrame(T.List), huge, encodeFrame(T.Exited, {}));
    const fr2 = new FrameReader();
    const got2 = [];
    for (let i = 0; i < mixed.length; i += 777) got2.push(...fr2.push(mixed.subarray(i, i + 777)));
    check('frames around a big one', got2.length === 3 && got2[1].msg.pad.length === MAX_PAYLOAD - 20 && got2[2].type === T.Exited);
  }

  // ---- base64url
  check('b64u empty', b64u(new Uint8Array(0)) === '');
  check('b64u url alphabet, no padding', b64u(Uint8Array.of(0xfb, 0xff)) === '-_8');
  check('fromB64u unpadded', hex(fromB64u('-_8')) === 'fbff');
  check('fromB64u padded', hex(fromB64u('-_8=')) === 'fbff');
  check('fromB64u refuses +/', throws(() => fromB64u('+/8')));
  check('fromB64u refuses junk', throws(() => fromB64u('a b')));
  {
    const rnd = globalThis.crypto.getRandomValues(new Uint8Array(257));
    check('b64u round trip', hex(fromB64u(b64u(rnd))) === hex(rnd));
  }

  // ---- SAS code: Go vector of pair_test.go
  {
    const code = await sasCode(range(0x00, 16), range(0x20, 32), utf8('pub'));
    check('SASCode vector equals Go (483657)', code === '483657', code);
  }

  // ---- resume key
  {
    const k = await newResumeKey();
    const nonce = range(9, 32);
    const sig = await resumeSig(k.key, nonce);
    const ok = await globalThis.crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, k.publicKey, sig,
      concat(utf8('nmx-resume-v1'), nonce));
    check('resume signature is raw r||s (64 bytes) and verifies', sig.length === 64 && ok, sig.length);
    check('resume public key is P-256 SPKI', k.spki.length === 91 && k.spki[0] === 0x30, k.spki.length);
    check('resume private key not extractable', k.key.extractable === false);
  }

  // ---- frames applied to the screen
  {
    const s = newScreen();
    const d1 = applyFrame(s, { cols: 10, rows: 3, offset: 0, hist: 7, cx: 2, cy: 0, cursor: true, full: true,
      lines: [[0, [['abc', 0xff0000, -1, 1]]], [1, [['x', 0xffffff, 0x000080, 32]]], [2, null]] });
    check('full frame: every row dirty', d1.size === 3);
    check('full frame: lines', screenText(s) === 'abc\nx' && s.lines[0][0].fg === 0xff0000 && s.lines[0][0].attr === 1 && s.lines[1][0].bg === 0x80 && s.lines[2].length === 0,
      JSON.stringify(s.lines));
    const d2 = applyFrame(s, { cols: 10, rows: 3, offset: 0, hist: 7, cx: 3, cy: 2, cursor: true, full: false,
      lines: [[1, [['yz', 0xffffff, -1, 0]]]] });
    check('partial frame: only that line changes', screenText(s) === 'abc\nyz' && s.lines[0][0].text === 'abc',
      JSON.stringify(s.lines));
    check('partial frame: dirty = changed line and cursor rows', same([...d2].sort(), [0, 1, 2]), JSON.stringify([...d2]));
    applyFrame(s, { cols: 10, rows: 3, cx: 0, cy: 0, full: false, lines: [[5, [['out', 0, -1, 0]]], [1, [[1, 2, 3, 4]]], 'junk'] });
    check('malformed lines ignored', screenText(s) === 'abc' && s.lines[1].length === 0, JSON.stringify(s.lines));
    applyFrame(s, { cols: 10, rows: 2, full: true, lines: [[0, [['n', 0, -1, 0]]]] });
    check('full frame after a resize', s.rows === 2 && s.lines.length === 2 && screenText(s) === 'n');
  }

  // ---- scrolling a full-screen program (Claude Code): Wheel, not Scroll
  {
    const s = newScreen();
    applyFrame(s, { cols: 80, rows: 24, full: true, alt: true, mouse: true, lines: [] });
    check('frame alt and mouse kept', s.alt === true && s.mouse === true);
    applyFrame(s, { cols: 80, rows: 24, full: false, lines: [] });
    check('frame without alt and mouse clears them', s.alt === false && s.mouse === false);
    check('wheel: nothing for no step', wheelMessages(0, 1, 2).length === 0);
    check('wheel: one message under the cap', same(wheelMessages(-3, 1, 2), [{ steps: -3, x: 1, y: 2 }]));
    const many = wheelMessages(23, 0, 0);
    check('wheel: split in messages of at most 10 steps', same(many.map(m => m.steps), [10, 10, 3]), JSON.stringify(many));
    const view = { left: 0, top: 0, panX: 0, panY: 0, scale: 0.5, cw: 20, ch: 40, cols: 80, rows: 24 };
    check('wheel: cell under the finger', same(cellUnder(25, 30, view), { x: 2, y: 1 }), JSON.stringify(cellUnder(25, 30, view)));
    check('wheel: cell clamped to the screen', same(cellUnder(-50, 1e6, view), { x: 0, y: 23 }) && same(cellUnder(1e6, -1, view), { x: 79, y: 0 }));
    const zoomed = { ...view, left: 10, top: 100, panX: -40, panY: -20, scale: 1 };
    check('wheel: cell under the finger when zoomed and panned', same(cellUnder(10 + 30, 100 + 30, zoomed), { x: 3, y: 1 }),
      JSON.stringify(cellUnder(40, 130, zoomed)));
  }

  // ---- QR payload (remote.go qrPayload)
  {
    const j = { n: 'arch', a: ['192.168.1.2', '100.64.0.1', 'fd7a:115c:a1e0::1'], p: 8007, h: b64u(range(0x20, 32)), t: b64u(range(0, 16)) };
    const q = parseQR('nmx1:' + b64u(utf8(JSON.stringify(j))));
    check('QR payload parsed', q.name === 'arch' && q.addrs.length === 3 && q.port === 8007 && q.hash.length === 32 && q.token.length === 16);
    check('QR without prefix refused', throws(() => parseQR(b64u(utf8(JSON.stringify(j))))));
    const qr = o => 'nmx1:' + b64u(utf8(JSON.stringify(o)));
    const bad = { ...j, a: ['evil.example/x?', '1.2.3.4@x'] };
    check('QR with only bad addresses refused', throws(() => parseQR(qr(bad))));
    const hostile = ['8.8.8.8', '127.0.0.1', '172.32.0.1', '172.15.255.255', '100.128.0.1', '100.63.0.1', '010.0.0.1',
      '10.0.0.256', '10.0.0', '10.0.0.1.2', ':::::', '::', '2001:db8::1', 'fd7a:115c:a1e0::1%eth0',
      'fd7a:115c:a1e0:0:0:0:0:0:1', 'fd7a:115c:a1e0::1::2', '::ffff:10.0.0.1', 'fd7a:115c:a1e1::1', 'fd7a:115c:a1e0:12345::1',
      'fd7a:115c:a1e0::g', 10, null, { x: 1 }];
    const kept = parseQR(qr({ ...j, a: [...hostile, '10.1.2.3'] })).addrs;
    check('QR keeps only private addresses', same(kept, ['10.1.2.3']), JSON.stringify(kept));
    for (const a of ['10.0.0.1', '172.16.0.1', '172.31.255.255', '192.168.0.1', '100.64.0.1', '100.127.255.255', 'fd7a:115c:a1e0::1', 'FD7A:115C:A1E0:1:2:3:4:5']) {
      check('QR accepts ' + a, same(parseQR(qr({ ...j, a: [a] })).addrs, [a]));
    }
    const many = Array.from({ length: 20 }, (_, i) => '10.0.0.' + (i + 1));
    check('QR keeps at most 8 addresses', parseQR(qr({ ...j, a: many })).addrs.length === 8);
    for (const [what, o] of [['port 0', { p: 0 }], ['port 65536', { p: 65536 }], ['port as string', { p: '8007' }],
      ['31-byte hash', { h: b64u(range(0, 31)) }], ['33-byte hash', { h: b64u(range(0, 33)) }],
      ['15-byte token', { t: b64u(range(0, 15)) }], ['17-byte token', { t: b64u(range(0, 17)) }], ['hash not base64url', { h: '<script>' }]]) {
      check('QR refuses ' + what, throws(() => parseQR(qr({ ...j, ...o }))));
    }
    check('QR refuses a JSON null', throws(() => parseQR('nmx1:' + b64u(utf8('null')))));
    check('QR name capped at 64', Array.from(parseQR(qr({ ...j, n: '🎉'.repeat(100) })).name).length === 64);
    check('QR name not a string', parseQR(qr({ ...j, n: { a: 1 } })).name === '');
    check('machine URL v4', machineURL('192.168.1.2', 8007) === 'https://192.168.1.2:8007/nmx');
    check('machine URL v6', machineURL('fd7a:115c:a1e0::1', 8007) === 'https://[fd7a:115c:a1e0::1]:8007/nmx');
    const hs = parseHashes([{ nb: 100, na: 200, h: b64u(range(0, 32)) }, { nb: 150, na: 300, h: b64u(range(1, 32)) }, { nb: 1, na: 2, h: 'AA' }]);
    check('cert hashes parsed', hs.length === 2);
    check('cert hashes valid now', validHashes(hs, 160).length === 2 && validHashes(hs, 250).length === 1 && validHashes(hs, 300).length === 0);
  }

  // ---- box drawing covers both ranges, inside the cell
  for (const [w, h] of [[8, 17], [21, 44]]) {
    const missing = [];
    for (let cp = 0x2500; cp <= 0x259f; cp++) {
      const ops = [];
      let outside = false;
      const ctx = {
        globalAlpha: 1, fillStyle: '#fff', strokeStyle: '', lineWidth: 1,
        fillRect(x, y, fw, fh) {
          ops.push('fill');
          if (x < 100 || y < 200 || x + fw > 100 + w || y + fh > 200 + h || fw <= 0 || fh <= 0) outside = true;
        },
        stroke() { ops.push('stroke'); },
        save() {}, restore() {}, beginPath() {}, rect() {}, clip() {}, arc() {}, moveTo() {}, lineTo() {},
      };
      const ok = isBoxRune(cp) && drawBoxRune(ctx, cp, 100, 200, w, h);
      if (!ok || !ops.length || outside) missing.push(cp.toString(16));
    }
    check(`boxdraw covers U+2500..U+259F in ${w}x${h}`, missing.length === 0, missing.join(' '));
  }
  check('boxdraw leaves other runes to the font', !isBoxRune(0x25a0) && !isBoxRune(0x24ff) && !drawBoxRune({}, 0x41, 0, 0, 8, 16));

  // ---- faint box runes: opaque, color blended (no darker joins)
  {
    const calls = [];
    const g = {
      globalAlpha: 1, fillStyle: '#000000', font: '', textBaseline: '',
      fillRect(x, y, w, h) { calls.push({ alpha: this.globalAlpha, color: this.fillStyle, x }); },
      fillText() {}, save() {}, restore() {}, beginPath() {}, rect() {}, clip() {}, arc() {}, stroke() {}, moveTo() {}, lineTo() {},
    };
    const t = Object.create(Term.prototype);
    Object.assign(t, { g, cw: 10, ch: 20, base: 15, lineW: 1, px: 13, theme: { fg: 0xffffff, bg: 0x000000, cursor: 0xff0000 } });
    t.screen = newScreen();
    applyFrame(t.screen, { cols: 4, rows: 1, cursor: false, full: true, lines: [[0, [['┼┼', 0xffffff, -1, 16]]]] });
    t.drawLine(0);
    const box = calls.filter(c => c.x >= 0 && c.color !== '#000000');
    const want = '#' + blend(0xffffff, 0x000000, 0.55).toString(16).padStart(6, '0');
    check('faint box runes drawn opaque in the blended color', box.length > 0 && box.every(c => c.alpha === 1 && c.color === want),
      JSON.stringify(box.slice(0, 3)));
    check('blend', blend(0xffffff, 0x000000, 0.55) === 0x8c8c8c && blend(0x123456, 0x123456, 0.3) === 0x123456);
  }

  // ---- a new terminal from the phone: the daemon's refusals in words
  check('new terminal busy', newTermText('new terminal: busy') === 'A terminal is already being opened');
  check('new terminal too many', newTermText('new terminal: too many') === 'Too many new terminals, wait a minute');
  check('new terminal failed', newTermText('new terminal: failed') === 'Could not open a terminal');
  check('other errors are not about a new terminal', newTermText('session not found') === null && newTermText(undefined) === null);

  // ---- keyboard up: the band is shorter than the grid, the cursor row
  // stays in view
  {
    check('fitPan: grid fits, no pan', fitPan(-50, 500, 600, 480, 500) === 0);
    check('fitPan: cursor below the band, shifted up just enough', fitPan(0, 1000, 600, 900, 920) === -320);
    check('fitPan: cursor above the band, shifted down', fitPan(-320, 1000, 600, 100, 120) === -100);
    check('fitPan: cursor in view, pan kept', fitPan(-200, 1000, 600, 400, 420) === -200);
    check('fitPan: never past the grid', fitPan(0, 1000, 600, 1100, 1120) === -400 && fitPan(-900, 1000, 600, 10, 30) === -10);
    const t = Object.create(Term.prototype);
    Object.assign(t, { grid: { width: 800, height: 1000 }, canvas: { width: 800, height: 600 }, zoom: 1, panX: 0, panY: 0, ch: 20,
      screen: { rows: 50, cy: 47, offset: 0, cursor: true } });
    t.clampPan();
    check('keyboard up: the cursor row is the band\'s last', t.panY === 600 - 48 * 20, t.panY);
    t.canvas.height = 1200;
    t.clampPan();
    check('keyboard down: the grid back at the top', t.panY === 0, t.panY);
    t.canvas.height = 600;
    t.screen = { rows: 50, cy: 2, offset: 0, cursor: true };
    t.panY = 0;
    t.clampPan();
    check('keyboard up, cursor near the top: no shift', t.panY === 0, t.panY);
  }

  // ---- the interface follows the theme (Theme message -> CSS variables)
  {
    const ansi = Array.from({ length: 16 }, (_, i) => [i * 10, 100 + i, 200 - i]);
    const v = chromeVars({ fg: [216, 220, 255], bg: [11, 10, 26], cursor: [255, 46, 136], ansi });
    const hexOK = v && Object.values(v).every(x => /^#[0-9a-f]{6}$/.test(x));
    check('chrome: every variable a #rrggbb color', hexOK, JSON.stringify(v));
    check('chrome: same variables as the defaults', v && same(Object.keys(v).sort(), Object.keys(CHROME_DEFAULTS).sort()), JSON.stringify(v && Object.keys(v)));
    check('chrome: background, text, accent from the theme', v && v['--bg'] === '#0b0a1a' && v['--fg'] === '#d8dcff' && v['--pink'] === '#ff2e88');
    check('chrome: second accent and states from the base colors', v && v['--cyan'] === '#3c6ac2' && v['--ko'] === '#0a65c7' && v['--ok'] === '#1466c6' && v['--warn'] === '#1e67c5',
      JSON.stringify(v));
    check('chrome: panel, border and dim text between background and text',
      v && v['--panel'] !== v['--bg'] && v['--line'] !== v['--panel'] && v['--dim'] !== v['--fg'] && v['--dim'] !== v['--bg']);
    const base = { fg: [216, 220, 255], bg: [11, 10, 26] };
    for (const cur of [[255, 255, 255], [0, 0, 0], [119, 119, 119], [255, 46, 136], [54, 249, 246], [0, 229, 255]]) {
      const w = chromeVars({ ...base, cursor: cur });
      check(`chrome: text on the accent ${cur} readable`, w && contrast(w['--on-pink'], w['--pink']) >= 4.5, w && w['--on-pink']);
    }
    const noAnsi = chromeVars({ ...base, cursor: [1, 2, 3] });
    check('chrome: without base colors, the default accents', noAnsi && noAnsi['--cyan'] === CHROME_DEFAULTS['--cyan'] && noAnsi['--ko'] === CHROME_DEFAULTS['--ko']);
    for (const [what, t] of [['no theme', null], ['a string', 'red'], ['fg not a triple', { ...base, cursor: [1, 2, 3], fg: [1, 2] }],
      ['a value over 255', { ...base, cursor: [256, 0, 0] }], ['a negative value', { ...base, cursor: [-1, 0, 0] }],
      ['a float', { ...base, cursor: [1.5, 0, 0] }], ['CSS in a value', { ...base, cursor: ['red;x', 0, 0] }],
      ['bg missing', { fg: [1, 2, 3], cursor: [1, 2, 3] }]]) {
      check('chrome refuses ' + what, chromeVars(t) === null);
    }
    const badAnsi = chromeVars({ ...base, cursor: [1, 2, 3], ansi: [['x', 1, 2]] });
    check('chrome: malformed base colors fall back to the defaults', badAnsi && badAnsi['--cyan'] === CHROME_DEFAULTS['--cyan']);
  }

  // ---- the phone's theme: one choice for the app, themes from /themes.json
  {
    const pal = (fg, bg, cursor) => ({ fg, bg, cursor, ansi: Array.from({ length: 16 }, (_, i) => [i, i, i]) });
    const raw = [
      { name: 'acid', label: 'Acid', ...pal([1, 2, 3], [4, 5, 6], [7, 8, 9]) },
      { name: 'synthwave', label: "Synthwave '84", ...pal([244, 233, 255], [26, 16, 41], [54, 249, 246]) },
      { name: 'nolabel', ...pal([1, 1, 1], [2, 2, 2], [3, 3, 3]) },
      { name: 'badcolor', label: 'Bad', ...pal([1, 2, 300], [0, 0, 0], [0, 0, 0]) },
      { name: 'nocolor', label: 'None' },
      { name: 3, ...pal([1, 1, 1], [2, 2, 2], [3, 3, 3]) }, null, 'x', { name: '', label: 'empty', ...pal([1, 1, 1], [2, 2, 2], [3, 3, 3]) },
    ];
    const list = cleanThemes(raw);
    check('themes.json cleaned: names, labels, valid colors only', same(list.map(t => [t.name, t.label]), [['acid', 'Acid'], ['synthwave', "Synthwave '84"], ['nolabel', 'nolabel']]),
      JSON.stringify(list.map(t => t.name)));
    check('themes keep their palette', same(list[1].bg, [26, 16, 41]) && list[1].ansi.length === 16);
    check('themes: not a list gives none', same(cleanThemes(undefined), []) && same(cleanThemes({}), []));
    check('themes: at most 64', cleanThemes(Array.from({ length: 100 }, (_, i) => ({ name: 't' + i, ...pal([1, 1, 1], [2, 2, 2], [3, 3, 3]) }))).length === 64);
    check('theme choice kept when it exists', themeChoice('synthwave', list) === 'synthwave');
    check('theme choice: unknown follows the desktop', themeChoice('gone', list) === '');
    check('theme choice: none follows the desktop', themeChoice(undefined, list) === '' && themeChoice('', list) === '');

    const machines = [{ id: 'a', used: 5, theme: 'acid' }, { id: 'b', used: 9, theme: 'synthwave' }, { id: 'c', theme: 'nolabel' }];
    check('migration: the most recently used machine\'s choice', migrateTheme(undefined, machines) === 'synthwave');
    check('migration: a global choice wins, "" included', migrateTheme('acid', machines) === 'acid' && migrateTheme('', machines) === '');
    check('migration: nothing to take follows the desktop', migrateTheme(undefined, []) === '' && migrateTheme(undefined, [{ id: 'a', used: 1 }]) === '' &&
      migrateTheme(undefined, [{ id: 'a', used: 1, theme: 42 }]) === '');

    const seen = pal([10, 10, 10], [20, 20, 20], [30, 30, 30]);
    check('chrome of a chosen theme, without a connection', chromeFor('synthwave', list, seen)['--bg'] === '#1a1029');
    check('chrome following the desktop: the last colors seen', chromeFor('', list, seen)['--bg'] === '#141414');
    check('chrome: unknown choice follows the desktop', chromeFor('gone', list, seen)['--bg'] === '#141414');
    check('chrome: nothing seen, nothing chosen: none (defaults)', chromeFor('', list, null) === null);
  }

  // ---- a machine paired again
  {
    const list = [{ id: 'a', name: 'arch', addrs: ['10.0.0.1', '100.64.0.1'] }, { id: 'b', name: 'mac', addrs: ['192.168.1.5'] }];
    check('re-pair: same name, overlapping addresses replaces', same(placeMachine(list, 'arch', ['100.64.0.1']), { id: 'a', name: 'arch' }));
    check('re-pair: same name, other addresses keeps both', same(placeMachine(list, 'arch', ['192.168.9.9']), { id: null, name: 'arch (2)' }));
    check('re-pair: suffix skips taken names', same(placeMachine([...list, { id: 'c', name: 'arch (2)', addrs: [] }], 'arch', ['10.9.9.9']), { id: null, name: 'arch (3)' }));
    check('re-pair: new name is new', same(placeMachine(list, 'nas', ['10.0.0.1']), { id: null, name: 'nas' }));
  }

  // ---- pairing survives a slow passkey creation
  {
    const t0 = 1e12;
    const open1 = { closed: false };
    check('pairing connection usable after 61 s', pairConnUsable(open1, t0, t0 + 61000));
    check('pairing connection unusable near the token expiry', !pairConnUsable(open1, t0, t0 + PAIR_TTL - 5000));
    check('pairing connection unusable once closed', !pairConnUsable({ closed: true }, t0, t0 + 1000) && !pairConnUsable(null, t0, t0));
    check('PAIR_TTL is 5 minutes (pair.go pairingTTL)', PAIR_TTL === 5 * 60 * 1000);
    // The daemon cuts a connection that has not authenticated after 60 s:
    // while the name is typed, the app opens a new one, until the token
    // is about to expire, and never once the passkey creation started.
    const cut = { closed: true };
    check('pairing connection reopened when cut while the name is typed', pairReopen({ state: 'ready', conn: cut }, cut, t0, t0 + 61000));
    check('pairing connection not reopened near the token expiry', !pairReopen({ state: 'ready', conn: cut }, cut, t0, t0 + PAIR_TTL - 5000));
    check('pairing connection not reopened during the passkey creation', !pairReopen({ state: 'creating', conn: cut }, cut, t0, t0 + 61000));
    check('pairing connection not reopened once replaced or cancelled',
      !pairReopen({ state: 'ready', conn: { closed: false } }, cut, t0, t0 + 61000) && !pairReopen({ state: 'ready', conn: cut, cancelled: true }, cut, t0, t0 + 61000));

    const never = new Promise(() => {});
    const fakeConn = writeOK => {
      let endSession;
      const wt = { closed: new Promise(r => { endSession = r; }), close() {} };
      const stream = {
        readable: { getReader: () => ({ read: () => never }) },
        writable: { getWriter: () => ({ write: () => (writeOK ? Promise.resolve() : Promise.reject(new Error('stop sending'))) }) },
      };
      return { conn: new Conn(wt, stream, '10.0.0.1'), endSession };
    };
    let { conn } = fakeConn(false);
    const okSend = await conn.send(T.Pair, { token: 'x' });
    check('send reports a failed write and closes', okSend === false && conn.closed === true);
    ({ conn } = fakeConn(true));
    check('send reports a written frame', (await conn.send(T.List)) === true);
    let ended;
    ({ conn, endSession: ended } = fakeConn(true));
    const t1 = Date.now();
    const waiting = conn.recv(PAIR_TTL).then(() => 'message', () => 'rejected');
    const doneSeen = conn.done ? conn.done.then(() => true) : Promise.resolve(false);
    ended();
    const [how, done] = await Promise.all([waiting, doneSeen]);
    check('session end rejects a waiting recv at once and resolves done', how === 'rejected' && done && Date.now() - t1 < 1000, how);
  }

  // ---- closing ends the wire stream at once, then the session: the daemon
  // frees the phone's slot without waiting for Safari's late session close
  {
    const calls = [];
    const never = new Promise(() => {});
    const wt = { closed: never, close() { calls.push('wt.close'); } };
    const stream = {
      readable: { getReader: () => ({ read: () => never, cancel: async () => { calls.push('reader.cancel'); } }) },
      writable: { getWriter: () => ({ write: async () => {}, close: async () => { calls.push('writer.close'); } }) },
    };
    const c = new Conn(wt, stream, '10.0.0.1');
    c.close();
    c.close();
    check('close: stream ended (FIN, read cancelled) before the session, once',
      same(calls, ['writer.close', 'reader.cancel', 'wt.close']), JSON.stringify(calls));
  }

  // ---- a Face ID signature survives its connection
  {
    const never = new Promise(() => {});
    // A session iOS killed under the Face ID sheet: writes never settle.
    const deadWT = { closed: never, close() {} };
    const deadStream = {
      readable: { getReader: () => ({ read: () => never }) },
      writable: { getWriter: () => ({ write: () => never }) },
    };
    const dead = new Conn(deadWT, deadStream, '10.0.0.1');
    dead.sendTimeout = 150;
    const t0 = Date.now();
    const ok = await Promise.race([dead.send(T.Pair, { token: 'x' }), new Promise(r => setTimeout(() => r('hung'), 2000))]);
    check('send on a dead session gives false within the timeout', ok === false && Date.now() - t0 < 1000 && dead.closed, String(ok));
    check('send timeout is 3 s by default', new Conn(deadWT, deadStream, 'x').sendTimeout === 3000);

    // Fake connection recording what it sends.
    const fake = (alive, inbox = []) => ({
      closed: !alive, sent: [], inbox,
      async send(type, msg) { if (this.closed) return false; this.sent.push([type, msg]); return true; },
      async recv() { if (!this.inbox.length) throw new Error('closed'); return this.inbox.shift(); },
      close() { this.closed = true; },
    });
    let creates = 0;
    const reg = { credId: 'c', pubKey: 'p' };
    const first = fake(false);
    const second = fake(true, [{ type: T.Challenge, msg: { proto: 1, nonce: 'AA', name: 'arch' } }]);
    let reconnects = 0;
    const out = await signThenSend(
      async () => { creates++; return reg; },
      r => ({ token: 't', reg: r, session_pub: 's' }),
      T.Pair, () => first, async () => { reconnects++; return second; },
    ).catch(e => ({ conn: null, error: String(e) }));
    check('pair: dead connection, the old registration goes on a new one, no second passkey',
      creates === 1 && reconnects === 1 && out.conn === second && second.sent.length === 2 &&
      second.sent[0][0] === T.Hello && second.sent[1][0] === T.Pair && second.sent[1][1].reg === reg,
      JSON.stringify({ creates, reconnects, error: out.error, sent: second.sent.map(x => x[0]) }));

    const live = fake(true);
    creates = 0; reconnects = 0;
    const out2 = await signThenSend(async () => { creates++; return reg; }, r => ({ reg: r }), T.Auth, () => live,
      async () => { reconnects++; return fake(true); });
    check('auth: live connection, no reconnect', out2.conn === live && reconnects === 0 && live.sent[0][0] === T.Auth);

    let err = null;
    await signThenSend(async () => reg, r => ({ reg: r }), T.Auth, () => fake(false), async () => fake(false)).catch(e => { err = e; });
    check('both connections dead: one reconnect, then an error', err !== null);
  }

  // ---- addresses tried one at a time, never two transports alive
  {
    const saved = globalThis.WebTransport;
    let alive = 0, maxAlive = 0;
    const urls = [];
    const plan = {}; // host -> {ms, ok}
    const never = new Promise(() => {});
    class FakeWT {
      constructor(url) {
        urls.push(url);
        alive++;
        maxAlive = Math.max(maxAlive, alive);
        this.open = true;
        let close;
        this.closed = new Promise(r => { close = r; });
        this.close = () => { if (this.open) { this.open = false; alive--; close(); } };
        const p = plan[new URL(url).hostname] || { ms: -1 };
        this.ready = p.ms < 0 ? never : new Promise((res, rej) => setTimeout(() => (p.ok ? res() : rej(new Error('refused'))), p.ms));
        const stream = {
          readable: { getReader: () => ({ read: () => never }) },
          writable: { getWriter: () => ({ write: async () => {} }) },
        };
        this.incomingBidirectionalStreams = { getReader: () => ({ read: async () => ({ value: stream, done: false }), releaseLock() {} }) };
      }
    }
    globalThis.WebTransport = FakeWT;
    try {
      const h = [range(0, 32)];
      // The slow preferred address still wins; the fast one never starts.
      plan['10.0.0.1'] = { ms: 400, ok: true };
      plan['10.0.0.2'] = { ms: 5, ok: true };
      let c = await open(['10.0.0.2', '10.0.0.1'], 8007, h, { preferred: '10.0.0.1', timeout: 1000 });
      check('open: preferred address first, one transport at a time', c.addr === '10.0.0.1' && maxAlive === 1 && urls.length === 1,
        JSON.stringify({ addr: c.addr, maxAlive, urls }));
      c.close();
      // A dead address times out and is closed before the next one starts.
      urls.length = 0; maxAlive = alive = 0;
      plan['10.0.0.3'] = { ms: -1 };
      plan['10.0.0.4'] = { ms: 400, ok: false };
      plan['10.0.0.5'] = { ms: 5, ok: true };
      c = await open(['10.0.0.3', '10.0.0.4', '10.0.0.5'], 8007, h, { timeout: 100 });
      check('open: failed tries closed before the next', c.addr === '10.0.0.5' && maxAlive === 1 && urls.length === 3,
        JSON.stringify({ addr: c.addr, maxAlive, urls }));
      c.close();
      urls.length = 0; maxAlive = alive = 0;
      let err = null;
      await open(['10.0.0.3', '10.0.0.4'], 8007, h, { timeout: 50 }).catch(e => { err = e; });
      check('open: unreachable when every address fails', err && err.message === 'unreachable' && alive === 0 && maxAlive === 1);
    } finally {
      globalThis.WebTransport = saved;
    }
  }

  return { pass, fail };
}
