// Pairing helpers: where a newly paired machine goes in the list, and
// whether the pairing connection can still carry Pair. No DOM: node tests it.

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
