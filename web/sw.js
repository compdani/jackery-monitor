/**
 * Solar Pow Monitor service worker.
 *
 * Goal: make the app installable as a PWA and keep the UI shell loading
 * fast even when the NAS is briefly unreachable. We cache the static
 * shell (HTML + hashed Svelte assets + icon) on install, and use a
 * network-first strategy for everything else (live API calls always go
 * to the bridge).
 */

// Bump this whenever the static-shell semantics change (or whenever a
// stuck cache is suspected). The activate handler drops every cache that
// isn't the current name, so installed clients get fresh files on the
// next navigation.
const CACHE = 'jackery-shell-v25';
const SHELL = [
  '/',
  '/static/icon.svg',
  '/manifest.webmanifest',
];

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)));
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Drop any older caches so an updated shell wins immediately.
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    )
  );
  self.clients.claim();
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);

  // Don't intercept WebSocket upgrades, /api/* (live data), or /ws.
  if (url.pathname.startsWith('/api/') || url.pathname === '/ws') return;

  // Network-first for everything else — falls back to cached shell when
  // offline so the dashboard still loads its frame and shows the last
  // telemetry the WS pushed before the connection dropped.
  // Hashed SvelteKit assets under /_app/ are immutable; cache them on
  // success so repeat visits stay fast.
  event.respondWith(
    fetch(req)
      .then((resp) => {
        if (resp && resp.ok) {
          const isShell = SHELL.includes(url.pathname);
          const isHashedAsset = url.pathname.startsWith('/_app/');
          if (isShell || isHashedAsset) {
            const copy = resp.clone();
            caches.open(CACHE).then((c) => c.put(req, copy));
          }
        }
        return resp;
      })
      .catch(() => caches.match(req).then((c) => c || caches.match('/')))
  );
});
