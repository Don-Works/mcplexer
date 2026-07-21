import { useCallback, useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  Bot,
  CheckCircle2,
  Database,
  Gauge,
  Loader2,
  RefreshCw,
  Route as RouteIcon,
  Search,
  SlidersHorizontal,
  Star,
  XCircle,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { listWorkspaces } from '@/api/client'
import type { Workspace } from '@/api/types'
import {
  listDelegationModelCapacity,
  listDelegations,
  type DelegationContext,
  type DelegationModelCapacity,
} from '@/api/workers'
import { useApi } from '@/hooks/use-api'
import {
  ANALYSIS_PERIODS,
  dedupeCapacityRows,
  filterDelegationsByPeriod,
  formatCost,
  formatDuration,
  formatTokens,
  periodLabel,
  rankDelegationModels,
  type AnalysisPeriod,
  type CapacityRow,
  type ModelRankRow,
} from './model-rank-utils'

const TASK_KIND_SHORTCUTS = ['coding', 'review', 'architecture', 'research', 'tool_calling']

export function ModelRanksPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [modelQuery, setModelQuery] = useState('')

  const workspaceID = searchParams.get('workspace_id') || ''
  const taskKind = searchParams.get('task_kind') || ''
  const period = parseAnalysisPeriod(searchParams.get('period'))

  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces, loading: workspacesLoading } = useApi(workspacesFetcher)

  const capacityFetcher = useCallback(
    () =>
      listDelegationModelCapacity({
        workspaceId: workspaceID || undefined,
        taskKind: taskKind.trim() || undefined,
        limit: 200,
      }),
    [workspaceID, taskKind],
  )
  const {
    data: capacityRows,
    loading: capacityLoading,
    error: capacityError,
    refetch: refetchCapacity,
  } = useApi(capacityFetcher)

  const delegationsFetcher = useCallback(
    () => listDelegations({ workspaceId: workspaceID || undefined, limit: 500 }),
    [workspaceID],
  )
  const {
    data: delegations,
    loading: delegationsLoading,
    error: delegationsError,
    refetch: refetchDelegations,
  } = useApi(delegationsFetcher)

  const scopedDelegations = useMemo(
    () => filterDelegationsByTaskKind(delegations ?? [], taskKind),
    [delegations, taskKind],
  )
  const periodDelegations = useMemo(
    () => filterDelegationsByPeriod(scopedDelegations, period),
    [scopedDelegations, period],
  )
  const reviewedRank = useMemo(() => rankDelegationModels(periodDelegations), [periodDelegations])
  const routerRows = useMemo(() => dedupeCapacityRows(capacityRows ?? []), [capacityRows])
  const taskKindOptions = useMemo(
    () => buildTaskKindOptions(delegations ?? [], taskKind),
    [delegations, taskKind],
  )
  const filteredRouterRows = useMemo(
    () => filterCapacityRows(routerRows, modelQuery),
    [routerRows, modelQuery],
  )
  const filteredReviewedRank = useMemo(
    () => filterReviewedRows(reviewedRank, modelQuery),
    [reviewedRank, modelQuery],
  )
  const summary = useMemo(
    () => buildSummary(routerRows, reviewedRank),
    [routerRows, reviewedRank],
  )

  function updateParam(key: 'workspace_id' | 'task_kind' | 'period', value: string) {
    const next = new URLSearchParams(searchParams)
    if (value) next.set(key, value)
    else next.delete(key)
    setSearchParams(next)
  }

  function refreshAll() {
    refetchCapacity()
    refetchDelegations()
  }

  const loading = capacityLoading || delegationsLoading
  const workspaceName = workspaceLabel(workspaces ?? [], workspaceID)

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <Button variant="ghost" size="sm" className="mb-2 rounded-none px-0" asChild>
            <Link to="/delegations">
              <ArrowLeft className="mr-1.5 h-4 w-4" />
              Delegations
            </Link>
          </Button>
          <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
            <Gauge className="h-6 w-6" /> Model ranks
          </h1>
          <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
            Current router capacity beside reviewed delegation performance for the selected workspace and task kind.
          </p>
        </div>
        <Button variant="outline" size="sm" className="rounded-none" onClick={refreshAll} disabled={loading}>
          {loading ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : <RefreshCw className="mr-1.5 h-4 w-4" />}
          Refresh
        </Button>
      </header>

      <FilterBand
        workspaces={workspaces ?? []}
        workspacesLoading={workspacesLoading}
        workspaceID={workspaceID}
        taskKind={taskKind}
        taskKindOptions={taskKindOptions}
        period={period}
        modelQuery={modelQuery}
        onWorkspaceChange={(value) => updateParam('workspace_id', value)}
        onTaskKindChange={(value) => updateParam('task_kind', value)}
        onPeriodChange={(value) => updateParam('period', value)}
        onModelQueryChange={setModelQuery}
      />

      {(capacityError || delegationsError) && (
        <section className="flex items-start justify-between gap-3 border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          <div className="space-y-1">
            {capacityError && <div>Router rank failed: {capacityError}</div>}
            {delegationsError && <div>Reviewed rank failed: {delegationsError}</div>}
          </div>
          <Button size="sm" variant="ghost" className="rounded-none" onClick={refreshAll}>
            Retry
          </Button>
        </section>
      )}

      <SummaryBand summary={summary} workspaceName={workspaceName} period={period} taskKind={taskKind} />

      <RankBasisBand />

      <RouterCapacityTable rows={filteredRouterRows} loading={capacityLoading && !capacityRows} />

      <ReviewedPerformanceTable rows={filteredReviewedRank} loading={delegationsLoading && !delegations} period={period} />
    </div>
  )
}

