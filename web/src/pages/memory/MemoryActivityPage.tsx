// MemoryActivityPage — the durable "human visual window" into what the
// memory system is DOING over time: a browsable, day-grouped, filterable
// timeline of memory mutations (written, offered, consolidated).
//
// Where the landing ActivityCard shows only the live last-15 tail, this
// page pages back through the persisted notification store (source ===
// 'memory'), so the operator can scroll the full history — and new events
// prepend live via the shared Signal stream.
//
// Data source: GET /api/v1/notifications (persisted), with server-side
// source=memory filtering and paged with the `before` cursor.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowUpRight, Brain, Filter, RefreshCw, TriangleAlert } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { listNotifications, type StoredNotification } from '@/api/notifications'
import { useSignal } from '@/components/notifications/use-signal'
import { isMemoryEvent, relativeTime } from './memory-utils'
import { cn } from '@/lib/utils'

const PAGE = 100

type KindFilter = 'all' | 'written' | 'offered' | 'consolidated'

const FILTERS: { key: KindFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'written', label: 'Written' },
  { key: 'offered', label: 'Offered' },
  { key: 'consolidated', label: 'Consolidated' },
]

function matchesFilter(n: StoredNotification, f: KindFilter): boolean {
  if (f === 'all') return true
  const k = (n.kind || '').toLowerCase()
  if (f === 'written') {
    return (
      k.includes('writ') ||
      k.includes('saved') ||
      k.includes('created') ||
      k.includes('invalidat') ||
      k.includes('pin')
    )
  }
  if (f === 'offered') return k.includes('offer') || k.includes('shar')
  if (f === 'consolidated') return k.includes('consolidat')
  return true
}

// Memory-only filter applied to every fetched / live batch.
function onlyMemory(rows: StoredNotification[]): StoredNotification[] {
  return rows.filter((n) => isMemoryEvent(n.kind, n.source))
}

