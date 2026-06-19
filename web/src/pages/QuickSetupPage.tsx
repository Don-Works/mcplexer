import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { CopyButton } from '@/components/ui/copy-button'
import { StepIndicator, type StepDef } from '@/components/ui/step-indicator'
import { useApi } from '@/hooks/use-api'
import {
  connectDownstream,
  createDownstream,
  createRoute,
  getDownstreamOAuthStatus,
  getOAuthCapabilities,
  listDownstreams,
  listWorkspaces,
} from '@/api/client'
import type { DownstreamOAuthStatusEntry, DownstreamServer, OAuthCapabilities } from '@/api/types'
import {
  AlertCircle,
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  Clock,
  Code2,
  ExternalLink,
  FileCode,
  Loader2,
  Plus,
  RotateCcw,
  Search,
  Terminal,
  Zap,
} from 'lucide-react'
import { CATEGORY_LABELS, CATEGORY_ORDER, toCatalogEntry, type CatalogEntry, type ServerCategory } from '@/data/server-catalog'
import { fetchCatalog } from '@/api/client'
import { redirectToOAuth } from '@/lib/safe-redirect'
import { formatRelativeTime } from '@/lib/utils'
import { StdioServerForm, type StdioFormValues } from '@/components/setup/StdioServerForm'
import { CustomMCPForm } from '@/components/setup/CustomMCPForm'
import { OpenAPIImportForm } from '@/components/setup/OpenAPIImportForm'

// ── Step model ──────────────────────────────────────────────────────────────

/**
 * pick           — tile grid (existing downstreams + new-server tiles)
 * stdio_form     — form to create a new stdio downstream
 * custom_mcp_form — form to create a new Custom MCP addon
 * openapi_form   — form to import an OpenAPI spec as an addon
 * configure      — OAuth credentials for an HTTP downstream
 * workspace      — multi-select workspaces (all paths)
 * review         — summary before commit
 * connecting     — in-flight OAuth redirect
 * success        — done
 */
type Step =
  | 'pick'
  | 'stdio_form'
  | 'custom_mcp_form'
  | 'openapi_form'
  | 'configure'
  | 'workspace'
  | 'review'
  | 'connecting'
  | 'success'

/** How the server will be connected (determines what "submit" does). */
type ConnectMode =
  | 'oauth'      // existing HTTP downstream — use connectDownstream (OAuth)
  | 'route_only' // existing stdio/internal downstream — just create route(s)
  | 'new_routes' // server was just created by a sub-form; just create route(s)

function buildSteps(mode: ConnectMode, skipsConfig: boolean): StepDef[] {
  const steps: StepDef[] = []
  if (mode === 'oauth' && !skipsConfig) steps.push({ id: 'configure', label: 'Credentials' })
  steps.push({ id: 'workspace', label: 'Workspace' })
  steps.push({ id: 'review', label: 'Review' })
  return steps
}

// ── Main page ────────────────────────────────────────────────────────────────

