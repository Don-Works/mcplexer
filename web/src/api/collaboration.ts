import { request } from './client'
import type { Workspace } from './types'

export type PrincipalKind = 'person' | 'machine'
export type PrincipalStatus = 'pending' | 'active' | 'legacy_unverified' | 'revoked'
export type TaskVisibility = 'private' | 'restricted' | 'workspace'

export interface CollaborationPrincipalRecord {
  id: string
  kind: PrincipalKind
  display_name: string
  status: PrincipalStatus
  controlling_principal_id?: string
  is_local_owner: boolean
  created_at: string
  activated_at?: string | null
  revoked_at?: string | null
  revocation_reason?: string
}

export interface CollaborationPrincipal extends CollaborationPrincipalRecord {
  keys: PrincipalIdentityKey[]
  devices: PrincipalDevice[]
  invitations: PrincipalInvitation[]
}

export interface PrincipalIdentityKey {
  id: string
  principal_id: string
  canonical_public_key: string
  fingerprint: string
  algorithm: string
  status: 'pending' | 'active' | 'revoked'
  comment?: string
  created_at: string
  verified_at?: string | null
  revoked_at?: string | null
}

export interface PrincipalDevice {
  id: string
  peer_id: string
  principal_id: string
  identity_key_id?: string
  display_name: string
  kind: 'laptop' | 'server' | 'daemon' | 'unknown'
  status: 'active' | 'legacy_unverified' | 'revoked'
  created_at: string
  verified_at?: string | null
  revoked_at?: string | null
  revocation_reason?: string
}

export interface PrincipalInvitation {
  id: string
  purpose: 'new_principal' | 'add_device' | 'rotate_key'
  principal_id: string
  identity_key_id: string
  created_at: string
  expires_at: string
  consumed_at?: string | null
  consumed_by_peer_id?: string
  revoked_at?: string | null
}

export interface WorkspaceGrant {
  id: string
  share_id: string
  principal_id: string
  capability: string
  granted_epoch: number
  created_at: string
  expires_at?: string | null
  revoked_at?: string | null
}

export interface WorkspacePublicationPolicy {
  share_id: string
  default_visibility: 'private' | 'workspace'
  agent_visibility_ceiling: TaskVisibility
  widening_requires_approval: boolean
  egress_profile: string
  allow_remote_evidence: boolean
}

export interface CollaborationWorkspace {
  share_id: string
  local_workspace_id: string
  home_peer_id: string
  owner_principal_id: string
  status: 'active' | 'revoked'
  access_epoch: number
  workspace?: Workspace
  grants: WorkspaceGrant[]
  policy: WorkspacePublicationPolicy
}

export interface WorkspaceMembership {
  share_id: string
  home_peer_id: string
  remote_workspace_id: string
  local_workspace_id: string
  workspace_name: string
  capabilities: string[]
  access_epoch: number
  cursor_hlc?: string
  status: 'active' | 'revoked'
  joined_at: string
  updated_at: string
  revoked_at?: string | null
}

export interface CollaborationWorkspaceAccess {
  share_id: string
  home_peer_id: string
  remote_workspace_id: string
  workspace_name: string
  access_epoch: number
  capabilities: string[]
  policy?: WorkspacePublicationPolicy
}

export interface CollaborationSnapshot {
  enabled: boolean
  local_peer_id?: string
  principals: CollaborationPrincipal[]
  workspaces: CollaborationWorkspace[]
  memberships: WorkspaceMembership[]
  capabilities: string[]
  profiles: Record<string, string[]>
}

export interface InvitationGrantInput {
  share_id: string
  capabilities: string[]
}

export interface CreateInvitationInput {
  purpose?: 'new_principal' | 'add_device' | 'rotate_key'
  principal_id?: string
  kind?: PrincipalKind
  display_name?: string
  controlling_principal_id?: string
  public_key: string
  replaces_key_id?: string
  workspace_grants?: InvitationGrantInput[]
  expires_in_hours?: number
}

