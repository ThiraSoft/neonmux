// IndexedDB "nmx": paired machines and resume keys.
//   machines {id, name, addrs[], port, hashes[{nb, na, h}], credId, deviceId, lastAddr, lastKey, used, reach{ok, at}, theme, chrome}
//            theme, chrome: from older versions, read once to migrate to prefs
//   prefs    {k, v}: "theme" the app's theme ("" follows the desktop), "chrome" the last Theme
//            message seen while following it {fg, bg, cursor, ansi}
//   resume   {machineId, resumeId, key: CryptoKey (ECDSA P-256, not extractable), issued, seen}

let dbp = null;

function db() {
  if (!dbp) {
    dbp = new Promise((resolve, reject) => {
      const req = indexedDB.open('nmx', 2);
      req.onupgradeneeded = () => {
        const d = req.result;
        if (!d.objectStoreNames.contains('machines')) d.createObjectStore('machines', { keyPath: 'id' });
        if (!d.objectStoreNames.contains('resume')) d.createObjectStore('resume', { keyPath: 'machineId' });
        if (!d.objectStoreNames.contains('prefs')) d.createObjectStore('prefs', { keyPath: 'k' });
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    dbp.catch(() => { dbp = null; });
  }
  return dbp;
}

async function tx(store, mode, f) {
  const d = await db();
  return new Promise((resolve, reject) => {
    const t = d.transaction(store, mode);
    const req = f(t.objectStore(store));
    t.oncomplete = () => resolve(req ? req.result : undefined);
    t.onerror = () => reject(t.error);
    t.onabort = () => reject(t.error);
  });
}

export const listMachines = () => tx('machines', 'readonly', s => s.getAll());
export const getMachine = id => tx('machines', 'readonly', s => s.get(id));
export const putMachine = m => tx('machines', 'readwrite', s => s.put(m));
export async function deleteMachine(id) {
  await tx('machines', 'readwrite', s => s.delete(id));
  await deleteResume(id);
}

export const getPref = k => tx('prefs', 'readonly', s => s.get(k)).then(r => (r ? r.v : undefined));
export const putPref = (k, v) => tx('prefs', 'readwrite', s => s.put({ k, v }));

export const getResume = id => tx('resume', 'readonly', s => s.get(id));
export const putResume = r => tx('resume', 'readwrite', s => s.put(r));
export const deleteResume = id => tx('resume', 'readwrite', s => s.delete(id));
