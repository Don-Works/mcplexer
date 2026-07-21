import { request } from './transport'

// Model Profiles (Layer 2: provider configurations workers reference by id).
export interface ModelProfile {
  id: string
  name: string
  provider:
    | 'anthropic'
    | 'openai'
    | 'openai_compat'
    | 'claude_cli'
    | 'opencode_cli'
    | 'grok_cli'
    | 'mimo_cli'
    | 'gemini_cli'
    | 'codex_cli'
    | 'pi_cli'
  endpoint_url: string
  secret_scope_id?: string
  known_models: string[]
  builtin: boolean
  created_at: string
  updated_at: string
}

export interface ModelProfileInput {
  name: string
  provider: ModelProfile['provider']
  endpoint_url?: string
  secret_scope_id?: string
  known_models?: string[]
}

export function listModelProfiles(): Promise<ModelProfile[]> {
  return request('/model-profiles')
}

export function getModelProfile(id: string): Promise<ModelProfile> {
  return request(`/model-profiles/${encodeURIComponent(id)}`)
}

export function createModelProfile(input: ModelProfileInput): Promise<ModelProfile> {
  return request('/model-profiles', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function updateModelProfile(
  id: string,
  input: Partial<ModelProfileInput>,
): Promise<ModelProfile> {
  return request(`/model-profiles/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

export function deleteModelProfile(id: string): Promise<{ deleted: boolean }> {
  return request(`/model-profiles/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export interface ModelProfileTestResult {
  ok: boolean
  latency_ms: number
  error?: string
}

export function testModelProfile(id: string): Promise<ModelProfileTestResult> {
  return request(
    `/model-profiles/${encodeURIComponent(id)}/test`,
    { method: 'POST' },
    { timeoutMs: 90_000 },
  )
}

export function listProfileModels(id: string): Promise<{ models: string[]; cached: boolean }> {
  return request(`/model-profiles/${encodeURIComponent(id)}/models`, undefined, { timeoutMs: 90_000 })
}

// OpenCode runtime (Layer 3: built-in subprocess management).
export interface OpenCodeStatus {
  installed: boolean
  running: boolean
  port?: number
  binary_path?: string
  version?: string
  started_at?: string
  last_error?: string
}

export function getOpenCodeStatus(): Promise<OpenCodeStatus> {
  return request('/opencode/status')
}

export function startOpenCode(): Promise<OpenCodeStatus> {
  return request('/opencode/start', { method: 'POST' }, { timeoutMs: 90_000 })
}

export function stopOpenCode(): Promise<OpenCodeStatus> {
  return request('/opencode/stop', { method: 'POST' }, { timeoutMs: 90_000 })
}

export function listOpenCodeModels(
  opts?: { refresh?: boolean },
): Promise<{ models: string[]; cached: boolean }> {
  const path = opts?.refresh ? '/opencode/models?refresh=1' : '/opencode/models'
  return request(path, undefined, { timeoutMs: 90_000 })
}
