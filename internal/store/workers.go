package store

import "time"

const (
	// WorkerWorkspaceAccessRead allows a worker to observe/read data in a
	// workspace. It does not permit mutations.
	WorkerWorkspaceAccessRead = "read"
	// WorkerWorkspaceAccessWrite allows a worker to read and mutate data
	// in a workspace. Write implies read.
	WorkerWorkspaceAccessWrite = "write"
)

// WorkerWorkspaceAccess is one explicit workspace grant for a Worker.
// WorkspaceID identifies the workspace the worker can see; Access is
// "read" or "write" (write implies read). Worker.WorkspaceID remains
// the preferred/home workspace used for scheduling identity and default
// routing; WorkspaceAccess is the full visibility set.
type WorkerWorkspaceAccess struct {
	WorkerID    string    `json:"worker_id,omitempty"`
	WorkspaceID string    `json:"workspace_id"`
	Access      string    `json:"access"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

func (g WorkerWorkspaceAccess) CanRead() bool {
	return g.Access == WorkerWorkspaceAccessRead || g.Access == WorkerWorkspaceAccessWrite
}

func (g WorkerWorkspaceAccess) CanWrite() bool {
	return g.Access == WorkerWorkspaceAccessWrite
}

// Worker is one scheduled in-process AI agent configuration (M0.1). The
// scheduler fires a Worker on its ScheduleSpec; the runner renders
// PromptTemplate with ParametersJSON, dispatches to the configured model
// with the ToolAllowlistJSON-bounded tool surface, and routes output to
// the OutputChannelsJSON sinks.
//
// SecretScopeID points at the AuthScope holding the model API key. The
// adapter only reads it at run time; the value never leaves the secrets
// subsystem on the wire.
//
// SkillName/SkillVersion are optional — a Worker can run with a pure
// PromptTemplate when no skill body is attached. When both are set the
// runner loads the InstalledSkill bundle and prepends its system prompt
// to the rendered user prompt.
//
// SkillRefs (M0.7+) is the canonical multi-skill source — an ordered
// list whose bodies are all loaded by the runner and joined with a
// markdown separator before being sent as the system prompt. The legacy
// SkillName/SkillVersion fields remain as a fallback for rows written
// before the multi-skill migration. Use EffectiveSkillRefs() everywhere
// the runner / dashboard wants "which skills are attached"; never read
// SkillRefs vs SkillName directly outside the persistence layer.
//
// MemoryScopeID is reserved for the future memory-system initiative; M0
// reads/writes it but does not act on it. The column ships now so adding
// memory is a non-migration change later.
//
// ExecMode is "propose" (the runner emits a draft + mesh message instead
// of writing output) or "autonomous" (the runner executes tool calls
// directly, subject to ToolAllowlistJSON). ConcurrencyPolicy is "skip"
// (default — drop the tick if a run is already in flight) or "queue"
// (let the next tick start a parallel run, audit-only).
type Worker struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	ModelProvider    string `json:"model_provider"` // anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli
	ModelID          string `json:"model_id"`
	ModelEndpointURL string `json:"model_endpoint_url,omitempty"`
	SecretScopeID    string `json:"secret_scope_id"`
	SkillName        string `json:"skill_name,omitempty"`
	SkillVersion     string `json:"skill_version,omitempty"`
	// SkillRefs is the canonical multi-skill list. Each ref's body is
	// loaded by the runner and joined with a markdown separator before
	// being sent as the system prompt. Empty (nil or zero-length) means
	// "fall back to SkillName/SkillVersion" — see EffectiveSkillRefs.
	SkillRefs         []SkillRef `json:"skill_refs"`
	PromptTemplate    string     `json:"prompt_template"`
	ParametersJSON    string     `json:"parameters_json"`
	ScheduleSpec      string     `json:"schedule_spec"`
	ToolAllowlistJSON string     `json:"tool_allowlist_json"`
	// CapabilityProfileJSON is the marshalled toolgate.CapabilityProfile
	// that scopes a delegate worker's reachable tool surface + mcplexer
	// features. Empty string = no profile = today's allow-all behavior
	// (only ToolAllowlistJSON gates). This is the ENFORCEMENT source of
	// truth — it is a first-class column, not the display-only
	// ParametersJSON delegation blob.
	CapabilityProfileJSON string `json:"capability_profile_json,omitempty"`
	// PreExecuteScript / PostExecuteScript are optional user-authored JS
	// snippets run in the code-mode sandbox around the model loop. The
	// pre-script runs BEFORE any model/CLI spend and can BLOCK the run
	// (throw / call abort(reason)) — e.g. hit an endpoint and gate on the
	// response. The post-script runs AFTER output is produced and can
	// REJECT the output (throw on a successful run flips it to "blocked",
	// suppressing channel emission). Both execute with the worker's own
	// tool allowlist + capability profile and are audited like any other
	// execute_code call. Empty string = no hook. See internal/workers/runner/hooks.go.
	PreExecuteScript   string `json:"pre_execute_script,omitempty"`
	PostExecuteScript  string `json:"post_execute_script,omitempty"`
	OutputChannelsJSON string `json:"output_channels_json"`
	ExecMode           string `json:"exec_mode"`          // propose|autonomous
	ConcurrencyPolicy  string `json:"concurrency_policy"` // skip|queue
	// MemoryScopeID is unused in M0 but persisted so future memory work
	// doesn't need a migration. Empty string when no memory scope is
	// attached.
	MemoryScopeID string `json:"memory_scope_id,omitempty"`

	// Per-worker budget caps (M1). 0 in any field means "use the runner
	// package default" (or "no cap" for MaxMonthlyCostUSD /
	// MaxConsecutiveFailures). Runner applies these on top of its base
	// Caps when constructing per-run loopState.
	MaxInputTokens         int     `json:"max_input_tokens"`
	MaxOutputTokens        int     `json:"max_output_tokens"`
	MaxToolCalls           int     `json:"max_tool_calls"`
	MaxWallClockSeconds    int     `json:"max_wall_clock_seconds"`
	MaxMonthlyCostUSD      float64 `json:"max_monthly_cost_usd"`
	MaxConsecutiveFailures int     `json:"max_consecutive_failures"`
	// AutoPausedReason carries WHY a worker was paused. Empty for manual
	// pauses; populated by the runner's auto-pause logic with messages
	// like "monthly budget exceeded" or "consecutive failures".
	AutoPausedReason string `json:"auto_paused_reason,omitempty"`

	Enabled     bool   `json:"enabled"`
	WorkspaceID string `json:"workspace_id"`
	// WorkspaceAccess is the explicit workspace visibility set for this
	// worker. Empty rows from legacy databases are treated as
	// [{workspace_id: Worker.WorkspaceID, access: "write"}] at the
	// service/store boundary so old workers preserve behaviour.
	WorkspaceAccess []WorkerWorkspaceAccess `json:"workspace_access,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
	// ArchivedAt marks a Worker as retired without deleting config or run
	// history. Archived workers are hidden from default lists, cannot be
	// resumed, and must never be scheduled or run.
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
	ArchivedReason string     `json:"archived_reason,omitempty"`

	// SourceTemplateName + SourceTemplateVersion record the skill-registry
	// `worker` template this Worker was installed from (M3). Empty Name /
	// zero Version mean "hand-built" — every legacy worker preserved by
	// the 050 migration backfills to these defaults. The dashboard shows
	// an "Update available — vN" hint when a higher version exists in the
	// registry.
	SourceTemplateName    string `json:"source_template_name,omitempty"`
	SourceTemplateVersion int    `json:"source_template_version,omitempty"`
}

