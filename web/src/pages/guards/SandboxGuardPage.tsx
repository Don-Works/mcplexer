import { useCallback, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import {
  disableSandbox,
  enableSandbox,
  getSandboxGuardDetail,
  updateSandboxGuard,
} from '@/api/client'
import type { SandboxClientStatus } from '@/api/client'
import { ArrowLeft, Box, Loader2 } from 'lucide-react'
import { toast } from 'sonner'

export function SandboxGuardPage() {
  const fetcher = useCallback(() => getSandboxGuardDetail(), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  const [busy, setBusy] = useState<string | null>(null)
  const [togglingDownstreams, setTogglingDownstreams] = useState(false)

  async function handleDownstreamsToggle() {
    if (!data) return
    setTogglingDownstreams(true)
    try {
      const next = !data.downstreams_enabled
      await updateSandboxGuard(next)
      toast.success(next ? 'Sandboxing downstream MCP servers' : 'Sandbox disabled')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Toggle failed')
    } finally {
      setTogglingDownstreams(false)
    }
  }

  async function handleToggle(client: SandboxClientStatus) {
    setBusy(client.id)
    try {
      if (client.enabled) {
        await disableSandbox(client.id)
        toast.success(`Sandbox disabled for ${client.name}`)
      } else {
        await enableSandbox(client.id)
        toast.success(`Sandbox enabled for ${client.name}`)
      }
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Toggle failed')
    } finally {
      setBusy(null)
    }
  }

  // Driver→platform mapping is informational for the macOS notice; we
  // don't gate the toggle UI on it because the user may still want to
  // configure receipts on a host that doesn't yet have the driver
  // installed.
  const driverLabel =
    data?.driver === 'sandbox-exec'
      ? 'sandbox-exec (macOS)'
      : data?.driver === 'bwrap'
        ? 'bwrap (Linux)'
        : data?.driver === 'unshare'
          ? 'unshare (Linux fallback)'
          : data?.driver || 'unavailable'

  return (
    <div className="space-y-5 max-w-5xl">
      <Link
        to="/guards"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Guards
      </Link>
      <div>
        <h1 className="text-2xl font-bold flex items-center gap-2">
          <Box className="h-6 w-6" /> Sandbox Guard
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Runs AI clients inside an OS-level sandbox so shell commands they
          escape from MCPlexer can't damage the host. macOS uses sandbox-exec;
          Linux uses bubblewrap with an unshare fallback.
        </p>
      </div>

      {loading && !data ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading sandbox status…
        </div>
      ) : error ? (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      ) : data ? (
        <>
          <Card>
            <CardContent className="flex items-center justify-between gap-4 p-4">
              <div>
                <div className="text-sm font-medium">Sandbox driver</div>
                <div className="text-xs text-muted-foreground">
                  Auto-selected from what's available on this host.
                </div>
                {data.driver === 'sandbox-exec' && (
                  <div className="mt-1 text-[10px] text-muted-foreground/70">
                    Sandbox is currently macOS-only on this host.
                  </div>
                )}
              </div>
              {data.unsupported_os ? (
                <Badge variant="outline" className="text-muted-foreground">unsupported OS</Badge>
              ) : (
                <Badge variant="secondary" className="font-mono text-[10px]">{driverLabel}</Badge>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-center justify-between gap-4">
                <div>
                  <div className="text-sm font-medium">
                    Sandbox downstream MCP server processes
                  </div>
                  <div className="text-xs text-muted-foreground mt-0.5">
                    Wraps every spawned MCP server in sandbox-exec so
                    credential paths (<code className="font-mono text-[10px]">~/.ssh</code>,{' '}
                    <code className="font-mono text-[10px]">~/.aws</code>,{' '}
                    <code className="font-mono text-[10px]">~/.docker/config.json</code>)
                    are inaccessible. Some servers legitimately need filesystem
                    access — start narrow.
                  </div>
                </div>
                <Button
                  size="sm"
                  variant={data.downstreams_enabled ? 'outline' : 'default'}
                  disabled={togglingDownstreams || data.unsupported_os}
                  onClick={handleDownstreamsToggle}
                  data-testid="sandbox-downstreams-toggle"
                >
                  {togglingDownstreams && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}
                  {data.downstreams_enabled ? 'Disable' : 'Enable'}
                </Button>
              </div>
              <div className="flex items-center gap-2 text-[11px]">
                <span className="text-muted-foreground">Active:</span>
                <span
                  className={
                    data.downstreams_enabled
                      ? 'font-mono text-emerald-400'
                      : 'font-mono text-muted-foreground'
                  }
                >
                  {data.active_description}
                </span>
              </div>
            </CardContent>
          </Card>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Per-client tracking
            </h2>
            <p className="text-xs text-muted-foreground -mt-1">
              Records the per-client sandbox intent. Real wrapping is
              global (above); per-client enforcement lands when M2.5 ships
              shim binaries.
            </p>
            <Card>
              <CardContent className="p-0">
                {data.clients.length === 0 ? (
                  <div className="p-6 text-center text-sm text-muted-foreground">
                    No installed clients tracked yet. Wire one via the Shell
                    Guard first.
                  </div>
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Client</TableHead>
                        <TableHead>Sandbox</TableHead>
                        <TableHead className="text-right">Action</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {data.clients.map((c) => (
                        <TableRow key={c.id}>
                          <TableCell>
                            <div className="font-medium">{c.name || c.id}</div>
                            <div className="font-mono text-[10px] text-muted-foreground/70">
                              {c.id}
                            </div>
                          </TableCell>
                          <TableCell>
                            {c.enabled ? (
                              <Badge className="bg-emerald-500/10 text-emerald-400 border-emerald-500/30 text-[10px]">
                                enabled
                              </Badge>
                            ) : (
                              <Badge variant="outline" className="text-[10px] text-muted-foreground">
                                disabled
                              </Badge>
                            )}
                          </TableCell>
                          <TableCell className="text-right">
                            <Button
                              size="sm"
                              variant={c.enabled ? 'outline' : 'default'}
                              disabled={busy === c.id || data.unsupported_os}
                              onClick={() => handleToggle(c)}
                              data-testid={`sandbox-toggle-${c.id}`}
                            >
                              {busy === c.id && (
                                <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                              )}
                              {c.enabled ? 'Disable' : 'Enable'}
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
              </CardContent>
            </Card>
          </section>
        </>
      ) : null}
    </div>
  )
}
