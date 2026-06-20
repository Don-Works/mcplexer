export interface Workspace {
  id: string
  name: string
  root_path: string
  tags: Record<string, string>
  default_policy: 'allow' | 'deny'
  created_at: string
  updated_at: string
}

// WorkspaceLink — an operator-declared link that makes a task created in
// one workspace replicate to a linked peer's workspace across machines.
export interface WorkspaceLink {
  peer_id: string
  local_workspace_id: string
  local_workspace_name?: string
  remote_workspace_id: string
  remote_workspace_name?: string
  link_established_by?: string
}

// WorkspaceLinkSuggestion — a candidate link surfaced by the gateway to
// prefill the "Link workspace" form.
export interface WorkspaceLinkSuggestion {
  peer_id: string
  remote_workspace_id: string
  remote_workspace_name?: string
  local_workspace_id: string
  local_workspace_name?: string
}

export interface EnvField {
  key: string
  label: string
  secret: boolean
}

export interface AuthScope {
  id: string
  name: string
  // Human-readable label preferred by UI surfaces. Empty string when
  // unset — UI falls back to a humanised form of `name` (the stable
  // slug). `name` remains the externally-referenced handle.
  display_name: string
  type: 'env' | 'header' | 'hawk' | 'oauth2' | 'client_credentials'
  oauth_provider_id: string
  has_secrets: boolean
  env_fields?: EnvField[]
  redaction_hints: string[]
  source: string
  created_at: string
  updated_at: string
}

export interface OAuthProvider {
  id: string
  name: string
  template_id: string
  authorize_url: string
  token_url: string
  client_id: string
  has_client_secret: boolean
  scopes: string[]
  use_pkce: boolean
  source: string
  created_at: string
  updated_at: string
}

export interface OAuthTemplate {
  id: string
  name: string
  authorize_url: string
  token_url: string
  scopes: string[]
  use_pkce: boolean
  needs_secret: boolean
  supports_auto_discovery: boolean
  setup_url: string
  help_text: string
  callback_url: string
}

export interface OAuthCapabilities {
  has_template: boolean
  template: OAuthTemplate | null
  supports_auto_discovery: boolean
  needs_credentials: boolean
}

export interface OAuthQuickSetupRequest {
  name: string
  template_id?: string
  provider_id?: string
  client_id?: string
  client_secret?: string
}

export interface OAuthQuickSetupResponse {
  auth_scope: AuthScope
  provider: OAuthProvider
  authorize_url: string
}

export interface OAuthStatus {
  status: 'valid' | 'expired' | 'refresh_needed' | 'not_configured'
  expires_at: string | null
}

export interface ConnectDownstreamRequest {
  workspace_id?: string
  client_id?: string
  client_secret?: string
  scope_name?: string
  account_label?: string
}

export interface ConnectDownstreamResponse {
  auth_scope: AuthScope
  provider: OAuthProvider
  route_rule: RouteRule
  authorize_url: string
}

export interface DownstreamOAuthSetupResponse {
  auth_scope: AuthScope
  provider: OAuthProvider
  authorize_url: string
}

export interface DownstreamOAuthStatusEntry {
  auth_scope_id: string
  auth_scope_name: string
  status: 'authenticated' | 'expired' | 'not_configured'
  expires_at: string | null
  workspace_id?: string
  route_rule_id?: string
}

export interface DownstreamOAuthStatusResponse {
  entries: DownstreamOAuthStatusEntry[]
}

export interface ServerCacheConfig {
  enabled?: boolean
  read_ttl_sec?: number
  cacheable_patterns?: string[]
  no_cacheable_patterns?: string[]
  mutation_patterns?: string[]
  max_entries?: number
}

export interface DownstreamServer {
  id: string
  name: string
  transport: 'stdio' | 'http' | 'internal'
  command: string
  args: string[]
  url: string | null
  tool_namespace: string
  capabilities_cache: Record<string, unknown>
  cache_config?: ServerCacheConfig
  idle_timeout_sec: number
  max_instances: number
  restart_policy: string
  disabled: boolean
  source: string
  created_at: string
  updated_at: string
}

