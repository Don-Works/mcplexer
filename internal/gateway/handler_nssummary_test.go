package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestNamespaceSummaryStableAcrossToolCountDrift (T-A): the execute_code tool
// description embeds the namespace summary, and any byte change to a tool
// definition invalidates the harness's entire prompt cache. The summary must
// therefore stay byte-identical when a namespace merely gains or loses tools,
// and only change when a namespace appears or disappears.
func TestNamespaceSummaryStableAcrossToolCountDrift(t *testing.T) {
	ctx := context.Background()
	ghTools := func(n int) json.RawMessage {
		tools := make([]Tool, 0, n)
		for i := range n {
			tools = append(tools, Tool{
				Name:        "tool_" + string(rune('a'+i)),
				InputSchema: json.RawMessage(`{"type":"object"}`),
			})
		}
		return toolsJSON(tools...)
	}

	lister := &mockToolLister{tools: map[string]json.RawMessage{"gh": ghTools(2)}}
	h, _ := newTestHandler(lister, []store.DownstreamServer{{
		ID: "gh", Name: "GitHub",
		ToolNamespace: "github", Discovery: "static",
	}})

	before := h.buildNamespaceSummary(ctx)
	if before == "" {
		t.Fatal("expected a non-empty namespace summary")
	}
	if strings.ContainsAny(before, "()0123456789") {
		t.Fatalf("summary must not embed live tool counts (prompt-cache buster): %q", before)
	}
	if !strings.Contains(before, "github") {
		t.Fatalf("summary missing namespace: %q", before)
	}

	// The namespace gains a tool — the summary must not move a byte.
	lister.tools["gh"] = ghTools(3)
	h.toolsListCache.Flush()
	after := h.buildNamespaceSummary(ctx)
	if after != before {
		t.Fatalf("summary changed on tool-count drift:\n before: %q\n after:  %q", before, after)
	}
}