function FilterBand({
  workspaces,
  workspacesLoading,
  workspaceID,
  taskKind,
  taskKindOptions,
  period,
  modelQuery,
  onWorkspaceChange,
  onTaskKindChange,
  onPeriodChange,
  onModelQueryChange,
}: {
  workspaces: Workspace[]
  workspacesLoading: boolean
  workspaceID: string
  taskKind: string
  taskKindOptions: string[]
  period: AnalysisPeriod
  modelQuery: string
  onWorkspaceChange: (value: string) => void
  onTaskKindChange: (value: string) => void
  onPeriodChange: (value: string) => void
  onModelQueryChange: (value: string) => void
}) {
  return (
    <section className="border border-border bg-card/25 p-3">
      <div className="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        <SlidersHorizontal className="h-3.5 w-3.5" />
        Filters
      </div>
      <div className="mt-3 grid gap-3 xl:grid-cols-[minmax(14rem,1.1fr)_minmax(14rem,1.3fr)_8rem_minmax(14rem,1fr)]">
        <label className="space-y-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Workspace</span>
          <select
            className="h-9 w-full rounded-none border border-input bg-background px-3 text-sm outline-none focus:border-primary"
            value={workspaceID}
            onChange={(event) => onWorkspaceChange(event.target.value)}
            disabled={workspacesLoading}
          >
            <option value="">All workspaces</option>
            {workspaces.map((workspace) => (
              <option key={workspace.id} value={workspace.id}>
                {workspace.name || workspace.id}
              </option>
            ))}
          </select>
        </label>

        <label className="space-y-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Task kind</span>
          <div className="flex min-w-0 gap-2">
            <Input
              className="h-9 rounded-none"
              value={taskKind}
              placeholder="All task kinds"
              onChange={(event) => onTaskKindChange(event.target.value)}
            />
            {taskKind && (
              <Button variant="outline" size="sm" className="h-9 rounded-none" onClick={() => onTaskKindChange('')}>
                Clear
              </Button>
            )}
          </div>
        </label>

        <label className="space-y-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Review window</span>
          <select
            className="h-9 w-full rounded-none border border-input bg-background px-3 text-sm outline-none focus:border-primary"
            value={period}
            onChange={(event) => onPeriodChange(event.target.value)}
          >
            {ANALYSIS_PERIODS.map((item) => (
              <option key={item.key} value={item.key}>
                {item.label}
              </option>
            ))}
          </select>
        </label>

        <label className="space-y-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Search models</span>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input
              className="h-9 rounded-none pl-8"
              value={modelQuery}
              placeholder="Provider, model, tag"
              onChange={(event) => onModelQueryChange(event.target.value)}
            />
          </div>
        </label>
      </div>

      {taskKindOptions.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1.5">
          <Button
            variant={!taskKind ? 'secondary' : 'outline'}
            size="sm"
            className="h-7 rounded-none px-2 text-[11px]"
            onClick={() => onTaskKindChange('')}
          >
            All
          </Button>
          {taskKindOptions.map((option) => (
            <Button
              key={option}
              variant={taskKind === option ? 'secondary' : 'outline'}
              size="sm"
              className="h-7 rounded-none px-2 font-mono text-[11px]"
              onClick={() => onTaskKindChange(option)}
            >
              {option}
            </Button>
          ))}
        </div>
      )}
    </section>
  )
}