export type ServerCategory =
  | 'dev'
  | 'productivity'
  | 'data'
  | 'cloud'
  | 'observability'
  | 'search'
  | 'core'
  | 'comms'

export type ServerAuth = 'none' | 'api-key' | 'oauth' | 'config'

export interface CatalogEntry {
  id: string
  name: string
  description: string
  category: ServerCategory
  tags: string[]
  auth: ServerAuth
  transport: 'stdio' | 'http' | 'internal'
  command?: string
  args?: string[] | null
  url?: string | null
  tool_namespace: string
  discovery: string
  idle_timeout_sec: number
  max_instances: number
  restart_policy: string
  disabled?: boolean
  recipes?: Array<{ id: string; label: string; description: string; scopes?: string[] }>
}

export interface CatalogResponse {
  entries: CatalogEntry[]
  source: string
  fetched_at: string
}

export interface RouteRule {
  id: string
  name: string
  priority: number
  workspace_id: string
  path_glob: string
  tool_match: string[]
  scope_policy: Record<string, string[]>
  downstream_server_id: string
  auth_scope_id: string
  policy: 'allow' | 'deny'
  log_level: string
  approval_mode: 'none' | 'write' | 'all'
  approval_timeout: number
  created_at: string
  updated_at: string
}

export interface AuditRecord {
  id: string
  timestamp: string
  session_id: string
  client_type: string
  model: string
  workspace_id: string
  workspace_name: string
  subpath: string
  tool_name: string
  params_redacted: Record<string, unknown>
  route_rule_id: string
  downstream_server_id: string
  downstream_instance_id: string
  auth_scope_id: string
  // "ok" is what the secrets resolver + some built-in emitters write; the
  // rest of the gateway writes "success". Treat both as success via
  // normalizeStatus() in @/lib/audit-semantics — never compare === 'success'.
  status: 'success' | 'ok' | 'error' | 'blocked'
  error_code: string
  error_message: string
  latency_ms: number
  response_size: number
  cache_hit: boolean
  execution_id?: string
  route_rule_summary?: string
  downstream_server_name?: string
  // Attribution — populated by the Go AuditRecord (store/models.go) but
  // historically not surfaced in the UI. actor_kind/actor_id is the
  // categorical "who" (user, worker, scheduler, secrets, …). correlation_id
  // joins every row produced by one logical operation, so a secret.read can
  // be traced back to the agent/worker that triggered it (when recorded).
  actor_kind?: string
  actor_id?: string
  correlation_id?: string
  skill_id?: string
}

export interface AuditFilter {
  id?: string // exact match — used by drawer deep-link fallback
  workspace_id?: string
  tool_name?: string
  status?: 'success' | 'error' | 'blocked'
  execution_id?: string
  session_id?: string
  after?: string
  before?: string
  limit?: number
  offset?: number
}

export interface AuditStats {
  total_requests: number
  success_count: number
  error_count: number
  blocked_count: number
  avg_latency_ms: number
  p95_latency_ms: number
}

export interface TimeSeriesPoint {
  bucket: string
  sessions: number
  servers: number
  total: number
  errors: number
  avg_latency_ms: number
  /** Mesh messages whose created_at fell in this bucket. */
  mesh_messages: number
}

export interface ToolLeaderboardEntry {
  tool_name: string
  server_name: string
  call_count: number
  error_count: number
  error_rate: number
  avg_latency_ms: number
  p95_latency_ms: number
}

export interface ServerHealthEntry {
  server_id: string
  server_name: string
  call_count: number
  error_count: number
  error_rate: number
  avg_latency_ms: number
  p95_latency_ms: number
}

export interface ErrorBreakdownEntry {
  group_key: string
  server_name: string
  error_type: 'error' | 'blocked'
  count: number
}

export interface RouteHitEntry {
  route_rule_id: string
  rule_name: string
  path_glob: string
  hit_count: number
  error_count: number
}

export interface ApprovalMetrics {
  pending_count: number
  approved_count: number
  denied_count: number
  timed_out_count: number
  avg_wait_ms: number
}

export interface SessionInfo {
  id: string
  client_type: string
  client_pid: number | null
  connected_at: string
  disconnected_at: string | null
  workspace_id: string | null
  model_hint: string
}

