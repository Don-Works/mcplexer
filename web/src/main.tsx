import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { ErrorBoundary } from '@/components/ErrorBoundary'

const rootEl = document.getElementById('root')
if (!rootEl) throw new Error('Missing #root element')

createRoot(rootEl).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)

// Register the service worker so Chrome treats the page as installable.
// Skipped in dev (Vite serves /sw.js via the public/ proxy but HMR + the
// "skipWaiting + claim clients" dance create reload races during edits).
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    const hadController = Boolean(navigator.serviceWorker.controller)
    let refreshing = false
    navigator.serviceWorker.addEventListener('controllerchange', () => {
      if (!hadController || refreshing) return
      refreshing = true
      window.location.reload()
    })
    navigator.serviceWorker.register('/sw.js')
      .then((registration) => {
        void registration.update()
        window.setInterval(() => {
          void registration.update()
        }, 15 * 60_000)
      })
      .catch((err) => {
        console.warn('[mcplexer] service worker registration failed:', err)
      })
  })
}

// hardRefresh unregisters every service worker, drops all caches, then
// reloads. Shared by the manual chord below and the automatic stale-chunk
// recovery, because both mean the same thing: the bundle this tab is
// running no longer matches the one the daemon serves.
async function hardRefresh() {
  try {
    if ('serviceWorker' in navigator) {
      const regs = await navigator.serviceWorker.getRegistrations()
      await Promise.all(regs.map((r) => r.unregister()))
    }
    if ('caches' in window) {
      const names = await caches.keys()
      await Promise.all(names.map((n) => caches.delete(n)))
    }
  } finally {
    window.location.reload()
  }
}

// Recover automatically from a stale lazy chunk after a daemon upgrade.
//
// Every page is React.lazy'd and Vite content-hashes each chunk, so an
// upgrade renames all of them. A tab opened before the upgrade still holds
// the old module graph, so the first navigation to a not-yet-loaded page
// requests e.g. /assets/AuditPage-CIxax44a.js, which 404s in the new build.
// The user sees "Failed to fetch dynamically imported module" and the route
// simply never renders. index.html is already no-store, so a reload always
// fixes it — the tab just had no way to know it should.
//
// Vite fires vite:preloadError for exactly this. Calling preventDefault
// stops the unhandled rejection, and the reload picks up the current
// index.html and its new chunk names.
//
// RELOAD-LOOP GUARD, and it is the important part: if the chunk is missing
// for any reason a reload cannot fix (a genuinely broken deploy, an asset
// the build never emitted), reloading on every failure would spin the tab
// forever and be far worse than the original error. So we allow ONE
// automatic recovery per minute per tab; a second failure inside that
// window is left to surface as the real error it is.
const RELOAD_GUARD_KEY = 'mcplexer:chunk-reload-at'
const RELOAD_GUARD_MS = 60_000

function recoverFromStaleChunk(detail: unknown) {
  let last = 0
  try {
    last = Number(sessionStorage.getItem(RELOAD_GUARD_KEY) ?? 0)
  } catch {
    // sessionStorage can throw in hardened/private modes. Treat it as "no
    // record" — one reload attempt is still better than a dead route.
  }
  if (Date.now() - last < RELOAD_GUARD_MS) {
    console.error(
      '[mcplexer] stale chunk persisted after a reload — not retrying. ' +
        'The asset is missing for a reason a refresh cannot fix.',
      detail,
    )
    return
  }
  try {
    sessionStorage.setItem(RELOAD_GUARD_KEY, String(Date.now()))
  } catch {
    // Non-fatal: without the marker we lose loop protection, not correctness.
  }
  console.warn('[mcplexer] asset missing (daemon upgraded?) — reloading', detail)
  void hardRefresh()
}

window.addEventListener('vite:preloadError', (event) => {
  event.preventDefault()
  recoverFromStaleChunk((event as unknown as { payload?: unknown }).payload)
})

// Belt and braces: a dynamic import that fails outside Vite's preload
// helper surfaces as an unhandled rejection instead, so match on the
// message browsers use for it.
window.addEventListener('unhandledrejection', (event) => {
  const msg = String((event.reason as Error | undefined)?.message ?? event.reason ?? '')
  if (/Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed/i.test(msg)) {
    event.preventDefault()
    recoverFromStaleChunk(msg)
  }
})

// Cmd/Ctrl+Shift+R hard-refresh inside the PWA. In a regular browser tab
// the chord already triggers a hard reload, but a standalone PWA window
// doesn't always honour it — Chrome intercepts the keyboard shortcut for
// the host app only when the page itself doesn't claim it. We claim it
// explicitly. Kept as the manual escape hatch now that the common case
// recovers on its own.
window.addEventListener('keydown', (e) => {
  if (e.shiftKey && (e.metaKey || e.ctrlKey) && (e.key === 'R' || e.key === 'r')) {
    e.preventDefault()
    void hardRefresh()
  }
})
