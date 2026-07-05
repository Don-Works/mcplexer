import { request } from './client'

// GitStatus mirrors brain.GitStatus on the Go side.
export interface BrainGitStatus {
  initialized: boolean
  dirty: boolean
  ahead: number
  behind: number
  has_remote: boolean
  has_upstream: boolean
  branch: string
  last_commit: string
}

// BrainStatus mirrors brainStatusResponse on the Go side: the dashboard
// tile payload (enable flag, repo dir for the open-in-VSCode link, git
// status, validation-error count).
export interface BrainStatus {
  enabled: boolean
  dir: string
  git?: BrainGitStatus
  git_error?: string
  error_count: number
}

// BrainError mirrors store.BrainError: one frontmatter validation failure
// surfaced to the dashboard rather than silently indexing a record that
// lies.
export interface BrainError {
  id: string
  path: string
  entity_kind?: string
  field?: string
  reason: string
  created_at: string
}

export interface BrainDrift {
  kind: string
  path: string
  entity_id?: string
  detail?: string
}

export interface BrainVerifyResult {
  ok: boolean
  files_checked: number
  drifts: BrainDrift[]
}

export interface BrainPushResult {
  pushed: boolean
  conflict: boolean
  detail?: string
  note?: string
  status?: BrainGitStatus
}

export function getBrainStatus(init?: RequestInit): Promise<BrainStatus> {
  return request('/brain/status', init)
}

export function listBrainErrors(): Promise<BrainError[]> {
  return request('/brain/errors')
}

export function pushBrain(): Promise<BrainPushResult> {
  return request('/brain/push', { method: 'POST' })
}

export function syncBrain(): Promise<BrainVerifyResult> {
  return request('/brain/sync', { method: 'POST' })
}
