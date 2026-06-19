import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Activity, CheckCircle2, Gauge, Loader2, RefreshCw, XCircle } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { listDelegationModelCapacity, type DelegationModelCapacity } from '@/api/workers'
import { useApi } from '@/hooks/use-api'
import { cn } from '@/lib/utils'

type TaskKind = 'all' | 'coding' | 'review' | 'architecture' | 'tool_calling' | 'visual'

const TASK_KINDS: { key: TaskKind; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'coding', label: 'Coding' },
  { key: 'review', label: 'Review' },
  { key: 'architecture', label: 'Architecture' },
  { key: 'tool_calling', label: 'Tools' },
  { key: 'visual', label: 'Visual' },
]

export function ModelLeaderboardPage() {
  const [taskKind, setTaskKind] = useState<TaskKind>('all')
  const fetcher = useCallback(
    () => listDelegationModelCapacity({
      taskKind: taskKind === 'all' ? undefined : taskKind,
      limit: 100,
    }),
    [taskKind],
  )
  const { data, loading, error, refetch } = useApi(fetcher)
  const rows = useMemo(() => sortRows(data ?? []), [data])
  const summary = useMemo(() => buildSummary(rows), [rows])

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold">
            <Gauge className="h-6 w-6" /> Model leaderboard
          </h1>
          <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
            Delegation model ranking from reviewed runs, operational success, active load, speed, and cost.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link to="/delegations">Open delegations</Link>
          </Button>
          <Button variant="ghost" size="sm" onClick={refetch} disabled={loading}>
            {loading ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : <RefreshCw className="mr-1.5 h-4 w-4" />}
            Refresh
          </Button>
        </div>
      </header>

      <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Task kind">
        {TASK_KINDS.map((kind) => (
          <Button
            key={kind.key}
            type="button"
            variant={taskKind === kind.key ? 'default' : 'outline'}
            size="sm"
            onClick={() => setTaskKind(kind.key)}
            aria-pressed={taskKind === kind.key}
            className="h-8"
          >
            {kind.label}
          </Button>
        ))}
      </div>

      {error && (
        <div className="flex items-center justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          <span>{error}</span>
          <Button size="sm" variant="ghost" onClick={refetch}>Retry</Button>
        </div>
      )}

      {loading && !data ? (
        <div className="flex items-center gap-2 border border-border bg-card/50 p-4 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading model ranks...
        </div>
      ) : rows.length > 0 ? (
        <>
          <SummaryStrip summary={summary} />
          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="flex items-center gap-2 text-base">
                <Activity className="h-4 w-4" /> Ranked models
              </CardTitle>
            </CardHeader>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-14 text-right">Rank</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead className="text-right">Capacity</TableHead>
                    <TableHead className="text-right">Runs</TableHead>
                    <TableHead className="text-right">Ops success</TableHead>
                    <TableHead className="text-right">Review</TableHead>
                    <TableHead className="text-right">Avg time</TableHead>
                    <TableHead className="text-right">Cost</TableHead>
                    <TableHead>Status</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row, index) => (
                    <LeaderboardRow key={row.model_key || `${row.model_provider}/${row.model_id}`} row={row} rank={index + 1} />
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </>
      ) : (
        <EmptyState
          icon={<Gauge className="h-10 w-10" />}
          title="No model rank data yet"
          description="Review completed delegations to give the leaderboard evidence."
          action={
            <Button asChild>
              <Link to="/delegations">Review delegations</Link>
            </Button>
          }
        />
      )}
    </div>
  )
}

function SummaryStrip({ summary }: { summary: ReturnType<typeof buildSummary> }) {
  return (
    <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
      <Metric label="Models" value={String(summary.models)} />
      <Metric label="Runs" value={formatCount(summary.runs)} />
      <Metric label="Available" value={String(summary.available)} />
      <Metric label="Reviewed" value={String(summary.reviewed)} />
    </div>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="border border-border bg-card/50 px-3 py-2">
      <div className="text-[10px] font-medium uppercase text-muted-foreground">{label}</div>
      <div className="mt-1 font-mono text-lg tabular-nums">{value}</div>
    </div>
  )
}

