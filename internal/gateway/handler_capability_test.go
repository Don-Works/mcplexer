package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
	"github.com/don-works/mcplexer/internal/workers/writeclass"
)

// expectedMemoryReadTools is the test's INDEPENDENT canonical list of
// READ-class memory builtins, kept separate from toolgate.memoryReadTools
// (unexported) so the two can be cross-checked. If they drift, the
// enumeration tests below fail. memory__request_memory is write (pulls a
// peer entry into the local store) and is intentionally absent.
var expectedMemoryReadTools = map[string]bool{
	"memory__recall":               true,
	"memory__recall_about":         true,
	"memory__get":                  true,
	"memory__list":                 true,
	"memory__list_entities":        true,
	"memory__related_entities":     true,
	"memory__spreading_activation": true,
	"memory__co_recalled":          true,
	"memory__suggestions":          true,
}

// expectedTaskReadTools is the test's INDEPENDENT canonical list of
// READ-class task builtins (see expectedMemoryReadTools).
var expectedTaskReadTools = map[string]bool{
	"task__get":              true,
	"task__list":             true,
	"task__list_milestones":  true,
	"task__list_offers":      true,
	"task__list_attachments": true,
	"task__get_attachment":   true,
	"task__recent_activity":  true,
	"task__heartbeat":        true,
}

// registeredMemoryToolNames enumerates EVERY memory__* builtin the gateway
// advertises (capability flags forced on so optional tools are included).
func registeredMemoryToolNames() []string {
	defs := memoryToolDefinitions(memoryToolCaps{HasEmbedder: true, RecallTracking: true})
	var out []string
	for _, d := range defs {
		if strings.HasPrefix(d.Name, "memory__") {
			out = append(out, d.Name)
		}
	}
	return out
}

// registeredTaskToolNames enumerates EVERY task__* builtin (the task__
// namespace only; task_status_vocabulary__* and the separate admin defs are
// excluded — admin is hard-denied independently via IsAdminTool).
func registeredTaskToolNames() []string {
	var out []string
	for _, d := range taskToolDefinitions() {
		if strings.HasPrefix(d.Name, "task__") {
			out = append(out, d.Name)
		}
	}
	return out
}

