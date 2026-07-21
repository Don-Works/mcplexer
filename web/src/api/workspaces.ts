import type {
  User,
  UsersResponse,
  UserWithPeers,
  Workspace,
  WorkspaceLink,
  WorkspaceLinkSuggestion,
} from './types'
import { request } from './transport'

// Workspaces
export function listWorkspaces(init?: RequestInit): Promise<Workspace[]> {
  return request('/workspaces', init)
}

export function getWorkspace(id: string): Promise<Workspace> {
  return request(`/workspaces/${id}`)
}

export function createWorkspace(
  data: Omit<Workspace, 'id' | 'created_at' | 'updated_at'>,
): Promise<Workspace> {
  return request('/workspaces', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateWorkspace(
  id: string,
  data: Partial<Omit<Workspace, 'id' | 'created_at' | 'updated_at'>>,
): Promise<Workspace> {
  return request(`/workspaces/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteWorkspace(id: string): Promise<void> {
  return request(`/workspaces/${id}`, { method: 'DELETE' })
}

// Workspace Links — operator-declared cross-machine links. A task created
// in the local workspace replicates to the linked peer's workspace.
export function listWorkspaceLinks(): Promise<WorkspaceLink[]> {
  return request('/workspace-links')
}

export interface CreateWorkspaceLinkRequest {
  peer_id: string
  // local_workspace accepts either the workspace id OR its name.
  local_workspace: string
  remote_workspace_id: string
  remote_workspace_name?: string
}

export interface CreateWorkspaceLinkResponse {
  linked: true
  peer_id: string
  local_workspace_id: string
  local_workspace_name: string
  remote_workspace_id: string
  remote_workspace_name: string
  granted_scope: string
  scope_grant_warning?: string
}

export function createWorkspaceLink(
  body: CreateWorkspaceLinkRequest,
): Promise<CreateWorkspaceLinkResponse> {
  return request('/workspace-links', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function deleteWorkspaceLink(
  peerId: string,
  remoteWorkspaceId: string,
): Promise<{ unlinked: true }> {
  const params = new URLSearchParams({
    peer_id: peerId,
    remote_workspace_id: remoteWorkspaceId,
  })
  return request(`/workspace-links?${params.toString()}`, { method: 'DELETE' })
}

export function suggestWorkspaceLinks(): Promise<{
  suggestions: WorkspaceLinkSuggestion[]
}> {
  return request('/workspace-links/suggest')
}

// People / owned devices
export function listUsers(): Promise<UsersResponse> {
  return request('/users')
}

export function createUser(displayName: string): Promise<User> {
  return request('/users', {
    method: 'POST',
    body: JSON.stringify({ display_name: displayName }),
  })
}

export function getSelfUser(): Promise<User> {
  return request('/users/self')
}

export function getUser(id: string): Promise<UserWithPeers> {
  return request(`/users/${encodeURIComponent(id)}`)
}

export function updateUser(id: string, displayName: string): Promise<User> {
  return request(`/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify({ display_name: displayName }),
  })
}

export function deleteUser(id: string): Promise<void> {
  return request(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function updateDeviceOwner(
  peerId: string,
  userId: string | null,
): Promise<{ peer_id: string; user_id: string | null }> {
  return request(`/users/devices/${encodeURIComponent(peerId)}`, {
    method: 'PATCH',
    body: JSON.stringify({ user_id: userId }),
  })
}
