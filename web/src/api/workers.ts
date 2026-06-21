// Workers API client (M0.6). Mirrors the workersadmin.Service surface
// exposed over HTTP by internal/api/workers_handler.go. Types are kept
// in sync with the Go structs (Worker, WorkerRun, WorkerSummary). When
// the backend evolves, this file changes first.

import { request } from './client'

// Worker is the full record persisted in SQLite. Mirrors store.Worker.
// JSON fields stay as strings here; the editor decodes them on demand
// so we don't pay parse/stringify on every list-page render.
export interface Worker {
  id: string
  name: string
  description: string
  model_provider: ModelProvider
  model_id: string
  model_endpoint_url?: string
  secret_scope_id: string
  skill_name?: string
  skill_version?: string
  // Canonical multi-skill list — joined in order by the runner before
  // being sent as the system prompt. Falls back to skill_name /
  // skill_version when null/empty (legacy single-skill workers).
  skill_refs?: SkillRef[] | null
  prompt_template: string
  parameters_json: string
  schedule_spec: string
  tool_allowlist_json: string
  output_channels_json: string
  exec_mode: ExecMode
  concurrency_policy: ConcurrencyPolicy
  memory_scope_id?: string
  // M1 safety caps — 0 means "use the runner default" (or "no cap" for
  // monthly cost + consecutive failures).
  max_input_tokens: number
  max_output_tokens: number
  max_tool_calls: number
  max_wall_clock_seconds: number
  max_monthly_cost_usd: number
  max_consecutive_failures: number
  // auto_paused_reason carries WHY the worker is paused. Empty for
  // manual pauses; populated by the runner's auto-pause logic.
  auto_paused_reason?: string
  enabled: boolean
  workspace_id: string
  workspace_access?: WorkerWorkspaceAccess[] | null
  created_at: string
  updated_at: string
  // M3 — registry-template lineage. Empty / 0 = hand-built.
  source_template_name?: string
  source_template_version?: number
}

export type ModelProvider =
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
export type ExecMode = 'propose' | 'autonomous'
export type ConcurrencyPolicy = 'skip' | 'queue'

// One entry in a Worker's ordered skill list. Version is optional;
// empty version = "latest stable" (matches the SkillReader convention).
export interface SkillRef {
  name: string
  version?: string
}

export type WorkerWorkspaceAccessMode = 'read' | 'write'

export interface WorkerWorkspaceAccess {
  workspace_id: string
  access: WorkerWorkspaceAccessMode
}

export type WorkerRunStatus =
  | 'running'
  | 'success'
  | 'failure'
  | 'paused'
  | 'cap_exceeded'
  | 'awaiting_approval'
  | 'rejected'
  | 'interrupted'
  | 'cancelled'

// WorkerSummary is the slim list-row from the admin service.
export interface WorkerSummary {
  id: string
  name: string
  model_provider: string
  model_id: string
  schedule_spec: string
  enabled: boolean
  last_run_status?: WorkerRunStatus | ''
  last_run_at?: string
  created_at: string
  workspace_id: string
  ephemeral?: boolean
  delegation_id?: string
  delegation_objective?: string
  delegation_task_id?: string
  delegation_task_kind?: string
  delegation_worker_mode?: 'execute' | 'review' | ''
}

