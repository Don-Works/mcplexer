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
    let reloadOnControllerChange = Boolean(navigator.serviceWorker.controller)
    let refreshing = false
    navigator.serviceWorker.addEventListener('controllerchange', () => {
      if (!reloadOnControllerChange || refreshing) return
      refreshing = true
      window.location.reload()
    })
    navigator.serviceWorker.register('/sw.js')
      .then((registration) => {
        void registration.update()
        window.setInterval(() => {
          void registration.update()
        }, 15 * 60_000)
        reloadOnControllerChange = true
      })
      .catch((err) => {
        console.warn('[mcplexer] service worker registration failed:', err)
      })
  })
}

// Cmd/Ctrl+Shift+R hard-refresh inside the PWA. In a regular browser tab
// the chord already triggers a hard reload, but a standalone PWA window
// doesn't always honour it — Chrome intercepts the keyboard shortcut for
// the host app only when the page itself doesn't claim it. We claim it
// explicitly: unregister all SW registrations, nuke caches, then reload.
// This is the "I just upgraded the daemon, give me the new bundle" key.
window.addEventListener('keydown', (e) => {
  if (e.shiftKey && (e.metaKey || e.ctrlKey) && (e.key === 'R' || e.key === 'r')) {
    e.preventDefault()
    void (async () => {
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
    })()
  }
})
