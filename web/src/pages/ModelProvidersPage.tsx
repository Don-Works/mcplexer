// ModelProvidersPage — manage worker model profiles + the built-in
// OpenCode subprocess. The profile concept replaces inline
// (provider, model, endpoint, secret_scope_id) configuration on every
// worker with a small reusable record. Workers reference profiles by
// id; the model is then a per-worker pick from the profile's catalogue.
//
// The "EASY PEASY" flow:
//   1. Set up a provider once: name, type, endpoint, API key.
//   2. Create a worker and pick (profile, model). Done.

import { useCallback, useEffect, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  CheckCircle2,
  Download,
  Loader2,
  Pencil,
  Play,
  Plus,
  Square,
  Terminal,
  Trash2,
  XCircle,
  Zap,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  createAuthScope,
  createModelProfile,
  deleteModelProfile,
  getOpenCodeStatus,
  listModelProfiles,
  putSecret,
  startOpenCode,
  stopOpenCode,
  testModelProfile,
  updateModelProfile,
} from '@/api/client'
import type {
  ModelProfile,
  ModelProfileInput,
  OpenCodeStatus,
} from '@/api/client'
import { useApi } from '@/hooks/use-api'
import { defaultKnownModelsFor } from '@/lib/model-catalog'
import type { ModelProvider } from '@/lib/model-catalog'

const PROVIDER_OPTIONS: Array<{
  value: ModelProvider
  label: string
  hint: string
  needsEndpoint: boolean
  endpointRequired?: boolean
  endpointHint?: string
  needsKey: boolean
}> = [
  {
    value: 'anthropic',
    label: 'Anthropic',
    hint: 'Direct Messages API. Your API key, billed per-token.',
    needsEndpoint: false,
    needsKey: true,
  },
  {
    value: 'openai',
    label: 'OpenAI',
    hint: 'Direct Chat Completions API.',
    needsEndpoint: false,
    needsKey: true,
  },
  {
    value: 'openai_compat',
    label: 'OpenAI-compatible',
    hint: 'OpenRouter, Together, Groq, vLLM, Ollama. Pick by endpoint.',
    needsEndpoint: true,
    endpointHint: 'https://openrouter.ai/api/v1',
    needsKey: true,
  },
  {
    value: 'claude_cli',
    label: 'Claude CLI',
    hint: 'Subprocess. Uses your Claude subscription, not an API key.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'claude  (blank = use $PATH)',
    needsKey: false,
  },
  {
    value: 'opencode_cli',
    label: 'OpenCode CLI',
    hint: 'Attached CLI. Prefer the local OpenCode server for concurrent Minimax, GLM/Z.ai, OpenRouter, and local runs.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'http://127.0.0.1:4096',
    needsKey: false,
  },
  {
    value: 'grok_cli',
    label: 'xAI Grok CLI',
    hint: 'Subprocess. Uses your Grok CLI login or XAI_API_KEY.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'grok  (blank = use $PATH)',
    needsKey: false,
  },
  {
    value: 'mimo_cli',
    label: 'Xiaomi MiMo CLI',
    hint: 'Subprocess. Uses your native MiMo CLI login.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'mimo  (blank = use $PATH), or http://127.0.0.1:4096',
    needsKey: false,
  },
  {
    value: 'gemini_cli',
    label: 'Google Gemini CLI',
    hint: 'Subprocess. Uses GEMINI_API_KEY or your local Gemini CLI auth.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'gemini  (blank = use $PATH)',
    needsKey: false,
  },
  {
    value: 'codex_cli',
    label: 'OpenAI Codex CLI',
    hint: 'Subprocess. Needs OPENAI_API_KEY set in environment.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'codex  (blank = use $PATH)',
    needsKey: false,
  },
  {
    value: 'pi_cli',
    label: 'Pi CLI',
    hint: 'Subprocess. Uses the host Pi coding harness; model ids resolve from ~/.pi/agent/models.json.',
    needsEndpoint: true,
    endpointRequired: false,
    endpointHint: 'pi  (blank = use $PATH)',
    needsKey: false,
  },
]