// TestCapabilityWriteDenyEnumeratesRealMemoryBuiltins is the drift guard for
// HIGH-3: it drives the REAL toolgate gate (via checkWorkerCapability +
// writeclass) over the FULL enumerated set of registered memory__* builtins.
// Under both the researcher preset AND a custom may_write_memory=false
// profile, every memory builtin NOT in the read allowlist MUST be denied.
// A new write tool added without deny coverage fails this test because it
// won't be in expectedMemoryReadTools, so the test demands denial — and the
// drift-proof gate (deny everything not in its read allowlist) delivers it.
func TestCapabilityWriteDenyEnumeratesRealMemoryBuiltins(t *testing.T) {
	names := registeredMemoryToolNames()
	if len(names) == 0 {
		t.Fatal("no memory builtins enumerated — registry wiring changed")
	}
	profiles := map[string]*toolgate.CapabilityProfile{
		"researcher":             toolgate.Researcher(),
		"may_write_memory=false": {Features: toolgate.CapabilityFeatures{MayWriteMemory: boolPtrTest(false)}},
	}
	for pName, profile := range profiles {
		ctx := WithWorkerCapabilityProfile(context.Background(), profile)
		for _, tool := range names {
			err := checkWorkerCapability(ctx, tool)
			if expectedMemoryReadTools[tool] {
				if err != nil {
					t.Errorf("%s: read tool %q denied: %v", pName, tool, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s: WRITE tool %q LEAKED (not denied) — add it to the read allowlist or confirm it is a mutator that must be denied", pName, tool)
			}
		}
	}
}

// TestCapabilityWriteDenyEnumeratesRealTaskBuiltins is the HIGH-4 twin of the
// memory drift guard, over the full registered task__* surface.
func TestCapabilityWriteDenyEnumeratesRealTaskBuiltins(t *testing.T) {
	names := registeredTaskToolNames()
	if len(names) == 0 {
		t.Fatal("no task builtins enumerated — registry wiring changed")
	}
	profiles := map[string]*toolgate.CapabilityProfile{
		"researcher":            toolgate.Researcher(),
		"may_write_tasks=false": {Features: toolgate.CapabilityFeatures{MayWriteTasks: boolPtrTest(false)}},
	}
	for pName, profile := range profiles {
		ctx := WithWorkerCapabilityProfile(context.Background(), profile)
		for _, tool := range names {
			// task__offer / task__assign_remote are gated by may_offer_tasks,
			// which the bare may_write_tasks=false profile leaves at its
			// default (true). They're still write — but the may_write_tasks
			// read-allowlist deny covers them too (not in taskReadTools).
			err := checkWorkerCapability(ctx, tool)
			if expectedTaskReadTools[tool] {
				if err != nil {
					t.Errorf("%s: read tool %q denied: %v", pName, tool, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s: WRITE tool %q LEAKED (not denied)", pName, tool)
			}
		}
	}
}

// TestCapabilityReadAllowlistHasNoPhantomEntries asserts the test's expected
// read sets reference only tools that actually exist in the registry. Catches
// the rot where a read tool is removed but left in the allowlist (which would
// then silently re-admit a future write tool reusing the name).
func TestCapabilityReadAllowlistHasNoPhantomEntries(t *testing.T) {
	memSet := map[string]bool{}
	for _, n := range registeredMemoryToolNames() {
		memSet[n] = true
	}
	for n := range expectedMemoryReadTools {
		if !memSet[n] {
			t.Errorf("expectedMemoryReadTools names %q which is not a registered builtin", n)
		}
	}
	taskSet := map[string]bool{}
	for _, n := range registeredTaskToolNames() {
		taskSet[n] = true
	}
	for n := range expectedTaskReadTools {
		if !taskSet[n] {
			t.Errorf("expectedTaskReadTools names %q which is not a registered builtin", n)
		}
	}
}

// TestCapabilityResearcherDeniesRealDownstreamWrites drives the REAL
// writeclass heuristic (not the toolgate-local fake) through the researcher
// gate over downstream mutators whose verbs the old writeVerbs list missed
// (HIGH-5 / MED). archive / assign / add / revoke / rename etc. must now be
// classified write and blanket-denied by the read-only profile.
func TestCapabilityResearcherDeniesRealDownstreamWrites(t *testing.T) {
	ctx := WithWorkerCapabilityProfile(context.Background(), toolgate.Researcher())
	writes := []string{
		"notion__archive_page", "jira__assign_issue", "github__add_collaborator",
		"linear__cancel_issue", "okta__revoke_token", "fs__rename_file",
		"server__disable_feature", "server__enable_feature", "db__restore_snapshot",
		"trello__move_card", "data__import_records", "github__create_issue",
	}
	for _, tool := range writes {
		if !writeclass.IsWriteClass(tool) {
			t.Errorf("writeclass missed %q — researcher gate cannot blanket-deny it", tool)
		}
		if err := checkWorkerCapability(ctx, tool); err == nil {
			t.Errorf("researcher allowed downstream write %q", tool)
		}
	}
	reads := []string{"github__list_issues", "notion__get_page", "linear__search_issues"}
	for _, tool := range reads {
		if err := checkWorkerCapability(ctx, tool); err != nil {
			t.Errorf("researcher denied downstream read %q: %v", tool, err)
		}
	}
}

func boolPtrTest(b bool) *bool { return &b }

// TestFuzzyRecoveryReChecksCapability is the HIGH-1 boundary test. A worker
// whose capability profile ToolDeny's deln__delegate_worker calls a TYPO of
// it. Crucially the profile is constructed so the TYPO PASSES the initial
// gate (the deny is keyed on the exact corrected name, which the typo does
// not match) — proving the security comes from the POST-recovery re-check,
// not an incidental early deny. Routing FAILS for the typo, fuzzy recovery
// rewrites it to the real deln__delegate_worker, and the re-check on the
// corrected name MUST deny + MUST NOT dispatch. Without the re-gate, leak.
func TestFuzzyRecoveryReChecksCapability(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"del-server": toolsJSON(Tool{
				Name:        "delegate_worker",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}),
		},
	}
	h, ms := newTestHandler(lister, []store.DownstreamServer{
		{ID: "del-server", ToolNamespace: "deln", Discovery: "static"},
	})
	// Route rules WITHOUT a wildcard: only mcpx__* and the EXACT corrected
	// tool route, so the typo route-fails and triggers fuzzy recovery.
	ms.routeRules["ws-global"] = []store.RouteRule{
		{
			ID: "builtin-allow", WorkspaceID: "ws-global", Priority: 100,
			PathGlob: "**", Policy: "allow",
			ToolMatch:          json.RawMessage(`["mcpx__*"]`),
			DownstreamServerID: "mcpx-builtin",
		},
		{
			ID: "del-allow", WorkspaceID: "ws-global", Priority: 50,
			PathGlob: "**", Policy: "allow",
			ToolMatch:          json.RawMessage(`["deln__delegate_worker"]`),
			DownstreamServerID: "del-server",
		},
	}

	// ToolDeny the EXACT corrected name (no wildcard). All namespaces allowed,
	// not read-only — so only the literal corrected name is denied.
	profile := &toolgate.CapabilityProfile{ToolDeny: []string{"deln__delegate_worker"}}
	ctx := WithWorkerCapabilityProfile(
		withInternalCodeModeCall(context.Background()), profile)

	// Precondition 1: the TYPO passes the initial gate (so the test cannot
	// pass for the wrong reason — an incidental pre-fuzzy deny).
	if err := checkWorkerCapability(ctx, "deln__delegate_worke"); err != nil {
		t.Fatalf("precondition: typo should PASS the initial gate, got: %v", err)
	}
	// Precondition 2: the corrected name IS denied by the profile.
	if err := checkWorkerCapability(ctx, "deln__delegate_worker"); err == nil {
		t.Fatal("precondition: profile should deny deln__delegate_worker")
	}

	params, _ := json.Marshal(CallToolRequest{
		Name:      "deln__delegate_worke", // typo (missing trailing r)
		Arguments: json.RawMessage(`{}`),
	})
	_, rpcErr := h.handleToolsCall(ctx, params)
	if rpcErr == nil {
		t.Fatal("typo'd denied tool was NOT blocked after fuzzy recovery (leak)")
	}
	if lister.callCount != 0 {
		t.Fatalf("denied tool was dispatched %d time(s) despite capability deny", lister.callCount)
	}

	// Second leg: the post-recovery re-check also re-runs the worker tool
	// ALLOWLIST (not just the capability profile). Allowlist permits only the
	// mcpx entrypoints + the typo itself, so the corrected name is out of the
	// allowlist and must be denied after recovery.
	allowCtx := WithWorkerToolAllowlist(
		withInternalCodeModeCall(context.Background()),
		[]string{"mcpx__*", "deln__delegate_worke"},
	)
	if err := checkWorkerToolAllowlist(allowCtx, "deln__delegate_worke"); err != nil {
		t.Fatalf("precondition: typo should be in allowlist, got: %v", err)
	}
	allowParams, _ := json.Marshal(CallToolRequest{
		Name:      "deln__delegate_worke",
		Arguments: json.RawMessage(`{}`),
	})
	if _, rpcErr := h.handleToolsCall(allowCtx, allowParams); rpcErr == nil {
		t.Fatal("corrected name outside allowlist was dispatched after fuzzy recovery (leak)")
	}
}

// TestFuzzyRecoveryAllowsWhenProfilePermits is the happy-path counterpart:
// with NO restrictive profile, fuzzy recovery still rewrites the typo and the
// dispatch proceeds (proves the re-check didn't break legitimate recovery).
func TestFuzzyRecoveryAllowsWhenProfilePermits(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"del-server": toolsJSON(Tool{
				Name:        "delegate_worker",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}),
		},
	}
	h, ms := newTestHandler(lister, []store.DownstreamServer{
		{ID: "del-server", ToolNamespace: "deln", Discovery: "static"},
	})
	ms.routeRules["ws-global"] = []store.RouteRule{
		{
			ID: "builtin-allow", WorkspaceID: "ws-global", Priority: 100,
			PathGlob: "**", Policy: "allow",
			ToolMatch:          json.RawMessage(`["mcpx__*"]`),
			DownstreamServerID: "mcpx-builtin",
		},
		{
			ID: "del-allow", WorkspaceID: "ws-global", Priority: 50,
			PathGlob: "**", Policy: "allow",
			ToolMatch:          json.RawMessage(`["deln__delegate_worker"]`),
			DownstreamServerID: "del-server",
		},
	}

	// No capability profile attached => allow-all (back-compat).
	ctx := withInternalCodeModeCall(context.Background())
	params, _ := json.Marshal(CallToolRequest{
		Name:      "deln__delegate_worke", // typo
		Arguments: json.RawMessage(`{}`),
	})
	_, rpcErr := h.handleToolsCall(ctx, params)
	if rpcErr != nil {
		t.Fatalf("fuzzy recovery happy-path broke: %s", rpcErr.Message)
	}
	if lister.callCount != 1 {
		t.Fatalf("expected exactly 1 dispatch after fuzzy recovery, got %d", lister.callCount)
	}
	if lister.lastCall.toolName != "delegate_worker" {
		t.Fatalf("dispatched wrong tool %q after recovery", lister.lastCall.toolName)
	}
}

