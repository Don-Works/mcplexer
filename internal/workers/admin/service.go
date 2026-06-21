// Package admin owns the CWD-gated mcplexer__*_worker MCP tool surface
// (M0.5). It exposes a small Service that wraps store.WorkerStore with
// validation, defaulting, and id generation so the gateway handler layer
// stays thin — just JSON-marshalling on the way in and out.
//
// The runner dependency is intentionally optional: when nil, RunNow
// returns a stub run row (status="running") with a TODO and a clear
// audit trail that the actual exec wiring is pending M0.3 landing.
package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// Runner abstracts the M0.3 runner so the admin service can fire an
// ad-hoc run without importing the full runner symbol everywhere.
// *runner.Runner satisfies this naturally.
type Runner interface {
	RunWithOpts(ctx context.Context, workerID string, opts runner.RunOpts) (string, error)
	// Cancel requests a hard stop for a live run: it interrupts the
	// runner goroutine and kills any in-flight model subprocess group,
	// then lets the runner finalize the row as the single writer of the
	// terminal `cancelled` state. reason is stamped onto that row.
	// Returns true when a live execution was found and signalled.
	Cancel(runID, reason string) bool
}

// OpenCodeRuntime is the small slice of the managed opencode subprocess
// needed by delegation. *opencode.Manager satisfies it in production.
type OpenCodeRuntime interface {
	Start(ctx context.Context) error
	Endpoint() string
}

// Auditor lets the admin service emit worker_approval.decided records
// when an operator approves / rejects a pending approval. Optional —
// nil makes every emit a no-op so non-daemon paths (CLI, tests) don't
// need to wire the audit pipeline.
type Auditor interface {
	Record(ctx context.Context, rec *store.AuditRecord) error
}

// AuditCounter is the read-side counterpart to Auditor. It backs the
// tool_calls_count derive-at-read-time fix for the claude_cli /
// opencode_cli / grok_cli adapter families (see store.AuditStore.CountChildCLIToolCalls
// for the rationale). Optional — when nil, GetRun/ListRuns leave the
// adapter-reported ToolCallsCount alone and stamp source="native".
type AuditCounter interface {
	CountChildCLIToolCalls(ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string) (int, error)
}

// Clock is the time abstraction the service uses for stub-run rows; tests
// can pin it for deterministic finished_at values.
type Clock interface {
	Now() time.Time
}

// realClock returns time.Now().UTC().
type realClock struct{}

// Now implements Clock.
func (realClock) Now() time.Time { return time.Now().UTC() }

// Service is the worker admin facade. Construct via New.
type Service struct {
	store         store.WorkerStore
	meshStore     store.MeshStore // optional — set via SetMeshStore for delegation review auto-ack
	workspaces    WorkspaceLister // optional — needed only when ListInput.WorkspaceID is empty
	authScopes    store.AuthScopeStore
	modelProfiles store.ModelProfileStore
	runner        Runner // optional — nil = stub RunNow
	clock         Clock
	// validateScheduleSpec is injected so tests can avoid pulling the
	// scheduler package via a stub. In production it points at
	// scheduler.NextRun-derived validation.
	validateScheduleSpec func(spec string) error
	// scheduleBridge keeps the ScheduledJob catalog aligned with the
	// Worker catalog (M0.4). Optional — nil means "no bridge wired";
	// CRUD calls then proceed without touching scheduled_jobs.
	scheduleBridge ScheduleBridge
	// templates is the publishable Worker template surface (M3). Optional —
	// when nil PublishAsTemplate / InstallFromTemplate return errors.
	templates TemplatePublisher
	// auditor records approval decisions. Optional; nil → no audit.
	auditor Auditor

	// auditCounter resolves the derive-at-read-time tool_calls_count
	// for CLI adapter runs (claude_cli / opencode_cli / grok_cli / mimo_cli). Optional; nil
	// → GetRun/ListRuns return ToolCallsCount verbatim from the run row
	// and stamp ToolCallsCountSource="native".
	auditCounter AuditCounter

	// M4 — mesh trigger surfaces. All optional; the CRUD methods
	// surface a structured error when their dependency is missing.
	meshTriggerStore   MeshTriggerStore
	peerScopeStore     PeerScopeStore
	dispatcherReloader DispatcherReloader

	// runBus is the live worker-run event bus, shared with the runner.
	// The HTTP SSE handler reads it via RunBus(). Optional; nil makes
	// the SSE surface fall back to a snapshot-only response (single
	// status frame on connect, then close — no live updates).
	runBus *runner.RunBus

	// openCodeRuntime is the managed `opencode serve` subprocess. When
	// wired, opencode_cli delegations can auto-start it and attach via
	// the stable loopback endpoint instead of racing raw CLI processes.
	openCodeRuntime OpenCodeRuntime

	// lifecycleCtx is the daemon's shutdown context. Detached work
	// (delegation dispatch goroutines) derives its context from here so
	// daemon shutdown cancels it instead of leaking runs rooted at
	// context.Background. Optional — nil falls back to Background for
	// tests / stdio callers.
	lifecycleCtx context.Context
}

