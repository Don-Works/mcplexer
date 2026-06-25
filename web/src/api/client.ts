import type {
  AuditFilter,
  AuditRecord,
  AuthScope,
  CatalogResponse,
  ConnectDownstreamRequest,
  ConnectDownstreamResponse,
  DashboardData,
  DownstreamOAuthSetupResponse,
  DownstreamOAuthStatusResponse,
  DownstreamServer,
  DryRunRequest,
  DryRunResult,
  MCPClient,
  MCPInstallPreview,
  MCPInstallStatus,
  FileClaimsResponse,
  MeshStatusResponse,
  OAuthCapabilities,
  OAuthProvider,
  OAuthQuickSetupRequest,
  OAuthQuickSetupResponse,
  OAuthStatus,
  OAuthTemplate,
  P2PIdentityResponse,
  P2PPeersResponse,
  PaginatedResponse,
  RouteRule,
  Settings,
  SettingsResponse,
  ToolApproval,
  ToolDescriptionFilter,
  ToolDescriptionVersion,
  User,
  UsersResponse,
  UserWithPeers,
  Workspace,
  WorkspaceLink,
  WorkspaceLinkSuggestion,
  HarnessKey,
  HarnessSetupRow,
  HarnessSetupStatusResponse,
} from './types'

const BASE = import.meta.env.VITE_API_BASE_URL || '/api/v1'

// Default per-request timeout. This is load-bearing, not a nicety: the
// gateway is served over plain http://, so the browser speaks HTTP/1.1 and
// caps us at ~6 connections per origin. Several always-on SSE streams
// (notifications/approvals/secret-prompts/…) each permanently hold one of
// those slots, leaving only a couple for everything else. Without a
// timeout a single slow response holds its connection open indefinitely;
// stacked sidebar polls then saturate the remaining slots and EVERY
// subsequent request hangs as "pending" with no way to recover (a classic
// connection-pool livelock — the symptom was "click around → whole UI goes
// to permanent loading"). A finite timeout guarantees a stuck request
// aborts and releases its connection so the pool always drains.
//
// 30s is comfortably above every healthy read/write (badge polls finish in
// well under a second) while still bounding recovery. Genuinely-long ops
// (downstream discovery, backups, external-API probes) pass an explicit
// larger `timeoutMs` below.
const DEFAULT_TIMEOUT_MS = 30_000

export interface RequestOptions {
  // Override the default request timeout in ms. Pass 0 to disable entirely
  // (reserve for genuinely unbounded operations).
  timeoutMs?: number
}

// apiURL composes a fully-qualified path under the API base. Useful for
// non-JSON endpoints (file downloads, multipart uploads) that need to
// bypass the request<T> JSON wrapper.
export function apiURL(path: string): string {
  return `${BASE}${path}`
}

export class ApiClientError extends Error {
  status: number
  body: string

  constructor(status: number, body: string) {
    super(`API error ${status}: ${body}`)
    this.name = 'ApiClientError'
    this.status = status
    this.body = body
  }
}

// timeoutSignal returns an AbortSignal that fires after `ms`, combined with
// any caller-supplied signal so both an external cancel and the timeout can
// abort the fetch. Returns undefined when timeouts are disabled and no
// caller signal was given.
function timeoutSignal(ms: number, caller?: AbortSignal | null): AbortSignal | undefined {
  if (ms <= 0) return caller ?? undefined
  const timeout = AbortSignal.timeout(ms)
  if (!caller) return timeout
  // AbortSignal.any combines both; guard for older runtimes.
  if (typeof AbortSignal.any === 'function') return AbortSignal.any([caller, timeout])
  return caller
}