function SummaryBand({
  summary,
  workspaceName,
  period,
  taskKind,
}: {
  summary: ReturnType<typeof buildSummary>
  workspaceName: string
  period: AnalysisPeriod
  taskKind: string
}) {
  return (
    <section className="grid gap-px border border-border bg-border md:grid-cols-5">
      <SummaryCell
        icon={<RouteIcon className="h-4 w-4" />}
        label="Router candidates"
        value={`${summary.routerModels}`}
        detail={`${summary.availableModels} available`}
      />
      <SummaryCell
        icon={<Star className="h-4 w-4" />}
        label="Reviewed models"
        value={`${summary.reviewedModels}`}
        detail={`${summary.reviewCount} reviews`}
      />
      <SummaryCell
        icon={<CheckCircle2 className="h-4 w-4" />}
        label="Known reliability"
        value={`${summary.knownSuccessRate}%`}
        detail={summary.accountingGaps ? `${summary.accountingGaps} usage gaps` : 'usage known'}
      />
      <SummaryCell
        icon={<Activity className="h-4 w-4" />}
        label="Live workers"
        value={`${summary.running}`}
        detail={summary.operationalFailures ? `${summary.operationalFailures} launch failures` : 'no launch failures'}
      />
      <SummaryCell
        icon={<Database className="h-4 w-4" />}
        label={workspaceName}
        value={periodLabel(period)}
        detail={taskKind ? `task: ${taskKind}` : 'all task kinds'}
      />
    </section>
  )
}

function SummaryCell({
  icon,
  label,
  value,
  detail,
}: {
  icon: ReactNode
  label: string
  value: string
  detail: string
}) {
  return (
    <div className="min-w-0 bg-background p-3">
      <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground">
        {icon}
        <span className="truncate">{label}</span>
      </div>
      <div className="mt-2 font-mono text-xl font-semibold tabular-nums">{value}</div>
      <div className="mt-1 truncate text-[11px] text-muted-foreground">{detail}</div>
    </div>
  )
}

function RankBasisBand() {
  return (
    <section className="border border-border bg-card/20 p-3">
      <div className="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        <Database className="h-3.5 w-3.5" />
        Rank basis
      </div>
      <div className="mt-3 grid gap-2 text-sm text-muted-foreground lg:grid-cols-3">
        <div>
          <span className="font-medium text-foreground">Router rank</span> uses registered candidates, availability, capability fit, reviewed score, reliability, cost, and duration.
        </div>
        <div>
          <span className="font-medium text-foreground">Reviewed rank</span> uses completed delegation reviews in the selected window, then review volume, success rate, cost, and duration.
        </div>
        <div>
          <span className="font-medium text-foreground">Accounting flags</span> separate missing usage telemetry from genuine failure so zero-cost rows do not look free by accident.
        </div>
      </div>
    </section>
  )
}

function RouterCapacityTable({ rows, loading }: { rows: CapacityRow[]; loading: boolean }) {
  return (
    <section className="border border-border bg-card/20">
      <TableTitle
        icon={<RouteIcon className="h-4 w-4" />}
        title="Current router rank"
        detail="What capacity mode can select now."
        count={rows.length}
        loading={loading}
      />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-12">Rank</TableHead>
            <TableHead>Model</TableHead>
            <TableHead className="w-32">Status</TableHead>
            <TableHead className="w-36">Capacity</TableHead>
            <TableHead className="w-32">Review</TableHead>
            <TableHead className="w-36">Reliability</TableHead>
            <TableHead className="w-24 text-right">Runs</TableHead>
            <TableHead className="w-28 text-right">Cost</TableHead>
            <TableHead className="w-24 text-right">Latency</TableHead>
            <TableHead className="min-w-52">Signals</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            <LoadingRow colSpan={10} label="Loading router rank" />
          ) : rows.length === 0 ? (
            <EmptyRow colSpan={10} label="No model capacity rows match the current filters." />
          ) : (
            rows.map((row) => <RouterCapacityRow key={`${row.model_profile_id || row.model_key}-${row.rank}`} row={row} />)
          )}
        </TableBody>
      </Table>
    </section>
  )
}

