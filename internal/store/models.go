package store

import (
	"encoding/json"
	"strings"
	"time"
)

// Workspace represents a workspace context for routing.
type Workspace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path"`
	// ParentID is the optional client/org parent workspace (migration 092,
	// docs/brain.md Appendix C.1). Empty = a root workspace (today's
	// behaviour). A child's recall/list scope fuses with its parent's.
	ParentID      string          `json:"parent_id,omitempty"`
	Tags          json.RawMessage `json:"tags,omitempty"`
	DefaultPolicy string          `json:"default_policy"`
	Source        string          `json:"source"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// AuthScope represents a credential scope for downstream server authentication.
//
// Name is the stable, externally-referenced handle (unique, used by
// route_rules.auth_scope_id wiring and the secrets store). DisplayName
// is presentation-only — empty string means "no operator-friendly name
// configured", and the UI falls back to a humanised form of Name.
type AuthScope struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	DisplayName     string          `json:"display_name"`
	Type            string          `json:"type"`
	EncryptedData   []byte          `json:"-"`
	RedactionHints  json.RawMessage `json:"redaction_hints,omitempty"`
	OAuthProviderID string          `json:"oauth_provider_id"`
	OAuthTokenData  []byte          `json:"-"`
	Source          string          `json:"source"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// OAuthProvider stores OAuth2 app configuration.
type OAuthProvider struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	TemplateID            string          `json:"template_id"`
	AuthorizeURL          string          `json:"authorize_url"`
	TokenURL              string          `json:"token_url"`
	ClientID              string          `json:"client_id"`
	EncryptedClientSecret []byte          `json:"-"`
	Scopes                json.RawMessage `json:"scopes,omitempty"`
	UsePKCE               bool            `json:"use_pkce"`
	RedirectURI           string          `json:"redirect_uri,omitempty"`
	Source                string          `json:"source"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

// OAuthTokenData holds decrypted OAuth2 token information.
type OAuthTokenData struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes,omitempty"`
}

// DownstreamServer represents a downstream MCP server configuration.
type DownstreamServer struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Transport         string          `json:"transport"`
	Command           string          `json:"command"`
	Args              json.RawMessage `json:"args,omitempty"`
	URL               *string         `json:"url,omitempty"`
	ToolNamespace     string          `json:"tool_namespace"`
	Discovery         string          `json:"discovery"` // "static" or "dynamic"
	CapabilitiesCache json.RawMessage `json:"capabilities_cache,omitempty"`
	CacheConfig       json.RawMessage `json:"cache_config,omitempty"`
	IdleTimeoutSec    int             `json:"idle_timeout_sec"`
	// CallTimeoutSec bounds an individual tools/call dispatch to this
	// downstream. Zero means "use the gateway default"
	// (downstream.DefaultCallTimeout, 120s). The cache-aggregator's
	// per-server tools/list timeout (PerServerListToolsTimeout, 15s)
	// is independent and applies to discovery, not dispatch.
	CallTimeoutSec int       `json:"call_timeout_sec"`
	MaxInstances   int       `json:"max_instances"`
	RestartPolicy  string    `json:"restart_policy"`
	Disabled       bool      `json:"disabled"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// RouteRule represents a routing rule for matching tool calls to downstream servers.
type RouteRule struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Priority           int             `json:"priority"`
	WorkspaceID        string          `json:"workspace_id"`
	PathGlob           string          `json:"path_glob"`
	ToolMatch          json.RawMessage `json:"tool_match,omitempty"`
	ScopePolicy        json.RawMessage `json:"scope_policy,omitempty"`
	DownstreamServerID string          `json:"downstream_server_id"`
	AuthScopeID        string          `json:"auth_scope_id"`
	Policy             string          `json:"policy"`
	LogLevel           string          `json:"log_level"`
	ApprovalMode       string          `json:"approval_mode"`
	ApprovalTimeout    int             `json:"approval_timeout"`
	Source             string          `json:"source"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// Session represents an active or past MCP client session.
type Session struct {
	ID             string     `json:"id"`
	ClientType     string     `json:"client_type"`
	ClientPID      *int       `json:"client_pid,omitempty"`
	ConnectedAt    time.Time  `json:"connected_at"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
	WorkspaceID    *string    `json:"workspace_id,omitempty"`
	ModelHint      string     `json:"model_hint"`
}

// AuditRecord represents a single audit log entry.
type AuditRecord struct {
	ID                   string          `json:"id"`
	Timestamp            time.Time       `json:"timestamp"`
	SessionID            string          `json:"session_id"`
	ClientType           string          `json:"client_type"`
	Model                string          `json:"model"`
	WorkspaceID          string          `json:"workspace_id"`
	WorkspaceName        string          `json:"workspace_name"`
	Subpath              string          `json:"subpath"`
	ToolName             string          `json:"tool_name"`
	ParamsRedacted       json.RawMessage `json:"params_redacted,omitempty"`
	RouteRuleID          string          `json:"route_rule_id"`
	DownstreamServerID   string          `json:"downstream_server_id"`
	DownstreamInstanceID string          `json:"downstream_instance_id"`
	AuthScopeID          string          `json:"auth_scope_id"`
	Status               string          `json:"status"`
	ErrorCode            string          `json:"error_code,omitempty"`
	ErrorMessage         string          `json:"error_message,omitempty"`
	LatencyMs            int             `json:"latency_ms"`
	ResponseSize         int             `json:"response_size"`
	CacheHit             bool            `json:"cache_hit"`
	ExecutionID          string          `json:"execution_id,omitempty"`
	// SkillID is set when the audit row was produced by a tool call dispatched
	// from a skill context. nil means "non-skill call" (legacy / direct).
	SkillID   *string   `json:"skill_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// ActorKind is the categorical "who" — user, worker, scheduler, api,
	// mesh, secrets, worker_admin. Mirrors the existing ClientType
	// conventions; emit sites populate it. Backfilled on migration 053
	// from client_type + session_id patterns; new rows must supply it.
	ActorKind string `json:"actor_kind,omitempty"`
	// ActorID is the specific identifier inside that kind — worker_id,
	// run_id, peer_id, user_id, scope_id, etc. Pairs with ActorKind for
	// the (actor_kind, actor_id, timestamp DESC) index.
	ActorID string `json:"actor_id,omitempty"`
	// CorrelationID joins every slog line and audit row produced by one
	// logical operation (HTTP request, worker run, MCP call, scheduler
	// tick). audit.Logger.Record auto-populates from audit.FromCtx(ctx)
	// when callers don't stamp it explicitly; also folded into
	// ParamsRedacted JSON as a defensive fallback.
	CorrelationID string `json:"correlation_id,omitempty"`

	// Tier is the trust-tier classification of a cross-boundary share
	// (epic 01KSK91Q4W8TNED9MAF0CTRVKC). One of "same_user", "same_org",
	// "cross_org". Empty for in-process / non-cross-boundary rows.
	// Populated by emit sites in skill_share, memory_share, task_share,
	// and peer-addressed mesh__send.
	Tier string `json:"tier,omitempty"`

	// AcceptedBy is the consent envelope that authorized the share.
	// JSON object:
	//   Tier 1 → {"kind":"auto_pair"}
	//   Tier 2/3 → {"kind":"human","user_id":...,"agent_id":...,"timestamp":...}
	// Empty for non-cross-boundary rows. Stored as raw JSON so the
	// shape can evolve without further migrations.
	AcceptedBy json.RawMessage `json:"accepted_by,omitempty"`

	// GrantOrigin references the scope grant that authorized a Tier 2/3
	// explicit-grant share. JSON object: {"peer_id":...,"agent_id":...,
	// "grant_id":...}. Empty on Tier 1 (no explicit grant) and on
	// pre-grant denial rows.
	GrantOrigin json.RawMessage `json:"grant_origin,omitempty"`

	// DenialReason is a short stable code attached to rejection rows
	// (e.g. "scope_revoked", "cross_org_no_grant", "not_paired"). Lets
	// ops + UI filter without string-matching error_message. Empty
	// when status=success.
	DenialReason string `json:"denial_reason,omitempty"`

	// Enriched fields for UI
	RouteRuleSummary     string `json:"route_rule_summary,omitempty"`
	DownstreamServerName string `json:"downstream_server_name,omitempty"`
}

// SkillInvocation records a single tool call attempted by a skill, including
// allow/deny outcome. Persisted independently of audit_records so the UI can
// show a per-skill activity feed without joining on JSON columns.
type SkillInvocation struct {
	ID        int64     `json:"id"`
	SkillName string    `json:"skill_name"`
	ToolName  string    `json:"tool_name"`
	Namespace string    `json:"namespace"`
	Allowed   bool      `json:"allowed"`
	Timestamp time.Time `json:"ts"`
}

// AuditFilter specifies query parameters for listing audit records.
type AuditFilter struct {
	ID          *string    `json:"id,omitempty"` // exact match — used for deep-link drawer fetches
	SessionID   *string    `json:"session_id,omitempty"`
	WorkspaceID *string    `json:"workspace_id,omitempty"`
	ToolName    *string    `json:"tool_name,omitempty"`
	Status      *string    `json:"status,omitempty"`
	ExecutionID *string    `json:"execution_id,omitempty"`
	After       *time.Time `json:"after,omitempty"`
	Before      *time.Time `json:"before,omitempty"`

	// Richer exact-match filters (audit overhaul). All optional —
	// nil/empty means "no constraint on that dimension".
	ActorKind          *string `json:"actor_kind,omitempty"`
	ActorID            *string `json:"actor_id,omitempty"`
	DownstreamServerID *string `json:"downstream_server_id,omitempty"`
	RouteRuleID        *string `json:"route_rule_id,omitempty"`
	ClientType         *string `json:"client_type,omitempty"`
	ErrorCode          *string `json:"error_code,omitempty"`
	Tier               *string `json:"tier,omitempty"`
	CacheHit           *bool   `json:"cache_hit,omitempty"`
	MinLatencyMs       *int    `json:"min_latency_ms,omitempty"`

	// Q is a free-text FTS5 query. When non-empty it AND-restricts the
	// result set to rows matching the audit_records_fts index (sanitised
	// before MATCH). Ordering/pagination are unaffected — still
	// timestamp/sort-driven + keyset/offset paged.
	Q string `json:"q,omitempty"`

	// Sort selects the ordering. One of "time_desc" (default), "time_asc",
	// "latency_desc", "latency_asc". Validated against an allowlist; an
	// unknown value falls back to time_desc.
	Sort string `json:"sort,omitempty"`

	// CursorTs/CursorID carry an opaque keyset cursor ("<RFC3339Nano>|<id>").
	// When CursorTs is non-nil, offset is ignored and the page advances by
	// (timestamp,id) strictly past the cursor in the sort direction.
	CursorTs *time.Time `json:"-"`
	CursorID string     `json:"-"`

	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// AuditAlert is a deterministic, locally-computed anomaly or security
// finding over the audit log. No external LLM — every alert is a
// threshold crossing the store can explain. Filter is a deep-link subset
// of AuditFilter fields so the UI can jump straight to the matching rows.
type AuditAlert struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`     // "anomaly" | "security"
	Severity    string  `json:"severity"` // "info" | "warning" | "critical"
	Title       string  `json:"title"`
	Detail      string  `json:"detail"`
	ToolName    string  `json:"tool_name,omitempty"`
	WorkspaceID string  `json:"workspace_id,omitempty"`
	Count       int     `json:"count"`
	Metric      float64 `json:"metric,omitempty"`
	Baseline    float64 `json:"baseline,omitempty"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// Filter is the deep-link AuditFilter subset (e.g.
	// {"tool_name":"x","status":"error"}) the UI uses to navigate to the
	// matching audit rows. Serialised as a JSON object.
	Filter map[string]any `json:"filter,omitempty"`
}