export interface CacheLayerStats {
  hits: number
  misses: number
  evictions: number
  entries: number
  hit_rate: number
}

export interface CacheStats {
  tool_call: CacheLayerStats
  route_resolution: CacheLayerStats
}

export interface DashboardData {
  active_sessions: number
  active_session_list: SessionInfo[]
  active_downstreams: DownstreamStatus[]
  recent_errors: AuditRecord[]
  recent_calls: AuditRecord[]
  stats: AuditStats | null
  timeseries: TimeSeriesPoint[]
  tool_leaderboard: ToolLeaderboardEntry[]
  server_health: ServerHealthEntry[]
  error_breakdown: ErrorBreakdownEntry[]
  route_hit_map: RouteHitEntry[]
  approval_metrics: ApprovalMetrics | null
  cache_stats: CacheStats | null
  /** Paired peers with last_seen within the recency window (currently 5 min). */
  peers_online: number
  /** Total paired peers, including offline. */
  peers_total: number
  /** Mesh-message count over the dashboard's selected range. */
  mesh_messages: number
  /** Most-recent tools/list outcome per downstream — feeds the Server Performance panel. */
  server_timings: ServerTiming[]
  /** Count of delegations with review_required=true that have not yet been reviewed by parent. Dashboard attention / sweep metric. */
  unreviewed_delegations?: number
}

export type ServerTimingStatus = 'ok' | 'slow' | 'timeout' | 'error'

export interface ServerTiming {
  server_id: string
  server_name: string
  status: ServerTimingStatus
  elapsed_ms: number
  at: string
}

export interface DownstreamStatus {
  server_id: string
  server_name: string
  instance_count: number
  state: string
  disabled: boolean
}

export interface DryRunRequest {
  workspace_id: string
  subpath: string
  tool_name: string
}

export interface DryRunAuthScope {
  id: string
  name: string
  type: string
  oauth_status: string // "valid", "expired", "none", "not_applicable"
  expires_at: string | null
}

export interface DryRunResult {
  matched: boolean
  policy: string
  matched_rule: RouteRule | null
  downstream_server: DownstreamServer | null
  auth_scope_id: string
  auth_scope: DryRunAuthScope | null
  candidate_rules: RouteRule[]
}

export interface PaginatedResponse<T> {
  data: T[]
  total: number
}

export interface ToolApproval {
  id: string
  status: 'pending' | 'approved' | 'denied' | 'timeout' | 'cancelled'
  request_session_id: string
  request_client_type: string
  request_model: string
  workspace_id: string
  workspace_name: string
  tool_name: string
  arguments: string
  justification: string
  route_rule_id: string
  downstream_server_id: string
  auth_scope_id: string
  approver_session_id: string
  approver_type: string
  resolution: string
  timeout_sec: number
  // Surface identifies which Guard raised the approval (shell, schedule,
  // mcp, network, sanitizer). Older approvals (pre-Guards) omit it and
  // the gateway reads "" as "mcp" for back-compat.
  surface?: string
  // OriginatingWorkspace — which workspace produced this approval.
  // Distinct from workspace_id (the *target* of the routed call). Set
  // by cross-boundary shares (skill_share / memory_share / task_offer /
  // mesh_direct / mesh_grant_consent). Omitted on legacy tool-call
  // rows. Added in migration 081 (BUG-ENV approval envelope schema).
  originating_workspace?: string
  // Kind classifies cross-boundary share approvals so the UI can pick a
  // renderer. One of: skill_share, memory_share, task_offer,
  // mesh_direct, mesh_grant_consent. Omitted on legacy tool-call rows
  // (the UI falls back to surface/tool_name in that case). Added in
  // migration 081.
  kind?: string
  // Summary — short human-readable preview of the share content (skill
  // name, memory title, task title, message head, or "Granted X to peer
  // Y" for mesh_grant_consent). Distinct from justification (the
  // agent's "why"). Secrets are expected to be redacted upstream. Added
  // in migration 081.
  summary?: string
  created_at: string
  resolved_at: string | null
}

export interface ApprovalEvent {
  type: 'pending' | 'resolved'
  approval: ToolApproval
}

