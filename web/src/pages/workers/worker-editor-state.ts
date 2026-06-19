// Editor state for WorkerEditorPage. Pulled out of the component file
// so the page itself stays under the 300-line budget. The shape is
// strictly a UI projection of CreateWorkerInput — every field is a
// string here even when the API takes a typed enum, because forms
// breathe text.

import type {
  ConcurrencyPolicy,
  CreateWorkerInput,
  ExecMode,
  ModelProvider,
  SkillRef,
  UpdateWorkerInput,
  Worker,
  WorkerWorkspaceAccess,
} from '@/api/workers'

// OutputChannelType enumerates every channel kind the runner knows
// about. Keep this in lockstep with internal/workers/runner/output.go's
// dispatchChannel switch — the dropdown in the editor only offers
// types the backend can route.
export type OutputChannelType =
  | 'mesh'
  | 'file'
  | 'webhook'
  | 'slack_webhook'
  | 'clickup_task'
  | 'github_issue'

export interface OutputChannel {
  type: OutputChannelType
  // mesh-only
  priority?: 'low' | 'normal' | 'high' | 'critical'
  priority_on_fail?: 'low' | 'normal' | 'high' | 'critical'
  tags?: string
  notify_user?: boolean
  reply_to_trigger?: boolean
  to_peer?: string
  broadcast_peers?: boolean
  // file-only
  path?: string
  mode?: 'append' | 'overwrite'
  // webhook / slack_webhook
  url?: string
  headers?: Record<string, string>
  include_metadata?: boolean
  // slack-only
  channel?: string
  prefix?: string
  // clickup-only
  list_id?: string
  name_prefix?: string
  // github-only
  repo?: string
  title_prefix?: string
  // clickup + github
  secret_scope_id?: string
}

export interface EditorState {
  name: string
  description: string
  provider: ModelProvider
  modelID: string
  endpointURL: string
  secretScopeID: string
  // skillRefs is the canonical multi-skill list. Legacy single-skill
  // workers hydrate into a single-element array; on save we always
  // write the array (and the backend mirrors entry[0] into the legacy
  // columns for downstream consumers still on the old shape).
  skillRefs: SkillRef[]
  promptTemplate: string
  parametersJSON: string
  scheduleSpec: string
  toolAllowlistJSON: string
  outputChannels: OutputChannel[]
  execMode: ExecMode
  concurrencyPolicy: ConcurrencyPolicy
  // memoryScopeID is reserved for the future memory subsystem; the
  // backend persists it but doesn't act on it yet.
  memoryScopeID: string
  enabled: boolean
  workspaceID: string
  workspaceAccess: WorkerWorkspaceAccess[]
  // M1 safety caps. Strings so the form layer can show "" for "default".
  maxInputTokens: string
  maxOutputTokens: string
  maxToolCalls: string
  maxWallClockSeconds: string
  maxMonthlyCostUSD: string
  maxConsecutiveFailures: string
}

export function defaultState(): EditorState {
  return {
    name: '',
    description: '',
    provider: 'anthropic',
    modelID: 'claude-opus-4-7',
    endpointURL: '',
    secretScopeID: '',
    skillRefs: [],
    promptTemplate: '',
    parametersJSON: '{}',
    scheduleSpec: '0 * * * *',
    toolAllowlistJSON: '[]',
    outputChannels: [{ type: 'mesh', priority: 'normal' }],
    execMode: 'propose',
    concurrencyPolicy: 'skip',
    memoryScopeID: '',
    enabled: true,
    workspaceID: '',
    workspaceAccess: [],
    maxInputTokens: '',
    maxOutputTokens: '',
    maxToolCalls: '',
    maxWallClockSeconds: '',
    maxMonthlyCostUSD: '',
    maxConsecutiveFailures: '',
  }
}

export function stateFromWorker(w: Worker): EditorState {
  return {
    name: w.name,
    description: w.description,
    provider: w.model_provider,
    modelID: w.model_id,
    endpointURL: w.model_endpoint_url ?? '',
    secretScopeID: w.secret_scope_id,
    skillRefs: hydrateSkillRefs(w),
    promptTemplate: w.prompt_template,
    parametersJSON: w.parameters_json || '{}',
    scheduleSpec: w.schedule_spec,
    toolAllowlistJSON: w.tool_allowlist_json || '[]',
    outputChannels: parseChannels(w.output_channels_json),
    execMode: w.exec_mode,
    concurrencyPolicy: w.concurrency_policy,
    memoryScopeID: w.memory_scope_id ?? '',
    enabled: w.enabled,
    workspaceID: w.workspace_id,
    workspaceAccess: hydrateWorkspaceAccess(w),
    maxInputTokens: capToString(w.max_input_tokens),
    maxOutputTokens: capToString(w.max_output_tokens),
    maxToolCalls: capToString(w.max_tool_calls),
    maxWallClockSeconds: capToString(w.max_wall_clock_seconds),
    maxMonthlyCostUSD: capToString(w.max_monthly_cost_usd),
    maxConsecutiveFailures: capToString(w.max_consecutive_failures),
  }
}

