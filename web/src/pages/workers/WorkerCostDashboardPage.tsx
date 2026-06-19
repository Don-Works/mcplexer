// WorkerCostDashboardPage — workspace-wide spend rollup. Sources
// /api/v1/workers/cost-aggregate?days=30 and renders:
//   - a "month progress" strip up top — visual reality-check on
//     elapsed-vs-spent
//   - hero numbers (MTD, window total, runs) augmented with a per-model
//     breakdown so the operator can see WHERE the money's going
//   - a sortable per-worker table with inline cap progress bars +
//     compact sparklines

import { useCallback, useId, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Bot, DollarSign, Loader2 } from 'lucide-react'
import {
  Area,
  AreaChart,
  ResponsiveContainer,
} from 'recharts'

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
import { useApi } from '@/hooks/use-api'
import {
  getWorkerCostAggregate,
  listWorkers,
  type WorkerCostAggregateRow,
  type WorkerSummary,
  type Worker,
  getWorker,
} from '@/api/workers'

type SortKey = 'name' | 'mtd' | 'runs'
type SortDir = 'asc' | 'desc'

export function WorkerCostDashboardPage() {
  const fetcher = useCallback(() => getWorkerCostAggregate({ days: 30 }), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  const workersFetcher = useCallback(() => listWorkers(), [])
  const { data: workers } = useApi(workersFetcher)
  const [sortKey, setSortKey] = useState<SortKey>('mtd')
  const [sortDir, setSortDir] = useState<SortDir>('desc')

  const rows = useMemo(() => {
    if (!data) return []
    return sortRows(data.workers, sortKey, sortDir)
  }, [data, sortKey, sortDir])

  // Per-model breakdown — derive client-side by joining the
  // cost-aggregate rows against the workers list (which carries the
  // model_provider per worker). Cheap; both lists are short.
  const modelBreakdown = useMemo(
    () => buildModelBreakdown(data?.workers ?? [], workers ?? []),
    [data, workers],
  )

  function toggleSort(k: SortKey) {
    if (sortKey === k) setSortDir(sortDir === 'asc' ? 'desc' : 'asc')
    else {
      setSortKey(k)
      setSortDir(k === 'name' ? 'asc' : 'desc')
    }
  }

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
            <DollarSign className="h-6 w-6" /> Worker cost
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Workspace-wide spend across every Worker. Updated when runs
            finalise — the daemon doesn't poll model APIs for billing.
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={refetch} disabled={loading}>
          {loading ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : null}
          Refresh
        </Button>
      </header>

      {error && (
        <div className="flex items-center justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          <span>{error}</span>
          <Button size="sm" variant="ghost" onClick={refetch}>Retry</Button>
        </div>
      )}

      {data && <MonthProgressStrip totalMTD={data.total_mtd_usd} workers={workers ?? []} />}

      {loading && !data ? (
        <SkeletonHero />
      ) : data ? (
        <HeroSummary
          totalMTD={data.total_mtd_usd}
          totalWindow={data.total_window_usd}
          totalRuns={data.total_runs_30d}
          days={data.days}
          breakdown={modelBreakdown}
        />
      ) : null}

      {data && data.workers.length === 0 ? (
        <EmptyState
          icon={<Bot className="h-10 w-10" />}
          title="No worker activity in the last 30 days"
          description="Once your Workers run, their spend shows up here."
          action={
            <Button asChild>
              <Link to="/workers">Manage workers</Link>
            </Button>
          }
        />
      ) : null}

      {rows.length > 0 ? (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>
                    <SortButton label="Worker" active={sortKey === 'name'} dir={sortDir} onClick={() => toggleSort('name')} />
                  </TableHead>
                  <TableHead className="text-right">
                    <SortButton label="MTD spend" active={sortKey === 'mtd'} dir={sortDir} onClick={() => toggleSort('mtd')} />
                  </TableHead>
                  <TableHead className="text-right">
                    <SortButton label="30-day runs" active={sortKey === 'runs'} dir={sortDir} onClick={() => toggleSort('runs')} />
                  </TableHead>
                  <TableHead className="w-48">Trend</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => (
                  <CostRow key={r.worker_id} row={r} workers={workers ?? []} />
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}
    </div>
  )
}

// MonthProgressStrip — single full-width thin bar showing month-elapsed
// vs MTD-spend. The track always represents the full month so the
// progress fill conveys "are we burning faster than time?"
function MonthProgressStrip({ totalMTD, workers }: { totalMTD: number; workers: WorkerSummary[] }) {
  const now = new Date()
  const start = new Date(now.getFullYear(), now.getMonth(), 1)
  const end = new Date(now.getFullYear(), now.getMonth() + 1, 1)
  const elapsedPct = Math.min(
    100,
    ((now.getTime() - start.getTime()) / (end.getTime() - start.getTime())) * 100,
  )
  // No monthly cap available on WorkerSummary; use MTD / elapsed * 100
  // as a soft "projected at this pace" target so the bar always has
  // meaning even without a budget set per-worker.
  void workers
  const projected = elapsedPct > 0 ? (totalMTD * 100) / elapsedPct : totalMTD
  const burnPct = projected > 0 ? Math.min(100, (totalMTD / projected) * 100) : 0
  return (
    <div className="space-y-1.5" data-testid="workers-month-progress">
      <div className="flex items-baseline justify-between text-[10px] uppercase tracking-wider text-muted-foreground/70">
        <span>Month progress</span>
        <span className="text-foreground font-mono normal-case">
          ${totalMTD.toFixed(4)} spent · {Math.round(elapsedPct)}% of month elapsed
        </span>
      </div>
      <div className="relative h-2 w-full overflow-hidden bg-muted">
        <div
          className="absolute inset-y-0 left-0 bg-muted-foreground/30"
          style={{ width: `${elapsedPct}%` }}
          aria-label="elapsed"
        />
        <div
          className="absolute inset-y-0 left-0 bg-gradient-to-r from-primary/60 to-primary/30"
          style={{ width: `${burnPct}%` }}
          aria-label="spent"
        />
      </div>
    </div>
  )
}

interface ModelBreakdownEntry { provider: string; cost: number }

function buildModelBreakdown(
  agg: WorkerCostAggregateRow[],
  workers: WorkerSummary[],
): ModelBreakdownEntry[] {
  const byID = new Map(workers.map((w) => [w.id, w.model_provider || 'unknown']))
  const totals = new Map<string, number>()
  for (const row of agg) {
    const provider = byID.get(row.worker_id) ?? 'unknown'
    totals.set(provider, (totals.get(provider) ?? 0) + row.month_to_date_usd)
  }
  return Array.from(totals.entries())
    .map(([provider, cost]) => ({ provider, cost }))
    .filter((e) => e.cost > 0)
    .sort((a, b) => b.cost - a.cost)
}

function HeroSummary({
  totalMTD,
  totalWindow,
  totalRuns,
  days,
  breakdown,
}: {
  totalMTD: number
  totalWindow: number
  totalRuns: number
  days: number
  breakdown: ModelBreakdownEntry[]
}) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
      <Card>
        <CardHeader className="pb-1">
          <CardTitle className="text-xs uppercase tracking-wide text-muted-foreground">
            Workspace MTD
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="font-mono text-3xl font-bold text-foreground">
            ${totalMTD.toFixed(4)}
          </div>
          {breakdown.length > 0 && (
            <div className="mt-2 space-y-1">
              <ModelStackBar breakdown={breakdown} total={totalMTD} />
              <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground/80">
                {breakdown.map((b) => (
                  <span key={b.provider} className="font-mono">
                    ${b.cost.toFixed(2)} {b.provider}
                  </span>
                ))}
              </div>
            </div>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-1">
          <CardTitle className="text-xs uppercase tracking-wide text-muted-foreground">
            Last {days} days
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="font-mono text-3xl font-bold text-foreground">
            ${totalWindow.toFixed(4)}
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-1">
          <CardTitle className="text-xs uppercase tracking-wide text-muted-foreground">
            Runs ({days}d)
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="font-mono text-3xl font-bold text-foreground">{totalRuns}</div>
        </CardContent>
      </Card>
    </div>
  )
}

// ModelStackBar — single thin stacked-bar coloured by provider so the
// MTD breakdown can be eyeballed in one glance.
function ModelStackBar({ breakdown, total }: { breakdown: ModelBreakdownEntry[]; total: number }) {
  if (total <= 0) return null
  const COLORS: Record<string, string> = {
    anthropic: 'bg-amber-500/70',
    openai: 'bg-emerald-500/70',
    openai_compat: 'bg-purple-500/70',
    claude_cli: 'bg-sky-500/70',
    opencode_cli: 'bg-violet-500/70',
    grok_cli: 'bg-teal-500/70',
    mimo_cli: 'bg-rose-500/70',
    unknown: 'bg-muted-foreground/40',
  }
  return (
    <div className="flex h-1.5 w-full overflow-hidden bg-muted">
      {breakdown.map((b) => (
        <span
          key={b.provider}
          className={COLORS[b.provider] ?? 'bg-muted-foreground/40'}
          style={{ width: `${(b.cost / total) * 100}%` }}
          title={`${b.provider}: $${b.cost.toFixed(4)}`}
        />
      ))}
    </div>
  )
}

function SkeletonHero() {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
      {Array.from({ length: 3 }).map((_, i) => (
        <Card key={i}>
          <CardContent className="p-4">
            <div className="mb-2 h-3 w-24 animate-pulse bg-muted" />
            <div className="h-8 w-32 animate-pulse bg-muted" />
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

function SortButton({
  label,
  active,
  dir,
  onClick,
}: {
  label: string
  active: boolean
  dir: SortDir
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-1 text-xs font-medium uppercase tracking-wide ${
        active ? 'text-foreground' : 'text-muted-foreground'
      }`}
    >
      {label}
      {active ? <span aria-hidden>{dir === 'asc' ? '▲' : '▼'}</span> : null}
    </button>
  )
}

function CostRow({ row, workers }: { row: WorkerCostAggregateRow; workers: WorkerSummary[] }) {
  // WorkerSummary doesn't carry max_monthly_cost_usd; fetch the full
  // Worker record lazily for the cap progress bar. We only fire one
  // GET per row, and only when the row is in view (no IntersectionObserver
  // here for simplicity — at typical worker counts this is sub-noise).
  const [cap, setCap] = useState<number | null>(null)
  // Skip fetch if the worker isn't in the workers list (e.g. recently
  // deleted but still has cost history).
  const found = workers.find((w) => w.id === row.worker_id)
  if (found && cap === null) {
    void getWorker(row.worker_id)
      .then((res: { worker: Worker }) => setCap(res.worker.max_monthly_cost_usd))
      .catch(() => setCap(0))
  }

  return (
    <TableRow>
      <TableCell className="font-medium">
        <Link to={`/workers/${row.worker_id}`} className="hover:underline">
          {row.worker_name}
        </Link>
      </TableCell>
      <TableCell className="text-right font-mono">
        <div>${row.month_to_date_usd.toFixed(4)}</div>
        {cap !== null && cap > 0 && (
          <CapProgressBar mtd={row.month_to_date_usd} cap={cap} />
        )}
      </TableCell>
      <TableCell className="text-right font-mono text-muted-foreground">
        {row.run_count_30d}
      </TableCell>
      <TableCell className="w-48">
        <RowSparkline data={row.daily_costs} />
      </TableCell>
    </TableRow>
  )
}

function CapProgressBar({ mtd, cap }: { mtd: number; cap: number }) {
  const pct = Math.min(100, (mtd / cap) * 100)
  const color =
    pct >= 90 ? 'bg-destructive' : pct >= 75 ? 'bg-amber-500' : 'bg-emerald-500/70'
  return (
    <div className="mt-1 flex items-center justify-end gap-2">
      <div className="h-1 w-20 overflow-hidden bg-muted">
        <div className={color} style={{ width: `${pct}%`, height: '100%' }} />
      </div>
      <span className="font-mono text-[9px] text-muted-foreground/70">
        / ${cap.toFixed(0)}
      </span>
    </div>
  )
}

function RowSparkline({ data }: { data: WorkerCostAggregateRow['daily_costs'] }) {
  const gradientID = useId().replace(/[^A-Za-z0-9_-]/g, '-') + '-row-cost-fill'
  const hasAny = data.some((d) => d.cost_usd > 0)
  if (!hasAny) return <span className="text-xs text-muted-foreground/60">no spend</span>
  return (
    <div className="h-10">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 2, right: 4, bottom: 2, left: 4 }}>
          <defs>
            <linearGradient id={gradientID} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="currentColor" stopOpacity={0.35} />
              <stop offset="100%" stopColor="currentColor" stopOpacity={0} />
            </linearGradient>
          </defs>
          <Area
            type="monotone"
            dataKey="cost_usd"
            stroke="currentColor"
            strokeWidth={1.5}
            fill={`url(#${gradientID})`}
            className="text-primary"
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

function sortRows(
  rows: WorkerCostAggregateRow[],
  key: SortKey,
  dir: SortDir,
): WorkerCostAggregateRow[] {
  const copy = [...rows]
  copy.sort((a, b) => {
    let cmp = 0
    if (key === 'name') cmp = a.worker_name.localeCompare(b.worker_name)
    else if (key === 'mtd') cmp = a.month_to_date_usd - b.month_to_date_usd
    else cmp = a.run_count_30d - b.run_count_30d
    return dir === 'asc' ? cmp : -cmp
  })
  return copy
}