// SavedSearch is a persisted audit query that the saved-search evaluator
// turns into a notification when its match count over a rolling window
// crosses threshold_count. Filter is a JSON object of AuditFilter fields.
type SavedSearch struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Q              string         `json:"q"`
	Filter         map[string]any `json:"filter"`
	ThresholdCount int            `json:"threshold_count"`
	WindowSec      int            `json:"window_sec"`
	WorkspaceID    string         `json:"workspace_id"`
	Enabled        bool           `json:"enabled"`
	LastFiredAt    *time.Time     `json:"last_fired_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// AuditSearchResult bundles a ranked record list with the tier that
// actually produced it: "vector" (query-time embedding rerank), "tfidf"
// (local TF-IDF over the FTS candidate pool), or "fts" (lexical/recency
// fallback when the query has no usable terms).
type AuditSearchResult struct {
	Records []AuditRecord `json:"data"`
	Mode    string        `json:"mode"`
}

// TimeSeriesPoint holds minute-bucketed aggregate metrics.
type TimeSeriesPoint struct {
	Bucket       time.Time `json:"bucket"`
	Sessions     int       `json:"sessions"`
	Servers      int       `json:"servers"`
	Total        int       `json:"total"`
	Errors       int       `json:"errors"`
	AvgLatencyMs float64   `json:"avg_latency_ms"`
	// MeshMessages is the count of mesh_messages.created_at falling in this
	// bucket. Surfaced on the dashboard as the "mesh activity" sparkline.
	MeshMessages int `json:"mesh_messages"`
}

// AuditStats holds aggregate statistics for audit records.
type AuditStats struct {
	TotalRequests int     `json:"total_requests"`
	SuccessCount  int     `json:"success_count"`
	ErrorCount    int     `json:"error_count"`
	BlockedCount  int     `json:"blocked_count"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	P95LatencyMs  int     `json:"p95_latency_ms"`
}

// ToolLeaderboardEntry holds per-tool aggregate metrics for the dashboard.
type ToolLeaderboardEntry struct {
	ToolName     string  `json:"tool_name"`
	ServerName   string  `json:"server_name"`
	CallCount    int     `json:"call_count"`
	ErrorCount   int     `json:"error_count"`
	ErrorRate    float64 `json:"error_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P95LatencyMs int     `json:"p95_latency_ms"`
}

// ServerHealthEntry holds per-server aggregate metrics for the dashboard.
type ServerHealthEntry struct {
	ServerID     string  `json:"server_id"`
	ServerName   string  `json:"server_name"`
	CallCount    int     `json:"call_count"`
	ErrorCount   int     `json:"error_count"`
	ErrorRate    float64 `json:"error_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P95LatencyMs int     `json:"p95_latency_ms"`
}

// ErrorBreakdownEntry holds per-tool error counts for the dashboard.
type ErrorBreakdownEntry struct {
	GroupKey   string `json:"group_key"`
	ServerName string `json:"server_name"`
	ErrorType  string `json:"error_type"` // "error" or "blocked"
	Count      int    `json:"count"`
}

// RouteHitEntry holds per-route hit/error counts for the dashboard.
type RouteHitEntry struct {
	RouteRuleID string `json:"route_rule_id"`
	RuleName    string `json:"rule_name"`
	PathGlob    string `json:"path_glob"`
	HitCount    int    `json:"hit_count"`
	ErrorCount  int    `json:"error_count"`
}

// AuditCacheStats holds cache hit/miss counts derived from audit records.
type AuditCacheStats struct {
	Hits    int     `json:"hits"`
	Misses  int     `json:"misses"`
	HitRate float64 `json:"hit_rate"`
}

// ApprovalMetrics holds aggregate approval statistics for the dashboard.
type ApprovalMetrics struct {
	PendingCount  int     `json:"pending_count"`
	ApprovedCount int     `json:"approved_count"`
	DeniedCount   int     `json:"denied_count"`
	TimedOutCount int     `json:"timed_out_count"`
	AvgWaitMs     float64 `json:"avg_wait_ms"`
}