function hydrateWorkspaceAccess(w: Worker): WorkerWorkspaceAccess[] {
  if (Array.isArray(w.workspace_access) && w.workspace_access.length > 0) {
    return normalizeWorkspaceAccess(w.workspace_id, w.workspace_access)
  }
  return [{ workspace_id: w.workspace_id, access: 'write' }]
}

// hydrateSkillRefs picks the canonical multi-skill list, falling back to
// the legacy (skill_name, skill_version) pair when the new column is
// empty so workers persisted before the M0.7 migration still surface
// their skill in the editor.
function hydrateSkillRefs(w: Worker): SkillRef[] {
  if (Array.isArray(w.skill_refs) && w.skill_refs.length > 0) {
    return w.skill_refs.map((r) => ({ name: r.name, version: r.version ?? '' }))
  }
  if (w.skill_name && w.skill_name.trim() !== '') {
    return [{ name: w.skill_name, version: w.skill_version ?? '' }]
  }
  return []
}

// capToString renders 0 (the "use default / no cap" sentinel) as an
// empty string so the form shows the placeholder rather than a literal
// "0". Non-zero values are stringified verbatim.
function capToString(v: number): string {
  if (!v) return ''
  return String(v)
}

const CHANNEL_TYPES: readonly OutputChannelType[] = [
  'mesh',
  'file',
  'webhook',
  'slack_webhook',
  'clickup_task',
  'github_issue',
]

function isChannelType(v: unknown): v is OutputChannelType {
  return typeof v === 'string' && (CHANNEL_TYPES as readonly string[]).includes(v)
}

function parseChannels(raw: string): OutputChannel[] {
  if (!raw) return [{ type: 'mesh', priority: 'normal' }]
  try {
    const parsed = JSON.parse(raw) as unknown
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return [{ type: 'mesh', priority: 'normal' }]
    }
    return parsed
      .map((c) => coerceChannel(c))
      .filter((c): c is OutputChannel => c !== null)
  } catch {
    return [{ type: 'mesh', priority: 'normal' }]
  }
}

function coerceChannel(raw: unknown): OutputChannel | null {
  if (!raw || typeof raw !== 'object') return null
  const obj = raw as Record<string, unknown>
  if (!isChannelType(obj.type)) return null
  const c: OutputChannel = { type: obj.type }
  if (
    obj.priority === 'low' ||
    obj.priority === 'normal' ||
    obj.priority === 'high' ||
    obj.priority === 'critical'
  ) {
    c.priority = obj.priority
  }
  if (
    obj.priority_on_fail === 'low' ||
    obj.priority_on_fail === 'normal' ||
    obj.priority_on_fail === 'high' ||
    obj.priority_on_fail === 'critical'
  ) {
    c.priority_on_fail = obj.priority_on_fail
  }
  if (typeof obj.tags === 'string') c.tags = obj.tags
  if (typeof obj.notify_user === 'boolean') c.notify_user = obj.notify_user
  if (typeof obj.reply_to_trigger === 'boolean') c.reply_to_trigger = obj.reply_to_trigger
  if (typeof obj.to_peer === 'string') c.to_peer = obj.to_peer
  if (typeof obj.broadcast_peers === 'boolean') c.broadcast_peers = obj.broadcast_peers
  if (typeof obj.path === 'string') c.path = obj.path
  if (obj.mode === 'append' || obj.mode === 'overwrite') c.mode = obj.mode
  if (typeof obj.url === 'string') c.url = obj.url
  if (typeof obj.include_metadata === 'boolean') c.include_metadata = obj.include_metadata
  if (obj.headers && typeof obj.headers === 'object') {
    const headers: Record<string, string> = {}
    for (const [k, v] of Object.entries(obj.headers as Record<string, unknown>)) {
      if (typeof v === 'string') headers[k] = v
    }
    c.headers = headers
  }
  if (typeof obj.channel === 'string') c.channel = obj.channel
  if (typeof obj.prefix === 'string') c.prefix = obj.prefix
  if (typeof obj.list_id === 'string') c.list_id = obj.list_id
  if (typeof obj.name_prefix === 'string') c.name_prefix = obj.name_prefix
  if (typeof obj.repo === 'string') c.repo = obj.repo
  if (typeof obj.title_prefix === 'string') c.title_prefix = obj.title_prefix
  if (typeof obj.secret_scope_id === 'string') c.secret_scope_id = obj.secret_scope_id
  return c
}

