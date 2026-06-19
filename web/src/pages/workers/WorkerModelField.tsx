// WorkerModelField — provider-aware model picker.
//
// The runner stores model_provider as a smaller enum than the UI shows.
// The UI surfaces convenience choices for OpenRouter and OpenCode server
// by pre-filling endpoint_url, then reverse-maps from (provider,
// endpoint_url) when loading an existing worker.
//
// Model ID is a typeahead via native <datalist> — every provider has
// curated suggestions, free text is always allowed for endpoints
// that ship custom models.

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { ModelProvider } from '@/api/workers'
import { listModelProfiles } from '@/api/client'
import { useApi } from '@/hooks/use-api'
import {
  ANTHROPIC_API_MODELS,
  CLAUDE_CLI_MODELS,
  CODEX_CLI_MODELS,
  GEMINI_CLI_MODELS,
  GROK_CLI_MODELS,
  MIMO_CLI_MODELS,
  OPENAI_MODELS,
  OPENROUTER_HIGHLIGHTS,
  PI_CLI_MODELS,
} from '@/lib/model-catalog'

// UIProvider widens the storage enum with the two openai_compat
// pre-canned endpoints we want first-class UI for.
export type UIProvider =
  | 'anthropic'
  | 'openai'
  | 'openrouter'
  | 'opencode'
  | 'openai_compat'
  | 'claude_cli'
  | 'opencode_cli'
  | 'grok_cli'
  | 'mimo_cli'
  | 'gemini_cli'
  | 'codex_cli'
  | 'pi_cli'

const OPENROUTER_ENDPOINT = 'https://openrouter.ai/api/v1'
const OPENCODE_ENDPOINT = 'http://127.0.0.1:4096'

interface ProviderMeta {
  storage: ModelProvider
  label: string
  description: string
  defaultEndpoint?: string
  defaultModel?: string
  modelSuggestions: string[]
  modelPlaceholder: string
  endpointEditable: boolean
}

