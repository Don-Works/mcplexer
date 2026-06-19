import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
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
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { useApi } from '@/hooks/use-api'
import { getHealth, getSettings, revealSystemPath, updateSettings } from '@/api/client'
import type { SystemRevealTarget } from '@/api/client'
import type { Settings } from '@/api/types'
import { getAgentRulesStatus, syncAgentRules } from '@/api/agent-rules'
import type { AgentRulesStatus } from '@/api/agent-rules'
import { CheckCircle2, FileText, FolderOpen, Globe, Loader2, Pencil, RefreshCcw, Save } from 'lucide-react'
import { toast } from 'sonner'

const SETTINGS_VIEW_KEY = 'mcplexer-settings-view'
type SettingsView = 'basic' | 'advanced'

export function SettingsPage() {
  const fetcher = useCallback(() => getSettings(), [])
  const { data, loading } = useApi(fetcher)

  const healthFetcher = useCallback(() => getHealth().catch(() => null), [])
  const { data: health } = useApi(healthFetcher)

  const [settings, setSettings] = useState<Settings | null>(null)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [view, setView] = useState<SettingsView>(() => {
    const stored = localStorage.getItem(SETTINGS_VIEW_KEY)
    return stored === 'advanced' ? 'advanced' : 'basic'
  })
  const [agentRules, setAgentRules] = useState<AgentRulesStatus | null>(null)
  const [agentRulesSyncing, setAgentRulesSyncing] = useState(false)

  const refreshAgentRules = useCallback(async () => {
    try {
      const next = await getAgentRulesStatus()
      setAgentRules(next)
    } catch {
      setAgentRules(null)
    }
  }, [])

  useEffect(() => {
    refreshAgentRules()
  }, [refreshAgentRules])

  async function handleAgentRulesSync() {
    setAgentRulesSyncing(true)
    try {
      const result = await syncAgentRules()
      if (result.changed) {
        toast.success(`Agent rules synced to v${result.version}`)
      } else {
        toast.info(`Agent rules already at v${result.version}`)
      }
      await refreshAgentRules()
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Sync failed'
      toast.error(msg)
    } finally {
      setAgentRulesSyncing(false)
    }
  }

  async function handleReveal(target: SystemRevealTarget, label: string) {
    try {
      await revealSystemPath(target)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Failed to open ${label}`)
    }
  }

  useEffect(() => {
    if (data) {
      setSettings({
        ...data.settings,
        slim_surface: data.settings.slim_surface ?? true,
        code_mode_max_output_bytes: data.settings.code_mode_max_output_bytes ?? 24 * 1024,
        mesh_receive_max_results: data.settings.mesh_receive_max_results ?? 20,
        mesh_receive_preview_bytes: data.settings.mesh_receive_preview_bytes ?? 512,
        mesh_send_max_content_bytes: data.settings.mesh_send_max_content_bytes ?? 64 * 1024,
        tool_description_overrides: data.settings.tool_description_overrides ?? {},
        remote_skill_server_url: data.settings.remote_skill_server_url ?? '',
      })
      setDirty(false)
    }
  }, [data])

  function patch(partial: Partial<Settings>) {
    setSettings((prev) => (prev ? { ...prev, ...partial } : prev))
    setDirty(true)
  }

  async function handleSave() {
    if (!settings) return
    setSaving(true)
    try {
      await updateSettings(settings)
      setDirty(false)
      toast.success('Settings saved')
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to save'
      toast.error(msg)
    } finally {
      setSaving(false)
    }
  }

  if (loading || !settings || !data) {
    return (
      <div className="flex items-center justify-center py-24">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <h1 className="text-2xl font-bold">Settings</h1>
          <ToggleGroup
            type="single"
            value={view}
            onValueChange={(v: SettingsView) => {
              if (v) {
                setView(v)
                localStorage.setItem(SETTINGS_VIEW_KEY, v)
              }
            }}
            variant="outline"
            size="sm"
            data-testid="settings-view-toggle"
          >
            <ToggleGroupItem value="basic" data-testid="settings-view-basic">Basic</ToggleGroupItem>
            <ToggleGroupItem value="advanced" data-testid="settings-view-advanced">Advanced</ToggleGroupItem>
          </ToggleGroup>
        </div>
        <Button onClick={handleSave} disabled={saving || !dirty} data-testid="settings-save">
          {saving ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Save className="mr-2 h-4 w-4" />
          )}
          Save
        </Button>
      </div>

      <div className="space-y-6">
        <div className="space-y-6">
          {health && (
            <Card data-testid="daemon-card">
              <CardHeader>
                <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                  Daemon
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="grid grid-cols-3 gap-2 text-xs">
                  <span className="text-muted-foreground">Mode</span>
                  <span className="col-span-2 font-mono">{health.mode}{health.system?.p2p_enabled ? ' · p2p' : ''}</span>
                  <span className="text-muted-foreground">Version</span>
                  <span className="col-span-2 font-mono">{health.version}</span>
                  {health.system?.http_addr && (
                    <>
                      <span className="text-muted-foreground">HTTP</span>
                      <span className="col-span-2 font-mono">{health.system.http_addr}</span>
                    </>
                  )}
                  {health.system?.socket_path && (
                    <>
                      <span className="text-muted-foreground">Socket</span>
                      <span className="col-span-2 truncate font-mono">{health.system.socket_path}</span>
                    </>
                  )}
                  <span className="text-muted-foreground">Uptime</span>
                  <span className="col-span-2 font-mono">{Math.floor(health.uptime_seconds / 60)} min</span>
                </div>
                <div className="border-t pt-3 space-y-2">
                  {health.system?.data_dir && (
                    <button
                      type="button"
                      onClick={() => handleReveal('data_dir', 'data dir')}
                      data-testid="settings-reveal-data-dir"
                      className="flex w-full items-center gap-2 rounded-md border border-border/50 px-3 py-2 text-left text-xs hover:bg-muted/50"
                    >
                      <FolderOpen className="h-4 w-4 text-muted-foreground" />
                      <div className="flex-1 min-w-0">
                        <div className="font-medium">Data folder</div>
                        <div className="truncate font-mono text-[10px] text-muted-foreground">
                          {health.system.data_dir}
                        </div>
                      </div>
                    </button>
                  )}
                  {health.system?.config_file && (
                    <button
                      type="button"
                      onClick={() => handleReveal('config_file', 'config file')}
                      data-testid="settings-reveal-config-file"
                      className="flex w-full items-center gap-2 rounded-md border border-border/50 px-3 py-2 text-left text-xs hover:bg-muted/50"
                    >
                      <FileText className="h-4 w-4 text-muted-foreground" />
                      <div className="flex-1 min-w-0">
                        <div className="font-medium">mcplexer.yaml</div>
                        <div className="truncate font-mono text-[10px] text-muted-foreground">
                          {health.system.config_file}
                        </div>
                      </div>
                    </button>
                  )}
                  {health.system?.log_path && (
                    <button
                      type="button"
                      onClick={() => handleReveal('log_path', 'log file')}
                      data-testid="settings-reveal-log"
                      className="flex w-full items-center gap-2 rounded-md border border-border/50 px-3 py-2 text-left text-xs hover:bg-muted/50"
                    >
                      <FileText className="h-4 w-4 text-muted-foreground" />
                      <div className="flex-1 min-w-0">
                        <div className="font-medium">Log file</div>
                        <div className="truncate font-mono text-[10px] text-muted-foreground">
                          {health.system.log_path}
                        </div>
                      </div>
                    </button>
                  )}
                  {health.system?.addons_dir && (
                    <button
                      type="button"
                      onClick={() => handleReveal('addons_dir', 'addons dir')}
                      data-testid="settings-reveal-addons-dir"
                      className="flex w-full items-center gap-2 rounded-md border border-border/50 px-3 py-2 text-left text-xs hover:bg-muted/50"
                    >
                      <FolderOpen className="h-4 w-4 text-muted-foreground" />
                      <div className="flex-1 min-w-0">
                        <div className="font-medium">Addons folder</div>
                        <div className="truncate font-mono text-[10px] text-muted-foreground">
                          {health.system.addons_dir}
                        </div>
                      </div>
                    </button>
                  )}
                </div>
              </CardContent>
            </Card>
          )}

          {agentRules && (
            <Card data-testid="agent-rules-card">
              <CardHeader>
                <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                  Agent rules
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                <p className="text-xs text-muted-foreground">
                  Marker-bounded mcplexer block in{' '}
                  <span className="font-mono">{agentRules.path}</span>. Tells every
                  Claude / OpenCode session on this machine that mcplexer is wired up
                  and which tool families it offers.
                </p>
                <div className="flex items-center justify-between gap-3">
                  <AgentRulesStatusBadge status={agentRules} />
                  <Button
                    onClick={handleAgentRulesSync}
                    disabled={agentRulesSyncing || (agentRules.present && agentRules.up_to_date)}
                    variant={agentRules.present ? 'outline' : 'default'}
                    size="sm"
                    data-testid="agent-rules-sync"
                  >
                    {agentRulesSyncing ? (
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    ) : (
                      <RefreshCcw className="mr-2 h-4 w-4" />
                    )}
                    {agentRules.present ? 'Sync' : 'Install'}
                  </Button>
                </div>
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                This Device
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="device-display-name">Display name</Label>
                <p className="text-xs text-muted-foreground">
                  Shown to paired devices and on cross-machine mesh messages
                  (e.g. "dev-mbp"). Letters, numbers, and . _ - only.
                  Renaming broadcasts the new name to paired peers.
                </p>
                <Input
                  id="device-display-name"
                  data-testid="settings-display-name"
                  value={settings.display_name ?? ''}
                  maxLength={50}
                  onChange={(e) => patch({ display_name: e.target.value })}
                  className="max-w-sm"
                  placeholder="dev-mbp"
                />
              </div>
            </CardContent>
          </Card>

          {view === 'advanced' && (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                  Skills Hub
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="remote-skill-server">Remote skills server</Label>
                  <p className="text-xs text-muted-foreground">
                    DNS name or URL for the shared registry. Used only when you choose a remote skill action.
                  </p>
                  <div className="flex max-w-xl items-center gap-2">
                    <Globe className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <Input
                      id="remote-skill-server"
                      data-testid="settings-remote-skill-server"
                      value={settings.remote_skill_server_url ?? ''}
                      onChange={(e) => patch({ remote_skill_server_url: e.target.value })}
                      placeholder="shared-skills"
                    />
                  </div>
                </div>
              </CardContent>
            </Card>
          )}

          {view === 'advanced' && (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                  Tool Display
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="flex items-center justify-between">
                  <div className="space-y-1">
                    <Label>Slim advertised tool surface</Label>
                    <p className="text-xs text-muted-foreground">
                      Keeps tools/list to discovery and batching entrypoints; use search_tools to find the rest.
                    </p>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={settings.slim_surface}
                    aria-label="Slim advertised tool surface"
                    data-testid="settings-toggle-slim-surface"
                    onClick={() => patch({ slim_surface: !settings.slim_surface })}
                    className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                      settings.slim_surface ? 'bg-primary' : 'bg-muted'
                    }`}
                  >
                    <span
                      className={`pointer-events-none block h-5 w-5 rounded-full bg-background shadow-lg ring-0 transition-transform ${
                        settings.slim_surface ? 'translate-x-5' : 'translate-x-0'
                      }`}
                    />
                  </button>
                </div>

                <div className="flex items-center justify-between border-t pt-4">
                  <div className="space-y-1">
                    <Label>Minify tool schemas (slim tools)</Label>
                    <p className="text-xs text-muted-foreground">
                      Strips property descriptions from schemas to save context window
                    </p>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={settings.slim_tools}
                    aria-label="Minify tool schemas"
                    data-testid="settings-toggle-slim-tools"
                    onClick={() => patch({ slim_tools: !settings.slim_tools })}
                    className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                      settings.slim_tools ? 'bg-primary' : 'bg-muted'
                    }`}
                  >
                    <span
                      className={`pointer-events-none block h-5 w-5 rounded-full bg-background shadow-lg ring-0 transition-transform ${
                        settings.slim_tools ? 'translate-x-5' : 'translate-x-0'
                      }`}
                    />
                  </button>
                </div>

                <div className="flex items-center justify-between border-t pt-4">
                  <div className="space-y-1">
                    <Label>Compact tool responses</Label>
                    <p className="text-xs text-muted-foreground">
                      Compresses verbose JSON tool responses into a token-efficient format.
                      Prunes empty fields and converts arrays to columnar layout.
                    </p>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={settings.compact_responses}
                    aria-label="Compact tool responses"
                    data-testid="settings-toggle-compact-responses"
                    onClick={() => patch({ compact_responses: !settings.compact_responses })}
                    className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                      settings.compact_responses ? 'bg-primary' : 'bg-muted'
                    }`}
                  >
                    <span
                      className={`pointer-events-none block h-5 w-5 rounded-full bg-background shadow-lg ring-0 transition-transform ${
                        settings.compact_responses ? 'translate-x-5' : 'translate-x-0'
                      }`}
                    />
                  </button>
                </div>
              </CardContent>
            </Card>
          )}

          {view === 'advanced' && (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                  Code Execution
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="space-y-2">
                  <Label>Execution timeout (seconds)</Label>
                  <p className="text-xs text-muted-foreground">
                    Maximum time a code execution can run before being killed (1-120)
                  </p>
                  <Input
                    type="number"
                    min={1}
                    max={120}
                    value={settings.code_mode_timeout_sec}
                    onChange={(e) =>
                      patch({ code_mode_timeout_sec: parseInt(e.target.value, 10) || 30 })
                    }
                    className="w-32"
                  />
                </div>
                <div className="space-y-2 border-t pt-4">
                  <Label>Captured output cap (bytes)</Label>
                  <p className="text-xs text-muted-foreground">
                    Maximum print/console output returned by code mode (1024-262144).
                  </p>
                  <Input
                    type="number"
                    min={1024}
                    max={262144}
                    step={1024}
                    value={settings.code_mode_max_output_bytes}
                    onChange={(e) =>
                      patch({ code_mode_max_output_bytes: parseInt(e.target.value, 10) || 24 * 1024 })
                    }
                    className="w-40"
                  />
                </div>
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                Inter-Agent Communication
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex items-center justify-between">
                <div className="space-y-1">
                  <Label>Agent Mesh</Label>
                  <p className="text-xs text-muted-foreground">
                    Enable inter-agent messaging. When enabled, agents can send and
                    receive messages to coordinate work across sessions. Requires restart.
                  </p>
                </div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={settings.mesh_enabled}
                  aria-label="Agent Mesh enabled"
                  data-testid="settings-toggle-mesh-enabled"
                  onClick={() => patch({ mesh_enabled: !settings.mesh_enabled })}
                  className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                    settings.mesh_enabled ? 'bg-primary' : 'bg-muted'
                  }`}
                >
                  <span
                    className={`pointer-events-none block h-5 w-5 rounded-full bg-background shadow-lg ring-0 transition-transform ${
                      settings.mesh_enabled ? 'translate-x-5' : 'translate-x-0'
                    }`}
                  />
                </button>
              </div>

              {view === 'advanced' && (
                <div className="grid gap-4 border-t pt-4 sm:grid-cols-3">
                  <div className="space-y-2">
                    <Label>Receive results</Label>
                    <p className="text-xs text-muted-foreground">Default and maximum messages returned (1-50).</p>
                    <Input
                      type="number"
                      min={1}
                      max={50}
                      value={settings.mesh_receive_max_results}
                      onChange={(e) =>
                        patch({ mesh_receive_max_results: parseInt(e.target.value, 10) || 20 })
                      }
                      className="w-28"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>Preview bytes</Label>
                    <p className="text-xs text-muted-foreground">Per-message receive preview cap (64-2048).</p>
                    <Input
                      type="number"
                      min={64}
                      max={2048}
                      step={64}
                      value={settings.mesh_receive_preview_bytes}
                      onChange={(e) =>
                        patch({ mesh_receive_preview_bytes: parseInt(e.target.value, 10) || 512 })
                      }
                      className="w-32"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>Send bytes</Label>
                    <p className="text-xs text-muted-foreground">Maximum mesh message body size (1024-65536).</p>
                    <Input
                      type="number"
                      min={1024}
                      max={65536}
                      step={1024}
                      value={settings.mesh_send_max_content_bytes}
                      onChange={(e) =>
                        patch({ mesh_send_max_content_bytes: parseInt(e.target.value, 10) || 64 * 1024 })
                      }
                      className="w-36"
                    />
                  </div>
                </div>
              )}
            </CardContent>
          </Card>

          {view === 'advanced' && (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                  Caching
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="space-y-2">
                  <Label>Tools list cache TTL (seconds)</Label>
                  <p className="text-xs text-muted-foreground">
                    How often tools/list re-queries downstream servers (0-300)
                  </p>
                  <Input
                    type="number"
                    min={0}
                    max={300}
                    value={settings.tools_cache_ttl_sec}
                    onChange={(e) =>
                      patch({ tools_cache_ttl_sec: parseInt(e.target.value, 10) || 0 })
                    }
                    className="w-32"
                  />
                </div>
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
                Logging
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label>Log level</Label>
                <Select
                  value={settings.log_level}
                  onValueChange={(v) => patch({ log_level: v })}
                >
                  <SelectTrigger className="w-40">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="debug">debug</SelectItem>
                    <SelectItem value="info">info</SelectItem>
                    <SelectItem value="warn">warn</SelectItem>
                    <SelectItem value="error">error</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>
        </div>

        {view === 'advanced' && (
          <Link
            to="/descriptions"
            data-testid="settings-link-descriptions"
            className="group flex items-center gap-3 rounded-md border border-border/50 px-4 py-3 text-sm transition-colors hover:border-primary/40 hover:bg-card/80"
          >
            <Pencil className="h-4 w-4 text-muted-foreground group-hover:text-primary" />
            <div className="flex-1">
              <div className="font-medium">Built-in tool description overrides</div>
              <div className="text-xs text-muted-foreground">Moved to Tool Descriptions →</div>
            </div>
          </Link>
        )}
      </div>
    </div>
  )
}

function AgentRulesStatusBadge({ status }: { status: AgentRulesStatus }) {
  if (!status.present) {
    return (
      <span
        data-testid="agent-rules-state"
        className="inline-flex items-center gap-1.5 rounded-full bg-amber-500/10 px-2.5 py-1 text-xs font-medium text-amber-600 dark:text-amber-400"
      >
        Not installed (latest v{status.latest_version})
      </span>
    )
  }
  if (!status.up_to_date) {
    return (
      <span
        data-testid="agent-rules-state"
        className="inline-flex items-center gap-1.5 rounded-full bg-amber-500/10 px-2.5 py-1 text-xs font-medium text-amber-600 dark:text-amber-400"
      >
        Out of date — installed v{status.current_version}, latest v{status.latest_version}
      </span>
    )
  }
  return (
    <span
      data-testid="agent-rules-state"
      className="inline-flex items-center gap-1.5 rounded-full bg-emerald-500/10 px-2.5 py-1 text-xs font-medium text-emerald-600 dark:text-emerald-400"
    >
      <CheckCircle2 className="h-3.5 w-3.5" />
      Synced (v{status.current_version})
    </span>
  )
}
