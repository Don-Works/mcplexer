// handler_tasks_compose_into_test.go — coverage of the compose_into
// ergonomic shortcuts on task__create: short prefix, "last" sentinel,
// and the unambiguous failure modes (too-short, ambiguous prefix,
// no-prior-create-in-session). Closes 01KSGCXFWCFCJKFYC4P5V4795N.
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// TestComposeInto_FullULIDPassesThrough confirms the legacy behaviour
// is preserved: a full 26-char ULID still resolves to the same parent.
func TestComposeInto_FullULIDPassesThrough(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandlerWithSession(t)

	parentRaw, _ := json.Marshal(map[string]any{"title": "epic"})
	resp, rpcErr := h.handleTaskCreate(ctx, parentRaw)
	if rpcErr != nil {
		t.Fatalf("create parent: %v", rpcErr)
	}
	parentID := unwrapResult(t, resp)["id"].(string)
	if len(parentID) != 26 {
		t.Fatalf("expected 26-char ULID, got %q (len=%d)", parentID, len(parentID))
	}

	childRaw, _ := json.Marshal(map[string]any{
		"title":        "child",
		"compose_into": parentID,
	})
	resp, rpcErr = h.handleTaskCreate(ctx, childRaw)
	if rpcErr != nil {
		t.Fatalf("create child: %v", rpcErr)
	}
	childID := unwrapResult(t, resp)["id"].(string)
	assertParentChild(t, h, ctx, wsID, parentID, childID)
}

// TestComposeInto_PrefixResolution confirms an 8+ char prefix resolves
// when unique, AND that the resolved parent receives the child link.
func TestComposeInto_PrefixResolution(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandlerWithSession(t)

	parentRaw, _ := json.Marshal(map[string]any{"title": "prefix-parent"})
	resp, rpcErr := h.handleTaskCreate(ctx, parentRaw)
	if rpcErr != nil {
		t.Fatalf("create parent: %v", rpcErr)
	}
	parentID := unwrapResult(t, resp)["id"].(string)
	prefix := parentID[:10] // 10 chars — well above the 8-min, very likely unique

	childRaw, _ := json.Marshal(map[string]any{
		"title":        "prefix-child",
		"compose_into": prefix,
	})
	resp, rpcErr = h.handleTaskCreate(ctx, childRaw)
	if rpcErr != nil {
		t.Fatalf("create child via prefix: %v", rpcErr)
	}
	childID := unwrapResult(t, resp)["id"].(string)
	assertParentChild(t, h, ctx, wsID, parentID, childID)
}

// TestComposeInto_LastSentinel confirms the "last" alias resolves to
// the most recently created task in the session+workspace pair.
func TestComposeInto_LastSentinel(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandlerWithSession(t)

	// Sequence: create epic -> compose child with "last".
	parentRaw, _ := json.Marshal(map[string]any{"title": "epic-for-last"})
	resp, rpcErr := h.handleTaskCreate(ctx, parentRaw)
	if rpcErr != nil {
		t.Fatalf("create parent: %v", rpcErr)
	}
	parentID := unwrapResult(t, resp)["id"].(string)

	childRaw, _ := json.Marshal(map[string]any{
		"title":        "child-via-last",
		"compose_into": "last",
	})
	resp, rpcErr = h.handleTaskCreate(ctx, childRaw)
	if rpcErr != nil {
		t.Fatalf("create child via \"last\": %v", rpcErr)
	}
	childID := unwrapResult(t, resp)["id"].(string)
	assertParentChild(t, h, ctx, wsID, parentID, childID)

	// "last" must now point at the child — a follow-up "last" should
	// land grandchild under child, not under parent.
	grandRaw, _ := json.Marshal(map[string]any{
		"title":        "grand-via-last",
		"compose_into": "last",
	})
	resp, rpcErr = h.handleTaskCreate(ctx, grandRaw)
	if rpcErr != nil {
		t.Fatalf("create grandchild via \"last\": %v", rpcErr)
	}
	grandID := unwrapResult(t, resp)["id"].(string)
	assertParentChild(t, h, ctx, wsID, childID, grandID)
}

// TestComposeInto_LastWithoutPriorCreateFails confirms the deterministic
// error message when "last" has no recorded id for this session+ws —
// the agent gets actionable feedback, not a silent miss.
func TestComposeInto_LastWithoutPriorCreateFails(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandlerWithSession(t)

	raw, _ := json.Marshal(map[string]any{
		"title":        "stranded",
		"compose_into": "last",
	})
	resp, _ := h.handleTaskCreate(ctx, raw)
	if !isErrResult(resp) {
		t.Fatalf("expected error result, got success: %s", string(resp))
	}
	text := extractToolText(t, resp)
	if !strings.Contains(text, "\"last\"") {
		t.Errorf("expected error to reference \"last\" sentinel, got %q", text)
	}
	if !strings.Contains(text, "session") {
		t.Errorf("expected error to mention session+workspace context, got %q", text)
	}
}