function LeaderboardRow({ row, rank }: { row: DelegationModelCapacity; rank: number }) {
  const successRate = row.operational_success_rate ?? row.success_rate
  const modelLabel = row.label || row.model_id
  const modelKey = row.model_key || `${row.model_provider}/${row.model_id}`
  return (
    <TableRow>
      <TableCell className="text-right font-mono text-xs text-muted-foreground">{rank}</TableCell>
      <TableCell>
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">{modelLabel}</div>
          <div className="truncate font-mono text-[11px] text-muted-foreground">{modelKey}</div>
          {row.capability_tags && row.capability_tags.length > 0 ? (
            <div className="mt-1 flex flex-wrap gap-1">
              {row.capability_tags.slice(0, 4).map((tag) => (
                <Badge key={tag} variant="outline" className="rounded-sm px-1.5 py-0 text-[10px]">
                  {tag}
                </Badge>
              ))}
            </div>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-right font-mono text-sm tabular-nums">{formatScore(row.capacity_score)}</TableCell>
      <TableCell className="text-right font-mono text-sm tabular-nums">
        {formatCount(row.runs)}
        {row.running > 0 ? <span className="ml-1 text-sky-300">+{row.running}</span> : null}
      </TableCell>
      <TableCell className="text-right">
        <span className={cn('font-mono text-sm tabular-nums', successRate >= 0.9 ? 'text-emerald-300' : successRate < 0.65 ? 'text-amber-300' : '')}>
          {formatPercent(successRate)}
        </span>
      </TableCell>
      <TableCell className="text-right">
        <div className="font-mono text-sm tabular-nums">{row.review_count ? formatScore(row.review_score) : '-'}</div>
        {row.review_count > 0 ? <div className="text-[10px] text-muted-foreground">{row.review_count} reviews</div> : null}
      </TableCell>
      <TableCell className="text-right font-mono text-sm tabular-nums">{formatDuration(row.avg_duration_ms)}</TableCell>
      <TableCell className="text-right font-mono text-sm tabular-nums">{formatUSD(row.cost_usd)}</TableCell>
      <TableCell>
        <Badge variant={row.available ? 'default' : 'secondary'} className="rounded-sm">
          {row.available ? <CheckCircle2 className="mr-1 h-3 w-3" /> : <XCircle className="mr-1 h-3 w-3" />}
          {row.available ? 'ready' : 'blocked'}
        </Badge>
        {!row.available && row.unavailable_reason ? (
          <div className="mt-1 max-w-48 truncate text-[11px] text-muted-foreground" title={row.unavailable_reason}>
            {row.unavailable_reason}
          </div>
        ) : null}
      </TableCell>
    </TableRow>
  )
}

function sortRows(rows: DelegationModelCapacity[]) {
  return [...rows].sort((a, b) => {
    if (a.available !== b.available) return a.available ? -1 : 1
    if (a.rank && b.rank && a.rank !== b.rank) return a.rank - b.rank
    if (b.capacity_score !== a.capacity_score) return b.capacity_score - a.capacity_score
    if (b.review_score !== a.review_score) return b.review_score - a.review_score
    return b.runs - a.runs
  })
}

function buildSummary(rows: DelegationModelCapacity[]) {
  return rows.reduce(
    (acc, row) => {
      acc.models += 1
      acc.runs += row.runs || 0
      if (row.available) acc.available += 1
      if (row.review_count > 0) acc.reviewed += 1
      return acc
    },
    { models: 0, runs: 0, available: 0, reviewed: 0 },
  )
}

function formatScore(n: number) {
  if (!Number.isFinite(n)) return '0'
  return n >= 10 ? n.toFixed(0) : n.toFixed(1)
}

function formatPercent(n: number) {
  if (!Number.isFinite(n) || n <= 0) return '-'
  return `${Math.round(n * 100)}%`
}

function formatDuration(ms: number) {
  if (!ms) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`
  return `${Math.round(ms / 60_000)}m`
}

function formatUSD(n: number) {
  if (!n) return '$0'
  return `$${n.toFixed(4)}`
}

function formatCount(n: number) {
  if (!n) return '0'
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}m`
  if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}
