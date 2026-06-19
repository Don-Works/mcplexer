import { Link } from 'react-router-dom'
import { cn } from '@/lib/utils'
import type { ServerTiming, ServerTimingStatus } from '@/api/types'

// Server Performance panel. Renders the per-server tools/list outcome
// from the most-recent catalog refresh: how fast each downstream
// responded (or whether it timed out / errored). Slow + timeout servers
// surface first because they're what the operator wants to act on.
//
// Rows are clickable — they deep-link to the workspace/server wiring page.
// so the operator can act on a slow server in one click (raise its
// per-server timeout, disable, or remove).

interface Props {
  timings: ServerTiming[]
}

const STATUS_RANK: Record<ServerTimingStatus, number> = {
  timeout: 0,
  error: 1,
  slow: 2,
  ok: 3,
}

const STATUS_LABEL: Record<ServerTimingStatus, string> = {
  ok: 'ok',
  slow: 'slow',
  timeout: 'timeout',
  error: 'error',
}

const STATUS_TONE: Record<ServerTimingStatus, string> = {
  ok: 'text-emerald-400 border-emerald-500/30 bg-emerald-500/[0.04]',
  slow: 'text-amber-400 border-amber-500/40 bg-amber-500/[0.05]',
  timeout: 'text-destructive border-destructive/40 bg-destructive/[0.05]',
  error: 'text-destructive border-destructive/40 bg-destructive/[0.05]',
}

export function ServerPerformancePanel({ timings }: Props) {
  if (timings.length === 0) return null

  const sorted = [...timings].sort((a, b) => {
    const r = STATUS_RANK[a.status] - STATUS_RANK[b.status]
    if (r !== 0) return r
    return b.elapsed_ms - a.elapsed_ms
  })

  const counts = sorted.reduce<Record<ServerTimingStatus, number>>(
    (acc, t) => {
      acc[t.status] = (acc[t.status] ?? 0) + 1
      return acc
    },
    { ok: 0, slow: 0, timeout: 0, error: 0 },
  )

  return (
    <section>
      <header className="mb-2 flex items-baseline justify-between">
        <h2 className="text-[11px] font-semibold uppercase tracking-widest text-muted-foreground/70">
          Server performance
        </h2>
        <span className="font-mono text-[11px] text-muted-foreground/60 tabular-nums">
          {summaryLine(counts)}
        </span>
      </header>
      <div className="border border-border bg-card/30">
        <ul className="divide-y divide-border/40">
          {sorted.map((t) => (
            <Row key={t.server_id} timing={t} />
          ))}
        </ul>
      </div>
      <p className="mt-2 text-[10.5px] leading-relaxed text-muted-foreground/60">
        Snapshot from the most recent catalog refresh. <span className="text-amber-400">Slow</span> = took longer than 3s
        but still inside the per-server budget. <span className="text-destructive">Timeout</span> means the server
        was skipped that round — bump <code className="text-foreground/70">PerServerListToolsTimeout</code> if it
        chronically misses.
      </p>
    </section>
  )
}

function Row({ timing }: { timing: ServerTiming }) {
  const href = `/workspaces?focus_server=${encodeURIComponent(timing.server_id)}`
  return (
    <li>
      <Link
        to={href}
        data-testid={`server-perf-row-${timing.server_id}`}
        className="flex items-center gap-3 px-3 py-2 font-mono text-[12px] transition-colors hover:bg-muted/30"
      >
        <span
          className={cn(
            'inline-flex w-[68px] shrink-0 items-center justify-center border px-1.5 py-px text-[10px] uppercase tracking-wider',
            STATUS_TONE[timing.status],
          )}
        >
          {STATUS_LABEL[timing.status]}
        </span>
        <span className="min-w-0 flex-1 truncate text-foreground">{timing.server_name}</span>
        <span className="shrink-0 tabular-nums text-muted-foreground">
          {formatElapsed(timing.elapsed_ms)}
        </span>
      </Link>
    </li>
  )
}

function formatElapsed(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`
  return `${ms}ms`
}

function summaryLine(counts: Record<ServerTimingStatus, number>): string {
  const parts: string[] = []
  if (counts.timeout > 0) parts.push(`${counts.timeout} timeout`)
  if (counts.error > 0) parts.push(`${counts.error} error`)
  if (counts.slow > 0) parts.push(`${counts.slow} slow`)
  if (counts.ok > 0) parts.push(`${counts.ok} ok`)
  return parts.join(' · ')
}