// SetLifecycleContext wires the daemon's shutdown context so detached
// delegation dispatches are cancelled on daemon shutdown.
func (s *Service) SetLifecycleContext(ctx context.Context) {
	s.lifecycleCtx = ctx
}

// lifecycleContext returns the wired daemon shutdown context, or
// context.Background when none was set.
func (s *Service) lifecycleContext() context.Context {
	if s.lifecycleCtx != nil {
		return s.lifecycleCtx
	}
	return context.Background()
}

// SetAuditor wires the audit logger so approval decisions land in the
// audit ledger. Nil-safe.
func (s *Service) SetAuditor(a Auditor) {
	s.auditor = a
}

func (s *Service) SetMeshStore(ms store.MeshStore) {
	s.meshStore = ms
}

// WorkspaceLister lets the admin service walk every workspace when the
// caller hasn't passed a workspace_id filter. Implemented by store.Store
// (and any test fake that needs cross-workspace listing).
type WorkspaceLister interface {
	ListWorkspaces(ctx context.Context) ([]store.Workspace, error)
}

// Options configure the Service. Every collaborator besides WorkerStore
// is optional. ScheduleValidator falls back to a permissive default
// that accepts any non-empty spec — production wiring always injects
// the scheduler-backed validator.
type Options struct {
	Workspaces        WorkspaceLister
	AuthScopes        store.AuthScopeStore
	ModelProfiles     store.ModelProfileStore
	Runner            Runner
	Clock             Clock
	ScheduleValidator func(spec string) error
	// ScheduleBridge keeps scheduled_jobs aligned with workers (M0.4).
	// Optional at construction; callers that build the bridge with a
	// scheduler reference may instead invoke SetScheduleBridge after
	// New to break the import cycle.
	ScheduleBridge ScheduleBridge
	// RunBus is the shared live-run event bus the runner publishes to.
	// When wired, the HTTP SSE surface fans events out to browser tabs
	// without polling the DB. Optional — nil is safe.
	RunBus *runner.RunBus
	// AuditCounter resolves the derive-at-read-time tool_calls_count
	// for CLI adapter runs (claude_cli / opencode_cli / grok_cli / mimo_cli). Optional;
	// production wiring passes the sqlite Store which satisfies this
	// interface naturally. When nil, GetRun/ListRuns leave
	// ToolCallsCount alone and stamp source="native".
	AuditCounter AuditCounter
	// OpenCodeRuntime is optional; daemon wiring passes the managed
	// runtime so opencode_cli delegations can start/use the server.
	OpenCodeRuntime OpenCodeRuntime
}

