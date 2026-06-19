import { useCallback, useMemo, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { CopyButton } from '@/components/ui/copy-button'
import { useApi } from '@/hooks/use-api'
import {
  getHarnessSetupStatus,
  getMCPInstallStatus,
  installHarness,
  installMCP,
  previewMCPInstall,
  recheckHarness,
  uninstallMCP,
} from '@/api/client'
import type {
  HarnessKey,
  HarnessSetupRow,
  MCPClient,
  MCPInstallPreview,
} from '@/api/types'
import { toast } from 'sonner'
import {
  AlertCircle,
  BookOpen,
  CheckCircle2,
  ClipboardCopy,
  Download,
  Eye,
  Loader2,
  Plug,
  RefreshCw,
  RotateCcw,
  Trash2,
  Wrench,
} from 'lucide-react'
import { cn } from '@/lib/utils'

const HARNESS_ORDER: HarnessKey[] = ['claude', 'codex', 'opencode', 'gemini', 'grok', 'mimo', 'pi']

const HARNESS_LABELS: Record<HarnessKey, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
  opencode: 'OpenCode',
  gemini: 'Gemini CLI',
  grok: 'Grok',
  mimo: 'MiMo',
  pi: 'Pi',
}

const HARNESS_ICONS: Record<HarnessKey, string> = {
  claude: '\u25C6',
  codex: '\u25A0',
  opencode: '\u25CB',
  gemini: '\u2726',
  grok: '\u2715',
  mimo: '\u25CF',
  pi: 'P',
}

const MCP_CLIENT_BY_HARNESS: Partial<Record<HarnessKey, string>> = {
  claude: 'claude_code',
  codex: 'codex',
  opencode: 'opencode',
  gemini: 'gemini_cli',
  grok: 'grok',
  mimo: 'mimocode',
}

const HARNESS_MCP_CLIENT_IDS = new Set<string>(Object.values(MCP_CLIENT_BY_HARNESS))
const PI_SETUP_URL = 'https://github.com/don-works/mcplexer/blob/main/docs/harnesses.md#pi-native-extension'

const CLIENT_ICONS: Record<string, string> = {
  claude_desktop: 'CD',
  cursor: 'Cu',
  windsurf: 'W',
  picoclaw: 'Pc',
}

function formatAge(iso: string | null): string {
  if (!iso) return 'Never'
  const diff = Date.now() - new Date(iso).getTime()
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const days = Math.floor(hr / 24)
  return `${days}d ago`
}

function bootstrapLabel(row: HarnessSetupRow): string {
  if (!row.bootstrap_installed) return 'Bootstrap missing'
  if (row.drifted) return `Bootstrap v${row.bootstrap_version} outdated`
  return `Bootstrap v${row.bootstrap_version} installed`
}

function bootstrapTitle(row: HarnessSetupRow): string {
  const name = HARNESS_LABELS[row.key] ?? row.key
  if (!row.bootstrap_installed) {
    return `${name} is missing the using-mcplexer bootstrap.`
  }
  if (row.drifted) {
    return `Bootstrap v${row.bootstrap_version} is installed, but v${row.registry_version} is available.`
  }
  return `Bootstrap v${row.bootstrap_version} is installed.`
}

function bootstrapTone(row: HarnessSetupRow): 'success' | 'warn' | 'critical' {
  if (!row.bootstrap_installed) return 'critical'
  if (row.drifted) return 'warn'
  return 'success'
}

function needsBootstrapInstall(row: HarnessSetupRow): boolean {
  return !row.bootstrap_installed
}

function isNativeHarness(key: HarnessKey): boolean {
  return key === 'pi'
}

function connectionLabel(row: HarnessSetupRow): string {
  if (isNativeHarness(row.key)) {
    return row.last_initialize_at ? 'Native extension seen' : 'Native extension'
  }
  return row.mcp_wired ? 'MCP server configured' : 'MCP server missing'
}

function connectionTitle(row: HarnessSetupRow): string {
  const name = HARNESS_LABELS[row.key] ?? row.key
  if (isNativeHarness(row.key)) {
    return `${name} uses the native Pi extension, not a generic MCP server entry.`
  }
  if (row.mcp_wired) {
    return `${name} is configured to use MCPlexer as an MCP server.`
  }
  return `${name} does not have MCPlexer configured as an MCP server.`
}

function connectionTone(row: HarnessSetupRow): 'success' | 'warn' | 'critical' {
  if (isNativeHarness(row.key)) return row.last_initialize_at ? 'success' : 'warn'
  return row.mcp_wired ? 'success' : 'critical'
}

