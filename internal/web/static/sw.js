// Service worker: membuat aplikasi tetap terbuka tanpa internet (cangkang aplikasi saja).
// Aturan keras: API (/v1/*) TIDAK PERNAH disimpan di cache. Data keuangan hanya ada di IndexedDB terenkripsi.
const CACHE = 'bukukas-shell-v1';
const SHELL = [
  '/', '/manifest.webmanifest', '/assets/style.css', '/assets/vue.global.prod.js', '/assets/app.js',
  '/assets/local-crypto.js', '/assets/local-db.js', '/assets/local-ledger.js', '/assets/local-backup.js',
  '/assets/icon.svg', '/assets/icon-192.png', '/assets/icon-512.png',
];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin || url.pathname.startsWith('/v1/')) return; // jaringan saja
  // Jaringan dulu (selalu versi terbaru bila online), cadangan dari cache bila offline.
  e.respondWith(
    fetch(req).then((res) => {
      if (res.ok) { const copy = res.clone(); caches.open(CACHE).then((c) => c.put(req, copy)); }
      return res;
    }).catch(() => caches.match(req, { ignoreSearch: true }).then((hit) => hit || caches.match('/'))),
  );
});