function RouterCapacityRow({ row }: { row: CapacityRow }) {
  const accountingKnown = row.accounting_known !== false
  return (
    <TableRow>
      <TableCell className="font-mono text-xs text-muted-foreground">#{row.rank}</TableCell>
      <TableCell>
        <ModelIdentity
          provider={row.model_provider}
          modelID={row.model_id}
          modelKey={row.model_key}
          label={row.label}
          tags={row.capability_tags}
        />
      </TableCell>
      <TableCell>
        <AvailabilityBadge row={row} />
      </TableCell>
      <TableCell>
        <ScoreWithBar value={row.capacity_score} label={Math.round(row.capacity_score).toString()} />
      </TableCell>
      <TableCell>
        {row.review_count ? (
          <ScoreWithBar value={row.review_score} label={`${Math.round(row.review_score)} / ${row.review_count}`} />
        ) : (
          <span className="text-xs text-muted-foreground">new</span>
        )}
      </TableCell>
      <TableCell>
        <div className="space-y-1">
          <div className="font-mono text-xs tabular-nums">
            {row.runs ? (accountingKnown ? formatPercent(row.success_rate) : 'usage gap') : 'new'}
          </div>
          <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
            {row.success} success, {row.failure} fail
          </div>
        </div>
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">
        <div>{row.runs}</div>
        {row.running > 0 && <div className="text-sky-300">{row.running} running</div>}
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">
        {accountingKnown || !row.runs ? formatCost(row.cost_usd) : 'unknown'}
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">{formatDuration(row.avg_duration_ms)}</TableCell>
      <TableCell>
        <CapacitySignals row={row} />
      </TableCell>
    </TableRow>
  )
}

function ReviewedPerformanceTable({
  rows,
  loading,
  period,
}: {
  rows: ModelRankRow[]
  loading: boolean
  period: AnalysisPeriod
}) {
  return (
    <section className="border border-border bg-card/20">
      <TableTitle
        icon={<Star className="h-4 w-4" />}
        title="Reviewed performance rank"
        detail={`Completed delegation reviews in ${periodLabel(period)}.`}
        count={rows.length}
        loading={loading}
      />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-12">Rank</TableHead>
            <TableHead>Model</TableHead>
            <TableHead className="w-36">Score</TableHead>
            <TableHead className="w-24 text-right">Reviews</TableHead>
            <TableHead className="w-32">Success</TableHead>
            <TableHead className="w-24 text-right">Runs</TableHead>
            <TableHead className="w-28 text-right">Tokens</TableHead>
            <TableHead className="w-28 text-right">Cost</TableHead>
            <TableHead className="w-24 text-right">Latency</TableHead>
            <TableHead className="min-w-56">Capability scores</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            <LoadingRow colSpan={10} label="Loading reviewed rank" />
          ) : rows.length === 0 ? (
            <EmptyRow colSpan={10} label="No reviewed delegation data matches the current filters." />
          ) : (
            rows.map((row, index) => <ReviewedPerformanceRow key={row.modelKey} row={row} rank={index + 1} />)
          )}
        </TableBody>
      </Table>
    </section>
  )
}

function ReviewedPerformanceRow({ row, rank }: { row: ModelRankRow; rank: number }) {
  return (
    <TableRow>
      <TableCell className="font-mono text-xs text-muted-foreground">#{rank}</TableCell>
      <TableCell>
        <ModelIdentity
          provider={row.modelProvider}
          modelID={row.modelID}
          modelKey={row.modelKey}
        />
      </TableCell>
      <TableCell>
        {row.reviewCount ? (
          <ScoreWithBar value={row.avgScore} label={`${Math.round(row.avgScore)}`} />
        ) : (
          <span className="text-xs text-muted-foreground">new</span>
        )}
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">{row.reviewCount}</TableCell>
      <TableCell>
        <div className="font-mono text-xs tabular-nums">{row.runs ? formatPercent(row.successRate) : 'new'}</div>
        {row.failure > 0 && <div className="text-[10px] uppercase tracking-wider text-red-300">{row.failure} fail</div>}
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">
        <div>{row.runs}</div>
        {row.running > 0 && <div className="text-sky-300">{row.running} running</div>}
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">{formatTokens(row.totalTokens)}</TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">
        {row.costKnown || !row.runs ? formatCost(row.costUSD) : 'unknown'}
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">{formatDuration(row.avgDurationMS)}</TableCell>
      <TableCell>
        <CapabilityScores scores={row.capabilityScores} />
      </TableCell>
    </TableRow>
  )
}

