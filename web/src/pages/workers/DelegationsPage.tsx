import { useCallback, useEffect, useMemo, useState, type ComponentProps, type MouseEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import {
  Activity,
  AlertTriangle,
  Bot,
  ChevronDown,
  ChevronRight,
  ChevronsUpDown,
  CheckCircle2,
  Clock,
  Gauge,
  GitBranch,
  Loader2,
  Play,
  Plus,
  Search,
  Square,
  Star,
} from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import {
  getSettings,
  listAuthScopes,
  listModelProfiles,
  listWorkspaces,
  updateSettings,
} from '@/api/client'
import type { ModelProfile } from '@/api/client'
import type { Settings } from '@/api/types'
import {
  cancelWorkerRun,
  createDelegation,
  listDelegationModelCapacity,
  listDelegations,
  reviewDelegation,
  type CreateDelegationInput,
  type DelegationContext,
  type DelegationModelCapacity,
  type DelegationModelCandidate,
  type DelegationModelStat,
  type DelegationWorkerContext,
  type ModelSelectionMode,
  type ModelProvider,
} from '@/api/workers'
import { useApi } from '@/hooks/use-api'
import { subscribeEvent } from '@/hooks/use-event-stream'
import { Markdown } from '@/lib/markdown'
import { cn } from '@/lib/utils'
import { WorkerLiveTail } from './WorkerLiveTail'
import { relativeTime, statusBadgeClass, summariseModel } from './worker-utils'

type FormState = {
  workspaceID: string
  objective: string
  handoff: string
  name: string
  taskID: string
  taskKind: string
  workerMode: 'execute' | 'review'
  reviewRequired: boolean
  modelSelectionMode: ModelSelectionMode
  modelCandidateIndex: number
  modelCandidatesJSON: string
  modelProfileID: string
  modelProvider: ModelProvider
  modelID: string
  modelEndpointURL: string
  secretScopeID: string
  parallelism: number
  parentModel: string
  parentInputTokens: number
  parentOutputTokens: number
  parentCostUSD: number
  baselineTokensEstimate: number
  baselineCostUSD: number
  maxToolCalls: number
  maxWallClockSeconds: number
  maxOutputTokens: number
}

const defaultForm: FormState = {
  workspaceID: '',
  objective: '',
  handoff: '',
  name: '',
  taskID: '',
  taskKind: '',
  workerMode: 'execute',
  reviewRequired: true,
  modelSelectionMode: 'single',
  modelCandidateIndex: 0,
  modelCandidatesJSON: '',
  modelProfileID: '',
  modelProvider: 'opencode_cli',
  modelID: 'minimax/MiniMax-M3',
  modelEndpointURL: '',
  secretScopeID: '',
  parallelism: 1,
  parentModel: '',
  parentInputTokens: 0,
  parentOutputTokens: 0,
  parentCostUSD: 0,
  baselineTokensEstimate: 0,
  baselineCostUSD: 0,
  maxToolCalls: 80,
  maxWallClockSeconds: 3600,
  maxOutputTokens: 0,
}

const providers: Array<{ value: ModelProvider; label: string }> = [
  { value: 'opencode_cli', label: 'OpenCode CLI' },
  { value: 'mimo_cli', label: 'Xiaomi MiMo CLI' },
  { value: 'claude_cli', label: 'Claude CLI' },
  { value: 'grok_cli', label: 'xAI Grok CLI' },
  { value: 'gemini_cli', label: 'Google Gemini CLI' },
  { value: 'codex_cli', label: 'OpenAI Codex CLI' },
  { value: 'pi_cli', label: 'Pi CLI' },
  { value: 'openai_compat', label: 'OpenAI compatible' },
  { value: 'anthropic', label: 'Anthropic API' },
  { value: 'openai', label: 'OpenAI API' },
]

type DelegationFilter =
  | 'all'
  | 'attention'
  | 'needs_review'
  | 'success'
  | 'failure'
  | 'interrupted'
  | 'cancelled'
  | 'reviewed'
type AnalysisPeriod = '1h' | '12h' | '24h' | '7d' | 'all'

const ANALYSIS_PERIODS: Array<{ key: AnalysisPeriod; label: string; ms?: number }> = [
  { key: '1h', label: '1h', ms: 60 * 60 * 1000 },
  { key: '12h', label: '12h', ms: 12 * 60 * 60 * 1000 },
  { key: '24h', label: '24h', ms: 24 * 60 * 60 * 1000 },
  { key: '7d', label: '7d', ms: 7 * 24 * 60 * 60 * 1000 },
  { key: 'all', label: 'All' },
]

export function DelegationsPage() {
  const [form, setForm] = useState<FormState>(defaultForm)
  const [submitting, setSubmitting] = useState(false)
  const [launchOpen, setLaunchOpen] = useState(false)
  const [historyFilter, setHistoryFilter] = useState<DelegationFilter>('all')
  const [analysisPeriod, setAnalysisPeriod] = useState<AnalysisPeriod>('12h')
  const [historyQuery, setHistoryQuery] = useState('')

  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)
  const profilesFetcher = useCallback(() => listModelProfiles(), [])
  const { data: profiles } = useApi(profilesFetcher)
  const scopesFetcher = useCallback(() => listAuthScopes(), [])
  const { data: scopes } = useApi(scopesFetcher)

  const settingsFetcher = useCallback(() => getSettings(), [])
  const { data: settingsData, refetch: refetchSettings } = useApi(settingsFetcher)
  const disabledProviders: Record<string, boolean> = settingsData?.settings?.delegation_disabled_providers ?? {}

  useEffect(() => {
    if (form.secretScopeID || !scopes?.length) return
    setForm((s) => ({ ...s, secretScopeID: scopes[0].id }))
  }, [form.secretScopeID, scopes])

  const selectedProfile = useMemo(
    () => profiles?.find((p) => p.id === form.modelProfileID) ?? null,
    [profiles, form.modelProfileID],
  )
  const modelSuggestions = selectedProfile?.known_models ?? []

  const delegationsFetcher = useCallback(
    () => listDelegations({ workspaceId: form.workspaceID || undefined, limit: 200 }),
    [form.workspaceID],
  )
  const { data: delegations, loading, error, refetch } = useApi(delegationsFetcher)
  const capacityFetcher = useCallback(
    () =>
      listDelegationModelCapacity({
        workspaceId: form.workspaceID || undefined,
        taskKind: form.taskKind.trim() || undefined,
        limit: 20,
      }),
    [form.workspaceID, form.taskKind],
  )
  const { data: capacityRows, refetch: refetchCapacity } = useApi(capacityFetcher)

  const refreshDelegationReports = useCallback(() => {
    refetch()
    refetchCapacity()
  }, [refetch, refetchCapacity])

  // Event-driven refresh: the 'workers' channel carries RunEvents (status/usage/tool_call)
  // for all workers including ephemeral delegation workers, plus lightweight signals we
  // publish on create/review. This makes the delegations list + capacity update promptly
  // while runs are live and on completion without a blind 30s poll.
  useEffect(() => {
    let pending = false
    const onWorkersEvent = () => {
      if (pending) return
      pending = true
      // small debounce so chatty usage/tool_call bursts don't storm the list endpoint
      window.setTimeout(() => {
        pending = false
        refreshDelegationReports()
      }, 250)
    }
    const unsub = subscribeEvent('workers', onWorkersEvent)

    // Fallback poll (longer interval) + visibility refresh, like other realtime hooks.
    const fallbackMs = 60_000
    let fallbackTimer: number | undefined
    const startFallback = () => {
      if (fallbackTimer) window.clearInterval(fallbackTimer)
      fallbackTimer = window.setInterval(() => {
        if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
          refreshDelegationReports()
        }
      }, fallbackMs)
    }
    startFallback()

    const onVis = () => {
      if (document.visibilityState === 'visible') refreshDelegationReports()
    }
    document.addEventListener('visibilitychange', onVis)

    return () => {
      unsub()
      if (fallbackTimer) window.clearInterval(fallbackTimer)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [refreshDelegationReports])

  // Provider group switches (OpenCode, Claude, Grok, OpenRouter, MiniMax, ...)
  // are persisted in settings and applied server-side to capacity + routing.
  const [savingDisabled, setSavingDisabled] = useState(false)
  async function setProviderDisabled(group: string, disabled: boolean) {
    if (!settingsData?.settings) return
    const next: Record<string, boolean> = {
      ...(settingsData.settings.delegation_disabled_providers ?? {}),
      [group]: disabled,
    }
    // Clean false entries for compactness (absent === enabled)
    const cleaned: Record<string, boolean> = {}
    for (const [k, v] of Object.entries(next)) {
      if (v) cleaned[k] = true
    }
    setSavingDisabled(true)
    try {
      const patch: Settings = {
        ...settingsData.settings,
        delegation_disabled_providers: cleaned,
      }
      await updateSettings(patch)
      await refetchSettings()
      await refetchCapacity()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to update provider switches')
    } finally {
      setSavingDisabled(false)
    }
  }

  function set<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((s) => ({ ...s, [key]: value }))
  }

  function applyProfile(profileID: string) {
    const p = profiles?.find((row) => row.id === profileID)
    setForm((s) => ({
      ...s,
      modelProfileID: profileID === '__manual__' ? '' : profileID,
      ...(p
        ? {
            modelProvider: p.provider,
            modelEndpointURL: p.endpoint_url || '',
            secretScopeID: p.secret_scope_id || s.secretScopeID,
            modelID: p.known_models?.[0] || s.modelID,
          }
        : {}),
    }))
  }

  async function handleSubmit() {
    if (!form.workspaceID) {
      toast.error('Choose a workspace')
      return
    }
    if (!form.objective.trim()) {
      toast.error('Objective is required')
      return
    }
    const hasCandidateModels = form.modelCandidatesJSON.trim().length > 0
    if (
      form.modelSelectionMode !== 'capacity' &&
      !hasCandidateModels &&
      !form.modelProfileID &&
      (!form.modelProvider || !form.modelID.trim())
    ) {
      toast.error('Choose a model profile or provider/model')
      return
    }
    let modelCandidates: DelegationModelCandidate[] | undefined
    try {
      modelCandidates = parseModelCandidates(form.modelCandidatesJSON)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Candidate models are invalid')
      return
    }
    setSubmitting(true)
    try {
      const body: CreateDelegationInput = {
        workspace_id: form.workspaceID,
        objective: form.objective.trim(),
        handoff: form.handoff.trim() || undefined,
        name: form.name.trim() || undefined,
        task_id: form.taskID.trim() || undefined,
        task_kind: form.taskKind.trim() || undefined,
        worker_mode: form.workerMode,
        review_required: form.reviewRequired,
        model_profile_id: form.modelProfileID || undefined,
        model_provider: form.modelProvider,
        model_id: form.modelID.trim(),
        model_endpoint_url: form.modelEndpointURL.trim() || undefined,
        secret_scope_id: form.secretScopeID || undefined,
        model_selection_mode: form.modelSelectionMode,
        model_candidate_index: form.modelCandidateIndex || undefined,
        model_candidates: modelCandidates,
        parallelism: form.parallelism,
        parent_model: form.parentModel.trim() || undefined,
        parent_input_tokens: nonzero(form.parentInputTokens),
        parent_output_tokens: nonzero(form.parentOutputTokens),
        parent_cost_usd: nonzero(form.parentCostUSD),
        baseline_tokens_estimate: nonzero(form.baselineTokensEstimate),
        baseline_cost_usd: nonzero(form.baselineCostUSD),
        max_tool_calls: nonzero(form.maxToolCalls),
        max_wall_clock_seconds: nonzero(form.maxWallClockSeconds),
        max_output_tokens: nonzero(form.maxOutputTokens),
      }
      const out = await createDelegation(body)
      toast.success(`Delegated to ${out.dispatches.length} worker context${out.dispatches.length === 1 ? '' : 's'}`)
      setForm((s) => ({ ...s, objective: '', handoff: '', taskID: '', name: '', modelCandidatesJSON: '' }))
      refreshDelegationReports()
      // create now returns fast; the delegation appears with "dispatched"/"waiting"
      // workers and transitions to running via polling + WorkerLiveTail. No 15s
      // block on CLI startup.
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delegation failed')
    } finally {
      setSubmitting(false)
    }
  }

  const periodDelegations = useMemo(
    () => filterDelegationsByPeriod(delegations ?? [], analysisPeriod),
    [delegations, analysisPeriod],
  )
  const liveDelegations = useMemo(
    () => (delegations ?? []).filter(delegationIsLive),
    [delegations],
  )
  const historicalBaseDelegations = useMemo(
    () => filterDelegationsBySearch(periodDelegations.filter((d) => !delegationIsLive(d)), historyQuery),
    [periodDelegations, historyQuery],
  )
  const summary = useMemo(() => summariseDelegations(periodDelegations), [periodDelegations])
  const modelRank = useMemo(() => rankDelegationModels(periodDelegations), [periodDelegations])
  const filterCounts = useMemo(() => delegationFilterCounts(historicalBaseDelegations), [historicalBaseDelegations])
  const periodCounts = useMemo(() => delegationFilterCounts(periodDelegations), [periodDelegations])
  const filteredHistoricalDelegations = useMemo(
    () => filterDelegations(historicalBaseDelegations, historyFilter),
    [historicalBaseDelegations, historyFilter],
  )
  const delegationSections = useMemo(
    () => buildDelegationSections(filteredHistoricalDelegations, historyFilter),
    [filteredHistoricalDelegations, historyFilter],
  )
  const [advancedOpen, setAdvancedOpen] = useState(false)

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
            <GitBranch className="h-6 w-6" /> Delegations
          </h1>
          <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
            One-shot worker runs spawned from a parent session. For scheduled automation, use{' '}
            <Link to="/workers" className="underline hover:text-foreground">
              Workers
            </Link>.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" asChild>
            <Link to="/delegations/models">Model ranks</Link>
          </Button>
          <Button variant="outline" asChild>
            <Link to="/model-providers">Model profiles</Link>
          </Button>
        </div>
      </header>

      <OperationsOverviewBar
        liveCount={liveDelegations.length}
        periodCounts={periodCounts}
        historicalCounts={filterCounts}
        summary={summary}
        period={analysisPeriod}
        filteredCount={filteredHistoricalDelegations.length}
        totalHistoricalCount={historicalBaseDelegations.length}
        periodTotalCount={periodDelegations.length}
        filter={historyFilter}
        onFilterChange={setHistoryFilter}
      />

      <LiveDelegationsPanel rows={liveDelegations} onReviewed={refreshDelegationReports} />

      {error && (
        <div className="border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      )}

      <DelegationReportHeader
        counts={filterCounts}
        filter={historyFilter}
        filteredCount={filteredHistoricalDelegations.length}
        totalCount={historicalBaseDelegations.length}
        period={analysisPeriod}
        query={historyQuery}
        onFilterChange={setHistoryFilter}
        onPeriodChange={setAnalysisPeriod}
        onQueryChange={setHistoryQuery}
      />

      {loading && !delegations ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading delegations...
        </div>
      ) : delegations && delegations.length > 0 ? (
        <div className="space-y-3">
          {filteredHistoricalDelegations.length === 0 && (
            <div className="border border-border bg-card/40 p-5 text-sm text-muted-foreground">
              No historical runs match these filters.
            </div>
          )}
          {delegationSections.map((section) => (
            <DelegationSection
              key={section.key}
              title={section.title}
              count={section.rows.length}
            >
              {section.rows.map((d) => (
                <CompactDelegationRow key={d.id} delegation={d} onReviewed={refreshDelegationReports} />
              ))}
            </DelegationSection>
          ))}
        </div>
      ) : (
        <div className="border border-border bg-card/40 p-5 text-sm text-muted-foreground">
          <div className="flex flex-col items-center gap-3 text-center">
            <GitBranch className="h-8 w-8 text-muted-foreground/60" />
            <div className="space-y-1">
              <div className="font-medium text-foreground">No delegated runs yet</div>
              <p className="max-w-md text-xs text-muted-foreground">
                Delegations are one-shot worker contexts spawned on demand from
                a parent session. Each run executes a single objective and
                returns results for review.
              </p>
              <p className="max-w-md text-[11px] text-muted-foreground/70">
                For durable automation on a schedule, use{' '}
                <Link to="/workers" className="underline hover:text-foreground">
                  Workers
                </Link>.
              </p>
            </div>
          </div>
        </div>
      )}

      <AddDelegationSection
        open={launchOpen}
        onOpenChange={setLaunchOpen}
        form={form}
        set={set}
        disabledProviders={disabledProviders}
        profiles={profiles ?? []}
        scopes={scopes ?? []}
        workspaces={workspaces ?? []}
        selectedProfile={selectedProfile}
        modelSuggestions={modelSuggestions}
        applyProfile={applyProfile}
        submitting={submitting}
        onSubmit={handleSubmit}
      />

      <section className="border border-border bg-card/30">
        <button
          type="button"
          onClick={() => setAdvancedOpen(!advancedOpen)}
          aria-expanded={advancedOpen}
          className="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left transition-colors hover:bg-card/50"
        >
          <span className="flex min-w-0 items-center gap-2">
            {advancedOpen ? (
              <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
            ) : (
              <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
            )}
            <span className="min-w-0">
              <span className="block text-sm font-semibold">Advanced</span>
              <span className="block truncate text-[11px] text-muted-foreground">
                Provider switches, model performance, and capacity routing.
              </span>
            </span>
          </span>
          <Badge variant="outline" className="shrink-0 text-[10px] uppercase">
            {advancedOpen ? 'open' : 'collapsed'}
          </Badge>
        </button>
        {advancedOpen && (
          <div className="space-y-4 border-t border-border/50 p-4">
            <ProviderSwitches
              disabled={disabledProviders}
              onToggle={setProviderDisabled}
              saving={savingDisabled}
            />
            <ModelPerformancePanel
              summary={summary}
              capacityRows={capacityRows ?? []}
              modelRank={modelRank}
              period={analysisPeriod}
            />
          </div>
        )}
      </section>
    </div>
  )
}

