import type { FileClaimsResponse, MeshStatusResponse } from './types'
import { request } from './transport'

// Mesh
export function getMeshStatus(params?: { msg?: string; includeTaskEvents?: boolean }): Promise<MeshStatusResponse> {
  const qs = new URLSearchParams()
  if (params?.msg) qs.set('msg', params.msg)
  if (params?.includeTaskEvents) qs.set('include_task_events', '1')
  return request(`/mesh/status${qs.toString() ? `?${qs}` : ''}`)
}

// Mesh agent "Focus" — switches the user's local tmux to the target
// agent's pane. Local agents use tmux switch-client; peer-origin agents
// spawn a new local tmux window with an SSH session to the peer.
export interface MeshFocusResponse {
  ok: boolean
  mode: 'local' | 'remote'
  message?: string
}

export function focusMeshAgent(sessionId: string): Promise<MeshFocusResponse> {
  return request(`/mesh/agents/${encodeURIComponent(sessionId)}/focus`, {
    method: 'POST',
  })
}

// File claims (M7.4)
export function getFileClaims(params?: {
  repo?: string
  branch?: string
  path?: string
  claimer?: string
}): Promise<FileClaimsResponse> {
  const qs = new URLSearchParams()
  if (params?.repo) qs.set('repo', params.repo)
  if (params?.branch) qs.set('branch', params.branch)
  if (params?.path) qs.set('path', params.path)
  if (params?.claimer) qs.set('claimer', params.claimer)
  const suffix = qs.toString() ? `?${qs.toString()}` : ''
  return request(`/claims${suffix}`)
}
