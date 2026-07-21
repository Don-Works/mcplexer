// DashboardPage — the operator's "morning glance" landing surface.
//
// Information architecture, top to bottom:
//   1. Header — title + state tag + time range
//   2. Vitals — 6 dense chips (sessions, mesh, workers, approvals, memory, skills)
//   3. Needs your attention — gathered actionable items, deep-linked
//   4. Right now — combined live tail of audit + signal events
//   5. Recent activity — last-5 worker runs / memories / mesh signals
//   6. Trends — collapsible, the previous charts/leaderboards/server health
//
// Hard rule: zero new SSE subscriptions. The page reuses the global
// approval stream, audit stream, signal stream + a single GET on workers
// + memories every 30s. Chrome's 6-per-origin HTTP/1.1 cap remains safe.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useApi } from '@/hooks/use-api'
import { useInterval } from '@/hooks/use-interval'
import { useAuditStream } from '@/hooks/use-audit-stream'
import { useApprovalStream } from '@/hooks/use-approval-stream'
import { useSignal } from '@/components/notifications/use-signal'
import {
  getDashboard,
  getMeshStatus,
  listAuthScopes,
  listSkillRegistry,
  listWorkspaces,
} from '@/api/client'
import { listMemoryOffers, countMemories } from '@/api/memory'
import { listWorkers } from '@/api/workers'
import type { AuditRecord, SessionInfo } from '@/api/types'
import { useConnectionHealth } from '@/components/connections/use-connection-health'

import { type TimeRange } from './chart-components'
import { VitalsStrip, type VitalItem } from './vitals-strip'
import { AttentionCard } from './attention-card'
import { IncidentsPanel } from './incidents-panel'
import { RightNowStream } from './right-now-stream'
import { RecentActivityGrid } from './recent-activity-grid'
import { TrendsSection } from './trends-section'
import {
  DashboardHeader,
  ErrorState,
  FirstRun,
  LoadingState,
} from './dashboard-shell'

const VALID_RANGES: TimeRange[] = ['1h', '6h', '24h', '7d']
const ATTENTION_REFRESH_MS = 30_000

export function DashboardPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [range, setRange] = useState<TimeRange>(() => initialRange(searchParams))

  // Sync `?range=` to URL when it changes.
  useEffect(() => {
    const current = searchParams.get('range')
    if (range === '1h' && !current) return
    if (current === range) return
    const next = new URLSearchParams(searchParams)
    if (range === '1h') next.delete('range')
    else next.set('range', range)
    setSearchParams(next, { replace: true })
  }, [range, searchParams, setSearchParams])

  const dashFetcher = useCallback(() => getDashboard(range), [range])
  const { data, loading, error, refetch } = useApi(dashFetcher)
  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)
  const authFetcher = useCallback(() => listAuthScopes(), [])
  const { data: authScopes } = useApi(authFetcher)
  const wsName = (id: string) => workspaces?.find((w) => w.id === id)?.name ?? id
  const asName = (id: string) => authScopes?.find((a) => a.id === id)?.name ?? id

  const workerFetcher = useCallback(() => listWorkers(), [])
  const { data: workers, refetch: refetchWorkers } = useApi(workerFetcher)
  const memCountFetcher = useCallback(() => countMemories(), [])
  const { data: memCount, refetch: refetchMemCount } = useApi(memCountFetcher)
  const memOffersFetcher = useCallback(
    () => listMemoryOffers({ pending_only: true }),
    [],
  )
  const { data: memOffers, refetch: refetchMemOffers } = useApi(memOffersFetcher)
  const skillsFetcher = useCallback(() => listSkillRegistry({ mode: 'all' }), [])
  const { data: skills, refetch: refetchSkills } = useApi(skillsFetcher)
  const meshFetcher = useCallback(() => getMeshStatus(), [])
  const { data: mesh, refetch: refetchMesh } = useApi(meshFetcher)

  const { records: liveRecords, connected } = useAuditStream({})
  const { pending: pendingApprovals } = useApprovalStream()
  const { events: signalEvents } = useSignal()

  const { serversNeedingCreds, workspacesWithoutRoutes } = useConnectionHealth()

  useInterval(refetch, 10_000)
  useInterval(refetchWorkers, ATTENTION_REFRESH_MS)
  useInterval(refetchMesh, ATTENTION_REFRESH_MS)
  useInterval(refetchMemCount, ATTENTION_REFRESH_MS)
  useInterval(refetchMemOffers, ATTENTION_REFRESH_MS)
  useInterval(refetchSkills, 60_000)

  const recentCalls = useMemo<AuditRecord[]>(() => {
    const dbRecords = data?.recent_calls ?? []
    const seen = new Set(liveRecords.map((r) => r.id))
    const merged = [...liveRecords, ...dbRecords.filter((r) => !seen.has(r.id))]
    merged.sort(
      (a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime(),
    )
    return merged.slice(0, 50)
  }, [liveRecords, data?.recent_calls])

  const activeSessions = data?.active_session_list ?? ([] as SessionInfo[])

  if (loading && !data) return <LoadingState onRetry={refetch} />
  if (error && !data) return <ErrorState message={error} onRetry={refetch} />
  if (!data) return null

  const totalDownstreams = (data.active_downstreams ?? []).length
  const totalRequests = data.stats?.total_requests ?? 0
  const needsWorkspaceSetup = workspacesWithoutRoutes.length > 0
  const needsCredentialSetup = serversNeedingCreds.length > 0
  const shouldShowFirstRun =
    totalRequests === 0 &&
    (totalDownstreams === 0 || needsWorkspaceSetup || needsCredentialSetup)
  if (shouldShowFirstRun) return <FirstRun />

  const totalMemories = (memCount?.facts ?? 0) + (memCount?.notes ?? 0)
  const unhealthyWorkers =
    workers?.filter((w) => {
      const s = w.last_run_status
      return s === 'failure' || s === 'cap_exceeded' || s === 'rejected'
    }).length ?? 0
  const runningWorkers =
    workers?.filter((w) => w.last_run_status === 'running').length ?? 0
  const meshAgents = mesh?.agents?.length ?? 0
  const peersOnline = data.peers_online ?? 0
  const peersTotal = data.peers_total ?? 0
  const offlinePeers = Math.max(0, peersTotal - peersOnline)

  const errorRateNum =
    data.stats && data.stats.total_requests > 0
      ? ((data.stats.error_count ?? 0) / data.stats.total_requests) * 100
      : 0
  const unreviewedDelegations = data?.unreviewed_delegations ?? 0
  const inTrouble =
    pendingApprovals.length > 0 ||
    unhealthyWorkers > 0 ||
    (data.stats?.blocked_count ?? 0) > 0 ||
    errorRateNum >= 5 ||
    unreviewedDelegations > 0
  const hasTraffic =
    activeSessions.length > 0 || recentCalls.length > 0 || totalRequests > 0
  const dashboardState: 'trouble' | 'busy' | 'quiet' = inTrouble
    ? 'trouble'
    : hasTraffic
      ? 'busy'
      : 'quiet'

  const vitalItems = buildVitals({
    sessions: activeSessions.length,
    meshAgents,
    runningWorkers,
    pendingApprovals: pendingApprovals.length,
    totalMemories,
    skillCount: skills?.length ?? 0,
  })

  return (
    <div className="space-y-5">
      <DashboardHeader
        state={dashboardState}
        range={range}
        setRange={setRange}
        ranges={VALID_RANGES}
      />

      <VitalsStrip items={vitalItems} />

      <AttentionCard
        input={{
          pendingApprovals: pendingApprovals.length,
          pendingWorkerApprovals:
            workers?.filter((w) => w.last_run_status === 'awaiting_approval')
              .length ?? 0,
          unhealthyWorkers,
          offlinePeers,
          pendingMemoryOffers: memOffers?.length ?? 0,
          expiringSecrets: 0,
          serversNeedingCreds,
          workspacesWithoutRoutes,
          unreviewedDelegations,
        }}
      />

      <IncidentsPanel />

      <RightNowStream
        audit={recentCalls.slice(0, 30)}
        signal={signalEvents.slice(0, 40)}
        connected={connected}
        wsName={wsName}
      />

      <RecentActivityGrid
        workers={workers ?? []}
        signals={signalEvents}
      />

      <TrendsSection
        data={data}
        sessions={activeSessions}
        wsName={wsName}
        asName={asName}
      />
    </div>
  )
}

