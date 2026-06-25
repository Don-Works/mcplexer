import { useCallback } from 'react'
import { useApi } from '@/hooks/use-api'
import { getAuditCapabilities } from '@/api/client'
import type { AuditCapabilities } from '@/api/types'

// While the capabilities request is in flight (or if it fails) we assume the
// always-present rankers (fts + tfidf) and turn OFF the optional ones (vector
// search, alerts, saved searches). This degrades safely: the UI never offers a
// feature the backend can't serve, and it never hides text search — which is
// always available — behind a loading flicker.
const DEFAULT_CAPABILITIES: AuditCapabilities = {
  search: { fts: true, tfidf: true, vector: false },
  alerts: false,
  saved_searches: false,
}

interface UseAuditCapabilities {
  capabilities: AuditCapabilities
  loading: boolean
  error: string | null
  refetch: () => void
}

// useAuditCapabilities fetches what the gateway's audit subsystem supports on
// this install. Always returns a usable `capabilities` object (the safe
// default until/unless the real one loads), so callers never branch on null.
export function useAuditCapabilities(): UseAuditCapabilities {
  const fetcher = useCallback(() => getAuditCapabilities(), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  return {
    capabilities: data ?? DEFAULT_CAPABILITIES,
    loading,
    error,
    refetch,
  }
}
