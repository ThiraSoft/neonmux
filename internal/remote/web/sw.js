// Service worker: the PWA opens without network, its origin stays the
// machine that served it. The page registers /sw.js?v=<version of
// /version.json> (plus a hash of the app's files for a development build),
// so each neonmux version gets its own cache and a new
// version installs a new worker in the background. The page asks it to
// take over (skipWaiting) only when no terminal is open.

const VERSION = new URL(self.location.href).searchParams.get('v') || 'dev';
const CACHE = 'nmx-' + VERSION;
const FILES = [
  '/', '/app.css', '/manifest.json', '/icon-180.png', '/icon-512.png',
  '/app.js', '/ui.js', '/store.js', '/machines.js', '/wire.js', '/auth.js', '/conn.js', '/deliver.js', '/pair.js', '/term.js', '/boxdraw.js', '/chrome.js', '/prompt.js', '/themes.json',
  '/vendor/jsQR.js',
  '/fonts/IosevkaTerm-Regular.ttf', '/fonts/IosevkaTerm-Bold.ttf',
  '/fonts/IosevkaTerm-Italic.ttf', '/fonts/IosevkaTerm-BoldItalic.ttf',
];

self.addEventListener('install', e => {
  e.waitUntil(caches.open(CACHE).then(c => c.addAll(FILES.map(u => new Request(u, { cache: 'reload' })))));
});

self.addEventListener('activate', e => {
  e.waitUntil(caches.keys()
    .then(keys => Promise.all(keys.filter(k => k.startsWith('nmx-') && k !== CACHE).map(k => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener('message', e => {
  if (e.data === 'skipWaiting') self.skipWaiting();
});

// A push is always shown: iOS ends the subscription of a worker that does not.
// The payload comes from the daemon (push.go), encrypted for this phone.
self.addEventListener('push', e => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch { /* an empty notification still shows */ }
  const title = typeof d.title === 'string' && d.title ? d.title : 'neonmux';
  const body = typeof d.body === 'string' ? d.body : 'An agent is waiting';
  const key = typeof d.key === 'string' ? d.key : '';
  e.waitUntil(self.registration.showNotification(title, {
    body,
    tag: key || 'neonmux',
    icon: '/icon-180.png',
    data: { key, device: typeof d.device === 'string' ? d.device : '' },
  }));
});

// A tap opens that terminal: in the app if it is running, else by starting it.
self.addEventListener('notificationclick', e => {
  e.notification.close();
  const { key = '', device = '' } = e.notification.data || {};
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(async list => {
    if (list.length) {
      const c = list[0];
      await c.focus();
      c.postMessage({ open: { key, device } });
      return;
    }
    await self.clients.openWindow('/?' + new URLSearchParams({ key, device }));
  }));
});

// Cache first for the app's own files; everything else (version.json, the
// self test) goes to the network.
self.addEventListener('fetch', e => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const u = new URL(req.url);
  if (u.origin !== self.location.origin) return;
  const path = u.pathname;
  // ?fresh: the page hashing a development build, past the cache.
  if (!FILES.includes(path) || u.searchParams.has('fresh')) return;
  e.respondWith(caches.open(CACHE).then(async c => {
    const hit = await c.match(path);
    if (hit) return hit;
    const r = await fetch(req);
    if (r.ok) c.put(path, r.clone());
    return r;
  }));
});
