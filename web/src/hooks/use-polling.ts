import { useEffect, useRef } from 'react'

// usePolling runs `fn` immediately, then re-runs it every `intervalMs` —
// but ONLY while the document is visible. When the tab / PWA window is
// backgrounded the interval is cleared, and on regaining visibility we
// fire once immediately to refresh and then resume the cadence.
//
// Why gate on visibility: the gateway is served over plain http://, so the
// browser talks HTTP/1.1 and caps us at ~6 connections per origin. Several
// always-on SSE streams already hold a few of those slots permanently, so
// the handful of sidebar badge-polls running every 5s are competing for a
// scarce resource. A backgrounded window has no reason to keep polling —
// pausing it frees connection slots for the foreground tab and stops a
// hidden PWA window from contributing to pool exhaustion. See
// api/client.ts for the timeout half of this fix.
//
// `fn` is held in a ref so callers don't need to memoize it; only
// `intervalMs` / `enabled` changes restart the timer.
export function usePolling(
  fn: () => void | Promise<void>,
  intervalMs: number,
  enabled = true,
): void {
  const fnRef = useRef(fn)
  fnRef.current = fn

  useEffect(() => {
    if (!enabled) return

    let timer: ReturnType<typeof setInterval> | undefined
    const run = () => {
      void fnRef.current()
    }
    const start = () => {
      if (timer == null) timer = setInterval(run, intervalMs)
    }
    const stop = () => {
      if (timer != null) {
        clearInterval(timer)
        timer = undefined
      }
    }
    const onVisibility = () => {
      if (document.visibilityState === 'visible') {
        run()
        start()
      } else {
        stop()
      }
    }

    if (document.visibilityState === 'visible') {
      run()
      start()
    }
    document.addEventListener('visibilitychange', onVisibility)

    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [intervalMs, enabled])
}