export function MemoryActivityPage() {
  const [items, setItems] = useState<StoredNotification[]>([])
  const [pageIndex, setPageIndex] = useState(0)
  const [pageCursors, setPageCursors] = useState<Array<number | undefined>>([
    undefined,
  ])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const [filter, setFilter] = useState<KindFilter>('all')

  const loadPage = useCallback(async (before: number | undefined, index: number) => {
    setLoading(true)
    setError(null)
    try {
      const res = await listNotifications({ source: 'memory', limit: PAGE, before })
      setItems(onlyMemory(res.notifications))
      const persisted = res.notifications.map((r) => r.id).filter((id) => id > 0)
      const nextCursor = persisted.length ? Math.min(...persisted) : undefined
      setDone(res.notifications.length < PAGE)
      setPageIndex(index)
      setPageCursors((prev) => {
        const next = prev.slice(0, index + 1)
        next[index] = before
        if (nextCursor !== undefined) next[index + 1] = nextCursor
        return next
      })
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to load activity')
    } finally {
      setLoading(false)
    }
  }, [])

  // Initial load.
  useEffect(() => {
    let active = true
    loadPage(undefined, 0).finally(() => {
      if (!active) return
    })
    return () => {
      active = false
    }
  }, [loadPage])

  // Live prepend — new memory events arriving on the Signal stream slot in
  // at the top without a refetch.
  const { events } = useSignal()
  const liveKey = useMemo(() => {
    const top = events.find((e) => isMemoryEvent(e.kind, e.source))
    return top ? top.message_id : ''
  }, [events])
  useEffect(() => {
    if (!liveKey) return
    if (pageIndex !== 0) return
    setItems((prev) => {
      const seen = new Set(prev.map((p) => p.message_id))
      const add = onlyMemory(events).filter((r) => !seen.has(r.message_id))
      return add.length ? [...add, ...prev].slice(0, PAGE) : prev
    })
  }, [liveKey, events, pageIndex])

  const filtered = useMemo(
    () => items.filter((n) => matchesFilter(n, filter)),
    [items, filter],
  )
  const groups = useMemo(() => groupByDay(filtered), [filtered])
  const olderCursor = pageCursors[pageIndex + 1]
  const hasOlder = !done && olderCursor !== undefined
  const hasNewer = pageIndex > 0

  return (
    <div className="space-y-6">
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2.5 text-2xl font-semibold tracking-tight">
          <Brain className="h-5 w-5 text-primary" />
          Memory activity
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Every memory your gateway has written, shared, or consolidated, newest
          first. New events appear live as agents learn.
        </p>
      </header>

      <div className="flex flex-wrap items-center gap-1.5">
        <span className="mr-1 inline-flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
          <Filter className="h-3 w-3" />
          Filter
        </span>
        {FILTERS.map((f) => (
          <button
            key={f.key}
            type="button"
            onClick={() => setFilter(f.key)}
            className={cn(
              'inline-flex items-center border px-2.5 py-1 font-mono text-[12px] transition-colors',
              filter === f.key
                ? 'border-primary/40 bg-primary/5 text-foreground'
                : 'border-dashed border-border text-muted-foreground hover:border-border/80 hover:text-foreground',
            )}
          >
            {f.label}
          </button>
        ))}
      </div>

      <Card>
        <CardContent className="p-0">
          {loading ? (
            <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
              <RefreshCw className="h-3.5 w-3.5 animate-spin" />
              Loading activity…
            </div>
          ) : error && items.length === 0 ? (
            <div className="flex items-center justify-between gap-3 p-6">
              <div className="flex items-center gap-2.5 text-sm">
                <TriangleAlert className="h-4 w-4 shrink-0 text-destructive" />
                <span className="text-foreground">Could not load activity</span>
                <span className="font-mono text-[11px] text-muted-foreground">{error}</span>
              </div>
              <Button variant="ghost" size="sm" onClick={() => loadPage(undefined, 0)}>
                Retry
              </Button>
            </div>
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<Brain className="h-7 w-7" />}
              title={
                items.length === 0
                  ? 'No memory activity yet'
                  : `No ${filter} events in the loaded history`
              }
              description={
                items.length === 0
                  ? 'When an agent writes a memory, accepts a peer offer, or runs a consolidation pass, it will appear here.'
                  : 'Switch filters or move to an older page to keep exploring.'
              }
              density="card"
              testid="memory-activity-empty"
            />
          ) : (
            <div className="divide-y divide-border/30">
              {groups.map((g) => (
                <section key={g.label}>
                  <div className="sticky top-0 z-10 flex items-center justify-between border-b border-border/40 bg-card/95 px-4 py-1.5 backdrop-blur">
                    <span className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                      {g.label}
                    </span>
                    <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
                      {g.rows.length}
                    </span>
                  </div>
                  <ul className="divide-y divide-border/20">
                    {g.rows.map((e) => (
                      <ActivityRow key={`${e.id}-${e.message_id}`} event={e} />
                    ))}
                  </ul>
                </section>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {!loading && (items.length > 0 || pageIndex > 0) && (
        <div className="flex flex-wrap items-center justify-center gap-2 pb-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => loadPage(pageCursors[pageIndex - 1], pageIndex - 1)}
            disabled={!hasNewer || loading}
            data-testid="memory-activity-newer"
          >
            Newer
          </Button>
          <span className="min-w-20 text-center font-mono text-[11px] tabular-nums text-muted-foreground/70">
            Page {pageIndex + 1}
          </span>
          {done && !hasOlder ? (
            <span className="text-[11px] text-muted-foreground/60">
              Beginning of recorded activity
            </span>
          ) : (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                if (olderCursor !== undefined) loadPage(olderCursor, pageIndex + 1)
              }}
              disabled={!hasOlder || loading}
              data-testid="memory-activity-older"
            >
              Older
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

function ActivityRow({ event }: { event: StoredNotification }) {
  const tone =
    event.priority === 'critical'
      ? 'bg-red-400'
      : event.priority === 'high'
        ? 'bg-orange-400'
        : 'bg-emerald-400/80'
  const body = (
    <div className="flex items-start gap-3 px-4 py-2.5 transition-colors hover:bg-muted/20">
      <span className={cn('mt-1.5 inline-flex h-1.5 w-1.5 shrink-0 rounded-full', tone)} />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[13px] font-medium text-foreground">
            {event.title}
          </span>
          <Badge variant="outline" tone="muted" className="shrink-0 font-mono text-[9px] uppercase">
            {humanizeKind(event.kind)}
          </Badge>
          {event.link && (
            <ArrowUpRight className="h-3 w-3 shrink-0 text-muted-foreground/40" />
          )}
        </div>
        {event.body && (
          <p className="mt-0.5 truncate text-[11.5px] text-muted-foreground/90">
            {event.body}
          </p>
        )}
      </div>
      <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
        {relativeTime(event.created_at)}
      </span>
    </div>
  )
  if (event.link) {
    return (
      <li>
        <Link to={event.link} className="block">
          {body}
        </Link>
      </li>
    )
  }
  return <li>{body}</li>
}

function humanizeKind(kind: string): string {
  if (!kind) return 'event'
  return kind.replace(/^memory[._]/i, '').replace(/_/g, ' ') || 'event'
}

// --- day grouping ------------------------------------------------------

interface DayGroup {
  label: string
  rows: StoredNotification[]
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

function dayLabel(iso: string): string {
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return 'Unknown'
  const diffDays = Math.round((startOfDay(new Date()) - startOfDay(d)) / 86_400_000)
  if (diffDays <= 0) return 'Today'
  if (diffDays === 1) return 'Yesterday'
  if (diffDays < 7) return d.toLocaleDateString(undefined, { weekday: 'long' })
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

function groupByDay(rows: StoredNotification[]): DayGroup[] {
  const out: DayGroup[] = []
  let current: DayGroup | null = null
  for (const r of rows) {
    const label = dayLabel(r.created_at)
    if (!current || current.label !== label) {
      current = { label, rows: [] }
      out.push(current)
    }
    current.rows.push(r)
  }
  return out
}