const PROVIDERS: Record<UIProvider, ProviderMeta> = {
  anthropic: {
    storage: 'anthropic',
    label: 'Anthropic (API key)',
    description: 'Direct Anthropic Messages API. Stable, expensive, top-quality.',
    defaultModel: 'claude-opus-4-7',
    modelSuggestions: ANTHROPIC_API_MODELS,
    modelPlaceholder: 'claude-opus-4-7',
    endpointEditable: false,
  },
  openai: {
    storage: 'openai',
    label: 'OpenAI',
    description: 'Direct OpenAI Chat Completions API.',
    defaultModel: 'gpt-4o',
    modelSuggestions: OPENAI_MODELS,
    modelPlaceholder: 'gpt-4o',
    endpointEditable: false,
  },
  openrouter: {
    storage: 'openai_compat',
    label: 'OpenRouter',
    description: 'One key, hundreds of models. OpenAI-compat over openrouter.ai.',
    defaultEndpoint: OPENROUTER_ENDPOINT,
    defaultModel: 'anthropic/claude-opus-4-7',
    modelSuggestions: OPENROUTER_HIGHLIGHTS,
    modelPlaceholder: 'anthropic/claude-opus-4-7',
    endpointEditable: true,
  },
  opencode: {
    storage: 'opencode_cli',
    label: 'OpenCode server (attached CLI)',
    description: 'Uses `opencode run --attach` against a locally-running server. Best path for concurrent MiniMax/GLM workers.',
    defaultEndpoint: OPENCODE_ENDPOINT,
    defaultModel: 'minimax/MiniMax-M3',
    modelSuggestions: [], // populated live from /api/v1/opencode/models — see useOpenCodeModels
    modelPlaceholder: 'pick from your OpenCode catalog',
    endpointEditable: true,
  },
  openai_compat: {
    storage: 'openai_compat',
    label: 'Custom OpenAI-compatible',
    description: 'Bring your own. Minimax, Ollama, Together, Groq, vLLM, etc.',
    defaultModel: '',
    modelSuggestions: [],
    modelPlaceholder: 'whatever your endpoint exposes',
    endpointEditable: true,
  },
  claude_cli: {
    storage: 'claude_cli',
    label: 'Claude CLI (subscription / Agent SDK)',
    description: 'Spawns `claude -p` as a subprocess. Uses your Claude subscription.',
    defaultModel: 'sonnet',
    modelSuggestions: CLAUDE_CLI_MODELS,
    modelPlaceholder: 'sonnet',
    endpointEditable: true, // binary path
  },
  opencode_cli: {
    storage: 'opencode_cli',
    label: 'OpenCode CLI (subscription, hundreds of models)',
    description:
      'Spawns `opencode run` directly. For parallel workers, prefer OpenCode server mode so the CLI attaches to one long-lived backend.',
    defaultModel: 'minimax/MiniMax-M3',
    modelSuggestions: [],
    modelPlaceholder: 'provider/model (e.g. minimax/MiniMax-M3)',
    endpointEditable: true, // binary path
  },
  grok_cli: {
    storage: 'grok_cli',
    label: 'xAI Grok CLI',
    description: 'Spawns `grok` in headless JSON mode. Uses your Grok CLI login or XAI_API_KEY.',
    defaultModel: 'grok-build',
    modelSuggestions: GROK_CLI_MODELS,
    modelPlaceholder: 'grok-build',
    endpointEditable: true, // binary path
  },
  mimo_cli: {
    storage: 'mimo_cli',
    label: 'Xiaomi MiMo CLI',
    description: 'Spawns native `mimo --never-ask-questions run --pure --format json --dangerously-skip-permissions`. Uses your MiMo CLI login.',
    defaultModel: 'xiaomi/mimo-v2.5',
    modelSuggestions: MIMO_CLI_MODELS,
    modelPlaceholder: 'xiaomi/mimo-v2.5',
    endpointEditable: true, // binary path or attach URL
  },
  gemini_cli: {
    storage: 'gemini_cli',
    label: 'Google Gemini CLI',
    description: 'Spawns `gemini` in non-interactive JSON mode. Uses GEMINI_API_KEY or your local Gemini CLI auth.',
    defaultModel: 'gemini-2.5-pro',
    modelSuggestions: GEMINI_CLI_MODELS,
    modelPlaceholder: 'gemini-2.5-pro',
    endpointEditable: true, // binary path
  },
  codex_cli: {
    storage: 'codex_cli',
    label: 'OpenAI Codex CLI',
    description: 'Spawns `codex` in non-interactive mode. Requires OPENAI_API_KEY in the daemon environment.',
    defaultModel: 'o3',
    modelSuggestions: CODEX_CLI_MODELS,
    modelPlaceholder: 'o3',
    endpointEditable: true, // binary path
  },
  pi_cli: {
    storage: 'pi_cli',
    label: 'Pi CLI',
    description: 'Spawns the Pi coding harness. The model id must match an alias in ~/.pi/agent/models.json.',
    defaultModel: 'qwen-local',
    modelSuggestions: PI_CLI_MODELS,
    modelPlaceholder: 'qwen-local',
    endpointEditable: true, // binary path
  },
}

