package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestPiggybackMeshNotice_SkipsInternalCodeModeCalls is the regression test for
// the bug the intervalspro agent hit: when a session has pending mesh messages,
// the gateway appended a "[mesh: N pending …]" notice as a SECOND content block
// to EVERY tool result — including tool calls dispatched from inside
// mcpx__execute_code. That second block defeated the sandbox's "exactly one
// text block" auto-unwrap gate, so task.create(...).id silently came back
// undefined (which in turn created subtasks with compose_into:undefined — i.e.
// unlinked children).
//
// The fix: piggybackMeshNotice must be a no-op when the call originates from
// inside the code sandbox (isInternalCodeModeCall). The OUTER execute_code
// result is dispatched WITHOUT that marker, so the agent is still nudged.
func TestPiggybackMeshNotice_SkipsInternalCodeModeCalls(t *testing.T) {
	const sid = "sess-codemode-tester"
	const ws = "ws-global"

	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := mesh.NewManager(db)

	// Register our agent so PendingCount can resolve it, then push a message
	// addressed to it so the pending count is non-zero.
	meta := mesh.SessionMeta{SessionID: sid, WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, meta, mesh.ReceiveRequest{Name: "codemode-tester"}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	sender := mesh.SessionMeta{SessionID: "sender-1", WorkspaceIDs: []string{ws}, ClientType: "sender"}
	if _, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind: "event", Content: "ping", Audience: sid,
	}); err != nil {
		t.Fatalf("send mesh message: %v", err)
	}

	// Build a handler bound to that session + workspace, wired to the real mesh.
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.sessions.session = &store.Session{ID: sid, ClientType: "test"}

	// A single-text-block JSON result, exactly what a builtin like task__create
	// returns and what the sandbox must be able to auto-unwrap.
	payload := `{"id":"T1","ok":true}`
	result := mustJSON(t, map[string]any{
		"content": []map[string]any{{"type": "text", "text": payload}},
	})

	// Sanity: pending count must be >0 so the notice WOULD be appended absent
	// the guard — otherwise the test proves nothing.
	if n, err := mgr.PendingCount(ctx, h.sessionMeshMeta(ctx)); err != nil || n == 0 {
		t.Fatalf("precondition: want pending>0, got n=%d err=%v", n, err)
	}

	// Direct (non-code-mode) call: notice IS appended (agent gets nudged).
	got := h.piggybackMeshNotice(ctx, result)
	if blocks := contentBlockCount(t, got); blocks != 2 {
		t.Fatalf("direct call: want 2 content blocks (payload + notice), got %d: %s", blocks, got)
	}

	// Inside execute_code: notice MUST be suppressed so the single JSON block
	// auto-unwraps and result.id is defined.
	innerCtx := withInternalCodeModeCall(ctx)
	gotInner := h.piggybackMeshNotice(innerCtx, result)
	if blocks := contentBlockCount(t, gotInner); blocks != 1 {
		t.Fatalf("code-mode call: want 1 content block (unwrappable), got %d: %s", blocks, gotInner)
	}
	if string(gotInner) != string(result) {
		t.Fatalf("code-mode call mutated the result.\n in: %s\nout: %s", result, gotInner)
	}
}

func contentBlockCount(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var env struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	return len(env.Content)
}