export function ModelProvidersPage() {
  const fetcher = useCallback(() => listModelProfiles(), [])
  const { data: profiles, refetch, loading } = useApi(fetcher)

  const [editing, setEditing] = useState<ModelProfile | null>(null)
  const [creating, setCreating] = useState(false)

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-bold">Model Providers</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Set up your model provider once, then pick a model when creating a worker.
          </p>
        </div>
        <Button onClick={() => setCreating(true)} data-testid="model-profile-new">
          <Plus className="mr-1.5 h-4 w-4" />
          New provider
        </Button>
      </div>

      <OpenCodeRuntimeCard onProfileChanged={() => refetch()} />

      <ProviderCaveatsCard />

      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
            Providers
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {loading && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-3 w-3 animate-spin" />
              Loading providers...
            </div>
          )}
          {!loading && (profiles ?? []).length === 0 && (
            <div className="rounded-md border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
              No providers yet. Click <strong>New provider</strong> to add one.
            </div>
          )}
          {(profiles ?? []).map((p) => (
            <ProfileRow
              key={p.id}
              profile={p}
              onEdit={() => setEditing(p)}
              onDeleted={() => refetch()}
            />
          ))}
        </CardContent>
      </Card>

      {creating && (
        <ProfileDialog
          mode="create"
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false)
            refetch()
          }}
        />
      )}
      {editing && (
        <ProfileDialog
          mode="edit"
          profile={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            refetch()
          }}
        />
      )}
    </div>
  )
}

function ProfileRow({
  profile,
  onEdit,
  onDeleted,
}: {
  profile: ModelProfile
  onEdit: () => void
  onDeleted: () => void
}) {
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; ms: number; err?: string } | null>(
    null,
  )
  const providerMeta = PROVIDER_OPTIONS.find((o) => o.value === profile.provider)

  async function handleTest() {
    setTesting(true)
    setTestResult(null)
    try {
      const r = await testModelProfile(profile.id)
      setTestResult({ ok: r.ok, ms: r.latency_ms, err: r.error })
    } catch (err) {
      setTestResult({ ok: false, ms: 0, err: err instanceof Error ? err.message : String(err) })
    } finally {
      setTesting(false)
    }
  }

  async function handleDelete() {
    if (!confirm(`Delete provider "${profile.name}"? Workers using it will fall back to inline config.`)) return
    try {
      await deleteModelProfile(profile.id)
      toast.success(`Deleted "${profile.name}"`)
      onDeleted()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    }
  }

  return (
    <div className="flex items-center justify-between gap-3 rounded-md border border-border/40 bg-background/40 px-3 py-2.5">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <Badge variant="outline" className="font-mono uppercase text-[10px]">
          {providerMeta?.label ?? profile.provider}
        </Badge>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{profile.name}</span>
            {profile.builtin && (
              <Badge variant="outline" className="text-[10px] uppercase tracking-wider">
                built-in
              </Badge>
            )}
          </div>
          <div className="text-xs text-muted-foreground">
            {profile.known_models.length} model{profile.known_models.length === 1 ? '' : 's'}
            {profile.endpoint_url && (
              <>
                {' · '}
                <code className="font-mono">{profile.endpoint_url}</code>
              </>
            )}
          </div>
        </div>
      </div>
      <div className="flex items-center gap-1">
        {testResult && (
          <span
            className={`flex items-center gap-1 text-xs font-mono ${testResult.ok ? 'text-emerald-400' : 'text-destructive'}`}
            title={testResult.err}
          >
            {testResult.ok ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
            {testResult.ok ? `${testResult.ms}ms` : 'failed'}
          </span>
        )}
        <Button variant="ghost" size="sm" onClick={handleTest} disabled={testing} aria-label="Test provider">
          {testing ? <Loader2 className="h-3 w-3 animate-spin" /> : <Zap className="h-3 w-3" />}
          <span className="ml-1 text-xs">Test</span>
        </Button>
        <Button variant="ghost" size="sm" onClick={onEdit} disabled={profile.builtin} aria-label="Edit provider">
          <Pencil className="h-3 w-3" />
        </Button>
        <Button variant="ghost" size="sm" onClick={handleDelete} disabled={profile.builtin} aria-label="Delete provider">
          <Trash2 className="h-3 w-3" />
        </Button>
      </div>
    </div>
  )
}