export interface MCPClient {
  id: string
  name: string
  config_path: string
  detected: boolean
  configured: boolean
  // hooks_installed is populated only by the Shell Guard detail endpoint
  // (GET /api/v1/guards/shell). It reflects whether the PreToolUse curl
  // hook is wired in this client's settings.json — distinct from
  // `configured`, which only reflects MCP-server registration. Optional
  // because the generic install endpoint doesn't include it.
  hooks_installed?: boolean
  // hooks_drifted is set true when hooks_installed=true but the gateway
  // re-read the underlying settings.json and the mcplexer endpoint
  // substring is no longer present (rules sync overwrote it, the user
  // edited it, another tool replaced the file). The dashboard surfaces
  // this as a red "Hook drifted — re-install to repair" badge. Optional
  // because endpoints that don't run the reconciler omit it.
  hooks_drifted?: boolean
  // hooks_drift_error carries a human-readable parse-error message
  // when settings.json existed but couldn't be parsed (corrupt JSON,
  // unexpected shape). Empty / undefined when no parse error.
  hooks_drift_error?: string
}

export interface MCPInstallStatus {
  clients: MCPClient[]
  binary_path: string
  server_entry: Record<string, unknown>
}

export interface MCPInstallPreview {
  config_path: string
  content: string
}

export interface ApiError {
  error: string
  code?: string
}

export interface Settings {
  slim_tools: boolean
  slim_surface: boolean
  compact_responses: boolean
  tools_cache_ttl_sec: number
  log_level: string
  code_mode_timeout_sec: number
  code_mode_max_output_bytes: number
  mesh_enabled: boolean
  mesh_receive_max_results: number
  mesh_receive_preview_bytes: number
  mesh_send_max_content_bytes: number
  // display_name is the user-visible label for THIS device shown on
  // paired peers' lists + as the "from" name on cross-machine mesh rows.
  // NOT auth-bearing — the Go side treats it as a UX hint only.
  display_name: string
  description_refinement_mode: 'off' | 'manual' | 'auto'
  tool_description_overrides: Record<string, string>
  // dangerous_mode_enabled — global escape hatch that disables every
  // approval gate. When on, the app chrome takes on a muted red wash
  // and a persistent banner reminds the user. Audit trail keeps
  // recording so a follow-up review can answer "what was I blocked
  // on?". Server-persisted; sticky across reloads. NOT per-workspace.
  dangerous_mode_enabled: boolean
  // delegation_disabled_providers — operator switches for whole
  // provider/subscription groups (opencode, local, claude, grok,
  // openrouter, minimax, and raw provider ids). When a key is true,
  // that group is excluded from delegation capacity, ranked choices, and routing.
  // Backend-owned via /settings; survives page reloads.
  delegation_disabled_providers?: Record<string, boolean>
  // remote_skill_server_url — central skills registry/hub base URL.
  // The backend accepts bare DNS names such as "shared-skills" and
  // normalizes them to the scheme default unless a port is provided.
  remote_skill_server_url?: string
  // auto_update_bootstrap — when true (default), the harness setup page
  // automatically triggers a bootstrap reinstall when version drift is
  // detected, instead of requiring a manual click.
  auto_update_bootstrap: boolean
  // shell_guard_allow_chaining — when true (default), the PreToolUse shell
  // hook stops hard-blocking command-chaining metacharacters (; | && || &
  // backtick, chaining newlines) and substitutions; such commands fall
  // through to the normal approval + audit path instead. The protected-path
  // / secret guard (~/.mcplexer) always runs first regardless, so allowing
  // chaining never opens a hole to the gateway's own state. Set false to
  // restore the historical hard-block. Optional on the wire — legacy rows
  // and older servers omit it and the backend backfills the default.
  shell_guard_allow_chaining?: boolean
}

export interface ToolDescriptionVersion {
  id: string
  tool_name: string
  description: string
  source: 'model' | 'manual' | 'original'
  status: 'pending' | 'active' | 'rejected' | 'superseded'
  session_id: string
  model: string
  workspace_id: string
  rationale: string
  reviewed_by: string
  review_note: string
  created_at: string
  reviewed_at: string | null
}

export interface ToolDescriptionFilter {
  tool_name?: string
  status?: string
  source?: string
  limit?: number
  offset?: number
}

