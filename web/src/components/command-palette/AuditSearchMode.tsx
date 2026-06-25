/* eslint-disable react-refresh/only-export-components --
 * This module exports the useAuditSearchMode hook (the palette's public
 * surface) alongside an internal AuditRow component, so Vite HMR's
 * component-only rule does not apply. */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { NavigateFunction } from 'react-router-dom'
import { searchAuditLogs } from '@/api/client'
import type { AuditRecord, AuditSearchMode as Ranker } from '@/api/types'
import { useAuditCapabilities } from '@/hooks/use-audit-capabilities'
import { normalizeStatus } from '@/lib/audit-semantics'
import { cn } from '@/lib/utils'

// AuditSearchMode — the palette body for the `/` audit-search mode (DESIGN
// §4.0 grammar, extended). It owns its own debounce, request lifecycle, and
// arrow/Enter/Escape keys (the parent input forwards keystrokes here while
// this mode is active), mirroring how the ref/tag/@ IndexTypeahead modes own
// their keyboard inside the same shared cmd+K input.
//
//   • Query is debounced ~200ms so each keystroke doesn't fire a request.
//   • A run-id guard drops superseded responses (slow earlier query can never
//     overwrite a faster later one).
//   • The group header badges the *returned* ranker — "Semantic" (vector),
//     "Smart" (tfidf), or "Text" (fts) — so the operator knows how fuzzy the
//     hits are. Capabilities gate the resting label before any query lands.
//   • Selecting a hit deep-links to /audit?selected=<id> (AuditPage reads the
//     param and fetches the row by id if it isn't already loaded).

const DEBOUNCE_MS = 200
const RESULT_LIMIT = 12

interface Props {
  // query is the residual text after the `/` prefix.
  query: string
  navigate: NavigateFunction
  onClose: () => void
  // listboxId wires the parent input's aria-controls to this listbox.
  listboxId: string
  // onActiveDescendant mirrors the highlighted row id up to the parent input
  // so it can carry aria-activedescendant (the §7 combobox contract).
  onActiveDescendant: (id: string) => void
}

// rankerBadge maps the ranker that answered to its operator-facing label.
function rankerBadge(mode: Ranker): { label: string; tone: string } {
  switch (mode) {
    case 'vector':
      return { label: 'Semantic', tone: 'text-primary' }
    case 'tfidf':
      return { label: 'Smart', tone: 'text-sky-300' }
    default:
      return { label: 'Text', tone: 'text-muted-foreground' }
  }
}

// restingLabel is shown before any query lands (or while typing the first
// chars). It reflects the *best available* ranker per capabilities, degrading
// to "Smart" when vector search isn't built on this install.
function restingLabel(canVector: boolean): string {
  return canVector ? 'Semantic' : 'Smart'
}