// MeshMessage represents a message in the agent mesh.
//
// SenderDisplayName is captured from incoming libp2p envelopes when the
// remote peer sends it. NOT auth-bearing — for display only. The trust
// anchor is still the libp2p PeerID (carried in SessionID for cross-machine
// rows) and the envelope signature.
type MeshMessage struct {
	ID                string    `json:"id"` // ULID
	WorkspaceID       string    `json:"workspace_id"`
	SessionID         string    `json:"session_id"`
	AgentName         string    `json:"agent_name"`
	SenderDisplayName string    `json:"sender_display_name,omitempty"`
	Kind              string    `json:"kind"`     // finding|task|alert|question|result|event|reply
	Priority          string    `json:"priority"` // critical|high|normal|low
	Content           string    `json:"content"`
	Audience          string    `json:"audience"` // "*", role name, or session ID
	Tags              string    `json:"tags"`     // comma-separated
	ReplyTo           string    `json:"reply_to"`
	ThreadRoot        string    `json:"thread_root"` // computed: root of thread chain
	ReplyCount        int       `json:"reply_count"`
	Status            string    `json:"status"` // live|archived
	ExpiresAt         time.Time `json:"expires_at"`
	CreatedAt         time.Time `json:"created_at"`
	// M7.3 — repo + branch scoping for cross-machine team coordination.
	Repo          string `json:"repo,omitempty"`
	Branch        string `json:"branch,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	RepoRemote    string `json:"repo_remote,omitempty"`

	// Phase-2 tasks plumbing. ActorKind identifies who emitted the
	// message — empty / "agent" is the default for live-session sends,
	// "worker" tags scheduled in-process agent loops, "user" tags REST
	// callers (e.g. the dashboard), "peer-import" tags inbound libp2p
	// ingest, "system" tags daemon-internal sends. Drives the
	// notify-suppression gate on task_event:assigned/closed/...
	ActorKind string `json:"actor_kind,omitempty"`
}

// MeshAgent represents an agent registered in the mesh.
//
// Origin tags where this agent was discovered:
//   - "local"          — connected via this daemon's stdio MCP socket
//   - "peer:<peer_id>" — observed over libp2p from a paired peer
//
// Default is "local" so historical rows + slim builds (no p2p) keep the
// pre-origin behaviour.
type MeshAgent struct {
	SessionID   string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	ClientType  string `json:"client_type"`
	ModelHint   string `json:"model_hint"`
	Cursor      string `json:"cursor"` // last-seen message ULID
	Origin      string `json:"origin"`
	// Status is a free-form persistent state string the agent advertises
	// to humans + peers ("building X, ETA 5m" / "idle" / "blocked on Y").
	// Set via mesh__set_agent_status; surfaced in mesh__list_agents +
	// the dashboard. NOT an enum on purpose — agents pick their own
	// words. Empty = no status set yet.
	Status string `json:"status"`
	// Terminal locator (tmux). Populated when the agent registered from
	// inside a tmux pane and sent the optional locator fields on
	// mesh__receive. Drives the "Focus" button on the mesh page — the
	// dashboard runs `tmux switch-client -t <session>:<window>.<pane>`
	// for local agents, and for peer-origin agents it shells out to
	// `ssh -t <p2p_peers.ssh_target> tmux attach ...` inside the user's
	// local tmux. Empty fields = no locator; UI greys the button.
	TmuxSession string    `json:"tmux_session,omitempty"`
	TmuxWindow  string    `json:"tmux_window,omitempty"`
	TmuxPane    string    `json:"tmux_pane,omitempty"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// MeshAgentOriginLocal marks agents reached via the local stdio socket.
const MeshAgentOriginLocal = "local"

// MeshAgentOriginPeerPrefix prefixes the libp2p peer ID for remote agents.
// Format: "peer:<peer_id>".
const MeshAgentOriginPeerPrefix = "peer:"

// Recipe captures a mined, reusable tool-call pattern harvested from audit
// records. Used by recipe search surfaces and harvester for cheap-model
// fluency.
type Recipe struct {
	ID             string          `json:"id"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ToolName       string          `json:"tool_name"`
	Namespace      string          `json:"namespace"`
	Description    string          `json:"description"`
	ParamsPattern  json.RawMessage `json:"params_pattern,omitempty"`
	SuccessCount   int             `json:"success_count"`
	TotalCount     int             `json:"total_count"`
	AvgLatencyMs   float64         `json:"avg_latency_ms"`
	ErrorRate      float64         `json:"error_rate"`
	Score          float64         `json:"score"`
	SessionCount   int             `json:"session_count"`
	LastUsedAt     *time.Time      `json:"last_used_at,omitempty"`
	Tags           json.RawMessage `json:"tags,omitempty"`
	SourceAuditIDs json.RawMessage `json:"source_audit_ids,omitempty"`
}

// RecipeFilter narrows recipe queries (list/search).
type RecipeFilter struct {
	Query     string   // FTS query (for SearchRecipes)
	ToolName  *string  // exact match
	Namespace *string  // exact match
	MinScore  *float64 // score >=
	Limit     int
	Offset    int
}

// MeshOutbound is one row in the offline-delivery queue. When a targeted
// `to_peer` send fails because the remote peer is offline / unreachable,
// the mesh manager parks the wire-format envelope here and retries when
// the peer comes back online (or on the 30s background sweep).
//
// Lifecycle: enqueued → next_attempt_at <= now triggers a retry → on
// success delivered_at is stamped + the row pruned a day later; on
// failure attempts++, last_error set, next_attempt_at backed off
// exponentially up to 5 min. expires_at is a hard TTL (default 7d).
type MeshOutbound struct {
	ID                   int64      `json:"id"`
	MessageID            string     `json:"message_id"`
	TargetPeerID         string     `json:"target_peer_id"`
	TargetAgentSessionID string     `json:"target_agent_session_id,omitempty"`
	Envelope             []byte     `json:"-"`
	Attempts             int        `json:"attempts"`
	LastError            string     `json:"last_error,omitempty"`
	EnqueuedAt           time.Time  `json:"enqueued_at"`
	NextAttemptAt        time.Time  `json:"next_attempt_at"`
	DeliveredAt          *time.Time `json:"delivered_at,omitempty"`
	ExpiresAt            time.Time  `json:"expires_at"`
}

// MeshMessageFilter specifies query parameters for listing mesh messages.
type MeshMessageFilter struct {
	WorkspaceIDs []string
	SinceID      string     // cursor-based: messages after this ID
	SinceTime    *time.Time // time-based: messages after this time
	Audience     string     // filter by audience match (session ID)
	AgentRole    string     // the querying agent's role
	Tags         string     // filter by tag
	ThreadRoot   string     // filter by thread
	StatusLive   bool       // only live messages
	// ExcludeSessionID drops messages SENT by this session. Used by the
	// "new for you" paths (receive filter=new, pending count) so an agent's
	// own task_event broadcasts don't perpetually re-trigger its own
	// pending-messages nag.
	ExcludeSessionID string
	Limit            int
	// M7.3 — repo + branch scoped filtering for cross-machine team coords.
	Repos         []string // multi-repo OR (preferred over Repo)
	Repo          string   // single-repo equality
	Branch        string
	WorkspacePath string
	// Kind-level filtering (mesh signal/noise). Kinds whitelists message
	// kinds (IN); ExcludeKinds blacklists (NOT IN). Both empty = all kinds.
	Kinds        []string
	ExcludeKinds []string
	// Actor-kind filtering on the actor_kind column (migration 065):
	// "agent" | "worker" | "user" | "peer-import" | "system". Lets
	// consumers hide worker-origin chatter without parsing content.
	ActorKinds        []string
	ExcludeActorKinds []string
	// OrderRecent, when true, orders results strictly by recency
	// (ORDER BY id DESC — id is a ULID, so lexicographic order is time
	// order) instead of the default priority-first ordering. Use this
	// for "recent conversation" windows where the most recent N messages
	// matter regardless of priority (e.g. the telegram-responder's
	// mesh_history): with priority-first ordering, low/normal-priority
	// agent-outbound rows are pushed below high-priority inbound traffic
	// and never make the window. Default (false) preserves the existing
	// priority_order(priority), id DESC ordering for every other caller.
	OrderRecent bool
}

// ToolDescriptionVersion represents a versioned tool description suggestion.
type ToolDescriptionVersion struct {
	ID          string     `json:"id"`
	ToolName    string     `json:"tool_name"`
	Description string     `json:"description"`
	Source      string     `json:"source"` // model, manual, original
	Status      string     `json:"status"` // pending, active, rejected, superseded
	SessionID   string     `json:"session_id"`
	Model       string     `json:"model"`
	WorkspaceID string     `json:"workspace_id"`
	Rationale   string     `json:"rationale"`
	ReviewedBy  string     `json:"reviewed_by"`
	ReviewNote  string     `json:"review_note"`
	CreatedAt   time.Time  `json:"created_at"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
}

// ToolDescriptionFilter specifies query parameters for listing description versions.
type ToolDescriptionFilter struct {
	ToolName *string
	Status   *string
	Source   *string
	Limit    int
	Offset   int
}

// TelegramChat is a chat bound to MCPlexer via a bridge adapter (Telegram, Google Chat, ...).
type TelegramChat struct {
	ID           string    `json:"id"`
	Platform     string    `json:"platform"`
	NativeChatID string    `json:"native_chat_id"`
	ChatType     string    `json:"chat_type"`
	Title        string    `json:"title"`
	WorkspaceID  string    `json:"workspace_id"`
	SessionID    string    `json:"session_id"`
	MinPriority  string    `json:"min_priority"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// TelegramPairing is a short-lived code that binds a chat to a workspace.
type TelegramPairing struct {
	Code               string    `json:"code"`
	Platform           string    `json:"platform"`
	WorkspaceID        string    `json:"workspace_id"`
	CreatedBySessionID string    `json:"created_by_session_id"`
	ExpiresAt          time.Time `json:"expires_at"`
	CreatedAt          time.Time `json:"created_at"`
}

// TelegramSentMessage maps a native platform message back to the mesh message it
// delivered, so that a native reply can be threaded correctly.
type TelegramSentMessage struct {
	ID              string    `json:"id"`
	Platform        string    `json:"platform"`
	NativeChatID    string    `json:"native_chat_id"`
	NativeMessageID string    `json:"native_message_id"`
	MeshMessageID   string    `json:"mesh_message_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// GoogleChatSpace is a Google Chat space (DM / group / Workspace "space")
// bound to MCPlexer. Sibling to TelegramChat — the model is intentionally
// platform-specific so we never have to retrofit telegram-only columns onto
// Google Chat or vice-versa.
type GoogleChatSpace struct {
	ID          string    `json:"id"`
	SpaceName   string    `json:"space_name"` // e.g. "spaces/AAAA..."
	Title       string    `json:"title"`
	SpaceType   string    `json:"space_type"` // "dm" | "group" | "space"
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id"`
	MinPriority string    `json:"min_priority"`
	ListenMode  string    `json:"listen_mode"` // "mention" | "all"
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
}

// GoogleChatPairing is a short-lived code that binds a space to a workspace.
type GoogleChatPairing struct {
	Code               string    `json:"code"`
	WorkspaceID        string    `json:"workspace_id"`
	CreatedBySessionID string    `json:"created_by_session_id"`
	ExpiresAt          time.Time `json:"expires_at"`
	CreatedAt          time.Time `json:"created_at"`
}

// GoogleChatSentMessage maps a native Google Chat message back to the mesh
// message it delivered, so a threaded reply can be tracked back to the
// originating mesh thread.
type GoogleChatSentMessage struct {
	ID              string    `json:"id"`
	SpaceName       string    `json:"space_name"`
	ThreadName      string    `json:"thread_name"`
	NativeMessageID string    `json:"native_message_id"`
	MeshMessageID   string    `json:"mesh_message_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// TrustedSigner represents an entry in the local skill-signer trust store
// (ADR 0002). PubkeyID is the 16-char uppercase hex form of minisign's
// 64-bit key id; PubkeyString is the canonical 56-char `RWR…` form.
// A non-nil RevokedAt means the signer is revoked: new installs must be
// refused and already-installed skills must surface a warning.
type TrustedSigner struct {
	PubkeyID     string     `json:"pubkey_id"`
	PubkeyString string     `json:"pubkey_string"`
	Name         string     `json:"name"`
	AddedAt      time.Time  `json:"added_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// InstalledSkill is one row in the installed-skills registry (M2.2).
//
// ManifestJSON is the full canonical manifest serialised as JSON — the
// installer parses the bundle's manifest.toml, validates it, and writes
// the JSON form here so callers don't need to re-read the on-disk file.
//
// SignerPubkey is the 56-char canonical minisign public key string of
// the signer (empty when installed unsigned via an explicit override).
// Source records the install origin: "file:<abs-path>" or "https://...".
type InstalledSkill struct {
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	ManifestJSON json.RawMessage `json:"manifest"`
	SignerPubkey string          `json:"signer_pubkey,omitempty"`
	Source       string          `json:"source,omitempty"`
	InstalledAt  time.Time       `json:"installed_at"`
}

// SkillRegistryEntry is one published version of a skill in the agent-
// facing skills registry (migration 037).
//
// Distinct from InstalledSkill: that table tracks locally installed,
// minisign-signed .mcskill bundles (ADR 0002). SkillRegistryEntry rows
// are pure SKILL.md text agents can search, fetch, and publish through
// the mcpx__skill_* MCP tools.
//
// Versioning is linear monotonic per Name. ContentHash (sha256 of Body)
// is the dedup key — re-publishing identical content returns the existing
// row. ParentVersion records the edit lineage.
//
// `@latest` is NOT a tag row — it's MAX(Version) WHERE DeletedAt IS NULL.
// `@stable` and other custom labels live in skill_registry_tags.
type SkillRegistryEntry struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Version          int             `json:"version"`
	ContentHash      string          `json:"content_hash"`
	Description      string          `json:"description"`
	Body             string          `json:"body"`
	MetadataJSON     json.RawMessage `json:"metadata,omitempty"`
	TagsJSON         json.RawMessage `json:"tags,omitempty"`
	Author           string          `json:"author,omitempty"`
	ParentVersion    *int            `json:"parent_version,omitempty"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
	PublishedAt      time.Time       `json:"published_at"`
	CreatedByAgentID string          `json:"created_by_agent_id,omitempty"`

	// WorkspaceID nil = global; non-nil = pinned to one workspace.
	// Mirrors route_rules scoping. Search/list scope by (current workspace ∪ global).
	WorkspaceID *string `json:"workspace_id,omitempty"`

	// SourceType: "inline" (body is the full SKILL.md), "path" (bundle on
	// disk at SourcePath, body mirrors SKILL.md for search), "bundle"
	// (tar.gz blob in Bundle, body mirrors SKILL.md), "git" (reserved).
	SourceType string `json:"source_type,omitempty"`
	SourcePath string `json:"source_path,omitempty"`

	// Bundle holds a tar.gz of the full skill directory (SKILL.md +
	// scripts/ + reference/ + ...) when SourceType="bundle". NULL/empty
	// for inline and path-source skills. Capped at 25 MiB in the publish
	// handler; the raw bytes round-trip across the mesh share path. The
	// SKILL.md inside the tar.gz MUST equal Body so the search index and
	// skill_get text response stay consistent without unpacking.
	Bundle []byte `json:"-"`

	// BundleSHA256 is the hex sha256 of Bundle. Empty when Bundle is
	// empty. Used as an integrity check on mesh transfers and lets
	// callers verify they got the bytes they asked for.
	BundleSHA256 string `json:"bundle_sha256,omitempty"`
}

// WorkerTemplateEntry is one published version of a Worker template in the
// worker_templates table (migration 057). A Worker template is a publishable
// Worker shape — model hints, prompt template, schedule hint, tool allowlist,
// parameter schema, secret slots — stored as a JSON blob in Body.
//
// Pre-057 these rows lived in skill_registry_entries with payload_type='worker',
// but that conflated them with markdown skills in the agent-facing skill
// catalog. Worker templates now have their own table and their own surface
// (mcplexer__worker_template_* tools + /api/worker-templates).
//
// Versioning + dedup mirror skill_registry_entries: linear monotonic Version,
// ContentHash dedup, COALESCE-based uniqueness on (workspace_id, name, version).
type WorkerTemplateEntry struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Version          int             `json:"version"`
	ContentHash      string          `json:"content_hash"`
	Description      string          `json:"description"`
	Body             string          `json:"body"`
	MetadataJSON     json.RawMessage `json:"metadata,omitempty"`
	TagsJSON         json.RawMessage `json:"tags,omitempty"`
	Author           string          `json:"author,omitempty"`
	ParentVersion    *int            `json:"parent_version,omitempty"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
	PublishedAt      time.Time       `json:"published_at"`
	CreatedByAgentID string          `json:"created_by_agent_id,omitempty"`

	// WorkspaceID nil = global; non-nil = pinned to one workspace.
	WorkspaceID *string `json:"workspace_id,omitempty"`
}

// Memory kinds. fact = atomic key/value (one active per scope+name);
// note = longer markdown blob, no uniqueness.
const (
	MemoryKindFact = "fact"
	MemoryKindNote = "note"
)

// Memory source kinds — populate every row so a poisoned source can be
// forensically purged with one DELETE WHERE source_session_id = ?.
const (
	MemorySourceAgent    = "agent"    // an MCP client wrote it
	MemorySourceHuman    = "human"    // dashboard write
	MemorySourceImported = "imported" // claude-cli importer / bulk load
	MemorySourceWorker   = "worker"   // a worker run wrote it
	MemorySourcePeer     = "peer"     // received from a libp2p peer
)

// MemoryEntry is one row in the memories table (migration 058).
//
// Two-tier model in one table: `Kind="fact"` records are dedup-unique
// per (workspace, worker, name) — updates invalidate (stamp T_valid_end)
// and insert a new active row, preserving the bi-temporal trail.
// `Kind="note"` records are append-only blobs of markdown, no uniqueness.
//
// Provenance is mandatory: SourceKind is always populated, and the
// optional Source* fields let `memory__forget_by_source` excise
// everything a poisoned session wrote.
//
// Embedding pointer (EmbedModel + EmbedVersion) lives here; the actual
// vector is in memories_vec keyed by ID. EmbedModel empty = no vector
// yet (FTS5 still works).
type MemoryEntry struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Kind             string          `json:"kind"` // fact|note
	Content          string          `json:"content"`
	TagsJSON         json.RawMessage `json:"tags,omitempty"`
	MetadataJSON     json.RawMessage `json:"metadata,omitempty"`
	WorkspaceID      *string         `json:"workspace_id,omitempty"`
	UserID           string          `json:"user_id,omitempty"`
	WorkerID         string          `json:"worker_id,omitempty"`
	RunID            string          `json:"run_id,omitempty"`
	SourceKind       string          `json:"source_kind"`
	SourceSessionID  string          `json:"source_session_id,omitempty"`
	SourcePeerID     string          `json:"source_peer_id,omitempty"`
	SourceToolCallID string          `json:"source_tool_call_id,omitempty"`
	OriginPeerID     string          `json:"origin_peer_id,omitempty"`
	EmbedModel       string          `json:"embed_model,omitempty"`
	EmbedVersion     int             `json:"embed_version,omitempty"`
	TValidStart      time.Time       `json:"t_valid_start"`
	TValidEnd        *time.Time      `json:"t_valid_end,omitempty"`
	InvalidatedBy    string          `json:"invalidated_by,omitempty"`
	Pinned           bool            `json:"pinned,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
}

// MemoryFilter narrows queries against the memories table. Scope mirrors
// SkillScope semantics: empty WorkspaceIDs = global only; non-empty =
// global ∪ those workspaces. IncludeAll bypasses scoping (admin tools).
//
// Entities + EntitiesAny add the "aboutness" axis (migration 076):
// Entities requires every link in the slice to exist on the row (AND);
// EntitiesAny requires at least one (OR). Both compose with scope so
// "memories in workspace W about task T" is one filter, not two passes.
type MemoryFilter struct {
	Scope           SkillScope
	Kind            string   // ""|"fact"|"note"
	Name            string   // exact name match; empty = all
	Tags            []string // every tag must be present (AND)
	WorkerID        string
	RunID           string
	UserID          string
	SourceKind      string
	SourceSessionID string
	OriginPeerID    string
	Entities        []EntityRef // AND: every link must exist (role optional)
	EntitiesAny     []EntityRef // OR:  any one link suffices
	IncludeInvalid  bool        // include rows with t_valid_end set
	IncludeDeleted  bool        // include soft-deleted rows
	SinceUpdated    *time.Time  // updated_at >= this
	// ValidAt restricts results to rows whose bi-temporal validity window
	// covered the given instant: t_valid_start <= ValidAt AND (t_valid_end
	// IS NULL OR t_valid_end > ValidAt). This is the point-in-time / "as of"
	// recall axis — "what did we believe at time T". It composes with (and
	// overrides the practical effect of) IncludeInvalid: a row invalidated
	// AFTER ValidAt is still returned because it was valid then. When nil,
	// validity is governed solely by IncludeInvalid.
	ValidAt *time.Time
	Limit   int // 0 = caller-implementation cap
	Offset  int
	// EntityDrivenIgnoresScope drops the workspace_id WHERE clause when
	// the query is narrowed by Entities/EntitiesAny against globally-
	// identifiable kinds (see IsGlobalEntityKind). Mirrors the brain's
	// semantic-memory model: facts about a person/task/skill follow the
	// entity, not the workspace where they were first encoded. The flag
	// is set by the handler (which knows the kinds + roles in play), not
	// the SQL layer (which is policy-free).
	EntityDrivenIgnoresScope bool
	// ScopeFilter narrows the default "workspaces ∪ global" visibility to
	// just one side. Empty = current behavior (workspaces + global).
	// "global_only" = workspace_id IS NULL only. "workspace_only" =
	// workspace_id IN (Scope.WorkspaceIDs) only, excluding global rows.
	// Used by the memory consolidator's two-pass mode so each pass writes
	// back to the right scope without bleeding global memory into
	// workspaces. The handler validates the value; the SQL layer trusts it.
	ScopeFilter string
}

// EntityRef identifies one "what is this memory about" link (migration 076).
// Kind is freeform vocabulary (task|person|place|peer|agent|org|skill|
// artifact|event|workspace|…). ID is the foreign identifier — ULID for
// tasks/peers, email/handle/URL/path for external things. Role
// (subject|mentioned|derived_from) qualifies the link; empty Role on the
// filter side matches any role on the row side.
type EntityRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

// Entity-link role vocabulary. Empty role on a filter matches any role on
// the row side; empty role on a save defaults to subject.
const (
	EntityRoleSubject     = "subject"
	EntityRoleMentioned   = "mentioned"
	EntityRoleDerivedFrom = "derived_from"
)

// IsGlobalEntityKind reports whether the kind's IDs are meaningful across
// workspace boundaries — i.e. whether a memory linked to (kind, id) should
// be reachable when the same (kind, id) is queried from a different
// workspace. This is the entity-store / episodic-store split: semantic
// facts about a person, task, or skill follow the entity (hub-and-spoke,
// Patterson/Bruce&Young person-identity-nodes); contextual notes about a
// local path or local event stay workspace-bound.
//
// Returns true for:
//
//   - task, person, peer, agent, org, skill, artifact — globally
//     identifiable; same id means same entity regardless of context.
//
// Returns false for:
//
//   - place         — absolute paths only make sense per-host/per-workspace.
//   - event         — locally-minted ULIDs; not globally meaningful.
//   - workspace     — inherently scoped; a workspace entity reference
//     IS a context, so cross-workspace reach would be circular.
//   - any unrecognised kind (conservative default — opt-in, not opt-out).
//
// The set mirrors p2p.peerLocalEntityKinds' complement; kept in sync by
// convention rather than cross-package import because store cannot
// import p2p (would create a cycle).
func IsGlobalEntityKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "task", "person", "peer", "agent", "org", "skill", "artifact":
		return true
	}
	return false
}

// EntityRecallCanEscapeScope reports whether a slice of EntityRef refs is
// strong enough to justify dropping the workspace_id filter on recall.
//
// Rule: ALL refs must be global-kind AND have a role that implies the
// memory is genuinely ABOUT the entity (subject or derived_from). Empty
// role is treated as subject (matches the save-side default).
// `mentioned` is deliberately excluded: a passing reference to Morgan
// in a meeting note doesn't make the meeting note universally relevant.
//
// Empty input returns false — without entity narrowing, dropping the
// scope clause would silently expose the entire global memory pool.
func EntityRecallCanEscapeScope(refs []EntityRef) bool {
	if len(refs) == 0 {
		return false
	}
	for _, r := range refs {
		if !IsGlobalEntityKind(r.Kind) {
			return false
		}
		role := strings.ToLower(strings.TrimSpace(r.Role))
		switch role {
		case "", EntityRoleSubject, EntityRoleDerivedFrom:
			// ok
		default:
			return false
		}
	}
	return true
}

// MemoryEntityRow is one row of memory_entities (migration 076) returned
// by ListMemoryEntities.
type MemoryEntityRow struct {
	ID         string    `json:"id"`
	MemoryID   string    `json:"memory_id"`
	EntityKind string    `json:"entity_kind"`
	EntityID   string    `json:"entity_id"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by,omitempty"`
}

// EntitySummary is one distinct entity surfaced by ListEntities — used to
// power the "Top entities" tile + entity picker autocomplete. MemoryCount
// is the number of non-deleted, non-invalidated memories linked to this
// entity within the queried scope. LastLinkedAt is the most-recent
// memory_entities.created_at for the entity.
type EntitySummary struct {
	Kind         string    `json:"kind"`
	ID           string    `json:"id"`
	MemoryCount  int       `json:"memory_count"`
	LastLinkedAt time.Time `json:"last_linked_at"`
}

// EntityFilter narrows ListEntities queries. Scope honors the same
// workspace ∪ global semantics as MemoryFilter — entities are derived
// from memories, so visibility follows the underlying rows.
type EntityFilter struct {
	Scope  SkillScope
	Kind   string // exact kind; empty = all kinds
	Limit  int
	Offset int
}

// EntityCoLink is one entity that co-occurs with a query entity in at
// least one memory (associative-recall axis, AR1). SharedCount is the
// number of memories that link BOTH the query entity and this one.
type EntityCoLink struct {
	Kind        string    `json:"kind"`
	ID          string    `json:"id"`
	SharedCount int       `json:"shared_count"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// ChatTurnSignal is one row of chat_turn_signals (migration 080). Each
// row is the concierge's read of the user's reaction to a previous turn:
// did they confirm, correct, get frustrated, redirect, escalate, or stay
// neutral. The friction-extractor worker (B2) polls negative labels and
// proposes refinements; the A/B telemetry worker (B4) aggregates by
// prompt_version to pick a winning prompt.
//
// ChatTurnLabel values — keep this list in sync with classifier code in
// internal/concierge/classifier.go and the friction-extractor SQL.
const (
	ChatTurnLabelConfirmation = "confirmation"
	ChatTurnLabelCorrection   = "correction"
	ChatTurnLabelFrustration  = "frustration"
	ChatTurnLabelRedirect     = "redirect"
	ChatTurnLabelEscalation   = "escalation"
	ChatTurnLabelNeutral      = "neutral"
)

// ChatTurnClassifierKind values.
const (
	ChatTurnClassifierRule  = "rule"
	ChatTurnClassifierModel = "model"
)

// ChatTurnSignal is one row of chat_turn_signals.
type ChatTurnSignal struct {
	ID                     string    `json:"id"`
	WorkerID               string    `json:"worker_id"`
	WorkspaceID            string    `json:"workspace_id"`
	UserIDExternal         string    `json:"user_id_external,omitempty"`
	Channel                string    `json:"channel"`
	PromptVersion          int       `json:"prompt_version"`
	TurnID                 string    `json:"turn_id,omitempty"`
	Label                  string    `json:"label"`
	UserMessage            string    `json:"user_message,omitempty"`
	AssistantMessage       string    `json:"assistant_message,omitempty"`
	Confidence             float64   `json:"confidence"`
	ClassifierKind         string    `json:"classifier_kind"`
	SourceSessionID        string    `json:"source_session_id,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	PromotedToRefinementID string    `json:"promoted_to_refinement_id,omitempty"`
}

// ChatTurnSignalFilter scopes ChatTurnSignalStore reads.
type ChatTurnSignalFilter struct {
	WorkerID       string
	WorkspaceID    string
	UserIDExternal string
	Channel        string
	PromptVersion  int      // 0 = any
	Labels         []string // empty = any
	NotPromoted    bool     // true = only signals where promoted_to_refinement_id is NULL
	Limit          int
}

// MemoryRecallEvent is one row of memory_recall_events (migration 077).
// Recorded async by the recall path when MCPLEXER_RECALL_TRACKING=1.
// Drives the co-recall aggregator (AR4): "memories surfaced alongside
// this one" + future RRF re-ranking by learned weight.
type MemoryRecallEvent struct {
	ID           string    `json:"id"`
	MemoryID     string    `json:"memory_id"`
	SessionID    string    `json:"session_id,omitempty"`
	WorkspaceID  string    `json:"workspace_id,omitempty"`
	Query        string    `json:"query"`
	EntityFilter string    `json:"entity_filter,omitempty"` // "kind:id"
	RankPosition int       `json:"rank_position"`           // 1-indexed
	ResultSetID  string    `json:"result_set_id"`
	Source       string    `json:"source"` // fts|vec|rrf|list
	CreatedAt    time.Time `json:"created_at"`
}

// CoRecalledMemory is one memory that frequently co-surfaces with the
// query memory in the recall log (AR4). CoOccurrences = distinct
// result_set_ids where both appeared. Score weights co-occurrence by
// rank proximity (top-1+top-2 scores more than top-1+top-10).
type CoRecalledMemory struct {
	MemoryID      string    `json:"memory_id"`
	Name          string    `json:"name"`
	CoOccurrences int       `json:"co_occurrences"`
	Score         float64   `json:"score"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

// MemoryRecallStat is the per-memory aggregate of recall activity from
// memory_recall_events (AR4), used as a BOUNDED recall-frequency/recency
// nudge in the ranking layer. RecentCount is the number of distinct recall
// result sets the memory surfaced in within the recency window;
// LastRecalledAt is the most-recent surfacing. Both zero/empty when the
// recall log has no events for the memory — the ranking term then
// degrades to a no-op (today's exact behaviour).
type MemoryRecallStat struct {
	MemoryID       string    `json:"memory_id"`
	RecentCount    int       `json:"recent_count"`
	LastRecalledAt time.Time `json:"last_recalled_at"`
}

// MemorySuggestion is one entry in the "you might also remember" bundle
// (AR5). Composes co-recall + related-entity + spreading-activation
// signals into a single ranked stream. Source tells the UI which axis
// produced this suggestion so it can render an explanation.
type MemorySuggestion struct {
	MemoryID string  `json:"memory_id"`
	Name     string  `json:"name"`
	Score    float64 `json:"score"`
	Source   string  `json:"source"` // "co_recall" | "related_entity" | "semantic"
	Reason   string  `json:"reason"` // human-readable hint, e.g. "co-linked via task:T"
}

// MemoryEmbedTarget is a minimal (id, content) pair for the embeddings
// backfill: the rows that still need a vector computed and stored.
type MemoryEmbedTarget struct {
	ID      string
	Content string
}

// MemoryConflict is one persisted possible-duplicate/conflict pair awaiting
// review (migration 116). Denormalised (names + preview captured at write
// time) so the dashboard conflict queue renders without joins and degrades
// gracefully if a side is later deleted. ResolvedAt nil = still open.
type MemoryConflict struct {
	ID               string     `json:"id"`
	MemoryID         string     `json:"memory_id"`
	MemoryName       string     `json:"memory_name"`
	CandidateID      string     `json:"candidate_id"`
	CandidateName    string     `json:"candidate_name"`
	CandidatePreview string     `json:"candidate_preview"`
	Kind             string     `json:"kind"` // "duplicate" | "related"
	Reason           string     `json:"reason"`
	WorkspaceID      string     `json:"workspace_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	Resolution       string     `json:"resolution,omitempty"` // "superseded" | "kept_both" | "dismissed"
}

// MemoryHit is a search result with the relevance score the matcher used.
// Score semantics: FTS5 BM25 rank (lower=better), or RRF combined score
// (higher=better) — Source distinguishes.
type MemoryHit struct {
	Entry  MemoryEntry `json:"entry"`
	Score  float64     `json:"score"`
	Source string      `json:"source"` // "fts" | "vec" | "rrf"
}

// MemoryOffer is one incoming memory offer from a paired libp2p peer.
// Pending = AcceptedAt and DeclinedAt both nil. AcceptedAsID points at
// the local memories.id once the user (or admin tool) requests + imports
// the full content via the /mcplexer/memory/1.0.0 protocol.
type MemoryOffer struct {
	ID           string          `json:"id"`
	PeerID       string          `json:"peer_id"`
	PeerName     string          `json:"peer_name,omitempty"`
	RemoteID     string          `json:"remote_id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Description  string          `json:"description,omitempty"`
	Preview      string          `json:"preview,omitempty"`
	TagsJSON     json.RawMessage `json:"tags,omitempty"`
	MetadataJSON json.RawMessage `json:"metadata,omitempty"`
	EmbedModel   string          `json:"embed_model,omitempty"`
	ReceivedAt   time.Time       `json:"received_at"`
	AcceptedAt   *time.Time      `json:"accepted_at,omitempty"`
	DeclinedAt   *time.Time      `json:"declined_at,omitempty"`
	AcceptedAsID string          `json:"accepted_as_id,omitempty"`
}

// MemoryOfferFilter narrows memory_offers queries.
type MemoryOfferFilter struct {
	PeerID      string
	PendingOnly bool
	IncludeDone bool // include accepted+declined when PendingOnly is false
	Limit       int
}

// MemoryStats is the aggregate "shape of the brain" snapshot that powers
// the memory landing header. Every field is best-effort: when a sub-query
// fails or the underlying data is unavailable, the implementation returns
// zero values rather than erroring the whole call. Honours SkillScope.
//
// Recall tracking: the recall log (migration 077, memory_recall_events)
// is the source of truth for "what has actually been surfaced". When it
// is populated, DecayPressure counts memories that are old AND not
// recently recalled, and RecallRate7d reports the share of memories
// surfaced in the last 7 days. When the log is empty (recall tracking
// disabled), DecayPressure falls back to the updated_at heuristic (older
// than 180d AND not pinned AND still valid) and RecallRate7d is 0.
type MemoryStats struct {
	BrainAgeDays    int                  `json:"brain_age_days"`
	BrainAgeBornAt  *time.Time           `json:"brain_age_born_at,omitempty"`
	TotalMemories   int                  `json:"total_memories"`
	TotalBytes      int64                `json:"total_bytes"`
	PagesEquivalent float64              `json:"pages_equivalent"` // ~500 bytes/page
	TypeMix         map[string]int       `json:"type_mix"`
	RecencyBuckets  MemoryRecencyBuckets `json:"recency_buckets"`
	WritesPerDay30d []MemoryDailyCount   `json:"writes_per_day_30d"`
	NetworkReach    MemoryNetworkReach   `json:"network_reach"`
	TopTags         []MemoryTagCount     `json:"top_tags"`
	DecayPressure   int                  `json:"decay_pressure"`
	// RecallRate7d is the fraction (0..1) of in-scope valid memories that
	// surfaced in at least one recall event within the last 7 days.
	// 0 when the recall log is empty (tracking disabled).
	RecallRate7d float64 `json:"recall_rate_7d"`
}

// MemoryRecencyBuckets groups memories by age of last update.
//   - fresh   : updated_at within last 7 days
//   - warm    : within last 30 days (and not fresh)
//   - cold    : within last 180 days (and not warm)
//   - dormant : older than 180 days
type MemoryRecencyBuckets struct {
	Fresh   int `json:"fresh"`
	Warm    int `json:"warm"`
	Cold    int `json:"cold"`
	Dormant int `json:"dormant"`
}

// MemoryDailyCount is one bar in the 30-day writes sparkline.
type MemoryDailyCount struct {
	Date  string `json:"date"` // YYYY-MM-DD (UTC)
	Count int    `json:"count"`
}

// MemoryNetworkReach summarises how much of the brain came from paired peers.
type MemoryNetworkReach struct {
	SharedMemoryCount int `json:"shared_memory_count"`
	PeerCount         int `json:"peer_count"`
}

// MemoryTagCount is one entry in the top-tags histogram.
type MemoryTagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// SkillRegistryTag is one (name, tag) → version pointer in the registry.
// The well-known tag `@stable` is human-curated; `@latest` is NEVER
// stored, always derived. Other free-form labels are allowed.
type SkillRegistryTag struct {
	Name    string    `json:"name"`
	Tag     string    `json:"tag"`
	Version int       `json:"version"`
	SetAt   time.Time `json:"set_at"`
	SetBy   string    `json:"set_by,omitempty"`
}

// SecretPrompt is a human-in-the-loop secret injection request. The agent
// calls the secret__prompt tool, mcplexer creates one of these in pending
// status, fires a UI/native notification, and blocks until the user submits
// or cancels. Secret value never lives here; FilePath points at a daemon-
// owned 0600 file under {data_dir}/secrets/ephemeral/<random> that is
// hard-deleted on first read (DeleteOnRead=true) or on expiry.
//
// SECURITY: FilePath is internal — it MUST NEVER be returned in any audit
// row, SSE event, or other surface readable by the agent. Only the agent's
// blocked tool-call response receives the path.
type SecretPrompt struct {
	ID           string     `json:"id"`
	Reason       string     `json:"reason"`
	Label        string     `json:"label"`
	Requester    string     `json:"requester"`
	Status       string     `json:"status"` // pending|submitted|cancelled|timeout
	FilePath     string     `json:"-"`      // never serialized to clients
	ExpiresAt    time.Time  `json:"expires_at"`
	CreatedAt    time.Time  `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	DeleteOnRead bool       `json:"delete_on_read"`
}

// ToolApproval represents a pending or resolved tool call approval request.
type ToolApproval struct {
	ID                 string `json:"id"`
	Status             string `json:"status"` // pending, approved, denied, timeout, cancelled
	RequestSessionID   string `json:"request_session_id"`
	RequestClientType  string `json:"request_client_type"`
	RequestModel       string `json:"request_model"`
	WorkspaceID        string `json:"workspace_id"`
	WorkspaceName      string `json:"workspace_name"`
	ToolName           string `json:"tool_name"`
	Arguments          string `json:"arguments"`
	Justification      string `json:"justification"`
	RouteRuleID        string `json:"route_rule_id"`
	DownstreamServerID string `json:"downstream_server_id"`
	AuthScopeID        string `json:"auth_scope_id"`
	ApproverSessionID  string `json:"approver_session_id"`
	ApproverType       string `json:"approver_type"` // mcp_agent, dashboard, system
	Resolution         string `json:"resolution"`
	TimeoutSec         int    `json:"timeout_sec"`
	// Surface identifies which Guard raised the approval (shell, schedule,
	// mcp, network, sanitizer). Empty string is read as "mcp" for backwards
	// compatibility with pre-Guards callers.
	Surface string `json:"surface,omitempty"`
	// OriginatingWorkspace identifies the workspace that produced this
	// approval request — distinct from WorkspaceID (the *target* of the
	// routed tool call). Set on cross-boundary shares (skill_share,
	// memory_share, task_offer, mesh_direct, mesh_grant_consent) so the
	// recipient's dashboard can tell which of their workspaces emitted
	// the share. Empty for legacy tool-call rows. Added in migration 081.
	OriginatingWorkspace string `json:"originating_workspace,omitempty"`
	// Kind classifies cross-boundary share approvals so the UI can pick a
	// renderer. One of: skill_share, memory_share, task_offer,
	// mesh_direct, mesh_grant_consent. Empty for legacy tool-call rows
	// (the UI falls back to surface/tool_name in that case). Added in
	// migration 081.
	Kind string `json:"kind,omitempty"`
	// Summary is a short human-readable preview of the share content —
	// skill name, memory title, task title, message head, or "Granted X
	// to peer Y" for mesh_grant_consent. Distinct from Justification
	// (which is the agent's "why"). Secrets are expected to be redacted
	// upstream before this field is populated. Added in migration 081.
	Summary    string     `json:"summary,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// P2PPeer represents a libp2p peer that has completed the pairing handshake
// with this node. Pairing codes are intentionally NOT persisted; they live
// in-memory (and in p2p_pending_pairs while in flight). RevokedAt nil means
// the peer is active; a non-nil value means revoked and must be ignored.
type P2PPeer struct {
	PeerID      string     `json:"peer_id"`
	DisplayName string     `json:"display_name"`
	PairedAt    time.Time  `json:"paired_at"`
	LastSeen    *time.Time `json:"last_seen,omitempty"`
	TrustLevel  int        `json:"trust_level"`
	Scopes      []string   `json:"scopes"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	// SSHTarget is the user@host (or ssh-config alias) the local dashboard
	// SSHes into when the user clicks "Focus" on a peer-origin agent. Empty
	// when unset — the UI greys the focus button for that peer and offers a
	// small inline editor to set it. Not auth-bearing; the libp2p peer ID
	// is still the cryptographic identity.
	SSHTarget string `json:"ssh_target,omitempty"`
	// SecretTransferRecipient is the peer's age X25519 recipient string
	// (`age1...`) learned via the `peer_identity` mesh broadcast. Empty
	// until the peer has announced once — mesh__send_secret refuses to
	// send to a peer with an empty recipient. Rotates whenever the peer
	// regenerates its transfer key. Not auth-bearing; signing is still
	// via the libp2p envelope.
	SecretTransferRecipient string `json:"secret_transfer_recipient,omitempty"`
}

// SecretOffer is a row in the secret_offers table. Tracks an in-flight
// peer→peer secret transfer for either side of the exchange. The
// plaintext is NEVER stored here — only the age ciphertext blob. On
// accept, the plaintext is moved into the auth_scopes secrets store.
type SecretOffer struct {
	OfferID    string            `json:"offer_id"`
	Direction  string            `json:"direction"` // "inbound" | "outbound"
	PeerID     string            `json:"peer_id"`
	Name       string            `json:"name"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Ciphertext []byte            `json:"-"`      // never JSON-marshalled
	Status     string            `json:"status"` // pending|accepted|rejected|expired|delivered
	CreatedAt  time.Time         `json:"created_at"`
	DecidedAt  *time.Time        `json:"decided_at,omitempty"`
	ExpiresAt  time.Time         `json:"expires_at"`
	SavedAs    string            `json:"saved_as,omitempty"`
}

// SkillOffer is an in-flight peer→peer registry-skill PUSH for either side
// of the exchange (mesh__push_skill). It carries only metadata: the skill
// body + tar.gz bundle are NOT stored here — they exceed the 1 MiB mesh
// envelope cap, so on accept the receiver pulls the full content from the
// sender over /mcplexer/skill-registry/1.0.0 and publishes it locally.
type SkillOffer struct {
	OfferID          string            `json:"offer_id"`
	Direction        string            `json:"direction"` // "inbound" | "outbound"
	PeerID           string            `json:"peer_id"`   // sender (inbound) / recipient (outbound)
	Name             string            `json:"name"`
	Version          int               `json:"version"`
	ContentHash      string            `json:"content_hash,omitempty"`
	BundleSHA256     string            `json:"bundle_sha256,omitempty"`
	Description      string            `json:"description,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Status           string            `json:"status"` // pending|accepted|rejected|expired
	CreatedAt        time.Time         `json:"created_at"`
	DecidedAt        *time.Time        `json:"decided_at,omitempty"`
	ExpiresAt        time.Time         `json:"expires_at"`
	PublishedVersion int               `json:"published_version,omitempty"` // local version after accept; 0 = none
}

// FileClaim represents a soft, advisory ownership claim on a set of path
// globs in a given repo+branch (M7.4). Claims are coordination signals only
// — there is NO enforcement: a remote agent that ignores a claim and edits a
// claimed file will not be blocked. The point is to surface "Alice is on
// internal/auth/* right now, intent: refactor token rotation" so peers can
// route around it before they collide.
//
// At least one of ClaimerUserID and ClaimerPeerID is non-empty:
//   - ClaimerUserID is set when M7.1's users table is wired (cross-machine
//     user identity).
//   - ClaimerPeerID is the libp2p peer ID of the announcing host. Always
//     populated for claims received from peers; may be empty for local
//     pre-M7.1 claims.
//
// ReleasedAt nil + ExpiresAt > now ⇒ "active". Either condition flipping
// makes the claim inactive.
type FileClaim struct {
	ClaimID            string     `json:"claim_id"`
	ClaimerUserID      string     `json:"claimer_user_id"`
	ClaimerPeerID      string     `json:"claimer_peer_id"`
	ClaimerDisplayName string     `json:"claimer_display_name"`
	Repo               string     `json:"repo"`
	Branch             string     `json:"branch"`
	Paths              []string   `json:"paths"`
	Intent             string     `json:"intent"`
	ClaimedAt          time.Time  `json:"claimed_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	ReleasedAt         *time.Time `json:"released_at,omitempty"`
}

// FileClaimFilter narrows file-claim queries. Empty fields mean "no filter
// on that field". When ActiveOnly is true, the result excludes released and
// expired claims.
type FileClaimFilter struct {
	Repo       string
	Branch     string
	Path       string // matches when any claim glob matches this literal path
	Claimer    string // matches user_id, peer_id, or display name (substring)
	ActiveOnly bool
	Now        time.Time // reference time for ActiveOnly check; zero = time.Now().UTC()
}

// P2PPendingPair holds an in-flight pairing handshake. Persisted so a daemon
// restart in the middle of the 5-minute TTL doesn't strand the user. The
// row is deleted when the code is consumed or expires.
type P2PPendingPair struct {
	Code       string    `json:"code"`
	PeerID     string    `json:"peer_id"`
	Multiaddrs []string  `json:"multiaddrs"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// User represents a per-HUMAN identity (M7.1). One human may operate
// multiple machines/peers; the peer_users join table links P2PPeer rows
// (per-machine) to the User row (per-human). Exactly one row in the users
// table has IsSelf=true: the local user.
type User struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	IsSelf      bool      `json:"is_self"`
}

// ScheduledJob is one entry in the Schedule Guard catalog (M0-A). Each
// job describes a recurring or event-triggered command mcplexer is
// responsible for: cron expression, fixed-interval duration, file-glob
// watch, or git-hook name. The in-process scheduler ticks on enabled rows
// whose NextRunAt <= now; SurviveDaemonDown=true rows additionally get
// promoted to a native systemd timer / launchd label in M3 so they keep
// firing while the daemon is down. NativeDriver+NativeID identify the OS
// resource for that promotion; empty strings mean "in-process only".
//
// ArgsJSON and EnvJSON are stored as JSON strings — callers Marshal/
// Unmarshal at the call site (no struct field for the parsed slices/maps
// because the storage layer stays format-agnostic).
type ScheduledJob struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Kind              string     `json:"kind"` // cron|interval|file_watch|git_hook|worker
	Spec              string     `json:"spec"`
	Command           string     `json:"command"`
	ArgsJSON          string     `json:"args_json"`
	EnvJSON           string     `json:"env_json"`
	CWD               string     `json:"cwd"`
	Surface           string     `json:"surface"`
	Enabled           bool       `json:"enabled"`
	SurviveDaemonDown bool       `json:"survive_daemon_down"`
	NativeDriver      string     `json:"native_driver"` // systemd_timer|launchd_label|''
	NativeID          string     `json:"native_id"`
	LastRunAt         *time.Time `json:"last_run_at,omitempty"`
	NextRunAt         *time.Time `json:"next_run_at,omitempty"`
	LastStatus        string     `json:"last_status"` // ''|success|failure|running
	LastError         string     `json:"last_error"`
	// WorkerID is set when Kind=worker — the scheduler dispatches to the
	// Worker row instead of execing j.Command. Empty string for every
	// non-worker kind. Stored as a nullable FK in the DB.
	WorkerID  string    `json:"worker_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SanitizerMeta is per-scope sanitizer policy + running counters (M0-A).
// Scope is one of "global" (ScopeID=""), "server" (ScopeID=server_id),
// or "tool" (ScopeID="namespace__tool"). The Sanitizer resolves effective
// policy by walking tool -> server -> global and taking the most-specific
// row that exists. ActionOnMatch is one of envelope, redact, block,
// quarantine; the three counters are bumped by the runtime and surfaced
// in the dashboard.
type SanitizerMeta struct {
	ID                string     `json:"id"`
	Scope             string     `json:"scope"`    // global|server|tool
	ScopeID           string     `json:"scope_id"` // server id or namespace__tool, '' for global
	DenylistEnabled   bool       `json:"denylist_enabled"`
	EnvelopeEnabled   bool       `json:"envelope_enabled"`
	ClassifierEnabled bool       `json:"classifier_enabled"`
	ClassifierModel   string     `json:"classifier_model"`
	ActionOnMatch     string     `json:"action_on_match"` // envelope|redact|block|quarantine
	DetectedCount     int        `json:"detected_count"`
	RedactedCount     int        `json:"redacted_count"`
	BlockedCount      int        `json:"blocked_count"`
	LastEventAt       *time.Time `json:"last_event_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// InstalledClient tracks a known MCP client that mcplexer has installed
// itself into on this machine (claude_code, picoclaw, cursor, ...).
// Each capability flag (HooksInstalled, ShimInstalled, SandboxEnabled)
// flips independently as integration steps are applied or rolled back.
// ConfigPath is the absolute path to the client's MCP config file when
// applicable (otherwise empty).
type InstalledClient struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ConfigPath     string `json:"config_path"`
	Installed      bool   `json:"installed"`
	HooksInstalled bool   `json:"hooks_installed"`
	// HooksDrifted is set true when HooksInstalled=true but the read-side
	// reconciler (api/guards_handler::shellDetail) re-checked the
	// underlying client settings file and the mcplexer endpoint substring
	// is no longer present. Surfaced in the dashboard as a red
	// "Hook drifted — re-install to repair" badge. Resets to false on
	// the next successful InstallClaudeCodeHooks. N/A when HooksInstalled
	// is false.
	HooksDrifted   bool       `json:"hooks_drifted"`
	ShimInstalled  bool       `json:"shim_installed"`
	SandboxEnabled bool       `json:"sandbox_enabled"`
	InstalledAt    *time.Time `json:"installed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// HarnessInitialization tracks per-harness last MCP initialize and the
// bootstrap receipt for the using-mcplexer rendered artifact(s).
// bootstrap_hash is the normalized content hash of the managed block
// (or skill file for claude sidecar). Drifted is set by recheck when
// live on-disk hash differs.
type HarnessInitialization struct {
	Key                string     `json:"key"`
	LastInitializeAt   *time.Time `json:"last_initialize_at,omitempty"`
	ClientInfo         string     `json:"client_info,omitempty"`
	BootstrapInstalled bool       `json:"bootstrap_installed"`
	BootstrapVersion   *int       `json:"bootstrap_version"`
	BootstrapHash      string     `json:"bootstrap_hash"`
	RegistryVersion    int        `json:"registry_version"`
	Drifted            bool       `json:"drifted"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// InstallReceipt records one reversible OS-side mutation that mcplexer
// performed during install/setup (write a config file, append to
// /etc/shells, register a launchd label, ...). The uninstall path
// replays these in reverse without guessing what the prior state was.
// ReverseData is a JSON blob whose schema depends on Action; the
// uninstaller knows how to read it for the action it owns. ReversedAt
// flips when the reversal succeeds; ReverseError captures the last
// failure so retries surface useful context.
type InstallReceipt struct {
	ID           string     `json:"id"`
	ClientID     string     `json:"client_id"`
	Action       string     `json:"action"`
	TargetPath   string     `json:"target_path"`
	BackupPath   string     `json:"backup_path"`
	ReverseData  string     `json:"reverse_data"`
	AppliedAt    time.Time  `json:"applied_at"`
	ReversedAt   *time.Time `json:"reversed_at,omitempty"`
	ReverseError string     `json:"reverse_error"`
}

// ApprovalRule is one entry in the 3-axis allowlist (pattern × directory
// × AI session) consulted by the various guards before prompting. A rule
// matches when Surface matches the guarded surface AND Pattern (glob)
// matches the request AND Directory is either empty or matches the
// caller's cwd AND AISessionID is either empty or matches the caller's
// session. Lower Priority wins; ExpiresAt NULL means "never expires".
// HitCount + LastHitAt are bumped on every match so the UI can show
// "this rule fired N times" for cleanup.
type ApprovalRule struct {
	ID          string     `json:"id"`
	Surface     string     `json:"surface"` // shell|schedule|mcp|network|sanitizer
	Pattern     string     `json:"pattern"`
	Directory   string     `json:"directory"`
	AISessionID string     `json:"ai_session_id"`
	Decision    string     `json:"decision"` // allow|deny|prompt
	Priority    int        `json:"priority"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	HitCount    int        `json:"hit_count"`
	LastHitAt   *time.Time `json:"last_hit_at,omitempty"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	// AllowMetachars opts THIS rule out of the shell-hook metachar
	// cheap-block (hooks_handler.go's "shell command contains
	// metacharacter X" rejection for ;|& backtick \n \r). The cheap-block
	// runs before the resolver, so a matching allow rule normally never
	// gets to fire on `ssh host 'a | b'`-style commands; setting this
	// flag tells the hook "if THIS rule would match the request, let it
	// reach the resolver". Narrower than dangerous-mode: every other
	// guard (protected paths, downstream-config validation, audit
	// logging) still applies. The UI defaults it on when installing the
	// "Allow + audit everything" wildcard rule.
	AllowMetachars bool `json:"allow_metachars"`
}

// Task source-kind constants — mirror the memory subsystem's vocabulary.
const (
	TaskSourceAgent      = "agent"
	TaskSourceWorker     = "worker"
	TaskSourceUser       = "user"
	TaskSourcePeerImport = "peer-import"
	TaskSourceSystem     = "system"
)

// Task assignee origin kinds.
const (
	TaskAssigneeLocal = "local"
	TaskAssigneePeer  = "peer"
	TaskAssigneeHuman = "human"
)

// Task offer states.
const (
	TaskOfferPending          = "pending"
	TaskOfferAccepted         = "accepted"
	TaskOfferDeclined         = "declined"
	TaskOfferExpired          = "expired"
	TaskOfferAutoAccepted     = "auto_accepted"
	TaskOfferRejectedThrottle = "rejected_throttle"
	TaskOfferRejectedUnscoped = "rejected_unscoped"
	TaskOfferConflict         = "conflict"
)

// Task is one row in the tasks table (migration 061). The operational
// primitive for work-to-be-done — per-workspace, freeform-status,
// composition-friendly (epics via meta:composes), and mesh-aware
// (lifecycle events emit kind="task_event" mesh messages in phase 2).
//
// Unlike memory, tasks are NOT bi-temporal. Updates mutate the row in
// place; the StatusHistoryJSON column carries row-local audit so
// "what was status on Monday" survives mesh-message retention changes.
type Task struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`

	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`

	Priority string     `json:"priority"`
	DueAt    *time.Time `json:"due_at,omitempty"`

	TagsJSON json.RawMessage `json:"tags,omitempty"`
	Meta     string          `json:"meta,omitempty"`

	AssigneeSessionID   string     `json:"assignee_session_id,omitempty"`
	AssigneeOriginKind  string     `json:"assignee_origin_kind"` // local|peer|human
	AssigneePeerID      string     `json:"assignee_peer_id,omitempty"`
	AssigneeUserID      string     `json:"assignee_user_id,omitempty"`
	AssignedBySessionID string     `json:"assigned_by_session_id,omitempty"`
	AssignedByPeerID    string     `json:"assigned_by_peer_id,omitempty"`
	AssignedAt          *time.Time `json:"assigned_at,omitempty"`
	// LeaseExpiresAt — populated by the service layer when status flips
	// to "doing" with an assignee. The assignee bumps it via
	// task__heartbeat; an expired lease is cleared by
	// Service.SweepExpiredLeases which nulls the assignee + appends
	// evt=lease_expired to the row's status_history. Pre-071 rows have
	// no value and fall back to updated_at as the UI's staleness proxy.
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`

	SourceKind         string `json:"source_kind"`
	SourceSessionID    string `json:"source_session_id,omitempty"`
	SourceToolCallID   string `json:"source_tool_call_id,omitempty"`
	CreatedBySessionID string `json:"created_by_session_id,omitempty"`
	UpdatedBySessionID string `json:"updated_by_session_id,omitempty"`
	OriginPeerID       string `json:"origin_peer_id,omitempty"`

	StatusHistoryJSON json.RawMessage `json:"status_history,omitempty"`

	// Collaboration visibility is independent of task content and assignment.
	// Generic task updates preserve these fields; widening or narrowing goes
	// through the dedicated authorization path so a normal edit cannot bypass
	// workspace policy.
	OwnerPrincipalID               string     `json:"owner_principal_id,omitempty"`
	Visibility                     string     `json:"visibility"`
	VisibilityEpoch                int64      `json:"visibility_epoch"`
	VisibilityUpdatedByPrincipalID string     `json:"visibility_updated_by_principal_id,omitempty"`
	VisibilityUpdatedAt            *time.Time `json:"visibility_updated_at,omitempty"`

	// HlcAt is the per-row Hybrid Logical Clock stamp written on every
	// mutation. 32-char lowercase hex string (wall_ms + counter; see
	// internal/clock/hlc.go). Sorts lexicographically by HLC order so
	// the gossip watermark query is `WHERE hlc_at > ?` with no decode.
	// Empty only on the brief window between row construction and the
	// first store write — production rows always carry a value.
	HlcAt string `json:"hlc_at,omitempty"`
	// RemoteBaseHLC is the last canonical home revision observed by a task
	// mirror. Local edits preserve it until a later home sync advances it.
	// Home-owned tasks leave it empty.
	RemoteBaseHLC string `json:"remote_base_hlc,omitempty"`

	Pinned    bool       `json:"pinned,omitempty"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TaskStatusHistoryEntry is one entry inside Task.StatusHistoryJSON.
// Append-only — the service layer reads, appends, and writes back.
type TaskStatusHistoryEntry struct {
	At        time.Time `json:"at"`
	BySession string    `json:"by_session,omitempty"`
	ByPeer    string    `json:"by_peer,omitempty"`
	Evt       string    `json:"evt"`            // status_changed|assigned|closed|...
	From      string    `json:"from,omitempty"` // old value (status, assignee, etc.)
	To        string    `json:"to,omitempty"`   // new value
	Note      string    `json:"note,omitempty"`
}

// TaskFilter narrows ListTasks. Empty fields = no filter.
type TaskFilter struct {
	WorkspaceID         string
	Status              string
	OnlyTerminal        *bool    // nil = no filter; true = only terminal statuses (joined via vocab); false = only non-terminal
	Tags                []string // ALL must be present (AND)
	AssigneeSessionID   string
	AssigneeOriginKind  string // local|peer|human
	AssigneePeerID      string
	AssigneeUserID      string
	AssignedBySessionID string
	AssignedByPeerID    string
	SourceSessionID     string
	OriginPeerID        string
	IncludeDeleted      bool
	UpdatedAfter        *time.Time
	CreatedAfter        *time.Time
	Limit               int
	Offset              int

	// MetaMatch — every (key, value) pair must match a meta entry.
	// Uses json_extract under the hood; scalar values match exactly,
	// array values match by containment (the JSON1 path returns the
	// whole array — the SQL surface uses EXISTS over json_each to
	// express "any element equals ?"). The meta_composed_by
	// generated column is preferred for that one key so the index
	// fires.
	MetaMatch map[string]string

	// MetaHasKey — meta object contains this key (regardless of
	// value or shape). Useful for filtering "tasks with a peer set"
	// without caring which peer.
	MetaHasKey []string

	// MetaIn — value at `key` is one of the listed values. Combines
	// with MetaMatch via AND.
	MetaIn map[string][]string
}

// TaskNote is one row in task_notes (append-only).
type TaskNote struct {
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	AuthorSessionID string    `json:"author_session_id,omitempty"`
	AuthorKind      string    `json:"author_kind"`
	Body            string    `json:"body"`
	CreatedAt       time.Time `json:"created_at"`
}

// TaskHistoryEntry is one immutable audit row for a task mutation or
// action. It stores full before/after task snapshots so callers can
// inspect what changed and restore a prior revision without relying on
// lossy tool-call audit params.
type TaskHistoryEntry struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	WorkspaceID string `json:"workspace_id"`
	Revision    int    `json:"revision"`
	Action      string `json:"action"`

	ActorKind      string `json:"actor_kind,omitempty"`
	ActorSessionID string `json:"actor_session_id,omitempty"`
	ActorPeerID    string `json:"actor_peer_id,omitempty"`
	ActorUserID    string `json:"actor_user_id,omitempty"`

	SourceKind        string          `json:"source_kind,omitempty"`
	SourceSessionID   string          `json:"source_session_id,omitempty"`
	SourceToolCallID  string          `json:"source_tool_call_id,omitempty"`
	WorkspacePath     string          `json:"workspace_path,omitempty"`
	OriginPeerID      string          `json:"origin_peer_id,omitempty"`
	RelatedRevision   int             `json:"related_revision,omitempty"`
	ChangedFieldsJSON json.RawMessage `json:"changed_fields,omitempty"`
	Note              string          `json:"note,omitempty"`
	BeforeJSON        json.RawMessage `json:"before,omitempty"`
	AfterJSON         json.RawMessage `json:"after,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

// TaskAttachment is one row in task_attachments (migration 079). The row
// is the index; the actual bytes live on disk at
// <data_dir>/<StoragePath>. Content-addressed by Sha256 so uploads of
// the same content within a task dedupe to one on-disk blob (multiple
// rows may share StoragePath). Soft-delete via DeletedAt preserves the
// audit trail; the GC of orphan on-disk files is a future concern (a
// second row may still reference the same Sha256).
type TaskAttachment struct {
	ID                string     `json:"id"`
	TaskID            string     `json:"task_id"`
	WorkspaceID       string     `json:"workspace_id"`
	Filename          string     `json:"filename,omitempty"`
	MimeType          string     `json:"mime_type"`
	SizeBytes         int64      `json:"size_bytes"`
	Sha256            string     `json:"sha256"`
	StoragePath       string     `json:"storage_path"`
	UploaderSessionID string     `json:"uploader_session_id,omitempty"`
	UploaderKind      string     `json:"uploader_kind"`
	CreatedAt         time.Time  `json:"created_at"`
	DeletedAt         *time.Time `json:"deleted_at,omitempty"`
}

// TaskStatusVocab is one row in task_status_vocabulary. Per-workspace,
// freeform status_text, with is_terminal as the canonical "closed-ish"
// signal a query layer can apply without enumerating the workspace's
// user-coined vocabulary. Kind classifies the status into one of the
// six canonical buckets (open | working | blocked | review | done |
// cancelled) so UI affordances + auto-claim service logic generalise
// across freeform vocabulary (migration 070; review split out of
// blocked in migration 099 — review is NOT working and NOT terminal).
type TaskStatusVocab struct {
	WorkspaceID  string    `json:"workspace_id"`
	StatusText   string    `json:"status_text"`
	IsTerminal   bool      `json:"is_terminal"`
	Kind         string    `json:"kind"` // open|working|blocked|review|done|cancelled
	DisplayColor string    `json:"display_color,omitempty"`
	DisplayOrder int       `json:"display_order"`
	ManagedBy    string    `json:"managed_by"` // user|skill|system
	UpdatedAt    time.Time `json:"updated_at"`
}

// WorkspacePeerBinding memoizes "peer A's workspace X maps to my
// workspace Y" once the local agent has accepted the first offer.
// Subsequent offers from the same peer→remote-workspace land
// deterministically in the bound local workspace.
type WorkspacePeerBinding struct {
	PeerID              string    `json:"peer_id"`
	RemoteWorkspaceID   string    `json:"remote_workspace_id"`
	LocalWorkspaceID    string    `json:"local_workspace_id"`
	RemoteWorkspaceName string    `json:"remote_workspace_name,omitempty"`
	EstablishedAt       time.Time `json:"established_at"`

	// Linked promotes a plain offer-routing binding into an explicit,
	// bidirectional "linked workspace": the (peer, remote_workspace) ↔
	// local_workspace pair opts into silent task replication (migration
	// 088). Plain offer-accept upserts never set this — linking is an
	// explicit SetWorkspaceLink write. See .planning/linked-workspaces/.
	Linked bool `json:"linked,omitempty"`
	// LinkEstablishedBy records who declared the link: "local" (this
	// machine's operator) or "peer" (a mirror created when the peer's
	// linked task first landed). Empty when not linked.
	LinkEstablishedBy string `json:"link_established_by,omitempty"`
	// LinkEstablishedAt is when the link was declared; nil when not linked.
	LinkEstablishedAt *time.Time `json:"link_established_at,omitempty"`
}

// TaskOffer is one row in task_offers — incoming or outgoing, gated by
// the task_offer:/task_assign: peer scopes. Carries preview-only
// payload; the full task body transfers over /mcplexer/task/1.0.0 on
// accept.
type TaskOffer struct {
	ID                  string `json:"id"`
	TaskID              string `json:"task_id,omitempty"` // null until accepted
	RemoteTaskID        string `json:"remote_task_id"`
	ShareID             string `json:"share_id,omitempty"`
	SenderPrincipalID   string `json:"sender_principal_id,omitempty"`
	AccessEpoch         int64  `json:"access_epoch,omitempty"`
	VisibilityEpoch     int64  `json:"visibility_epoch,omitempty"`
	BaseHLC             string `json:"base_hlc,omitempty"`
	FromPeerID          string `json:"from_peer_id"`
	ToPeerID            string `json:"to_peer_id"`
	RemoteWorkspaceID   string `json:"remote_workspace_id"`
	RemoteWorkspaceName string `json:"remote_workspace_name,omitempty"`
	WorkspaceID         string `json:"workspace_id,omitempty"` // resolved local workspace once accepted

	Title              string          `json:"title"`
	DescriptionPreview string          `json:"description_preview,omitempty"`
	MetaPreview        string          `json:"meta_preview,omitempty"`
	StatusPreview      string          `json:"status_preview,omitempty"`
	PriorityPreview    string          `json:"priority_preview,omitempty"`
	TagsJSON           json.RawMessage `json:"tags,omitempty"`

	IsDirectAssign    bool       `json:"is_direct_assign"`
	EnvelopeNonce     string     `json:"envelope_nonce"`
	EnvelopeCreatedAt time.Time  `json:"envelope_created_at"`
	Direction         string     `json:"direction"` // incoming|outgoing
	State             string     `json:"state"`
	AcceptedAt        *time.Time `json:"accepted_at,omitempty"`
	DeclinedAt        *time.Time `json:"declined_at,omitempty"`
	DeclinedReason    string     `json:"declined_reason,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

// TaskOfferFilter narrows ListTaskOffers.
type TaskOfferFilter struct {
	Direction string // ""|incoming|outgoing
	State     string // ""|pending|accepted|...
	PeerID    string // ""|<peer_id>
	// EnvelopeNonce and RemoteTaskID scope the query to a single offer. The
	// cross-peer Phase B payload lookup uses them so the authorization match
	// runs in SQL rather than by paging a bounded list (a valid offer beyond
	// the row cap was otherwise spuriously denied under load).
	EnvelopeNonce string
	RemoteTaskID  string
	Since         *time.Time
	Limit         int
}

// MilestoneBurndown is the aggregate view of one milestone-tagged epic
// task: the task itself plus its children rollup + the per-day burndown
// series. A "milestone" is convention-only — any task carrying the
// `milestone` tag with `due_at` set. No new column / migration.
//
// DaysRemaining is signed: negative values indicate overdue. BurndownPoints
// covers every UTC day from the milestone's created_at up to its due_at
// (inclusive on both ends), with each point reporting the open vs. closed
// child counts as of that day's end. Empty when due_at <= created_at.
type MilestoneBurndown struct {
	Task           Task            `json:"task"`
	TotalChildren  int             `json:"total_children"`
	ClosedChildren int             `json:"closed_children"`
	DaysRemaining  int             `json:"days_remaining"`
	BurndownPoints []BurndownPoint `json:"burndown_points"`
}

// BurndownPoint is one day-bucket sample on a milestone burndown curve.
// Date is YYYY-MM-DD (UTC). Open + Closed always sums to TotalChildren
// of the parent MilestoneBurndown (closed/open partition of the same
// snapshot).
type BurndownPoint struct {
	Date           string `json:"date"`
	ChildrenOpen   int    `json:"children_open"`
	ChildrenClosed int    `json:"children_closed"`
}

// TaskAssignThrottle records the throttle window for direct-assigns
// from a peer into a workspace. The receiving daemon updates this on
// every accepted direct-assign and checks it on the next.
type TaskAssignThrottle struct {
	PeerID          string    `json:"peer_id"`
	WorkspaceID     string    `json:"workspace_id"`
	LastAssignAt    time.Time `json:"last_assign_at"`
	CountInWindow   int       `json:"count_in_window"`
	WindowStartedAt time.Time `json:"window_started_at"`
}

// === Skill telemetry (W2) ===
//
// SkillRun is one agent invocation of a registry skill — the runtime
// substrate of the skills-first epic. The dashboard renders these on
// each skill's detail page; the refinement loop (W3) ingests outcome
// + tools_used as A/B signal; the composition graph (W6) walks the
// inter-run links via metadata.
//
// PhasesJSON is the append-only event log: each entry is a JSON
// object `{phase, event, at, note}` where event ∈ started|completed|
// failed. Restarts append rather than overwrite; that "phase X
// restarted N times" pattern is itself a refinement signal.
//
// ToolsUsedJSON is an aggregated count: `[{name, count}]`. Filled by
// the handler at run_complete time from the per-skill invocation
// audit trail; left empty mid-run so the column stays cheap to
// rewrite as phases progress.

// SkillRunOutcomeRunning is the default outcome stamped at
// run_start. Terminal outcomes set CompletedAt.
const (
	SkillRunOutcomeRunning   = "running"
	SkillRunOutcomeSuccess   = "success"
	SkillRunOutcomeFailure   = "failure"
	SkillRunOutcomeCancelled = "cancelled"
)

// SkillRun is one row in the skill_runs table.
type SkillRun struct {
	ID             string          `json:"id"`
	SkillName      string          `json:"skill_name"`
	SkillVersion   int             `json:"skill_version"`
	WorkspaceID    string          `json:"workspace_id"`
	StartedAt      time.Time       `json:"started_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
	Outcome        string          `json:"outcome"`
	PhasesJSON     json.RawMessage `json:"phases_json"`
	ToolsUsedJSON  json.RawMessage `json:"tools_used_json"`
	TaskEpicID     string          `json:"task_epic_id,omitempty"`
	AgentSessionID string          `json:"agent_session_id,omitempty"`
	MetadataJSON   json.RawMessage `json:"metadata_json"`
}

// SkillRunPhaseEvent is one entry in PhasesJSON. Append-only.
type SkillRunPhaseEvent struct {
	Phase string    `json:"phase"`
	Event string    `json:"event"` // "started"|"completed"|"failed"
	At    time.Time `json:"at"`
	Note  string    `json:"note,omitempty"`
}

// SkillRunToolUse is one entry in ToolsUsedJSON.
type SkillRunToolUse struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SkillRunPatch is the partial-update payload for UpdateSkillRun. Nil
// pointers leave the column unchanged; non-nil overwrites. PhasesJSON
// is set whole (callers append in memory then PUT the whole array).
type SkillRunPatch struct {
	CompletedAt   *time.Time
	Outcome       *string
	PhasesJSON    json.RawMessage // nil = no change; non-nil overwrites
	ToolsUsedJSON json.RawMessage
	TaskEpicID    *string
	MetadataJSON  json.RawMessage
}

// SkillRunFilter narrows ListSkillRuns. All fields are optional;
// zero values mean "no filter on this dimension".
type SkillRunFilter struct {
	SkillName   string
	WorkspaceID string
	Outcome     string     // ""|running|success|failure|cancelled
	Since       *time.Time // started_at >= since
	Limit       int        // default 50, max 500
}

// === Skill refinement (W3) ===
//
// A SkillRefinementProposal is an agent's suggestion that a particular
// skill version could be improved. It pairs concrete *friction* with a
// proposed *suggested_change* and a one-line *rationale*. Proposals
// are NEVER applied to the underlying SKILL.md automatically — every
// promotion is a separate, audited decision (human or quorum-driven).
//
// Lifecycle:
//
//	pending    — fresh proposal, not yet matched by quorum
//	candidate  — fuzzy-match count crossed quorum threshold; surfaced
//	             on dashboard inbox + broadcast as mesh finding
//	promoted   — reviewer approved; recorded as the decision (the
//	             actual skill_registry version bump is a separate
//	             follow-up step, see TODO in skill_refinement_handler)
//	rejected   — reviewer declined with a note
//
// Why a separate table from skill_runs: refinement proposals are
// authored synthesis (the agent saying "this could be better"), not
// raw telemetry. Keeping the two append-only stores side-by-side lets
// the quorum aggregator JOIN against skill_runs for A/B selection
// later without inflating the SkillRun row with unrelated columns.
const (
	RefinementStatusPending   = "pending"
	RefinementStatusCandidate = "candidate"
	RefinementStatusPromoted  = "promoted"
	RefinementStatusRejected  = "rejected"
	RefinementStatusApplied   = "applied"
)

// SkillRefinementProposal is one row in skill_refinement_proposals.
// ProposedByPeerID is empty for local-only deployments where libp2p
// isn't running; cross-peer attribution fills it from the mesh
// envelope's peer ID when present.
type SkillRefinementProposal struct {
	ID                  string          `json:"id"`
	SkillName           string          `json:"skill_name"`
	SkillVersion        int             `json:"skill_version"`
	Friction            string          `json:"friction"`
	SuggestedChange     string          `json:"suggested_change"`
	Rationale           string          `json:"rationale"`
	ProposedBySessionID string          `json:"proposed_by_session_id"`
	ProposedByPeerID    string          `json:"proposed_by_peer_id,omitempty"`
	WorkspaceID         string          `json:"workspace_id"`
	CreatedAt           time.Time       `json:"created_at"`
	Status              string          `json:"status"`
	CandidateAt         *time.Time      `json:"candidate_at,omitempty"`
	ResolvedAt          *time.Time      `json:"resolved_at,omitempty"`
	ResolvedBySessionID string          `json:"resolved_by_session_id,omitempty"`
	ResolutionNote      string          `json:"resolution_note,omitempty"`
	MetadataJSON        json.RawMessage `json:"metadata_json"`
}

// RefinementFilter narrows ListRefinementProposals. All fields are
// optional; zero values mean "no filter on this dimension". Default
// limit is 100 when Limit is zero; hard cap is 500 to keep one-shot
// responses bounded.
type RefinementFilter struct {
	SkillName   string
	Status      string // ""|pending|candidate|promoted|rejected|applied
	WorkspaceID string
	Limit       int
}

// RefinementProposalPatch is the partial-update payload for
// UpdateRefinementProposal. Nil pointers leave the column unchanged;
// non-nil overwrites. ResolvedAt is auto-stamped when Status flips to
// a terminal value (promoted/rejected/applied) AND the caller leaves it nil.
type RefinementProposalPatch struct {
	Status              *string
	CandidateAt         *time.Time
	ResolvedAt          *time.Time
	ResolvedBySessionID *string
	ResolutionNote      *string
	MetadataJSON        json.RawMessage
}