export interface MeshAgent {
  session_id: string
  name: string
  role: string
  client_type: string
  model_hint: string
  // Origin tags how this agent was reached:
  //   "local"            — connected to this daemon's stdio MCP socket
  //   "peer:<peer_id>"   — observed via libp2p from a paired peer
  // Older daemons may omit this field; treat absent as "local".
  origin?: string
  // Free-form persistent status the agent advertised via
  // mesh__set_agent_status. Empty when the agent never set one.
  status?: string
  // Workspace this agent is bound to. Name is server-resolved from the
  // workspaces table when possible. Absent for peer-origin agents whose
  // workspace_id doesn't match any local row.
  workspace_id?: string
  workspace_name?: string
  // Tmux locator — drives the "Focus" action. All three populated when
  // the agent registered from inside a tmux pane. Empty fields ⇒ no
  // locator; UI greys the Focus button.
  tmux_session?: string
  tmux_window?: string
  tmux_pane?: string
  last_seen_at: string
}

export interface MeshMessage {
  id: string
  agent_name: string
  // sender_display_name is captured from the libp2p envelope when the
  // remote peer sent one. UI prefers this over agent_name for cross-
  // machine rows. NOT auth-bearing — see Go-side comments.
  sender_display_name?: string
  kind: string
  priority: string
  content: string
  audience: string
  tags: string
  reply_to: string
  thread_root: string
  reply_count: number
  expires_at: string
  created_at: string
  // M7.3 — repo/branch scope. Optional; absent on pre-M7.3 envelopes.
  repo?: string
  branch?: string
  workspace_path?: string
  // workspace_name is derived server-side from the workspaces table
  // (by root_path) — falls back to the last path component of
  // workspace_path. UI renders it as a badge next to the message.
  workspace_name?: string
}

export interface MeshStatusResponse {
  agents: MeshAgent[]
  messages: MeshMessage[]
  live_messages: number
}

export interface FileClaim {
  claim_id: string
  claimer_user_id?: string
  claimer_peer_id?: string
  claimer_display_name?: string
  repo: string
  branch: string
  paths: string[]
  intent?: string
  claimed_at: string
  expires_at: string
  seconds_remaining: number
}

export interface FileClaimsResponse {
  claims: FileClaim[]
  total: number
}

export interface SettingsResponse {
  settings: Settings
  builtin_tool_defaults: Record<string, string>
}

export interface User {
  user_id: string
  display_name: string
  created_at: string
  is_self: boolean
}

export interface P2PPeer {
  peer_id: string
  display_name: string
  paired_at: string
  last_seen?: string
  trust_level: number
  scopes?: string[]
  revoked_at?: string
  ssh_target?: string
  secret_transfer_recipient?: string
}

export interface UsersResponse {
  users: User[]
}

export interface UserWithPeers extends User {
  peers: P2PPeer[]
}

// P2P debug — exposed by GET /api/p2p/peers (not /api/v1/...).
// Mode strings come from internal/p2p/connmode.go: ModeDirect, ModeHolePunched,
// ModeRelay, or ModeNone — kept loosely-typed (string) here so we render new
// states without a UI release if the server adds them.
export interface P2PPeerMode {
  peer: string
  mode: 'direct' | 'hole-punched' | 'via-relay' | 'none' | string
}

export interface P2PPeersResponse {
  peers: P2PPeerMode[]
}

export interface P2PIdentityResponse {
  peer_id: string
  multiaddrs: string[]
}

export type HarnessKey = 'claude' | 'codex' | 'opencode' | 'gemini' | 'grok' | 'mimo' | 'pi'

export interface HarnessSetupRow {
  key: HarnessKey
  mcp_wired: boolean
  config_path: string
  last_initialize_at: string | null
  client_info: string | null
  bootstrap_installed: boolean
  bootstrap_version: number | null
  registry_version: number
  drifted: boolean
  accretion?: {
    extra_skills?: string[]
    extra_commands?: string[]
  }
}

export interface HarnessSetupStatusResponse {
  harnesses: HarnessSetupRow[]
}

export interface HarnessSetupError {
  error: {
    code: string
    message: string
    hint: string
  }
}
