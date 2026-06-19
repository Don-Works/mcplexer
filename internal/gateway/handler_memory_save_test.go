// handler_memory_save_test.go — gateway-level coverage for the memory__save
// response surfacing the post-write neighbour-scan candidates (wave-B2 item 1).
//
// The Service-layer scan (surfaceContradictions) is proven in
// internal/memory; this test verifies only the gateway's job: switch the
// note-write path to WriteWithResult and surface the candidate ids in the
// save-tool response (both the human text line AND structuredContent's
// possible_duplicates). Before the wiring the handler called the back-compat
// Write facade and discarded WriteResult.Candidates — the scan ran for zero
// user-facing benefit.
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// dispatchMemorySave calls memory__save with the given content (global scope so
// requireWorkspaceWrite is skipped) and returns the raw tool-result envelope.
func dispatchMemorySave(t *testing.T, h *handler, ctx context.Context, content string) json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"content": content, "scope": "global"})
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__save", body)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__save: handled=%v rpcErr=%v", handled, rpcErr)
	}
	return resp
}

func TestMemorySave_SurfacesPossibleDuplicates(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newHandlerWithMemoryStore(t)

	// First note: no prior memories, so no candidates surface.
	first := dispatchMemorySave(t, h, ctx,
		"deployment pipeline rollback procedure requires manual approval gate")
	if dup := possibleDuplicates(t, first); len(dup) != 0 {
		t.Fatalf("first save should surface no duplicates, got %v", dup)
	}
	if txt := toolResultText(t, first); strings.Contains(txt, "possibly-related") {
		t.Fatalf("first save text should not mention possibly-related: %q", txt)
	}
	firstID := structuredID(t, first)

	// Second note shares enough distinctive significant tokens with the first
	// (deployment, pipeline, rollback, procedure, approval) to clear the
	// overlap gate, so the first surfaces as a possible duplicate.
	second := dispatchMemorySave(t, h, ctx,
		"deployment pipeline rollback procedure now needs two approval reviewers")
	dup := possibleDuplicates(t, second)
	if len(dup) != 1 || dup[0] != firstID {
		t.Fatalf("second save should surface the first note %q as a duplicate, got %v", firstID, dup)
	}
	txt := toolResultText(t, second)
	if !strings.Contains(txt, "possibly-related") || !strings.Contains(txt, firstID) {
		t.Fatalf("second save text should name the possibly-related id %q; got %q", firstID, txt)
	}
}

// possibleDuplicates reads structuredContent.possible_duplicates (nil/absent
// when no candidates surfaced).
func possibleDuplicates(t *testing.T, resp json.RawMessage) []string {
	t.Helper()
	var env struct {
		Structured struct {
			PossibleDuplicates []string `json:"possible_duplicates"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal structured: %v (raw=%s)", err, string(resp))
	}
	return env.Structured.PossibleDuplicates
}

// structuredID reads structuredContent.id, falling back to parsing the text
// line "Saved memory <name> (<id>) ..." when no structured block is present
// (the no-candidate case omits structuredContent).
func structuredID(t *testing.T, resp json.RawMessage) string {
	t.Helper()
	var env struct {
		Structured struct {
			ID string `json:"id"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(resp, &env); err == nil && env.Structured.ID != "" {
		return env.Structured.ID
	}
	txt := toolResultText(t, resp)
	open := strings.Index(txt, "(")
	closeIdx := strings.Index(txt, ")")
	if open < 0 || closeIdx <= open {
		t.Fatalf("could not parse id from save text: %q", txt)
	}
	return txt[open+1 : closeIdx]
}
