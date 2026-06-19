import { useCallback, useEffect, useState } from 'react'

interface UseApiState<T> {
  data: T | null
  loading: boolean
  error: string | null
}

interface UseApiReturn<T> extends UseApiState<T> {
  refetch: () => void
}

// Hard timeout for a single fetch — caps the "Loading…" spinner so a
// hung request never silently strands the user. 15s is generous; even
// the dashboard endpoint (the heaviest one) returns in ~50ms.
const FETCH_TIMEOUT_MS = 15_000

export function useApi<T>(fetcher: () => Promise<T>): UseApiReturn<T> {
  const [state, setState] = useState<UseApiState<T>>({
    data: null,
    loading: true,
    error: null,
  })
  const [trigger, setTrigger] = useState(0)

  const refetch = useCallback(() => {
    setTrigger((t) => t + 1)
  }, [])

  useEffect(() => {
    let active = true

    // On refetch, mark loading but keep existing data so the user keeps
    // seeing the previous render instead of flashing back to a spinner.
    if (trigger > 0) {
      setState((prev) => ({ ...prev, loading: true, error: null }))
    }

    // Race the fetch against a wall-clock timeout. Without this, a
    // hung request (browser HTTP/1.1 pool exhaustion, daemon stall,
    // dead SSE crowding the slots) leaves loading=true forever and
    // the user sees "Loading…" with no recovery path.
    const timeoutId = setTimeout(() => {
      if (active) {
        setState((prev) => ({
          data: prev.data,
          loading: false,
          error: `Request timed out after ${FETCH_TIMEOUT_MS / 1000}s — try again`,
        }))
        active = false
      }
    }, FETCH_TIMEOUT_MS)

    fetcher()
      .then((data) => {
        if (active) {
          clearTimeout(timeoutId)
          setState({ data, loading: false, error: null })
        }
      })
      .catch((err: unknown) => {
        if (active) {
          clearTimeout(timeoutId)
          const message = err instanceof Error ? err.message : 'Unknown error'
          setState((prev) => ({ data: prev.data, loading: false, error: message }))
        }
      })

    return () => {
      active = false
      clearTimeout(timeoutId)
    }
  }, [fetcher, trigger])

  return { ...state, refetch }
}
