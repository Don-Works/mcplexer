// RightNowStream — the dashboard's live tail. Combines audit records
// (already streamed by useAuditStream from the parent) with notification
// signals (already streamed by useSignal from the global store) into a
// single chronological feed that feels like watching the gateway breathe.
//
// Zero new SSE subscriptions — both inputs are passed in as props.
// The component is presentational + does its own filter chip state.
//
// Each row links into its deep surface: audit rows open the AuditDetail
// dialog (via the URL ?selected= flow on the audit page), signals link
// to their `link` field when present, falling back to the appropriate
// section (mesh / approvals / etc.).

import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Activity, Bell, FileText, Radio, ShieldCheck } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { isSuccessStatus } from '@/lib/audit-semantics'
import { SecretEventBadge } from '@/components/AuditDetailDialog'
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

export function RightNowStream({ audit, signal, connected, wsName }: Props) {
  const [filter, setFilter] = useState<FeedFilter>('all')

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
        <ul className="max-h-[480px] divide-y divide-border/30 overflow-y-auto">
          {visible.map((row) => (
            <FeedRowView key={`${row.kind}:${row.id}`} row={row} wsName={wsName} />
          ))}
        </ul>
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

function FeedRowView({ row, wsName }: { row: FeedRow; wsName: (id: string) => string }) {
  if (row.kind === 'audit') return <AuditRow record={row.record} wsName={wsName} />
  return <SignalRow event={row.event} />
}

function AuditRow({ record, wsName }: { record: AuditRecord; wsName: (id: string) => string }) {
  const err = !isSuccessStatus(record.status)
  return (
    <li
      data-testid={`dash-feed-audit-${record.id}`}
      className="grid grid-cols-[5rem_1fr_auto] items-center gap-3 px-4 py-2 hover:bg-accent/15"
    >
      <span className="font-mono text-[10.5px] tabular-nums text-muted-foreground/70">
        {formatHM(record.timestamp)}
      </span>
      <Link
        to={`/audit?selected=${record.id}`}
        className="group flex min-w-0 items-center gap-2"
        title={record.tool_name}
      >
        <FileText className={cn('h-3.5 w-3.5 shrink-0', err ? 'text-red-400' : 'text-muted-foreground/60')} />
        <span className="truncate font-mono text-[12px] text-foreground group-hover:text-primary">
          {record.tool_name}
        </span>
        <SecretEventBadge toolName={record.tool_name} className="shrink-0" />
        {record.workspace_id && (
          <span className="hidden truncate text-[11px] text-muted-foreground/70 lg:inline">
            · {record.workspace_name || wsName(record.workspace_id)}
          </span>
        )}
      </Link>
      <span className="flex items-center gap-2">
        {err ? (
          <Badge tone={record.status === 'blocked' ? 'warn' : 'critical'} variant="outline" className="text-[10px]">
            {record.status}
          </Badge>
        ) : (
          <span className="font-mono text-[10.5px] tabular-nums text-muted-foreground/70">
            {record.latency_ms}ms
          </span>
        )}
      </span>
    </li>
  )
}

function SignalRow({ event }: { event: StoredNotification }) {
  const dest = event.link || destinationFor(event)
  return (
    <li
      data-testid={`dash-feed-signal-${event.message_id}`}
      className="grid grid-cols-[5rem_1fr_auto] items-center gap-3 px-4 py-2 hover:bg-accent/15"
    >
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
    </li>
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