func TestCheckWorkerCapabilityNilProfileAllowsAll(t *testing.T) {
	// No profile attached => allow-all (back-compat, identical to
	// pre-change behavior where only the tool allowlist gated).
	ctx := context.Background()
	for _, name := range []string{
		"github__create_issue", "memory__save", "mesh__send",
		"mcpx__execute_code",
	} {
		if err := checkWorkerCapability(ctx, name); err != nil {
			t.Errorf("nil profile blocked %q: %v", name, err)
		}
	}
}

func TestWithWorkerCapabilityProfileNilNoOp(t *testing.T) {
	// Attaching a nil profile must NOT change the context (so an
	// interactive/non-delegate session is never accidentally gated).
	ctx := context.Background()
	got := WithWorkerCapabilityProfile(ctx, nil)
	if workerCapabilityFromContext(got) != nil {
		t.Error("nil profile attached a non-nil context value")
	}
}

func TestCheckWorkerCapabilityCoderBlocksAtDispatch(t *testing.T) {
	profile := toolgate.Coder()
	ctx := WithWorkerCapabilityProfile(context.Background(), profile)

	allowed := []string{"mcpx__execute_code", "task__create", "memory__save"}
	denied := []string{
		"mcpx__delegate_worker",                          // sub-delegation
		"mcplexer__create_worker", "mcpx__provision_mcp", // admin
		"mesh__send", "secret__list_refs", "task__offer",
	}
	for _, name := range allowed {
		if err := checkWorkerCapability(ctx, name); err != nil {
			t.Errorf("coder blocked allowed tool %q: %v", name, err)
		}
	}
	for _, name := range denied {
		if err := checkWorkerCapability(ctx, name); err == nil {
			t.Errorf("coder failed to block %q at dispatch", name)
		}
	}
}