export function HarnessSetupPage() {
  const harnessFetcher = useCallback(() => getHarnessSetupStatus(), [])
  const mcpFetcher = useCallback(() => getMCPInstallStatus(), [])
  const { data: harnessStatus, loading: harnessLoading, refetch: refetchHarness } = useApi(harnessFetcher)
  const { data: mcpStatus, loading: mcpLoading, refetch: refetchMCP } = useApi(mcpFetcher)

  const [busyHarness, setBusyHarness] = useState<HarnessKey | null>(null)
  const [busyBootstrapAction, setBusyBootstrapAction] = useState<'install' | 'reinstall' | 'recheck' | null>(null)
  const [busyClient, setBusyClient] = useState<string | null>(null)
  const [busyClientAction, setBusyClientAction] = useState<'install' | 'uninstall' | null>(null)
  const [dialogClient, setDialogClient] = useState<MCPClient | null>(null)
  const [preview, setPreview] = useState<MCPInstallPreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)

  const rows = harnessStatus?.harnesses ?? []
  const rowMap = useMemo(() => {
    const m = new Map<HarnessKey, HarnessSetupRow>()
    for (const r of rows) m.set(r.key, r)
    return m
  }, [rows])

  const mcpClientMap = useMemo(() => {
    const m = new Map<string, MCPClient>()
    for (const c of mcpStatus?.clients ?? []) m.set(c.id, c)
    return m
  }, [mcpStatus])

  const otherMCPClients = useMemo(() => {
    return (mcpStatus?.clients ?? [])
      .filter((c) => !HARNESS_MCP_CLIENT_IDS.has(c.id) && c.id !== 'pi_cli')
      .sort((a, b) => {
        if (a.configured !== b.configured) return a.configured ? -1 : 1
        if (a.detected !== b.detected) return a.detected ? -1 : 1
        return a.name.localeCompare(b.name)
      })
  }, [mcpStatus])

  const loading = harnessLoading || mcpLoading
  const serverEntryJSON = mcpStatus
    ? JSON.stringify({ mcpServers: { mcplexer: mcpStatus.server_entry } }, null, 2)
    : ''

  function refreshAll() {
    refetchHarness()
    refetchMCP()
  }

  async function handleBootstrap(harness: HarnessKey, action: 'install' | 'reinstall' | 'recheck') {
    setBusyHarness(harness)
    setBusyBootstrapAction(action)
    try {
      if (action === 'recheck') {
        await recheckHarness(harness)
      } else {
        await installHarness(harness)
      }
      toast.success(`${HARNESS_LABELS[harness]} ${action === 'recheck' ? 'status checked' : 'bootstrap installed'} successfully.`)
      refreshAll()
    } catch (err: unknown) {
      if (err instanceof Error) {
        try {
          const parsed = JSON.parse(err.message.replace(/^API error \d+: /, ''))
          const msg = parsed?.error?.message ?? err.message
          const hint = parsed?.error?.hint ?? ''
          toast.error(msg + (hint ? ` - ${hint}` : ''))
        } catch {
          toast.error(err.message)
        }
      } else {
        toast.error('Unknown error')
      }
    } finally {
      setBusyHarness(null)
      setBusyBootstrapAction(null)
    }
  }

  async function handleMCPInstallClick(client: MCPClient) {
    setDialogClient(client)
    setPreview(null)
    setPreviewLoading(true)
    try {
      setPreview(await previewMCPInstall(client.id))
    } catch {
      setPreview(null)
    } finally {
      setPreviewLoading(false)
    }
  }

  async function handleConfirmMCPInstall() {
    if (!dialogClient) return
    setBusyClient(dialogClient.id)
    setBusyClientAction('install')
    try {
      await installMCP(dialogClient.id)
      toast.success(`Installed. Restart ${dialogClient.name} to pick up changes.`)
      refreshAll()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Install failed')
    } finally {
      setBusyClient(null)
      setBusyClientAction(null)
      setDialogClient(null)
    }
  }

  async function handleMCPUninstall(client: MCPClient) {
    setBusyClient(client.id)
    setBusyClientAction('uninstall')
    try {
      await uninstallMCP(client.id)
      toast.success(`Removed from ${client.name}.`)
      refreshAll()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Uninstall failed')
    } finally {
      setBusyClient(null)
      setBusyClientAction(null)
    }
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">AI Harnesses</h1>
          <p className="max-w-2xl text-sm text-muted-foreground">
            MCP wiring, native Pi setup, bootstrap status, and last initialization in one place.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={refreshAll}
          disabled={loading}
        >
          <RefreshCw className={cn('mr-1.5 h-3 w-3', loading && 'animate-spin')} />
          Refresh
        </Button>
      </header>

      {loading && !harnessStatus ? (
        <div className="space-y-3">
          {HARNESS_ORDER.map((key) => (
            <div key={key} className="animate-pulse rounded-lg border border-border p-4 space-y-3">
              <div className="flex items-center gap-2.5">
                <div className="h-4 w-4 rounded bg-muted" />
                <div className="h-3.5 w-24 rounded bg-muted" />
                <div className="ml-auto h-5 w-16 rounded-full bg-muted" />
              </div>
              <div className="flex gap-2">
                <div className="h-8 w-20 rounded bg-muted/40" />
                <div className="h-8 w-20 rounded bg-muted/40" />
                <div className="h-8 w-20 rounded bg-muted/40" />
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div className="space-y-3">
          {HARNESS_ORDER.map((key) => {
            const row = rowMap.get(key)
            if (!row) return null
            const clientID = MCP_CLIENT_BY_HARNESS[key]
            const client = clientID ? mcpClientMap.get(clientID) : undefined
            return (
              <HarnessRow
                key={key}
                row={row}
                mcpClient={client}
                bootstrapBusy={busyHarness === key}
                busyBootstrapAction={busyHarness === key ? busyBootstrapAction : null}
                clientBusy={client ? busyClient === client.id : false}
                busyClientAction={client && busyClient === client.id ? busyClientAction : null}
                onInstallBootstrap={() => handleBootstrap(key, 'install')}
                onReinstallBootstrap={() => handleBootstrap(key, 'reinstall')}
                onRecheck={() => handleBootstrap(key, 'recheck')}
                onInstallMCP={client ? () => handleMCPInstallClick(client) : undefined}
                onUninstallMCP={client ? () => handleMCPUninstall(client) : undefined}
              />
            )
          })}
        </div>
      )}

      {mcpStatus && (
        <section className="space-y-3">
          <div className="flex items-center justify-between gap-3">
            <div>
              <h2 className="text-sm font-semibold">Other MCP Clients</h2>
              <p className="text-xs text-muted-foreground">
                Desktop and server-prefixed clients that only need an MCP server entry.
              </p>
            </div>
          </div>
          {otherMCPClients.length > 0 && (
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {otherMCPClients.map((client) => (
                <MCPClientCard
                  key={client.id}
                  client={client}
                  busy={busyClient === client.id}
                  busyAction={busyClient === client.id ? busyClientAction : null}
                  onInstall={() => handleMCPInstallClick(client)}
                  onUninstall={() => handleMCPUninstall(client)}
                />
              ))}
            </div>
          )}
          <ManualConfig value={serverEntryJSON} />
        </section>
      )}

      <Dialog open={!!dialogClient} onOpenChange={(open) => !open && setDialogClient(null)}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Install to {dialogClient?.name}</DialogTitle>
            <DialogDescription>
              This will add the MCPlexer server entry to{' '}
              <code className="rounded bg-muted px-1 py-0.5 text-xs font-mono">
                {dialogClient?.config_path}
              </code>
            </DialogDescription>
          </DialogHeader>

          <div className="max-h-72 overflow-auto rounded-md border border-border bg-muted/30">
            {previewLoading ? (
              <div className="flex items-center justify-center py-8">
                <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
              </div>
            ) : preview ? (
              <pre className="p-4 font-mono text-xs leading-relaxed text-foreground">
                {preview.content}
              </pre>
            ) : (
              <p className="p-4 text-sm text-muted-foreground">
                Failed to load preview.
              </p>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogClient(null)} data-testid="mcp-install-cancel">
              Cancel
            </Button>
            <Button
              onClick={handleConfirmMCPInstall}
              disabled={busyClient !== null}
              data-testid="mcp-install-confirm"
            >
              {busyClient ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <Download className="mr-2 h-4 w-4" />
              )}
              Install
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function HarnessRow({
  row,
  mcpClient,
  bootstrapBusy,
  busyBootstrapAction,
  clientBusy,
  busyClientAction,
  onInstallBootstrap,
  onReinstallBootstrap,
  onRecheck,
  onInstallMCP,
  onUninstallMCP,
}: {
  row: HarnessSetupRow
  mcpClient?: MCPClient
  bootstrapBusy: boolean
  busyBootstrapAction: 'install' | 'reinstall' | 'recheck' | null
  clientBusy: boolean
  busyClientAction: 'install' | 'uninstall' | null
  onInstallBootstrap: () => void
  onReinstallBootstrap: () => void
  onRecheck: () => void
  onInstallMCP?: () => void
  onUninstallMCP?: () => void
}) {
  const icon = HARNESS_ICONS[row.key]
  const label = HARNESS_LABELS[row.key]
  const showInstall = needsBootstrapInstall(row)
  const extraSkills = row.accretion?.extra_skills ?? []
  const extraCommands = row.accretion?.extra_commands ?? []
  const hasAccretion = extraSkills.length > 0 || extraCommands.length > 0
  const native = isNativeHarness(row.key)
  const setupPath = row.config_path || mcpClient?.config_path || 'N/A'

  return (
    <div
      className="rounded-lg border border-border p-4 space-y-3"
      data-testid={`harness-row-${row.key}`}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="text-base shrink-0">{icon}</span>
          <div className="min-w-0">
            <p className="text-sm font-medium leading-none truncate">{label}</p>
            <p className="mt-1 font-mono text-[10px] text-muted-foreground truncate">
              {setupPath}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:ml-auto sm:justify-end">
          <Badge
            variant="outline"
            tone={connectionTone(row)}
            className="text-[10px] px-1.5"
            title={connectionTitle(row)}
          >
            {connectionTone(row) === 'success' ? (
              <CheckCircle2 className="h-2.5 w-2.5" />
            ) : (
              <AlertCircle className="h-2.5 w-2.5" />
            )}{' '}
            {connectionLabel(row)}
          </Badge>
          <Badge
            variant="outline"
            tone={bootstrapTone(row)}
            className="text-[10px] px-1.5"
            title={bootstrapTitle(row)}
          >
            <Wrench className="h-2.5 w-2.5" /> {bootstrapLabel(row)}
          </Badge>
          <span className="text-[10px] text-muted-foreground tabular-nums" title={row.last_initialize_at ?? undefined}>
            {formatAge(row.last_initialize_at)}
          </span>
          {row.client_info && (
            <span className="text-[10px] text-muted-foreground/60 truncate max-w-32">
              {row.client_info}
            </span>
          )}
        </div>
      </div>
      {hasAccretion && (
        <div className="flex gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-2 text-xs text-amber-900 dark:text-amber-200">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <div className="min-w-0 space-y-1">
            <p className="font-medium">Your {label} config has extra skills that may conflict</p>
            {extraSkills.length > 0 && (
              <p className="break-words">
                Skills: <span className="font-mono">{extraSkills.join(', ')}</span>
              </p>
            )}
            {extraCommands.length > 0 && (
              <p className="break-words">
                Commands: <span className="font-mono">{extraCommands.join(', ')}</span>
              </p>
            )}
          </div>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        {showInstall ? (
          <Button
            size="sm"
            className="text-xs h-8"
            disabled={bootstrapBusy}
            onClick={onInstallBootstrap}
            data-testid={`harness-install-${row.key}`}
          >
            {bootstrapBusy && busyBootstrapAction === 'install' ? (
              <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
            ) : (
              <Download className="mr-1.5 h-3 w-3" />
            )}
            Install Bootstrap
          </Button>
        ) : (
          <Button
            variant="outline"
            size="sm"
            className="text-xs h-8"
            disabled={bootstrapBusy}
            onClick={onReinstallBootstrap}
            data-testid={`harness-reinstall-${row.key}`}
          >
            {bootstrapBusy && busyBootstrapAction === 'reinstall' ? (
              <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
            ) : (
              <RotateCcw className="mr-1.5 h-3 w-3" />
            )}
            Reinstall Bootstrap
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          className="text-xs h-8"
          disabled={bootstrapBusy}
          onClick={onRecheck}
          data-testid={`harness-recheck-${row.key}`}
        >
          {bootstrapBusy && busyBootstrapAction === 'recheck' ? (
            <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
          ) : (
            <RefreshCw className="mr-1.5 h-3 w-3" />
          )}
          Check Status
        </Button>
        {native ? (
          <Button
            asChild
            variant="outline"
            size="sm"
            className="text-xs h-8"
            data-testid={`harness-pi-docs-${row.key}`}
          >
            <a href={PI_SETUP_URL} target="_blank" rel="noreferrer">
              <BookOpen className="mr-1.5 h-3 w-3" />
              Pi Setup Notes
            </a>
          </Button>
        ) : row.mcp_wired ? (
          mcpClient && onUninstallMCP && (
            <Button
              variant="outline"
              size="sm"
              className="text-xs h-8"
              disabled={clientBusy}
              onClick={onUninstallMCP}
              data-testid={`harness-remove-mcp-${row.key}`}
            >
              {clientBusy && busyClientAction === 'uninstall' ? (
                <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
              ) : (
                <Trash2 className="mr-1.5 h-3 w-3" />
              )}
              Remove MCP Server
            </Button>
          )
        ) : (
          mcpClient && onInstallMCP && (
            <Button
              variant="outline"
              size="sm"
              className="text-xs h-8"
              disabled={clientBusy || !mcpClient.detected}
              onClick={onInstallMCP}
              data-testid={`harness-configure-mcp-${row.key}`}
            >
              {clientBusy && busyClientAction === 'install' ? (
                <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
              ) : (
                <Plug className="mr-1.5 h-3 w-3" />
              )}
              {mcpClient.detected ? 'Set up MCPlexer' : 'Client not found'}
            </Button>
          )
        )}
      </div>
    </div>
  )
}

function MCPClientCard({
  client,
  busy,
  busyAction,
  onInstall,
  onUninstall,
}: {
  client: MCPClient
  busy: boolean
  busyAction: 'install' | 'uninstall' | null
  onInstall: () => void
  onUninstall: () => void
}) {
  const icon = CLIENT_ICONS[client.id] ?? 'MCP'

  return (
    <div
      className={cn(
        'rounded-lg border border-border p-4 space-y-3 transition-colors',
        !client.detected && !client.configured && 'opacity-60',
      )}
      data-testid={`mcp-client-${client.id}`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{icon}</span>
          <div className="min-w-0">
            <p className="text-sm font-medium leading-none truncate">{client.name}</p>
            <p className="mt-1 font-mono text-[10px] text-muted-foreground truncate">
              {client.config_path || 'N/A'}
            </p>
          </div>
        </div>
        <ClientStatusBadge client={client} />
      </div>
      <div className="flex gap-2">
        {!client.detected && !client.configured ? (
          <span className="flex-1 inline-flex items-center justify-center h-8 border border-border/50 text-[11px] text-muted-foreground/60">
            Not Found
          </span>
        ) : client.configured ? (
          <Button
            variant="outline"
            size="sm"
            className="flex-1 text-xs h-8"
            disabled={busy}
            onClick={onUninstall}
            data-testid={`mcp-uninstall-${client.id}`}
          >
            {busy && busyAction === 'uninstall' ? (
              <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
            ) : (
              <Trash2 className="mr-1.5 h-3 w-3" />
            )}
            Uninstall
          </Button>
        ) : (
          <Button
            size="sm"
            className="flex-1 text-xs h-8"
            disabled={busy}
            onClick={onInstall}
            data-testid={`mcp-install-${client.id}`}
          >
            {busy && busyAction === 'install' ? (
              <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
            ) : (
              <Eye className="mr-1.5 h-3 w-3" />
            )}
            Install
          </Button>
        )}
      </div>
    </div>
  )
}

function ClientStatusBadge({ client }: { client: MCPClient }) {
  if (client.configured) {
    return (
      <Badge className="border-0 bg-emerald-500/15 text-emerald-600 text-[10px] px-1.5 shrink-0">
        Installed
      </Badge>
    )
  }
  if (client.detected) {
    return (
      <Badge variant="outline" className="text-[10px] px-1.5 shrink-0">
        Available
      </Badge>
    )
  }
  return (
    <Badge variant="outline" className="text-[10px] text-muted-foreground border-muted px-1.5 shrink-0">
      Not Found
    </Badge>
  )
}

function ManualConfig({ value }: { value: string }) {
  if (!value) return null
  return (
    <div className="rounded-lg border border-border p-4 space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-center gap-2.5">
          <ClipboardCopy className="h-[18px] w-[18px] text-muted-foreground" />
          <div>
            <p className="text-sm font-medium leading-none">Manual MCP config</p>
            <p className="mt-1 text-[11px] text-muted-foreground">
              Copy into any MCP client that is not listed above.
            </p>
          </div>
        </div>
        <CopyButton value={value} />
      </div>
      <div className="relative rounded-md border border-border bg-muted/30">
        <pre className="overflow-x-auto p-3 font-mono text-[10px] leading-relaxed text-foreground max-h-48">
          {value}
        </pre>
      </div>
    </div>
  )
}