function OperationsOverviewBar({
  liveCount,
  periodCounts,
  historicalCounts,
  summary,
  period,
  filteredCount,
  totalHistoricalCount,
  periodTotalCount,
  filter,
  onFilterChange,
}: {
  liveCount: number
  periodCounts: ReturnType<typeof delegationFilterCounts>
  historicalCounts: ReturnType<typeof delegationFilterCounts>
  summary: ReturnType<typeof summariseDelegations>
  period: AnalysisPeriod
  filteredCount: number
  totalHistoricalCount: number
  periodTotalCount: number
  filter: DelegationFilter
  onFilterChange: (filter: DelegationFilter) => void
}) {
  const hasSavingsEstimate = summary.baselineKnown || summary.costBaselineKnown
  const chips: Array<{
    key: DelegationFilter | 'live'
    label: string
    count: number
    accent?: 'live' | 'warn' | 'good'
    filterKey?: DelegationFilter
  }> = [
    { key: 'live', label: 'Live', count: liveCount, accent: liveCount > 0 ? 'live' : undefined },
    {
      key: 'attention',
      label: 'Attention',
      count: periodCounts.attention,
      accent: periodCounts.attention > 0 ? 'warn' : undefined,
      filterKey: 'attention',
    },
    {
      key: 'needs_review',
      label: 'Needs review',
      count: periodCounts.needsReview,
      accent: periodCounts.needsReview > 0 ? 'warn' : undefined,
      filterKey: 'needs_review',
    },
    { key: 'success', label: 'Success', count: periodCounts.success, accent: 'good', filterKey: 'success' },
    { key: 'failure', label: 'Failure', count: periodCounts.failure, filterKey: 'failure' },
    { key: 'reviewed', label: 'Reviewed', count: periodCounts.reviewed, filterKey: 'reviewed' },
  ]
  return (
    <section className="border border-border bg-card/40 px-3 py-2.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          {chips.map((chip) => {
            const active = chip.filterKey ? filter === chip.filterKey : false
            const inner = (
              <>
                <span className="text-[10px] uppercase tracking-wider text-muted-foreground">{chip.label}</span>
                <span
                  className={cn(
                    'font-mono text-sm font-semibold tabular-nums',
                    chip.accent === 'live' && 'text-sky-300',
                    chip.accent === 'warn' && 'text-amber-300',
                    chip.accent === 'good' && 'text-emerald-300',
                  )}
                >
                  {chip.count}
                </span>
              </>
            )
            if (!chip.filterKey) {
              return (
                <div key={chip.key} className="flex items-center gap-1.5 border border-border/60 bg-background/50 px-2 py-1">
                  {liveCount > 0 && <Activity className="h-3 w-3 text-sky-300" />}
                  {inner}
                </div>
              )
            }
            const filterKey = chip.filterKey
            return (
              <button
                key={chip.key}
                type="button"
                onClick={() => onFilterChange(filterKey)}
                aria-pressed={active}
                className={cn(
                  'flex items-center gap-1.5 border px-2 py-1 text-left transition-colors',
                  active ? 'border-primary/50 bg-primary/10' : 'border-border/60 bg-background/50 hover:bg-background/80',
                )}
              >
                {inner}
              </button>
            )
          })}
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
          <span className="tabular-nums">
            Frontier avoided{' '}
            <span className="font-mono font-medium text-foreground">
              {hasSavingsEstimate ? formatSignedTokens(summary.frontierAvoided) : '—'}
            </span>
          </span>
          <span className="tabular-nums">
            Cost saved{' '}
            <span
              className={cn(
                'font-mono font-medium',
                summary.costBaselineKnown && summary.costSaved >= 0 ? 'text-emerald-300' : 'text-foreground',
              )}
            >
              {summary.costBaselineKnown ? formatSignedCost(summary.costSaved) : '—'}
            </span>
          </span>
          <span className="tabular-nums">
            Worker tokens <span className="font-mono font-medium text-foreground">{formatTokens(summary.workerTokens)}</span>
          </span>
        </div>
      </div>
      <div className="mt-2 flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-2 text-[10px] text-muted-foreground">
        <span>
          <Badge variant="outline" className="mr-1.5 text-[9px] uppercase">
            {periodLabel(period)}
          </Badge>
          {periodTotalCount} in period · {filteredCount}/{totalHistoricalCount} history shown
          {historicalCounts.needsReview > 0 && filter !== 'needs_review' && (
            <span className="ml-2 text-amber-300">{historicalCounts.needsReview} awaiting review in history</span>
          )}
        </span>
        <button
          type="button"
          className="text-primary hover:underline"
          onClick={() => onFilterChange('all')}
        >
          Reset filters
        </button>
      </div>
    </section>
  )
}