export interface WorkerRun {
  id: string
  worker_id: string
  workspace_id?: string
  started_at: string
  finished_at?: string
  duration_ms: number
  status: WorkerRunStatus
  prompt_rendered: string
  model_provider: string
  model_id: string
  input_tokens: number
  output_tokens: number
  cost_usd: number
  tool_calls_count: number
  // How tool_calls_count was computed:
  // - 'native'  — the runner populated it from the model adapter's ToolCalls slice
  // - 'derived' — derived from audit_records at read time, because the adapter
  //              family (claude_cli, opencode_cli, grok_cli, mimo_cli) doesn't surface tool_use
  //              events natively yet. The UI shows a hint tooltip in this case
  //              so operators don't conclude a healthy CLI worker is hallucinating.
  tool_calls_count_source?: 'native' | 'derived'
  accounting_missing?: boolean
  tool_calls_cap_scope?: 'gateway_loop' | 'cli_audit' | ''
  tool_calls_cap_exceeded?: boolean
  deliverable_status?: 'success_with_output' | 'spend_no_commit' | 'failed_no_output' | 'partial' | 'unknown'
  has_deliverable_commit?: boolean
  has_deliverable_branch?: boolean
  deliverable_commit?: string
  deliverable_branch?: string
  worker_reported_status?: string
  output_text: string
  error: string
  mesh_message_ids_json: string
  audit_record_ids_json: string
}

// GetOutput bundles the worker config and a slice of recent runs in one
// response — matches workersadmin.GetOutput on the Go side.
export interface WorkerWithRuns {
  worker: Worker
  recent_runs: WorkerRun[]
}

export interface CreateWorkerInput {
  name: string
  description?: string
  model_provider: ModelProvider
  model_id: string
  model_endpoint_url?: string
  secret_scope_id: string
  skill_name?: string
  skill_version?: string
  // Canonical multi-skill list. When set, overrides skill_name /
  // skill_version. Empty array clears every attached skill.
  skill_refs?: SkillRef[]
  prompt_template: string
  parameters_json?: string
  schedule_spec: string
  tool_allowlist_json?: string
  output_channels_json?: string
  exec_mode?: ExecMode
  concurrency_policy?: ConcurrencyPolicy
  memory_scope_id?: string
  enabled?: boolean
  workspace_id: string
  workspace_access?: WorkerWorkspaceAccess[]

  // M1 safety caps.
  max_input_tokens?: number
  max_output_tokens?: number
  max_tool_calls?: number
  max_wall_clock_seconds?: number
  max_monthly_cost_usd?: number
  max_consecutive_failures?: number
}

// WorkerApproval — propose-first approval ledger row (M1).
export type WorkerApprovalStatus = 'pending' | 'approved' | 'rejected'

export interface WorkerApproval {
  id: string
  worker_id: string
  run_id: string
  tool_name: string
  tool_input: string
  reason: string
  status: WorkerApprovalStatus
  decision: string
  decided_by?: string
  created_at: string
  decided_at?: string
  resumed_run_id?: string
}

export interface ApproveOutput {
  approval_id: string
  status: 'approved'
  resumed_run_id: string
  original_run_id: string
}

export interface RejectOutput {
  approval_id: string
  status: 'rejected'
  original_run_id: string
}

// All fields optional on update — backend leaves omitted fields untouched.
export type UpdateWorkerInput = Partial<Omit<CreateWorkerInput, 'workspace_id'>> & {
  enabled?: boolean
  workspace_id?: string
}

export interface RunNowOutput {
  run_id: string
  status: WorkerRunStatus
}

export interface DelegationParent {
  context_id?: string
  session_id?: string
  model?: string
  input_tokens?: number
  output_tokens?: number
  cost_usd?: number
}

export interface DelegationBaseline {
  tokens_estimate?: number
  cost_usd?: number
}

export interface DelegationReview {
  reviewed?: boolean
  score?: number
  outcome?: 'accepted' | 'partial' | 'rejected' | ''
  notes?: string
  reviewer_context_id?: string
  reviewer_model?: string
  task_kind?: string
  scores?: Record<string, number>
  model_scores?: DelegationModelReview[]
  reviewed_at?: string
}

export interface DelegationModelReview {
  model_key?: string
  worker_id?: string
  score: number
  outcome?: 'accepted' | 'partial' | 'rejected' | ''
  notes?: string
  scores?: Record<string, number>
}

