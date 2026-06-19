package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestAppendMeshNoticeBlock_PreservesJSONPayload is the regression test for
// the bug where the mesh footer was concatenated into the first content
// block's text, corrupting JSON payloads from downstream servers (e.g.
// Reddit MCP returns a single text block containing JSON; the appended
// "[mesh: N pending ...]" suffix made JSON.parse fail and toRows() return
// []). The notice must live in its own content block.
func TestAppendMeshNoticeBlock_PreservesJSONPayload(t *testing.T) {
	redditPayload := `{"data":{"children":[{"id":"abc"},{"id":"def"}]}}`
	input := mustJSON(t, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": redditPayload},
		},
	})

	out := appendMeshNoticeBlock(input, 3)

	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(env.Content) != 2 {
		t.Fatalf("want 2 content blocks (payload + notice), got %d: %s", len(env.Content), out)
	}

	// First block MUST be the untouched JSON payload — this is the
	// invariant the Reddit MCP (and any JSON-returning tool) depends on.
	if env.Content[0].Text != redditPayload {
		t.Fatalf("payload block mutated.\n want: %s\n  got: %s", redditPayload, env.Content[0].Text)
	}
	var roundtrip any
	if err := json.Unmarshal([]byte(env.Content[0].Text), &roundtrip); err != nil {
		t.Fatalf("payload block is no longer parseable JSON: %v", err)
	}

	// Second block is the notice.
	if env.Content[1].Type != "text" {
		t.Fatalf("notice block type = %q, want text", env.Content[1].Type)
	}
	if !strings.Contains(env.Content[1].Text, "3 pending") {
		t.Fatalf("notice block missing count: %q", env.Content[1].Text)
	}
	if strings.Contains(env.Content[1].Text, redditPayload) {
		t.Fatal("notice block leaked into payload")
	}
}

func TestAppendMeshNoticeBlock_SkipsErrorEnvelopes(t *testing.T) {
	input := mustJSON(t, map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": "boom"},
		},
	})
	out := appendMeshNoticeBlock(input, 5)
	if string(out) != string(input) {
		t.Fatalf("error envelope was modified.\n in: %s\nout: %s", input, out)
	}
}

func TestAppendMeshNoticeBlock_PreservesUnknownFields(t *testing.T) {
	input := mustJSON(t, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": "ok"},
		},
		"_meta": map[string]any{"cache_hit": true},
	})
	out := appendMeshNoticeBlock(input, 1)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := env["_meta"]; !ok {
		t.Fatalf("unknown field _meta dropped: %s", out)
	}
}

func TestAppendMeshNoticeBlock_EmptyContentReturnsUnchanged(t *testing.T) {
	input := mustJSON(t, map[string]any{"content": []map[string]any{}})
	out := appendMeshNoticeBlock(input, 2)
	if string(out) != string(input) {
		t.Fatalf("expected unchanged for empty content.\n in: %s\nout: %s", input, out)
	}
}

