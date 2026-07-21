// handler_memory_valid_at_test.go — gateway-level coverage for the
// bi-temporal as-of (valid_at) filter on memory__recall + memory__list.
//
// The store-level point-in-time semantics are already proven in
// internal/store/sqlite/memory_test.go (TestListMemoriesValidAtPointInTime).
// These tests verify only the gateway layer's job: parse the valid_at
// argument off the wire, propagate it onto MemoryFilter.ValidAt, and return
// a clear RPC error for an unparseable timestamp. We seed v1/v2 directly via
// the store so the validity windows are deterministic (the service Write path
// stamps wall-clock instants and exposes no TValidStart knob).
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newHandlerWithMemoryStore is like newHandlerWithMemoryDB but also returns
// the underlying *sqlite.DB so a test can seed rows with explicit validity
// windows (TValidStart), which WriteOptions does not expose.
func newHandlerWithMemoryStore(t *testing.T) (*handler, *memory.Service, *sqlite.DB) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.memorySvc = svc
	return h, svc, d
}

// seedSupersededFact writes v1 (valid from t0) then v2 (valid from t1) under
// the same name+workspace so the fact-supersession path stamps v1's
// t_valid_end. Returns the two ids plus the actual t_valid_end the
// supersession recorded on v1 (wall-clock, learned by reading back).
func seedSupersededFact(
	t *testing.T, d *sqlite.DB, wsID string, t0, t1 time.Time,
) (v1ID, v2ID string, v1End time.Time) {
	t.Helper()
	ctx := context.Background()
	ws := &wsID
	v1 := &store.MemoryEntry{
		Name: "policy", Kind: store.MemoryKindFact, Content: "v1 belief",
		WorkspaceID: ws, TValidStart: t0,
	}
	if err := d.WriteMemory(ctx, v1); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	v2 := &store.MemoryEntry{
		Name: "policy", Kind: store.MemoryKindFact, Content: "v2 belief",
		WorkspaceID: ws, TValidStart: t1,
	}
	if err := d.WriteMemory(ctx, v2); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	hist, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}, IncludeInvalid: true,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	var end *time.Time
	for i := range hist {
		if hist[i].ID == v1.ID {
			end = hist[i].TValidEnd
		}
	}
	if end == nil {
		t.Fatal("v1 was not invalidated by v2 supersession")
	}
	return v1.ID, v2.ID, *end
}

// dispatchMemoryListIDs calls memory__list with the given extra JSON and
// returns the ordered ids in the structured response.
func dispatchMemoryListIDs(
	t *testing.T, h *handler, ctx context.Context, extra string,
) []string {
	t.Helper()
	raw := json.RawMessage(`{"include_invalid":false` + extra + `}`)
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__list", raw)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__list: handled=%v rpcErr=%v", handled, rpcErr)
	}
	return memoryIDsFromToolResult(t, resp, "memories")
}

// dispatchMemoryRecallIDs calls memory__recall with an empty query (browse
// mode) plus the given extra JSON and returns the ordered hit ids.
func dispatchMemoryRecallIDs(
	t *testing.T, h *handler, ctx context.Context, extra string,
) []string {
	t.Helper()
	raw := json.RawMessage(`{"query":""` + extra + `}`)
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__recall", raw)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__recall: handled=%v rpcErr=%v", handled, rpcErr)
	}
	return memoryIDsFromToolResult(t, resp, "hits")
}

// dispatchMemoryRecallQueryIDs calls memory__recall with a NON-EMPTY query
// (so the FTS SearchMemories arm runs, not the empty-query ListMemories
// browse path) plus the given extra JSON, returning the ordered hit ids.
func dispatchMemoryRecallQueryIDs(
	t *testing.T, h *handler, ctx context.Context, query, extra string,
) []string {
	t.Helper()
	raw := json.RawMessage(`{"query":"` + query + `"` + extra + `}`)
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__recall", raw)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__recall: handled=%v rpcErr=%v", handled, rpcErr)
	}
	return memoryIDsFromToolResult(t, resp, "hits")
}

// memoryIDsFromToolResult unwraps the MCP tool-result envelope and pulls the
// id field out of each element of the named array (memories|hits).
func memoryIDsFromToolResult(t *testing.T, resp json.RawMessage, arrayKey string) []string {
	t.Helper()
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw=%s)", err, string(resp))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty tool-result content: %s", string(resp))
	}
	var payload struct {
		Memories []struct {
			ID string `json:"id"`
		} `json:"memories"`
		Hits []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(env.Content[0].Text), &payload); err != nil {
		t.Fatalf("unmarshal payload %q: %v", env.Content[0].Text, err)
	}
	var ids []string
	switch arrayKey {
	case "memories":
		for _, m := range payload.Memories {
			ids = append(ids, m.ID)
		}
	case "hits":
		for _, hh := range payload.Hits {
			ids = append(ids, hh.ID)
		}
	}
	return ids
}

