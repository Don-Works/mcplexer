package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/toolgate"
	"github.com/don-works/mcplexer/internal/workers/delegscope"
)

// citationTestInput is a minimal CLI-provider delegation. grok_cli +
// worker_isolation=none resolves through normalizeDelegationInput without a DB
// (matching the other normalize unit tests), and CLI is the provider class
// verify_citations most targets.
func citationTestInput() *DelegationInput {
	return &DelegationInput{
		WorkspaceID:     "ws",
		Objective:       "Find the hook entrypoint and cite its exact file:line",
		ModelProvider:   "grok_cli",
		ModelID:         "grok-build",
		SecretScopeID:   "scope-test",
		WorkerIsolation: "none",
	}
}

// TestVerifyCitationsInjectsEmbeddedGate is the happy path: verify_citations
// with no caller post_execute_script and the default allowlist installs the
// embedded gate verbatim, and — crucially — leaves the allowlist as the system
// default so the CLI scope guard still exempts it (see the guard assertion).
func TestVerifyCitationsInjectsEmbeddedGate(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput()
	in.VerifyCitations = true

	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if in.PostExecuteScript != citationGateScript {
		t.Fatalf("post_execute_script is not the embedded gate (len got=%d want=%d)",
			len(in.PostExecuteScript), len(citationGateScript))
	}
	// The allowlist must remain the untouched system default: mutating it (e.g.
	// appending the index tools) would make delegscope.IsDefaultAllowlist false
	// and the runner would refuse this CLI worker as operator-scoped.
	if !delegscope.IsDefaultAllowlist(in.ToolAllowlistJSON) {
		t.Errorf("verify_citations perturbed the default allowlist: %s", in.ToolAllowlistJSON)
	}
	if strings.TrimSpace(in.capabilityProfileJSON) != "" {
		t.Errorf("verify_citations should not synthesize a capability profile, got %q", in.capabilityProfileJSON)
	}
}

// TestVerifyCitationsDefaultAllowlistNotRefusedByCLIGuard closes the loop the
// task calls out: with the default allowlist + verify_citations, a CLI worker
// is NOT operator-scoped. delegscope.IsDefaultAllowlist is the exact predicate
// the runner's cliScopeUnenforceable uses (workerAllowlistOperatorScopeSet =
// scopeSet && !IsDefaultAllowlist), so a true here is the guard not firing.
func TestVerifyCitationsDefaultAllowlistNotRefusedByCLIGuard(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput()
	in.VerifyCitations = true

	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !delegscope.IsDefaultAllowlist(in.ToolAllowlistJSON) {
		t.Fatalf("default allowlist + verify_citations is NOT recognised as the default, "+
			"so the CLI scope guard would refuse the run: %s", in.ToolAllowlistJSON)
	}
	// And the default really does carry the index tools the gate calls, so the
	// gate can run (this is a property of delegscope.DefaultToolsJSON, asserted
	// here so a future edit to the default that drops them trips this test).
	for _, tool := range citationGateRequiredTools {
		if !strings.Contains(in.ToolAllowlistJSON, tool) {
			t.Errorf("default allowlist is missing %s that the citation gate needs", tool)
		}
	}
}

// TestVerifyCitationsAbsentInjectsNothing is the no-regression half: without
// the flag no post_execute_script is injected.
func TestVerifyCitationsAbsentInjectsNothing(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput() // VerifyCitations defaults false

	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if in.PostExecuteScript != "" {
		t.Errorf("no verify_citations, but a post_execute_script was injected: %q", tail(in.PostExecuteScript))
	}
}

// TestVerifyCitationsRejectsCallerSuppliedScript pins the chosen interaction:
// verify_citations + a caller post_execute_script is REJECTED, never silently
// overwritten. The gate aborts the run on a bad citation, so wrapping or
// replacing the caller's own gate would change its meaning.
func TestVerifyCitationsRejectsCallerSuppliedScript(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput()
	in.VerifyCitations = true
	in.PostExecuteScript = "// my own gate\nif (hook.run.cost_usd > 1) abort('too dear');"

	err := svc.normalizeDelegationInput(t.Context(), in)
	if err == nil {
		t.Fatal("want an error combining verify_citations with a caller post_execute_script, got nil")
	}
	if !strings.Contains(err.Error(), "post_execute_script") {
		t.Errorf("error does not name the conflicting field: %v", err)
	}
	// The caller's script must be left intact — we reject, we do not clobber.
	if !strings.Contains(in.PostExecuteScript, "too dear") {
		t.Errorf("caller post_execute_script was mutated on rejection: %q", in.PostExecuteScript)
	}
}

