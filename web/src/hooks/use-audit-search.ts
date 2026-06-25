import { useCallback, useRef, useState } from 'react'
import { searchAuditLogs } from '@/api/client'
import type {
  AuditFilter,
  AuditRecord,
  AuditSearchMode,
} from '@/api/types'

interface UseAuditSearch {
  results: AuditRecord[] | null
  mode: AuditSearchMode | null
  total: number
  loading: boolean
  error: string | null
  // run executes a search; an empty/blank query clears instead of querying.
  run: (q: string, filter?: AuditFilter) => void
  clear: () => void
}

// useAuditSearch drives the free-text / semantic audit search box. Unlike the
// list (which streams + paginates), search is a one-shot request the user
// triggers explicitly. `results === null` means "no search active" (show the
// normal list); an empty array means "searched, nothing matched". A run-id
// guard drops responses from superseded queries so a slow earlier search can
// never overwrite a faster later one.
export function useAuditSearch(): UseAuditSearch {
  const [results, setResults] = useState<AuditRecord[] | null>(null)
  const [mode, setMode] = useState<AuditSearchMode | null>(null)
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const runIdRef = useRef(0)

  const clear = useCallback(() => {
    runIdRef.current += 1 // invalidate any in-flight search
    setResults(null)
    setMode(null)
    setTotal(0)
    setLoading(false)
    setError(null)
  }, [])

  const run = useCallback(
    (q: string, filter?: AuditFilter) => {
      const trimmed = q.trim()
      if (!trimmed) {
        clear()
        return
      }
      const id = ++runIdRef.current
      setLoading(true)
      setError(null)
      searchAuditLogs(trimmed, filter)
        .then((resp) => {
          if (id !== runIdRef.current) return // superseded
          setResults(resp.data ?? [])
          setMode(resp.mode)
          setTotal(resp.total ?? resp.data?.length ?? 0)
          setLoading(false)
        })
        .catch((err: unknown) => {
          if (id !== runIdRef.current) return
          setError(err instanceof Error ? err.message : 'Search failed')
          setLoading(false)
        })
    },
    [clear],
  )

  return { results, mode, total, loading, error, run, clear }
}
