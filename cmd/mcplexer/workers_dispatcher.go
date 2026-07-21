package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
	"github.com/don-works/mcplexer/internal/workers/runner"
	"github.com/don-works/mcplexer/internal/workers/writeclass"
)

// toolDispatcher is the runner's gateway hook. Workers see exactly two
// tools at the model layer: mcpx__search_tools and mcpx__execute_code.
// Every other capability (downstream MCP, mesh, secret, memory, admin)
// is reachable from inside an execute_code snippet, which routes through
// the gateway's full pipeline (sanitize, approval, audit, code-mode
// sandbox) via the BuiltinToolCaller.
//
// The store + engine + manager fields are retained for backwards-
// compatible construction (the M0 dispatcher rooted in them is now an
// unused fallback path; we kept the wiring rather than rip up every test
// that constructs a toolDispatcher directly) and may be removed in a
// follow-up once the test suite no longer constructs dispatchers that
// pre-date BuiltinToolCaller.
//
// Worker tool allowlists are attached to the gateway context before
// dispatching these builtins. The gateway applies that allowlist to
// mcpx__search_tools discovery and to every inner tool call made from
// inside mcpx__execute_code.
type toolDispatcher struct {
	store   store.Store
	engine  *routing.Engine
	manager *downstream.Manager
	builtin runner.BuiltinToolCaller // late-bound via SetBuiltinCaller
}

func newToolDispatcher(s store.Store, e *routing.Engine, m *downstream.Manager) *toolDispatcher {
	return &toolDispatcher{store: s, engine: e, manager: m}
}

// SetBuiltinCaller wires the gateway-backed builtin caller after the
// dispatcher has been constructed. The wiring order in serve.go builds
// the dispatcher (inside buildWorkerRunner) before the worker-bound
// gateway.Server exists, so this is the seam that closes the loop.
// Calling with nil disables the two-tool surface (workers see zero
// tools, every dispatch fails — fail-closed beats fail-open).
func (d *toolDispatcher) SetBuiltinCaller(c runner.BuiltinToolCaller) {
	d.builtin = c
}

// ReleaseBrowserSession tears down any per-session browser-automation
// instances this worker spawned (keyed "worker:<id>" in withWorkerAllowlist).
// The runner calls it from its finalize defer when a run completes so the
// worker's headless browser process dies promptly instead of lingering until
// the idle timer or instance cap reaps it. Satisfies the runner's optional
// browserSessionReleaser capability. No-op when the manager is unwired.
func (d *toolDispatcher) ReleaseBrowserSession(sessionID string) {
	if d.manager == nil {
		return
	}
	d.manager.ReleaseSession(sessionID)
}

// ListTools returns the two-tool surface workers see at the model layer:
// mcpx__search_tools and mcpx__execute_code. Everything else is reachable
// from inside an execute_code snippet through the gateway's full pipeline.
//
// The allowlist argument is retained on the interface for backwards
// compatibility. A non-empty worker allowlist no longer filters the
// top-level two-tool surface; instead it is enforced by the gateway for
// discovery results and sandbox inner calls. An empty (but non-nil)
// allowlist still fails closed (zero tools) as the operator's explicit
// "deny everything" signal, and is checked BEFORE the builtin-wired
// check so the deny contract holds regardless of wiring state.
//
// SECURITY: fails closed when the BuiltinToolCaller has not been wired.
// A silent fallback to "list every downstream tool" would defeat the
// whole point of the two-tool surface: an operator who deliberately
// constrained workers to mcpx__search_tools + mcpx__execute_code would
// suddenly have workers seeing every github/linear/slack/customer tool
// directly if any wiring path forgot to call SetBuiltinCaller. Fail
// loudly so the misconfiguration surfaces at the first worker run.
func (d *toolDispatcher) ListTools(ctx context.Context, allowlist []string) ([]models.ToolSchema, error) {
	if allowlist != nil && len(allowlist) == 0 {
		return []models.ToolSchema{}, nil
	}
	if d.builtin == nil {
		return nil, errors.New("worker dispatcher: BuiltinToolCaller not wired — call SetBuiltinCaller before running workers")
	}
	if runCtx, ok := runner.WorkerRunCtxFromContext(ctx); ok && runCtx.FilesystemRoot != "" {
		var err error
		ctx, err = gateway.WithWorkerFilesystemScope(
			ctx, runCtx.FilesystemRoot, runCtx.WorkspacePath, runCtx.ClaimedPaths,
		)
		if err != nil {
			return nil, fmt.Errorf("worker dispatcher tool-surface scope: %w", err)
		}
	}
	return d.builtin.WorkerToolSurface(ctx), nil
}