func TestHandleMeshReceiveReturnsBoundedPreviews(t *testing.T) {
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
	if _, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:     "event",
		Content:  strings.Repeat("a", mesh.MaxReceivePreviewBytes+25),
		Audience: sid,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.sessions.session = &store.Session{ID: sid, ClientType: "test"}
	args := mustJSON(t, map[string]any{
		"filter":            "new",
		"max_results":       mesh.MaxReceiveResults + 100,
		"max_content_bytes": mesh.MaxReceivePreviewBytes + 100,
	})
	out, rpcErr := h.handleMeshReceive(ctx, args)
	if rpcErr != nil {
		t.Fatalf("handleMeshReceive rpc error: %v", rpcErr)
	}
	text := singleTextResult(t, out)

	// The result must be a parseable JSON envelope — code-mode consumers
	// auto-unwrap it into an object and read .messages directly.
	var env mesh.ReceiveEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("mesh receive result is not parseable JSON: %v\n%q", err, text)
	}
	if len(env.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(env.Messages))
	}
	msg := env.Messages[0]

	// Cross-peer free text keeps the trust marker — per-field, not
	// whole-result, so the envelope stays valid JSON.
	if !strings.HasPrefix(msg.Preview, "<untrusted-content") || !strings.Contains(msg.Preview, `source="tool:mesh__receive"`) {
		t.Fatalf("message preview was not peer-origin enveloped: %q", msg.Preview)
	}
	if !strings.Contains(strings.Join(env.Notices, "\n"), "max_results capped at 20") {
		t.Fatalf("missing max_results cap notice: %v", env.Notices)
	}
	if !strings.Contains(strings.Join(env.Notices, "\n"), "content previews capped at 512 bytes/message") {
		t.Fatalf("missing preview cap notice: %v", env.Notices)
	}
	if strings.Contains(msg.Preview, strings.Repeat("a", mesh.DefaultReceivePreviewBytes+1)) {
		t.Fatalf("receive returned more than the preview cap: %q", msg.Preview)
	}
	if !msg.Truncated {
		t.Fatal("truncated flag not set on over-budget preview")
	}
	if msg.ContentBytes != mesh.MaxReceivePreviewBytes+25 {
		t.Fatalf("content_bytes = %d, want %d", msg.ContentBytes, mesh.MaxReceivePreviewBytes+25)
	}
	if !strings.Contains(env.Hint, "mesh__hydrate") {
		t.Fatalf("envelope hint did not point at hydration: %q", env.Hint)
	}
}