export interface DelegationAggregate {
  workers: number
  running: number
  success: number
  failure: number
  cancelled?: number
  interrupted?: number
  dispatched: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  tool_calls: number
  duration_ms: number
  parent_tokens: number
  combined_tokens: number
  parent_cost_usd: number
  combined_cost_usd: number
  baseline_tokens: number
  baseline_cost_usd: number
  frontier_tokens_avoided?: number
  frontier_cost_avoided_usd?: number
  worker_token_delta?: number
  estimated_parent_tokens_saved: number
  net_tokens_delta: number
  estimated_cost_saved_usd: number
  savings_basis?: 'baseline_estimate' | 'parent_usage_only' | 'missing_baseline' | string
  savings_confidence?: 'estimated' | 'missing' | string
  parent_tokens_known: boolean
  review_score?: number
  unknown_cost_runs?: number
  unknown_duration_ms?: number
  cost_all_missing?: boolean
  real_dollars_spent?: number
  quota_tokens_by_bucket?: Record<string, number>
  frontier_quota_preserved?: number
  frontier_quota_burned?: number
  real_cost_saved_usd?: number
}

export interface DelegationModelStat {
  model_provider: string
  model_id: string
  model_key: string
  worker_ids?: string[]
  runs: number
  success: number
  failure: number
  running: number
  cancelled?: number
  interrupted?: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  unknown_cost_runs?: number
  duration_ms: number
  avg_duration_ms: number
  unknown_duration_ms?: number
  review_count?: number
  review_score: number
  task_kind?: string
  capability_scores?: Record<string, number>
}

export interface DelegationWorkerContext {
  worker: Worker
  latest_run?: WorkerRun
  recent_runs: WorkerRun[]
  parallel_index: number
  parallel_total: number
}

export interface DelegationContext {
  id: string
  workspace_id: string
  objective: string
  handoff?: string
  task_id?: string
  task_kind?: string
  worker_mode: 'execute' | 'review'
  model_selection_mode?: ModelSelectionMode
  review_required: boolean
  parent: DelegationParent
  baseline: DelegationBaseline
  review?: DelegationReview
  parallel_total: number
  created_at: string
  updated_at: string
  status: string
  aggregate: DelegationAggregate
  model_stats?: DelegationModelStat[]
  workers: DelegationWorkerContext[]
}

export type ModelSelectionMode = 'single' | 'ranked' | 'random' | 'side_by_side' | 'capacity'

export interface DelegationModelCandidate {
  label?: string
  model_profile_id?: string
  model_provider?: ModelProvider
  model_id?: string
  model_endpoint_url?: string
  secret_scope_id?: string
  capability_tags?: string[]
  input_modalities?: string[]
  output_modalities?: string[]
}

export interface CreateDelegationInput {
  workspace_id: string
  objective: string
  handoff?: string
  name?: string
  task_id?: string
  task_kind?: string
  worker_mode?: 'execute' | 'review'
  review_required?: boolean
  model_profile_id?: string
  model_provider?: ModelProvider
  model_id?: string
  model_endpoint_url?: string
  secret_scope_id?: string
  model_selection_mode?: ModelSelectionMode
  model_candidate_index?: number
  model_candidates?: DelegationModelCandidate[]
  tool_allowlist_json?: string
  parent_context_id?: string
  parent_session_id?: string
  parent_model?: string
  parent_input_tokens?: number
  parent_output_tokens?: number
  parent_cost_usd?: number
  baseline_tokens_estimate?: number
  baseline_cost_usd?: number
  parallelism?: number
  max_input_tokens?: number
  max_output_tokens?: number
  max_tool_calls?: number
  max_wall_clock_seconds?: number
  max_monthly_cost_usd?: number
}

export interface DelegationDispatch {
  worker_id: string
  run_id?: string
  status: string
  name: string
  parallel_index: number
  parallel_total: number
}

