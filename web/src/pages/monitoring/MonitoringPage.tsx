// MonitoringPage — the workspace's remote-log-intelligence command
// surface (docs/design/remote-log-intelligence.md): what we watch
// (hosts + sources), where alerts go (channels with severity floors),
// what the distiller learned (templates + digest), who runs the jobs
// (peer responsibilities), and the log-watch worker's state.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import { listAuthScopes, listWorkspaces } from '@/api/client'
import { listWorkers } from '@/api/workers'
import {
  listChannels, listLogSources, listRemoteHosts, listTemplates, monitoringStatus,
} from '@/api/monitoring'
import type { MonitoringStatus } from '@/api/monitoring'
import { RunnerStrip } from './RunnerStrip'
import { HostsSection } from './HostsSection'
import { SourcesSection } from './SourcesSection'
import { ChannelsSection } from './ChannelsSection'
import { TemplatesSection } from './TemplatesSection'
import { DigestPanel } from './DigestPanel'

export function MonitoringPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [workspaceId, setWorkspaceId] = useState(() => searchParams.get('workspace') ?? '')
  const [status, setStatus] = useState<MonitoringStatus | null>(null)

  const { data: workspaces } = useApi(useCallback(() => listWorkspaces(), []))
  const { data: authScopes } = useApi(useCallback(() => listAuthScopes(), []))

  useEffect(() => {
    monitoringStatus().then(setStatus).catch(() => setStatus(null))
  }, [])
  useEffect(() => {
    if (!workspaces || workspaces.length === 0 || workspaces.some(w => w.id === workspaceId)) return

    // The global workspace is often first alphabetically but intentionally has
    // no hosts. Landing there made a healthy watcher look completely empty.
    // Probe the small workspace list and open the first real monitoring
    // workspace instead; explicit ?workspace= deep links still win above.
    let cancelled = false
    const candidates = workspaces.filter(w => w.name.trim().toLowerCase() !== 'global')
    void Promise.all(candidates.map(async workspace => ({
      workspace,
      hosts: await listRemoteHosts(workspace.id).catch(() => []),
    }))).then(rows => {
      if (cancelled) return
      const preferred = rows.find(row => row.hosts.length > 0)?.workspace
        ?? candidates[0]
        ?? workspaces[0]
      setWorkspaceId(preferred.id)
      const next = new URLSearchParams(searchParams)
      next.set('workspace', preferred.id)
      setSearchParams(next, { replace: true })
    })
    return () => { cancelled = true }
  }, [searchParams, setSearchParams, workspaces, workspaceId])

  const hostsFetcher = useCallback(
    () => (workspaceId ? listRemoteHosts(workspaceId) : Promise.resolve([])), [workspaceId])
  const sourcesFetcher = useCallback(
    () => (workspaceId ? listLogSources(workspaceId) : Promise.resolve([])), [workspaceId])
  const channelsFetcher = useCallback(
    () => (workspaceId ? listChannels(workspaceId) : Promise.resolve([])), [workspaceId])
  const templatesFetcher = useCallback(
    () => (workspaceId
      ? listTemplates(workspaceId)
      : Promise.resolve({ templates: [], window: '24h' })), [workspaceId])
  const workerFetcher = useCallback(
    () => (workspaceId
      ? listWorkers({ workspaceId, namePattern: 'log-watch' })
      : Promise.resolve([])), [workspaceId])

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

      {workspaceId && (
        <>
          <HostsSection workspaceId={workspaceId} hosts={hosts ?? []}
            authScopes={authScopes ?? []} refetch={refetchHosts} />
          <SourcesSection workspaceId={workspaceId} sources={sources ?? []}
            hosts={hosts ?? []} refetch={refetchSources} />
          <ChannelsSection workspaceId={workspaceId} channels={channels ?? []}
            hosts={hosts ?? []} notifyEnabled={status?.notify_enabled ?? false}
            refetch={refetchChannels} />
          <TemplatesSection templates={templatesRes?.templates ?? []}
            refetch={refetchTemplates} />
          <DigestPanel workspaceId={workspaceId} />
        </>
      )}
    </div>
  )
}
