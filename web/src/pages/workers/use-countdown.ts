// useCountdown — recomputes nextRunCountdown for a Worker every second
// so the vitals strip + list-page row stay live without re-rendering
// the rest of the page.
//
// When `enabled` is false the hook short-circuits to the PAUSED result
// without ever scheduling an interval — saves a tick of work for paused
// workers and keeps the "no setState in effect body" lint rule happy.

import { useEffect, useState } from 'react'

import { nextRunCountdown, type CountdownResult } from './worker-utils'

const PAUSED: CountdownResult = { humanCountdown: 'Paused', nextRunDate: null }

export function useCountdown(
  scheduleSpec: string,
  lastRunAt: string | undefined,
  enabled = true,
): CountdownResult {
  // Live state ticks via the interval; we read it only when enabled.
  // When enabled flips back on, the next interval tick (<=1s) refreshes.
  const [result, setResult] = useState<CountdownResult>(() =>
    nextRunCountdown(scheduleSpec, lastRunAt),
  )

  useEffect(() => {
    if (!enabled) return
    const id = window.setInterval(() => {
      setResult(nextRunCountdown(scheduleSpec, lastRunAt))
    }, 1_000)
    return () => window.clearInterval(id)
  }, [scheduleSpec, lastRunAt, enabled])

  // Render-time short-circuit so toggling `enabled` is instant.
  if (!enabled) return PAUSED
  return result
}
