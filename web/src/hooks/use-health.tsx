import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { getHealth, type HealthResponse } from '@/api/client'

interface HealthState {
  data: HealthResponse | null
  loading: boolean
  error: string | null
  refetch: () => void
}

const HealthContext = createContext<HealthState>({
  data: null,
  loading: true,
  error: null,
  refetch: () => {},
})

const POLL_INTERVAL_MS = 30_000

export function HealthProvider({ children }: { children: React.ReactNode }) {
  const [data, setData] = useState<HealthResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [trigger, setTrigger] = useState(0)
  const mountedRef = useRef(true)

  const refetch = useCallback(() => setTrigger((t) => t + 1), [])

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  useEffect(() => {
    const controller = new AbortController()

    async function poll() {
      try {
        const result = await getHealth({ signal: controller.signal })
        if (mountedRef.current) {
          setData(result)
          setLoading(false)
          setError(null)
        }
      } catch (err) {
        if (mountedRef.current && !controller.signal.aborted) {
          setError(err instanceof Error ? err.message : 'Unknown error')
          setLoading(false)
        }
      }
    }

    void poll()
    const id = setInterval(() => void poll(), POLL_INTERVAL_MS)

    return () => {
      controller.abort()
      clearInterval(id)
    }
  }, [trigger])

  return (
    <HealthContext.Provider value={{ data, loading, error, refetch }}>
      {children}
    </HealthContext.Provider>
  )
}

export function useHealth() {
  return useContext(HealthContext)
}