export interface CreateDelegationOutput {
  delegation_id: string
  workspace_id: string
  objective: string
  task_kind?: string
  worker_mode: 'execute' | 'review'
  model_selection_mode?: ModelSelectionMode
  review_required: boolean
  parent: DelegationParent
  baseline: DelegationBaseline
  dispatches: DelegationDispatch[]
}

export interface ReviewDelegationInput {
  workspace_id?: string
  score: number
  outcome?: 'accepted' | 'partial' | 'rejected'
  notes?: string
  reviewer_context_id?: string
  reviewer_model?: string
  task_kind?: string
  scores?: Record<string, number>
  model_scores?: DelegationModelReview[]
}

export interface DelegationModelCapacity {
  rank: number
  label?: string
  model_profile_id?: string
  model_provider: ModelProvider
  model_id: string
  model_key: string
  capability_tags?: string[]
  input_modalities?: string[]
  output_modalities?: string[]
  available: boolean
  unavailable_reason?: string
  capacity_score: number
  runs: number
  success: number
  failure: number
  running: number
  operational_failures?: number
  review_count: number
  review_score: number
  success_rate: number
  operational_success_rate?: number
  accounting_known?: boolean
  cost_usd: number
  avg_duration_ms: number
  // exploration_bonus is the UCB-style optimism folded into capacity_score for
  // an under-sampled candidate (decays to ~0 as runs accrue). exploring marks a
  // fresh/rarely-tried model the ranker is still boosting — a "new / promising"
  // option to try, not yet a proven default.
  exploration_bonus?: number
  exploring?: boolean
}

export interface ListDelegationCapacityParams {
  workspaceId?: string
  taskKind?: string
  limit?: number
}

export interface ListWorkersParams {
  workspaceId?: string
  enabledOnly?: boolean
  namePattern?: string
}

function buildListQuery(params?: ListWorkersParams): string {
  if (!params) return ''
  const q = new URLSearchParams()
  if (params.workspaceId) q.set('workspace_id', params.workspaceId)
  if (params.enabledOnly) q.set('enabled_only', 'true')
  if (params.namePattern) q.set('name_pattern', params.namePattern)
  const s = q.toString()
  return s ? `?${s}` : ''
}

export function listWorkers(params?: ListWorkersParams): Promise<WorkerSummary[]> {
  return request(`/workers${buildListQuery(params)}`)
}

export function getWorker(id: string): Promise<WorkerWithRuns> {
  return request(`/workers/${encodeURIComponent(id)}`)
}

