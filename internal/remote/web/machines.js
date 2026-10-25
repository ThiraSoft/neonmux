// Where a newly paired machine goes in the list. No DOM: node tests it.

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
