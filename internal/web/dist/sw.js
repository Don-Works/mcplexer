// MCPlexer service worker.
//
// Scope: installability + notification routing.
//   • install/activate — take control immediately so the first load is the
//     installed scope, no second-refresh dance.
//   • fetch — network-first for navigations with a tiny cached shell as the
//     offline fallback. Chrome's PWA-install criteria require a fetch
//     handler that returns a valid response for the start_url; without it,
//     the address-bar install icon never appears. Everything else falls
//     through to the network unmodified, so JS/CSS bundles aren't cached
//     (avoids serving stale UI after `make upgrade`).
//   • notificationclick — route to the right SPA route, focus an existing
//     window if one is open, otherwise open a new one.

// Bumped to v5 to evict stale localhost PWA shells that blank the dashboard.
// v3 shipped no-cache for sw.js itself but existing installs had already
// cached a broken shell; v4 forced a clean sweep via the version bump.
// v5 is a second sweep for stragglers whose Chrome kept the old registration.
// activate() prunes any cache name that isn't this one.
const CACHE_NAME = 'mcplexer-shell-v5';
const SHELL_URLS = ['/', '/icon.svg', '/icon-192.png', '/manifest.webmanifest'];

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    try {
      const cache = await caches.open(CACHE_NAME);
      // Cache one navigation-shell entry so the start_url responds offline.
      // Errors here (e.g. an asset 404'd during a partial deploy) must not
      // block install — the SW is still useful for notifications + future
      // navigations once the deploy completes.
      await cache.addAll(SHELL_URLS).catch(() => {});
    } finally {
      await self.skipWaiting();
    }
  })());
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    // Drop any prior caches whose name doesn't match the current shell version.
    const names = await caches.keys();
    await Promise.all(names.filter((n) => n !== CACHE_NAME).map((n) => caches.delete(n)));
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', (event) => {
  const req = event.request;

  // Only handle GETs on same-origin HTTP(S). Everything else (POSTs to /api,
  // SSE event streams, cross-origin font fetches) falls through to the
  // browser's default network path.
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // SSE streams are long-lived and accept: text/event-stream — never cache
  // or intercept them.
  if (url.pathname.startsWith('/api/')) return;

  // Navigations: try network first so users see the latest hashed-asset
  // index.html, fall back to the cached shell only when offline.
  const isNavigation = req.mode === 'navigate'
    || (req.headers.get('accept') || '').includes('text/html');

  if (isNavigation) {
    event.respondWith((async () => {
      try {
        const fresh = await fetch(req);
        // Refresh the shell entry opportunistically so the offline copy
        // stays close to the live one.
        try {
          const cache = await caches.open(CACHE_NAME);
          await cache.put('/', fresh.clone());
        } catch { /* quota or opaque clone failure — fine */ }
        return fresh;
      } catch {
        const cache = await caches.open(CACHE_NAME);
        const cached = await cache.match('/');
        if (cached) return cached;
        return new Response('Offline', { status: 503, statusText: 'Offline' });
      }
    })());
  }
  // Non-navigation GETs (JS, CSS, icons, fonts): pass through unmodified.
  // Don't call respondWith — the browser uses its normal network + HTTP
  // cache path, which respects the Cache-Control headers the Go server sets.
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = event.notification.data && event.notification.data.url
    ? event.notification.data.url
    : '/';

  event.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    const target = new URL(url, self.location.origin).href;

    for (const client of all) {
      if (client.url.startsWith(self.location.origin) && 'focus' in client) {
        await client.focus();
        if ('navigate' in client) {
          try { await client.navigate(target); } catch { /* nav rejected — fine */ }
        }
        return;
      }
    }

    if (self.clients.openWindow) {
      await self.clients.openWindow(target);
    }
  })());
});