export interface InvitationResult {
  principal: CollaborationPrincipalRecord
  identity_key: PrincipalIdentityKey
  invitation: PrincipalInvitation
  invite_code: string
}

export interface JoinCollaborationResult {
  principal_id: string
  device: PrincipalDevice
  grants: WorkspaceGrant[]
  workspaces: CollaborationWorkspaceAccess[]
}

export interface TaskAccess {
  task_id: string
  workspace_id: string
  share_id?: string
  owner_principal_id?: string
  visibility: TaskVisibility
  visibility_epoch: number
  audience_principal_ids?: string[]
  visibility_editable: boolean
}

export function getCollaboration(signal?: AbortSignal): Promise<CollaborationSnapshot> {
  return request<CollaborationSnapshot>('/collaboration', signal ? { signal } : undefined)
}

export function createCollaborationInvitation(body: CreateInvitationInput): Promise<InvitationResult> {
  return request<InvitationResult>('/collaboration/invitations', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function joinCollaborationInvitation(body: {
  invitation: string
  device_name: string
  device_kind: PrincipalDevice['kind']
}): Promise<JoinCollaborationResult> {
  return request<JoinCollaborationResult>('/collaboration/invitations/join', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function syncWorkspaceMembership(shareId: string): Promise<{
  share_id: string
  home_peer_id: string
  sync_requested: boolean
}> {
  return request(`/collaboration/memberships/${encodeURIComponent(shareId)}/sync`, {
    method: 'POST',
  })
}

export function enrollLocalIdentity(body: {
  public_key: string
  device_name: string
  device_kind: PrincipalDevice['kind']
}): Promise<{ principal_id: string; device: PrincipalDevice; grants: WorkspaceGrant[] }> {
  return request('/collaboration/identity/enroll', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function setWorkspaceGrants(
  shareId: string,
  principalId: string,
  capabilities: string[],
): Promise<{ access_epoch: number; grants: WorkspaceGrant[] }> {
  return request(`/collaboration/shares/${encodeURIComponent(shareId)}/principals/${encodeURIComponent(principalId)}`, {
    method: 'PUT',
    body: JSON.stringify({ capabilities }),
  })
}

export function updateWorkspacePolicy(
  shareId: string,
  policy: Omit<WorkspacePublicationPolicy, 'share_id'>,
): Promise<WorkspacePublicationPolicy> {
  return request(`/collaboration/shares/${encodeURIComponent(shareId)}/policy`, {
    method: 'PUT',
    body: JSON.stringify(policy),
  })
}

export function revokePrincipal(id: string, reason: string): Promise<void> {
  return request(`/collaboration/principals/${encodeURIComponent(id)}/revoke`, {
    method: 'POST',
    body: JSON.stringify({ reason }),
  })
}

export function revokeDevice(peerId: string, reason: string): Promise<void> {
  return request(`/collaboration/devices/${encodeURIComponent(peerId)}/revoke`, {
    method: 'POST',
    body: JSON.stringify({ reason }),
  })
}

export function revokeIdentityKey(keyId: string): Promise<void> {
  return request(`/collaboration/keys/${encodeURIComponent(keyId)}/revoke`, {
    method: 'POST',
  })
}

export function setTaskVisibility(
  taskId: string,
  visibility: TaskVisibility,
  audiencePrincipalIds: string[] = [],
): Promise<TaskAccess> {
  return request(`/collaboration/tasks/${encodeURIComponent(taskId)}/visibility`, {
    method: 'PUT',
    body: JSON.stringify({ visibility, audience_principal_ids: audiencePrincipalIds }),
  })
}

export function getTaskAccess(taskId: string, signal?: AbortSignal): Promise<TaskAccess> {
  return request<TaskAccess>(
    `/collaboration/tasks/${encodeURIComponent(taskId)}/visibility`,
    signal ? { signal } : undefined,
  )
}
