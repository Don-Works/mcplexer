import { useCallback, useMemo, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import { discoverTools, dryRun, listDownstreams, listWorkspaces } from '@/api/client'
import type { DownstreamServer, DryRunResult } from '@/api/types'
import { Loader2, Play, Search, Terminal } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

interface CachedTool {
  name: string
  description: string
  inputSchema?: Record<string, unknown>
}

function extractToolsFromCache(server: DownstreamServer): CachedTool[] {
  const cache = server.capabilities_cache
  if (!cache || typeof cache !== 'object') return []

  const raw = cache as Record<string, unknown>
  const tools = raw.tools as CachedTool[] | undefined
  if (!Array.isArray(tools)) return []

  return tools.map((t) => ({
    name: t.name,
    description: t.description ?? '',
    inputSchema: t.inputSchema as Record<string, unknown> | undefined,
  }))
}

export function DryRunPage() {
  const [workspaceId, setWorkspaceId] = useState('')
  const [subpath, setSubpath] = useState('')
  const [serverId, setServerId] = useState('')
  const [toolName, setToolName] = useState('')
  const [result, setResult] = useState<DryRunResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [discovering, setDiscovering] = useState(false)

  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)

  const downstreamsFetcher = useCallback(() => listDownstreams(), [])
  const { data: downstreams, refetch: refetchDownstreams } = useApi(downstreamsFetcher)

  const selectedServer = useMemo(
    () => downstreams?.find((d) => d.id === serverId) ?? null,
    [downstreams, serverId],
  )

  const cachedTools = useMemo(
    () => (selectedServer ? extractToolsFromCache(selectedServer) : []),
    [selectedServer],
  )

  const namespace = selectedServer?.tool_namespace ?? ''

  function handleServerChange(id: string) {
    setServerId(id)
    setToolName('')
  }

  function handleToolChange(rawName: string) {
    const namespacedName = namespace ? `${namespace}__${rawName}` : rawName
    setToolName(namespacedName)
  }

  async function handleDiscover() {
    if (!serverId) return
    setDiscovering(true)
    setError(null)
    try {
      await discoverTools(serverId)
      await refetchDownstreams()
      setToolName('')
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Discovery failed'
      setError(message)
    } finally {
      setDiscovering(false)
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setResult(null)
    setRunning(true)

    try {
      const res = await dryRun({
        workspace_id: workspaceId,
        subpath,
        tool_name: toolName,
      })
      setResult(res)
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Unknown error'
      setError(message)
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">Dry Run Simulator</h1>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
              Simulate a Tool Call
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit} className="space-y-4">
              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Workspace</Label>
                <Select value={workspaceId} onValueChange={setWorkspaceId}>
                  <SelectTrigger>
                    <SelectValue placeholder="Select workspace..." />
                  </SelectTrigger>
                  <SelectContent>
                    {workspaces?.map((w) => (
                      <SelectItem key={w.id} value={w.id}>
                        {w.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Subpath</Label>
                <Input
                  placeholder="/src/components"
                  value={subpath}
                  onChange={(e) => setSubpath(e.target.value)}
                />
              </div>

              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Downstream Server</Label>
                <div className="flex gap-2">
                  <Select value={serverId} onValueChange={handleServerChange}>
                    <SelectTrigger className="flex-1">
                      <SelectValue placeholder="Select server..." />
                    </SelectTrigger>
                    <SelectContent>
                      {downstreams?.map((d) => (
                        <SelectItem key={d.id} value={d.id}>
                          {d.name}{' '}
                          <span className="text-muted-foreground">({d.tool_namespace})</span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button
                        type="button"
                        variant="outline"
                        size="icon"
                        aria-label="Discover tools from server"
                        data-testid="dryrun-discover"
                        disabled={!serverId || discovering}
                        onClick={handleDiscover}
                      >
                        {discovering ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <Search className="h-4 w-4" />
                        )}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>Discover tools from server</TooltipContent>
                  </Tooltip>
                </div>
              </div>

              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Tool</Label>
                {cachedTools.length > 0 ? (
                  <Select
                    value={toolName ? toolName.replace(`${namespace}__`, '') : ''}
                    onValueChange={handleToolChange}
                  >
                    <SelectTrigger className="w-full min-w-0 font-mono text-sm">
                      <SelectValue placeholder="Select tool..." />
                    </SelectTrigger>
                    <SelectContent position="popper" sideOffset={4} className="max-h-72">
                      {cachedTools.map((t) => (
                        <SelectItem key={t.name} value={t.name}>
                          <span className="truncate">
                            <span className="font-mono text-sm">{t.name}</span>
                            {t.description && (
                              <span className="ml-2 text-xs text-muted-foreground">
                                {t.description.slice(0, 60)}
                                {t.description.length > 60 ? '...' : ''}
                              </span>
                            )}
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : (
                  <Input
                    className="font-mono text-sm"
                    placeholder={
                      serverId
                        ? 'No cached tools — run a search first'
                        : 'Select a server above, or type manually'
                    }
                    value={toolName}
                    onChange={(e) => setToolName(e.target.value)}
                  />
                )}
                {toolName && (
                  <p className="font-mono text-xs text-muted-foreground">
                    Namespaced: {toolName}
                  </p>
                )}
              </div>

              <Button type="submit" disabled={running || !workspaceId || !toolName} className="w-full" data-testid="dryrun-submit">
                <Play className="mr-2 h-4 w-4" />
                {running ? 'Simulating...' : 'Simulate'}
              </Button>
            </form>
          </CardContent>
        </Card>

        <div className="space-y-6">
          {error && (
            <Card className="border-destructive/50">
              <CardContent className="pt-6">
                <p className="font-mono text-sm text-destructive">{error}</p>
              </CardContent>
            </Card>
          )}

          {result ? (
            <DryRunResultCard result={result} />
          ) : (
            <Card className="border-dashed">
              <CardContent className="pt-6">
                <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
                  <Terminal className="mb-3 h-8 w-8 text-muted-foreground/50" />
                  <p className="font-mono text-sm">
                    <span className="text-primary">&gt;</span> awaiting simulation
                    <span className="ml-0.5 inline-block w-1.5 animate-pulse bg-primary/70">
                      &nbsp;
                    </span>
                  </p>
                  <p className="mt-2 text-xs text-muted-foreground/60">
                    Select a server and tool, then hit Simulate
                  </p>
                </div>
              </CardContent>
            </Card>
          )}
        </div>
      </div>
    </div>
  )
}

function OAuthStatusBadge({ status }: { status: string }) {
  switch (status) {
    case 'valid':
      return <Badge className="bg-emerald-500/15 text-emerald-600 text-xs border-0">Connected</Badge>
    case 'expired':
    case 'refresh_needed':
      return <Badge variant="outline" className="text-xs text-amber-600 border-amber-300">Expired</Badge>
    case 'none':
      return <Badge variant="outline" className="text-xs text-muted-foreground">Not Connected</Badge>
    default:
      return null
  }
}

function DryRunResultCard({ result }: { result: DryRunResult }) {
  const policy = result.policy
  const badgeClass =
    policy === 'allow'
      ? 'bg-chart-2/15 text-chart-2 border-chart-2/30'
      : policy === 'deny'
        ? 'bg-destructive/15 text-destructive border-destructive/30'
        : 'bg-chart-4/15 text-chart-4 border-chart-4/30'

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-3 text-sm">
          Result
          <Badge variant="outline" className={badgeClass}>
            {result.matched ? policy : 'no match'}
          </Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        <div>
          <h4 className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Matched Rule
          </h4>
          {result.matched_rule ? (
            <div className="space-y-1 rounded-md border border-border bg-background p-3 font-mono text-xs">
              <p>
                <span className="text-muted-foreground">id:</span>{' '}
                <span className="text-accent-foreground">{result.matched_rule.id}</span>
              </p>
              <p>
                <span className="text-muted-foreground">priority:</span>{' '}
                {result.matched_rule.priority}
              </p>
              <p>
                <span className="text-muted-foreground">path_glob:</span>{' '}
                <span className="text-accent-foreground">{result.matched_rule.path_glob}</span>
              </p>
              <p>
                <span className="text-muted-foreground">policy:</span>{' '}
                {result.matched_rule.policy}
              </p>
            </div>
          ) : (
            <p className="text-muted-foreground/60">
              {result.matched ? 'Denied by rule' : 'No matching rule found'}
            </p>
          )}
        </div>

        <div>
          <h4 className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Downstream Server
          </h4>
          {result.downstream_server ? (
            <div className="space-y-1 rounded-md border border-border bg-background p-3 font-mono text-xs">
              <p>
                <span className="text-muted-foreground">name:</span>{' '}
                {result.downstream_server.name}
              </p>
              <p>
                <span className="text-muted-foreground">transport:</span>{' '}
                {result.downstream_server.transport}
              </p>
              <p>
                <span className="text-muted-foreground">namespace:</span>{' '}
                <span className="text-accent-foreground">
                  {result.downstream_server.tool_namespace}
                </span>
              </p>
            </div>
          ) : (
            <p className="text-muted-foreground/60">None</p>
          )}
        </div>

        {result.auth_scope && (
          <div>
            <h4 className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
              Auth Scope
            </h4>
            <div className="space-y-1 rounded-md border border-border bg-background p-3 font-mono text-xs">
              <p>
                <span className="text-muted-foreground">name:</span>{' '}
                <span className="text-accent-foreground">{result.auth_scope.name}</span>
              </p>
              <p>
                <span className="text-muted-foreground">id:</span>{' '}
                <span className="text-accent-foreground">{result.auth_scope.id}</span>
              </p>
              <p>
                <span className="text-muted-foreground">type:</span>{' '}
                {result.auth_scope.type}
              </p>
              {result.auth_scope.type === 'oauth2' && (
                <p className="flex items-center gap-2">
                  <span className="text-muted-foreground">oauth:</span>{' '}
                  <OAuthStatusBadge status={result.auth_scope.oauth_status} />
                </p>
              )}
            </div>
          </div>
        )}

        {(result.candidate_rules ?? []).length > 0 && (
          <div>
            <h4 className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
              Candidate Rules ({result.candidate_rules.length})
            </h4>
            <div className="space-y-1 font-mono text-xs">
              {result.candidate_rules.map((rule) => (
                <div
                  key={rule.id}
                  className={`flex items-center gap-2 rounded px-2 py-1 ${
                    result.matched_rule?.id === rule.id
                      ? 'bg-chart-2/10 text-chart-2'
                      : 'text-muted-foreground'
                  }`}
                >
                  <span className="w-6 text-right">{rule.priority}</span>
                  <span>{rule.id}</span>
                  <Badge
                    variant="outline"
                    className="ml-auto text-[10px] px-1 py-0"
                  >
                    {rule.policy}
                  </Badge>
                </div>
              ))}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