// TestComposeInto_PrefixTooShortFails confirms <8-char prefixes are
// rejected with a guiding message — better UX than scanning the
// workspace and finding "100 matches".
func TestComposeInto_PrefixTooShortFails(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandlerWithSession(t)

	raw, _ := json.Marshal(map[string]any{
		"title":        "tiny",
		"compose_into": "01KS", // 4 chars — below the floor
	})
	resp, _ := h.handleTaskCreate(ctx, raw)
	if !isErrResult(resp) {
		t.Fatalf("expected error result for too-short prefix, got %s", string(resp))
	}
	text := extractToolText(t, resp)
	if !strings.Contains(text, "too short") {
		t.Errorf("expected too-short hint, got %q", text)
	}
}

// TestComposeInto_AmbiguousPrefixListsCandidates confirms the agent
// gets a candidate list back when their prefix matches >1 task — the
// "did you mean" UX flagged in the source task's acceptance criteria.
func TestComposeInto_AmbiguousPrefixListsCandidates(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	// Seed two tasks with a shared id prefix. We force the ids so the
	// collision is deterministic — the ULID generator wouldn't normally
	// produce duplicates this aggressively in a single ms. IDs use only
	// the Crockford ULID alphabet (no I/L/O/U) so the sanitizer keeps
	// them intact.
	for _, id := range []string{
		"01KSAMBGAA0000000000000004",
		"01KSAMBGAA0000000000000005",
	} {
		if err := db.CreateTask(ctx, &store.Task{
			ID: id, WorkspaceID: wsID, Title: "t-" + id, Status: "open",
		}); err != nil {
			t.Fatalf("seed CreateTask %s: %v", id, err)
		}
	}

	raw, _ := json.Marshal(map[string]any{
		"title":        "ambiguous-child",
		"compose_into": "01KSAMBGA", // 9 chars — matches both seeds
	})
	resp, _ := h.handleTaskCreate(ctx, raw)
	if !isErrResult(resp) {
		t.Fatalf("expected ambiguous-prefix error, got success: %s", string(resp))
	}
	text := extractToolText(t, resp)
	if !strings.Contains(text, "ambiguous") {
		t.Errorf("expected error to say 'ambiguous', got %q", text)
	}
	// Both candidates should be listed verbatim so the agent can re-call.
	if !strings.Contains(text, "01KSAMBGAA0000000000000004") || !strings.Contains(text, "01KSAMBGAA0000000000000005") {
		t.Errorf("expected both candidate ids in error, got %q", text)
	}
}

// TestComposeInto_NoPrefixMatchFails confirms a unique-but-unmatched
// prefix surfaces a no-match error instead of silently creating a
// top-level task — the agent expected composition; not getting it
// should not be silent.
func TestComposeInto_NoPrefixMatchFails(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandlerWithSession(t)

	raw, _ := json.Marshal(map[string]any{
		"title":        "orphan",
		"compose_into": "01KSDOESNOTEXIST",
	})
	resp, _ := h.handleTaskCreate(ctx, raw)
	if !isErrResult(resp) {
		t.Fatalf("expected no-match error, got success: %s", string(resp))
	}
	text := extractToolText(t, resp)
	if !strings.Contains(text, "no task") {
		t.Errorf("expected 'no task' hint, got %q", text)
	}
}

// assertParentChild fetches both tasks and asserts the parent-child
// link is bidirectional in meta (composed_by on child, composes on
// parent) — exactly the contract task__compose promises.
func assertParentChild(t *testing.T, h *handler, ctx context.Context, wsID, parentID, childID string) {
	t.Helper()
	parent, err := h.tasksSvc.Get(ctx, wsID, parentID)
	if err != nil {
		t.Fatalf("fetch parent: %v", err)
	}
	child, err := h.tasksSvc.Get(ctx, wsID, childID)
	if err != nil {
		t.Fatalf("fetch child: %v", err)
	}
	composes := tasks.ReadMetaList(parent.Meta, "composes")
	if !stringSliceContains(composes, childID) {
		t.Errorf("parent.meta.composes = %v, want to contain %s", composes, childID)
	}
	composedBy := tasks.ReadMetaList(child.Meta, "composed_by")
	if !stringSliceContains(composedBy, parentID) {
		t.Errorf("child.meta.composed_by = %v, want to contain %s", composedBy, parentID)
	}
}

func stringSliceContains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

// extractToolText pulls Content[0].Text out of an MCP tool result —
// used to assert against the human-readable error string surfaced via
// marshalErrorResult (which goes out as an isError=true text block).
func extractToolText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var env struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content envelope: %s", string(raw))
	}
	return env.Content[0].Text
}
