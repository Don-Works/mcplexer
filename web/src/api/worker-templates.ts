// Worker template API client (M3). Mirrors the Go-side
// api.workerTemplatesHandler + skillregistry.WorkerTemplate JSON shape.
// Templates live in the skill registry under payload_type=worker; this
// module hides that detail so callers only think about templates.

import { request } from './client'
import type { Worker } from './workers'

// Slim row returned by the list endpoint — the card surface.
export interface WorkerTemplateSummary {
  name: string
  version: number
  description: string
  model_provider_hint?: string
  model_id_hint?: string
  parameter_count: number
  secret_slot_count: number
  published_at: string
  author?: string
}

// Parameter slot rendered as one input in the install modal.
export interface TemplateParameter {
  name: string
  label?: string
  type?: 'text' | 'textarea' | 'number'
  required?: boolean
  default?: string
  description?: string
}

// Secret slot — the install modal lets the user bind an existing
// AuthScope OR create a new one carrying this slot's keyed value.
export interface TemplateSecretSlot {
  name: string
  description?: string
  provider_hint?: string
}

// OutputChannel hint, surfaced verbatim from the template body so the
// installed Worker inherits the publisher's mesh/file routing.
// `priority_on_fail` (optional) overrides `priority` when the run
// terminated in a non-success state — lets templates declare e.g.
// priority=low on green runs and priority_on_fail=high on red ones.
export interface OutputChannelHint {
  type: string
  priority?: string
  priority_on_fail?: string
  // Channel-specific fields. Each is consumed by a subset of types and
  // ignored by the rest — same wide-union shape the runner uses, so the
  // template install path is a straight passthrough.
  url?: string
  channel?: string
  prefix?: string
  path?: string
  mode?: string
}

// Full template body — what the install modal needs to render its form.
export interface WorkerTemplate {
  name: string
  description?: string
  model_provider_hint?: string
  model_id_hint?: string
  skill_name?: string
  skill_version?: string
  prompt_template: string
  schedule_spec_hint?: string
  tool_allowlist?: string[]
  output_channels_hint?: OutputChannelHint[]
  exec_mode_hint?: string
  parameter_schema?: TemplateParameter[]
  secret_slots?: TemplateSecretSlot[]
}

// /api/v1/worker-templates/{name}/{version} returns the full registry
// entry alongside the decoded template body.
export interface WorkerTemplateFull {
  entry: {
    id: string
    name: string
    version: number
    description: string
    published_at: string
    author?: string
    payload_type: string
  }
  template: WorkerTemplate
}

// Install payload — matches the Go-side
// workersadmin.InstallFromTemplateInput one-for-one.
//
// Secret-slot binding: prefer `secret_bindings` (per-slot map) for
// multi-slot templates. `secret_scope_id` is the legacy single-slot
// path; servers fall back to it when secret_bindings is empty.
export interface InstallTemplateInput {
  template_name: string
  template_version?: number
  worker_name: string
  workspace_id: string
  secret_scope_id?: string
  secret_bindings?: Record<string, string>
  parameters?: Record<string, string>
  schedule_spec?: string
  exec_mode?: string
  enabled?: boolean
}

export interface ListTemplatesParams {
  search?: string
  limit?: number
}

export function listWorkerTemplates(
  params?: ListTemplatesParams,
): Promise<WorkerTemplateSummary[]> {
  const q = new URLSearchParams()
  if (params?.search) q.set('search', params.search)
  if (params?.limit) q.set('limit', String(params.limit))
  const tail = q.toString() ? `?${q.toString()}` : ''
  return request(`/worker-templates${tail}`)
}

export function getWorkerTemplate(
  name: string,
  version: number | 'latest' = 'latest',
): Promise<WorkerTemplateFull> {
  const v = typeof version === 'number' ? String(version) : version
  return request(
    `/worker-templates/${encodeURIComponent(name)}/${encodeURIComponent(v)}`,
  )
}

export function installWorkerTemplate(
  data: InstallTemplateInput,
): Promise<Worker> {
  return request(`/worker-templates/install`, {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export interface PublishWorkerInput {
  name?: string
  description?: string
}

// Skill registry entry returned on publish — the dashboard uses
// `version` + `content_hash` for the success-toast confirmation.
export interface PublishedTemplateEntry {
  id: string
  name: string
  version: number
  content_hash: string
  description: string
  payload_type: string
}

export function publishWorkerAsTemplate(
  workerId: string,
  data?: PublishWorkerInput,
): Promise<PublishedTemplateEntry> {
  return request(`/workers/${encodeURIComponent(workerId)}/publish`, {
    method: 'POST',
    body: JSON.stringify(data ?? {}),
  })
}