function DelegationReportHeader({
  counts,
  filter,
  filteredCount,
  totalCount,
  period,
  query,
  onFilterChange,
  onPeriodChange,
  onQueryChange,
}: {
  counts: ReturnType<typeof delegationFilterCounts>
  filter: DelegationFilter
  filteredCount: number
  totalCount: number
  period: AnalysisPeriod
  query: string
  onFilterChange: (filter: DelegationFilter) => void
  onPeriodChange: (period: AnalysisPeriod) => void
  onQueryChange: (query: string) => void
}) {
  const filters: Array<{ key: DelegationFilter; label: string; count: number }> = [
    { key: 'all', label: 'All', count: counts.all },
    { key: 'attention', label: 'Attention', count: counts.attention },
    { key: 'needs_review', label: 'Needs review', count: counts.needsReview },
    { key: 'success', label: 'Success', count: counts.success },
    { key: 'failure', label: 'Failure', count: counts.failure },
    { key: 'interrupted', label: 'Interrupted', count: counts.interrupted },
    { key: 'cancelled', label: 'Cancelled', count: counts.cancelled },
    { key: 'reviewed', label: 'Reviewed', count: counts.reviewed },
  ]
  return (
    <section className="space-y-2 border border-border bg-card/30 px-3 py-2.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
          History · {filteredCount}/{totalCount}
        </h2>
        <div className="relative w-full min-w-48 sm:w-64">
          <Search className="pointer-events-none absolute left-2 top-2 h-3.5 w-3.5 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => onQueryChange(e.target.value)}
            className="h-8 pl-7 text-xs"
            placeholder="Search objective, ids, model, output..."
          />
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        {ANALYSIS_PERIODS.map((item) => (
          <Button
            key={item.key}
            type="button"
            size="xs"
            variant={period === item.key ? 'default' : 'outline'}
            aria-pressed={period === item.key}
            onClick={() => onPeriodChange(item.key)}
            className="h-6 gap-1 px-2 tabular-nums"
          >
            <Clock className="h-3 w-3" />
            {item.label}
          </Button>
        ))}
        <span className="mx-1 hidden h-4 w-px bg-border/60 sm:inline" aria-hidden="true" />
        {filters.map((item) => (
          <Button
            key={item.key}
            type="button"
            size="xs"
            variant={filter === item.key ? 'default' : 'outline'}
            aria-pressed={filter === item.key}
            onClick={() => onFilterChange(item.key)}
            className="h-6 px-2 tabular-nums"
          >
            {item.label}
            <span className="font-mono text-[9px] opacity-75">{item.count}</span>
          </Button>
        ))}
      </div>
    </section>
  )
}

function DelegationSection({
  title,
  count,
  children,
}: {
  title: string
  count: number
  children: ReactNode
}) {
  if (count === 0) return null
  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between border border-border/60 bg-background/50 px-3 py-2">
        <h2 className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
          {title}
        </h2>
        <Badge variant="outline" className="font-mono text-[10px]">
          {count}
        </Badge>
      </div>
      <div className="space-y-1.5">{children}</div>
    </section>
  )
}

function AddDelegationSection({
  open,
  onOpenChange,
  ...panelProps
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
} & ComponentProps<typeof LaunchPanel>) {
  const Icon = open ? ChevronDown : ChevronRight
  return (
    <section className="border border-border bg-card/30">
      <button
        type="button"
        onClick={() => onOpenChange(!open)}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left transition-colors hover:bg-card/50"
      >
        <span className="flex min-w-0 items-center gap-2">
          <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="min-w-0">
            <span className="block text-sm font-semibold">Add delegation</span>
            <span className="block truncate text-[11px] text-muted-foreground">
              Spawn worker contexts only when you need to run a new comparison.
            </span>
          </span>
        </span>
        <Badge variant="outline" className="shrink-0 text-[10px] uppercase">
          {open ? 'open' : 'collapsed'}
        </Badge>
      </button>
      {open && (
        <div className="border-t border-border/50 p-3">
          <LaunchPanel {...panelProps} />
        </div>
      )}
    </section>
  )
}