// TestVerifyCitationsRejectsAllowlistWithoutIndexTools proves the scope-guard
// interaction #3: an explicit allowlist that scopes out the index tools the
// gate needs is a hard error, because the gate would otherwise fail closed and
// reject every run on a dispatch error.
func TestVerifyCitationsRejectsAllowlistWithoutIndexTools(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput()
	in.VerifyCitations = true
	in.ToolAllowlistJSON = `["mcpx__execute_code","mcpx__search_tools","task__create"]`

	err := svc.normalizeDelegationInput(t.Context(), in)
	if err == nil {
		t.Fatal("want an error for verify_citations without the index tools, got nil")
	}
	for _, want := range []string{"index__summary", "fail closed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// TestVerifyCitationsAcceptsExplicitAllowlistWithIndexTools: an operator who
// hand-authors an allowlist that DOES include the index tools gets the gate
// injected (the flag composes with a compatible explicit scope).
func TestVerifyCitationsAcceptsExplicitAllowlistWithIndexTools(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput()
	in.VerifyCitations = true
	in.ToolAllowlistJSON = `["mcpx__execute_code","index__summary","index__symbols"]`

	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if in.PostExecuteScript != citationGateScript {
		t.Fatal("gate not injected despite an allowlist that grants the index tools")
	}
}

// TestVerifyCitationsRejectsCapabilityProfileDenyingIndex covers the second
// half of interaction #3: even with a compatible allowlist, a capability
// profile whose namespace_allow omits the index namespace would fail the gate
// closed, so it is rejected too.
func TestVerifyCitationsRejectsCapabilityProfileDenyingIndex(t *testing.T) {
	svc := &Service{clock: realClock{}}
	in := citationTestInput()
	in.VerifyCitations = true
	// namespace_allow that omits "index" — Allows("index__summary") is then
	// false, so the gate would fail closed.
	in.CapabilityProfile = &toolgate.CapabilityProfile{NamespaceAllow: []string{"mcpx", "task"}}

	err := svc.normalizeDelegationInput(t.Context(), in)
	if err == nil {
		t.Fatal("want an error for a capability profile that denies the index namespace, got nil")
	}
	if !strings.Contains(err.Error(), "capability profile") {
		t.Errorf("error does not name the capability profile: %v", err)
	}
}

// TestEmbeddedCitationGateMatchesRepoScript is the drift guard the task
// requires: the embedded citation_gate.js must be byte-identical to the vetted
// scripts/citation-gate.js. Editing one without re-copying the other fails
// here, so the flag can never inject a stale gate. A known marker line is also
// asserted so the intent (this is THE citation gate) is legible.
func TestEmbeddedCitationGateMatchesRepoScript(t *testing.T) {
	const repoScript = "../../../scripts/citation-gate.js"
	onDisk, err := os.ReadFile(repoScript)
	if err != nil {
		t.Fatalf("read %s: %v", repoScript, err)
	}
	if citationGateScript != string(onDisk) {
		t.Fatalf("embedded citation_gate.js has drifted from %s; re-copy it "+
			"(cp scripts/citation-gate.js internal/workers/admin/citation_gate.js). "+
			"embed=%d bytes, repo=%d bytes", repoScript, len(citationGateScript), len(onDisk))
	}
	// Marker lines unique to the vetted gate — their absence means the wrong
	// file was embedded even if byte lengths happened to line up.
	for _, marker := range []string{
		"citation-gate.js — a reusable `post_execute_script`",
		"citation verification failed (",
	} {
		if !strings.Contains(citationGateScript, marker) {
			t.Errorf("embedded gate missing marker %q", marker)
		}
	}
}
