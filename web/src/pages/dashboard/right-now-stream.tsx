// RightNowStream — the dashboard's live tail. Merges audit records and
// notification signals (both streamed by the parent; zero new SSE here) into
// one chronological feed. Presentational + owns its filter chip state.
//
// Audit rows render through the shared AuditRow (dense, compact columns) so
// they read identically to Mission Control. AuditRow needs a <table> ancestor,
// so the whole feed is one header-less table: audit rows are AuditRows, signals
// are a full-width row that keeps its own layout. Single tbody = the merged
// chronological order is preserved verbatim. Audit rows deep-link via
// /audit?selected=; signals follow their `link` (falling back per kind).

import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Activity, Bell, Radio, ShieldCheck } from 'lucide-react'
import { Table, TableBody, TableCell, TableRow } from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { isSuccessStatus } from '@/lib/audit-semantics'
import { AuditRow, type AuditColumns } from '@/components/audit/AuditRow'
import type { AuditRecord } from '@/api/types'
import type { StoredNotification } from '@/api/notifications'

type FeedFilter = 'all' | 'tools' | 'signal' | 'errors'

interface FeedRowBase {
  id: string
  ts: number
  kind: 'audit' | 'signal'
}

interface AuditFeedRow extends FeedRowBase {
  kind: 'audit'
  record: AuditRecord
}

interface SignalFeedRow extends FeedRowBase {
  kind: 'signal'
  event: StoredNotification
}

type FeedRow = AuditFeedRow | SignalFeedRow

// The compact tail shows 5 columns; signal rows span all of them.
const FEED_COLSPAN = 5

interface Props {
  audit: AuditRecord[]
  signal: StoredNotification[]
  connected: boolean
  wsName: (id: string) => string
}

const FILTERS: { key: FeedFilter; label: string }[] = [
  { key: 'all', label: 'all' },
  { key: 'tools', label: 'tools' },
  { key: 'signal', label: 'signal' },
  { key: 'errors', label: 'errors' },
]

// Dense compact column set for the tail: the same fields the bespoke 3-col
// grid carried (time / tool+secret badge / workspace / status+latency). The
// rest stay hidden to keep the tail narrow; the row click opens the inspector.
const STREAM_COLUMNS: AuditColumns = {
  timestamp: true,
  tool: true,
  workspace: true,
  status: true,
  latency: true,
  session: false,
  client: false,
  reason: false,
  cache: false,
  group: false,
}