// validateState returns null on success or the first error string.
// Light validation only — the backend service does the heavy lifting
// (schedule spec, model provider, JSON shapes).
export function validateState(s: EditorState): string | null {
  if (!s.name.trim()) return 'Name is required'
  if (!s.workspaceID.trim()) return 'Workspace is required'
  const workspaceAccess = normalizeWorkspaceAccess(s.workspaceID, s.workspaceAccess)
  if (workspaceAccess.length === 0) return 'At least one workspace access grant is required'
  const primary = workspaceAccess.find((g) => g.workspace_id === s.workspaceID.trim())
  if (!primary || primary.access !== 'write') return 'Preferred workspace requires write access'
  if (!s.modelID.trim()) return 'Model ID is required'
  if (!s.secretScopeID.trim()) return 'Secret scope is required'
  if (!s.promptTemplate.trim()) return 'Prompt template is required'
  if (!s.scheduleSpec.trim()) return 'Schedule is required'
  if (s.parametersJSON.trim() !== '') {
    try {
      JSON.parse(s.parametersJSON)
    } catch {
      return 'Parameters: invalid JSON'
    }
  }
  if (s.toolAllowlistJSON.trim() !== '') {
    try {
      const parsed = JSON.parse(s.toolAllowlistJSON)
      if (!Array.isArray(parsed)) return 'Tool allowlist must be a JSON array'
    } catch {
      return 'Tool allowlist: invalid JSON'
    }
  }
  const channelErr = validateOutputChannels(s.outputChannels)
  if (channelErr) return channelErr
  const skillErr = validateSkillRefs(s.skillRefs)
  if (skillErr) return skillErr
  for (const [label, raw] of capInputs(s)) {
    if (raw.trim() === '') continue
    const n = Number(raw)
    if (!Number.isFinite(n) || n < 0) {
      return `${label}: must be a non-negative number`
    }
  }
  return null
}

function validateSkillRefs(refs: SkillRef[]): string | null {
  const seen = new Set<string>()
  for (let i = 0; i < refs.length; i++) {
    const r = refs[i]
    if (!r.name.trim()) return `Skill row ${i + 1}: name is required`
    const key = `${r.name}@${r.version ?? ''}`
    if (seen.has(key)) {
      return `Skill "${r.name}" appears twice (with version "${r.version ?? ''}") — remove the duplicate`
    }
    seen.add(key)
  }
  return null
}

function validateOutputChannels(channels: OutputChannel[]): string | null {
  for (const c of channels) {
    if (c.type === 'file' && !c.path?.trim()) {
      return 'File output channel requires a path'
    }
    if (c.type === 'webhook' && !c.url?.trim()) {
      return 'Webhook channel requires a URL'
    }
    if (c.type === 'slack_webhook' && !c.url?.trim()) {
      return 'Slack webhook channel requires a URL'
    }
    if (c.type === 'clickup_task') {
      if (!c.list_id?.trim()) return 'ClickUp channel requires a list ID'
      if (!c.secret_scope_id?.trim()) {
        return 'ClickUp channel requires a secret scope (key=api_key)'
      }
    }
    if (c.type === 'github_issue') {
      if (!c.repo?.trim() || !c.repo.includes('/')) {
        return 'GitHub channel requires repo in owner/name form'
      }
      if (!c.secret_scope_id?.trim()) {
        return 'GitHub channel requires a secret scope (key=api_key)'
      }
    }
  }
  return null
}

function capInputs(s: EditorState): Array<[string, string]> {
  return [
    ['Max input tokens', s.maxInputTokens],
    ['Max output tokens', s.maxOutputTokens],
    ['Max tool calls', s.maxToolCalls],
    ['Max wall-clock seconds', s.maxWallClockSeconds],
    ['Max monthly cost (USD)', s.maxMonthlyCostUSD],
    ['Max consecutive failures', s.maxConsecutiveFailures],
  ]
}

// parseCap converts the form's string field to the API's number field.
// Empty / non-numeric strings collapse to 0 (the "use default / no cap"
// sentinel). Decimals are preserved for cost; everything else floor-rounds.
function parseCap(raw: string, allowDecimal = false): number {
  const n = Number(raw)
  if (!Number.isFinite(n) || n < 0) return 0
  return allowDecimal ? n : Math.floor(n)
}

// normalizeSkillRefs trims whitespace and drops empty rows so the
// API payload never carries a half-filled row. Returns the cleaned
// slice (always non-null; may be empty).
function normalizeSkillRefs(refs: SkillRef[]): SkillRef[] {
  const out: SkillRef[] = []
  for (const r of refs) {
    const name = r.name.trim()
    if (!name) continue
    const version = (r.version ?? '').trim()
    out.push(version ? { name, version } : { name })
  }
  return out
}