// resolveUIProvider reverse-maps (storage, endpoint) onto the UI's wider
// enum so a worker saved as openai_compat+openrouter.ai still shows as
// OpenRouter when reloaded.
export function resolveUIProvider(provider: ModelProvider, endpointURL: string): UIProvider {
  if (provider === 'anthropic') return 'anthropic'
  if (provider === 'openai') return 'openai'
  if (provider === 'claude_cli') return 'claude_cli'
  if (provider === 'opencode_cli' && /^https?:\/\//.test(endpointURL)) return 'opencode'
  if (provider === 'opencode_cli') return 'opencode_cli'
  if (provider === 'grok_cli') return 'grok_cli'
  if (provider === 'mimo_cli') return 'mimo_cli'
  if (provider === 'gemini_cli') return 'gemini_cli'
  if (provider === 'codex_cli') return 'codex_cli'
  if (provider === 'pi_cli') return 'pi_cli'
  if (provider === 'openai_compat') {
    if (endpointURL.includes('openrouter.ai')) return 'openrouter'
    return 'openai_compat'
  }
  return 'openai_compat'
}

interface Props {
  provider: ModelProvider
  modelID: string
  endpointURL: string
  // When a profile is picked, secretScopeID is included so the parent
  // form can set its sibling secret-scope dropdown in one shot. Manual
  // provider / model / endpoint edits omit secretScopeID — the parent
  // leaves its current scope alone in that case.
  onChange: (next: {
    provider: ModelProvider
    modelID: string
    endpointURL: string
    secretScopeID?: string
  }) => void
}

export function WorkerModelField({ provider, modelID, endpointURL, onChange }: Props) {
  const uiProvider = resolveUIProvider(provider, endpointURL)
  const baseMeta = PROVIDERS[uiProvider]
  const liveOpencodeModels = useOpenCodeModels(uiProvider === 'opencode')
  const profilesFetcher = useCallback(() => listModelProfiles(), [])
  const { data: profiles } = useApi(profilesFetcher)
  const [profileId, setProfileId] = useState<string>('')

  // Model suggestions: profile.known_models wins over live OpenCode list
  // wins over the static catalogue. Free-text typing is always allowed.
  const selectedProfile = (profiles ?? []).find((p) => p.id === profileId) || null
  const profileSuggestions = selectedProfile?.known_models ?? []
  const meta =
    profileSuggestions.length > 0
      ? { ...baseMeta, modelSuggestions: profileSuggestions }
      : uiProvider === 'opencode' && liveOpencodeModels.length > 0
        ? { ...baseMeta, modelSuggestions: liveOpencodeModels }
        : baseMeta

  function handleProviderChange(next: UIProvider) {
    const nextMeta = PROVIDERS[next]
    setProfileId('') // a manual provider switch clears the profile prefill
    onChange({
      provider: nextMeta.storage,
      modelID: nextMeta.defaultModel ?? '',
      endpointURL: nextMeta.defaultEndpoint ?? '',
    })
  }

  function handleProfilePick(id: string) {
    setProfileId(id)
    if (!id) return
    const p = (profiles ?? []).find((x) => x.id === id)
    if (!p) return
    const firstModel = p.known_models[0] ?? ''
    onChange({
      provider: p.provider,
      modelID: firstModel,
      endpointURL: p.endpoint_url,
      secretScopeID: p.secret_scope_id,
    })
  }

  return (
    <div className="space-y-3">
      {(profiles ?? []).length > 0 && (
        <Field label="Saved profile" htmlFor="w-profile">
          <Select value={profileId} onValueChange={handleProfilePick}>
            <SelectTrigger className="w-full" data-testid="worker-profile">
              <SelectValue placeholder="Pick a saved provider..." />
            </SelectTrigger>
            <SelectContent>
              {(profiles ?? []).map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.name}
                  <span className="ml-2 text-xs text-muted-foreground">
                    {p.provider}
                    {p.known_models.length > 0 ? ` · ${p.known_models.length} models` : ''}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-[10px] text-muted-foreground/70">
            Sets up the fields below.{' '}
            <Link to="/model-providers" className="underline hover:text-foreground">
              Manage providers
            </Link>
          </p>
        </Field>
      )}
      {(profiles ?? []).length === 0 && (
        <p className="rounded-md border border-dashed border-border px-3 py-2 text-[11px] text-muted-foreground">
          Tip: set up a provider once on{' '}
          <Link to="/model-providers" className="underline hover:text-foreground">
            Model providers
          </Link>{' '}
          and reuse it across workers.
        </p>
      )}
      <Field label="Provider" required>
        <Select value={uiProvider} onValueChange={(v) => handleProviderChange(v as UIProvider)}>
          <SelectTrigger className="w-full" data-testid="worker-provider">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(PROVIDERS) as UIProvider[]).map((id) => (
              <SelectItem key={id} value={id}>
                {PROVIDERS[id].label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-[10px] text-muted-foreground/70">{meta.description}</p>
      </Field>

      <Field label="Model" required htmlFor="w-model-id">
        <Input
          id="w-model-id"
          value={modelID}
          onChange={(e) => onChange({ provider, modelID: e.target.value, endpointURL })}
          placeholder={meta.modelPlaceholder}
          list={meta.modelSuggestions.length > 0 ? `models-${uiProvider}` : undefined}
          autoComplete="off"
          data-testid="worker-model-id"
        />
        {meta.modelSuggestions.length > 0 && (
          <datalist id={`models-${uiProvider}`}>
            {meta.modelSuggestions.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
        )}
        {meta.modelSuggestions.length > 0 ? (
          <p className="text-[10px] text-muted-foreground/70">
            Start typing or pick from{' '}
            <code className="font-mono">{meta.modelSuggestions.slice(0, 3).join(', ')}</code>
            {meta.modelSuggestions.length > 3 ? ', …' : ''}.
          </p>
        ) : (
          <p className="text-[10px] text-muted-foreground/70">
            Free-text — whatever model id your endpoint expects.
          </p>
        )}
      </Field>

      {meta.endpointEditable && (
        <Field
          label={endpointLabel(uiProvider)}
          required={meta.storage === 'openai_compat' && uiProvider !== 'opencode' && uiProvider !== 'openrouter'}
          htmlFor="w-endpoint"
        >
          <Input
            id="w-endpoint"
            value={endpointURL}
            onChange={(e) =>
              onChange({ provider, modelID, endpointURL: e.target.value })
            }
            placeholder={endpointPlaceholder(uiProvider)}
          />
          {uiProvider === 'opencode' && (
            <p className="text-[10px] text-muted-foreground/70">
              MCPlexer starts the managed OpenCode server when needed; workers attach through the CLI at this URL.
            </p>
          )}
          {uiProvider === 'openrouter' && (
            <p className="text-[10px] text-muted-foreground/70">
              Default OpenRouter API — leave as-is unless you proxy through your own host.
            </p>
          )}
          {uiProvider === 'claude_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">claude</code> is on <code>$PATH</code>.
            </p>
          )}
          {uiProvider === 'opencode_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">opencode</code> is on <code>$PATH</code>.
            </p>
          )}
          {uiProvider === 'grok_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">grok</code> is on <code>$PATH</code>.
            </p>
          )}
          {uiProvider === 'mimo_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">mimo</code> is on <code>$PATH</code>. HTTP URLs are passed to <code className="font-mono">mimo run --attach</code>.
            </p>
          )}
          {uiProvider === 'gemini_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">gemini</code> is on <code>$PATH</code>.
            </p>
          )}
          {uiProvider === 'codex_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">codex</code> is on <code>$PATH</code>. Requires <code className="font-mono">OPENAI_API_KEY</code> in the daemon environment.
            </p>
          )}
          {uiProvider === 'pi_cli' && (
            <p className="text-[10px] text-muted-foreground/70">
              Leave blank to use whatever <code className="font-mono">pi</code> is on <code>$PATH</code>. Models are resolved from <code className="font-mono">~/.pi/agent/models.json</code>.
            </p>
          )}
        </Field>
      )}
    </div>
  )
}

