// Machine helpers: where a newly paired machine goes in the list, whether
// the pairing connection can still carry Pair, the app's theme. No DOM:
// node tests it.

import { chromeVars } from './chrome.js';

// placeMachine returns the record id to replace (or null for a new one)
// and the name to store. A machine paired again replaces the record of the
// same name only when their addresses overlap; two different machines that
// share a name are both kept, the new one with a suffix.
export function placeMachine(list, name, addrs) {
  const same = list.find(m => m.name === name && (m.addrs || []).some(a => addrs.includes(a)));
  if (same) return { id: same.id, name };
  const taken = new Set(list.map(m => m.name));
  if (!taken.has(name)) return { id: null, name };
  for (let i = 2; ; i++) {
    const n = `${name} (${i})`;
    if (!taken.has(n)) return { id: null, name: n };
  }
}

// PAIR_TTL is how long the daemon keeps a pairing token (pairingTTL in
// pair.go); a connection that can only pair stays open that long.
export const PAIR_TTL = 5 * 60 * 1000;

// pairConnUsable reports whether the pairing connection may still be used
// to create the passkey and send Pair: open, and the token scanned at
// scannedAt not about to expire (10 s margin). Otherwise the app connects
// again before asking for the passkey.
export function pairConnUsable(conn, scannedAt, now) {
  return !!conn && !conn.closed && now - scannedAt < PAIR_TTL - 10000;
}

// ---- the phone's theme: one choice for the whole app ("" follows the
// desktop of the machine connected), among the themes of /themes.json.


const MAX_THEMES = 64;

// cleanThemes keeps the well-formed entries of /themes.json: a name, a
// label, and colors chromeVars accepts (integers 0..255).
export function cleanThemes(list) {
  if (!Array.isArray(list)) return [];
  const out = [];
  for (const t of list) {
    if (!t || typeof t.name !== 'string' || !t.name || !chromeVars(t)) continue;
    out.push({
      name: t.name.slice(0, 64),
      label: (typeof t.label === 'string' && t.label ? t.label : t.name).slice(0, 64),
      fg: t.fg, bg: t.bg, cursor: t.cursor,
      ansi: Array.isArray(t.ansi) ? t.ansi.slice(0, 16) : [],
    });
    if (out.length === MAX_THEMES) break;
  }
  return out;
}

// themeChoice is the theme to ask for: the choice if it exists, else ""
// (follow the desktop).
export function themeChoice(stored, themes) {
  return typeof stored === 'string' && themes.some(t => t.name === stored) ? stored : '';
}

// migrateTheme is the app's choice: the stored one, else (it used to be
// per machine) the most recently used machine's.
export function migrateTheme(pref, machines) {
  if (typeof pref === 'string') return pref;
  const last = machines.slice().sort((a, b) => (b.used || 0) - (a.used || 0))[0];
  return last && typeof last.theme === 'string' ? last.theme : '';
}

// chromeFor is the interface's colors: the chosen theme's, or when
// following the desktop the last Theme seen; null keeps the defaults.
export function chromeFor(choice, themes, lastSeen) {
  const t = themes.find(x => x.name === choice);
  return chromeVars(t || lastSeen);
}