export function normalizeWorkspaceAccess(
  preferredWorkspaceID: string,
  grants: WorkerWorkspaceAccess[],
): WorkerWorkspaceAccess[] {
  const out: WorkerWorkspaceAccess[] = []
  const seen = new Map<string, number>()
  for (const g of grants) {
    const workspaceID = g.workspace_id.trim()
    if (!workspaceID) continue
    const access = g.access === 'write' ? 'write' : 'read'
    if (seen.has(workspaceID)) {
      const idx = seen.get(workspaceID)!
      if (access === 'write') out[idx].access = 'write'
      continue
    }
    seen.set(workspaceID, out.length)
    out.push({ workspace_id: workspaceID, access })
  }
  const preferred = preferredWorkspaceID.trim()
  if (preferred) {
    const idx = seen.get(preferred)
    if (idx === undefined) {
      out.unshift({ workspace_id: preferred, access: 'write' })
    } else {
      out[idx].access = 'write'
    }
  }
  return out
}

export function toCreateInput(s: EditorState): CreateWorkerInput {
  const refs = normalizeSkillRefs(s.skillRefs)
  const workspaceAccess = normalizeWorkspaceAccess(s.workspaceID, s.workspaceAccess)
  const head = refs[0]
  return {
    name: s.name.trim(),
    description: s.description.trim() || undefined,
    model_provider: s.provider,
    model_id: s.modelID.trim(),
    model_endpoint_url:
      providerUsesEndpointField(s.provider) ? s.endpointURL.trim() || undefined : undefined,
    secret_scope_id: s.secretScopeID.trim(),
    // Mirror the first entry into the legacy single-skill fields so a
    // backend rolling on the old shape (or a Worker decoded by a
    // pre-M0.7 consumer) still sees the head skill.
    skill_name: head?.name,
    skill_version: head?.version,
    skill_refs: refs,
    prompt_template: s.promptTemplate,
    parameters_json: s.parametersJSON.trim() || '{}',
    schedule_spec: s.scheduleSpec.trim(),
    tool_allowlist_json: s.toolAllowlistJSON.trim() || '[]',
    output_channels_json: JSON.stringify(s.outputChannels),
    exec_mode: s.execMode,
    concurrency_policy: s.concurrencyPolicy,
    memory_scope_id: s.memoryScopeID.trim() || undefined,
    enabled: s.enabled,
    workspace_id: s.workspaceID.trim(),
    workspace_access: workspaceAccess,
    max_input_tokens: parseCap(s.maxInputTokens),
    max_output_tokens: parseCap(s.maxOutputTokens),
    max_tool_calls: parseCap(s.maxToolCalls),
    max_wall_clock_seconds: parseCap(s.maxWallClockSeconds),
    max_monthly_cost_usd: parseCap(s.maxMonthlyCostUSD, true),
    max_consecutive_failures: parseCap(s.maxConsecutiveFailures),
  }
}

function providerUsesEndpointField(provider: ModelProvider): boolean {
  return (
    provider === 'openai_compat' ||
    provider === 'claude_cli' ||
    provider === 'opencode_cli' ||
    provider === 'grok_cli' ||
    provider === 'mimo_cli'
  )
}

export function toUpdateInput(s: EditorState): UpdateWorkerInput {
  // PATCH is permissive: we just send every field. The backend leaves
  // omitted fields untouched, but we don't want to silently drop user
  // edits, so we keep the full payload.
  const c = toCreateInput(s)
  const u: UpdateWorkerInput = {
    name: c.name,
    description: c.description,
    model_provider: c.model_provider,
    model_id: c.model_id,
    model_endpoint_url: c.model_endpoint_url,
    secret_scope_id: c.secret_scope_id,
    skill_name: c.skill_name,
    skill_version: c.skill_version,
    skill_refs: c.skill_refs,
    prompt_template: c.prompt_template,
    parameters_json: c.parameters_json,
    schedule_spec: c.schedule_spec,
    tool_allowlist_json: c.tool_allowlist_json,
    output_channels_json: c.output_channels_json,
    exec_mode: c.exec_mode,
    concurrency_policy: c.concurrency_policy,
    memory_scope_id: c.memory_scope_id,
    enabled: c.enabled,
    workspace_id: c.workspace_id,
    workspace_access: c.workspace_access,
    max_input_tokens: c.max_input_tokens,
    max_output_tokens: c.max_output_tokens,
    max_tool_calls: c.max_tool_calls,
    max_wall_clock_seconds: c.max_wall_clock_seconds,
    max_monthly_cost_usd: c.max_monthly_cost_usd,
    max_consecutive_failures: c.max_consecutive_failures,
  }
  return u
}
