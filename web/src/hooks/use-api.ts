import { useCallback, useEffect, useState } from 'react'

interface UseApiState<T> {
  data: T | null
  loading: boolean
  error: string | null
}

interface UseApiReturn<T> extends UseApiState<T> {
  refetch: () => void
  setData: (data: T) => void
}

type ApiFetcher<T> = (signal: AbortSignal) => Promise<T>

// Hard timeout for a single fetch — caps the "Loading…" spinner so a
// hung request never silently strands the user. Most reads use 15s; pages
// that intentionally run bounded external probes can opt into longer.
const FETCH_TIMEOUT_MS = 15_000

export function useApi<T>(
  fetcher: ApiFetcher<T>,
  timeoutMs = FETCH_TIMEOUT_MS,
): UseApiReturn<T> {
  const [state, setState] = useState<UseApiState<T>>({
    data: null,
    loading: true,
    error: null,
  })
  const [trigger, setTrigger] = useState(0)

  const refetch = useCallback(() => {
    setTrigger((t) => t + 1)
  }, [])

  const setData = useCallback((data: T) => {
    setState({ data, loading: false, error: null })
  }, [])

  useEffect(() => {
    let active = true
    const controller = new AbortController()

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
        controller.abort()
        setState((prev) => ({
          data: prev.data,
          loading: false,
          error: `Request timed out after ${timeoutMs / 1000}s — try again`,
        }))
        active = false
      }
    }, timeoutMs)

    fetcher(controller.signal)
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
      controller.abort()
      clearTimeout(timeoutId)
    }
  }, [fetcher, timeoutMs, trigger])

  return { ...state, refetch, setData }
}