function LaunchPanel({
  form,
  set,
  disabledProviders,
  profiles,
  scopes,
  workspaces,
  selectedProfile,
  modelSuggestions,
  applyProfile,
  submitting,
  onSubmit,
}: {
  form: FormState
  set: <K extends keyof FormState>(key: K, value: FormState[K]) => void
  disabledProviders: Record<string, boolean>
  profiles: ModelProfile[]
  scopes: Array<{ id: string; name: string; display_name?: string }>
  workspaces: Array<{ id: string; name: string }>
  selectedProfile: ModelProfile | null
  modelSuggestions: string[]
  applyProfile: (id: string) => void
  submitting: boolean
  onSubmit: () => void
}) {
  const selectedDisabled = disabledReasonForCandidate(disabledProviders, {
    provider: form.modelProvider,
    modelID: form.modelID,
    endpoint: form.modelEndpointURL,
    label: selectedProfile?.name,
  })
  const profileDisabled = selectedProfile
    ? disabledReasonForCandidate(disabledProviders, {
        provider: selectedProfile.provider,
        modelID: selectedProfile.known_models?.[0] || form.modelID,
        endpoint: selectedProfile.endpoint_url,
        label: selectedProfile.name,
      })
    : ''
  const activeDisabledGroups = activeProviderGroups(disabledProviders)
  const explicitSelectionDisabled = form.modelSelectionMode !== 'capacity' && Boolean(selectedDisabled || profileDisabled)
  return (
    <Card>
      <CardContent className="space-y-4 p-4">
        {activeDisabledGroups.length > 0 && (
          <div className="flex flex-wrap items-center gap-2 border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-200">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            <span>Routing disabled:</span>
            {activeDisabledGroups.map((group) => (
              <Badge key={group} variant="outline" className="border-amber-500/40 text-[10px] uppercase text-amber-200">
                {group}
              </Badge>
            ))}
            <span className="text-amber-100/70">Capacity mode will skip these groups; explicit selections are blocked below.</span>
          </div>
        )}
        {explicitSelectionDisabled && (
          <div className="flex items-center gap-2 border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            <span>{profileDisabled || selectedDisabled}. Pick an enabled route or re-enable the group in Delegation provider switches.</span>
          </div>
        )}
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-[1.2fr_0.8fr]">
          <div className="space-y-3">
            <Field label="Objective">
              <Textarea
                value={form.objective}
                onChange={(e) => set('objective', e.target.value)}
                className="min-h-24"
                placeholder="Implement the feature slice, inspect the files, run focused tests, and return a concise handoff."
              />
            </Field>
            <Field label="Handoff Packet">
              <Textarea
                value={form.handoff}
                onChange={(e) => set('handoff', e.target.value)}
                className="min-h-28"
                placeholder="Relevant files, constraints, acceptance criteria, and what the worker should avoid re-reading."
              />
            </Field>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-1">
            <Field label="Workspace">
              <Select value={form.workspaceID || '__all__'} onValueChange={(v) => set('workspaceID', v === '__all__' ? '' : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Workspace" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__all__">All workspaces</SelectItem>
                  {workspaces.map((w) => (
                    <SelectItem key={w.id} value={w.id}>
                      {w.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Model Profile">
              <Select value={form.modelProfileID || '__manual__'} onValueChange={applyProfile}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Manual" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__manual__">Manual</SelectItem>
                  {profiles.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {profileDisabled && (
                <p className="text-[10px] text-destructive">{profileDisabled}</p>
              )}
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Provider">
                <Select
                  value={form.modelProvider}
                  onValueChange={(v) => set('modelProvider', v as ModelProvider)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {providers.map((p) => (
                      <SelectItem key={p.value} value={p.value}>
                        {p.label}{disabledReasonForCandidate(disabledProviders, { provider: p.value }) ? ' (off)' : ''}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {selectedDisabled && (
                  <p className="text-[10px] text-destructive">{selectedDisabled}</p>
                )}
              </Field>
              <Field label="Parallel">
                <Input
                  type="number"
                  min={1}
                  max={20}
                  value={form.parallelism}
                  onChange={(e) => set('parallelism', clampInt(e.target.value, 1, 20))}
                />
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Selection">
                <Select
                  value={form.modelSelectionMode}
                  onValueChange={(v) => set('modelSelectionMode', v as ModelSelectionMode)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="single">Single</SelectItem>
                    <SelectItem value="ranked">Ranked</SelectItem>
                    <SelectItem value="random">Random</SelectItem>
                    <SelectItem value="side_by_side">Side by side</SelectItem>
                    <SelectItem value="capacity">Capacity</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label="Candidate #">
                <Input
                  type="number"
                  min={0}
                  max={19}
                  value={form.modelCandidateIndex}
                  onChange={(e) => set('modelCandidateIndex', clampInt(e.target.value, 0, 19))}
                />
              </Field>
            </div>
            <Field label="Model">
              <Input
                value={form.modelID}
                onChange={(e) => set('modelID', e.target.value)}
                className={selectedDisabled ? 'border-destructive/60' : undefined}
                list="delegation-models"
                placeholder={selectedProfile ? selectedProfile.known_models?.[0] : 'minimax/MiniMax-M3'}
              />
              <datalist id="delegation-models">
                {modelSuggestions.map((m) => (
                  <option key={m} value={m} />
                ))}
              </datalist>
            </Field>
            <Field label="Candidate Models">
              <Textarea
                value={form.modelCandidatesJSON}
                onChange={(e) => set('modelCandidatesJSON', e.target.value)}
                className="min-h-20 font-mono text-[11px]"
                placeholder='[{"model_provider":"opencode_cli","model_id":"minimax/MiniMax-M3","capability_tags":["coding"]}]'
              />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Mode">
                <Select value={form.workerMode} onValueChange={(v) => set('workerMode', v as FormState['workerMode'])}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="execute">Execute</SelectItem>
                    <SelectItem value="review">Review</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label="Parent Review">
                <label className="flex h-10 items-center gap-2 rounded-md border border-input bg-background px-3 text-xs text-muted-foreground">
                  <Checkbox
                    checked={form.reviewRequired}
                    onCheckedChange={(v) => set('reviewRequired', Boolean(v))}
                  />
                  Required
                </label>
              </Field>
            </div>
            <Field label="Secret Scope">
              <Select value={form.secretScopeID || '__none__'} onValueChange={(v) => set('secretScopeID', v === '__none__' ? '' : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Scope" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">None</SelectItem>
                  {scopes.map((s) => (
                    <SelectItem key={s.id} value={s.id}>
                      {s.display_name || s.name || s.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          <NumberField label="Parent In" value={form.parentInputTokens} onChange={(v) => set('parentInputTokens', v)} />
          <NumberField label="Parent Out" value={form.parentOutputTokens} onChange={(v) => set('parentOutputTokens', v)} />
          <NumberField label="Frontier Tokens" value={form.baselineTokensEstimate} onChange={(v) => set('baselineTokensEstimate', v)} />
          <NumberField label="Frontier Cost" value={form.baselineCostUSD} step="0.01" onChange={(v) => set('baselineCostUSD', v)} />
          <Field label="Parent Model">
            <Input value={form.parentModel} onChange={(e) => set('parentModel', e.target.value)} placeholder="opus / gpt-5" />
          </Field>
          <Field label="Task ID">
            <Input value={form.taskID} onChange={(e) => set('taskID', e.target.value)} placeholder="task id" />
          </Field>
          <Field label="Task Kind">
            <Input value={form.taskKind} onChange={(e) => set('taskKind', e.target.value)} placeholder="coding / review / visual" />
          </Field>
          <NumberField label="Tool Calls" value={form.maxToolCalls} onChange={(v) => set('maxToolCalls', v)} />
          <NumberField label="Wall Seconds" value={form.maxWallClockSeconds} onChange={(v) => set('maxWallClockSeconds', v)} />
        </div>

        <div className="flex justify-end">
          <Button onClick={onSubmit} disabled={submitting || explicitSelectionDisabled}>
            {submitting ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : <Plus className="mr-1.5 h-4 w-4" />}
            Delegate
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function SummaryStrip({ summary }: { summary: ReturnType<typeof summariseDelegations> }) {
  const hasSavingsEstimate = summary.baselineKnown || summary.costBaselineKnown
  const colCount = summary.frontierQuotaBurned > 0 ? 8 : 7
  return (
    <div className={cn('grid grid-cols-2 gap-3', colCount <= 7 ? 'lg:grid-cols-7' : 'lg:grid-cols-8')}>
      <Metric
        label="Parent Actual"
        value={summary.parentKnown ? formatTokens(summary.parentTokens) : 'unknown'}
        title="Optional parent-client usage telemetry. Some parent clients do not report this reliably."
      />
      <Metric label="Worker Tokens" value={formatTokens(summary.workerTokens)} accent={summary.workerTokens > 0 ? 'live' : undefined} />
      <Metric
        label="Frontier Avoided"
        value={hasSavingsEstimate ? formatSignedTokens(summary.frontierAvoided) : 'needs estimate'}
        accent={summary.frontierAvoided > 0 ? 'good' : undefined}
        title="Estimated expensive parent-model tokens avoided for delegated work. This comes from the baseline estimate, not parent actual tokens."
      />
      <Metric
        label="Worker Token Delta"
        value={summary.baselineKnown ? formatSignedTokens(summary.workerTokenDelta) : 'unknown'}
        accent={summary.baselineKnown ? (summary.workerTokenDelta >= 0 ? 'good' : 'warn') : undefined}
        title="Frontier tokens avoided minus worker tokens. Useful for context/noise, but cost saved is the business metric."
      />
      <Metric
        label="Cost Saved"
        value={summary.costBaselineKnown ? formatSignedCost(summary.costSaved) : 'unknown'}
        accent={summary.costBaselineKnown ? (summary.costSaved >= 0 ? 'good' : 'warn') : undefined}
      />
      <Metric
        label="Real $ Spent"
        value={formatCost(summary.realDollarsSpent)}
        title="Out-of-pocket cash (OpenRouter/metered only). Excludes subscription quota usage."
      />
      <Metric
        label="Frontier Quota Preserved"
        value={formatTokens(summary.frontierQuotaPreserved)}
        accent={summary.frontierQuotaPreserved > 0 ? 'good' : undefined}
        title="Frontier (Claude) subscription tokens kept off the plan by delegating to cheaper workers."
      />
      {summary.frontierQuotaBurned > 0 && (
        <Metric
          label="Frontier Quota Burned"
          value={formatTokens(summary.frontierQuotaBurned)}
          accent="burn"
          title="Claude subscription tokens spent running Opus-as-worker. This is waste — frontier quota consumed by workers instead of the parent."
        />
      )}
    </div>
  )
}

type CapacityRow = DelegationModelCapacity & { duplicate_count?: number }

function dedupeCapacityRows(rows: DelegationModelCapacity[]): CapacityRow[] {
  const byKey = new Map<string, CapacityRow>()
  const counts = new Map<string, number>()
  for (const row of rows) {
    const key = row.model_key || `${row.model_provider}/${row.model_id}`
    counts.set(key, (counts.get(key) || 0) + 1)
    const current = byKey.get(key)
    if (
      !current ||
      (row.available && !current.available) ||
      row.capacity_score > current.capacity_score
    ) {
      byKey.set(key, { ...row })
    }
  }
  return Array.from(byKey.values())
    .map((row) => ({
      ...row,
      duplicate_count: counts.get(row.model_key || `${row.model_provider}/${row.model_id}`) || 1,
    }))
    .sort((a, b) => {
      if (a.available !== b.available) return a.available ? -1 : 1
      if (a.capacity_score !== b.capacity_score) return b.capacity_score - a.capacity_score
      return a.model_key.localeCompare(b.model_key)
    })
    .map((row, index) => ({ ...row, rank: index + 1 }))
}

function ModelPerformancePanel({
  summary,
  capacityRows,
  modelRank,
  period,
}: {
  summary: ReturnType<typeof summariseDelegations>
  capacityRows: DelegationModelCapacity[]
  modelRank: ModelRankRow[]
  period: AnalysisPeriod
}) {
  const currentCapacity = dedupeCapacityRows(capacityRows).slice(0, 5)
  const reviewedModels = modelRank.slice(0, 5)
  return (
    <section className="space-y-3 border border-border bg-card/30 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="flex items-center gap-1.5 text-sm font-semibold">
            <Gauge className="h-4 w-4" /> Model performance
          </h2>
          <p className="text-[11px] text-muted-foreground">
            Period ROI, reviewed model rank, and current capacity at a glance.
          </p>
        </div>
        <Badge variant="outline" className="text-[10px] uppercase">
          {periodLabel(period)}
        </Badge>
      </div>

      <SummaryStrip summary={summary} />

      <div className="grid gap-3 lg:grid-cols-2">
        <div className="border border-border/60 bg-background/50">
          <div className="flex items-center justify-between border-b border-border/40 px-3 py-2">
            <h3 className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">Reviewed rank</h3>
            <Badge variant="outline" className="text-[10px] uppercase">{reviewedModels.length} shown</Badge>
          </div>
          {reviewedModels.length === 0 ? (
            <div className="px-3 py-4 text-sm text-muted-foreground">Review completed delegations to rank models.</div>
          ) : (
            <div className="divide-y divide-border/40">
              {reviewedModels.map((row, index) => (
                <CompactModelRankRow key={row.modelKey} row={row} rank={index + 1} />
              ))}
            </div>
          )}
        </div>

        <div className="border border-border/60 bg-background/50">
          <div className="flex items-center justify-between border-b border-border/40 px-3 py-2">
            <h3 className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">Current router</h3>
            <Badge variant="outline" className="text-[10px] uppercase">
              {currentCapacity.filter((row) => row.available).length} available
            </Badge>
          </div>
          {currentCapacity.length === 0 ? (
            <div className="px-3 py-4 text-sm text-muted-foreground">No capacity rows for the current workspace/task kind.</div>
          ) : (
            <div className="divide-y divide-border/40">
              {currentCapacity.map((row) => (
                <CompactCapacityRow key={`${row.model_profile_id || row.model_key}-${row.rank}`} row={row} />
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}

function CompactModelRankRow({ row, rank }: { row: ModelRankRow; rank: number }) {
  // Reliability = OPERATIONAL success (did the run terminate successfully),
  // over runs with a terminal outcome. NOT gated on cost telemetry: a CLI run
  // (grok/mimo) routinely succeeds + gets reviewed yet reports no usage, and
  // the old accounting-gated successRate dropped those, so a reviewed model
  // absurdly showed reliability "unmeasured". If it ran, we know if it worked.
  const terminalRuns = row.success + row.failure
  const reliability = terminalRuns ? row.success / terminalRuns : 0
  return (
    <div className="grid gap-2 px-3 py-2.5 sm:grid-cols-[2rem_minmax(0,1fr)_4.5rem_4.5rem_4.5rem] sm:items-center">
      <div className="font-mono text-[12px] text-muted-foreground">#{rank}</div>
      <div className="min-w-0">
        <div className="truncate text-[12px] font-medium">{row.modelID || row.modelKey}</div>
        <div className="font-mono text-[10px] text-muted-foreground">{row.modelProvider || 'provider'}</div>
        <div className="mt-1 grid grid-cols-2 gap-1.5">
          <RankMeter label="quality" value={row.avgScore / 100} known={row.reviewCount > 0} />
          <RankMeter label="reliability" value={reliability} known={terminalRuns > 0} />
        </div>
      </div>
      <TinyStat label="score" value={row.reviewCount ? `${Math.round(row.avgScore)}` : 'new'} />
      <TinyStat label="success" value={terminalRuns ? `${Math.round(reliability * 100)}%` : 'new'} />
      <TinyStat label="cost" value={formatCost(row.costUSD)} />
    </div>
  )
}

function CompactCapacityRow({ row }: { row: CapacityRow }) {
  return (
    <div className="grid gap-2 px-3 py-2.5 sm:grid-cols-[2rem_minmax(0,1fr)_4.5rem_4.5rem_4.5rem] sm:items-center">
      <div className="font-mono text-[12px] text-muted-foreground">#{row.rank}</div>
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="truncate text-[12px] font-medium">{row.model_id || row.model_key}</span>
          <Badge variant="outline" className={row.available ? 'border-emerald-500/40 text-emerald-300' : 'border-red-500/40 text-red-300'}>
            {row.available ? 'available' : 'off'}
          </Badge>
          {row.running > 0 && (
            <Badge variant="outline" className="border-sky-500/40 text-sky-300">
              <Activity className="mr-1 h-3 w-3" />
              {row.running}
            </Badge>
          )}
          {row.duplicate_count && row.duplicate_count > 1 && (
            <Badge variant="outline" className="text-[10px] uppercase">
              {row.duplicate_count} routes
            </Badge>
          )}
        </div>
        <div className="truncate font-mono text-[10px] text-muted-foreground">
          {row.label ? `${row.label} · ` : ''}{row.model_provider}
        </div>
        {row.unavailable_reason && (
          <div className="mt-1 line-clamp-1 text-[10px] text-destructive">{row.unavailable_reason}</div>
        )}
      </div>
      <TinyStat label="score" value={`${Math.round(row.capacity_score)}`} />
      <TinyStat label="reviews" value={row.review_count ? `${Math.round(row.review_score)}` : 'new'} />
      <TinyStat label="time" value={formatDuration(row.avg_duration_ms)} />
    </div>
  )
}

function LiveDelegationsPanel({
  rows,
  onReviewed,
}: {
  rows: DelegationContext[]
  onReviewed: () => void
}) {
  return (
    <section className="space-y-2 border border-border bg-card/30 px-3 py-2.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-1.5 text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
          <Activity className="h-3.5 w-3.5 text-sky-300" /> Live delegations
        </h2>
        <Badge variant="outline" className={cn('text-[10px] uppercase', rows.length > 0 && 'border-sky-500/40 text-sky-300')}>
          {rows.length} active
        </Badge>
      </div>
      {rows.length === 0 ? (
        <div className="border border-border/60 bg-background/50 px-3 py-2 text-xs text-muted-foreground">
          No delegations are currently dispatched or running.
        </div>
      ) : (
        <div className="space-y-1.5">
          {rows.map((d) => (
            <CompactDelegationRow key={d.id} delegation={d} onReviewed={onReviewed} defaultExpanded />
          ))}
        </div>
      )}
    </section>
  )
}

const PROVIDER_GROUPS: Array<{ key: string; label: string; hint: string }> = [
  { key: 'opencode', label: 'OpenCode', hint: 'opencode_cli (MiniMax/GLM/others via opencode)' },
  { key: 'local', label: 'Local models', hint: 'Loopback / LM Studio / Ollama compatible routes' },
  { key: 'minimax', label: 'MiniMax', hint: 'minimax/* model ids' },
  { key: 'openrouter', label: 'OpenRouter', hint: 'openrouter endpoints or profiles' },
  { key: 'claude', label: 'Claude', hint: 'Anthropic/Claude API, CLI, or routed Claude models' },
  { key: 'grok', label: 'Grok', hint: 'grok_cli' },
  { key: 'mimo', label: 'MiMo', hint: 'mimo_cli / xiaomi/mimo model ids' },
  { key: 'pi', label: 'Pi', hint: 'pi_cli / Pi harness routes' },
]

function activeProviderGroups(disabled: Record<string, boolean>): string[] {
  return Object.keys(disabled).filter((key) => disabled[key]).sort()
}

function disabledReasonForCandidate(
  disabled: Record<string, boolean>,
  candidate: { provider?: string; modelID?: string; endpoint?: string; label?: string },
): string {
  const groups = providerGroupSet(candidate)
  const disabledGroup = Array.from(groups).find((group) => disabled[group])
  if (!disabledGroup) return ''
  return `Disabled by ${providerGroupLabel(disabledGroup)} switch`
}

function providerGroupSet(candidate: {
  provider?: string
  modelID?: string
  endpoint?: string
  label?: string
}): Set<string> {
  const groups = new Set<string>()
  const provider = (candidate.provider || '').trim().toLowerCase()
  const modelID = (candidate.modelID || '').trim().toLowerCase()
  const endpoint = (candidate.endpoint || '').trim().toLowerCase()
  const label = (candidate.label || '').trim().toLowerCase()
  if (provider) groups.add(provider)
  if (provider === 'opencode_cli' || provider === 'opencode') groups.add('opencode')
  if (provider === 'claude_cli' || provider === 'anthropic' || provider === 'claude') groups.add('claude')
  if (provider === 'grok_cli' || provider === 'grok') groups.add('grok')
  if (provider === 'mimo_cli' || provider === 'mimo') groups.add('mimo')
  if (provider === 'pi_cli' || provider === 'pi') groups.add('pi')
  if (
    modelID.includes('claude') ||
    modelID.includes('anthropic/') ||
    endpoint.includes('anthropic') ||
    label.includes('claude') ||
    label.includes('anthropic')
  ) {
    groups.add('claude')
  }
  if (modelID.includes('minimax') || endpoint.includes('minimax') || label.includes('minimax')) {
    groups.add('minimax')
  }
  if (modelID.includes('openrouter') || endpoint.includes('openrouter') || label.includes('openrouter')) {
    groups.add('openrouter')
  }
  if (
    endpoint.includes('localhost') ||
    endpoint.includes('127.0.0.1') ||
    endpoint.includes('[::1]') ||
    endpoint.includes('0.0.0.0') ||
    modelID.includes('lmstudio') ||
    endpoint.includes('lmstudio') ||
    label.includes('lmstudio') ||
    modelID.includes('lm studio') ||
    endpoint.includes('lm studio') ||
    label.includes('lm studio') ||
    modelID.includes('ollama') ||
    endpoint.includes('ollama') ||
    label.includes('ollama')
  ) {
    groups.add('local')
  }
  if (
    modelID.includes('mimo') ||
    endpoint.includes('mimo') ||
    label.includes('mimo') ||
    endpoint.includes('xiaomi') ||
    label.includes('xiaomi')
  ) {
    groups.add('mimo')
  }
  if (
    modelID.includes('pi_cli') ||
    endpoint.includes('pi_cli') ||
    label.includes('pi cli') ||
    label.includes('pi.dev') ||
    label.includes('pi harness')
  ) {
    groups.add('pi')
  }
  return groups
}

function providerGroupLabel(group: string): string {
  return PROVIDER_GROUPS.find((g) => g.key === group)?.label || group
}

function ProviderSwitches({
  disabled,
  onToggle,
  saving,
}: {
  disabled: Record<string, boolean>
  onToggle: (group: string, next: boolean) => void | Promise<void>
  saving?: boolean
}) {
  const offGroups = activeProviderGroups(disabled)
  return (
    <section className="space-y-3 border border-border bg-card/30 p-4" data-testid="delegation-options-panel">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold">Delegation options</h2>
          <p className="text-[11px] text-muted-foreground">
            Provider and subscription routes for future worker selection.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge
            variant="outline"
            className={cn('text-[10px] uppercase', offGroups.length > 0 && 'border-amber-500/40 text-amber-300')}
          >
            {offGroups.length === 0 ? 'all enabled' : `${offGroups.length} off`}
          </Badge>
          {saving && <Badge variant="outline" className="text-[10px]">saving...</Badge>}
        </div>
      </div>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        {PROVIDER_GROUPS.map((g) => {
          const isOff = !!disabled[g.key]
          const enabled = !isOff
          return (
            <button
              key={g.key}
              type="button"
              role="switch"
              aria-checked={enabled}
              onClick={() => onToggle(g.key, !isOff)}
              className={cn(
                'flex min-h-16 items-center gap-3 border px-3 py-2 text-left text-xs transition',
                isOff
                  ? 'border-destructive/40 bg-destructive/5 text-destructive'
                  : 'border-emerald-500/40 bg-emerald-500/5 text-emerald-200 hover:bg-emerald-500/10',
                saving && 'opacity-60',
              )}
              title={g.hint}
              disabled={saving}
              data-testid={`delegation-provider-switch-${g.key}`}
              data-state={enabled ? 'checked' : 'unchecked'}
            >
              <span
                className={cn(
                  'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-border transition-colors',
                  enabled ? 'bg-emerald-500/70' : 'bg-muted',
                )}
                aria-hidden="true"
              >
                <span
                  className={cn(
                    'inline-block h-3.5 w-3.5 rounded-full bg-background shadow transition-transform',
                    enabled ? 'translate-x-4' : 'translate-x-1',
                  )}
                />
              </span>
              <span className="min-w-0">
                <span className="block truncate font-medium">{g.label}</span>
                <span className="block truncate font-mono text-[10px] opacity-70">{enabled ? 'enabled' : 'disabled'}</span>
              </span>
            </button>
          )
        })}
      </div>
    </section>
  )
}

// RankMeter renders one axis (quality or reliability). `known=false` means
// there is no data yet (unreviewed model, or no accounted runs) — drawn as a
// dashed empty track + "n/a" so it reads as "not yet measured" rather than a
// misleading measured-zero. Quality and reliability are independent axes
// (review score vs run success rate), so one can be known while the other is
// not — both always render, with an honest reason for any empty bar.
function RankMeter({
  label,
  value,
  known = true,
}: {
  label: string
  value: number
  known?: boolean
}) {
  const pct = Math.max(0, Math.min(100, Math.round(value * 100)))
  return (
    <div title={known ? `${label}: ${pct}%` : `${label}: not measured yet`}>
      <div className="mb-1 flex items-center justify-between text-[8px] uppercase tracking-wider text-muted-foreground">
        <span>{label}</span>
        {!known && <span className="text-muted-foreground/50">n/a</span>}
      </div>
      <div
        className={cn(
          'h-1.5 overflow-hidden rounded-full',
          known ? 'bg-muted/60' : 'border border-dashed border-border/70',
        )}
      >
        {known && (
          <div
            className={cn(
              'h-full rounded-full',
              pct >= 80 ? 'bg-emerald-400' : pct >= 50 ? 'bg-amber-400' : 'bg-red-400',
            )}
            style={{ width: `${pct}%` }}
          />
        )}
      </div>
    </div>
  )
}

function CompactDelegationRow({
  delegation,
  onReviewed,
  defaultExpanded = false,
}: {
  delegation: DelegationContext
  onReviewed: () => void
  defaultExpanded?: boolean
}) {
  const [expanded, setExpanded] = useState(defaultExpanded)
  const [reviewOpen, setReviewOpen] = useState(false)
  const agg = delegation.aggregate
  const frontierAvoided = frontierTokensAvoided(agg)
  const workerDelta = workerTokenDelta(agg)
  const parentTokensKnown = agg.parent_tokens_known || agg.parent_tokens > 0
  const savingsEstimated = agg.savings_confidence === 'estimated' || agg.baseline_tokens > 0 || agg.baseline_cost_usd > 0
  const reviewed = Boolean(delegation.review?.reviewed)
  const needsReview = delegationNeedsReview(delegation)
  const isLive = delegationIsRunning(delegation)
  const modelLabel = delegationModelSummary(delegation)
  const costSaved =
    agg.baseline_cost_usd > 0 && !agg.cost_all_missing
      ? formatSignedCost(agg.estimated_cost_saved_usd)
      : '—'
  const scoreLabel =
    reviewed && typeof delegation.review?.score === 'number'
      ? `${delegation.review.score}`
      : needsReview ? 'pending' : '—'

  function openReview() {
    setExpanded(true)
    setReviewOpen(true)
  }

  function toggleExpanded() {
    setExpanded((value) => {
      if (value) setReviewOpen(false)
      return !value
    })
  }

  return (
    <div
      className={cn(
        'border border-border bg-card/40',
        isLive && 'border-sky-500/30',
        needsReview && !reviewed && !isLive && 'border-amber-500/25',
      )}
    >
      <div className="flex flex-wrap items-center gap-2 px-2.5 py-2 sm:gap-3">
        <button
          type="button"
          onClick={toggleExpanded}
          aria-expanded={expanded}
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-sm border border-border/60 text-muted-foreground hover:bg-background/60"
          title={expanded ? 'Collapse' : 'Expand details'}
        >
          {expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        </button>

        <Badge variant="outline" className={cn('shrink-0 text-[10px]', statusClass(delegation.status))}>
          {delegation.status}
        </Badge>

        <div className="min-w-0 flex-1 basis-[12rem]">
          <p className="line-clamp-1 text-[13px] font-medium leading-tight">{delegation.objective}</p>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-muted-foreground">
            <span>{relativeTime(delegation.updated_at)}</span>
            <span className="truncate">{modelLabel}</span>
            <span>{agg.workers} worker{agg.workers === 1 ? '' : 's'}</span>
            {delegation.task_kind && <span className="uppercase">{delegation.task_kind}</span>}
          </div>
        </div>

        <div className="hidden shrink-0 items-center gap-3 sm:flex">
          <InlineStat
            label="avoided"
            value={savingsEstimated ? formatSignedTokens(frontierAvoided) : '—'}
            accent={frontierAvoided > 0 ? 'good' : undefined}
          />
          <InlineStat
            label="saved"
            value={costSaved}
            accent={agg.baseline_cost_usd > 0 && !agg.cost_all_missing && agg.estimated_cost_saved_usd >= 0 ? 'good' : undefined}
          />
          <InlineStat label="tokens" value={formatTokens(agg.total_tokens)} />
          <InlineStat
            label="score"
            value={scoreLabel}
            accent={reviewed && (delegation.review?.score ?? 0) >= 80 ? 'good' : needsReview ? 'warn' : undefined}
          />
        </div>

        <div className="flex shrink-0 flex-wrap items-center gap-1.5">
          {needsReview && !reviewed && (
            <Button type="button" size="xs" variant="default" className="h-6 px-2" onClick={openReview}>
              <Star className="mr-1 h-3 w-3" />
              Review
            </Button>
          )}
          {reviewed && (
            <Badge variant="outline" className="h-6 border-emerald-500/40 text-[10px] text-emerald-300">
              <CheckCircle2 className="mr-1 h-3 w-3" />
              {delegation.review?.score}/100
            </Badge>
          )}
          <Button type="button" size="xs" variant="outline" className="h-6 px-2" onClick={toggleExpanded}>
            <ChevronsUpDown className="mr-1 h-3 w-3" />
            {expanded ? 'Hide' : 'Details'}
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3 border-t border-border/30 px-2.5 py-1.5 text-[10px] text-muted-foreground sm:hidden">
        <InlineStat label="avoided" value={savingsEstimated ? formatSignedTokens(frontierAvoided) : '—'} compact />
        <InlineStat label="saved" value={costSaved} compact />
        <InlineStat label="tokens" value={formatTokens(agg.total_tokens)} compact />
        <InlineStat label="score" value={scoreLabel} compact />
      </div>

      {expanded && (
        <div className="space-y-3 border-t border-border/50 px-3 py-3">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" className="text-[10px] uppercase">
              {delegation.worker_mode || 'execute'}
            </Badge>
            {delegation.model_selection_mode && (
              <Badge variant="outline" className="text-[10px] uppercase">
                {delegation.model_selection_mode.replace(/_/g, ' ')}
              </Badge>
            )}
            {delegation.review_required && !reviewed && (
              <Badge variant="outline" className="border-amber-500/40 text-[10px] uppercase text-amber-300">
                review required
              </Badge>
            )}
            <Badge
              variant="outline"
              className={cn(
                'text-[10px] uppercase',
                savingsEstimated ? 'border-emerald-500/40 text-emerald-300' : 'border-amber-500/40 text-amber-300',
              )}
            >
              {savingsEstimated ? 'savings estimate' : 'savings unknown'}
            </Badge>
            <span className="font-mono text-[10px] text-muted-foreground">{delegation.id}</span>
            {delegation.task_id && (
              <Link
                to={delegation.workspace_id
                  ? `/tasks/${encodeURIComponent(delegation.task_id)}?workspace=${encodeURIComponent(delegation.workspace_id)}`
                  : `/tasks/${encodeURIComponent(delegation.task_id)}`}
                className="font-mono text-[10px] text-primary hover:underline"
              >
                {delegation.task_id}
              </Link>
            )}
          </div>

          <div className="grid grid-cols-2 gap-1.5 md:grid-cols-6">
            <Metric
              label="Parent Actual"
              value={parentTokensKnown ? formatTokens(agg.parent_tokens) : 'unknown'}
              title="Optional parent-client usage telemetry. Some parent clients do not report this reliably."
            />
            <Metric label="Worker Tokens" value={formatTokens(agg.total_tokens)} />
            <Metric
              label="Frontier Avoided"
              value={savingsEstimated ? formatSignedTokens(frontierAvoided) : 'needs estimate'}
              accent={frontierAvoided > 0 ? 'good' : undefined}
            />
            <Metric
              label="Worker Delta"
              value={savingsEstimated ? formatSignedTokens(workerDelta) : 'unknown'}
              accent={savingsEstimated ? (workerDelta >= 0 ? 'good' : 'warn') : undefined}
            />
            <Metric label="Worker Cost" value={formatCost(agg.cost_usd)} />
            <Metric
              label="Cost Saved"
              value={costSaved === '—' ? 'unknown' : costSaved}
              accent={agg.baseline_cost_usd > 0 && !agg.cost_all_missing ? (agg.estimated_cost_saved_usd >= 0 ? 'good' : 'warn') : undefined}
            />
          </div>

          <div className="grid grid-cols-2 gap-1.5 md:grid-cols-5">
            <Metric label="Real $ Spent" value={formatCost(agg.real_dollars_spent || 0)} />
            <Metric
              label="Real Cost Saved"
              value={formatSignedCost(agg.real_cost_saved_usd || 0)}
              accent={(agg.real_cost_saved_usd ?? 0) >= 0 ? 'good' : 'warn'}
            />
            <Metric label="Quota by Bucket" value={formatQuotaBuckets(agg.quota_tokens_by_bucket)} />
            {(agg.frontier_quota_burned ?? 0) > 0 ? (
              <Metric label="Frontier Quota Burned" value={formatTokens(agg.frontier_quota_burned ?? 0)} accent="burn" />
            ) : (
              <Metric
                label="Frontier Quota Preserved"
                value={formatTokens(agg.frontier_quota_preserved || 0)}
                accent={(agg.frontier_quota_preserved || 0) > 0 ? 'good' : undefined}
              />
            )}
          </div>

          <ContextRows delegation={delegation} onRunSettled={onReviewed} />

          {(reviewOpen || reviewed) && (
            <div>
              {!reviewOpen && reviewed ? (
                <button
                  type="button"
                  onClick={() => setReviewOpen(true)}
                  className="mb-2 text-[11px] text-muted-foreground hover:text-foreground"
                >
                  Show review notes
                </button>
              ) : null}
              {reviewOpen && <ReviewPanel delegation={delegation} onReviewed={onReviewed} />}
            </div>
          )}

          {needsReview && !reviewed && !reviewOpen && (
            <div className="flex justify-end border-t border-border/40 pt-2">
              <Button type="button" size="sm" variant="outline" onClick={openReview}>
                <Star className="mr-1.5 h-3.5 w-3.5" />
                Open review form
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function delegationModelSummary(delegation: DelegationContext) {
  const stats = delegation.model_stats?.length ? delegation.model_stats : fallbackModelStats(delegation)
  if (stats.length === 0) return 'no model'
  if (stats.length === 1) return summariseModel(stats[0].model_provider, stats[0].model_id)
  const primary = stats[0]
  return `${summariseModel(primary.model_provider, primary.model_id)} +${stats.length - 1}`
}

function InlineStat({
  label,
  value,
  accent,
  compact,
}: {
  label: string
  value: string
  accent?: 'good' | 'warn'
  compact?: boolean
}) {
  return (
    <div className={cn('tabular-nums', compact ? 'flex items-center gap-1' : 'text-right')}>
      <span
        className={cn(
          'font-mono font-medium',
          compact ? 'text-[11px]' : 'text-[12px]',
          accent === 'good' && 'text-emerald-300',
          accent === 'warn' && 'text-amber-300',
        )}
      >
        {value}
      </span>
      <span className={cn('uppercase tracking-wider text-muted-foreground', compact ? 'text-[8px]' : 'text-[9px]')}>
        {label}
      </span>
    </div>
  )
}

function ContextRows({
  delegation,
  onRunSettled,
}: {
  delegation: DelegationContext
  onRunSettled: () => void
}) {
  const parentTokensKnown = delegation.aggregate.parent_tokens_known || delegation.aggregate.parent_tokens > 0
  return (
    <div className="border border-border/60 bg-background/50">
      <div className="grid grid-cols-[1.2rem_1fr] gap-3 border-b border-border/40 px-3 py-2">
        <GitBranch className="mt-0.5 h-4 w-4 text-primary" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[12px] font-medium">Parent context</span>
            <Badge variant="outline" className="text-[10px] uppercase">
              {delegation.review_required ? 'review gated' : 'review optional'}
            </Badge>
            {delegation.parent.model && <Badge variant="outline">{delegation.parent.model}</Badge>}
            {delegation.parent.context_id && (
              <span className="font-mono text-[10px] text-muted-foreground">{delegation.parent.context_id}</span>
            )}
          </div>
          <div className="mt-1 grid grid-cols-2 gap-2 text-[11px] text-muted-foreground sm:grid-cols-4">
            <span>{parentTokensKnown ? `${formatTokens(delegation.parent.input_tokens || 0)} in` : 'parent tokens unknown'}</span>
            <span>{parentTokensKnown ? `${formatTokens(delegation.parent.output_tokens || 0)} out` : 'usage not reported'}</span>
            <span>{formatCost(delegation.parent.cost_usd || 0)}</span>
            <span>{delegation.parallel_total} child context{delegation.parallel_total === 1 ? '' : 's'}</span>
          </div>
        </div>
      </div>
      <div className="divide-y divide-border/40">
        {delegation.workers.map((w) => (
          <WorkerContextRow key={w.worker.id} row={w} onRunSettled={onRunSettled} />
        ))}
      </div>
    </div>
  )
}

function WorkerContextRow({
  row,
  onRunSettled,
}: {
  row: DelegationWorkerContext
  onRunSettled: () => void
}) {
  const run = row.latest_run
  const live = run?.status === 'running'
  const status = run?.status || 'dispatched'
  const output = run?.output_text?.replace(/\s+/g, ' ').trim() || ''
  const [cancelling, setCancelling] = useState(false)

  async function handleHardStop(e: MouseEvent) {
    e.preventDefault()
    e.stopPropagation()
    if (!live || !run || cancelling) return
    if (!window.confirm('Hard stop this delegated run? This kills the model subprocess immediately.')) return
    setCancelling(true)
    try {
      await cancelWorkerRun(run.id)
      toast.success('Hard stop requested')
      onRunSettled()
    } catch (err: unknown) {
      const m = err instanceof Error ? err.message : String(err || '')
      if (m.includes('409') || m.includes('not cancellable')) toast.info('Run already finished')
      else if (m.includes('404')) toast.error('Run not found')
      else toast.error('Failed to hard-stop run')
    } finally {
      setCancelling(false)
    }
  }

  return (
    <div className="grid grid-cols-[1.2rem_1fr] gap-3 px-3 py-2.5">
      <div className="pt-0.5">
        {live ? (
          <Loader2 className="h-4 w-4 animate-spin text-sky-300" />
        ) : status === 'success' ? (
          <CheckCircle2 className="h-4 w-4 text-emerald-300" />
        ) : status === 'failure' || status === 'cap_exceeded' ? (
          <AlertTriangle className="h-4 w-4 text-red-300" />
        ) : status === 'interrupted' ? (
          <AlertTriangle className="h-4 w-4 text-amber-300" />
        ) : (
          <Bot className="h-4 w-4 text-muted-foreground" />
        )}
      </div>
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline" className={statusBadgeClass(status)}>
            {status}
          </Badge>
          <Link to={`/workers/${row.worker.id}`} className="truncate text-[12px] font-medium hover:underline">
            {row.worker.name}
          </Link>
          <span className="font-mono text-[10px] text-muted-foreground">
            {row.parallel_index}/{row.parallel_total}
          </span>
          {live && run && (
            <Button
              size="sm"
              variant="destructive"
              className="h-5 px-1.5 text-[10px]"
              disabled={cancelling}
              onClick={handleHardStop}
              data-testid={`delegation-hard-stop-${run.id}`}
            >
              {cancelling ? <Loader2 className="mr-1 h-3 w-3 animate-spin" /> : <Square className="mr-1 h-3 w-3" />}
              {cancelling ? 'Stopping…' : 'Hard stop'}
            </Button>
          )}
        </div>
        <div className="mt-1 grid grid-cols-2 gap-2 text-[11px] text-muted-foreground md:grid-cols-5">
          <span>{summariseModel(row.worker.model_provider, row.worker.model_id)}</span>
          <span>{formatTokens((run?.input_tokens || 0) + (run?.output_tokens || 0))} tokens</span>
          <span>{formatCost(run?.cost_usd || 0)}</span>
          <span>
            {run?.tool_calls_count || 0} tool calls
            {run?.tool_calls_cap_scope === 'cli_audit' ? ' (cli)' : ''}
          </span>
          <span>{run?.started_at ? relativeTime(run.started_at) : 'waiting'}</span>
        </div>
        {run?.deliverable_status === 'spend_no_commit' && (
          <p className="mt-1 text-[11px] text-amber-300">
            Spent tokens/cost with no branch or commit reported — review before accepting.
          </p>
        )}
        {run?.deliverable_status === 'failed_no_output' && (
          <p className="mt-1 text-[11px] text-red-300">
            Failed with no usable output — operational failure, not a model-quality signal.
          </p>
        )}
        {run?.deliverable_status === 'success_with_output' && (run.deliverable_branch || run.deliverable_commit) && (
          <p className="mt-1 font-mono text-[10px] text-emerald-300/90">
            {run.deliverable_branch ? `branch ${run.deliverable_branch}` : ''}
            {run.deliverable_branch && run.deliverable_commit ? ' · ' : ''}
            {run.deliverable_commit ? `commit ${run.deliverable_commit.slice(0, 12)}` : ''}
          </p>
        )}
        {output && (
          <p className="mt-1 line-clamp-2 text-[11px] text-muted-foreground/80">{output}</p>
        )}
        {run?.error && (
          <p className="mt-1 line-clamp-2 text-[11px] text-destructive">{run.error}</p>
        )}
        {run && live && (
          <div className="mt-2">
            <WorkerLiveTail liveRun={run} onRunSettled={onRunSettled} />
          </div>
        )}
      </div>
    </div>
  )
}

function ReviewPanel({
  delegation,
  onReviewed,
}: {
  delegation: DelegationContext
  onReviewed: () => void
}) {
  const reviewed = Boolean(delegation.review?.reviewed)
  const [score, setScore] = useState(reviewed ? delegation.review?.score ?? 80 : 80)
  const [notes, setNotes] = useState(delegation.review?.notes || '')
  const [scoreAreas, setScoreAreas] = useState<Record<string, string>>(
    stringifyScoreAreas(delegation.review?.scores),
  )
  const [modelScores, setModelScores] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const stats = delegation.model_stats?.length ? delegation.model_stats : fallbackModelStats(delegation)

  useEffect(() => {
    const hasReview = Boolean(delegation.review?.reviewed)
    setScore(hasReview ? delegation.review?.score ?? 80 : 80)
    setNotes(delegation.review?.notes || '')
    setScoreAreas(stringifyScoreAreas(delegation.review?.scores))
    setModelScores({})
  }, [delegation.id, delegation.review?.reviewed, delegation.review?.score, delegation.review?.notes, delegation.review?.scores])

  async function submit() {
    setBusy(true)
    try {
      const capabilityScores = parseScoreAreas(scoreAreas)
      const perModelScores = parseModelScoreInputs(modelScores)
      await reviewDelegation(delegation.id, {
        workspace_id: delegation.workspace_id,
        score,
        notes,
        task_kind: delegation.task_kind || undefined,
        scores: Object.keys(capabilityScores).length ? capabilityScores : undefined,
        model_scores: perModelScores.length ? perModelScores : undefined,
      })
      toast.success('Delegation reviewed')
      onReviewed()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Review failed')
    } finally {
      setBusy(false)
    }
  }

  if (reviewed) {
    return (
      <div className="grid grid-cols-1 gap-3 border-t border-border/40 pt-3 md:grid-cols-[10rem_1fr]">
        <Field label="Score">
          <div className="flex h-10 items-center gap-2 rounded-md border border-border/60 bg-background/60 px-3">
            <Star className={cn('h-4 w-4', score >= 80 ? 'text-emerald-300' : score >= 50 ? 'text-amber-300' : 'text-red-300')} />
            <span className="font-mono text-sm font-semibold tabular-nums">{score}/100</span>
          </div>
        </Field>
        <Field label="Review Notes">
          <div className="max-h-72 overflow-auto rounded-md border border-border/60 bg-background/60 p-3 text-sm">
            {delegation.review?.notes ? (
              <Markdown source={delegation.review.notes} />
            ) : (
              <p className="text-sm text-muted-foreground">No review notes recorded.</p>
            )}
            {delegation.review?.scores && Object.keys(delegation.review.scores).length > 0 && (
              <div className="mt-3 flex flex-wrap gap-2">
                {Object.entries(delegation.review.scores).map(([key, value]) => (
                  <Badge key={key} variant="outline" className="text-[10px] uppercase">
                    {key}: {value}
                  </Badge>
                ))}
              </div>
            )}
          </div>
        </Field>
      </div>
    )
  }

  return (
    <div className="grid grid-cols-1 gap-3 border-t border-border/40 pt-3 md:grid-cols-[10rem_1fr_auto]">
      <Field label="Score">
        <div className="flex items-center gap-2">
          <Star className={cn('h-4 w-4', score >= 80 ? 'text-emerald-300' : score >= 50 ? 'text-amber-300' : 'text-red-300')} />
          <Input
            type="number"
            min={0}
            max={100}
            value={score}
            onChange={(e) => setScore(clampInt(e.target.value, 0, 100))}
          />
        </div>
      </Field>
      <Field label="Review Notes">
        <Textarea
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          className="min-h-20"
          placeholder={delegation.review_required ? 'Required: correctness, gaps, and whether this saved parent context.' : 'Correctness, gaps, and whether this saved parent context.'}
        />
      </Field>
      <div className="md:col-span-3">
        <ReviewScoreAreas values={scoreAreas} onChange={setScoreAreas} />
        {stats.length > 1 && (
          <ModelScoreInputs
            stats={stats}
            values={modelScores}
            onChange={setModelScores}
          />
        )}
      </div>
      <div className="flex items-end">
        <Button variant="outline" onClick={submit} disabled={busy}>
          {busy ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : <Play className="mr-1.5 h-4 w-4" />}
          Save review
        </Button>
      </div>
    </div>
  )
}

const reviewScoreKeys = ['coding', 'review', 'architecture', 'tool_calling', 'visual']

function ReviewScoreAreas({
  values,
  onChange,
}: {
  values: Record<string, string>
  onChange: (values: Record<string, string>) => void
}) {
  return (
    <div className="mb-3 grid grid-cols-2 gap-2 md:grid-cols-5">
      {reviewScoreKeys.map((key) => (
        <Field key={key} label={key.replace(/_/g, ' ')}>
          <Input
            type="number"
            min={0}
            max={100}
            value={values[key] ?? ''}
            onChange={(e) => onChange({ ...values, [key]: e.target.value })}
          />
        </Field>
      ))}
    </div>
  )
}

function ModelScoreInputs({
  stats,
  values,
  onChange,
}: {
  stats: DelegationModelStat[]
  values: Record<string, string>
  onChange: (values: Record<string, string>) => void
}) {
  return (
    <div className="mb-3 grid grid-cols-1 gap-2 md:grid-cols-2">
      {stats.map((stat) => (
        <Field key={stat.model_key} label={stat.model_id || stat.model_key}>
          <Input
            type="number"
            min={0}
            max={100}
            value={values[stat.model_key] ?? ''}
            onChange={(e) => onChange({ ...values, [stat.model_key]: e.target.value })}
          />
        </Field>
      ))}
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-[11px] uppercase tracking-wider text-muted-foreground">{label}</Label>
      {children}
    </div>
  )
}

function NumberField({
  label,
  value,
  onChange,
  step = '1',
}: {
  label: string
  value: number
  onChange: (v: number) => void
  step?: string
}) {
  return (
    <Field label={label}>
      <Input
        type="number"
        min={0}
        step={step}
        value={value}
        onChange={(e) => onChange(numberValue(e.target.value))}
      />
    </Field>
  )
}

function Metric({
  label,
  value,
  accent,
  title,
}: {
  label: string
  value: string
  accent?: 'good' | 'warn' | 'live' | 'burn'
  title?: string
}) {
  return (
    <div className="border border-border bg-card/40 px-3 py-2" title={title}>
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground">{label}</div>
      <div
        className={cn(
          'mt-1 font-mono text-[16px] font-semibold tabular-nums',
          accent === 'good' && 'text-emerald-300',
          accent === 'warn' && 'text-amber-300',
          accent === 'live' && 'text-sky-300',
          accent === 'burn' && 'text-red-400',
        )}
      >
        {value}
      </div>
    </div>
  )
}

function TinyStat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="font-mono text-[13px] tabular-nums">{value}</div>
      <div className="text-[9px] uppercase tracking-wider text-muted-foreground">{label}</div>
    </div>
  )
}

function summariseDelegations(rows: DelegationContext[]) {
  return rows.reduce(
    (acc, d) => {
      acc.parentTokens += d.aggregate.parent_tokens || 0
      if (d.aggregate.parent_tokens_known || d.aggregate.parent_tokens > 0) acc.parentKnown = true
      if ((d.aggregate.baseline_tokens || 0) > 0) acc.baselineKnown = true
      if ((d.aggregate.baseline_cost_usd || 0) > 0) acc.costBaselineKnown = true
      acc.workerTokens += d.aggregate.total_tokens || 0
      acc.frontierAvoided += frontierTokensAvoided(d.aggregate)
      acc.workerTokenDelta += workerTokenDelta(d.aggregate)
      acc.costSaved += d.aggregate.estimated_cost_saved_usd || 0
      acc.realDollarsSpent += d.aggregate.real_dollars_spent || 0
      acc.frontierQuotaPreserved += d.aggregate.frontier_quota_preserved || 0
      acc.frontierQuotaBurned += d.aggregate.frontier_quota_burned || 0
      return acc
    },
    {
      parentTokens: 0,
      parentKnown: false,
      baselineKnown: false,
      costBaselineKnown: false,
      workerTokens: 0,
      frontierAvoided: 0,
      workerTokenDelta: 0,
      costSaved: 0,
      realDollarsSpent: 0,
      frontierQuotaPreserved: 0,
      frontierQuotaBurned: 0,
    },
  )
}

function filterDelegationsByPeriod(rows: DelegationContext[], period: AnalysisPeriod) {
  const spec = ANALYSIS_PERIODS.find((row) => row.key === period)
  if (!spec?.ms) return rows
  const cutoff = Date.now() - spec.ms
  return rows.filter((row) => delegationActivityTime(row) >= cutoff)
}

function filterDelegationsBySearch(rows: DelegationContext[], query: string) {
  const q = query.trim().toLowerCase()
  if (!q) return rows
  return rows.filter((row) => delegationSearchText(row).includes(q))
}

function delegationActivityTime(row: DelegationContext) {
  const times = [
    row.created_at,
    row.updated_at,
    row.review?.reviewed_at,
    ...row.workers.flatMap((worker) => [
      worker.latest_run?.started_at,
      worker.latest_run?.finished_at,
      worker.worker.updated_at,
      worker.worker.created_at,
    ]),
  ]
    .map((value) => (value ? Date.parse(value) : NaN))
    .filter((value) => Number.isFinite(value))
  return times.length ? Math.max(...times) : 0
}

function delegationSearchText(row: DelegationContext) {
  return [
    row.id,
    row.objective,
    row.handoff,
    row.task_id,
    row.task_kind,
    row.status,
    row.review?.outcome,
    row.review?.notes,
    ...(row.model_stats?.flatMap((stat) => [stat.model_key, stat.model_provider, stat.model_id]) ?? []),
    ...row.workers.flatMap((worker) => [
      worker.worker.id,
      worker.worker.name,
      worker.worker.model_provider,
      worker.worker.model_id,
      worker.latest_run?.id,
      worker.latest_run?.status,
      worker.latest_run?.model_provider,
      worker.latest_run?.model_id,
      worker.latest_run?.output_text,
      worker.latest_run?.error,
    ]),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

function delegationFilterCounts(rows: DelegationContext[]) {
  return rows.reduce(
    (acc, d) => {
      acc.all += 1
      if (delegationNeedsAttention(d)) acc.attention += 1
      if (delegationNeedsReview(d)) acc.needsReview += 1
      if (delegationSucceeded(d)) acc.success += 1
      if (delegationFailed(d)) acc.failure += 1
      if (delegationInterrupted(d)) acc.interrupted += 1
      if (delegationCancelled(d)) acc.cancelled += 1
      if (d.review?.reviewed) acc.reviewed += 1
      return acc
    },
    { all: 0, attention: 0, needsReview: 0, success: 0, failure: 0, interrupted: 0, cancelled: 0, reviewed: 0 },
  )
}

function filterDelegations(rows: DelegationContext[], filter: DelegationFilter) {
  switch (filter) {
    case 'attention':
      return rows.filter(delegationNeedsAttention)
    case 'needs_review':
      return rows.filter(delegationNeedsReview)
    case 'success':
      return rows.filter(delegationSucceeded)
    case 'failure':
      return rows.filter(delegationFailed)
    case 'interrupted':
      return rows.filter(delegationInterrupted)
    case 'cancelled':
      return rows.filter(delegationCancelled)
    case 'reviewed':
      return rows.filter((d) => Boolean(d.review?.reviewed))
    default:
      return rows
  }
}

function buildDelegationSections(rows: DelegationContext[], filter: DelegationFilter) {
  if (filter !== 'all' && filter !== 'attention') {
    return [{ key: filter, title: filterLabel(filter), rows }]
  }
  const needsReview = rows.filter(delegationNeedsReview)
  const failed = rows.filter((d) => !delegationNeedsReview(d) && delegationFailed(d))
  const interrupted = rows.filter((d) => !delegationNeedsReview(d) && !delegationFailed(d) && delegationInterrupted(d))
  const cancelled = rows.filter((d) => !delegationNeedsReview(d) && !delegationFailed(d) && !delegationInterrupted(d) && delegationCancelled(d))
  const completed = rows.filter(
    (d) => !delegationNeedsReview(d) && !delegationFailed(d) && !delegationInterrupted(d) && !delegationCancelled(d),
  )
  return [
    { key: 'needs_review', title: 'Needs parent review', rows: needsReview },
    { key: 'failure', title: 'Failed or partial', rows: failed },
    { key: 'interrupted', title: 'Interrupted by daemon restart', rows: interrupted },
    { key: 'cancelled', title: 'Cancelled', rows: cancelled },
    { key: 'completed', title: 'Completed history', rows: completed },
  ].filter((section) => section.rows.length > 0)
}

function filterLabel(filter: DelegationFilter) {
  switch (filter) {
    case 'attention':
      return 'Attention'
    case 'needs_review':
      return 'Needs parent review'
    case 'success':
      return 'Success'
    case 'failure':
      return 'Failed or partial'
    case 'interrupted':
      return 'Interrupted'
    case 'cancelled':
      return 'Cancelled'
    case 'reviewed':
      return 'Reviewed'
    default:
      return 'All delegations'
  }
}

function delegationNeedsAttention(d: DelegationContext) {
  return delegationNeedsReview(d) || delegationFailed(d) || delegationInterrupted(d)
}

function delegationNeedsReview(d: DelegationContext) {
  return d.status === 'needs_review' || Boolean(d.review_required && !d.review?.reviewed)
}

function delegationIsRunning(d: DelegationContext) {
  return d.status === 'running' || (d.aggregate?.running || 0) > 0
}

function delegationIsLive(d: DelegationContext) {
  return delegationIsRunning(d) || d.status === 'dispatched' || (d.aggregate?.dispatched || 0) > 0
}

function delegationSucceeded(d: DelegationContext) {
  return d.status === 'success'
}

function delegationFailed(d: DelegationContext) {
  return d.status === 'failure' || d.status === 'partial' || (d.aggregate?.failure || 0) > 0
}

function delegationCancelled(d: DelegationContext) {
  return d.status === 'cancelled'
}

function delegationInterrupted(d: DelegationContext) {
  return d.status === 'interrupted' || (d.aggregate?.interrupted || 0) > 0
}

function periodLabel(period: AnalysisPeriod) {
  return ANALYSIS_PERIODS.find((row) => row.key === period)?.label ?? period
}

type ModelRankRow = {
  modelKey: string
  modelProvider: string
  modelID: string
  runs: number
  success: number
  failure: number
  running: number
  totalTokens: number
  costUSD: number
  unknownCostRuns: number
  durationMS: number
  unknownDurationMS: number
  avgDurationMS: number
  costKnown: boolean
  reviewCount: number
  avgScore: number
  successRate: number
  capabilityScores: Record<string, number>
}

function rankDelegationModels(rows: DelegationContext[]): ModelRankRow[] {
  const byKey = new Map<
    string,
    ModelRankRow & {
      scoreTotal: number
      capabilityTotals: Record<string, number>
      capabilityCounts: Record<string, number>
    }
  >()
  for (const d of rows) {
    const stats = d.model_stats?.length ? d.model_stats : fallbackModelStats(d)
    for (const stat of stats) {
      const key = stat.model_key || `${stat.model_provider}/${stat.model_id}`
      const row = byKey.get(key) ?? {
        modelKey: key,
        modelProvider: stat.model_provider,
        modelID: stat.model_id,
        runs: 0,
        success: 0,
        failure: 0,
        running: 0,
        totalTokens: 0,
        costUSD: 0,
        unknownCostRuns: 0,
        durationMS: 0,
        unknownDurationMS: 0,
        avgDurationMS: 0,
        costKnown: false,
        reviewCount: 0,
        avgScore: 0,
        successRate: 0,
        capabilityScores: {},
        scoreTotal: 0,
        capabilityTotals: {},
        capabilityCounts: {},
      }
      row.runs += stat.runs || 0
      row.success += stat.success || 0
      row.failure += stat.failure || 0
      row.running += stat.running || 0
      row.totalTokens += stat.total_tokens || 0
      row.costUSD += stat.cost_usd || 0
      row.unknownCostRuns += stat.unknown_cost_runs || 0
      row.durationMS += stat.duration_ms || 0
      row.unknownDurationMS += stat.unknown_duration_ms || 0
      if ((stat.review_count || 0) > 0) {
        row.reviewCount += stat.review_count || 0
        row.scoreTotal += (stat.review_score || 0) * (stat.review_count || 0)
        addCapabilityScores(row, stat.capability_scores)
      } else if (d.review?.reviewed && typeof d.review.score === 'number') {
        row.reviewCount += 1
        row.scoreTotal += d.review.score
        addCapabilityScores(row, d.review.scores)
      }
      byKey.set(key, row)
    }
  }
  return Array.from(byKey.values())
    .map((row) => {
      const knownRuns = Math.max(0, row.runs - row.unknownCostRuns)
      const knownSuccess = Math.max(0, row.success - row.unknownCostRuns)
      const knownDurationMS = Math.max(0, row.durationMS - row.unknownDurationMS)
      return {
        ...row,
        avgScore: row.reviewCount ? row.scoreTotal / row.reviewCount : 0,
        successRate: knownRuns ? knownSuccess / knownRuns : 0,
        avgDurationMS: knownRuns ? Math.round(knownDurationMS / knownRuns) : 0,
        costKnown: knownRuns > 0,
        capabilityScores: averageCapabilityScores(row.capabilityTotals, row.capabilityCounts),
      }
    })
    .filter((row) => row.reviewCount > 0 || row.runs > 0)
    .sort((a, b) => {
      if (b.avgScore !== a.avgScore) return b.avgScore - a.avgScore
      if (b.reviewCount !== a.reviewCount) return b.reviewCount - a.reviewCount
      if (b.successRate !== a.successRate) return b.successRate - a.successRate
      if (a.costKnown && b.costKnown && a.costUSD !== b.costUSD) return a.costUSD - b.costUSD
      return a.avgDurationMS - b.avgDurationMS
    })
}

function fallbackModelStats(d: DelegationContext): DelegationModelStat[] {
  const byKey = new Map<string, DelegationModelStat>()
  for (const row of d.workers) {
    const run = row.latest_run
    const provider = run?.model_provider || row.worker.model_provider
    const modelID = run?.model_id || row.worker.model_id
    const key = `${provider}/${modelID}`
    const stat = byKey.get(key) ?? {
      model_provider: provider,
      model_id: modelID,
      model_key: key,
      runs: 0,
      success: 0,
      failure: 0,
      running: 0,
      cancelled: 0,
      interrupted: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
      cost_usd: 0,
      unknown_cost_runs: 0,
      duration_ms: 0,
      avg_duration_ms: 0,
      unknown_duration_ms: 0,
      review_count: 0,
      review_score: 0,
      worker_ids: [],
    }
    stat.worker_ids = [...(stat.worker_ids || []), row.worker.id]
    if (run) {
      if (run.status === 'cancelled') {
        stat.cancelled = (stat.cancelled || 0) + 1
        continue
      }
      if (run.status === 'interrupted') {
        stat.interrupted = (stat.interrupted || 0) + 1
        continue
      }
      stat.runs += 1
      if (run.status === 'success') stat.success += 1
      if (['failure', 'cap_exceeded', 'paused', 'rejected'].includes(run.status)) stat.failure += 1
      if (['running', 'awaiting_approval'].includes(run.status)) stat.running += 1
      stat.input_tokens += run.input_tokens || 0
      stat.output_tokens += run.output_tokens || 0
      stat.total_tokens += (run.input_tokens || 0) + (run.output_tokens || 0)
      stat.cost_usd += run.cost_usd || 0
      stat.duration_ms += run.duration_ms || 0
      if (run.accounting_missing || isRunAccountingMissing(run)) {
        stat.unknown_cost_runs = (stat.unknown_cost_runs || 0) + 1
        stat.unknown_duration_ms = (stat.unknown_duration_ms || 0) + (run.duration_ms || 0)
      }
    }
    if (d.review?.reviewed && typeof d.review.score === 'number') {
      stat.review_count = 1
      stat.review_score = d.review.score
      stat.task_kind = d.review.task_kind || d.task_kind
      stat.capability_scores = d.review.scores
    }
    byKey.set(key, stat)
  }
  return Array.from(byKey.values()).map((stat) => ({
    ...stat,
    avg_duration_ms:
      stat.runs && stat.runs > (stat.unknown_cost_runs || 0)
        ? Math.round(
            (stat.duration_ms - (stat.unknown_duration_ms || 0)) /
              (stat.runs - (stat.unknown_cost_runs || 0)),
          )
        : 0,
  }))
}

function isRunAccountingMissing(run: DelegationWorkerContext['latest_run']) {
  if (!run) return false
  return run.status === 'success' && !run.input_tokens && !run.output_tokens && !run.cost_usd
}

function addCapabilityScores(
  row: { capabilityTotals: Record<string, number>; capabilityCounts: Record<string, number> },
  scores?: Record<string, number>,
) {
  if (!scores) return
  for (const [key, value] of Object.entries(scores)) {
    if (!Number.isFinite(value)) continue
    row.capabilityTotals[key] = (row.capabilityTotals[key] || 0) + value
    row.capabilityCounts[key] = (row.capabilityCounts[key] || 0) + 1
  }
}

function averageCapabilityScores(
  totals: Record<string, number>,
  counts: Record<string, number>,
) {
  const out: Record<string, number> = {}
  for (const [key, value] of Object.entries(totals)) {
    const count = counts[key] || 0
    if (count > 0) out[key] = value / count
  }
  return out
}

function frontierTokensAvoided(agg: DelegationContext['aggregate']) {
  return agg.frontier_tokens_avoided ?? agg.estimated_parent_tokens_saved ?? 0
}

function workerTokenDelta(agg: DelegationContext['aggregate']) {
  return agg.worker_token_delta ?? agg.net_tokens_delta ?? 0
}

function statusClass(status: string) {
  if (status === 'success') return statusBadgeClass('success')
  if (status === 'running') return statusBadgeClass('running')
  if (status === 'partial') return statusBadgeClass('partial')
  if (status === 'interrupted') return statusBadgeClass('paused')
  if (status === 'needs_review' || status === 'failure') return statusBadgeClass('failure')
  return statusBadgeClass('paused')
}

function formatTokens(n: number) {
  if (!n) return '0'
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}m`
  if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function formatSignedTokens(n: number) {
  if (!n) return '0'
  return `${n > 0 ? '+' : ''}${formatTokens(n)}`
}

function formatCost(n: number) {
  return `$${(n || 0).toFixed(4)}`
}

function formatDuration(ms: number) {
  if (!ms) return '0s'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`
  return `${Math.round(ms / 60_000)}m`
}

function formatSignedCost(n: number) {
  return `${n >= 0 ? '+' : '-'}$${Math.abs(n || 0).toFixed(4)}`
}

function formatQuotaBuckets(buckets?: Record<string, number>) {
  if (!buckets || Object.keys(buckets).length === 0) return 'none'
  return Object.entries(buckets)
    .filter(([, v]) => (v || 0) > 0)
    .map(([k, v]) => `${k}: ${formatTokens(v)}`)
    .join(' · ') || 'none'
}

function numberValue(v: string) {
  const n = Number(v)
  return Number.isFinite(n) && n >= 0 ? n : 0
}

function clampInt(v: string, min: number, max: number) {
  const n = Math.round(numberValue(v))
  return Math.min(max, Math.max(min, n))
}

function nonzero(n: number) {
  return n > 0 ? n : undefined
}

function parseModelCandidates(raw: string): DelegationModelCandidate[] | undefined {
  const trimmed = raw.trim()
  if (!trimmed) return undefined
  const parsed = JSON.parse(trimmed) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('Candidate models must be a JSON array')
  }
  return parsed.map((row, idx) => {
    if (!row || typeof row !== 'object') {
      throw new Error(`Candidate ${idx} must be an object`)
    }
    const candidate = row as DelegationModelCandidate
    if (!candidate.model_profile_id && (!candidate.model_provider || !candidate.model_id)) {
      throw new Error(`Candidate ${idx} needs a profile or provider/model`)
    }
    return candidate
  })
}

function stringifyScoreAreas(scores?: Record<string, number>) {
  return Object.fromEntries(
    reviewScoreKeys.map((key) => [
      key,
      typeof scores?.[key] === 'number' ? String(scores[key]) : '',
    ]),
  )
}

function parseScoreAreas(values: Record<string, string>) {
  const out: Record<string, number> = {}
  for (const [key, raw] of Object.entries(values)) {
    if (!raw.trim()) continue
    out[key] = clampInt(raw, 0, 100)
  }
  return out
}

function parseModelScoreInputs(values: Record<string, string>) {
  return Object.entries(values)
    .filter(([, raw]) => raw.trim() !== '')
    .map(([modelKey, raw]) => ({
      model_key: modelKey,
      score: clampInt(raw, 0, 100),
    }))
}
