package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeMeshSender captures the last Send call so tests can assert what the
// telegram manager persists for an agent-outbound message.
type fakeMeshSender struct {
	meta    mesh.SessionMeta
	req     mesh.SendRequest
	calls   int
	failNow bool
}

func (f *fakeMeshSender) Send(
	_ context.Context, meta mesh.SessionMeta, req mesh.SendRequest,
) (*store.MeshMessage, error) {
	f.calls++
	f.meta = meta
	f.req = req
	if f.failNow {
		return nil, errors.New("boom")
	}
	return &store.MeshMessage{ID: "msg-1"}, nil
}

func TestRecordOutboundToMesh_TagsAndScope(t *testing.T) {
	fake := &fakeMeshSender{}
	m := &Manager{mesh: fake}
	chat := store.TelegramChat{
		ID:          "chat-1",
		SessionID:   "telegram-999",
		WorkspaceID: "ws-1",
	}

	m.recordOutboundToMesh(context.Background(), chat, "hello from another agent")

	if fake.calls != 1 {
		t.Fatalf("mesh.Send called %d times, want 1", fake.calls)
	}
	if fake.req.Content != "hello from another agent" {
		t.Errorf("content = %q", fake.req.Content)
	}
	if fake.req.Tags != "telegram,agent-outbound" {
		t.Errorf("tags = %q, want telegram,agent-outbound", fake.req.Tags)
	}
	// Must NOT carry the "human" trigger tag, or it would re-fire the
	// responder (migration 110 scopes the trigger to tag_match='human').
	if contains(fake.req.Tags, "human") {
		t.Errorf("agent-outbound tags must not contain 'human': %q", fake.req.Tags)
	}
	// The persisted mesh-row priority is hardcoded "high" so these rows
	// aren't archived ahead of the high-priority telegram conversation.
	if fake.req.Priority != "high" {
		t.Errorf("priority = %q, want high (hardcoded)", fake.req.Priority)
	}
	if !fake.req.LocalOnly {
		t.Error("agent-outbound mesh row should be LocalOnly")
	}
	if fake.req.ActorKind != "agent" {
		t.Errorf("actor_kind = %q, want agent", fake.req.ActorKind)
	}
	if got := fake.meta.WorkspaceIDs; len(got) != 1 || got[0] != "ws-1" {
		t.Errorf("workspace scope = %v, want [ws-1]", got)
	}
	if fake.meta.SessionID != "telegram-999" {
		t.Errorf("session = %q, want telegram-999", fake.meta.SessionID)
	}
	if fake.meta.ModelHint != "agent" {
		t.Errorf("model hint = %q, want agent", fake.meta.ModelHint)
	}
}

func TestRecordOutboundToMesh_SkipsEmptyAndNilMesh(t *testing.T) {
	fake := &fakeMeshSender{}
	m := &Manager{mesh: fake}
	m.recordOutboundToMesh(context.Background(), store.TelegramChat{}, "   ")
	if fake.calls != 0 {
		t.Fatalf("blank text should not persist; calls = %d", fake.calls)
	}

	// nil mesh must not panic.
	mNil := &Manager{}
	mNil.recordOutboundToMesh(context.Background(), store.TelegramChat{}, "hi")
}

func TestRecordOutboundToMesh_SendErrorIsBestEffort(t *testing.T) {
	fake := &fakeMeshSender{failNow: true}
	m := &Manager{mesh: fake}
	// Must not panic / propagate — best-effort persistence.
	m.recordOutboundToMesh(context.Background(), store.TelegramChat{ID: "c"}, "hi")
	if fake.calls != 1 {
		t.Fatalf("Send should have been attempted once, got %d", fake.calls)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
