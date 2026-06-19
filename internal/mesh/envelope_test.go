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

func TestBuildReceiveEnvelopeHintOmitsTaskEventNoteWhenIncluded(t *testing.T) {
	t.Parallel()
	env := BuildReceiveEnvelope(&ReceiveResult{}, "self", ReceiveEnvelopeOptions{})
	if strings.Contains(env.Hint, "task_event") {
		t.Errorf("hint mentions task_event exclusion on an opted-in read: %q", env.Hint)
	}
}