function relativeTime(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return iso
  const diff = Date.now() - then
  const sec = Math.round(Math.abs(diff) / 1000)
  if (sec < 5) return 'just now'
  if (sec < 60) return `${sec}s ago`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr}h ago`
  const day = Math.round(hr / 24)
  if (day < 30) return `${day}d ago`
  const mo = Math.round(day / 30)
  if (mo < 12) return `${mo}mo ago`
  return `${Math.round(mo / 12)}y ago`
}

function statusDot(status: string): string {
  switch (normalizeStatus(status)) {
    case 'success':
      return 'bg-emerald-500'
    case 'blocked':
      return 'bg-amber-500'
    default:
      return 'bg-destructive'
  }
}

// useAuditSearchMode is the imperative-ish state hook the parent drives: it
// returns the rendered body plus a key handler the parent forwards into.
export function useAuditSearchMode({
  query,
  navigate,
  onClose,
  listboxId,
  onActiveDescendant,
}: Props): { body: React.ReactNode; onKeyDown: (e: React.KeyboardEvent) => void } {
  const { capabilities } = useAuditCapabilities()
  const canVector = capabilities.search.vector

  const [results, setResults] = useState<AuditRecord[]>([])
  const [ranker, setRanker] = useState<Ranker | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [active, setActive] = useState(0)
  const runIdRef = useRef(0)
  const listRef = useRef<HTMLDivElement | null>(null)

  const trimmed = query.trim()

  // Debounced search. An empty query clears rather than firing a request.
  useEffect(() => {
    if (!trimmed) {
      runIdRef.current += 1
      setResults([])
      setRanker(null)
      setLoading(false)
      setError(null)
      return
    }
    const id = ++runIdRef.current
    setLoading(true)
    setError(null)
    const handle = window.setTimeout(() => {
      searchAuditLogs(trimmed, { limit: RESULT_LIMIT })
        .then((resp) => {
          if (id !== runIdRef.current) return
          setResults(resp.data ?? [])
          setRanker(resp.mode)
          setLoading(false)
        })
        .catch((err: unknown) => {
          if (id !== runIdRef.current) return
          setError(err instanceof Error ? err.message : 'Search failed')
          setResults([])
          setLoading(false)
        })
    }, DEBOUNCE_MS)
    return () => window.clearTimeout(handle)
  }, [trimmed])

  // Reset the cursor whenever the result set changes.
  useEffect(() => {
    setActive(0)
  }, [results])

  // Keep the highlighted row in view + mirror its id to the parent input.
  useEffect(() => {
    const hit = results[active]
    onActiveDescendant(hit ? `audit-opt-${hit.id}` : '')
    if (!listRef.current) return
    const el = listRef.current.querySelector<HTMLElement>(`[data-audit-index="${active}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [active, results, onActiveDescendant])

  const open = useCallback(
    (record: AuditRecord) => {
      onClose()
      navigate(`/audit?selected=${encodeURIComponent(record.id)}`)
    },
    [navigate, onClose],
  )

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
        return
      }
      if (results.length === 0) return
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(i + 1, results.length - 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(i - 1, 0))
      } else if (e.key === 'Enter') {
        e.preventDefault()
        const hit = results[active]
        if (hit) open(hit)
      } else if (e.key === 'Home') {
        e.preventDefault()
        setActive(0)
      } else if (e.key === 'End') {
        e.preventDefault()
        setActive(results.length - 1)
      }
    },
    [results, active, open, onClose],
  )

  const badge = useMemo(
    () => (ranker ? rankerBadge(ranker) : { label: restingLabel(canVector), tone: 'text-muted-foreground' }),
    [ranker, canVector],
  )

  const body = (
    <div ref={listRef} role="listbox" id={listboxId} aria-label="Audit search results">
      <div className="flex items-baseline justify-between px-3 pb-1 pt-2.5">
        <span className="text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/60">
          Audit logs
        </span>
        <span className={cn('font-mono text-[10px] uppercase tracking-wider', badge.tone)}>
          {loading ? 'searching…' : badge.label}
        </span>
      </div>

      {error ? (
        <div className="px-4 py-10 text-center text-[12px] text-destructive">{error}</div>
      ) : !trimmed ? (
        <div className="px-4 py-10 text-center text-[12px] text-muted-foreground">
          <p className="text-foreground/80">Search the audit trail.</p>
          <p className="mt-2 text-muted-foreground/60">
            try{' '}
            <span className="border border-border bg-muted/30 px-1.5 py-0.5 text-foreground/80">
              payment failed
            </span>
            {' · '}
            <span className="border border-border bg-muted/30 px-1.5 py-0.5 text-foreground/80">
              denied secret
            </span>
          </p>
        </div>
      ) : loading && results.length === 0 ? (
        <div className="px-4 py-10 text-center text-[12px] text-muted-foreground">
          <span className="animate-pulse">searching audit logs…</span>
        </div>
      ) : results.length === 0 ? (
        <div className="px-4 py-10 text-center text-[12px] text-muted-foreground">
          no audit rows for <span className="text-foreground">"{trimmed}"</span>
        </div>
      ) : (
        <div className="px-1 pb-1">
          {results.map((r, i) => (
            <AuditRow
              key={r.id}
              record={r}
              index={i}
              active={i === active}
              onSelect={() => open(r)}
            />
          ))}
        </div>
      )}
    </div>
  )

  return { body, onKeyDown }
}

function AuditRow({
  record,
  index,
  active,
  onSelect,
}: {
  record: AuditRecord
  index: number
  active: boolean
  onSelect: () => void
}) {
  const ws = record.workspace_name || record.workspace_id || '—'
  return (
    <button
      type="button"
      role="option"
      id={`audit-opt-${record.id}`}
      aria-selected={active}
      data-audit-index={index}
      data-testid={`cmdk-audit-${record.id}`}
      onClick={onSelect}
      className={cn(
        'group flex w-full items-center gap-3 px-3 py-2 text-left transition-colors',
        active ? 'bg-accent/50' : 'hover:bg-accent/25',
      )}
    >
      <span aria-hidden className={cn('h-1.5 w-1.5 shrink-0', statusDot(record.status))} />
      <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-foreground">
        {record.tool_name || record.subpath || record.id}
      </span>
      <span className="hidden shrink-0 truncate font-mono text-[11px] text-muted-foreground/70 sm:inline-block sm:max-w-[10rem]">
        {ws}
      </span>
      <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60 tabular-nums">
        {relativeTime(record.timestamp)}
      </span>
    </button>
  )
}
