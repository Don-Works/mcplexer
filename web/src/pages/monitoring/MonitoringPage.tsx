// MonitoringPage — the workspace's remote-log-intelligence command
// surface (docs/design/remote-log-intelligence.md): what we watch
// (hosts + sources), where alerts go (channels with severity floors),
// what the distiller learned (templates + digest), who runs the jobs
// (peer responsibilities), and the log-watch worker's state.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import { listAuthScopes, listWorkspaces, request } from '@/api/client'
import { listWorkers } from '@/api/workers'
import {
  listChannels, listLogSources, listRemoteHosts, listTemplates, monitoringStatus,
} from '@/api/monitoring'
import type { MonitoringStatus, RemoteHost } from '@/api/monitoring'
import { RunnerStrip } from './RunnerStrip'
import { HostsSection } from './HostsSection'
import { SourcesSection } from './SourcesSection'
import { ChannelsSection } from './ChannelsSection'
import { TemplatesSection } from './TemplatesSection'
import { DigestPanel } from './DigestPanel'

const WORKSPACE_PROBE_TIMEOUT_MS = 10_000

function probeWorkspaceHosts(workspaceId: string, signal: AbortSignal) {
  return request<RemoteHost[]>(
    `/remote-hosts?workspace_id=${encodeURIComponent(workspaceId)}`,
    { signal },
    { timeoutMs: WORKSPACE_PROBE_TIMEOUT_MS },
  )
}

