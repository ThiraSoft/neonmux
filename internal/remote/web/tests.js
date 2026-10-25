// Checks of the pure modules, shared by selftest.html (browser) and
// selftest.mjs (node: `node internal/remote/web/selftest.mjs`).

import {
  T, PROTO, MAX_PAYLOAD, encodeFrame, FrameReader, b64u, fromB64u, utf8, concat,
  newScreen, applyFrame, screenText, parseQR, machineURL, parseHashes, validHashes,
} from './wire.js';
import { sasCode, newResumeKey, resumeSig } from './auth.js';
import { drawBoxRune, isBoxRune } from './boxdraw.js';

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
    [T.Theme]: { fg: [1, 2, 3], bg: [4, 5, 6], cursor: [7, 8, 9] },
    [T.Frame]: { cols: 4, rows: 2, offset: 0, hist: 0, cx: 0, cy: 0, cursor: true, full: true, lines: [[0, [['ab', 0xffffff, -1, 1]]], [1, null]] },
    [T.Exited]: {},
    [T.Error]: { msg: 'proto', min: 1, max: 1 },
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

  // ---- QR payload (remote.go qrPayload)
  {
    const j = { n: 'arch', a: ['192.168.1.2', '100.64.0.1', 'fd7a:115c:a1e0::1'], p: 8007, h: b64u(range(0x20, 32)), t: b64u(range(0, 16)) };
    const q = parseQR('nmx1:' + b64u(utf8(JSON.stringify(j))));
    check('QR payload parsed', q.name === 'arch' && q.addrs.length === 3 && q.port === 8007 && q.hash.length === 32 && q.token.length === 16);
    check('QR without prefix refused', throws(() => parseQR(b64u(utf8(JSON.stringify(j))))));
    const bad = { ...j, a: ['evil.example/x?', '1.2.3.4@x'] };
    check('QR with only bad addresses refused', throws(() => parseQR('nmx1:' + b64u(utf8(JSON.stringify(bad))))));
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

  return { pass, fail };
}
