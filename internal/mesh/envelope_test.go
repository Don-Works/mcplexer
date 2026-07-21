package mesh

import (
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestBuildReceiveEnvelopeExposesActorKindAndBodyHint: actor_kind was
// write-only (stored by migration 065, never surfaced), and consumers kept
// reading messages[].content (undefined — the field is `preview`). The
// envelope must expose actor_kind per message and document the body field.
func TestBuildReceiveEnvelopeExposesActorKindAndBodyHint(t *testing.T) {
	t.Parallel()
	res := &ReceiveResult{
		Messages: []store.MeshMessage{{
			ID: "01MSG", Kind: "finding", Priority: "normal",
			AgentName: "worker-7", ActorKind: "worker",
			Content: "hello", CreatedAt: time.Now().UTC(),
		}},
		TaskEventsExcluded: true,
	}
	env := BuildReceiveEnvelope(res, "self", ReceiveEnvelopeOptions{})

	if len(env.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(env.Messages))
	}
	if env.Messages[0].ActorKind != "worker" {
		t.Errorf("actor_kind = %q, want worker", env.Messages[0].ActorKind)
	}
	if !strings.Contains(env.Hint, "messages[].preview carries the message body") {
		t.Errorf("hint does not document the preview body field: %q", env.Hint)
	}
	if !strings.Contains(env.Hint, "mesh__hydrate") {
		t.Errorf("hint lost the hydrate pointer: %q", env.Hint)
	}
	if !strings.Contains(env.Hint, "task_event") {
		t.Errorf("hint does not surface the default task_event exclusion: %q", env.Hint)
	}
}

// TestBuildReceiveEnvelopePopulatesCreatedAt: agents mapping over messages
// kept reading a `.created_at` that did not exist (only relative `age` was
// exposed). created_at must now carry the absolute send time as RFC3339 UTC,
// and a zero CreatedAt must render as "" rather than the year-0001 zero value.
func TestBuildReceiveEnvelopePopulatesCreatedAt(t *testing.T) {
	t.Parallel()
	sent := time.Date(2026, 7, 20, 9, 30, 0, 0, time.UTC)
	res := &ReceiveResult{Messages: []store.MeshMessage{
		{ID: "01REAL", Kind: "finding", Content: "x", CreatedAt: sent},
		{ID: "01ZERO", Kind: "finding", Content: "y"}, // zero CreatedAt
	}}
	env := BuildReceiveEnvelope(res, "self", ReceiveEnvelopeOptions{})
	if len(env.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(env.Messages))
	}
	if got, want := env.Messages[0].CreatedAt, "2026-07-20T09:30:00Z"; got != want {
		t.Errorf("created_at = %q, want %q", got, want)
	}
	if env.Messages[0].Age == "" {
		t.Errorf("age should still be populated alongside created_at")
	}
	if env.Messages[1].CreatedAt != "" {
		t.Errorf("zero CreatedAt should render empty, got %q", env.Messages[1].CreatedAt)
	}
}

func TestBuildReceiveEnvelopeHintOmitsTaskEventNoteWhenIncluded(t *testing.T) {
	t.Parallel()
	env := BuildReceiveEnvelope(&ReceiveResult{}, "self", ReceiveEnvelopeOptions{})
	if strings.Contains(env.Hint, "task_event") {
		t.Errorf("hint mentions task_event exclusion on an opted-in read: %q", env.Hint)
	}
}