func TestHandleMeshWaitForEventWakesOnTaskReviewTransition(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh-wait.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := mesh.NewManager(db)

	const sid = "reviewer-session"
	const ws = "ws-global"
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.sessions.session = &store.Session{ID: sid, ClientType: "test"}

	type waitResult struct {
		raw json.RawMessage
		err *RPCError
	}
	waitArgs := mustJSON(t, map[string]any{
		"name":            "codex-reviewer",
		"role":            "reviewer",
		"kinds":           "task_event",
		"status_to":       "review",
		"timeout_seconds": 2,
	})
	done := make(chan waitResult, 1)
	go func() {
		out, rpcErr := h.handleMeshWaitForEvent(ctx, waitArgs)
		done <- waitResult{raw: out, err: rpcErr}
	}()

	select {
	case r := <-done:
		t.Fatalf("wait returned before event: rpc=%v raw=%s", r.err, r.raw)
	case <-time.After(100 * time.Millisecond):
	}

	sender := mesh.SessionMeta{SessionID: "sender", WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:     "task_event",
		Content:  "Task status_changed: Review me - doing -> review",
		Audience: "*",
		Tags:     "task_event:status_changed,task_id:01WAITREVIEW,status_from:doing,status_to:review",
	}); err != nil {
		t.Fatalf("send review event: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("wait rpc error: %v", r.err)
		}
		var env struct {
			TimedOut bool `json:"timed_out"`
			Count    int  `json:"count"`
			Messages []struct {
				Kind    string `json:"kind"`
				Tags    string `json:"tags"`
				Preview string `json:"preview"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(singleTextResult(t, r.raw)), &env); err != nil {
			t.Fatalf("wait result not parseable: %v", err)
		}
		if env.TimedOut || env.Count != 1 || len(env.Messages) != 1 {
			t.Fatalf("unexpected wait envelope: %+v", env)
		}
		if env.Messages[0].Kind != "task_event" || !strings.Contains(env.Messages[0].Tags, "task_id:01WAITREVIEW") {
			t.Fatalf("wait returned wrong event: %+v", env.Messages[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not wake after review transition")
	}
}

func TestHandleMeshCapsHonorSettings(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := mesh.NewManager(db)

	settingsSvc := config.NewSettingsService(db)
	settings := config.DefaultSettings()
	settings.MeshReceiveMaxResults = 3
	settings.MeshReceivePreviewBytes = 128
	settings.MeshSendMaxContentBytes = 2048
	if err := settingsSvc.Save(ctx, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	const sid = "receiver"
	const ws = "ws-global"
	receiver := mesh.SessionMeta{SessionID: sid, WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, mesh.ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	sender := mesh.SessionMeta{SessionID: "sender", WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:     "event",
		Content:  strings.Repeat("b", 256),
		Audience: sid,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.settingsSvc = settingsSvc
	h.sessions.session = &store.Session{ID: sid, ClientType: "test"}
	args := mustJSON(t, map[string]any{
		"filter":            "new",
		"max_results":       10,
		"max_content_bytes": 512,
	})
	out, rpcErr := h.handleMeshReceive(ctx, args)
	if rpcErr != nil {
		t.Fatalf("handleMeshReceive rpc error: %v", rpcErr)
	}
	text := singleTextResult(t, out)
	if !strings.Contains(text, "max_results capped at 3") {
		t.Fatalf("missing settings max_results cap notice: %q", text)
	}
	if !strings.Contains(text, "content previews capped at 128 bytes/message") {
		t.Fatalf("missing settings preview cap notice: %q", text)
	}
	if strings.Contains(text, strings.Repeat("b", 129)) {
		t.Fatalf("receive returned more than settings preview cap: %q", text)
	}

	sendArgs := mustJSON(t, map[string]any{
		"kind":    "event",
		"content": strings.Repeat("x", settings.MeshSendMaxContentBytes+1),
	})
	_, rpcErr = h.handleMeshSend(ctx, sendArgs)
	if rpcErr == nil {
		t.Fatal("expected settings send cap rejection")
	}
	if !strings.Contains(rpcErr.Message, "max 2048") {
		t.Fatalf("unexpected send cap error: %v", rpcErr)
	}
}

func TestHandleMeshListAgentsScopesToSessionWorkspace(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	for _, agent := range []store.MeshAgent{
		{
			SessionID: "agent-a", WorkspaceID: "ws-global", Name: "visible",
			ClientType: "test", Origin: store.MeshAgentOriginLocal,
			LastSeenAt: now, CreatedAt: now,
		},
		{
			SessionID: "agent-b", WorkspaceID: "ws-hidden", Name: "hidden",
			ClientType: "test", Origin: store.MeshAgentOriginLocal,
			LastSeenAt: now, CreatedAt: now,
		},
	} {
		if err := db.UpsertMeshAgent(ctx, &agent); err != nil {
			t.Fatalf("upsert agent %s: %v", agent.SessionID, err)
		}
	}

	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mesh.NewManager(db)
	out, rpcErr := h.handleMeshListAgents(ctx)
	if rpcErr != nil {
		t.Fatalf("handleMeshListAgents rpc error: %v", rpcErr)
	}
	text := singleTextResult(t, json.RawMessage(out))
	if !strings.Contains(text, "visible") {
		t.Fatalf("visible agent missing from directory: %q", text)
	}
	if strings.Contains(text, "hidden") || strings.Contains(text, "agent-b") {
		t.Fatalf("hidden workspace agent leaked into directory: %q", text)
	}
}

func TestHandleMeshHydrateRejectsOverlargeContentBudget(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mesh.NewManager(nil)
	args := mustJSON(t, map[string]any{
		"message_id":        "msg",
		"max_content_bytes": mesh.MaxHydrateContentBytes + 1,
	})

	_, rpcErr := h.handleMeshHydrate(context.Background(), args)
	if rpcErr == nil {
		t.Fatal("expected invalid params for overlarge hydrate content budget")
	}
	if !strings.Contains(rpcErr.Message, "max_content_bytes too large") {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
}

func TestHandleMeshSendRejectsOverlargeContent(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mesh.NewManager(nil)
	args := mustJSON(t, map[string]any{
		"kind":    "event",
		"content": strings.Repeat("x", mesh.MaxSendContentBytes+1),
	})

	_, rpcErr := h.handleMeshSend(context.Background(), args)
	if rpcErr == nil {
		t.Fatal("expected invalid params for overlarge send content")
	}
	if !strings.Contains(rpcErr.Message, "content too large") {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
}

func TestHandleMeshReceiveLargeBacklog(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := mesh.NewManager(db)

	const sid = "receiver-backlog"
	const ws = "ws-global"
	receiver := mesh.SessionMeta{SessionID: sid, WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, mesh.ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	sender := mesh.SessionMeta{SessionID: "sender-backlog", WorkspaceIDs: []string{ws}, ClientType: "test"}
	const backlogSize = 62
	const payloadSize = 8 * 1024
	for i := 0; i < backlogSize; i++ {
		content := strings.Repeat(fmt.Sprintf("delegation_reply payload %d ", i), payloadSize/30+1)
		if len(content) > payloadSize {
			content = content[:payloadSize]
		}
		if _, err := mgr.Send(ctx, sender, mesh.SendRequest{
			Kind:      "finding",
			Content:   content,
			Audience:  sid,
			Priority:  "normal",
			Tags:      "delegation_reply,token_preservation",
			ActorKind: "worker",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.sessions.session = &store.Session{ID: sid, ClientType: "test"}

	start := time.Now()
	out, rpcErr := h.handleMeshReceive(ctx, mustJSON(t, map[string]any{
		"filter": "new",
	}))
	elapsed := time.Since(start)
	if rpcErr != nil {
		t.Fatalf("handleMeshReceive rpc error: %v", rpcErr)
	}
	text := singleTextResult(t, out)

	var env mesh.ReceiveEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("mesh receive result is not parseable JSON: %v\n%q", err, text)
	}

	if len(env.Messages) > mesh.DefaultReceiveMaxResults {
		t.Fatalf("returned %d messages, want <= %d (default max_results)",
			len(env.Messages), mesh.DefaultReceiveMaxResults)
	}
	if len(env.Messages) == 0 {
		t.Fatal("expected messages from backlog, got 0")
	}

	for _, msg := range env.Messages {
		if msg.ContentBytes < 1024 {
			t.Errorf("message %s content_bytes=%d, want >= 1024 (large delegation_reply payload)",
				msg.ID, msg.ContentBytes)
		}
		previewLen := len(msg.Preview)
		if previewLen > mesh.DefaultReceivePreviewBytes+200 {
			t.Errorf("message %s preview too large: %d bytes (envelope overhead should be modest over %d cap)",
				msg.ID, previewLen, mesh.DefaultReceivePreviewBytes)
		}
		if !strings.Contains(msg.Preview, "delegation_reply payload") {
			t.Errorf("message %s preview missing expected content", msg.ID)
		}
	}

	if elapsed > 5*time.Second {
		t.Fatalf("receive on %d-message backlog took %v — should be fast with bounded previews",
			backlogSize, elapsed)
	}

	if env.Stats.NewForYou == 0 {
		t.Fatal("stats.new_for_you should report available messages")
	}

	remaining := backlogSize - len(env.Messages)
	if remaining > 0 {
		out2, rpcErr2 := h.handleMeshReceive(ctx, mustJSON(t, map[string]any{
			"filter": "new",
		}))
		if rpcErr2 != nil {
			t.Fatalf("second receive rpc error: %v", rpcErr2)
		}
		var env2 mesh.ReceiveEnvelope
		text2 := singleTextResult(t, out2)
		if err := json.Unmarshal([]byte(text2), &env2); err != nil {
			t.Fatalf("second receive not parseable: %v", err)
		}
		if len(env2.Messages) == 0 {
			t.Fatal("second receive returned 0 messages despite remaining backlog")
		}
		if len(env2.Messages)+len(env.Messages) > backlogSize {
			t.Fatalf("paginated total %d exceeds backlog %d",
				len(env2.Messages)+len(env.Messages), backlogSize)
		}
	}
}

func singleTextResult(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var env CallToolResult
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if len(env.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(env.Content))
	}
	return env.Content[0].Text
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
