import { useCallback, useEffect, useRef, useState } from 'react'
import { streamAssistComplete } from '@/api/brainBrowser'

// useGhostText drives the Copilot-style inline ghost-text suggestion engine
// (DESIGN §4.2). It owns: the ~250ms debounce after a typing pause, the
// sub-100ms latency gate (show nothing rather than a stale ghost), the
// streaming request (cancelled when the user keeps typing), and the
// graded-accept word boundaries. Presentation lives in the GhostText
// components; this hook is pure state + side-effects so the boundary logic
// stays unit-testable.
//
// Trigger conditions (DESIGN §4.2): a model profile exists, the caret is at
// the END of the field value (end-of-line/end-of-token), and the user paused.
// We only suggest at end-of-value to keep the overlay geometry honest — a
// mid-string ghost would need full caret-coordinate measurement we
// deliberately avoid.

const DEBOUNCE_MS = 250
// Latency gate: if the first token hasn't arrived within this window we keep
// showing nothing rather than a ghost-from-the-past (DESIGN §3.4).
const LATENCY_GATE_MS = 1500
const MIN_CONTEXT = 8

export interface GhostState {
  // ghost is the suggestion text to render after the caret ("" = none).
  ghost: string
  // inFlight drives the ModelPresenceLabel shimmer.
  inFlight: boolean
  // profile is the resolving model profile (null until a stream completes, or
  // when no profile is configured — the silent-degrade signal).
  profile: string | null
  // degraded is true once the server returned 204 (no model). The caller can
  // stop asking; ghost text simply stays absent.
  degraded: boolean
}

export interface UseGhostTextArgs {
  field: string
  workspace?: string
  // value + caret describe the live field. caret is the selectionEnd; the
  // hook only suggests when caret === value.length (end of value).
  value: string
  caret: number
}

export function useGhostText({ field, workspace, value, caret }: UseGhostTextArgs) {
  const [state, setState] = useState<GhostState>({
    ghost: '',
    inFlight: false,
    profile: null,
    degraded: false,
  })
  const abortRef = useRef<AbortController | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const gateRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // The context the in-flight request was issued for; if the live value has
  // diverged when tokens arrive, we drop them (stale-ghost guard).
  const requestedFor = useRef('')

  const cancel = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    if (gateRef.current) clearTimeout(gateRef.current)
    gateRef.current = null
  }, [])

  // clear hides any current ghost without cancelling a profile (used on
  // accept/dismiss). Distinct from cancel which tears down an in-flight req.
  const clear = useCallback(() => {
    cancel()
    setState((s) => ({ ...s, ghost: '', inFlight: false }))
  }, [cancel])

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    // Only suggest at end-of-value, with enough context, and not after the
    // server told us there's no model.
    const atEnd = caret >= value.length
    if (state.degraded || !atEnd || value.trim().length < MIN_CONTEXT) {
      // Hide a stale ghost when the trigger condition lapses.
      if (state.ghost) setState((s) => ({ ...s, ghost: '' }))
      return
    }
    debounceRef.current = setTimeout(() => {
      cancel()
      const ac = new AbortController()
      abortRef.current = ac
      requestedFor.current = value
      let acc = ''
      // Latency gate (DESIGN §3.4): if the FIRST token hasn't landed within
      // the window, abort the request and show nothing rather than letting a
      // stale ghost-from-the-past arrive late and flash in after the user has
      // moved on. Once a token lands the gate is satisfied and tokens stream
      // into view as they arrive.
      let gotToken = false
      gateRef.current = setTimeout(() => {
        if (!gotToken) ac.abort()
      }, LATENCY_GATE_MS)

      setState((s) => ({ ...s, inFlight: true }))
      streamAssistComplete(
        { context: value, field, workspace },
        (chunk) => {
          gotToken = true
          if (gateRef.current) {
            clearTimeout(gateRef.current)
            gateRef.current = null
          }
          acc += chunk
          // Stream into view as tokens arrive (after the first one is fast).
          if (requestedFor.current === value) {
            setState((s) => ({ ...s, ghost: acc }))
          }
        },
        ac.signal,
      )
        .then(({ profile, degraded }) => {
          if (gateRef.current) clearTimeout(gateRef.current)
          if (requestedFor.current !== value) return // stale
          setState((s) => ({
            ...s,
            inFlight: false,
            profile: profile ?? s.profile,
            degraded: degraded || s.degraded,
            ghost: acc,
          }))
        })
        .catch((e) => {
          if ((e as Error)?.name === 'AbortError') return
          setState((s) => ({ ...s, inFlight: false, ghost: '' }))
        })
    }, DEBOUNCE_MS)

    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, caret, field, workspace, state.degraded])

  // Tear everything down on unmount.
  useEffect(() => () => cancel(), [cancel])

  return { ...state, clear, cancel }
}

// nextWordBoundary returns the index in ghost up to and including the next
// word (graded accept: the right-arrow accepts one word at a time). It keeps
// a leading space with the word so accepting reproduces the spacing.
export function nextWordBoundary(ghost: string): number {
  if (!ghost) return 0
  let i = 0
  // Skip leading whitespace into the accepted slice.
  while (i < ghost.length && isWs(ghost[i])) i++
  // Consume the word.
  while (i < ghost.length && !isWs(ghost[i])) i++
  return i
}

function isWs(c: string): boolean {
  return c === ' ' || c === '\t' || c === '\n' || c === '\r'
}