export function QuickSetupPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [stepStack, setStepStack] = useState<Step[]>(['pick'])
  const [selectedDs, setSelectedDs] = useState<DownstreamServer | null>(null)
  const [caps, setCaps] = useState<OAuthCapabilities | null>(null)
  const [connectMode, setConnectMode] = useState<ConnectMode>('oauth')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  // Pick step search and filter
  const [pickQuery, setPickQuery] = useState('')
  const [pickFilter, setPickFilter] = useState<'all' | 'connected' | 'needs-auth' | 'one-click' | 'not-connected'>('all')
  // Multi-select workspace IDs
  const [selectedWorkspaceIds, setSelectedWorkspaceIds] = useState<string[]>([])
  const [accountLabel, setAccountLabel] = useState('')
  const [connectError, setConnectError] = useState<string | null>(null)
  const [oauthStatuses, setOauthStatuses] = useState<Record<string, DownstreamOAuthStatusEntry[]>>({})
  const [capsCache, setCapsCache] = useState<Record<string, OAuthCapabilities>>({})
  const [statusErrors, setStatusErrors] = useState<Record<string, boolean>>({})
  // Progress state for multi-workspace fanout
  const [routeProgress, setRouteProgress] = useState<{ done: number; total: number } | null>(null)
  // Name of a newly-created server (stdio/custom/openapi) for display
  const [newServerName, setNewServerName] = useState<string | null>(null)
  // ID of a newly-created server to wire into routes
  const [newServerId, setNewServerId] = useState<string | null>(null)
  // Welcome overlay
  const [showWelcome, setShowWelcome] = useState(false)
  const [welcomeStep, setWelcomeStep] = useState(0)

  const step = stepStack[stepStack.length - 1]
  function pushStep(s: Step) { setStepStack((prev) => [...prev, s]) }
  function goBack() { setStepStack((prev) => prev.length > 1 ? prev.slice(0, -1) : prev) }

  const dsFetcher = useCallback(() => listDownstreams(), [])
  const { data: downstreams } = useApi(dsFetcher)
  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)

  // Curated "start here" server IDs
  const CURATED_IDS = ['github', 'linear', 'slack', 'notion', 'clickup', 'obsidian']

  // Show welcome overlay on first run
  useEffect(() => {
    const dismissed = localStorage.getItem('mcplexer_welcome_dismissed')
    const hasWelcomeParam = searchParams.get('welcome') === 'true'
    const noServers = downstreams && downstreams.filter((d) => !d.disabled).length === 0
    if (hasWelcomeParam || (!dismissed && noServers)) {
      setShowWelcome(true)
      setWelcomeStep(0)
    }
  }, [downstreams, searchParams])

  function dismissWelcome() {
    setShowWelcome(false)
    localStorage.setItem('mcplexer_welcome_dismissed', '1')
  }

  // Curated subset for "Start here" section
  const curatedServers = useMemo(() => {
    if (!downstreams) return []
    return CURATED_IDS
      .map((id) => downstreams.find((ds) => ds.id === id && !ds.disabled))
      .filter((ds): ds is DownstreamServer => !!ds)
  }, [downstreams])

  // Built-in recipe templates for popular servers
  type Recipe = { id: string; label: string; description: string }
  const RECIPE_TEMPLATES: Record<string, Recipe[]> = {
    github: [
      { id: 'readonly', label: 'Read-only', description: 'Browse repos, issues, and PRs. No write access.' },
      { id: 'full', label: 'Full access', description: 'Read and write: create issues, comment on PRs, push branches.' },
    ],
    slack: [
      { id: 'all', label: 'All workspaces', description: 'Access all workspaces your OAuth token permits.' },
      { id: 'specific', label: 'Specific workspace', description: 'Limit to a single workspace after auth.' },
    ],
  }

  function getRecipesForServer(dsId: string): Recipe[] {
    return RECIPE_TEMPLATES[dsId] ?? []
  }

  const [catalogEntries, setCatalogEntries] = useState<CatalogEntry[]>([])
  useEffect(() => {
    fetchCatalog()
      .then((res) => setCatalogEntries(res.entries.map(toCatalogEntry)))
      .catch(() => {})
  }, [])

  // Build catalog lookup to match downstream servers with their catalog entries
  const catalogMap = useMemo(() => {
    const m = new Map<string, CatalogEntry>()
    for (const entry of catalogEntries) { m.set(entry.id, entry) }
    return m
  }, [catalogEntries])

  // Group servers by category; uncategorised go in "Other"; add-new tiles at bottom
  type PickGroup = {
    key: string
    label: string
    servers: DownstreamServer[]
    isNewTiles?: boolean
  }
  const groupedServers = useMemo((): PickGroup[] => {
    if (!downstreams) return []
    const byCat = new Map<ServerCategory | '__other__', DownstreamServer[]>()
    for (const ds of downstreams) {
      if (ds.disabled) continue
      const c = (catalogMap.get(ds.id)?.category ?? '__other__') as ServerCategory | '__other__'
      if (!byCat.has(c)) byCat.set(c, [])
      byCat.get(c)!.push(ds)
    }
    const result: PickGroup[] = []
    for (const cat of CATEGORY_ORDER) {
      const servers = byCat.get(cat)
      if (servers && servers.length > 0) {
        result.push({ key: cat, label: CATEGORY_LABELS[cat], servers })
      }
    }
    const other = byCat.get('__other__')
    if (other && other.length > 0) {
      result.push({ key: 'other', label: 'Other', servers: other })
    }
    return result
  }, [downstreams, catalogMap])

  // Filter and search grouped servers
  const filteredGroups = useMemo(() => {
    return groupedServers.map((g) => {
      let servers = g.servers
      if (pickQuery.trim()) {
        const q = pickQuery.toLowerCase().trim()
        servers = servers.filter((ds) =>
          ds.name.toLowerCase().includes(q) ||
          ds.tool_namespace.toLowerCase().includes(q) ||
          (catalogMap.get(ds.id)?.description ?? '').toLowerCase().includes(q)
        )
      }
      if (pickFilter !== 'all') {
        servers = servers.filter((ds) => {
          const { connected, expired } = getStatusInfo(ds.id)
          const autoDisc = ds.transport === 'http' ? (capsCache[ds.id]?.supports_auto_discovery ?? false) : false
          switch (pickFilter) {
            case 'connected': return connected
            case 'needs-auth': return ds.transport === 'http' && !connected && !autoDisc
            case 'one-click': return ds.transport === 'http' && !connected && autoDisc
            case 'not-connected': return ds.transport === 'http' ? (!connected && !expired) : true
            default: return true
          }
        })
      }
      return { ...g, servers }
    }).filter((g) => g.servers.length > 0 || pickQuery.trim() === '')
  }, [groupedServers, pickQuery, pickFilter, capsCache, catalogMap, oauthStatuses])

  const showNewTiles = pickQuery.trim() === '' && pickFilter === 'all'

  // Pick filter counts
  const pickCounts = useMemo(() => {
    const all = groupedServers.reduce((s, g) => s + g.servers.length, 0)
    const states = { connected: 0, needsAuth: 0, oneClick: 0, notConnected: 0 }
    for (const g of groupedServers) {
      for (const ds of g.servers) {
        if (ds.transport !== 'http') { states.notConnected++; continue }
        const { connected } = getStatusInfo(ds.id)
        const autoDisc = capsCache[ds.id]?.supports_auto_discovery ?? false
        if (connected) states.connected++
        else if (autoDisc) states.oneClick++
        else states.needsAuth++
      }
    }
    return { all, ...states }
  }, [groupedServers, capsCache, oauthStatuses])

  // Handle OAuth redirect back
  useEffect(() => {
    const oauthResult = searchParams.get('oauth')
    if (oauthResult === 'success') {
      setStepStack(['success'])
      setSearchParams({}, { replace: true })
    } else if (oauthResult === 'error') {
      setConnectError(searchParams.get('message') ?? 'Authentication failed')
      setStepStack(['pick'])
      setSearchParams({}, { replace: true })
    }
  }, [searchParams, setSearchParams])

  // Fetch OAuth status + capabilities for HTTP downstreams
  useEffect(() => {
    if (!downstreams) return
    let active = true
    for (const ds of downstreams) {
      if (ds.transport !== 'http') continue
      getDownstreamOAuthStatus(ds.id)
        .then((res) => { if (active) setOauthStatuses((prev) => ({ ...prev, [ds.id]: res.entries })) })
        .catch(() => { if (active) setStatusErrors((prev) => ({ ...prev, [ds.id]: true })) })
      getOAuthCapabilities(ds.id)
        .then((res) => { if (active) setCapsCache((prev) => ({ ...prev, [ds.id]: res })) })
      .catch((err) => { console.warn('Failed to fetch catalog', err) })
    }
    return () => { active = false }
  }, [downstreams])

  // Seed workspace selection with first workspace when data loads
  useEffect(() => {
    if (!workspaces || workspaces.length === 0) return
    setSelectedWorkspaceIds((prev) => {
      const valid = prev.filter((id) => workspaces.some((w) => w.id === id))
      if (valid.length > 0) return valid
      return [workspaces[0].id]
    })
  }, [workspaces])

  const skipsConfig = !!(caps?.supports_auto_discovery && !caps.needs_credentials)
  const completedSteps = stepStack
    .slice(0, -1)
    .filter((s): s is 'configure' | 'workspace' | 'review' =>
      s === 'configure' || s === 'workspace' || s === 'review',
    )

  function getStatusInfo(dsId: string) {
    const entries = oauthStatuses[dsId]
    const connected = entries?.some((e) => e.status === 'authenticated')
    const expired = entries?.some((e) => e.status === 'expired')
    const expiresAt = entries?.find((e) => e.status === 'authenticated')?.expires_at
    return { connected, expired, expiresAt }
  }

  function toggleWorkspace(id: string) {
    setSelectedWorkspaceIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    )
  }

  // ── Pick handlers ──────────────────────────────────────────────────────────

  function pickIntegration(ds: DownstreamServer) {
    setSelectedDs(ds)
    setNewServerName(null)
    setNewServerId(null)
    const dsCaps = ds.transport === 'http' ? (capsCache[ds.id] ?? null) : null
    setCaps(dsCaps)
    setClientId('')
    setClientSecret('')
    setAccountLabel('')
    setConnectError(null)

    if (ds.transport === 'http') {
      setConnectMode('oauth')
      if (dsCaps?.supports_auto_discovery && !dsCaps.needs_credentials) {
        setStepStack(['pick', 'workspace'])
      } else {
        setStepStack(['pick', 'configure'])
      }
    } else {
      // stdio / internal — skip OAuth, just create a route
      setConnectMode('route_only')
      setStepStack(['pick', 'workspace'])
    }
  }

  // ── Sub-form callbacks ─────────────────────────────────────────────────────

  /** Called when StdioServerForm is submitted. Creates the downstream and advances. */
  async function handleStdioFormSubmit(values: StdioFormValues) {
    setConnectError(null)
    try {
      const ds = await createDownstream({
        name: values.name,
        command: values.command,
        args: values.args,
        url: null,
        tool_namespace: values.name.toLowerCase().replace(/[^a-z0-9]/g, '_'),
        transport: 'stdio',
        idle_timeout_sec: 0,
        max_instances: 1,
        restart_policy: 'on_failure',
        disabled: false,
      })
      setNewServerId(ds.id)
      setNewServerName(ds.name)
      setConnectMode('new_routes')
      pushStep('workspace')
    } catch (err) {
      setConnectError(err instanceof Error ? err.message : 'Failed to create server')
    }
  }

  /** Called after Custom MCP or OpenAPI addon is created. Just advance. */
  function handleAddonCreated(serverName: string) {
    // The addon is already created server-side; we just need its name.
    // The route will be created at submit time using listDownstreams to find it.
    setNewServerName(serverName)
    setConnectMode('new_routes')
    pushStep('workspace')
  }

  // ── Submit / connect ───────────────────────────────────────────────────────

  async function handleConnect() {
    setConnectError(null)

    if (connectMode === 'oauth' && selectedDs) {
      // Original OAuth flow — one workspace, one call
      const workspaceId = selectedWorkspaceIds[0] ?? 'global'
      pushStep('connecting')
      try {
        const resp = await connectDownstream(selectedDs.id, {
          workspace_id: workspaceId,
          client_id: clientId || undefined,
          client_secret: clientSecret || undefined,
          account_label: accountLabel || undefined,
        })
        if (resp.authorize_url) {
          redirectToOAuth(resp.authorize_url)
          return
        }
        // If no redirect, fan out remaining workspaces if selected
        const remaining = selectedWorkspaceIds.slice(1)
        if (remaining.length > 0) {
          setRouteProgress({ done: 1, total: selectedWorkspaceIds.length })
          for (let i = 0; i < remaining.length; i++) {
            await connectDownstream(selectedDs.id, {
              workspace_id: remaining[i],
              client_id: clientId || undefined,
              client_secret: clientSecret || undefined,
              account_label: accountLabel || undefined,
            })
            setRouteProgress({ done: i + 2, total: selectedWorkspaceIds.length })
          }
        }
        setStepStack(['success'])
      } catch (err) {
        setConnectError(err instanceof Error ? err.message : 'Failed to connect')
        setStepStack((prev) => prev.filter((s) => s !== 'connecting'))
      }
      return
    }

    // route_only or new_routes — create route rules for each selected workspace
    const serverId = connectMode === 'new_routes' ? newServerId : selectedDs?.id
    const serverName = connectMode === 'new_routes' ? newServerName : selectedDs?.name
    const serverNamespace =
      connectMode === 'new_routes'
        ? newServerName?.toLowerCase().replace(/[^a-z0-9]/g, '_') ?? ''
        : selectedDs?.tool_namespace ?? ''

    if (!serverId) {
      setConnectError('No server selected')
      return
    }

    pushStep('connecting')
    setRouteProgress({ done: 0, total: selectedWorkspaceIds.length })
    try {
      for (let i = 0; i < selectedWorkspaceIds.length; i++) {
        const wsId = selectedWorkspaceIds[i]
        const wsName = workspaces?.find((w) => w.id === wsId)?.name ?? wsId
        await createRoute({
          name: `${wsName} → ${serverName ?? serverId}`,
          priority: 100,
          workspace_id: wsId,
          path_glob: '**',
          tool_match: serverNamespace ? [`${serverNamespace}__*`] : [],
          scope_policy: {},
          downstream_server_id: serverId,
          auth_scope_id: '',
          policy: 'allow',
          log_level: 'info',
          approval_mode: 'write',
          approval_timeout: 0,
        })
        setRouteProgress({ done: i + 1, total: selectedWorkspaceIds.length })
      }
      setStepStack(['success'])
    } catch (err) {
      setConnectError(err instanceof Error ? err.message : 'Failed to create route')
      setStepStack((prev) => prev.filter((s) => s !== 'connecting'))
    } finally {
      setRouteProgress(null)
    }
  }

  // ── Help text map ──────────────────────────────────────────────────────────

  const STEP_HELP: Partial<Record<Step, string>> = {
    pick: 'Select a server to connect to your MCP gateway.',
    stdio_form: 'Configure a local stdio MCP server.',
    custom_mcp_form: 'Create a custom HTTP-based MCP addon.',
    openapi_form: 'Import an OpenAPI spec as an MCP server.',
    configure: "Enter your OAuth app credentials. We'll handle the rest.",
    workspace: 'Choose which workspaces this server will be available in.',
    review: 'Review your setup before connecting.',
    connecting: 'Setting up your connection...',
  }

  // ── Render ─────────────────────────────────────────────────────────────────

  return (
    <div className="space-y-6">
      {/* ── Welcome overlay ─────────────────────────────────────────────── */}
      {showWelcome && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm">
          <div className="mx-4 w-full max-w-lg border border-border bg-card p-6">
            <div className="mb-6 flex items-center justify-between">
              <h2 className="text-lg font-semibold">Welcome to MCPlexer</h2>
              <div className="flex gap-1">
                {[0, 1, 2].map((i) => (
                  <div
                    key={i}
                    className={`h-1.5 w-6 ${i <= welcomeStep ? 'bg-primary' : 'bg-muted'}`}
                  />
                ))}
              </div>
            </div>

            {welcomeStep === 0 && (
              <div className="space-y-3">
                <p className="text-sm text-muted-foreground">
                  MCPlexer connects your AI tools to external services like GitHub, Slack, and Linear.
                </p>
                <p className="text-sm text-muted-foreground">
                  Each integration gives your AI agent the ability to read and write to that service
                  through a secure, audited connection.
                </p>
                <p className="text-sm text-muted-foreground">
                  Pick a server below to get started. You can always add more later.
                </p>
              </div>
            )}

            {welcomeStep === 1 && (
              <div className="space-y-3">
                <p className="text-sm text-muted-foreground">
                  Some integrations use OAuth -- you will sign in with your account and MCPlexer
                  handles the credentials. Others need an API key or run locally.
                </p>
                <p className="text-sm text-muted-foreground">
                  Your credentials are stored locally and never leave your machine except when
                  making API calls on your behalf.
                </p>
              </div>
            )}

            {welcomeStep === 2 && (
              <div className="space-y-4">
                <p className="text-sm text-muted-foreground">
                  These are the most popular integrations to start with:
                </p>
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                  {curatedServers.map((ds) => {
                    const catEntry = catalogMap.get(ds.id)
                    return (
                      <button
                        key={ds.id}
                        type="button"
                        className="flex flex-col gap-1 border border-border p-3 text-left transition-all hover:border-primary hover:bg-muted/50"
                        onClick={() => {
                          dismissWelcome()
                          pickIntegration(ds)
                        }}
                      >
                        <span className="text-sm font-semibold">{ds.name}</span>
                        {catEntry && (
                          <span className="text-[10px] text-muted-foreground/60 line-clamp-2">
                            {catEntry.description}
                          </span>
                        )}
                      </button>
                    )
                  })}
                </div>
              </div>
            )}

            <div className="mt-6 flex justify-between">
              <Button
                variant="ghost"
                size="sm"
                onClick={dismissWelcome}
              >
                Skip
              </Button>
              <div className="flex gap-2">
                {welcomeStep > 0 && (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setWelcomeStep((s) => s - 1)}
                  >
                    <ArrowLeft className="mr-1.5 h-3 w-3" /> Back
                  </Button>
                )}
                {welcomeStep < 2 ? (
                  <Button
                    size="sm"
                    onClick={() => setWelcomeStep((s) => s + 1)}
                  >
                    Next <ArrowRight className="ml-1.5 h-3 w-3" />
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    onClick={dismissWelcome}
                  >
                    Get Started
                  </Button>
                )}
              </div>
            </div>
          </div>
        </div>
      )}
      {/* Header */}
      <div className="flex items-center gap-3">
        {step !== 'pick' && step !== 'success' && step !== 'connecting' && (
          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0"
            aria-label="Back"
            data-testid="setup-back"
            onClick={goBack}
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
        )}
        <h1 className="text-2xl font-bold">Add an Integration</h1>
      </div>

      {step === 'pick' && (
        <p className="text-xs text-muted-foreground/60">
          Choose an integration and attach it to the workspace where your AI tool should be allowed to use it.
        </p>
      )}

      {/* Step indicator */}
      {step !== 'pick' &&
        step !== 'stdio_form' &&
        step !== 'custom_mcp_form' &&
        step !== 'openapi_form' &&
        step !== 'success' &&
        step !== 'connecting' && (
          <StepIndicator
            steps={buildSteps(connectMode, skipsConfig)}
            currentStep={step}
            completedSteps={completedSteps}
          />
        )}

      {/* Help text */}
      {STEP_HELP[step] && (
        <p className="text-sm text-muted-foreground">{STEP_HELP[step]}</p>
      )}

      {/* Global error banner */}
      {connectError && step !== 'connecting' && (
        <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
          <div className="flex-1 text-sm text-destructive">{connectError}</div>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 text-xs"
            onClick={() => setConnectError(null)}
          >
            Dismiss
          </Button>
        </div>
      )}

      {/* ── Step: Pick ──────────────────────────────────────────────────── */}
      {step === 'pick' && (
        <div className="space-y-4">
          {/* Search + filter bar */}
          <div className="flex flex-wrap items-center gap-3">
            <div className="relative min-w-48 flex-1 sm:max-w-xs">
              <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground/70" />
              <Input
                value={pickQuery}
                onChange={(e) => setPickQuery(e.target.value)}
                placeholder="Search servers…"
                className="pl-8"
                data-testid="setup-pick-search"
              />
            </div>
            <PickFilterChips filter={pickFilter} setFilter={setPickFilter} counts={pickCounts} />
          </div>

          {/* Start here — curated servers when nothing is connected */}
          {pickCounts.connected === 0 && pickQuery.trim() === '' && pickFilter === 'all' && curatedServers.length > 0 && (
            <div>
              <div className="mb-2 flex items-baseline gap-2 px-1">
                <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Start here
                </span>
                <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
                  {curatedServers.length}
                </span>
              </div>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {curatedServers.map((ds) => {
                  const catEntry = catalogMap.get(ds.id)
                  const recipes = getRecipesForServer(ds.id)
                  return (
                    <button
                      key={ds.id}
                      type="button"
                      data-testid={`setup-curated-${ds.id}`}
                      className="flex flex-col gap-1.5 border border-border p-4 text-left transition-all hover:border-primary hover:bg-muted/50"
                      onClick={() => pickIntegration(ds)}
                    >
                      <span className="text-sm font-semibold">{ds.name}</span>
                      <span className="text-xs text-muted-foreground">{ds.tool_namespace}</span>
                      {catEntry && (
                        <span className="text-[10px] text-muted-foreground/60 line-clamp-1">
                          {catEntry.description}
                        </span>
                      )}
                      {recipes.length > 0 && (
                        <span className="text-[10px] text-primary/70">
                          {recipes.length} presets available
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>
              <div className="mt-2 px-1">
                <button
                  type="button"
                  className="text-xs text-primary hover:underline"
                  onClick={() => setPickFilter('all')}
                >
                  Show all integrations
                </button>
              </div>
            </div>
          )}

          {filteredGroups.length === 0 && pickQuery.trim() !== '' && (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No servers match "{pickQuery}".
            </p>
          )}

          {filteredGroups.map((group) => (
            <div key={group.key}>
              <div className="mb-2 flex items-baseline gap-2 px-1">
                <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  {group.label}
                </span>
                <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
                  {group.servers.length}
                </span>
              </div>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {group.servers.map((ds) => {
                  const isHttp = ds.transport === 'http'
                  const { connected, expired, expiresAt } = isHttp
                    ? getStatusInfo(ds.id)
                    : { connected: false, expired: false, expiresAt: undefined }
                  const autoDisc = isHttp ? (capsCache[ds.id]?.supports_auto_discovery ?? false) : false
                  const hasError = isHttp ? (statusErrors[ds.id] ?? false) : false
                  const capsLoaded = isHttp ? !!capsCache[ds.id] : true
                  const catEntry = catalogMap.get(ds.id)

                  return (
                    <button
                      key={ds.id}
                      type="button"
                      data-testid={`setup-pick-${ds.id}`}
                      className="flex flex-col gap-1.5 rounded-md border border-border p-4 text-left transition-all hover:border-primary hover:bg-muted/50"
                      onClick={() => pickIntegration(ds)}
                    >
                      <div className="flex w-full items-start justify-between gap-2">
                        <span className="min-w-0 truncate text-sm font-semibold">{ds.name}</span>
                        <div className="flex shrink-0 gap-1">
                          {ds.transport !== 'http' && (
                            <Badge variant="outline" className="text-[10px] px-1.5">{ds.transport}</Badge>
                          )}
                          {isHttp && hasError && (
                            <Badge variant="outline" className="text-destructive border-destructive/30 text-[10px] px-1.5">
                              <AlertCircle className="mr-0.5 h-2.5 w-2.5" /> Error
                            </Badge>
                          )}
                          {isHttp && !hasError && connected && (
                            <Badge className="bg-emerald-500/15 text-emerald-600 border-0 text-[10px] px-1.5">
                              <CheckCircle2 className="mr-0.5 h-2.5 w-2.5" /> Connected
                              {expiresAt && (
                                <span className="ml-1 opacity-70">
                                  <Clock className="mr-0.5 inline h-2 w-2" />
                                  {formatRelativeTime(expiresAt)}
                                </span>
                              )}
                            </Badge>
                          )}
                          {isHttp && !hasError && expired && (
                            <Badge variant="outline" className="text-amber-600 border-amber-300 text-[10px] px-1.5">
                              Expired — Reconnect
                            </Badge>
                          )}
                          {isHttp && !hasError && !connected && !expired && autoDisc && capsLoaded && (
                            <Badge className="bg-emerald-500/15 text-emerald-600 border-0 text-[10px] px-1.5">
                              <Zap className="mr-0.5 h-2.5 w-2.5" /> 1-Click
                            </Badge>
                          )}
                          {isHttp && !hasError && !connected && !expired && !autoDisc && capsLoaded && (
                            <Badge variant="outline" className="text-amber-600 border-amber-300 text-[10px] px-1.5">
                              Credentials Required
                            </Badge>
                          )}
                          {isHttp && !hasError && !connected && !expired && !capsLoaded && (
                            <Loader2 className="h-3 w-3 animate-spin text-muted-foreground/50" />
                          )}
                        </div>
                      </div>
                      <span className="text-xs text-muted-foreground">{ds.tool_namespace}</span>
                      {catEntry && (
                        <span className="text-[10px] text-muted-foreground/60 line-clamp-1">
                          {catEntry.description}
                        </span>
                      )}
                      {isHttp &&
                        oauthStatuses[ds.id]?.filter((e) => e.status === 'authenticated').length > 0 && (
                          <div className="flex flex-wrap gap-1 mt-0.5">
                            {oauthStatuses[ds.id]
                              .filter((e) => e.status === 'authenticated')
                              .map((e) => (
                                <Badge key={e.auth_scope_id} variant="secondary" className="text-[10px] font-normal">
                                  {e.auth_scope_name.replace(/_oauth_?/, ' ').replace(/_/g, ' ').trim() || 'Default'}
                                </Badge>
                              ))}
                          </div>
                        )}
                    </button>
                  )
                })}
              </div>
            </div>
          ))}

          {/* New server tiles — only show when not searching/filtering */}
          {showNewTiles && (
            <div>
              <div className="mb-2 flex items-baseline gap-2 px-1">
                <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Add New
                </span>
              </div>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <button
                  type="button"
                  data-testid="setup-pick-stdio"
                  aria-label="Add stdio server"
                  className="flex flex-col gap-1.5 rounded-md border border-dashed border-border p-4 text-left transition-all hover:border-primary hover:bg-muted/50"
                  onClick={() => {
                    setSelectedDs(null); setNewServerName(null); setNewServerId(null)
                    setConnectError(null); setStepStack(['pick', 'stdio_form'])
                  }}
                >
                  <div className="flex w-full items-start justify-between gap-2">
                    <span className="min-w-0 truncate text-sm font-semibold">stdio Server</span>
                    <Terminal className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  </div>
                  <span className="text-xs text-muted-foreground">Local process via standard I/O</span>
                </button>
                <button
                  type="button"
                  data-testid="setup-pick-custom-mcp"
                  aria-label="Add custom MCP server"
                  className="flex flex-col gap-1.5 rounded-md border border-dashed border-border p-4 text-left transition-all hover:border-primary hover:bg-muted/50"
                  onClick={() => {
                    setSelectedDs(null); setNewServerName(null); setNewServerId(null)
                    setConnectError(null); setStepStack(['pick', 'custom_mcp_form'])
                  }}
                >
                  <div className="flex w-full items-start justify-between gap-2">
                    <span className="min-w-0 truncate text-sm font-semibold">Custom MCP</span>
                    <Code2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  </div>
                  <span className="text-xs text-muted-foreground">HTTP addon with custom endpoints</span>
                </button>
                <button
                  type="button"
                  data-testid="setup-pick-openapi"
                  aria-label="Import from OpenAPI spec"
                  className="flex flex-col gap-1.5 rounded-md border border-dashed border-border p-4 text-left transition-all hover:border-primary hover:bg-muted/50"
                  onClick={() => {
                    setSelectedDs(null); setNewServerName(null); setNewServerId(null)
                    setConnectError(null); setStepStack(['pick', 'openapi_form'])
                  }}
                >
                  <div className="flex w-full items-start justify-between gap-2">
                    <span className="min-w-0 truncate text-sm font-semibold">From OpenAPI</span>
                    <FileCode className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  </div>
                  <span className="text-xs text-muted-foreground">Import an OpenAPI 3.x spec as tools</span>
                </button>
                <Link
                  to="/workspaces"
                  className="flex flex-col items-center justify-center gap-1.5 rounded-md border border-dashed border-border p-4 text-muted-foreground transition-colors hover:border-primary hover:text-foreground"
                >
                  <Plus className="h-5 w-5" />
                  <span className="text-sm font-medium">Browse Catalog</span>
                </Link>
              </div>
            </div>
          )}
        </div>
      )}

      {/* ── Step: stdio form ────────────────────────────────────────────── */}
      {step === 'stdio_form' && (
        <StdioServerForm
          onSubmit={(values) => { void handleStdioFormSubmit(values) }}
        />
      )}

      {/* ── Step: Custom MCP form ────────────────────────────────────────── */}
      {step === 'custom_mcp_form' && (
        <CustomMCPForm onCreated={handleAddonCreated} />
      )}

      {/* ── Step: OpenAPI import form ────────────────────────────────────── */}
      {step === 'openapi_form' && (
        <OpenAPIImportForm onCreated={handleAddonCreated} />
      )}

      {/* ── Step: Configure Credentials (HTTP OAuth) ─────────────────────── */}
      {step === 'configure' && selectedDs && caps && (
        <div className="mx-auto max-w-md space-y-4">
          <h2 className="text-lg font-semibold">{selectedDs.name}</h2>
          {caps.has_template && caps.template ? (
            <div className="space-y-3 rounded-md border border-border p-4">
              <p className="text-xs text-muted-foreground">{caps.template.help_text}</p>
              {caps.template.callback_url && (
                <div className="space-y-1">
                  <Label className="text-xs text-muted-foreground">Callback URL</Label>
                  <div className="flex items-center gap-2">
                    <code className="flex-1 truncate rounded-none border border-border bg-muted/50 px-2 py-1.5 font-mono text-xs">
                      {caps.template.callback_url}
                    </code>
                    <CopyButton value={caps.template.callback_url} />
                  </div>
                </div>
              )}
              {caps.template.setup_url && (
                <Button
                  variant="outline"
                  size="sm"
                  className="rounded-none"
                  asChild
                >
                  <a
                    href={caps.template.setup_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
                    Create {caps.template.name} OAuth App
                  </a>
                </Button>
              )}
              <div className="space-y-1">
                <Label className="text-xs text-muted-foreground">Client ID</Label>
                <Input
                  value={clientId}
                  onChange={(e) => setClientId(e.target.value)}
                  placeholder="Paste your client ID"
                />
              </div>
              {caps.template.needs_secret && (
                <div className="space-y-1">
                  <Label className="text-xs text-muted-foreground">Client Secret</Label>
                  <Input
                    type="password"
                    value={clientSecret}
                    onChange={(e) => setClientSecret(e.target.value)}
                    placeholder="Paste your client secret"
                  />
                </div>
              )}
              {caps.template.scopes.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  <span className="text-xs text-muted-foreground mr-1">Scopes:</span>
                  {caps.template.scopes.map((s) => (
                    <Badge key={s} variant="secondary" className="font-mono text-xs">
                      {s}
                    </Badge>
                  ))}
                </div>
              )}
            </div>
          ) : (
            <div className="rounded-md border border-border p-4">
              <p className="text-sm text-muted-foreground">
                This server supports automatic OAuth setup. Click Next to pick a workspace and
                connect.
              </p>
            </div>
          )}
          <div className="flex justify-end">
            <Button
              onClick={() => pushStep('workspace')}
              disabled={
                caps.needs_credentials &&
                (!clientId.trim() || (!!caps.template?.needs_secret && !clientSecret.trim()))
              }
              data-testid="setup-next-workspace"
            >
              Next <ArrowRight className="ml-2 h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      {/* ── Step: Workspace (multi-select) ──────────────────────────────── */}
      {step === 'workspace' && (
        <div className="mx-auto max-w-md space-y-4">
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Workspaces</Label>
            <div className="rounded-md border border-border divide-y divide-border">
              {(workspaces ?? []).map((ws) => (
                <label
                  key={ws.id}
                  className="flex cursor-pointer items-center gap-3 px-4 py-3 hover:bg-muted/30"
                >
                  <Checkbox
                    id={`ws-${ws.id}`}
                    checked={selectedWorkspaceIds.includes(ws.id)}
                    onCheckedChange={() => toggleWorkspace(ws.id)}
                  />
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium">{ws.name}</p>
                    {ws.root_path && (
                      <p className="text-xs text-muted-foreground truncate">{ws.root_path}</p>
                    )}
                  </div>
                </label>
              ))}
            </div>
            <p className="text-xs text-muted-foreground/60">
              A route rule will be created for each selected workspace.
            </p>
          </div>

          {connectMode === 'oauth' && (
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Account Label (optional)</Label>
              <Input
                value={accountLabel}
                onChange={(e) => setAccountLabel(e.target.value)}
                placeholder="e.g., Personal, Work, Client X"
              />
              <p className="text-xs text-muted-foreground/60">
                Label this account to connect multiple accounts for the same service.
              </p>
            </div>
          )}

          <div className="flex justify-end">
            <Button
              onClick={() => pushStep('review')}
              disabled={selectedWorkspaceIds.length === 0}
              data-testid="setup-next-review"
            >
              Next <ArrowRight className="ml-2 h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      {/* ── Step: Review ────────────────────────────────────────────────── */}
      {step === 'review' && (
        <div className="mx-auto max-w-md space-y-4">
          <div className="rounded-md border border-border divide-y divide-border">
            <div className="flex justify-between p-3">
              <span className="text-xs text-muted-foreground">Integration</span>
              <span className="text-sm font-medium">
                {selectedDs?.name ?? newServerName ?? '—'}
              </span>
            </div>
            <div className="flex justify-between p-3">
              <span className="text-xs text-muted-foreground">Workspaces</span>
              <span className="text-sm font-medium text-right">
                {selectedWorkspaceIds
                  .map((id) => workspaces?.find((w) => w.id === id)?.name ?? id)
                  .join(', ')}
              </span>
            </div>
            {connectMode === 'oauth' && (
              <div className="flex justify-between p-3">
                <span className="text-xs text-muted-foreground">Auth Method</span>
                <span className="text-sm font-medium">
                  {skipsConfig ? 'Auto-discovery' : caps?.has_template ? 'OAuth Template' : 'OAuth'}
                </span>
              </div>
            )}
            {connectMode !== 'oauth' && (
              <div className="flex justify-between p-3">
                <span className="text-xs text-muted-foreground">Connection</span>
                <span className="text-sm font-medium">Route rule(s) only</span>
              </div>
            )}
            {accountLabel && (
              <div className="flex justify-between p-3">
                <span className="text-xs text-muted-foreground">Account</span>
                <span className="text-sm font-medium">{accountLabel}</span>
              </div>
            )}
            {clientId && (
              <div className="flex justify-between p-3">
                <span className="text-xs text-muted-foreground">Client ID</span>
                <span className="truncate ml-4 max-w-[180px] font-mono text-xs text-muted-foreground">
                  {clientId}
                </span>
              </div>
            )}
          </div>
          <p className="text-xs text-muted-foreground/60">
            {connectMode === 'oauth'
              ? selectedWorkspaceIds.length === 1
                ? 'This will create a credential set and route rule, then redirect you to authenticate.'
                : `This will create credential sets and ${selectedWorkspaceIds.length} route rules, then redirect you to authenticate.`
              : `This will create ${selectedWorkspaceIds.length} route rule(s).`}
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={goBack} data-testid="setup-review-back">
              Back
            </Button>
            <Button onClick={() => { void handleConnect() }} data-testid="setup-connect">
              <Zap className="mr-2 h-4 w-4" /> Connect
            </Button>
          </div>
        </div>
      )}

      {/* ── Step: Connecting ────────────────────────────────────────────── */}
      {step === 'connecting' && (
        <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
          <Loader2 className="mb-4 h-8 w-8 animate-spin text-primary" />
          {routeProgress ? (
            <>
              <p className="text-sm">
                Creating route {routeProgress.done} / {routeProgress.total}…
              </p>
              <p className="mt-1 text-xs text-muted-foreground/60">
                Connecting{' '}
                {selectedWorkspaceIds[routeProgress.done - 1]
                  ? (workspaces?.find(
                      (w) => w.id === selectedWorkspaceIds[routeProgress.done - 1],
                    )?.name ?? '')
                  : ''}
              </p>
            </>
          ) : (
            <>
              <p className="text-sm">
                Connecting to {selectedDs?.name ?? newServerName ?? 'server'}…
              </p>
              <p className="mt-1 text-xs text-muted-foreground/60">
                You will be redirected to authenticate.
              </p>
            </>
          )}
        </div>
      )}

      {/* ── Step: Success ───────────────────────────────────────────────── */}
      {step === 'success' && (
        <div className="mx-auto flex max-w-md flex-col items-center py-12 text-center">
          <div className="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-emerald-500/10">
            <CheckCircle2 className="h-8 w-8 text-emerald-600" />
          </div>
          <h2 className="text-xl font-semibold">Connected!</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            Your integration has been set up and is ready to use.
          </p>
          <div className="mt-6 flex gap-3">
            <Button variant="outline" asChild>
              <Link to="/">Back to Dashboard</Link>
            </Button>
            <Button
              onClick={() => {
                setStepStack(['pick'])
                setSelectedDs(null)
                setCaps(null)
                setConnectError(null)
                setNewServerName(null)
                setNewServerId(null)
                setRouteProgress(null)
              }}
            >
              <RotateCcw className="mr-2 h-4 w-4" /> Connect Another
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// ── Pick filter chips ─────────────────────────────────────────────────────

interface PickCounts { all: number; connected: number; needsAuth: number; oneClick: number; notConnected: number }
type PickFilter = 'all' | 'connected' | 'needs-auth' | 'one-click' | 'not-connected'

function PickFilterChips({
  filter, setFilter, counts,
}: {
  filter: PickFilter
  setFilter: (f: PickFilter) => void
  counts: PickCounts
}) {
  const opts: Array<{ key: PickFilter; label: string; n: number }> = [
    { key: 'all', label: 'All', n: counts.all },
    { key: 'connected', label: 'Connected', n: counts.connected },
    { key: 'needs-auth', label: 'Needs Auth', n: counts.needsAuth },
    { key: 'one-click', label: '1-Click', n: counts.oneClick },
    { key: 'not-connected', label: 'Not Connected', n: counts.notConnected },
  ]
  return (
    <div className="flex flex-nowrap items-center gap-1.5 overflow-x-auto pb-1" data-testid="setup-pick-filters">
      {opts.map((o) => {
        const active = filter === o.key
        return (
          <button
            key={o.key}
            type="button"
            onClick={() => setFilter(o.key)}
            className={
              'inline-flex items-center gap-1.5 border px-2.5 py-1 text-xs transition-colors ' +
              (active
                ? 'border-primary/60 bg-primary/10 text-primary'
                : 'border-border bg-card/40 text-muted-foreground hover:text-foreground')
            }
            data-testid={`setup-pick-filter-${o.key}`}
          >
            <span>{o.label}</span>
            <span className={active ? 'text-primary' : 'text-muted-foreground/60'}>({o.n})</span>
          </button>
        )
      })}
    </div>
  )
}