export function RightNowStream({ audit, signal, connected, wsName }: Props) {
  const [filter, setFilter] = useState<FeedFilter>('all')
  const navigate = useNavigate()

  const merged = useMemo(() => buildFeed(audit, signal), [audit, signal])
  const visible = useMemo(() => applyFilter(merged, filter).slice(0, 40), [merged, filter])

  return (
    <section
      data-testid="dash-right-now"
      className="border border-border bg-card/30"
    >
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 px-4 py-2">
        <div className="flex items-center gap-2.5">
          <h2 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            Right now
          </h2>
          <LiveDot connected={connected} />
        </div>
        <FilterChips filter={filter} onSet={setFilter} />
      </header>
      {visible.length === 0 ? (
        <EmptyStream filter={filter} />
      ) : (
        // One header-less table so AuditRow (a <tr>) and the signal rows share a
        // single tbody, keeping the merged chronological order intact.
        <div className="max-h-[480px] overflow-y-auto">
          <Table className="table-fixed">
            <colgroup>
              <col className="w-[7rem]" />
              <col />
              <col className="hidden md:table-column w-[10rem]" />
              <col className="w-[6rem]" />
              <col className="hidden sm:table-column w-[5rem]" />
            </colgroup>
            <TableBody>
              {visible.map((row, idx) =>
                row.kind === 'audit' ? (
                  <AuditRow
                    key={`audit:${row.id}`}
                    record={row.record}
                    columns={STREAM_COLUMNS}
                    dense
                    isLive={idx === 0}
                    wsName={wsName}
                    onSelect={(record) => navigate(`/audit?selected=${record.id}`)}
                  />
                ) : (
                  <SignalRow key={`signal:${row.id}`} event={row.event} />
                ),
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  )
}

function LiveDot({ connected }: { connected: boolean }) {
  if (!connected) {
    return (
      <span className="inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60">
        <span className="h-1.5 w-1.5 rounded-full bg-muted-foreground/40" />
        connecting
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-wider text-emerald-400">
      <span className="relative flex h-1.5 w-1.5">
        <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
        <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-emerald-500" />
      </span>
      live
    </span>
  )
}

function FilterChips({ filter, onSet }: { filter: FeedFilter; onSet: (f: FeedFilter) => void }) {
  return (
    <div className="flex items-center gap-1">
      {FILTERS.map((f) => (
        <button
          key={f.key}
          type="button"
          onClick={() => onSet(f.key)}
          data-testid={`dash-feed-filter-${f.key}`}
          className={cn(
            'border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wider transition-colors',
            filter === f.key
              ? 'border-primary/40 bg-primary/5 text-foreground'
              : 'border-dashed border-border text-muted-foreground/80 hover:border-border/80 hover:text-foreground',
          )}
        >
          {f.label}
        </button>
      ))}
    </div>
  )
}

// SignalRow — a notification rendered as a full-width row inside the shared
// feed table (colSpan over all audit columns), keeping its own 3-zone layout
// (time / linked title / kind badge) and deep link.
function SignalRow({ event }: { event: StoredNotification }) {
  const dest = event.link || destinationFor(event)
  return (
    <TableRow
      data-testid={`dash-feed-signal-${event.message_id}`}
      className="border-border/30 hover:bg-accent/15"
    >
      <TableCell colSpan={FEED_COLSPAN} className="py-2">
        <div className="grid grid-cols-[5rem_1fr_auto] items-center gap-3">
          <span className="font-mono text-[10.5px] tabular-nums text-muted-foreground/70">
            {formatHM(event.created_at)}
          </span>
          <Link to={dest} className="group flex min-w-0 items-center gap-2" title={event.title}>
            <SignalIcon event={event} />
            <span className="truncate text-[12px] text-foreground group-hover:text-primary">
              {event.title}
            </span>
            {event.agent_name && (
              <span className="hidden truncate text-[11px] text-muted-foreground/70 lg:inline">
                · {event.agent_name}
              </span>
            )}
          </Link>
          <Badge tone={toneBadge(event.priority)} variant="outline" className="text-[10px]">
            {event.kind || event.source || 'signal'}
          </Badge>
        </div>
      </TableCell>
    </TableRow>
  )
}

function SignalIcon({ event }: { event: StoredNotification }) {
  const kind = event.kind || event.source
  const cls = cn('h-3.5 w-3.5 shrink-0', toneIconClass(event.priority))
  if (kind?.includes('approval')) return <ShieldCheck className={cls} />
  if (kind?.includes('mesh') || event.source === 'mesh') return <Radio className={cls} />
  return <Bell className={cls} />
}

function EmptyStream({ filter }: { filter: FeedFilter }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1 px-4 py-8 text-center text-muted-foreground">
      <Activity className="h-5 w-5 text-muted-foreground/40" />
      <p className="text-[12px]">
        {filter === 'all'
          ? 'Nothing happening yet. New tool calls and signals will tail here.'
          : `No ${filter} events in this window.`}
      </p>
    </div>
  )
}

// --- helpers --------------------------------------------------------------

function buildFeed(audit: AuditRecord[], signal: StoredNotification[]): FeedRow[] {
  const rows: FeedRow[] = [
    ...audit.map<AuditFeedRow>((r) => ({
      id: r.id,
      ts: new Date(r.timestamp).getTime(),
      kind: 'audit',
      record: r,
    })),
    ...signal.map<SignalFeedRow>((e) => ({
      id: `${e.id}:${e.message_id}`,
      ts: new Date(e.created_at).getTime(),
      kind: 'signal',
      event: e,
    })),
  ]
  rows.sort((a, b) => b.ts - a.ts)
  return rows
}

function applyFilter(rows: FeedRow[], filter: FeedFilter): FeedRow[] {
  if (filter === 'all') return rows
  if (filter === 'tools') return rows.filter((r) => r.kind === 'audit')
  if (filter === 'signal') return rows.filter((r) => r.kind === 'signal')
  return rows.filter((r) => r.kind === 'audit' && !isSuccessStatus(r.record.status))
}

function formatHM(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  } catch {
    return ''
  }
}

function destinationFor(event: StoredNotification): string {
  const kind = event.kind || event.source
  if (kind?.includes('approval')) return '/approvals'
  if (kind?.startsWith('memory')) return '/memory'
  if (event.source === 'mesh' || kind?.includes('mesh')) return `/mesh${event.message_id ? `?msg=${event.message_id}` : ''}`
  return '/signals'
}

function toneBadge(
  p: string,
): 'critical' | 'high' | 'warn' | 'info' | 'muted' {
  if (p === 'critical') return 'critical'
  if (p === 'high') return 'high'
  if (p === 'normal') return 'info'
  return 'muted'
}

function toneIconClass(p: string): string {
  if (p === 'critical') return 'text-red-400'
  if (p === 'high') return 'text-orange-400'
  if (p === 'normal') return 'text-sky-400'
  return 'text-muted-foreground/60'
}
