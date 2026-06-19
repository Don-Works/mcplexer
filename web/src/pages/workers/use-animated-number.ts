// useAnimatedNumber — interpolate from the previous value to the
// current value over `duration` ms via requestAnimationFrame, returning
// the live frame value. When the target equals the previous (or this is
// the first render) it returns the target straight away — no animation
// for steady-state.
//
// Used by WorkerRunCard + WorkerVitalsStrip to make token counters and
// cost numbers FEEL like they're ticking up rather than slamming from
// one value to the next.

import { useEffect, useRef, useState } from 'react'

export function useAnimatedNumber(target: number, duration = 400): number {
  const [value, setValue] = useState(target)
  const fromRef = useRef(target)
  const rafRef = useRef<number | null>(null)
  const startedAtRef = useRef<number>(0)

  useEffect(() => {
    if (target === fromRef.current) return
    const from = value
    fromRef.current = target
    startedAtRef.current = performance.now()

    function step(now: number) {
      const elapsed = now - startedAtRef.current
      const t = Math.min(1, elapsed / duration)
      const next = from + (target - from) * easeOutCubic(t)
      setValue(next)
      if (t < 1) {
        rafRef.current = requestAnimationFrame(step)
      } else {
        setValue(target)
      }
    }

    rafRef.current = requestAnimationFrame(step)
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
    }
    // We intentionally omit `value` from deps — it'd cause re-entrant
    // RAF loops. The effect re-runs only when `target` flips.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target, duration])

  return value
}

function easeOutCubic(t: number): number {
  return 1 - Math.pow(1 - t, 3)
}
