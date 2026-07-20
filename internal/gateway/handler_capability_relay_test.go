package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
)

// TestCapabilityGateIsContextOnlyNotSessionDerived pins the invariant the
// runner's CLI scope guard rests on: the gateway derives a worker capability
// profile from the CALL CONTEXT only, never from the MCP session the call
// arrived on.
//
// That is why an unscoped CLI child is unscoped. The child opens its own MCP
// session; a fresh session carries none of the spawning run's context values;
// checkWorkerCapability therefore sees nil and allows everything. The test
// drives a real inner code-mode dispatch on a handler with a bound session and
// asserts it succeeds — the hole, stated as an executable fact rather than a
// comment — and then asserts the same call IS blocked once a profile is on the
// context, proving the gate itself works and only the carrier is missing.
//
// If a future change makes the gateway resolve a profile from session state
// (the per-run endpoint design), this test fails. That is intentional: the
// runner-side refusal in internal/workers/runner/cli_scope_guard.go exists
// only because this invariant holds, and must be removed in the same change.
func TestCapabilityGateIsContextOnlyNotSessionDerived(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{
				Name:        "create_issue",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}),
		},
	}
	h, ms := newTestHandler(lister, []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	})
	ms.routeRules["ws-global"] = []store.RouteRule{
		{
			ID: "gh-allow", WorkspaceID: "ws-global", Priority: 50,
			PathGlob: "**", Policy: "allow",
			ToolMatch:          json.RawMessage(`["github__*"]`),
			DownstreamServerID: "gh-server",
		},
	}
	// newTestHandler binds a session (clientPath + workspace chain) — the same
	// state a CLI child's connection produces. Nothing about it reaches the
	// capability gate.
	if h.sessions.clientRoot() == "" {
		t.Fatal("precondition: handler has no bound session to reason about")
	}

	params, err := json.Marshal(CallToolRequest{
		Name:      "github__create_issue",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Leg 1 — no profile on the context: the write-class downstream tool
	// dispatches. This is exactly what a CLI child gets today.
	sessionCtx := withInternalCodeModeCall(context.Background())
	if _, rpcErr := h.handleToolsCall(sessionCtx, params); rpcErr != nil {
		t.Fatalf("session-only context was gated: %s", rpcErr.Message)
	}
	if lister.callCount != 1 {
		t.Fatalf("dispatches = %d, want 1 (session-only context must be unrestricted)", lister.callCount)
	}

	// Leg 2 — same handler, same session, same call, with a read-only profile
	// on the CONTEXT: blocked, and never dispatched. The gate works; only the
	// relay that would put the profile there for a CLI child is missing.
	scopedCtx := WithWorkerCapabilityProfile(sessionCtx, toolgate.Researcher())
	if _, rpcErr := h.handleToolsCall(scopedCtx, params); rpcErr == nil {
		t.Fatal("researcher profile on the context failed to block a downstream write")
	}
	if lister.callCount != 1 {
		t.Fatalf("dispatches = %d, want 1 (the blocked call must not reach the downstream)", lister.callCount)
	}
}

// TestWorkerCapabilityProfileHasSingleContextConstructor pins that
// WithWorkerCapabilityProfile is the only way a profile enters a context, so
// "which callers can scope a session" is answerable by grepping one symbol.
// A profile placed under the key by any other means would not round-trip.
func TestWorkerCapabilityProfileHasSingleContextConstructor(t *testing.T) {
	profile := toolgate.Minimal()
	ctx := WithWorkerCapabilityProfile(context.Background(), profile)
	if workerCapabilityFromContext(ctx) != profile {
		t.Fatal("WithWorkerCapabilityProfile did not round-trip the profile")
	}
	// A value stored under any other key is invisible to the gate — the key is
	// unexported, so no package outside gateway can attach one.
	type lookalikeKey struct{}
	spoofed := context.WithValue(context.Background(), lookalikeKey{}, profile)
	if workerCapabilityFromContext(spoofed) != nil {
		t.Fatal("gate read a profile stored under a foreign context key")
	}
	if err := checkWorkerCapability(spoofed, "github__create_issue"); err != nil {
		t.Fatalf("foreign-key context was gated: %v", err)
	}
}