function TableTitle({
  icon,
  title,
  detail,
  count,
  loading,
}: {
  icon: ReactNode
  title: string
  detail: string
  count: number
  loading: boolean
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2.5">
      <div>
        <h2 className="flex items-center gap-1.5 text-sm font-semibold">
          {icon}
          {title}
        </h2>
        <p className="mt-0.5 text-[11px] text-muted-foreground">{detail}</p>
      </div>
      <div className="flex items-center gap-2">
        {loading && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
        <Badge variant="outline" className="text-[10px] uppercase">
          {count} rows
        </Badge>
      </div>
    </div>
  )
}

function ModelIdentity({
  provider,
  modelID,
  modelKey,
  label,
  tags,
}: {
  provider: string
  modelID: string
  modelKey: string
  label?: string
  tags?: string[]
}) {
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
        <span className="truncate text-sm font-medium">{modelID || modelKey}</span>
        {label && (
          <Badge variant="outline" className="max-w-48 truncate text-[10px] uppercase">
            {label}
          </Badge>
        )}
      </div>
      <div className="truncate font-mono text-[11px] text-muted-foreground">{provider || 'provider'}</div>
      {tags && tags.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-1">
          {tags.slice(0, 4).map((tag) => (
            <Badge key={tag} variant="outline" className="font-mono text-[10px] text-muted-foreground">
              {tag}
            </Badge>
          ))}
          {tags.length > 4 && (
            <Badge variant="outline" className="font-mono text-[10px] text-muted-foreground">
              +{tags.length - 4}
            </Badge>
          )}
        </div>
      )}
    </div>
  )
}

function AvailabilityBadge({ row }: { row: CapacityRow }) {
  if (row.available) {
    return (
      <Badge variant="outline" className="border-emerald-500/40 text-emerald-300">
        <CheckCircle2 className="mr-1 h-3 w-3" />
        available
      </Badge>
    )
  }
  return (
    <Badge variant="outline" className="border-red-500/40 text-red-300">
      <XCircle className="mr-1 h-3 w-3" />
      off
    </Badge>
  )
}

function CapacitySignals({ row }: { row: CapacityRow }) {
  const signals: ReactNode[] = []
  if (row.unavailable_reason) {
    signals.push(
      <Badge key="unavailable" variant="outline" className="max-w-72 truncate border-red-500/40 text-red-300">
        <AlertTriangle className="mr-1 h-3 w-3" />
        {row.unavailable_reason}
      </Badge>,
    )
  }
  if (row.accounting_known === false && row.runs > 0) {
    signals.push(
      <Badge key="accounting" variant="outline" className="border-amber-500/40 text-amber-300">
        <Database className="mr-1 h-3 w-3" />
        usage missing
      </Badge>,
    )
  }
  if ((row.operational_failures || 0) > 0) {
    signals.push(
      <Badge key="ops" variant="outline" className="border-red-500/40 text-red-300">
        <AlertTriangle className="mr-1 h-3 w-3" />
        {row.operational_failures} launch fail
      </Badge>,
    )
  }
  if (row.running > 0) {
    signals.push(
      <Badge key="running" variant="outline" className="border-sky-500/40 text-sky-300">
        <Activity className="mr-1 h-3 w-3" />
        {row.running} live
      </Badge>,
    )
  }
  if (row.duplicate_count && row.duplicate_count > 1) {
    signals.push(
      <Badge key="routes" variant="outline" className="text-[10px] uppercase">
        {row.duplicate_count} routes
      </Badge>,
    )
  }
  if (!signals.length) {
    return <span className="text-xs text-muted-foreground">clear</span>
  }
  return <div className="flex flex-wrap gap-1">{signals}</div>
}