func TestFilterByWorkerCapabilityHidesDeniedNamespaces(t *testing.T) {
	profile := toolgate.Coder()
	ctx := WithWorkerCapabilityProfile(context.Background(), profile)

	tools := []Tool{
		{Name: "mcpx__execute_code"},
		{Name: "task__create"},
		{Name: "memory__save"},
		{Name: "mesh__send"},              // denied namespace
		{Name: "secret__list_refs"},       // denied namespace
		{Name: "mcpx__delegate_worker"},   // denied feature
		{Name: "mcplexer__create_worker"}, // admin
	}
	got := filterByWorkerCapability(ctx, tools)
	seen := map[string]bool{}
	for _, tool := range got {
		seen[tool.Name] = true
	}
	wantVisible := []string{"mcpx__execute_code", "task__create", "memory__save"}
	for _, name := range wantVisible {
		if !seen[name] {
			t.Errorf("filter hid an allowed tool %q", name)
		}
	}
	wantHidden := []string{
		"mesh__send", "secret__list_refs",
		"mcpx__delegate_worker", "mcplexer__create_worker",
	}
	for _, name := range wantHidden {
		if seen[name] {
			t.Errorf("filter leaked a denied tool %q into discovery", name)
		}
	}
}

func TestFilterByWorkerCapabilityNilProfilePassesThrough(t *testing.T) {
	ctx := context.Background()
	tools := []Tool{{Name: "github__create_issue"}, {Name: "mesh__send"}}
	got := filterByWorkerCapability(ctx, tools)
	if len(got) != len(tools) {
		t.Errorf("nil profile filtered tools: got %d want %d", len(got), len(tools))
	}
}

func TestCheckWorkerCapabilityMinimalGatesToMcpx(t *testing.T) {
	profile := toolgate.Minimal()
	ctx := WithWorkerCapabilityProfile(context.Background(), profile)
	if err := checkWorkerCapability(ctx, "mcpx__execute_code"); err != nil {
		t.Errorf("minimal bricked mcpx__execute_code: %v", err)
	}
	if err := checkWorkerCapability(ctx, "github__list_issues"); err == nil {
		t.Error("minimal allowed a downstream tool")
	}
}

func TestCheckWorkerCapabilityFailClosedDenyEverything(t *testing.T) {
	// A non-nil empty NamespaceAllow is the dispatcher's fail-closed profile
	// for a corrupt capability_profile_json column: deny everything except
	// the irreducible mcpx entrypoint.
	profile := &toolgate.CapabilityProfile{NamespaceAllow: []string{}}
	ctx := WithWorkerCapabilityProfile(context.Background(), profile)
	if err := checkWorkerCapability(ctx, "mcpx__execute_code"); err != nil {
		t.Errorf("deny-everything profile bricked mcpx: %v", err)
	}
	for _, name := range []string{
		"github__list_issues", "task__list", "memory__recall", "mesh__send",
	} {
		if err := checkWorkerCapability(ctx, name); err == nil {
			t.Errorf("deny-everything profile allowed %q", name)
		}
	}
}
