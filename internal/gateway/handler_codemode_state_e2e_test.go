package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// These tests exercise the cross-call state features (`session` + `kv`) through
// the REAL gateway entrypoint `handleCodeExecute` — i.e. the exact path an agent
// drives via mcpx__execute_code. The unit tests in internal/codemode hand-thread
// SessionState between calls; here the gateway's own sessionID() →
// saveSessionState/loadSessionState → rehydrate plumbing is what carries state,
// so a regression in that wiring (the part most likely to break in practice)
// fails loudly.

// runCode runs one execute_code invocation and returns the agent-facing text
// (all content blocks joined), the way an agent would actually read it.
func runCode(t *testing.T, h *handler, code string) string {
	t.Helper()
	raw, rpcErr := h.handleCodeExecute(context.Background(), code)
	if rpcErr != nil {
		t.Fatalf("handleCodeExecute rpc error: %+v", rpcErr)
	}
	var env struct {
		Content []struct{ Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	parts := make([]string, 0, len(env.Content))
	for _, c := range env.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}

// setSession pins the handler's MCP session id so session-state is enabled and
// keyed deterministically (mirrors what a live connection's session id does).
func setSession(h *handler, id string) {
	h.sessions.session = &store.Session{ID: id, ClientType: "claude-code"}
}

// TestSessionState_E2E_PersistAcrossCalls is the headline contract: assign to
// `session` in one call, read it back in the next call of the SAME session —
// without any explicit save/load by the agent.
func TestSessionState_E2E_PersistAcrossCalls(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	setSession(h, "sess-persist")

	// Call 1: build an "expensive" dataset once and park it on session.
	out1 := runCode(t, h, `session.customers = {count: 3, names: ["acme","globex","initech"]};
print("built", session.customers.count);`)
	if !strings.Contains(out1, "built 3") {
		t.Fatalf("call 1 unexpected output: %q", out1)
	}

	// Call 2 (fresh VM): the value is just there — no re-fetch, no save/load.
	out2 := runCode(t, h, `print("reused", session.customers.names.length, session.customers.names[1]);`)
	if !strings.Contains(out2, "reused 3 globex") {
		t.Fatalf("call 2 did not rehydrate session: %q", out2)
	}

	// Call 3: mutate the nested value and confirm the mutation persists too.
	_ = runCode(t, h, `session.customers.count += 10;`)
	out4 := runCode(t, h, `print("mutated", session.customers.count);`)
	if !strings.Contains(out4, "mutated 13") {
		t.Fatalf("nested mutation did not persist across calls: %q", out4)
	}
}

// TestSessionState_E2E_IsolatedBetweenSessions proves state is keyed by MCP
// session id: one session can never read another's `session` object.
func TestSessionState_E2E_IsolatedBetweenSessions(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)

	setSession(h, "sess-A")
	_ = runCode(t, h, `session.secret = "alpha";`)

	setSession(h, "sess-B")
	out := runCode(t, h, `print("B sees", typeof session.secret);`)
	if !strings.Contains(out, "B sees undefined") {
		t.Fatalf("session B leaked session A state: %q", out)
	}

	// Back to A — its state is intact.
	setSession(h, "sess-A")
	outA := runCode(t, h, `print("A sees", session.secret);`)
	if !strings.Contains(outA, "A sees alpha") {
		t.Fatalf("session A lost its own state: %q", outA)
	}
}

// TestSessionState_E2E_ClearedOnDisconnect proves the disconnect hook
// (clearSessionState) actually frees the per-session memory.
func TestSessionState_E2E_ClearedOnDisconnect(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	setSession(h, "sess-disc")

	_ = runCode(t, h, `session.x = 42;`)
	out := runCode(t, h, `print("before", session.x);`)
	if !strings.Contains(out, "before 42") {
		t.Fatalf("precondition failed: %q", out)
	}

	h.clearSessionState("sess-disc")

	out2 := runCode(t, h, `print("after", typeof session.x);`)
	if !strings.Contains(out2, "after undefined") {
		t.Fatalf("disconnect did not clear session state: %q", out2)
	}
}

// TestSessionState_E2E_ErrorDoesNotClobberPriorState proves the skip-on-error
// contract end-to-end: a call that throws after mutating `session` must NOT
// overwrite the last good snapshot.
func TestSessionState_E2E_ErrorDoesNotClobberPriorState(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	setSession(h, "sess-err")

	_ = runCode(t, h, `session.good = "keep-me";`)

	// This call corrupts session in-VM then throws — must be discarded.
	out := runCode(t, h, `session.good = "CORRUPTED"; throw new Error("boom");`)
	if !strings.Contains(out, "boom") {
		t.Fatalf("expected the thrown error surfaced to the agent: %q", out)
	}

	out2 := runCode(t, h, `print("recovered", session.good);`)
	if !strings.Contains(out2, "recovered keep-me") {
		t.Fatalf("error run clobbered prior good session state: %q", out2)
	}
}

// TestSessionState_E2E_NonSerializableWarningSurfaced proves the
// "your value didn't persist" warning is actually shown to the agent in the
// execute_code output (not swallowed server-side).
func TestSessionState_E2E_NonSerializableWarningSurfaced(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	setSession(h, "sess-warn")

	out := runCode(t, h, `session.fn = function(){ return 1 }; session.ok = 5;`)
	if !strings.Contains(out, "not JSON-serializable") || !strings.Contains(out, "fn") {
		t.Fatalf("expected a surfaced non-serializable warning naming fn: %q", out)
	}
	// The serializable sibling still persists.
	out2 := runCode(t, h, `print("ok", session.ok, "fn", typeof session.fn);`)
	if !strings.Contains(out2, "ok 5 fn undefined") {
		t.Fatalf("serializable value should persist, function should not: %q", out2)
	}
}

// TestSessionState_E2E_NoSessionIDDisablesFeature documents the load-bearing
// pre-condition: when the MCP session id is empty, `session` is undefined and
// NOTHING persists. (Relevant to transports that don't carry a stable session
// id — the feature silently degrades rather than erroring.)
func TestSessionState_E2E_NoSessionIDDisablesFeature(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	setSession(h, "") // empty id

	out := runCode(t, h, `print("typeof session:", typeof session);`)
	if !strings.Contains(out, "typeof session: undefined") {
		t.Fatalf("with no session id, session must be undefined, got: %q", out)
	}
}

// TestKV_E2E_DurableThroughSandbox proves the durable kv layer works when
// called the way an agent calls it — from inside execute_code, with the stored
// value auto-unwrapped back to a native JS value on get. Uses a real sqlite
// store for persistence while keeping the permissive mock routing engine.
func TestKV_E2E_DurableThroughSandbox(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.store = db // real persistence; routing engine still on the mock store
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	setSession(h, "sess-kv")

	// Set in one "session", read in a brand-new one — kv is durable + cross-session.
	out := runCode(t, h, `kv.set({key:"report-2026", value:{rows:2,top:["acme","globex"]}});
print("stored");`)
	if !strings.Contains(out, "stored") {
		t.Fatalf("kv.set did not run cleanly: %q", out)
	}

	setSession(h, "a-totally-different-session")
	out2 := runCode(t, h, `const r = kv.get({key:"report-2026"});
print("rows", r.rows, "first", r.top[0]);`)
	if !strings.Contains(out2, "rows 2 first acme") {
		t.Fatalf("kv.get did not durably rehydrate across sessions: %q", out2)
	}

	// Missing key returns null so `|| build()` fallback works.
	out3 := runCode(t, h, `const r = kv.get({key:"absent"}); print("missing", r === null);`)
	if !strings.Contains(out3, "missing true") {
		t.Fatalf("kv.get of absent key should be null: %q", out3)
	}
}

// TestData_E2E_RoutesThroughSandbox guards the sibling routing regression: the
// data workbench (migration 114) shipped with no seeded route, so data.* failed
// 'no matching route' from a normal session exactly like kv did. Drives
// data.ingest/data.list through the real gateway entrypoint.
func TestData_E2E_RoutesThroughSandbox(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.store = db
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	setSession(h, "sess-data")

	out := runCode(t, h, `data.ingest({name:"sales", rows:[{q:1,rev:10},{q:2,rev:20}]});
print("ingested");`)
	if !strings.Contains(out, "ingested") {
		t.Fatalf("data.ingest did not route/run cleanly: %q", out)
	}

	out2 := runCode(t, h, `const r = data.list({});
print("count", r.count, "name", r.collections[0].name, "rows", r.collections[0].row_count);`)
	if !strings.Contains(out2, "count 1 name sales rows 2") {
		t.Fatalf("data.list did not durably route/return the ingested collection: %q", out2)
	}
}
