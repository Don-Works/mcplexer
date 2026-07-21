package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestHandleMeshSendKindHintIsTruthful: the validation hint used to advertise
// kinds (plan, ack, status, error, "custom tags") that mesh.Send rejects.
// The hint must contain exactly the enforced vocabulary and nothing else.
func TestHandleMeshSendKindHintIsTruthful(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mesh.NewManager(nil)

	out, rpcErr := h.handleMeshSend(context.Background(),
		mustJSON(t, map[string]any{"content": "hello"})) // kind omitted
	if rpcErr != nil {
		t.Fatalf("expected in-band validation envelope, got rpc error: %v", rpcErr)
	}
	text := string(out)
	for _, kind := range mesh.ValidKinds {
		if !strings.Contains(text, kind) {
			t.Errorf("kind hint missing enforced kind %q: %s", kind, text)
		}
	}
	for _, lie := range []string{"plan", "ack", "status", "custom tag"} {
		if strings.Contains(text, lie) {
			t.Errorf("kind hint still advertises rejected kind/phrase %q: %s", lie, text)
		}
	}
}

// TestHandleMeshSendRejectsWhitespaceContent enforces the non-blank gate at
// the mesh boundary through the gateway handler.
func TestHandleMeshSendRejectsWhitespaceContent(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mesh.NewManager(db)
	h.sessions.session = &store.Session{ID: "sender", ClientType: "test"}

	out, rpcErr := h.handleMeshSend(ctx, mustJSON(t, map[string]any{
		"kind": "event", "content": "   \n\t ",
	}))
	if rpcErr != nil {
		t.Fatalf("expected in-band error envelope, got rpc error: %v", rpcErr)
	}
	if !strings.Contains(string(out), "content is required") {
		t.Fatalf("whitespace-only content was not rejected: %s", out)
	}
}

// TestHandleMeshReceiveDefaultHidesTaskEvents drives the kind filter through
// the gateway layer: a default receive hides task_event rows and says so in
// the hint; kinds:"task_event" opts back in.
func TestHandleMeshReceiveDefaultHidesTaskEvents(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := mesh.NewManager(db)

	const sid = "receiver"
	const ws = "ws-global"
	receiver := mesh.SessionMeta{SessionID: sid, WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, mesh.ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	sender := mesh.SessionMeta{SessionID: "sender", WorkspaceIDs: []string{ws}, ClientType: "test"}
	for _, kind := range []string{"task_event", "finding"} {
		if _, err := mgr.Send(ctx, sender, mesh.SendRequest{
			Kind: kind, Content: "body " + kind, Audience: sid,
		}); err != nil {
			t.Fatalf("send %s: %v", kind, err)
		}
	}

	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.sessions.session = &store.Session{ID: sid, ClientType: "test"}

	out, rpcErr := h.handleMeshReceive(ctx, mustJSON(t, map[string]any{"filter": "new"}))
	if rpcErr != nil {
		t.Fatalf("handleMeshReceive: %v", rpcErr)
	}
	var env mesh.ReceiveEnvelope
	if err := json.Unmarshal([]byte(singleTextResult(t, out)), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(env.Messages) != 1 || env.Messages[0].Kind != "finding" {
		t.Fatalf("default receive delivered %+v, want only the finding", env.Messages)
	}
	if !strings.Contains(env.Hint, "task_event") {
		t.Fatalf("hint does not mention the task_event exclusion: %q", env.Hint)
	}

	out, rpcErr = h.handleMeshReceive(ctx, mustJSON(t, map[string]any{
		"filter": "all", "since_minutes": 10, "kinds": "task_event",
	}))
	if rpcErr != nil {
		t.Fatalf("handleMeshReceive opt-in: %v", rpcErr)
	}
	if err := json.Unmarshal([]byte(singleTextResult(t, out)), &env); err != nil {
		t.Fatalf("unmarshal opt-in envelope: %v", err)
	}
	if len(env.Messages) != 1 || env.Messages[0].Kind != "task_event" {
		t.Fatalf("kinds=task_event delivered %+v, want only the task_event", env.Messages)
	}
}