function ProfileDialog({
  mode,
  profile,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit'
  profile?: ModelProfile
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(profile?.name ?? '')
  const [provider, setProvider] = useState<ModelProvider>(profile?.provider ?? 'anthropic')
  const [endpoint, setEndpoint] = useState(profile?.endpoint_url ?? '')
  const [apiKey, setApiKey] = useState('')
  const [saving, setSaving] = useState(false)
  const meta = PROVIDER_OPTIONS.find((o) => o.value === provider)!

  async function handleSave() {
    if (!name.trim()) {
      toast.error('Name is required')
      return
    }
    if ((meta.endpointRequired ?? meta.needsEndpoint) && !endpoint.trim()) {
      toast.error('Endpoint URL or binary path is required for this provider')
      return
    }
    setSaving(true)
    try {
      let secretScopeId = profile?.secret_scope_id
      // Create an auth scope on first key, or update if a fresh key was typed.
      if (meta.needsKey && apiKey.trim()) {
        if (!secretScopeId) {
          const scope = await createAuthScope({
            name: `model-key-${slug(name)}`,
            display_name: '',
            type: 'env',
            oauth_provider_id: '',
            redaction_hints: [],
          })
          secretScopeId = scope.id
        }
        await putSecret(secretScopeId!, 'api_key', apiKey.trim())
      }
      const input: ModelProfileInput = {
        name: name.trim(),
        provider,
        endpoint_url: endpoint.trim(),
        secret_scope_id: secretScopeId,
        known_models: profile?.known_models ?? defaultKnownModelsFor(provider),
      }
      if (mode === 'create') {
        await createModelProfile(input)
        toast.success(`Created "${name}"`)
      } else {
        await updateModelProfile(profile!.id, input)
        toast.success(`Updated "${name}"`)
      }
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{mode === 'create' ? 'New provider' : `Edit ${profile?.name}`}</DialogTitle>
          <DialogDescription>
            Set up a model provider once, then reference it from any worker.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">Name</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My Anthropic key"
              autoFocus
            />
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Provider</Label>
            <div className="grid grid-cols-2 gap-2">
              {PROVIDER_OPTIONS.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  onClick={() => setProvider(opt.value)}
                  className={`rounded-md border px-3 py-2 text-left transition-colors ${
                    provider === opt.value
                      ? 'border-primary/60 bg-primary/5'
                      : 'border-border hover:border-border/80'
                  }`}
                >
                  <div className="text-xs font-semibold">{opt.label}</div>
                  <div className="mt-0.5 text-[10px] text-muted-foreground leading-snug">
                    {opt.hint}
                  </div>
                </button>
              ))}
            </div>
          </div>
          {meta.needsEndpoint && (
            <div className="space-y-1.5">
              <Label className="text-xs">Endpoint</Label>
              <Input
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                placeholder={meta.endpointHint}
              />
            </div>
          )}
          {meta.needsKey && (
            <div className="space-y-1.5">
              <Label className="text-xs">
                API key {mode === 'edit' && '(leave blank to keep existing)'}
              </Label>
              <Input
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder="sk-…"
                autoComplete="off"
              />
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : null}
            {mode === 'create' ? 'Create' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// OpenCodeRuntimeCard — surface for the built-in OpenCode subprocess.
function OpenCodeRuntimeCard({ onProfileChanged }: { onProfileChanged: () => void }) {
  const fetcher = useCallback(() => getOpenCodeStatus(), [])
  const { data, refetch } = useApi(fetcher)
  const [busy, setBusy] = useState(false)

  // Poll status every 5s when subprocess is in-flight.
  useEffect(() => {
    if (!data?.running) return
    const t = setInterval(() => refetch(), 5000)
    return () => clearInterval(t)
  }, [data?.running, refetch])

  async function handleStart() {
    setBusy(true)
    try {
      await startOpenCode()
      await refetch()
      onProfileChanged()
      toast.success('OpenCode running')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Start failed')
    } finally {
      setBusy(false)
    }
  }

  async function handleStop() {
    setBusy(true)
    try {
      await stopOpenCode()
      await refetch()
      toast.success('OpenCode stopped')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Stop failed')
    } finally {
      setBusy(false)
    }
  }

  const status = data
  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="flex items-center gap-2 text-sm font-medium uppercase tracking-wider text-muted-foreground">
            <Terminal className="h-3 w-3" />
            OpenCode runtime
          </CardTitle>
          <OpenCodeStatusPill status={status} />
        </div>
      </CardHeader>
      <CardContent>
        {!status && (
          <p className="text-sm text-muted-foreground">Checking environment...</p>
        )}
        {status && !status.installed && (
          <div className="space-y-3 text-sm">
            <p className="text-muted-foreground">
              MCPlexer couldn't find the <code className="font-mono">opencode</code> binary. The
              macOS launchd daemon runs with a stripped-down <code>$PATH</code>, so even an
              installed opencode is invisible unless it's in one of the probed locations.
            </p>
            <div className="rounded-md border border-border/40 bg-background/40 p-3 text-xs">
              <p className="mb-1.5 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
                Probed automatically
              </p>
              <ul className="space-y-0.5 font-mono text-[11px] text-muted-foreground">
                <li>$PATH lookup</li>
                <li>~/.opencode/bin/opencode</li>
                <li>~/.local/bin/opencode</li>
                <li>~/bin/opencode</li>
                <li>/opt/homebrew/bin/opencode</li>
                <li>/usr/local/bin/opencode</li>
              </ul>
            </div>
            <div className="rounded-md border border-border/40 bg-background/40 p-3 text-xs leading-relaxed">
              <p className="font-medium text-foreground">Already have opencode installed?</p>
              <p className="mt-1 text-muted-foreground">
                Point the daemon at the binary by adding{' '}
                <code className="font-mono">MCPLEXER_OPENCODE_PATH</code> to{' '}
                <code className="font-mono">
                  ~/Library/LaunchAgents/com.mcplexer.daemon.plist
                </code>{' '}
                under <code className="font-mono">EnvironmentVariables</code>, then run:
              </p>
              <pre className="mt-2 overflow-auto rounded bg-background p-2 font-mono text-[10px] text-accent-foreground">
                {`launchctl kickstart -k gui/$UID/com.mcplexer.daemon`}
              </pre>
            </div>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => refetch()}
              >
                Re-check
              </Button>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      window.open(
                        'https://github.com/sst/opencode',
                        '_blank',
                        'noopener,noreferrer',
                      )
                    }
                  >
                    <Download className="mr-1.5 h-3 w-3" />
                    Install OpenCode
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Opens the OpenCode install docs</TooltipContent>
              </Tooltip>
            </div>
          </div>
        )}
        {status && status.installed && (
          <div className="space-y-3">
            <dl className="grid grid-cols-2 gap-x-6 gap-y-1.5 text-xs">
              <div>
                <dt className="text-muted-foreground">Binary</dt>
                <dd className="font-mono text-foreground break-all">
                  {status.binary_path || 'opencode (on $PATH)'}
                </dd>
              </div>
              {status.version && (
                <div>
                  <dt className="text-muted-foreground">Version</dt>
                  <dd className="font-mono text-foreground">{status.version}</dd>
                </div>
              )}
              {status.port && (
                <div>
                  <dt className="text-muted-foreground">Endpoint</dt>
                  <dd className="font-mono text-foreground">
                    http://127.0.0.1:{status.port}
                  </dd>
                </div>
              )}
              {status.last_error && (
                <div className="col-span-2">
                  <dt className="text-destructive text-muted-foreground">Last error</dt>
                  <dd className="font-mono text-destructive">{status.last_error}</dd>
                </div>
              )}
            </dl>
            <div className="flex items-center gap-2">
              {status.running ? (
                <Button variant="outline" size="sm" onClick={handleStop} disabled={busy}>
                  <Square className="mr-1.5 h-3 w-3" />
                  Stop
                </Button>
              ) : (
                <Button size="sm" onClick={handleStart} disabled={busy}>
                  {busy ? (
                    <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                  ) : (
                    <Play className="mr-1.5 h-3 w-3" />
                  )}
                  Start server
                </Button>
              )}
              <span className="text-[10px] text-muted-foreground">
                opencode_cli model profiles should use this endpoint so workers attach through one server.
              </span>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function OpenCodeStatusPill({ status }: { status: OpenCodeStatus | null }) {
  if (!status)
    return (
      <Badge variant="outline" className="text-[10px] uppercase">
        checking
      </Badge>
    )
  if (!status.installed)
    return (
      <Badge variant="outline" className="text-[10px] uppercase text-muted-foreground">
        not installed
      </Badge>
    )
  if (status.running)
    return (
      <Badge
        variant="outline"
        className="border-emerald-500/40 text-emerald-400 text-[10px] uppercase"
      >
        running
      </Badge>
    )
  return (
    <Badge variant="outline" className="text-[10px] uppercase">
      stopped
    </Badge>
  )
}

function slug(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40) || 'profile'
}

// ProviderCaveatsCard surfaces provider-specific quirks operators should
// know about. Rendered as a collapsible info card on the Model Providers
// page so users discover caveats before hitting them in production.
function ProviderCaveatsCard() {
  const [open, setOpen] = useState(false)
  return (
    <Card>
      <CardHeader>
        <button
          type="button"
          onClick={() => setOpen(!open)}
          className="flex w-full items-center justify-between text-left"
        >
          <CardTitle className="flex items-center gap-2 text-sm font-medium uppercase tracking-wider text-muted-foreground">
            <Zap className="h-3 w-3" />
            Provider caveats
          </CardTitle>
          <span className="text-xs text-muted-foreground">{open ? 'Hide' : 'Show'}</span>
        </button>
      </CardHeader>
      {open && (
        <CardContent>
          <div className="space-y-3 text-xs leading-relaxed">
            <div className="rounded-md border border-border/40 bg-background/40 p-3">
              <p className="font-semibold text-foreground">grok_cli</p>
              <p className="mt-1 text-muted-foreground">
                Headless JSON mode may omit usage/cost fields. When absent, mcplexer records token
                counts and cost as zero rather than inventing values. This means delegation ROI
                panels will show $0 for Grok runs even when tokens were consumed.
              </p>
            </div>
            <div className="rounded-md border border-border/40 bg-background/40 p-3">
              <p className="font-semibold text-foreground">mimo_cli</p>
              <p className="mt-1 text-muted-foreground">
                For parallel workers, prefer a local OpenCode server URL (e.g.{' '}
                <code className="font-mono">http://127.0.0.1:4096</code>) so concurrent runs attach
                through one long-lived backend instead of each spawning a separate process. HTTP
                URLs are passed to <code className="font-mono">mimo run --attach</code>.
              </p>
            </div>
            <div className="rounded-md border border-border/40 bg-background/40 p-3">
              <p className="font-semibold text-foreground">codex_cli</p>
              <p className="mt-1 text-muted-foreground">
                Requires <code className="font-mono">OPENAI_API_KEY</code> to be set in the
                daemon's environment. The key is read by the <code className="font-mono">codex</code>{' '}
                binary itself — mcplexer never stores or transmits it. Ensure the launchd plist or
                systemd unit exports the variable.
              </p>
            </div>
            <div className="rounded-md border border-border/40 bg-background/40 p-3">
              <p className="font-semibold text-foreground">pi_cli</p>
              <p className="mt-1 text-muted-foreground">
                Pi resolves model ids from <code className="font-mono">~/.pi/agent/models.json</code>.
                Treat local aliases as exploratory until they have parent review scores; delegation
                capacity ranking applies an extra penalty to unreviewed Pi routes.
              </p>
            </div>
            <div className="rounded-md border border-border/40 bg-background/40 p-3">
              <p className="font-semibold text-foreground">opencode_cli</p>
              <p className="mt-1 text-muted-foreground">
                Requires a running OpenCode server for{' '}
                <code className="font-mono">--attach</code> mode. The managed server (started from
                the OpenCode runtime card above) auto-restarts on crash, but mid-run session errors
                are retried automatically by the adapter. For parallel workers, always use server
                mode — direct CLI invocations contend over the local session DB.
              </p>
            </div>
          </div>
        </CardContent>
      )}
    </Card>
  )
}