export function MonitoringPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [workspaceId, setWorkspaceId] = useState(() => searchParams.get('workspace') ?? '')
  const [workspaceResolved, setWorkspaceResolved] = useState(false)
  const [status, setStatus] = useState<MonitoringStatus | null>(null)

  const { data: workspaces, loading: workspacesLoading } = useApi(useCallback(() => listWorkspaces(), []))
  const { data: authScopes } = useApi(useCallback(() => listAuthScopes(), []))

  useEffect(() => {
    monitoringStatus().then(setStatus).catch(() => setStatus(null))
  }, [])
  useEffect(() => {
    const requestedWorkspaceId = searchParams.get('workspace') ?? ''
    if (requestedWorkspaceId !== workspaceId) {
      setWorkspaceId(requestedWorkspaceId)
      setWorkspaceResolved(false)
    }
  }, [searchParams, workspaceId])
  useEffect(() => {
    if (workspacesLoading) {
      setWorkspaceResolved(false)
      return
    }
    if (!workspaces || workspaces.length === 0) {
      setWorkspaceResolved(true)
      return
    }
    if (workspaces.some(w => w.id === workspaceId)) {
      setWorkspaceResolved(true)
      return
    }

    setWorkspaceResolved(false)
    const controller = new AbortController()
    let cancelled = false
    const candidates = workspaces.filter(w => w.name.trim().toLowerCase() !== 'global')
    void Promise.all(candidates.map(async workspace => ({
      workspace,
      hosts: await probeWorkspaceHosts(workspace.id, controller.signal).catch(() => []),
    }))).then(rows => {
      if (cancelled) return
      const preferred = rows.find(row => row.hosts.length > 0)?.workspace
        ?? candidates[0]
        ?? workspaces[0]
      setWorkspaceId(preferred.id)
      const next = new URLSearchParams(searchParams)
      next.set('workspace', preferred.id)
      setSearchParams(next, { replace: true })
    }).catch(() => {
      if (!cancelled) setWorkspaceResolved(true)
    })
    return () => {
      cancelled = true
      controller.abort()
    }
  }, [searchParams, setSearchParams, workspaceId, workspaces, workspacesLoading])

  const activeWorkspaceId = workspaceResolved && workspaces?.some(w => w.id === workspaceId)
    ? workspaceId
    : ''
  const hostsFetcher = useCallback(
    () => (activeWorkspaceId ? listRemoteHosts(activeWorkspaceId) : Promise.resolve([])), [activeWorkspaceId])
  const sourcesFetcher = useCallback(
    () => (activeWorkspaceId ? listLogSources(activeWorkspaceId) : Promise.resolve([])), [activeWorkspaceId])
  const channelsFetcher = useCallback(
    () => (activeWorkspaceId ? listChannels(activeWorkspaceId) : Promise.resolve([])), [activeWorkspaceId])
  const templatesFetcher = useCallback(
    () => (activeWorkspaceId
      ? listTemplates(activeWorkspaceId)
      : Promise.resolve({ templates: [], window: '24h' })), [activeWorkspaceId])
  const workerFetcher = useCallback(
    () => (activeWorkspaceId
      ? listWorkers({ workspaceId: activeWorkspaceId, namePattern: 'log-watch' })
      : Promise.resolve([])), [activeWorkspaceId])

  const { data: hosts, refetch: refetchHosts } = useApi(hostsFetcher)
  const { data: sources, refetch: refetchSources } = useApi(sourcesFetcher)
  const { data: channels, refetch: refetchChannels } = useApi(channelsFetcher)
  const { data: templatesRes, refetch: refetchTemplates } = useApi(templatesFetcher)
  const { data: watchWorkers } = useApi(workerFetcher)

  const watchWorker = useMemo(
    () => (watchWorkers ?? []).find(w => w.name === 'log-watch'),
    [watchWorkers])

  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Monitoring</h1>
          <p className="text-sm text-muted-foreground">
            Remote docker logs, distilled to templates. The worker only ever reads
            the digest; this feature is read-only against every watched box.
          </p>
        </div>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          Workspace
          <select
            aria-label="Monitoring workspace"
            className="border border-border bg-background px-2 py-1.5 text-sm text-foreground"
            value={workspaceId}
            onChange={e => {
              const id = e.target.value
              setWorkspaceId(id)
              const next = new URLSearchParams(searchParams)
              next.set('workspace', id)
              setSearchParams(next, { replace: true })
            }}
          >
            {(workspaces ?? []).map(w => (
              <option key={w.id} value={w.id}>{w.name}</option>
            ))}
          </select>
        </label>
      </div>

      <RunnerStrip status={status} />

      <div className="flex items-center gap-3 border border-border px-4 py-3 text-sm">
        <span className="text-muted-foreground">log-watch worker</span>
        {watchWorker ? (
          <>
            <Badge tone={watchWorker.enabled ? 'success' : 'muted'}>
              {watchWorker.enabled ? 'installed · enabled' : 'installed · paused'}
            </Badge>
            <span className="text-xs text-muted-foreground">
              wakes every {watchWorker.schedule_spec} + on logwatch alerts; quiet
              ticks abort before any model spend
            </span>
            <Link className="ml-auto text-xs text-primary hover:underline"
              to={`/workers/${watchWorker.id}`}>
              open worker
            </Link>
          </>
        ) : (
          <>
            <Badge tone="warn">not installed</Badge>
            <span className="text-xs text-muted-foreground">
              install the log-watch template from the Workers page, or set
              MCPLEXER_AUTO_INSTALL_LOG_WATCH=1 on the runner daemon
            </span>
            <Link className="ml-auto text-xs text-primary hover:underline" to="/workers">
              open workers
            </Link>
          </>
        )}
      </div>

      {activeWorkspaceId && (
        <>
          <HostsSection workspaceId={activeWorkspaceId} hosts={hosts ?? []}
            authScopes={authScopes ?? []} refetch={refetchHosts} />
          <SourcesSection workspaceId={activeWorkspaceId} sources={sources ?? []}
            hosts={hosts ?? []} refetch={refetchSources} />
          <ChannelsSection workspaceId={activeWorkspaceId} channels={channels ?? []}
            hosts={hosts ?? []} notifyEnabled={status?.notify_enabled ?? false}
            refetch={refetchChannels} />
          <TemplatesSection workspaceId={activeWorkspaceId}
            templates={templatesRes?.templates ?? []}
            refetch={refetchTemplates} />
          <DigestPanel workspaceId={activeWorkspaceId} />
        </>
      )}
    </div>
  )
}