export function createWorker(data: CreateWorkerInput): Promise<Worker> {
  return request('/workers', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateWorker(id: string, data: UpdateWorkerInput): Promise<Worker> {
  return request(`/workers/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(data),
  })
}

export function deleteWorker(id: string): Promise<void> {
  return request(`/workers/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function pauseWorker(id: string): Promise<Worker> {
  return request(`/workers/${encodeURIComponent(id)}/pause`, { method: 'POST' })
}

export function resumeWorker(id: string): Promise<Worker> {
  return request(`/workers/${encodeURIComponent(id)}/resume`, { method: 'POST' })
}

export function runWorkerNow(id: string): Promise<RunNowOutput> {
  return request(`/workers/${encodeURIComponent(id)}/run-now`, { method: 'POST' })
}

export interface ListDelegationsParams {
  workspaceId?: string
  limit?: number
}

export function listDelegations(
  params?: ListDelegationsParams,
): Promise<DelegationContext[]> {
  const q = new URLSearchParams()
  if (params?.workspaceId) q.set('workspace_id', params.workspaceId)
  if (params?.limit) q.set('limit', String(params.limit))
  const tail = q.toString() ? `?${q.toString()}` : ''
  return request(`/delegations${tail}`)
}

export function listDelegationModelCapacity(
  params?: ListDelegationCapacityParams,
): Promise<DelegationModelCapacity[]> {
  const q = new URLSearchParams()
  if (params?.workspaceId) q.set('workspace_id', params.workspaceId)
  if (params?.taskKind) q.set('task_kind', params.taskKind)
  if (params?.limit) q.set('limit', String(params.limit))
  const tail = q.toString() ? `?${q.toString()}` : ''
  return request(`/delegations/model-capacity${tail}`)
}

export function createDelegation(
  data: CreateDelegationInput,
): Promise<CreateDelegationOutput> {
  // Returns promptly (202 Accepted) with delegation_id + dispatches even if
  // managed CLI runtime (opencode etc.) or worker startup is slow. The
  // heavy work is async; UI observes via list + WorkerLiveTail/live updates.
  // Do not extend timeout here; the backend change is the fix.
  return request('/delegations', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function reviewDelegation(
  id: string,
  data: ReviewDelegationInput,
): Promise<DelegationContext> {
  return request(`/delegations/${encodeURIComponent(id)}/review`, {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export interface ListRunsParams {
  limit?: number
  status?: WorkerRunStatus
}

export function listWorkerRuns(id: string, params?: ListRunsParams): Promise<WorkerRun[]> {
  const q = new URLSearchParams()
  if (params?.limit) q.set('limit', String(params.limit))
  if (params?.status) q.set('status', params.status)
  const tail = q.toString() ? `?${q.toString()}` : ''
  return request(`/workers/${encodeURIComponent(id)}/runs${tail}`)
}

export function getWorkerRun(workerID: string, runID: string): Promise<WorkerRun> {
  return request(
    `/workers/${encodeURIComponent(workerID)}/runs/${encodeURIComponent(runID)}`,
  )
}

export interface CancelWorkerRunResult {
  run_id: string
  status: string
  reason: string
}

// cancelWorkerRun hard-stops a live (or orphaned) worker run. The
// backend interrupts the runner goroutine + kills any model subprocess
// group and finalises the run as status=cancelled. 409 when the run is
// already finished; 404 when it doesn't exist.
export function cancelWorkerRun(runID: string, reason?: string): Promise<CancelWorkerRunResult> {
  return request(`/worker-runs/${encodeURIComponent(runID)}/cancel`, {
    method: 'POST',
    body: reason ? JSON.stringify({ reason }) : undefined,
  })
}

// ToolCatalogItem is one row from /api/v1/tools — the editor uses it
// to render a namespace-grouped checkbox grid in place of the M0
// JSON-textarea fallback. write_class is inferred the same way the
// runner's dispatcher classifies a tool on dispatch, so the UI's
// write/read split agrees with the propose-mode gate.
export interface ToolCatalogItem {
  name: string
  description: string
  namespace: string
  write_class: boolean
}

export function listTools(): Promise<ToolCatalogItem[]> {
  return request('/tools')
}

// M2 — workspace-wide cost dashboard payload.

export interface WorkerCostDailyPoint {
  date: string // YYYY-MM-DD (UTC day)
  cost_usd: number
}

export interface WorkerCostAggregateRow {
  worker_id: string
  worker_name: string
  workspace_id: string
  daily_costs: WorkerCostDailyPoint[]
  month_to_date_usd: number
  run_count_30d: number
}

export interface WorkerCostAggregateOutput {
  days: number
  workspace_id?: string
  total_mtd_usd: number
  total_window_usd: number
  total_runs_30d: number
  workers: WorkerCostAggregateRow[]
}

export interface CostAggregateParams {
  days?: number
  workspaceId?: string
}

export function getWorkerCostAggregate(
  params?: CostAggregateParams,
): Promise<WorkerCostAggregateOutput> {
  const q = new URLSearchParams()
  if (params?.days) q.set('days', String(params.days))
  if (params?.workspaceId) q.set('workspace_id', params.workspaceId)
  const tail = q.toString() ? `?${q.toString()}` : ''
  return request(`/workers/cost-aggregate${tail}`)
}

// M1 — propose-first approval surface.

export interface ListApprovalsParams {
  status?: WorkerApprovalStatus | ''
  limit?: number
}

export function listWorkerApprovals(
  params?: ListApprovalsParams,
): Promise<WorkerApproval[]> {
  const q = new URLSearchParams()
  if (params?.status) q.set('status', params.status)
  if (params?.limit) q.set('limit', String(params.limit))
  const tail = q.toString() ? `?${q.toString()}` : ''
  return request(`/worker-approvals${tail}`)
}

export function approveWorkerApproval(
  id: string,
  decidedBy?: string,
): Promise<ApproveOutput> {
  return request(`/worker-approvals/${encodeURIComponent(id)}/approve`, {
    method: 'POST',
    body: JSON.stringify({ decided_by: decidedBy ?? 'operator' }),
  })
}

export function rejectWorkerApproval(
  id: string,
  decidedBy?: string,
): Promise<RejectOutput> {
  return request(`/worker-approvals/${encodeURIComponent(id)}/reject`, {
    method: 'POST',
    body: JSON.stringify({ decided_by: decidedBy ?? 'operator' }),
  })
}

// M4 — mesh-trigger surface. A WorkerMeshTrigger fires its parent
// worker whenever an arriving mesh message matches every non-empty
// match field. throttle_seconds bounds per-(trigger, source) re-fires;
// max_chain_depth is the reflexive-loop guard.
export interface TriggerFromFilter {
  peer_id?: string
  agent_name?: string
  // Legacy read-only field: role filtering was never implemented and the
  // backend now rejects inputs that set it ("role filtering is not
  // implemented"). Kept so old rows that persisted a role still render.
  role?: string
}

export interface WorkerMeshTrigger {
  id: string
  worker_id: string
  tag_match: string
  kind_match: string
  audience_match: string
  content_regex: string
  from_filters: TriggerFromFilter[]
  throttle_seconds: number
  max_chain_depth: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface MeshTriggerInput {
  tag_match?: string
  kind_match?: string
  audience_match?: string
  content_regex?: string
  from_filters?: TriggerFromFilter[]
  throttle_seconds?: number
  max_chain_depth?: number
  enabled?: boolean
  all_messages?: boolean
}

export function listWorkerMeshTriggers(
  workerID: string,
): Promise<WorkerMeshTrigger[]> {
  return request(`/workers/${encodeURIComponent(workerID)}/mesh-triggers`)
}

export function createWorkerMeshTrigger(
  workerID: string,
  data: MeshTriggerInput,
): Promise<WorkerMeshTrigger> {
  return request(`/workers/${encodeURIComponent(workerID)}/mesh-triggers`, {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateWorkerMeshTrigger(
  workerID: string,
  triggerID: string,
  data: MeshTriggerInput,
): Promise<WorkerMeshTrigger> {
  return request(
    `/workers/${encodeURIComponent(workerID)}/mesh-triggers/${encodeURIComponent(triggerID)}`,
    {
      method: 'PATCH',
      body: JSON.stringify(data),
    },
  )
}

export function deleteWorkerMeshTrigger(
  workerID: string,
  triggerID: string,
): Promise<void> {
  return request(
    `/workers/${encodeURIComponent(workerID)}/mesh-triggers/${encodeURIComponent(triggerID)}`,
    { method: 'DELETE' },
  )
}

export interface PeerTriggerGrant {
  peer_id: string
  scope: string
}

export function grantTriggerToPeer(
  peerID: string,
  workerName: string,
): Promise<PeerTriggerGrant> {
  return request(`/peers/${encodeURIComponent(peerID)}/trigger-grants`, {
    method: 'POST',
    body: JSON.stringify({ worker_name: workerName }),
  })
}

export function revokeTriggerGrant(
  peerID: string,
  workerName: string,
): Promise<void> {
  return request(
    `/peers/${encodeURIComponent(peerID)}/trigger-grants/${encodeURIComponent(workerName)}`,
    { method: 'DELETE' },
  )
}
