import { useCallback, useEffect, useState } from 'react'
import { getBrainStatus } from '@/api/brain'

interface UseBrainStatusReturn {
  enabled: boolean | null
  loading: boolean
  error: string | null
  refetch: () => void
}

export function useBrainStatus(): UseBrainStatusReturn {
  const [state, setState] = useState<UseBrainStatusReturn>({
    enabled: null,
    loading: true,
    error: null,
    refetch: () => {},
  })
  const [trigger, setTrigger] = useState(0)

  const refetch = useCallback(() => {
    setTrigger((t) => t + 1)
  }, [])

  useEffect(() => {
    let active = true

    if (trigger > 0) {
      setState((prev) => ({ ...prev, loading: true, error: null }))
    }

    const timeoutId = setTimeout(() => {
      if (active) {
        setState((prev) => ({
          ...prev,
          loading: false,
          error: `Request timed out after 15s — try again`,
        }))
        active = false
      }
    }, 15_000)

    getBrainStatus()
      .then((data) => {
        if (active) {
          clearTimeout(timeoutId)
          setState({
            enabled: data.enabled,
            loading: false,
            error: null,
            refetch,
          })
        }
      })
      .catch((err: unknown) => {
        if (active) {
          clearTimeout(timeoutId)
          const message = err instanceof Error ? err.message : 'Unknown error'
          setState((prev) => ({
            ...prev,
            loading: false,
            error: message,
          }))
        }
      })

    return () => {
      active = false
      clearTimeout(timeoutId)
    }
  }, [trigger])

  return { ...state, refetch }
}
