import { useCallback, useEffect, useRef } from 'react'
import { useApi } from '@/hooks/use-api'
import { listAuditAlerts } from '@/api/client'
import type { AuditAlert } from '@/api/types'

interface UseAuditAlertsOptions {
  workspace_id?: string
  window_sec?: number
  // Poll interval in ms. Default 30s. Pass 0 to disable polling (fetch once).
  pollMs?: number
  // When false, skip fetching entirely (e.g. the install lacks the alerts
  // capability). Returns empty + not-loading so callers render nothing.
  enabled?: boolean
}

interface UseAuditAlerts {
  alerts: AuditAlert[]
  generatedAt: string | null
  loading: boolean
  error: string | null
  refetch: () => void
}

const DEFAULT_POLL_MS = 30_000

// useAuditAlerts polls the gateway's alert engine on an interval. Alerts are
// derived server-side (anomaly + security heuristics), so the client just
// re-pulls periodically rather than computing anything. When disabled it stays
// inert — no fetch, no spinner — so a no-alerts install costs nothing.
export function useAuditAlerts(
  opts: UseAuditAlertsOptions = {},
): UseAuditAlerts {
  const { workspace_id, window_sec, pollMs = DEFAULT_POLL_MS, enabled = true } =
    opts

  const fetcher = useCallback(
    () => listAuditAlerts({ workspace_id, window_sec }),
    [workspace_id, window_sec],
  )
  const { data, loading, error, refetch } = useApi(fetcher)

  // Keep refetch identity-stable for the interval effect without re-arming the
  // timer on every render.
  const refetchRef = useRef(refetch)
  refetchRef.current = refetch

  useEffect(() => {
    if (!enabled || pollMs <= 0) return
    const handle = setInterval(() => refetchRef.current(), pollMs)
    return () => clearInterval(handle)
  }, [enabled, pollMs])

  return {
    alerts: enabled ? data?.alerts ?? [] : [],
    generatedAt: data?.generated_at ?? null,
    loading: enabled ? loading : false,
    error: enabled ? error : null,
    refetch,
  }
}