// New constructs a Service.
func New(s store.WorkerStore, opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.ScheduleValidator == nil {
		opts.ScheduleValidator = defaultSpecValidator
	}
	if opts.AuthScopes == nil {
		if v, ok := s.(store.AuthScopeStore); ok {
			opts.AuthScopes = v
		}
	}
	if opts.ModelProfiles == nil {
		if v, ok := s.(store.ModelProfileStore); ok {
			opts.ModelProfiles = v
		}
	}
	return &Service{
		store:                s,
		workspaces:           opts.Workspaces,
		authScopes:           opts.AuthScopes,
		modelProfiles:        opts.ModelProfiles,
		runner:               opts.Runner,
		clock:                opts.Clock,
		validateScheduleSpec: opts.ScheduleValidator,
		scheduleBridge:       opts.ScheduleBridge,
		runBus:               opts.RunBus,
		auditCounter:         opts.AuditCounter,
		openCodeRuntime:      opts.OpenCodeRuntime,
	}
}

// RunBus returns the live worker-run event bus, or nil when unwired.
// The HTTP SSE handler uses this to subscribe to mid-flight events
// without coupling the api package to the runner's internal layout.
func (s *Service) RunBus() *runner.RunBus {
	return s.runBus
}

// ErrModelProfilesUnavailable is returned by the ModelProfiles facade
// when no ModelProfileStore was wired into the Service. The MCP handler
// layer maps it to a structured "not available" errorResult instead of
// nil-panicking.
var ErrModelProfilesUnavailable = errors.New("model profile store not available")

// ModelProfiles returns a ModelProfileCore backed by the Service's wired
// store, or nil when no store was injected. The CWD-gated MCP tools in
// internal/control reach the SAME validation + Builtin/secret rules as
// the HTTP handlers through this accessor — there is exactly one core
// implementation, so the two surfaces cannot drift.
func (s *Service) ModelProfiles() *ModelProfileCore {
	if s.modelProfiles == nil {
		return nil
	}
	return NewModelProfileCore(s.modelProfiles)
}

// defaultSpecValidator accepts any non-empty trimmed string, plus the
// manual sentinel (schedule_spec="manual" → mesh / RunNow only). Real
// production wiring uses scheduler.NextRun.
func defaultSpecValidator(spec string) error {
	if scheduler.IsManualSpec(spec) {
		return nil
	}
	if strings.TrimSpace(spec) == "" {
		return errors.New("schedule_spec required")
	}
	return nil
}

// WorkerSummary is the slim row returned by List. We compute last_run_*
// per worker so admin agents can triage at a glance without a follow-up
// list_runs call.
type WorkerSummary struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	ModelProvider string     `json:"model_provider"`
	ModelID       string     `json:"model_id"`
	ScheduleSpec  string     `json:"schedule_spec"`
	Enabled       bool       `json:"enabled"`
	LastRunStatus string     `json:"last_run_status,omitempty"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	WorkspaceID   string     `json:"workspace_id"`

	// Ephemeral workers are one-shot contexts created by Delegate. They
	// live in the same workers table so runs/cost/audit keep one ledger,
	// but operator UIs should route them to the Delegations surface instead
	// of treating them as durable scheduled workers.
	Ephemeral            bool   `json:"ephemeral,omitempty"`
	DelegationID         string `json:"delegation_id,omitempty"`
	DelegationObjective  string `json:"delegation_objective,omitempty"`
	DelegationTaskID     string `json:"delegation_task_id,omitempty"`
	DelegationTaskKind   string `json:"delegation_task_kind,omitempty"`
	DelegationWorkerMode string `json:"delegation_worker_mode,omitempty"`
}

// ListInput matches the mcplexer__list_workers tool input schema.
type ListInput struct {
	EnabledOnly bool   `json:"enabled_only,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	NamePattern string `json:"name_pattern,omitempty"`
}

// List returns workers across the configured workspace(s). When
// WorkspaceID is empty all workspaces are scanned (admin convenience
// today; M0.6 will narrow to the active workspace by default).
func (s *Service) List(ctx context.Context, in ListInput) ([]WorkerSummary, error) {
	workspaces, err := s.collectWorkspaceIDs(ctx, in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkerSummary, 0)
	for _, wsID := range workspaces {
		ws, lerr := s.store.ListWorkers(ctx, wsID, in.EnabledOnly)
		if lerr != nil {
			return nil, fmt.Errorf("list workers in %s: %w", wsID, lerr)
		}
		for _, w := range ws {
			if !matchesNamePattern(w.Name, in.NamePattern) {
				continue
			}
			out = append(out, s.summarize(ctx, w))
		}
	}
	return out, nil
}

