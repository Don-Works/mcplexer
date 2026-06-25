import { useCallback, useEffect, useMemo, useState } from 'react'
import { queryAuditLogs } from '@/api/client'
import type { AuditFilter, AuditRecord } from '@/api/types'

interface UseAuditFeed {
  // Accumulated rows across every loaded window (newest first).
  records: AuditRecord[]
  total: number
  loading: boolean
  error: string | null
  // Truthy when another window can be fetched (drives the sentinel + button).
  hasMore: boolean
  loadMore: () => void
}

/**
 * useAuditFeed — the infinite keyset (cursor) feed behind the Mission Control
 * list. It accumulates windows in order and resets to the top whenever the
 * query identity (everything in the filter except limit/cursor) changes. Keyset
 * pagination stays stable under live inserts, so appending never duplicates or
 * skips rows the way offset paging does.
 */
export function useAuditFeed(filter: AuditFilter): UseAuditFeed {
  // Strip cursor so it never participates in the identity; carry q/sort/facets.
  const feedFilter = useMemo(() => ({ ...filter, cursor: undefined }), [filter])
  const feedKey = useMemo(
    () => JSON.stringify({ ...feedFilter, limit: undefined }),
    [feedFilter],
  )

  const [records, setRecords] = useState<AuditRecord[]>([])
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [total, setTotal] = useState(0)
  const [loadCursor, setLoadCursor] = useState<string | undefined>(undefined)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Reset the accumulator whenever the query identity changes.
  useEffect(() => {
    setRecords([])
    setCursor(undefined)
    setTotal(0)
    setLoadCursor(undefined)
  }, [feedKey])

  // Fetch the window for the current loadCursor and append (or replace on top).
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    queryAuditLogs({ ...feedFilter, cursor: loadCursor })
      .then((resp) => {
        if (cancelled) return
        setRecords((prev) => (loadCursor ? [...prev, ...resp.data] : resp.data))
        setCursor(resp.next_cursor)
        setTotal(resp.total)
        setLoading(false)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : 'Failed to load audit logs')
        setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // feedFilter is captured via feedKey; depending on it directly would refetch
    // on every render since the object identity changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [feedKey, loadCursor])

  const loadMore = useCallback(() => {
    if (cursor && !loading) setLoadCursor(cursor)
  }, [cursor, loading])

  return { records, total, loading, error, hasMore: Boolean(cursor), loadMore }
}
