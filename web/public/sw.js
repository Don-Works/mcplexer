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

// Version history:
//   v3 — no-cache for sw.js itself (prevents OS-level caching of stale SW).
//   v4 — version bump to force a clean cache sweep.
//   v5 — second sweep for stragglers.
//   v7 — template-based version from the build pipeline.
//   v8 — evict v7 caches that blanked the dashboard on localhost.
//   v9 — mobile PWA start_url shell + push notification display hook.
//   v10 — auto-update messaging and safer notification actions.
//   v11 — canonical HTTPS PWA origin + notification permission gesture fix.
//   v12 — task notification deep links + Mesh task-event noise filter.
// activate() prunes any cache name that isn't this one.
const CACHE_NAME = 'mcplexer-shell-v12';
const SHELL_URLS = ['/', '/app?source=pwa', '/icon.svg', '/icon-192.png', '/manifest.webmanifest'];

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
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const client of all) {
      client.postMessage({ type: 'mcplexer-sw-activated', version: CACHE_NAME });
    }
  })());
});

self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
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

self.addEventListener('push', (event) => {
  event.waitUntil((async () => {
    let payload = {};
    try {
      payload = event.data ? event.data.json() : {};
    } catch {
      payload = {
        title: 'MCPlexer',
        body: event.data ? event.data.text() : 'New notification',
      };
    }

    const title = payload.title || 'MCPlexer';
    const options = {
      body: payload.body || payload.summary || 'New approval or task update',
      tag: payload.tag || payload.id || 'mcplexer',
      icon: payload.icon || '/icon-192.png',
      badge: payload.badge || '/icon-192.png',
      requireInteraction: payload.priority === 'critical',
      data: {
        url: payload.url || payload.path || '/app',
      },
    };

    await self.registration.showNotification(title, options);
  })());
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
