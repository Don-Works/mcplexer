import { useCallback, useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'
import {
  AlertTriangle,
  CheckCircle2,
  Clipboard,
  Download,
  HelpCircle,
  RefreshCw,
  ShieldAlert,
  Wand2,
  XCircle,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Separator } from '@/components/ui/separator'
import {
  deleteSecret,
  fetchHammerspoonSnippet,
  hammerspoonSnippetURL,
  installHammerspoon,
  listSecretKeys,
  probeHammerspoon,
  putSecret,
} from '@/api/client'
import type {
  HammerspoonHealth,
  HammerspoonProbeResponse,
} from '@/api/client'
import type { DownstreamServer } from '@/api/types'

const HS_SCOPE_ID = 'hammerspoon-bridge'
const ALLOW_EXEC_KEY = 'HAMMERSPOON_ALLOW_EXEC_LUA'

// Probe checks render in this order regardless of the JSON object key order
// the API happens to return. Keeps the UI deterministic and makes the
// dependency chain obvious (app -> bridge -> auth -> ax -> smoke).
const PROBE_ORDER = [
  'app_running',
  'bridge_reachable',
  'auth_ok',
  'accessibility',
  'smoke',
] as const

const CHECK_LABELS: Record<string, string> = {
  app_running: 'Hammerspoon.app running',
  bridge_reachable: 'Bridge reachable',
  auth_ok: 'Bridge auth',
  accessibility: 'Accessibility permission',
  smoke: 'list_windows smoke test',
}

interface HammerspoonPanelProps {
  open: boolean
  onClose: () => void
  server: DownstreamServer | null
}

// healthFromCache reads the cached probe from a DownstreamServer.CapabilitiesCache
// blob. We re-derive `health` from the cached `health` field rather than
// re-computing it, so the dashboard and the panel stay in lockstep with what
// the backend wrote.
function healthFromCache(server: DownstreamServer | null): HammerspoonHealth | null {
  if (!server) return null
  const cache = server.capabilities_cache
  if (!cache || typeof cache !== 'object') return null
  const h = (cache as Record<string, unknown>).health
  if (h === 'ok' || h === 'degraded' || h === 'broken') return h
  return null
}

function probeFromCache(server: DownstreamServer | null): HammerspoonProbeResponse | null {
  if (!server) return null
  const cache = server.capabilities_cache
  if (!cache || typeof cache !== 'object') return null
  const c = cache as Record<string, unknown>
  if (!c.checks || typeof c.checks !== 'object') return null
  const health = c.health
  if (health !== 'ok' && health !== 'degraded' && health !== 'broken') return null
  return cache as unknown as HammerspoonProbeResponse
}

// HealthBadge renders a one-glance traffic light. The button on the card uses
// this same component so the card and the panel header agree visually.
export function HealthBadge({
  health,
  className,
}: {
  health: HammerspoonHealth | null
  className?: string
}) {
  if (health === 'ok') {
    return (
      <Badge tone="success" className={className} data-testid="hammerspoon-health-ok">
        <CheckCircle2 className="h-3 w-3" /> Healthy
      </Badge>
    )
  }
  if (health === 'degraded') {
    return (
      <Badge tone="warn" className={className} data-testid="hammerspoon-health-degraded">
        <AlertTriangle className="h-3 w-3" /> Degraded
      </Badge>
    )
  }
  if (health === 'broken') {
    return (
      <Badge tone="critical" className={className} data-testid="hammerspoon-health-broken">
        <XCircle className="h-3 w-3" /> Broken
      </Badge>
    )
  }
  return (
    <Badge tone="muted" className={className} data-testid="hammerspoon-health-unknown">
      <HelpCircle className="h-3 w-3" /> Not probed
    </Badge>
  )
}

export function HammerspoonPanel({ open, onClose, server }: HammerspoonPanelProps) {
  const cachedProbe = useMemo(() => probeFromCache(server), [server])
  const [probe, setProbe] = useState<HammerspoonProbeResponse | null>(cachedProbe)
  const [probing, setProbing] = useState(false)
  const [installing, setInstalling] = useState(false)
  const [copying, setCopying] = useState(false)
  const [confirmRotate, setConfirmRotate] = useState(false)
  const [storedKeys, setStoredKeys] = useState<string[]>([])
  const [allowExecToggling, setAllowExecToggling] = useState(false)

  // Sync local probe state with the cache whenever the server row changes (e.g.
  // a refetch after install). Subtle: we don't auto-probe on open — that's an
  // explicit user action by design (it touches the user's filesystem).
  useEffect(() => {
    setProbe(cachedProbe)
  }, [cachedProbe])

  const refreshSecretKeys = useCallback(async () => {
    try {
      const res = await listSecretKeys(HS_SCOPE_ID)
      setStoredKeys(res.keys)
    } catch {
      setStoredKeys([])
    }
  }, [])

  useEffect(() => {
    if (!open) return
    refreshSecretKeys()
  }, [open, refreshSecretKeys])

  const allowExecLua = storedKeys.includes(ALLOW_EXEC_KEY)

  const handleProbe = useCallback(async () => {
    setProbing(true)
    try {
      const res = await probeHammerspoon()
      setProbe(res)
      if (res.health === 'ok') {
        toast.success('Hammerspoon bridge is healthy')
      } else if (res.health === 'degraded') {
        toast.warning('Hammerspoon bridge is degraded — see remediation')
      } else {
        toast.error('Hammerspoon bridge is broken — see remediation')
      }
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Probe failed')
    } finally {
      setProbing(false)
    }
  }, [])

  const handleInstall = useCallback(async () => {
    setInstalling(true)
    try {
      const res = await installHammerspoon()
      const parts: string[] = []
      if (res.files_written.length > 0) {
        parts.push(`Wrote ${res.files_written.join(', ')}`)
      }
      if (res.init_lua_modified) {
        parts.push('appended require to init.lua')
      }
      if (!res.reload_attempted) {
        parts.push('reload skipped — open Hammerspoon menu → Reload Config')
      } else if (res.reload_error) {
        parts.push(`reload error: ${res.reload_error}`)
      }
      toast.success(parts.join(' · ') || 'Bridge installed')
      refreshSecretKeys()
    } catch (err: unknown) {
      // The handler returns {error, step} JSON in non-2xx bodies. The shared
      // ApiClientError captures the raw body — we surface it verbatim so the
      // user can see which step broke without us having to mirror the schema.
      toast.error(err instanceof Error ? err.message : 'Install failed')
    } finally {
      setInstalling(false)
    }
  }, [refreshSecretKeys])

  const handleCopySnippet = useCallback(async () => {
    setCopying(true)
    try {
      const text = await fetchHammerspoonSnippet()
      await navigator.clipboard.writeText(text)
      toast.success('Snippet copied to clipboard')
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Copy failed')
    } finally {
      setCopying(false)
    }
  }, [])

  const handleToggleAllowExec = useCallback(
    async (next: boolean) => {
      setAllowExecToggling(true)
      try {
        if (next) {
          await putSecret(HS_SCOPE_ID, ALLOW_EXEC_KEY, 'true')
          toast.success('exec_lua enabled — restart the bridge to take effect')
        } else {
          await deleteSecret(HS_SCOPE_ID, ALLOW_EXEC_KEY)
          toast.success('exec_lua disabled')
        }
        await refreshSecretKeys()
      } catch (err: unknown) {
        toast.error(err instanceof Error ? err.message : 'Failed to update setting')
      } finally {
        setAllowExecToggling(false)
      }
    },
    [refreshSecretKeys],
  )

  // Regenerate password reuses /install — it rotates on every call. We surface
  // it as a separate affordance because users will think of it that way; the
  // backend doesn't need a second endpoint.
  const handleConfirmRotate = useCallback(async () => {
    setConfirmRotate(false)
    await handleInstall()
  }, [handleInstall])

  const health = probe?.health ?? healthFromCache(server)
  const remediation = probe?.remediation ?? []
  const nextSteps = useMemo(() => {
    if (!probe) return [] as string[]
    const out: string[] = []
    if (probe.checks.app_running && !probe.checks.app_running.ok) {
      out.push('Install + launch Hammerspoon.app from hammerspoon.org')
    }
    if (probe.checks.accessibility && !probe.checks.accessibility.ok) {
      out.push('Grant Accessibility in System Settings → Privacy & Security → Accessibility')
    }
    return out
  }, [probe])

  return (
    <>
      <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
        <DialogContent className="sm:max-w-2xl" data-testid="hammerspoon-panel">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              Hammerspoon bridge
              <HealthBadge health={health} />
            </DialogTitle>
            <DialogDescription>
              macOS desktop automation. Install the Lua bridge into{' '}
              <span className="font-mono">~/.hammerspoon/</span>, then probe to confirm the bridge
              is reachable and Accessibility is granted.
            </DialogDescription>
          </DialogHeader>

          {nextSteps.length > 0 && (
            <div className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-300">
              <div className="mb-1 font-medium">Next steps</div>
              <ul className="list-inside list-disc space-y-1 text-xs">
                {nextSteps.map((s) => (
                  <li key={s}>{s}</li>
                ))}
              </ul>
            </div>
          )}

          <section className="space-y-2">
            <h3 className="text-sm font-medium">Bridge install</h3>
            <p className="text-xs text-muted-foreground">
              Writes <span className="font-mono">~/.hammerspoon/hammerspoon-mcp.lua</span> and a
              fresh 0600 password file, then appends one <span className="font-mono">require</span>{' '}
              line to your <span className="font-mono">init.lua</span> (with a timestamped backup).
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                onClick={handleInstall}
                disabled={installing}
                data-testid="hammerspoon-install"
              >
                <Wand2 className="mr-1.5 h-3.5 w-3.5" />
                {installing ? 'Installing…' : 'Install bridge'}
              </Button>
              <Button
                variant="outline"
                onClick={handleCopySnippet}
                disabled={copying}
                data-testid="hammerspoon-copy-snippet"
              >
                <Clipboard className="mr-1.5 h-3.5 w-3.5" />
                {copying ? 'Copying…' : 'Copy snippet'}
              </Button>
              <Button asChild variant="outline" data-testid="hammerspoon-download-snippet">
                <a href={hammerspoonSnippetURL()} download>
                  <Download className="mr-1.5 h-3.5 w-3.5" />
                  Download .lua
                </a>
              </Button>
              <Button
                variant="outline"
                onClick={() => setConfirmRotate(true)}
                disabled={installing}
                data-testid="hammerspoon-rotate"
              >
                <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                Regenerate password
              </Button>
            </div>
          </section>

          <Separator />

          <section className="space-y-2">
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-medium">Diagnostic</h3>
              <Button
                variant="outline"
                size="sm"
                onClick={handleProbe}
                disabled={probing}
                data-testid="hammerspoon-probe"
              >
                <RefreshCw className={`mr-1.5 h-3.5 w-3.5 ${probing ? 'animate-spin' : ''}`} />
                {probing ? 'Probing…' : 'Probe now'}
              </Button>
            </div>
            {!probe && (
              <p className="text-xs text-muted-foreground" data-testid="hammerspoon-no-probe">
                No probe yet. Click <span className="font-medium">Probe now</span> after installing
                the bridge.
              </p>
            )}
            {probe && (
              <ul className="space-y-1.5" data-testid="hammerspoon-checks">
                {PROBE_ORDER.map((key) => {
                  const c = probe.checks[key]
                  if (!c) return null
                  return (
                    <li
                      key={key}
                      className="flex items-start gap-2 rounded-md border border-border/50 p-2"
                      data-testid={`hammerspoon-check-${key}`}
                    >
                      {c.ok ? (
                        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-500" />
                      ) : (
                        <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-red-500" />
                      )}
                      <div className="flex-1">
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-sm font-medium">
                            {CHECK_LABELS[key] ?? key}
                          </span>
                          <span className="font-mono text-[10px] text-muted-foreground">
                            {c.duration_ms}ms
                          </span>
                        </div>
                        {c.detail && (
                          <p className="mt-0.5 text-xs text-muted-foreground">{c.detail}</p>
                        )}
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}
            {remediation.length > 0 && (
              <div className="space-y-2" data-testid="hammerspoon-remediation">
                {remediation.map((r) => (
                  <div
                    key={r.check}
                    className="rounded-md border border-amber-500/30 bg-amber-500/5 p-2 text-xs"
                  >
                    <div className="font-medium text-amber-300">{r.title}</div>
                    <div className="mt-0.5 text-muted-foreground">{r.body}</div>
                  </div>
                ))}
              </div>
            )}
          </section>

          <Separator />

          <section className="space-y-2">
            <h3 className="text-sm font-medium">Advanced</h3>
            <label
              className="flex cursor-pointer items-start gap-2 rounded-md border border-border/50 p-2.5"
              data-testid="hammerspoon-allow-exec-row"
            >
              <Checkbox
                checked={allowExecLua}
                disabled={allowExecToggling}
                onCheckedChange={(v) => handleToggleAllowExec(v === true)}
                data-testid="hammerspoon-allow-exec-checkbox"
              />
              <div className="flex-1">
                <div className="flex items-center gap-1.5 text-sm font-medium">
                  <ShieldAlert className="h-3.5 w-3.5 text-amber-400" />
                  Allow <span className="font-mono">exec_lua</span>
                </div>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  Enables raw Lua execution. Off unless you need it.
                </p>
              </div>
            </label>
          </section>

          <DialogFooter>
            <Button variant="outline" onClick={onClose}>
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmRotate}
        onOpenChange={setConfirmRotate}
        title="Regenerate bridge password?"
        description="This rotates the bridge password and rewrites ~/.hammerspoon/.mcp-password. Hammerspoon will reload."
        confirmLabel="Regenerate"
        onConfirm={handleConfirmRotate}
      />
    </>
  )
}

// HammerspoonStatusButton is a one-glance entry point we mount inside
// ServerCard. Clicking it opens the panel; the badge reflects the cached
// health so the user sees the right colour even before clicking.
export function HammerspoonStatusButton({
  server,
  onOpen,
}: {
  server: DownstreamServer
  onOpen: () => void
}) {
  const health = healthFromCache(server)
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-7 gap-1.5 px-1.5"
      onClick={onOpen}
      data-testid={`hammerspoon-open-panel-${server.id}`}
      aria-label="Open Hammerspoon bridge panel"
    >
      <HealthBadge health={health} className="cursor-pointer" />
    </Button>
  )
}

