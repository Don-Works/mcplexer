import { useCallback, useEffect, useMemo, useState } from 'react'
import type { useSearchParams } from 'react-router-dom'
import { queryAuditLogs } from '@/api/client'
import type { AuditRecord } from '@/api/types'

interface UseAuditSelection {
  selected: AuditRecord | null
  selectedIndex: number
  setSelected: (record: AuditRecord | null) => void
  goPrev: () => void
  goNext: () => void
  hasPrev: boolean
  hasNext: boolean
  // True at the 2xl breakpoint — the page mounts the inline inspector vs the
  // Sheet drawer off this so only one keyboard nav handler is ever live.
  isWide: boolean
}

/**
 * useAuditSelection — URL-backed (`?selected=<id>`) record selection for the
 * audit page. Selection survives filter changes and deep links: when the id
 * isn't in the loaded feed (deep link to an old page) it fetches the record by
 * id and pins it. Owns the keyboard nav (j/k + arrows to walk, Esc to clear),
 * armed only at 2xl+ where the inline inspector is the active surface.
 */
export function useAuditSelection(
  feedRecords: AuditRecord[],
  searchParams: URLSearchParams,
  setSearchParams: ReturnType<typeof useSearchParams>[1],
): UseAuditSelection {
  const selectedId = searchParams.get('selected')
  const [fallbackRecord, setFallbackRecord] = useState<AuditRecord | null>(null)

  useEffect(() => {
    if (!selectedId || feedRecords.some((r) => r.id === selectedId)) {
      setFallbackRecord(null)
      return
    }
    let cancelled = false
    queryAuditLogs({ id: selectedId, limit: 1 })
      .then((resp) => !cancelled && setFallbackRecord(resp.data?.[0] ?? null))
      .catch(() => !cancelled && setFallbackRecord(null))
    return () => {
      cancelled = true
    }
  }, [selectedId, feedRecords])

  const selected = useMemo<AuditRecord | null>(
    () => (selectedId ? feedRecords.find((r) => r.id === selectedId) ?? fallbackRecord : null),
    [selectedId, feedRecords, fallbackRecord],
  )

  const setSelected = useCallback(
    (record: AuditRecord | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (record) next.set('selected', record.id)
          else next.delete('selected')
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const selectedIndex = selected ? feedRecords.findIndex((r) => r.id === selected.id) : -1
  const hasPrev = selectedIndex > 0
  const hasNext = selectedIndex >= 0 && selectedIndex < feedRecords.length - 1
  const goPrev = useCallback(() => {
    if (hasPrev) setSelected(feedRecords[selectedIndex - 1])
  }, [hasPrev, selectedIndex, feedRecords, setSelected])
  const goNext = useCallback(() => {
    if (hasNext) setSelected(feedRecords[selectedIndex + 1])
  }, [hasNext, selectedIndex, feedRecords, setSelected])

  // Track the 2xl breakpoint reactively. Below this the audit table needs the
  // width more than it needs a persistent empty inspector pane.
  const [isWide, setIsWide] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(min-width: 1536px)').matches,
  )
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 1536px)')
    const onChange = (e: MediaQueryListEvent) => setIsWide(e.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  // Inline-inspector keyboard nav, armed only when a record is selected at 2xl+.
  useEffect(() => {
    if (!selected || !isWide) return
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
      if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault()
        goNext()
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault()
        goPrev()
      } else if (e.key === 'Escape') {
        e.preventDefault()
        setSelected(null)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selected, isWide, goNext, goPrev, setSelected])

  return { selected, selectedIndex, setSelected, goPrev, goNext, hasPrev, hasNext, isWide }
}
