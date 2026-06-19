package runner

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestFormatMeshHistory covers the mesh-history rendering contract: the
// triggering message and worker-lifecycle rows are excluded, the block is
// ordered oldest-first, and each message is size-capped so one verbose row
// can't blow up the prompt window.
func TestFormatMeshHistory(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC)
	long := strings.Repeat("x", meshHistoryMaxCharsPerMsg+500)
	// QueryMeshMessages returns newest-first.
	msgs := []store.MeshMessage{
		{ID: "m4", AgentName: "Operator", Content: "newest", CreatedAt: base.Add(4 * time.Minute)},
		{ID: "trigger", AgentName: "Operator", Content: "the trigger line", CreatedAt: base.Add(3 * time.Minute)},
		{ID: "life", AgentName: "worker", Content: "lifecycle noise", Tags: "worker,worker_started", CreatedAt: base.Add(2 * time.Minute)},
		{ID: "m2", AgentName: "bot", Content: long, CreatedAt: base.Add(1 * time.Minute)},
		{ID: "m1", AgentName: "Operator", Content: "oldest", CreatedAt: base},
	}

	got := formatMeshHistory(msgs, "trigger", 10)

	if strings.Contains(got, "the trigger line") {
		t.Error("triggering message must be excluded from history")
	}
	if strings.Contains(got, "lifecycle noise") {
		t.Error("worker lifecycle row must be excluded from history")
	}
	if io, in := strings.Index(got, "oldest"), strings.Index(got, "newest"); io < 0 || in < 0 || io > in {
		t.Errorf("expected oldest-first ordering, got:\n%s", got)
	}
	if strings.Contains(got, long) {
		t.Error("long message content must be size-capped")
	}
	if !strings.Contains(got, "…") {
		t.Error("expected a truncation marker on the over-long message")
	}
}

// TestFormatMeshHistoryRespectsLimit confirms only the most recent `limit`
// conversation rows are kept — the core "only pipe the last N turns" bound.
func TestFormatMeshHistoryRespectsLimit(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC)
	var msgs []store.MeshMessage
	for i := 0; i < 20; i++ {
		msgs = append(msgs, store.MeshMessage{
			ID:        "m" + strconv.Itoa(i),
			AgentName: "Operator",
			Content:   "line" + strconv.Itoa(i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	got := formatMeshHistory(msgs, "", 5)
	if lines := strings.Count(got, "\n") + 1; got == "" || lines != 5 {
		t.Errorf("expected exactly 5 lines, got %d:\n%s", lines, got)
	}
}

// TestHistoryTags covers the opt-in tag-scoping knob: only when
// mesh_history_tags is set does the window scope to a conversation.
func TestHistoryTags(t *testing.T) {
	t.Parallel()
	if got := historyTags(map[string]any{}); got != "" {
		t.Errorf("absent param should yield empty, got %q", got)
	}
	if got := historyTags(map[string]any{"mesh_history_tags": " telegram "}); got != "telegram" {
		t.Errorf("expected trimmed 'telegram', got %q", got)
	}
}
