package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/index"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newHandlerWithIndex builds a test handler whose workspace root is an existing
// (empty) temp directory, so the D8 root-safety gate passes and auto-builds
// produce an empty-but-valid index.
func newHandlerWithIndex(t *testing.T) *handler {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.store = db
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-idx", RootPath: t.TempDir()}}
	h.codeIndex = index.NewService(db, nil)
	return h
}

// indexErrText dispatches an index tool expecting an isError=true tool result
// and returns the error message text.
func indexErrText(t *testing.T, h *handler, name, args string) string {
	t.Helper()
	raw, rpcErr, handled := h.dispatchIndexTool(context.Background(), name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	var env struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	if !env.IsError {
		t.Fatalf("expected isError=true, got %s", string(raw))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content envelope: %s", string(raw))
	}
	return env.Content[0].Text
}

// TestIndexStatusSurfacesNotBuilt proves index__status does NOT auto-build: on
// a never-built workspace it maps ErrNotBuilt to an agent-readable message.
func TestIndexStatusSurfacesNotBuilt(t *testing.T) {
	h := newHandlerWithIndex(t)
	if msg := indexErrText(t, h, "index__status", `{}`); !strings.Contains(msg, "not built") {
		t.Fatalf("status: expected not-built message, got %q", msg)
	}
}

// TestIndexSymbolsAutoBuilds proves the D3 contract: a query on a never-built
// workspace triggers a build instead of erroring, then answers (here: empty).
func TestIndexSymbolsAutoBuilds(t *testing.T) {
	h := newHandlerWithIndex(t)
	out := indexOK(t, h, "index__symbols", `{"query":"dispatch"}`)
	if !strings.Contains(out, `"count":0`) {
		t.Fatalf("expected empty auto-built symbol result, got %q", out)
	}
}

// TestIndexSymbolsRequiresQuery proves handler-side param validation fires before
// the service is touched.
func TestIndexSymbolsRequiresQuery(t *testing.T) {
	h := newHandlerWithIndex(t)
	if msg := indexErrText(t, h, "index__symbols", `{}`); !strings.Contains(msg, "query is required") {
		t.Fatalf("expected query-required error, got %q", msg)
	}
}

// TestIndexRefusesGlobalRoot proves the D8 gate: a workspace rooted at "/" is
// refused with guidance to run from a project workspace.
func TestIndexRefusesGlobalRoot(t *testing.T) {
	h := newHandlerWithIndex(t)
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-idx", RootPath: "/"}}
	if msg := indexErrText(t, h, "index__status", `{}`); !strings.Contains(msg, "project workspace") {
		t.Fatalf("expected project-workspace refusal, got %q", msg)
	}
}

// TestIndexWorkspaceAccessDenied proves a workspace_id override outside the
// session's readable scope is rejected before any index work runs.
func TestIndexWorkspaceAccessDenied(t *testing.T) {
	h := newHandlerWithIndex(t)
	if msg := indexErrText(t, h, "index__status", `{"workspace_id":"ws-denied"}`); !strings.Contains(msg, "outside") {
		t.Fatalf("expected out-of-scope error, got %q", msg)
	}
}

// TestIndexNilServiceUnavailable proves the nil-service guard: with no indexer
// wired, every index tool returns a clean "unavailable" result rather than
// panicking on the nil pointer.
func TestIndexNilServiceUnavailable(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.codeIndex = nil
	msg := indexErrText(t, h, "index__status", `{}`)
	if !strings.Contains(msg, "unavailable") {
		t.Fatalf("expected unavailable error, got %q", msg)
	}
}

// TestIndexUnknownToolNotHandled proves an unrecognized index__* name falls
// through (handled=false) so handleBuiltinCall can report not-found.
func TestIndexUnknownToolNotHandled(t *testing.T) {
	h := newHandlerWithIndex(t)
	_, _, handled := h.dispatchIndexTool(context.Background(), "index__does_not_exist", json.RawMessage(`{}`))
	if handled {
		t.Fatalf("unknown index tool should not be handled")
	}
}