// filterToolsByAllowlist is the pure half of ListTools — given the raw
// per-server listings, the namespace map, and the allowlist slice,
// return the filtered ToolSchema slice. Separated so unit tests can
// drive every allowlist branch without standing up a real Manager.
func filterToolsByAllowlist(
	raw map[string]json.RawMessage,
	nsByServer map[string]string,
	allowlist []string,
) []models.ToolSchema {
	allowed := allowlistSet(allowlist)
	out := make([]models.ToolSchema, 0)
	for serverID, payload := range raw {
		for _, schema := range parseToolListing(payload, nsByServer[serverID]) {
			if allowed != nil {
				if _, ok := allowed[schema.Name]; !ok {
					continue
				}
			}
			out = append(out, schema)
		}
	}
	return out
}

// allowlistSet returns a set keyed by tool name, or nil when the
// caller passed nil (= "no allowlist configured, no filtering"). An
// empty slice is treated by the caller separately so this helper
// doesn't need to distinguish nil vs empty here.
func allowlistSet(allowlist []string) map[string]struct{} {
	if allowlist == nil {
		return nil
	}
	out := make(map[string]struct{}, len(allowlist))
	for _, n := range allowlist {
		out[n] = struct{}{}
	}
	return out
}

// parseToolListing decodes one server's tools/list payload into the
// runner's ToolSchema shape, prepending the configured namespace when
// the raw tool name isn't already namespaced.
func parseToolListing(payload []byte, namespace string) []models.ToolSchema {
	var listing struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(payload, &listing); err != nil {
		return nil
	}
	out := make([]models.ToolSchema, 0, len(listing.Tools))
	for _, t := range listing.Tools {
		name := t.Name
		if namespace != "" && !strings.Contains(name, "__") {
			name = namespace + "__" + t.Name
		}
		schema := map[string]any{}
		if len(t.InputSchema) > 0 {
			_ = json.Unmarshal(t.InputSchema, &schema)
		}
		out = append(out, models.ToolSchema{
			Name:        name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

// DispatchTool routes the worker's tool call. With the two-tool surface
// (mcpx__search_tools + mcpx__execute_code) workers should only ever ask
// the dispatcher to invoke one of those two names. Any other name is a
// model hallucination — the worker's tool inventory doesn't list it —
// and we fail closed with a clear message rather than try to route it.
//
// Both supported names delegate to the BuiltinToolCaller, which runs the
// call through the gateway's full pipeline (sanitize, approval, audit,
// code-mode sandbox). WriteClass is set to false because the two builtins
// are themselves stateless wrappers; the side effects happen inside the
// execute_code sandbox where the gateway's own write-class auditing
// fires per inner call.
//
// SECURITY: fails closed when the BuiltinToolCaller has not been wired.
// See ListTools for the rationale — a silent fallback to engine.Route
// would let workers reach downstream tools directly, bypassing the
// two-tool surface contract.
func (d *toolDispatcher) DispatchTool(ctx context.Context, call runner.ToolCallRequest) (runner.ToolCallResult, error) {
	if d.builtin == nil {
		return runner.ToolCallResult{}, errors.New("worker dispatcher: BuiltinToolCaller not wired — call SetBuiltinCaller before running workers")
	}
	return d.dispatchBuiltin(ctx, call)
}

// dispatchBuiltin routes the call through the gateway-backed builtin
// caller. Rejects non-builtin names with a clear error so a hallucinated
// downstream name (e.g. the model wrote `github__create_issue` directly
// instead of going through execute_code) fails loudly rather than
// silently routing.
func (d *toolDispatcher) dispatchBuiltin(ctx context.Context, call runner.ToolCallRequest) (runner.ToolCallResult, error) {
	switch call.Name {
	case "mcpx__search_tools", "mcpx__execute_code":
		// supported — fall through
	default:
		return runner.ToolCallResult{
			OutputJSON: fmt.Sprintf(`{"error":"tool %q is not in the worker tool surface (workers only get mcpx__search_tools + mcpx__execute_code directly). Use mcpx__search_tools then mcpx__execute_code with JS (mcpx.* / task.* namespaces available inside the snippet; print() to capture results). The worker allowlist gates what execute_code can reach."}`, call.Name),
			IsError:    true,
		}, nil
	}
	args := json.RawMessage(call.InputJSON)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var scopeErr error
	ctx, scopeErr = d.withWorkerAllowlist(ctx, call)
	if scopeErr != nil {
		return runner.ToolCallResult{
			OutputJSON: fmt.Sprintf(`{"error":%q}`, scopeErr.Error()),
			IsError:    true,
		}, nil
	}
	out, err := d.builtin.CallBuiltin(ctx, call.Name, args)
	if err != nil {
		return runner.ToolCallResult{
			OutputJSON: fmt.Sprintf(`{"error":%q}`, err.Error()),
			IsError:    true,
		}, nil
	}
	return runner.ToolCallResult{
		OutputJSON: string(out),
		IsError:    extractIsError(out),
		WriteClass: false,
	}, nil
}

func (d *toolDispatcher) withWorkerAllowlist(
	ctx context.Context, call runner.ToolCallRequest,
) (context.Context, error) {
	// In-process workers all share one uninitialized gateway session, so the
	// gateway's session-id fallback would map every worker's browser calls to
	// the same shared instance. Scope by worker id instead: stable across a
	// worker's repeated runs (one warm browser reused, idle-reaped between
	// runs) and distinct from every other worker and interactive session.
	if call.WorkerID != "" {
		ctx = downstream.WithBrowserSessionID(ctx, "worker:"+call.WorkerID)
	}
	worker, err := d.lookupWorker(ctx, call.WorkerID)
	if err != nil || worker == nil {
		slog.Warn("worker context lookup failed; failing closed",
			"worker_id", call.WorkerID, "error", err)
		ctx = gateway.WithWorkerToolAllowlist(ctx, []string{})
		// Fail closed on capability too: a non-nil deny-everything profile
		// (NamespaceAllow=[] excludes every non-mcpx namespace) so a worker
		// we cannot identify reaches nothing beyond the irreducible mcpx
		// entrypoint.
		return gateway.WithWorkerCapabilityProfile(ctx, denyEverythingCapabilityProfile()), nil
	}
	requiresIsolation, policyErr := runner.DelegationIsolationRequired(worker.ParametersJSON)
	if policyErr != nil {
		return ctx, fmt.Errorf("worker isolation metadata: %w", policyErr)
	}
	runCtx, hasRunCtx := runner.WorkerRunCtxFromContext(ctx)
	if requiresIsolation && (!hasRunCtx || runCtx.FilesystemRoot == "") {
		return ctx, errors.New("persisted worktree isolation requires a matching runtime worker scope")
	}
	if !requiresIsolation && hasRunCtx && runCtx.FilesystemRoot != "" {
		return ctx, errors.New("runtime worker filesystem scope conflicts with persisted isolation metadata")
	}
	allowlist := parseAllowlistPatternsForWorker(worker.ID, worker.ToolAllowlistJSON)
	profile := parseCapabilityProfileForWorker(worker.ID, worker.CapabilityProfileJSON)
	if requiresIsolation {
		if allowlist == nil {
			allowlist = []string{}
		}
		if profile == nil {
			reviewOnly, reviewErr := runner.DelegationIsolationReviewOnly(worker.ParametersJSON)
			if reviewErr != nil {
				return ctx, fmt.Errorf("worker isolation role: %w", reviewErr)
			}
			if reviewOnly {
				profile = toolgate.Researcher()
			} else {
				profile = toolgate.Coder()
			}
		}
	}
	ctx = gateway.WithWorkerToolAllowlist(ctx, allowlist)
	ctx = gateway.WithWorkerCapabilityProfile(ctx, profile)
	preferredRoot := ""
	if hasRunCtx && runCtx.FilesystemRoot != "" {
		if runCtx.WorkerID != call.WorkerID || runCtx.RunID != call.RunID {
			return ctx, errors.New("worker filesystem scope does not match tool-call run identity")
		}
		var err error
		ctx, err = gateway.WithWorkerFilesystemScope(
			ctx, runCtx.FilesystemRoot, runCtx.WorkspacePath, runCtx.ClaimedPaths,
		)
		if err != nil {
			return ctx, fmt.Errorf("worker filesystem scope: %w", err)
		}
		preferredRoot = runCtx.WorkspacePath
	}
	ctx = gateway.WithWorkerWorkspaceAccess(
		ctx,
		worker.WorkspaceID,
		d.workerWorkspaceGrants(ctx, worker, preferredRoot),
	)
	return ctx, nil
}

func (d *toolDispatcher) lookupWorker(ctx context.Context, workerID string) (*store.Worker, error) {
	if workerID == "" || d.store == nil {
		return nil, errors.New("worker store unavailable")
	}
	return d.store.GetWorker(ctx, workerID)
}

func (d *toolDispatcher) workerWorkspaceGrants(
	ctx context.Context, worker *store.Worker, preferredRoot string,
) []gateway.WorkerWorkspaceGrant {
	if worker == nil {
		return nil
	}
	grants := worker.WorkspaceAccess
	if len(grants) == 0 && worker.WorkspaceID != "" {
		grants = []store.WorkerWorkspaceAccess{{
			WorkspaceID: worker.WorkspaceID,
			Access:      store.WorkerWorkspaceAccessWrite,
		}}
	}
	out := make([]gateway.WorkerWorkspaceGrant, 0, len(grants))
	for _, g := range grants {
		gg := gateway.WorkerWorkspaceGrant{
			WorkspaceID: g.WorkspaceID,
			Access:      g.Access,
		}
		if d.store != nil && g.WorkspaceID != "" {
			if ws, err := d.store.GetWorkspace(ctx, g.WorkspaceID); err == nil && ws != nil {
				gg.WorkspaceName = ws.Name
				gg.RootPath = ws.RootPath
			}
		}
		if g.WorkspaceID == worker.WorkspaceID && preferredRoot != "" {
			gg.RootPath = preferredRoot
		}
		out = append(out, gg)
	}
	return out
}

// extractIsError parses the isError flag out of an MCP tools/call
// response envelope. The envelope shape is
// `{"content":[...],"isError":bool}` — when the field is missing or the
// JSON is malformed we return false (treat as success) because the
// envelope's content carries the actual error text either way.
func extractIsError(envelope json.RawMessage) bool {
	if len(envelope) == 0 {
		return false
	}
	var parsed struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(envelope, &parsed); err != nil {
		return false
	}
	return parsed.IsError
}

func parseAllowlistPatternsForWorker(workerID, raw string) []string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(s), &names); err != nil {
		slog.Warn("worker allowlist parse failed; failing closed (deny-everything)",
			"worker_id", workerID, "allowlist", raw, "error", err)
		return []string{} // explicit empty slice, NOT nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// parseCapabilityProfileForWorker decodes the worker's
// capability_profile_json column into a *toolgate.CapabilityProfile.
//
//   - Empty / "null" => nil (no profile => allow-all, back-compat).
//   - Parse error => fail closed: a non-nil deny-everything profile
//     (NamespaceAllow=[] denies every namespace except the irreducible
//     mcpx entrypoint) so a corrupt column denies rather than widens.
//     Mirrors parseAllowlistPatternsForWorker's fail-closed contract.
func parseCapabilityProfileForWorker(workerID, raw string) *toolgate.CapabilityProfile {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var p toolgate.CapabilityProfile
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		slog.Warn("worker capability profile parse failed; failing closed (deny-everything)",
			"worker_id", workerID, "error", err)
		return denyEverythingCapabilityProfile()
	}
	return &p
}

// denyEverythingCapabilityProfile returns the tightest fail-closed profile
// for an unidentifiable / corrupt-profile worker: toolgate.Minimal().
//
// SECURITY: a bare &CapabilityProfile{NamespaceAllow:[]} is NOT
// deny-everything — its feature flags are nil, and nil features default to
// ALLOWED, so the mcpx-tool always-allow would still let a worker we cannot
// identify reach mcpx__delegate_worker / mcpx__invoke_model (sub-delegation
// + model spend) before any namespace gate runs. Minimal() additionally
// pins every may_* feature flag explicitly false, so the feature-derived
// tool-deny (which runs BEFORE the mcpx bypass) blocks those mcpx tools. The
// result: only the irreducible mcpx__search_tools + mcpx__execute_code
// entrypoints survive — true deny-everything-else.
func denyEverythingCapabilityProfile() *toolgate.CapabilityProfile {
	return toolgate.Minimal()
}

// Classify is the runner.ToolDispatcher hook the runner consults BEFORE
// DispatchTool. Backed by the shared writeclass.IsWriteClass heuristic
// (same one the UI surfaces and the post-dispatch stamping uses) so
// both legs of the propose-mode SECURITY contract agree.
func (d *toolDispatcher) Classify(name string) bool {
	return writeclass.IsWriteClass(name)
}