function ScoreWithBar({ value, label }: { value: number; label: string }) {
  const pct = clamp(value, 0, 100)
  return (
    <div className="space-y-1">
      <div className="font-mono text-xs tabular-nums">{label}</div>
      <div className="h-1.5 w-24 bg-muted">
        <div className="h-full bg-primary/70" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function CapabilityScores({ scores }: { scores: Record<string, number> }) {
  const rows = Object.entries(scores)
    .filter(([, value]) => Number.isFinite(value))
    .sort((a, b) => b[1] - a[1])
    .slice(0, 4)
  if (!rows.length) return <span className="text-xs text-muted-foreground">none recorded</span>
  return (
    <div className="flex flex-wrap gap-1">
      {rows.map(([key, value]) => (
        <Badge key={key} variant="outline" className="font-mono text-[10px]">
          {key}: {Math.round(value)}
        </Badge>
      ))}
    </div>
  )
}

function LoadingRow({ colSpan, label }: { colSpan: number; label: string }) {
  return (
    <TableRow>
      <TableCell colSpan={colSpan} className="py-8 text-center text-sm text-muted-foreground">
        <Loader2 className="mr-2 inline h-4 w-4 animate-spin" />
        {label}
      </TableCell>
    </TableRow>
  )
}

function EmptyRow({ colSpan, label }: { colSpan: number; label: string }) {
  return (
    <TableRow>
      <TableCell colSpan={colSpan} className="py-8 text-center text-sm text-muted-foreground">
        <Bot className="mr-2 inline h-4 w-4" />
        {label}
      </TableCell>
    </TableRow>
  )
}

function buildSummary(routerRows: CapacityRow[], reviewedRank: ModelRankRow[]) {
  const knownRows = routerRows.filter((row) => row.runs > 0 && row.accounting_known !== false)
  const success = knownRows.reduce((sum, row) => sum + row.success, 0)
  const runs = knownRows.reduce((sum, row) => sum + row.runs, 0)
  return {
    routerModels: routerRows.length,
    availableModels: routerRows.filter((row) => row.available).length,
    reviewedModels: reviewedRank.filter((row) => row.reviewCount > 0).length,
    reviewCount: reviewedRank.reduce((sum, row) => sum + row.reviewCount, 0),
    running: routerRows.reduce((sum, row) => sum + row.running, 0),
    accountingGaps: routerRows.filter((row) => row.runs > 0 && row.accounting_known === false).length,
    operationalFailures: routerRows.reduce((sum, row) => sum + (row.operational_failures || 0), 0),
    knownSuccessRate: runs ? Math.round((success / runs) * 100) : 0,
  }
}

function filterDelegationsByTaskKind(rows: DelegationContext[], taskKind: string) {
  const key = taskKind.trim().toLowerCase()
  if (!key) return rows
  return rows.filter((row) => (row.task_kind || row.review?.task_kind || '').trim().toLowerCase() === key)
}

function buildTaskKindOptions(rows: DelegationContext[], current: string) {
  const values = new Set(TASK_KIND_SHORTCUTS)
  for (const row of rows) {
    const key = (row.task_kind || row.review?.task_kind || '').trim()
    if (key) values.add(key)
  }
  if (current.trim()) values.add(current.trim())
  return Array.from(values).sort((a, b) => a.localeCompare(b))
}

function filterCapacityRows(rows: CapacityRow[], query: string) {
  const q = query.trim().toLowerCase()
  if (!q) return rows
  return rows.filter((row) => capacitySearchText(row).includes(q))
}

function filterReviewedRows(rows: ModelRankRow[], query: string) {
  const q = query.trim().toLowerCase()
  if (!q) return rows
  return rows.filter((row) => reviewedSearchText(row).includes(q))
}

function capacitySearchText(row: DelegationModelCapacity) {
  return [
    row.label,
    row.model_provider,
    row.model_id,
    row.model_key,
    row.unavailable_reason,
    ...(row.capability_tags || []),
    ...(row.input_modalities || []),
    ...(row.output_modalities || []),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

function reviewedSearchText(row: ModelRankRow) {
  return [
    row.modelProvider,
    row.modelID,
    row.modelKey,
    ...Object.keys(row.capabilityScores || {}),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

function workspaceLabel(workspaces: Workspace[], workspaceID: string) {
  if (!workspaceID) return 'All workspaces'
  const workspace = workspaces.find((row) => row.id === workspaceID)
  return workspace?.name || workspaceID
}

function parseAnalysisPeriod(value: string | null): AnalysisPeriod {
  const valid = ANALYSIS_PERIODS.some((row) => row.key === value)
  return valid ? (value as AnalysisPeriod) : '12h'
}

function formatPercent(value: number) {
  if (!Number.isFinite(value)) return '0%'
  return `${Math.round(value * 100)}%`
}

function clamp(value: number, min: number, max: number) {
  if (!Number.isFinite(value)) return min
  return Math.max(min, Math.min(max, value))
}