// SkillRef is one entry in a Worker's ordered skill list. Name is
// required; Version is optional (empty = "latest stable", matching the
// SkillReader convention).
type SkillRef struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// EffectiveSkillRefs returns the ordered skill refs that the runner
// should prepend to the rendered prompt. When SkillRefs is non-empty it
// wins outright; otherwise the legacy (SkillName, SkillVersion) tuple
// is synthesised into a single-element slice so workers persisted
// before the M0.7 multi-skill migration keep working. Returns nil when
// neither source has any skill configured.
func (w *Worker) EffectiveSkillRefs() []SkillRef {
	if len(w.SkillRefs) > 0 {
		return w.SkillRefs
	}
	if w.SkillName == "" {
		return nil
	}
	return []SkillRef{{Name: w.SkillName, Version: w.SkillVersion}}
}

// WorkerApproval is one pending or decided write-tool approval request
// (M1). When a propose-mode Worker hits a write-class tool the runner
// persists this row, fires a mesh alert, and stops the run. The
// operator decides via the HTTP / MCP surface; on Approve the admin
// service fires a NEW run with PreApprovedTools = []string{ToolName}
// so propose-gating skips this single tool. Mid-run resume of the
// original run isn't supported (would need loop snapshotting).
type WorkerApproval struct {
	ID           string     `json:"id"`
	WorkerID     string     `json:"worker_id"`
	RunID        string     `json:"run_id"`
	ToolName     string     `json:"tool_name"`
	ToolInput    string     `json:"tool_input"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"` // pending|approved|rejected
	Decision     string     `json:"decision"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	ResumedRunID string     `json:"resumed_run_id,omitempty"`
}

// WorkerRun is one execution of a Worker. Rows are inserted on dispatch
// (Status="running") and finalized by the runner once the model returns
// or the run cap fires. ModelProvider/ModelID are denormalized so a
// later edit to the Worker config doesn't rewrite history.
//
// MeshMessageIDsJSON and AuditRecordIDsJSON are JSON arrays of foreign
// keys into the mesh + audit tables (no real DB-level FK so cascading
// deletes on those tables don't ripple through the worker_runs ledger).
//
// TriggerKind/TriggerMessageID/TriggerSourcePeer/TriggerChainDepth carry
// M4 mesh-trigger provenance. TriggerKind is "schedule" (default),
// "mesh", or "manual"; the mesh-trigger fields are only populated when
// TriggerKind == "mesh".
type WorkerRun struct {
	ID             string     `json:"id"`
	WorkerID       string     `json:"worker_id"`
	WorkspaceID    string     `json:"workspace_id"` // denormalized from workers; persists after parent worker hard-delete
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	DurationMS     int64      `json:"duration_ms"`
	Status         string     `json:"status"` // running|success|failure|paused|cap_exceeded|awaiting_approval|rejected
	PromptRendered string     `json:"prompt_rendered"`
	ModelProvider  string     `json:"model_provider"`
	ModelID        string     `json:"model_id"`
	InputTokens    int        `json:"input_tokens"`
	OutputTokens   int        `json:"output_tokens"`
	CostUSD        float64    `json:"cost_usd"`
	// BillingModel records how this run was billed: "metered",
	// "subscription", "free", or "unknown". Set by callers at finalize
	// time; this layer stores/reads it as a plain string.
	BillingModel string `json:"billing_model,omitempty"`
	// SubscriptionBucket identifies the subscription plan covering this
	// run: "claude", "grok", "codex", "minimax", "zai", or "" for
	// metered/free runs. Set by callers; persisted as-is.
	SubscriptionBucket string `json:"subscription_bucket,omitempty"`
	// RealCostUSD is the out-of-pocket dollar cost for this run. Zero
	// for subscription or free runs. Set by callers at finalize time.
	RealCostUSD    float64 `json:"real_cost_usd,omitempty"`
	ToolCallsCount int     `json:"tool_calls_count"`
	// ToolCallsCountSource annotates how ToolCallsCount was computed.
	// "native" — the runner populated it from the model adapter's
	// ToolCalls slice (anthropic, openai, openai_compat).
	// "derived" — derived at read time from audit_records, because the
	// adapter family doesn't surface tool_use events natively yet
	// (claude_cli, opencode_cli, grok_cli, mimo_cli — each spawns a child CLI that opens its
	// own MCP connection back to the gateway, so the actual tool calls
	// land in audit_records rather than in the model response). The UI
	// uses this to show a "derived from audit log" tooltip so operators
	// don't conclude a healthy CLI worker is hallucinating. NOT
	// persisted to worker_runs — populated by workers/admin.GetRun /
	// ListRuns post-processing.
	ToolCallsCountSource string `json:"tool_calls_count_source,omitempty"`
	// AccountingMissing flags a SUCCESSFUL run whose adapter reported
	// zero input tokens, zero output tokens AND $0 cost — i.e. the CLI /
	// provider omitted usage telemetry (grok_cli headless JSON does
	// this), not "the run was free". NOT persisted to worker_runs —
	// derived at read time via StampAccountingMissing (same pattern as
	// ToolCallsCountSource) so the UI can render "accounting missing"
	// instead of a misleading $0.00, and the delegation ranker can stop
	// treating missing data as a cost advantage.
	AccountingMissing bool `json:"accounting_missing,omitempty"`
	// ToolCallsCapScope annotates how max_tool_calls applies to this run.
	// "gateway_loop" — enforced by the runner loop on mcplexer-dispatched
	// tool calls (anthropic/openai/openai_compat).
	// "cli_audit" — CLI subprocess tools are counted via audit_records;
	// the runner enforces the cap at finalize and the admin layer re-flags
	// on read. NOT persisted to worker_runs.
	ToolCallsCapScope string `json:"tool_calls_cap_scope,omitempty"`
	// ToolCallsCapExceeded is true when the run exceeded its configured
	// max_tool_calls. NOT persisted — derived at finalize/read time.
	ToolCallsCapExceeded bool `json:"tool_calls_cap_exceeded,omitempty"`
	// DeliverableStatus classifies committed-output truth for delegation
	// review: success_with_output, spend_no_commit, failed_no_output,
	// partial, unknown. NOT persisted — derived from output_text at read.
	DeliverableStatus string `json:"deliverable_status,omitempty"`
	// HasDeliverableCommit/HasDeliverableBranch are parsed from the
	// worker's required final-response contract (commit SHA, branch).
	HasDeliverableCommit bool   `json:"has_deliverable_commit,omitempty"`
	HasDeliverableBranch bool   `json:"has_deliverable_branch,omitempty"`
	DeliverableCommit    string `json:"deliverable_commit,omitempty"`
	DeliverableBranch    string `json:"deliverable_branch,omitempty"`
	// WorkerReportedStatus is the STATUS: line from the worker output
	// (success | blocked | partial).
	WorkerReportedStatus string `json:"worker_reported_status,omitempty"`
	OutputText           string `json:"output_text"`
	Error                string `json:"error"`
	MeshMessageIDsJSON   string `json:"mesh_message_ids_json"`
	AuditRecordIDsJSON   string `json:"audit_record_ids_json"`

	// M4 — trigger provenance. TriggerKind defaults to "schedule" for
	// runs written before the M4 migration. "mesh" sets the remaining
	// three fields; "manual" sets only TriggerKind.
	TriggerKind       string `json:"trigger_kind,omitempty"`
	TriggerMessageID  string `json:"trigger_message_id,omitempty"`
	TriggerSourcePeer string `json:"trigger_source_peer,omitempty"`
	TriggerChainDepth int    `json:"trigger_chain_depth,omitempty"`
}

// StampAccountingMissing computes + sets the derived AccountingMissing
// flag from the run's persisted columns. Call at read time before
// returning a run over an API surface. Returns the computed value for
// callers that only need the predicate.
func (r *WorkerRun) StampAccountingMissing() bool {
	if r == nil {
		return false
	}
	r.AccountingMissing = r.Status == "success" &&
		r.InputTokens == 0 && r.OutputTokens == 0 && r.CostUSD == 0
	return r.AccountingMissing
}

// WorkerMeshTrigger is one trigger row that fires its Worker when a
// matching mesh message arrives (M4). All match fields are AND'd; empty
// means "any". FromFilters is OR'd internally — any matching filter
// admits the message.
//
// ThrottleSeconds bounds how often the same (trigger, source) pair may
// fire — measured per (agent_name|peer_id) so different peers don't
// share a bucket. MaxChainDepth is the reflexive-loop guard; the runner
// stamps a chain-depth tag on its mesh output and the dispatcher refuses
// to fire when the inbound depth meets or exceeds this cap.
//
// StatusFromMatch / StatusToMatch narrow the trigger to
// task_event:status_changed messages with a specific transition. Unlike
// TagMatch (OR-semantics), they are AND'd into the match set: a non-empty
// value requires the message's status_from:/status_to: tag to equal it.
// Empty = "any". This is what lets a trigger fire only when a task (e.g.
// an epic) transitions INTO a given status like "review".
type WorkerMeshTrigger struct {
	ID              string              `json:"id"`
	WorkerID        string              `json:"worker_id"`
	TagMatch        string              `json:"tag_match"`
	KindMatch       string              `json:"kind_match"`
	AudienceMatch   string              `json:"audience_match"`
	ContentRegex    string              `json:"content_regex"`
	StatusFromMatch string              `json:"status_from_match"`
	StatusToMatch   string              `json:"status_to_match"`
	FromFilters     []TriggerFromFilter `json:"from_filters"`
	ThrottleSeconds int                 `json:"throttle_seconds"`
	MaxChainDepth   int                 `json:"max_chain_depth"`
	Enabled         bool                `json:"enabled"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

// TriggerFromFilter narrows a WorkerMeshTrigger to messages originating
// from a particular peer / agent / role. Any non-empty field is an
// equality constraint (peer_id = libp2p ID, agent_name = MeshAgent.Name,
// role = MeshAgent.Role). PeerID "self" matches local-origin messages
// (the host daemon's own sends + non-p2p messages).
type TriggerFromFilter struct {
	PeerID    string `json:"peer_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Role      string `json:"role,omitempty"`
}

// WorkerCostDailyPoint is one (worker, day) cost bucket. Used by the
// workspace-wide cost dashboard so the UI can render a sparkline per
// worker without hitting the full worker_runs table.
type WorkerCostDailyPoint struct {
	Date    string  `json:"date"` // YYYY-MM-DD (UTC day)
	CostUSD float64 `json:"cost_usd"`
}

// WorkerCostAggregate is one Worker's slice of the dashboard ledger:
// last-N-days daily costs + month-to-date total + 30-day run count.
// Constructed by the sqlite WorkerCostAggregate query so the HTTP
// layer doesn't have to fan out N queries per worker.
type WorkerCostAggregate struct {
	WorkerID       string                 `json:"worker_id"`
	WorkerName     string                 `json:"worker_name"`
	WorkspaceID    string                 `json:"workspace_id"`
	DailyCosts     []WorkerCostDailyPoint `json:"daily_costs"`
	MonthToDateUSD float64                `json:"month_to_date_usd"`
	RunCount30D    int                    `json:"run_count_30d"`
}

// WorkerRunFinalize captures the columns the runner writes when a run
// transitions out of "running". Fields are passed by-value so a nil-ish
// finalize is impossible: every terminal-status update must commit the
// full snapshot. FinishedAt is required and used to derive DurationMS
// against the row's StartedAt.
type WorkerRunFinalize struct {
	Status             string
	FinishedAt         time.Time
	InputTokens        int
	OutputTokens       int
	CostUSD            float64
	ToolCallsCount     int
	OutputText         string
	Error              string
	MeshMessageIDsJSON string
	AuditRecordIDsJSON string
	BillingModel       string
	SubscriptionBucket string
	RealCostUSD        float64
}