func TestMemoryListValidAt(t *testing.T) {
	ctx := context.Background()
	h, _, d := newHandlerWithMemoryStore(t)
	wsID := "ws-asof-list"
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: wsID, RootPath: "/test/asof"},
	}

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.AddDate(0, 0, 30)
	v1ID, v2ID, v1End := seedSupersededFact(t, d, wsID, t0, t1)

	tests := []struct {
		name    string
		validAt string // RFC3339, "" = omit valid_at
		wantIDs []string
	}{
		{
			name:    "as-of inside v1 window returns v1",
			validAt: t0.Add(time.Hour).Format(time.RFC3339),
			wantIDs: []string{v1ID},
		},
		{
			name:    "as-of after supersession returns v2",
			validAt: v1End.Add(time.Hour).Format(time.RFC3339),
			wantIDs: []string{v2ID},
		},
		{
			name:    "as-of before t0 returns nothing",
			validAt: t0.Add(-time.Hour).Format(time.RFC3339),
			wantIDs: nil,
		},
		{
			name:    "omitting valid_at returns only the active row",
			validAt: "",
			wantIDs: []string{v2ID},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := `,"scope":"workspace_only"`
			if tc.validAt != "" {
				extra += `,"valid_at":"` + tc.validAt + `"`
			}
			got := dispatchMemoryListIDs(t, h, ctx, extra)
			assertSameIDs(t, got, tc.wantIDs)
		})
	}
}

func TestMemoryRecallValidAt(t *testing.T) {
	ctx := context.Background()
	h, _, d := newHandlerWithMemoryStore(t)
	wsID := "ws-asof-recall"
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: wsID, RootPath: "/test/asof-recall"},
	}

	t0 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.AddDate(0, 0, 30)
	v1ID, v2ID, v1End := seedSupersededFact(t, d, wsID, t0, t1)

	tests := []struct {
		name    string
		validAt string
		wantIDs []string
	}{
		{
			name:    "as-of inside v1 window returns v1",
			validAt: t0.Add(time.Hour).Format(time.RFC3339),
			wantIDs: []string{v1ID},
		},
		{
			name:    "as-of after supersession returns v2",
			validAt: v1End.Add(time.Hour).Format(time.RFC3339),
			wantIDs: []string{v2ID},
		},
		{
			name:    "omitting valid_at returns only the active row",
			validAt: "",
			wantIDs: []string{v2ID},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := ""
			if tc.validAt != "" {
				extra = `,"valid_at":"` + tc.validAt + `"`
			}
			got := dispatchMemoryRecallIDs(t, h, ctx, extra)
			assertSameIDs(t, got, tc.wantIDs)
		})
	}
}

// TestMemoryRecallValidAtNonEmptyQuery proves valid_at filters a real FTS
// (SearchMemories) result set, not just the empty-query browse path. The
// service runs with NoopEmbedder, so a non-empty query exercises the
// FTS-only recall arm. The query "belief" matches both v1 ("v1 belief") and
// v2 ("v2 belief"); the as-of filter must restrict the FTS hits to whichever
// row was valid at the instant.
func TestMemoryRecallValidAtNonEmptyQuery(t *testing.T) {
	ctx := context.Background()
	h, _, d := newHandlerWithMemoryStore(t)
	wsID := "ws-asof-recall-fts"
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: wsID, RootPath: "/test/asof-recall-fts"},
	}

	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.AddDate(0, 0, 30)
	v1ID, v2ID, v1End := seedSupersededFact(t, d, wsID, t0, t1)

	// Sanity: with no valid_at the FTS query returns the active row (v2).
	gotCurrent := dispatchMemoryRecallQueryIDs(t, h, ctx, "belief", "")
	assertSameIDs(t, gotCurrent, []string{v2ID})

	// As-of inside v1's window: FTS hits filtered to v1 only.
	extraV1 := `,"valid_at":"` + t0.Add(time.Hour).Format(time.RFC3339) + `"`
	gotV1 := dispatchMemoryRecallQueryIDs(t, h, ctx, "belief", extraV1)
	assertSameIDs(t, gotV1, []string{v1ID})

	// As-of after supersession: FTS hits filtered to v2 only.
	extraV2 := `,"valid_at":"` + v1End.Add(time.Hour).Format(time.RFC3339) + `"`
	gotV2 := dispatchMemoryRecallQueryIDs(t, h, ctx, "belief", extraV2)
	assertSameIDs(t, gotV2, []string{v2ID})
}

func TestMemoryValidAtInvalidTimestamp(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newHandlerWithMemoryStore(t)

	for _, tool := range []string{"memory__recall", "memory__list"} {
		t.Run(tool, func(t *testing.T) {
			raw := json.RawMessage(`{"query":"","valid_at":"not-a-timestamp"}`)
			_, rpcErr, handled := h.dispatchMemoryTool(ctx, tool, raw)
			if !handled {
				t.Fatalf("%s not handled", tool)
			}
			if rpcErr == nil {
				t.Fatalf("%s: expected RPC error for bad valid_at, got nil", tool)
			}
			if rpcErr.Code != CodeInvalidParams {
				t.Errorf("%s: code=%d want CodeInvalidParams(%d)", tool, rpcErr.Code, CodeInvalidParams)
			}
			if !strings.Contains(rpcErr.Message, "valid_at") || !strings.Contains(rpcErr.Message, "RFC3339") {
				t.Errorf("%s: message should name valid_at + RFC3339; got %q", tool, rpcErr.Message)
			}
		})
	}
}

// assertSameIDs compares two id sets order-independently.
func assertSameIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("id count: got %v, want %v", got, want)
	}
	seen := make(map[string]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] == 0 {
			t.Fatalf("missing id %s: got %v, want %v", id, got, want)
		}
		seen[id]--
	}
}