interface FieldProps {
  label: string
  required?: boolean
  htmlFor?: string
  children: React.ReactNode
}

function Field({ label, required, htmlFor, children }: FieldProps) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={htmlFor} className="text-xs">
        {label}
        {required && <span className="ml-1 text-destructive">*</span>}
      </Label>
      {children}
    </div>
  )
}

function endpointPlaceholder(provider: UIProvider): string {
  switch (provider) {
    case 'openrouter':
      return OPENROUTER_ENDPOINT
    case 'opencode':
      return OPENCODE_ENDPOINT
    case 'claude_cli':
      return 'claude  (blank = use $PATH)'
    case 'grok_cli':
      return 'grok  (blank = use $PATH)'
    case 'mimo_cli':
      return 'mimo  (blank = use $PATH), or http://127.0.0.1:4096'
    case 'gemini_cli':
      return 'gemini  (blank = use $PATH)'
    case 'codex_cli':
      return 'codex  (blank = use $PATH)'
    case 'pi_cli':
      return 'pi  (blank = use $PATH)'
    case 'opencode_cli':
      return 'opencode  (blank = use $PATH)'
    default:
      return 'https://my-endpoint/v1'
  }
}

function endpointLabel(provider: UIProvider): string {
  switch (provider) {
    case 'mimo_cli':
      return 'Binary path / attach URL'
    case 'claude_cli':
    case 'opencode_cli':
    case 'grok_cli':
    case 'gemini_cli':
    case 'codex_cli':
    case 'pi_cli':
      return 'Binary path'
    default:
      return 'Endpoint URL'
  }
}

// useOpenCodeModels lazily fetches the live OpenCode model list when the
// user selects the OpenCode provider. The endpoint is provided by the
// built-in OpenCode subprocess manager (Layer 3); when it's unavailable
// (404 / network error / older daemon) the hook returns [] and the
// component falls back to free-text input. Cached at the daemon for 5
// minutes so we don't burn the binary on every render.
function useOpenCodeModels(enabled: boolean): string[] {
  const [models, setModels] = useState<string[]>([])

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    fetch('/api/v1/opencode/models')
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled || !data) return
        const list = Array.isArray(data?.models) ? data.models : []
        if (list.length > 0) setModels(list)
      })
      .catch(() => {
        // Endpoint not available (Layer 3 not deployed, or opencode not
        // installed). Static fallback is empty, free-text remains the
        // way in.
      })
    return () => {
      cancelled = true
    }
  }, [enabled])

  return models
}
