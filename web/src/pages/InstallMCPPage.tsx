import { useCallback, useMemo, useState } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useApi } from '@/hooks/use-api'
import {
  getMCPInstallStatus,
  installMCP,
  previewMCPInstall,
  uninstallMCP,
} from '@/api/client'
import type { MCPClient, MCPInstallPreview } from '@/api/types'
import { toast } from 'sonner'
import {
  ClipboardCopy,
  Download,
  Eye,
  Loader2,
  Trash2,
} from 'lucide-react'
import { CopyButton } from '@/components/ui/copy-button'
import { cn } from '@/lib/utils'

const CLIENT_ICONS: Record<string, string> = {
  claude_desktop: '🖥️',
  claude_code: '⌨️',
  cursor: '▲',
  windsurf: '🏄',
  codex: '📦',
  opencode: '🔓',
  gemini_cli: '✦',
  grok: 'X',
  mimocode: 'M',
}

export function InstallMCPPage() {
  const statusFetcher = useCallback(() => getMCPInstallStatus(), [])
  const { data: status, loading, refetch } = useApi(statusFetcher)

  const [dialogClient, setDialogClient] = useState<MCPClient | null>(null)
  const [preview, setPreview] = useState<MCPInstallPreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [installing, setInstalling] = useState<string | null>(null)

  async function handleInstallClick(client: MCPClient) {
    setDialogClient(client)
    setPreview(null)
    setPreviewLoading(true)
    try {
      const p = await previewMCPInstall(client.id)
      setPreview(p)
    } catch {
      setPreview(null)
    } finally {
      setPreviewLoading(false)
    }
  }

  async function handleConfirmInstall() {
    if (!dialogClient) return
    setInstalling(dialogClient.id)
    try {
      await installMCP(dialogClient.id)
      toast.success(`Installed. Restart ${dialogClient.name} to pick up changes.`)
      refetch()
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Install failed'
      toast.error(message)
    } finally {
      setInstalling(null)
      setDialogClient(null)
    }
  }

  async function handleUninstall(client: MCPClient) {
    setInstalling(client.id)
    try {
      await uninstallMCP(client.id)
      toast.success(`Removed from ${client.name}.`)
      refetch()
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Uninstall failed'
      toast.error(message)
    } finally {
      setInstalling(null)
    }
  }

  const serverEntryJSON = status
    ? JSON.stringify({ mcpServers: { mx: status.server_entry } }, null, 2)
    : ''

  const sortedClients = useMemo(() => {
    if (!status?.clients) return []
    return [...status.clients].sort((a, b) => {
      if (a.configured !== b.configured) return a.configured ? -1 : 1
      if (a.detected !== b.detected) return a.detected ? -1 : 1
      return a.name.localeCompare(b.name)
    })
  }, [status])

  const clientCounts = useMemo(() => {
    if (!status?.clients) return { installed: 0, available: 0, notFound: 0 }
    let installed = 0, available = 0, notFound = 0
    for (const c of status.clients) {
      if (c.configured) installed++
      else if (c.detected) available++
      else notFound++
    }
    return { installed, available, notFound }
  }, [status])

  return (
    <div className="space-y-5">
      <header className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">Install MCP</h1>
          <p className="max-w-2xl text-sm text-muted-foreground">
            Configure MCPlexer as an MCP server in your AI tools.
          </p>
        </div>
      </header>

      {loading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="animate-pulse rounded-lg border border-border p-4 space-y-3">
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-2.5">
                  <div className="h-4 w-4 rounded bg-muted" />
                  <div className="space-y-1.5">
                    <div className="h-3.5 w-24 rounded bg-muted" />
                    <div className="h-2.5 w-32 rounded bg-muted/60" />
                  </div>
                </div>
                <div className="h-5 w-16 rounded-full bg-muted" />
              </div>
              <div className="h-8 rounded bg-muted/40" />
            </div>
          ))}
        </div>
      ) : (
        <>
          {/* Status summary */}
          {status && (
            <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
              <span className="inline-flex items-center gap-1.5">
                <span className="h-2 w-2 rounded-full bg-emerald-500/70" />
                <span className="tabular-nums">{clientCounts.installed}</span> installed
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className="h-2 w-2 rounded-full bg-muted-foreground/40" />
                <span className="tabular-nums">{clientCounts.available}</span> available
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className="h-2 w-2 rounded-full border border-muted-foreground/20" />
                <span className="tabular-nums">{clientCounts.notFound}</span> not found
              </span>
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {sortedClients.map((client) => (
              <ToolCard
                key={client.id}
                client={client}
                installing={installing === client.id}
                onInstall={() => handleInstallClick(client)}
                onUninstall={() => handleUninstall(client)}
              />
            ))}

            {/* Other Tools — raw JSON card */}
            <Card>
              <CardContent className="flex flex-col gap-3 pt-5">
                <div className="flex items-start justify-between">
                  <div className="flex items-center gap-2.5">
                    <ClipboardCopy className="h-[18px] w-[18px] text-muted-foreground" />
                    <div>
                      <p className="text-sm font-medium leading-none">Other</p>
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        Copy into your MCP client&apos;s config
                      </p>
                    </div>
                  </div>
                  <CopyButton value={serverEntryJSON} />
                </div>
                <div className="relative rounded-md border border-border bg-muted/30">
                  <pre className="overflow-x-auto p-3 font-mono text-[10px] leading-relaxed text-foreground max-h-48">
                    {serverEntryJSON}
                  </pre>
                </div>
              </CardContent>
            </Card>
          </div>
        </>
      )}

      {/* Install confirmation dialog */}
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
              onClick={handleConfirmInstall}
              disabled={installing !== null}
              data-testid="mcp-install-confirm"
            >
              {installing ? (
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

function ToolCard({
  client,
  installing,
  onInstall,
  onUninstall,
}: {
  client: MCPClient
  installing: boolean
  onInstall: () => void
  onUninstall: () => void
}) {
  const icon = CLIENT_ICONS[client.id] ?? '🔌'

  return (
    <Card
      className={cn(
        'transition-colors',
        !client.detected && !client.configured && 'opacity-50',
      )}
    >
      <CardContent className="flex flex-col gap-3 pt-4">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-2.5 min-w-0">
            <span className="text-base shrink-0">{icon}</span>
            <div className="min-w-0">
              <p className="text-sm font-medium leading-none truncate">{client.name}</p>
              <p className="mt-1 font-mono text-[10px] text-muted-foreground truncate">
                {client.config_path || 'N/A'}
              </p>
            </div>
          </div>
          <StatusBadge client={client} />
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
              disabled={installing}
              onClick={onUninstall}
              data-testid={`mcp-uninstall-${client.id}`}
            >
              {installing ? (
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
              disabled={installing}
              onClick={onInstall}
              data-testid={`mcp-install-${client.id}`}
            >
              {installing ? (
                <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
              ) : (
                <Eye className="mr-1.5 h-3 w-3" />
              )}
              Install
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function StatusBadge({ client }: { client: MCPClient }) {
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