// --- small atoms ---------------------------------------------------------

function initialRange(params: URLSearchParams): TimeRange {
  const r = params.get('range') as TimeRange | null
  return r && VALID_RANGES.includes(r) ? r : '1h'
}

function buildVitals(input: {
  sessions: number
  meshAgents: number
  runningWorkers: number
  pendingApprovals: number
  totalMemories: number
  skillCount: number
}): VitalItem[] {
  return [
    {
      label: 'Live sessions',
      value: input.sessions,
      detail: input.sessions > 0 ? 'agents talking to the gateway' : 'no sessions',
      href: '/audit',
      tone: input.sessions > 0 ? 'live' : 'idle',
    },
    {
      label: 'Mesh agents',
      value: input.meshAgents,
      detail:
        input.meshAgents > 0 ? 'connected across local + peers' : 'no agents',
      href: '/mesh',
      tone: input.meshAgents > 0 ? 'live' : 'idle',
    },
    {
      label: 'Workers running',
      value: input.runningWorkers,
      detail:
        input.runningWorkers > 0 ? 'chewing tokens right now' : 'all idle',
      href: '/workers',
      tone: input.runningWorkers > 0 ? 'live' : 'idle',
    },
    {
      label: 'Pending approvals',
      value: input.pendingApprovals,
      detail:
        input.pendingApprovals > 0
          ? 'waiting on your decision'
          : 'all cleared',
      href: '/approvals',
      tone:
        input.pendingApprovals === 0
          ? 'idle'
          : input.pendingApprovals > 3
            ? 'critical'
            : 'warn',
    },
    {
      label: 'Memories',
      value: input.totalMemories,
      detail:
        input.totalMemories > 0
          ? 'persistent facts + notes'
          : 'not learning yet',
      href: '/memory',
      tone: input.totalMemories > 0 ? 'info' : 'idle',
    },
    {
      label: 'Skills',
      value: input.skillCount,
      detail:
        input.skillCount > 0
          ? 'recipes in the shared library'
          : 'no skills published',
      href: '/skills',
      tone: input.skillCount > 0 ? 'info' : 'idle',
    },
  ]
}
