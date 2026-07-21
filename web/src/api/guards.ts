import type { MCPClient, ToolApproval } from './types'
import { request } from './transport'

// Guards (M1-D) — top-level Guards section in the sidebar. The overview
// endpoint returns lightweight per-Guard summary cards; the per-Guard
// detail endpoints return everything needed to render the subpage.
export interface GuardsOverview {
  shell: {
    hooks_installed_count: number
    hooks_total_clients: number
    recent_denied_count_24h: number
  }
  sanitizer: {
    denylist_size: number
    detected_count_24h: number
    envelope_always: boolean
  }
  schedule: {
    jobs_total: number
    jobs_ran_24h: number
  }
  sandbox: {
    driver: string
    unsupported_os: boolean
    clients: Array<{ id: string; name: string; enabled: boolean }>
  }
  mcp: {
    downstream_count: number
    route_count: number
  }
}

export interface ShellGuardDetail {
  hooks_enabled: boolean
  clients: MCPClient[]
  recent_approvals: ToolApproval[]
}

export interface SanitizerGuardDetail {
  envelope_always: boolean
  denylist: string[]
  recent_events: unknown[]
}

export interface SandboxClientStatus {
  id: string
  name: string
  enabled: boolean
}

export interface SandboxGuardDetail {
  driver: string
  unsupported_os: boolean
  downstreams_enabled: boolean
  active_description: string
  clients: SandboxClientStatus[]
}

// ScheduledJob mirrors store.ScheduledJob. Kept here rather than in
// types.ts because nothing outside the Guards section consumes it.
export interface ScheduledJob {
  id: string
  name: string
  kind: string
  spec: string
  command: string
  args_json: string
  env_json: string
  cwd: string
  surface: string
  enabled: boolean
  survive_daemon_down: boolean
  native_driver: string
  native_id: string
  last_run_at?: string
  next_run_at?: string
  last_status: string
  last_error: string
  created_at: string
  updated_at: string
}

export interface ScheduleListResult {
  jobs: ScheduledJob[]
}

export function getGuardsOverview(): Promise<GuardsOverview> {
  return request('/guards')
}

export function getShellGuardDetail(): Promise<ShellGuardDetail> {
  return request('/guards/shell')
}

export function installShellHooks(clientId: string): Promise<{ installed: boolean; receipt_id: string }> {
  return request(`/guards/shell/clients/${encodeURIComponent(clientId)}/install_hooks`, {
    method: 'POST',
  })
}

export function uninstallShellHooks(clientId: string): Promise<{ uninstalled: boolean }> {
  return request(`/guards/shell/clients/${encodeURIComponent(clientId)}/uninstall_hooks`, {
    method: 'POST',
  })
}

// Approval rules — trusted-allowlist patterns that the manager's
// PolicyResolver consults after a 5s grace period, auto-deciding any
// approval whose surface + tool pattern + cwd match a rule. Empty
// pattern means "every tool on this surface".
export interface ApprovalRule {
  id: string
  surface: string
  pattern: string
  directory: string
  ai_session_id: string
  decision: 'allow' | 'deny' | 'prompt'
  priority: number
  expires_at?: string | null
  hit_count: number
  last_hit_at?: string | null
  created_by: string
  created_at: string
  updated_at: string
  // allow_metachars opts THIS rule into bypassing the shell-hook's
  // cheap-block on shell metacharacters (`;|&` + backtick + newlines).
  // Useful for the wildcard "allow + audit everything" rule so it can
  // honour its own name for commands like `ssh host 'a | b'` or
  // `cmd 2>&1`. Narrower than dangerous-mode: only the metachar block
  // is lifted; protected paths, downstream-config checks, and audit
  // logging still apply.
  allow_metachars?: boolean
}

export interface ApprovalRuleInput {
  surface: string
  pattern: string
  directory: string
  ai_session_id: string
  decision: 'allow' | 'deny' | 'prompt'
  priority: number
  expires_at?: string | null
  created_by: string
  allow_metachars?: boolean
}

export function listApprovalRules(surface?: string): Promise<{ rules: ApprovalRule[] }> {
  const q = surface ? `?surface=${encodeURIComponent(surface)}` : ''
  return request(`/approval-rules${q}`)
}

export function createApprovalRule(input: ApprovalRuleInput): Promise<ApprovalRule> {
  return request('/approval-rules', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function updateApprovalRule(id: string, input: ApprovalRuleInput): Promise<ApprovalRule> {
  return request(`/approval-rules/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

export function deleteApprovalRule(id: string): Promise<void> {
  return request(`/approval-rules/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

export function getSanitizerGuardDetail(): Promise<SanitizerGuardDetail> {
  return request('/guards/sanitizer')
}

export function updateSanitizerGuard(envelopeAlways: boolean): Promise<SanitizerGuardDetail> {
  return request('/guards/sanitizer', {
    method: 'PUT',
    body: JSON.stringify({ envelope_always: envelopeAlways }),
  })
}

export function getScheduleGuardList(): Promise<ScheduleListResult> {
  return request('/guards/schedule')
}

export interface ScheduleCreateInput {
  name: string
  kind: string
  spec: string
  command: string
  args?: string[]
  env?: Record<string, string>
  cwd?: string
}

export function createScheduledJob(input: ScheduleCreateInput): Promise<{ job: ScheduledJob }> {
  return request('/guards/schedule', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function runScheduledJob(id: string): Promise<{ ran: boolean }> {
  return request(`/guards/schedule/${encodeURIComponent(id)}/run`, { method: 'POST' })
}

export function deleteScheduledJob(id: string): Promise<{ deleted: boolean }> {
  return request(`/guards/schedule/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function getSandboxGuardDetail(): Promise<SandboxGuardDetail> {
  return request('/guards/sandbox')
}

export function enableSandbox(clientId: string): Promise<{ enabled: boolean }> {
  return request(`/guards/sandbox/clients/${encodeURIComponent(clientId)}/enable`, {
    method: 'POST',
  })
}

export function disableSandbox(clientId: string): Promise<{ disabled: boolean }> {
  return request(`/guards/sandbox/clients/${encodeURIComponent(clientId)}/disable`, {
    method: 'POST',
  })
}

export function updateSandboxGuard(downstreamsEnabled: boolean): Promise<SandboxGuardDetail> {
  return request('/guards/sandbox', {
    method: 'PUT',
    body: JSON.stringify({ downstreams_enabled: downstreamsEnabled }),
  })
}
