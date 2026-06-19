// memory_entries_valid_at_test.go — REST coverage for the bi-temporal as-of
// (valid_at) filter on POST /api/v1/memory/search.
//
// The store-level point-in-time semantics are proven in
// internal/store/sqlite/memory_test.go. These tests verify only the REST
// layer's job: accept valid_at (JSON body or ?valid_at= querystring), thread
// it onto MemoryFilter.ValidAt, and return a clear 400 for an unparseable
// timestamp. v1/v2 are seeded directly via the store so validity windows are
// deterministic (the create endpoint stamps wall-clock instants).
package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// seedSupersededFactAPI writes v1 (valid from t0) then v2 (valid from t1)
// under the same name+workspace so the fact-supersession path stamps v1's
// t_valid_end. Returns the ids plus the wall-clock t_valid_end recorded on v1.
func seedSupersededFactAPI(
	t *testing.T, db *sqlite.DB, wsID string, t0, t1 time.Time,
) (v1ID, v2ID string, v1End time.Time) {
	t.Helper()
	ctx := context.Background()
	ws := &wsID
	v1 := &store.MemoryEntry{
		Name: "policy", Kind: store.MemoryKindFact, Content: "v1 belief",
		WorkspaceID: ws, TValidStart: t0,
	}
	if err := db.WriteMemory(ctx, v1); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	v2 := &store.MemoryEntry{
		Name: "policy", Kind: store.MemoryKindFact, Content: "v2 belief",
		WorkspaceID: ws, TValidStart: t1,
	}
	if err := db.WriteMemory(ctx, v2); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	hist, err := db.ListMemories(ctx, store.MemoryFilter{
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

// searchIDs hits POST /api/v1/memory/search with the given body and returns
// the ordered hit ids.
func searchIDs(t *testing.T, url string, body map[string]any) []string {
	t.Helper()
	hits := mustSearchMemory(t, url, body)
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		if entry, ok := h["entry"].(map[string]any); ok {
			if id, ok := entry["id"].(string); ok {
				ids = append(ids, id)
				continue
			}
		}
		if id, ok := h["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestMemorySearchValidAt_Body(t *testing.T) {
	srv, db, _ := newMemoryTestServer(t)
	wsID := "ws-asof-rest"
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.AddDate(0, 0, 30)
	v1ID, v2ID, v1End := seedSupersededFactAPI(t, db, wsID, t0, t1)

	url := srv.URL + "/api/v1/memory/search"
	base := map[string]any{"query": "", "limit": 50, "workspace_id": wsID}

	tests := []struct {
		name    string
		validAt string // "" = omit
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
			body := map[string]any{}
			for k, v := range base {
				body[k] = v
			}
			if tc.validAt != "" {
				body["valid_at"] = tc.validAt
			}
			got := searchIDs(t, url, body)
			assertSameIDsAPI(t, got, tc.wantIDs)
		})
	}
}

func TestMemorySearchValidAt_Querystring(t *testing.T) {
	srv, db, _ := newMemoryTestServer(t)
	wsID := "ws-asof-qs"
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.AddDate(0, 0, 30)
	v1ID, _, _ := seedSupersededFactAPI(t, db, wsID, t0, t1)

	// ?valid_at= on the querystring feeds the same filter path as the body.
	url := srv.URL + "/api/v1/memory/search?valid_at=" +
		t0.Add(time.Hour).Format(time.RFC3339)
	got := searchIDs(t, url, map[string]any{
		"query": "", "limit": 50, "workspace_id": wsID,
	})
	assertSameIDsAPI(t, got, []string{v1ID})
}

// TestMemorySearchValidAt_BodyWinsOverQuerystring pins the documented
// precedence: when valid_at is present in BOTH the JSON body and the
// ?valid_at= querystring, the BODY wins. We point the body at v1's window
// and the querystring at the post-supersession window; the result must be v1
// (body), proving the querystring is ignored when the body supplies it.
func TestMemorySearchValidAt_BodyWinsOverQuerystring(t *testing.T) {
	srv, db, _ := newMemoryTestServer(t)
	wsID := "ws-asof-precedence"
	t0 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.AddDate(0, 0, 30)
	v1ID, _, v1End := seedSupersededFactAPI(t, db, wsID, t0, t1)

	// Querystring asks for the post-supersession window (would return v2).
	qsValidAt := v1End.Add(time.Hour).Format(time.RFC3339)
	url := srv.URL + "/api/v1/memory/search?valid_at=" + qsValidAt

	// Body asks for v1's window — body must win → v1.
	got := searchIDs(t, url, map[string]any{
		"query":        "",
		"limit":        50,
		"workspace_id": wsID,
		"valid_at":     t0.Add(time.Hour).Format(time.RFC3339),
	})
	assertSameIDsAPI(t, got, []string{v1ID})
}

func TestMemorySearchValidAt_Invalid(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)
	// Bad timestamp in the body → 400 with a clear message.
	postJSON(t, srv.URL+"/api/v1/memory/search",
		map[string]any{"query": "", "valid_at": "not-a-timestamp"},
		http.StatusBadRequest)
	// Bad timestamp in the querystring → 400 too.
	postJSON(t, srv.URL+"/api/v1/memory/search?valid_at=nope",
		map[string]any{"query": ""},
		http.StatusBadRequest)
}

func assertSameIDsAPI(t *testing.T, got, want []string) {
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