// collectWorkspaceIDs returns either the provided id or every workspace
// id in the store (for the "no filter" case). We surface a typed error
// when the explicit id is missing and no WorkspaceLister was wired.
func (s *Service) collectWorkspaceIDs(
	ctx context.Context, explicit string,
) ([]string, error) {
	if explicit != "" {
		return []string{explicit}, nil
	}
	if s.workspaces == nil {
		return nil, errors.New(
			"workspace_id is required (no workspace lister wired)",
		)
	}
	all, err := s.workspaces.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	ids := make([]string, 0, len(all))
	for _, w := range all {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// summarize converts a Worker plus its most recent run into a thin row.
// last_run_* errors are swallowed (an empty run history just means no
// run yet) — they're a UI hint, not a load-bearing field.
func (s *Service) summarize(ctx context.Context, w *store.Worker) WorkerSummary {
	row := WorkerSummary{
		ID:            w.ID,
		Name:          w.Name,
		ModelProvider: w.ModelProvider,
		ModelID:       w.ModelID,
		ScheduleSpec:  w.ScheduleSpec,
		Enabled:       w.Enabled,
		CreatedAt:     w.CreatedAt,
		WorkspaceID:   w.WorkspaceID,
	}
	if meta, ok := parseDelegationMetadata(w.ParametersJSON); ok {
		row.Ephemeral = true
		row.DelegationID = meta.ID
		row.DelegationObjective = meta.Objective
		row.DelegationTaskID = meta.TaskID
		row.DelegationTaskKind = meta.TaskKind
		row.DelegationWorkerMode = meta.WorkerMode
	}
	runs, err := s.store.ListWorkerRuns(ctx, w.ID, 1)
	if err == nil && len(runs) > 0 {
		row.LastRunStatus = runs[0].Status
		ts := runs[0].StartedAt
		row.LastRunAt = &ts
	}
	return row
}

// matchesNamePattern returns true when the pattern is empty or when
// strings.Contains(name, pattern) (case-insensitive, no glob — the M0
// surface stays small).
func matchesNamePattern(name, pattern string) bool {
	if pattern == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(pattern))
}

// GetInput is the mcplexer__get_worker arg payload.
type GetInput struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// GetOutput bundles the full Worker with a recent_runs slice.
type GetOutput struct {
	Worker     *store.Worker      `json:"worker"`
	RecentRuns []*store.WorkerRun `json:"recent_runs"`
}

// Get resolves one Worker by id, or by (workspace_id + name). At least
// one of those must be present. recent_runs always serialises as a JSON
// array — never `null` — so the dashboard / MCP callers can iterate
// without an extra nil check on freshly-created workers.
func (s *Service) Get(ctx context.Context, in GetInput) (*GetOutput, error) {
	w, err := s.resolveWorker(ctx, in.ID, in.WorkspaceID, in.Name)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.ListWorkerRuns(ctx, w.ID, 5)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	if runs == nil {
		runs = []*store.WorkerRun{}
	}
	for _, run := range runs {
		run.StampAccountingMissing()
	}
	s.annotateRunsToolCallsSource(ctx, runs)
	return &GetOutput{Worker: w, RecentRuns: runs}, nil
}

// resolveWorker looks up by id, then by (workspaceID, name). Returns an
// "id required" error when both lookups are unspecified.
func (s *Service) resolveWorker(
	ctx context.Context, id, workspaceID, name string,
) (*store.Worker, error) {
	if id != "" {
		return s.store.GetWorker(ctx, id)
	}
	if name == "" || workspaceID == "" {
		return nil, errors.New("id, or (workspace_id+name), required")
	}
	return s.store.GetWorkerByName(ctx, workspaceID, name)
}

// defaultStr returns fallback when s is empty.
func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