export async function request<T>(
  path: string,
  init?: RequestInit,
  opts?: RequestOptions,
): Promise<T> {
  const signal = timeoutSignal(opts?.timeoutMs ?? DEFAULT_TIMEOUT_MS, init?.signal)
  const res = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
    ...(signal ? { signal } : {}),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new ApiClientError(res.status, body)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

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

// Human users / owned devices
export function listUsers(): Promise<UsersResponse> {
  return request('/users')
}

export function getSelfUser(): Promise<User> {
  return request('/users/self')
}

export function getUser(id: string): Promise<UserWithPeers> {
  return request(`/users/${encodeURIComponent(id)}`)
}

// Auth Scopes
export function listAuthScopes(init?: RequestInit): Promise<AuthScope[]> {
  return request('/auth-scopes', init)
}

export function getAuthScope(id: string): Promise<AuthScope> {
  return request(`/auth-scopes/${id}`)
}

export function createAuthScope(
  data: Omit<AuthScope, 'id' | 'has_secrets' | 'source' | 'created_at' | 'updated_at'>,
): Promise<AuthScope> {
  return request('/auth-scopes', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateAuthScope(
  id: string,
  data: Partial<Omit<AuthScope, 'id' | 'created_at' | 'updated_at'>>,
): Promise<AuthScope> {
  return request(`/auth-scopes/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteAuthScope(id: string): Promise<void> {
  return request(`/auth-scopes/${id}`, { method: 'DELETE' })
}

// Secrets (env-type auth scopes)
export function listSecretKeys(scopeId: string): Promise<{ keys: string[] }> {
  return request(`/auth-scopes/${scopeId}/secrets`)
}

export function putSecret(scopeId: string, key: string, value: string): Promise<{ status: string }> {
  return request(`/auth-scopes/${scopeId}/secrets`, {
    method: 'PUT',
    body: JSON.stringify({ key, value }),
  })
}

export function deleteSecret(scopeId: string, key: string): Promise<void> {
  return request(`/auth-scopes/${scopeId}/secrets/${encodeURIComponent(key)}`, {
    method: 'DELETE',
  })
}

// OAuth Providers
export function listOAuthProviders(): Promise<OAuthProvider[]> {
  return request('/oauth-providers')
}

export function getOAuthProvider(id: string): Promise<OAuthProvider> {
  return request(`/oauth-providers/${id}`)
}

export function createOAuthProvider(
  data: Omit<OAuthProvider, 'id' | 'has_client_secret' | 'source' | 'created_at' | 'updated_at'> & { client_secret?: string },
): Promise<OAuthProvider> {
  return request('/oauth-providers', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateOAuthProvider(
  id: string,
  data: Partial<Omit<OAuthProvider, 'id' | 'has_client_secret' | 'source' | 'created_at' | 'updated_at'>>,
): Promise<OAuthProvider> {
  return request(`/oauth-providers/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteOAuthProvider(id: string): Promise<void> {
  return request(`/oauth-providers/${id}`, { method: 'DELETE' })
}

// OAuth Templates
export function listOAuthTemplates(): Promise<OAuthTemplate[]> {
  return request('/oauth-templates')
}

// OIDC Discovery
export function discoverOIDC(issuerURL: string): Promise<{
  authorize_url: string
  token_url: string
  scopes: string[]
  use_pkce: boolean
  issuer: string
}> {
  return request('/oauth-providers/discover', {
    method: 'POST',
    body: JSON.stringify({ issuer_url: issuerURL }),
  })
}

// OAuth Quick Setup
export function oauthQuickSetup(data: OAuthQuickSetupRequest): Promise<OAuthQuickSetupResponse> {
  return request(
    '/auth-scopes/oauth-quick-setup',
    { method: 'POST', body: JSON.stringify(data) },
    { timeoutMs: 90_000 },
  )
}

// OAuth Flow
export function getOAuthAuthorizeURL(scopeId: string): Promise<{ authorize_url: string }> {
  return request(`/auth-scopes/${scopeId}/oauth/authorize`)
}

export function getOAuthStatus(scopeId: string): Promise<OAuthStatus> {
  return request(`/auth-scopes/${scopeId}/oauth/status`)
}

export function revokeOAuthToken(scopeId: string): Promise<void> {
  return request(`/auth-scopes/${scopeId}/oauth/revoke`, { method: 'POST' })
}

// Server Catalog
export function fetchCatalog(): Promise<CatalogResponse> {
  return request('/catalog')
}

// Downstream Servers
export function listDownstreams(init?: RequestInit): Promise<DownstreamServer[]> {
  return request('/downstreams', init)
}

export function getDownstream(id: string): Promise<DownstreamServer> {
  return request(`/downstreams/${id}`)
}

export function createDownstream(
  data: Omit<DownstreamServer, 'id' | 'created_at' | 'updated_at' | 'capabilities_cache' | 'source'> & { id?: string },
): Promise<DownstreamServer> {
  return request('/downstreams', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateDownstream(
  id: string,
  data: Partial<Omit<DownstreamServer, 'id' | 'created_at' | 'updated_at'>>,
): Promise<DownstreamServer> {
  return request(`/downstreams/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteDownstream(id: string): Promise<void> {
  return request(`/downstreams/${id}`, { method: 'DELETE' })
}

// Route Rules
export function listRoutes(init?: RequestInit): Promise<RouteRule[]> {
  return request('/routes', init)
}

export function getRoute(id: string): Promise<RouteRule> {
  return request(`/routes/${id}`)
}

export function createRoute(
  data: Omit<RouteRule, 'id' | 'created_at' | 'updated_at'>,
): Promise<RouteRule> {
  return request('/routes', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateRoute(
  id: string,
  data: Partial<Omit<RouteRule, 'id' | 'created_at' | 'updated_at'>>,
): Promise<RouteRule> {
  return request(`/routes/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteRoute(id: string): Promise<void> {
  return request(`/routes/${id}`, { method: 'DELETE' })
}

export function bulkCreateRoutes(
  rules: Omit<RouteRule, 'id' | 'created_at' | 'updated_at'>[],
): Promise<RouteRule[]> {
  return request('/routes/bulk', {
    method: 'POST',
    body: JSON.stringify(rules),
  })
}

// Audit
export function queryAuditLogs(
  filter: AuditFilter,
): Promise<PaginatedResponse<AuditRecord>> {
  const params = new URLSearchParams()
  if (filter.id) params.set('id', filter.id)
  if (filter.workspace_id) params.set('workspace_id', filter.workspace_id)
  if (filter.tool_name) params.set('tool_name', filter.tool_name)
  if (filter.status) params.set('status', filter.status)
  if (filter.execution_id) params.set('execution_id', filter.execution_id)
  if (filter.session_id) params.set('session_id', filter.session_id)
  if (filter.after) params.set('after', filter.after)
  if (filter.before) params.set('before', filter.before)
  if (filter.limit) params.set('limit', String(filter.limit))
  if (filter.offset) params.set('offset', String(filter.offset))
  return request(`/audit?${params.toString()}`)
}

// Dashboard
export function getDashboard(range_?: string): Promise<DashboardData> {
  const params = range_ ? `?range=${range_}` : ''
  return request(`/dashboard${params}`)
}

// Discover Tools — spawns the downstream process and lists its tools; can
// take tens of seconds for a cold start.
export function discoverTools(id: string): Promise<DownstreamServer> {
  return request(`/downstreams/${id}/discover`, { method: 'POST' }, { timeoutMs: 120_000 })
}

// Downstream OAuth
export function setupDownstreamOAuth(
  id: string,
  scopeName?: string,
): Promise<DownstreamOAuthSetupResponse> {
  return request(
    `/downstreams/${id}/oauth-setup`,
    {
      method: 'POST',
      body: JSON.stringify(scopeName ? { auth_scope_name: scopeName } : {}),
    },
    { timeoutMs: 90_000 },
  )
}

// Connect Downstream (unified setup)
export function connectDownstream(
  id: string,
  data: ConnectDownstreamRequest,
): Promise<ConnectDownstreamResponse> {
  return request(
    `/downstreams/${id}/connect`,
    { method: 'POST', body: JSON.stringify(data) },
    { timeoutMs: 120_000 },
  )
}

export function getDownstreamOAuthStatus(
  id: string,
): Promise<DownstreamOAuthStatusResponse> {
  return request(`/downstreams/${id}/oauth-status`)
}

export function getOAuthCapabilities(
  id: string,
): Promise<OAuthCapabilities> {
  return request(`/downstreams/${id}/oauth-capabilities`)
}

// Approvals
export function listApprovals(status?: string): Promise<ToolApproval[]> {
  const params = new URLSearchParams()
  if (status) params.set('status', status)
  const qs = params.toString()
  return request(`/approvals${qs ? `?${qs}` : ''}`)
}

export function getApproval(id: string): Promise<ToolApproval> {
  return request(`/approvals/${id}`)
}

export function resolveApproval(
  id: string,
  data: { approved: boolean; reason: string },
): Promise<{ status: string }> {
  return request(`/approvals/${id}/resolve`, {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

// Dry Run
export function dryRun(params: DryRunRequest): Promise<DryRunResult> {
  return request('/dry-run', {
    method: 'POST',
    body: JSON.stringify(params),
  })
}

// MCP Install
export function getMCPInstallStatus(): Promise<MCPInstallStatus> {
  return request('/mcp-install/status')
}

export function installMCP(clientId: string): Promise<MCPClient> {
  return request(`/mcp-install/${clientId}/install`, { method: 'POST' })
}

export function uninstallMCP(clientId: string): Promise<MCPClient> {
  return request(`/mcp-install/${clientId}/uninstall`, { method: 'POST' })
}

export function previewMCPInstall(clientId: string): Promise<MCPInstallPreview> {
  return request(`/mcp-install/${clientId}/preview`)
}

// Cache / reload
export interface CacheStats {
  hits: number
  misses: number
  evictions: number
  entries: number
  hit_rate: number
}

export interface CacheStatsResponse {
  tool_call: CacheStats
  route_resolution: CacheStats
}

export function getCacheStats(): Promise<CacheStatsResponse> {
  return request('/cache/stats')
}

export function flushCache(layer: 'tool_call' | 'route' | 'all' = 'all'): Promise<{ status: string }> {
  return request('/cache/flush', {
    method: 'POST',
    body: JSON.stringify({ layer }),
  })
}

// Health / System
export interface SystemInfo {
  mode: string
  version: string
  http_addr?: string
  socket_path?: string
  data_dir?: string
  config_file?: string
  log_path?: string
  addons_dir?: string
  p2p_enabled: boolean
  server_profile?: string
  capabilities?: Record<string, boolean>
}

export interface HealthResponse {
  status: string
  version: string
  uptime_seconds: number
  mode: string
  system: SystemInfo
}

export function getHealth(): Promise<HealthResponse> {
  return request('/health')
}

export type SystemRevealTarget = 'data_dir' | 'config_file' | 'log_path' | 'addons_dir'

export function revealSystemPath(target: SystemRevealTarget): Promise<void> {
  return request('/system/reveal', {
    method: 'POST',
    body: JSON.stringify({ target }),
  })
}

export type SystemTerminalTarget = 'data_dir' | 'addons_dir'

// Open a terminal window with cwd set to one of the daemon's known paths
// (data_dir by default). Used by the "Configure with AI" CTA — the agent
// running in that terminal then drives mcplexer's MCP tools to configure it.
export function launchSystemTerminal(target: SystemTerminalTarget = 'data_dir'): Promise<void> {
  return request('/system/launch-terminal', {
    method: 'POST',
    body: JSON.stringify({ target }),
  })
}

// Settings
export function getSettings(): Promise<SettingsResponse> {
  return request('/settings')
}

export function updateSettings(data: Settings): Promise<SettingsResponse> {
  return request('/settings', {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}
// Description Refinement
export function listDescriptionVersions(
  filter: ToolDescriptionFilter = {},
): Promise<PaginatedResponse<ToolDescriptionVersion>> {
  const params = new URLSearchParams()
  if (filter.tool_name) params.set('tool_name', filter.tool_name)
  if (filter.status) params.set('status', filter.status)
  if (filter.source) params.set('source', filter.source)
  if (filter.limit) params.set('limit', String(filter.limit))
  if (filter.offset) params.set('offset', String(filter.offset))
  const qs = params.toString()
  return request(`/descriptions${qs ? `?${qs}` : ''}`)
}

export function getDescriptionVersion(id: string): Promise<ToolDescriptionVersion> {
  return request(`/descriptions/${id}`)
}

export function acceptDescription(
  id: string,
  reviewNote?: string,
): Promise<{ status: string }> {
  return request(`/descriptions/${id}/accept`, {
    method: 'POST',
    body: JSON.stringify({ review_note: reviewNote || '' }),
  })
}

export function rejectDescription(
  id: string,
  reviewNote: string,
): Promise<{ status: string }> {
  return request(`/descriptions/${id}/reject`, {
    method: 'POST',
    body: JSON.stringify({ review_note: reviewNote }),
  })
}

export function submitDescription(data: {
  tool_name: string
  description: string
  rationale?: string
}): Promise<ToolDescriptionVersion> {
  return request('/descriptions', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

// Mesh
export function getMeshStatus(): Promise<MeshStatusResponse> {
  return request('/mesh/status')
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

// Custom MCP Addons (M6.2)
export type AddonAuthKind =
  | 'none'
  | 'bearer'
  | 'api_key_header'
  | 'api_key_query'
  | 'hawk'
  | 'oauth2'
  | 'oauth2_pending'

export type OAuth2GrantType = 'authorization_code' | 'client_credentials'

export interface AddonAuthSpec {
  kind: AddonAuthKind
  header_name?: string
  query_name?: string

  // OAuth2 fields. Empty unless kind is oauth2 or oauth2_pending.
  auth_url?: string
  token_url?: string
  scopes?: string[]
  client_id?: string
  use_pkce?: boolean
  grant_type?: OAuth2GrantType
}

export interface AddonParamSpec {
  name: string
  type: 'string' | 'integer' | 'number' | 'boolean'
  in: 'path' | 'query' | 'body'
  description?: string
  required?: boolean
}

export interface AddonEndpointSpec {
  name: string
  description: string
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: string
  params?: AddonParamSpec[]
}

export interface AddonSpec {
  name: string
  description: string
  base_url: string
  parent_server: string
  auth_scope?: string
  auth: AddonAuthSpec
  endpoints: AddonEndpointSpec[]
}

export interface AddonCreateResponse {
  name: string
  path: string
  tools: string[]
}

export function previewAddon(spec: AddonSpec): Promise<{ yaml: string }> {
  return request('/addons/preview', {
    method: 'POST',
    body: JSON.stringify(spec),
  })
}

export function createAddon(spec: AddonSpec): Promise<AddonCreateResponse> {
  return request('/addons', {
    method: 'POST',
    body: JSON.stringify(spec),
  })
}

// importAddonOpenAPI returns a draft AddonSpec parsed from an OpenAPI 3.x
// document. parent_server is intentionally left unset — the user picks one
// in the wizard's Basics step.
export function importAddonOpenAPI(input: {
  spec_url?: string
  spec_inline?: string
}): Promise<AddonSpec> {
  return request(
    '/addons/import-openapi',
    { method: 'POST', body: JSON.stringify(input) },
    { timeoutMs: 90_000 },
  )
}

export interface AddonOAuthWizardRequest {
  auth_scope_name: string
  parent_server: string
  auth_url: string
  token_url: string
  scopes: string[]
  client_id: string
  client_secret?: string
  use_pkce: boolean
  grant_type: OAuth2GrantType
}

export interface AddonOAuthWizardResponse {
  auth_scope: { id: string; name: string } | null
  provider: { id: string; name: string } | null
  authorize_url?: string
  human_approval_required: boolean
  message: string
}

export function runAddonOAuthSetup(
  req: AddonOAuthWizardRequest,
): Promise<AddonOAuthWizardResponse> {
  return request(
    '/addons/oauth-setup',
    { method: 'POST', body: JSON.stringify(req) },
    { timeoutMs: 90_000 },
  )
}

export interface AddonPreviewCallRequest {
  spec: AddonSpec
  endpoint: string
  args: Record<string, unknown>
  auth_scope_id?: string
}

export interface AddonPreviewHTTP {
  method?: string
  url?: string
  headers: Record<string, string>
  body: string
}

export interface AddonPreviewCallResponse {
  request: AddonPreviewHTTP
  response: AddonPreviewHTTP
  status: number
  duration_ms: number
  note: string
}

export function previewAddonCall(
  req: AddonPreviewCallRequest,
): Promise<AddonPreviewCallResponse> {
  return request(
    '/addons/preview-call',
    { method: 'POST', body: JSON.stringify(req) },
    { timeoutMs: 90_000 },
  )
}

// P2P (debug-only — endpoints live at /api/p2p/* not /api/v1/p2p/*).
// Returns 501 if the daemon was built without the `p2p` build tag; the UI
// should treat that as "feature off, hide the panel".
async function p2pRequest<T>(path: string): Promise<T | null> {
  const base = import.meta.env.VITE_API_BASE_URL ? import.meta.env.VITE_API_BASE_URL.replace(/\/v1$/, '') : '/api'
  const res = await fetch(`${base}${path}`, { signal: AbortSignal.timeout(DEFAULT_TIMEOUT_MS) })
  if (res.status === 501 || res.status === 404) return null
  if (!res.ok) {
    throw new ApiClientError(res.status, await res.text())
  }
  return res.json() as Promise<T>
}

export function getP2PIdentity(): Promise<P2PIdentityResponse | null> {
  return p2pRequest('/p2p/identity')
}

export function getP2PPeers(): Promise<P2PPeersResponse | null> {
  return p2pRequest('/p2p/peers')
}

// P2P (M1.3) — peer connection-mode status.
export interface P2PPeerStatus {
  peer_id: string
  connection_mode?: 'direct' | 'hole-punched' | 'relay' | ''
  last_seen?: string
  addrs: string[]
}

export async function getP2PPeerStatus(peerID: string): Promise<P2PPeerStatus> {
  const res = await fetch(`/api/p2p/peers/${encodeURIComponent(peerID)}/status`, {
    headers: { 'Content-Type': 'application/json' },
    signal: AbortSignal.timeout(DEFAULT_TIMEOUT_MS),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new ApiClientError(res.status, body)
  }
  return res.json() as Promise<P2PPeerStatus>
}

// Backup / restore — full snapshots of the data dir (DB + secrets + skills).
export interface BackupManifest {
  id: string
  created_at: string
  mcplexer_version?: string
  note?: string
  db_sha256: string
  size_bytes: number
  includes_secrets: boolean
  includes_skills: boolean
  pre_restore_of?: string
}

export interface BackupRestoreResult {
  restored_from: string
  pre_restore_snapshot_id: string
  daemon_restart_required: boolean
}

export function listBackups(): Promise<BackupManifest[]> {
  return request('/backups')
}

export function createBackup(note?: string): Promise<BackupManifest> {
  return request(
    '/backups',
    { method: 'POST', body: JSON.stringify({ note: note ?? '' }) },
    { timeoutMs: 180_000 },
  )
}

export function restoreBackup(id: string): Promise<BackupRestoreResult> {
  return request(
    `/backups/${encodeURIComponent(id)}/restore`,
    { method: 'POST' },
    { timeoutMs: 180_000 },
  )
}

export function deleteBackup(id: string): Promise<void> {
  return request(`/backups/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// Direct download URL — server streams the tarball with Content-Disposition.
// Used as <a href={...} download> rather than a fetch + blob roundtrip.
export function backupDownloadURL(id: string): string {
  return `${BASE}/backups/${encodeURIComponent(id)}/download`
}

// Skills Registry — agent-facing search/publish/versioning for SKILL.md docs.
// MCP equivalents (mcpx__skill_search/get/publish/list, plus admin tools)
// are the canonical surface for agents; this is the human's view.
export interface SkillRegistryEntry {
  id: string
  name: string
  version: number
  content_hash: string
  description: string
  body: string
  metadata?: Record<string, unknown>
  tags?: string[]
  author?: string
  parent_version?: number
  deleted_at?: string | null
  published_at: string
  created_by_agent_id?: string
  workspace_id?: string | null
  source_type?: 'inline' | 'path' | 'bundle' | 'git'
  source_path?: string
  bundle_sha256?: string
}

export interface SkillSearchHit {
  name: string
  version: number
  description: string
  score: number
  scope?: string
}

export interface PublishSkillResult {
  name: string
  version: number
  content_hash: string
  action: 'created' | 'deduped'
}

export type SkillScopeFilter =
  | { mode: 'all' }
  | { mode: 'global' }
  | { mode: 'workspace'; workspaceId: string }

function skillScopeQuery(s?: SkillScopeFilter): string {
  if (!s || s.mode === 'all') return ''
  if (s.mode === 'global') return '?scope=global'
  return `?scope=workspace&workspace_id=${encodeURIComponent(s.workspaceId)}`
}

export function listSkillRegistry(scope?: SkillScopeFilter): Promise<SkillRegistryEntry[]> {
  return request(`/skill-registry${skillScopeQuery(scope)}`)
}

export function searchSkillRegistry(
  q: string,
  limit = 10,
): Promise<SkillSearchHit[]> {
  const params = new URLSearchParams({ q, limit: String(limit) })
  return request(`/skill-registry/search?${params.toString()}`)
}

export function getSkillRegistryEntry(
  name: string,
  version?: number | 'latest' | 'stable',
): Promise<SkillRegistryEntry> {
  const params = new URLSearchParams()
  if (version != null && version !== 'latest') params.set('version', String(version))
  const qs = params.toString()
  return request(`/skill-registry/${encodeURIComponent(name)}${qs ? '?' + qs : ''}`)
}

export function listSkillRegistryVersions(name: string): Promise<SkillRegistryEntry[]> {
  return request(`/skill-registry/${encodeURIComponent(name)}/versions`)
}

export interface SkillVersionDiff {
  name: string
  old_version: number
  new_version: number
  body_diff?: string
  frontmatter_diff?: string
  tree?: Array<{
    path: string
    old_sha?: string
    new_sha?: string
    status: string
  }>
  old_has_bundle: boolean
  new_has_bundle: boolean
}

export function getSkillRegistryVersionDiff(
  name: string,
  oldVersion?: number | 'latest',
  newVersion?: number | 'latest',
): Promise<SkillVersionDiff> {
  const params = new URLSearchParams()
  if (oldVersion != null) params.set('old_version', String(oldVersion))
  if (newVersion != null) params.set('new_version', String(newVersion))
  const qs = params.toString()
  return request(
    `/skill-registry/${encodeURIComponent(name)}/diff${qs ? `?${qs}` : ''}`,
  )
}

export function publishSkillRegistry(opts: {
  name: string
  body: string
  parent_version?: number
  author?: string
  scope?: 'global' | 'workspace'
  workspace_id?: string
}): Promise<PublishSkillResult> {
  return request('/skill-registry', {
    method: 'POST',
    body: JSON.stringify(opts),
  })
}

export function deleteSkillRegistry(name: string, version?: number): Promise<void> {
  const qs = version ? `?version=${version}` : ''
  return request(`/skill-registry/${encodeURIComponent(name)}${qs}`, {
    method: 'DELETE',
  })
}

export function setSkillRegistryTag(
  name: string,
  tag: string,
  version: number,
): Promise<void> {
  return request(`/skill-registry/${encodeURIComponent(name)}/tags`, {
    method: 'POST',
    body: JSON.stringify({ tag, version }),
  })
}

// Local-skills migration (W5). Walks a local directory of agentskills.io
// SKILL.md folders (default ~/.claude/skills/) and classifies each row
// against the registry: 'new' (will publish), 'duplicate' (same hash —
// nothing to do), 'version-conflict' (different hash — overwrite to bump
// the version), or 'unparseable' (bad frontmatter; surface the error).
export type LocalSkillStatus = 'new' | 'duplicate' | 'version-conflict' | 'unparseable'

export interface LocalSkill {
  dir: string
  path: string
  name: string
  description: string
  content_hash: string
  status: LocalSkillStatus
  registry_version?: number
  parse_error?: string
}

export interface LocalUnpublishedResponse {
  path: string
  skills: LocalSkill[]
}

export type LocalSkillImportAction =
  | 'imported'
  | 'skipped'
  | 'updated'
  | 'failed'

export interface LocalSkillImportResult {
  name: string
  dir: string
  path: string
  action: LocalSkillImportAction
  version?: number
  bundle_sha256?: string
  archived_to?: string
  error?: string
  dry_run?: boolean
}

export function listLocalUnpublishedSkills(
  source?: string,
): Promise<LocalUnpublishedResponse> {
  const params = new URLSearchParams()
  if (source) params.set('source', source)
  const qs = params.toString()
  return request(`/skills/local-unpublished${qs ? '?' + qs : ''}`)
}

export function importLocalSkill(req: {
  name: string
  source_dir: string
  overwrite?: boolean
}): Promise<LocalSkillImportResult> {
  return request('/skills/import', {
    method: 'POST',
    body: JSON.stringify(req),
  })
}

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

// Hammerspoon — optional macOS desktop-automation bridge. Three endpoints:
//   GET  /hammerspoon/snippet — embedded Lua bridge (download via <a href>).
//   POST /hammerspoon/install — write the snippet + rotate the bridge password.
//   POST /hammerspoon/probe   — 5-step diagnostic; result also persisted into
//                               the downstream row's CapabilitiesCache so the
//                               dashboard can render a traffic-light without
//                               re-probing on every page load.
export interface HammerspoonInstallResponse {
  ok: boolean
  files_written: string[]
  init_lua_modified: boolean
  init_lua_backup?: string
  reload_attempted: boolean
  reload_error?: string
  next_steps: string[]
}

export interface HammerspoonInstallErrorBody {
  error: string
  step: string
}

export interface HammerspoonProbeCheck {
  ok: boolean
  duration_ms: number
  detail?: string
}

export interface HammerspoonProbeRemediation {
  check: string
  title: string
  body: string
}

export type HammerspoonHealth = 'ok' | 'degraded' | 'broken'

export interface HammerspoonProbeResponse {
  health: HammerspoonHealth
  checks: Record<string, HammerspoonProbeCheck>
  probed_at: string
  remediation?: HammerspoonProbeRemediation[]
}

export function hammerspoonSnippetURL(): string {
  return `${BASE}/hammerspoon/snippet`
}

export async function fetchHammerspoonSnippet(): Promise<string> {
  const res = await fetch(hammerspoonSnippetURL())
  if (!res.ok) {
    throw new ApiClientError(res.status, await res.text())
  }
  return res.text()
}

export function installHammerspoon(): Promise<HammerspoonInstallResponse> {
  return request(
    '/hammerspoon/install',
    { method: 'POST', body: JSON.stringify({}) },
    { timeoutMs: 90_000 },
  )
}

export function probeHammerspoon(): Promise<HammerspoonProbeResponse> {
  return request(
    '/hammerspoon/probe',
    { method: 'POST', body: JSON.stringify({}) },
    { timeoutMs: 90_000 },
  )
}

export function getHarnessSetupStatus(): Promise<HarnessSetupStatusResponse> {
  return request('/setup/status')
}

export function installHarness(harness: HarnessKey): Promise<HarnessSetupRow> {
  return request('/setup/install', {
    method: 'POST',
    body: JSON.stringify({ harness }),
  })
}

export function recheckHarness(harness: HarnessKey): Promise<HarnessSetupRow> {
  return request('/setup/recheck', {
    method: 'POST',
    body: JSON.stringify({ harness }),
  })
}
