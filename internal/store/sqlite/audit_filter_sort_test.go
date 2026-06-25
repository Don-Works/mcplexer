package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestQueryAuditNewFilters drives every new exact-match dimension.
func TestQueryAuditNewFilters(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)

	mk := func(i int, mut func(*store.AuditRecord)) {
		r := &store.AuditRecord{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			ToolName:  "t", Status: "success", LatencyMs: 100,
		}
		mut(r)
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	mk(0, func(r *store.AuditRecord) { r.ActorKind = "worker"; r.ActorID = "w1"; r.ClientType = "claude_cli" })
	mk(1, func(r *store.AuditRecord) { r.ActorKind = "user"; r.DownstreamServerID = "ds1" })
	mk(2, func(r *store.AuditRecord) { r.RouteRuleID = "rr1"; r.ErrorCode = "E_OOPS"; r.Tier = "cross_org" })
	mk(3, func(r *store.AuditRecord) { r.CacheHit = true; r.LatencyMs = 5 })
	mk(4, func(r *store.AuditRecord) { r.LatencyMs = 900 })

	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		f    store.AuditFilter
		want int
	}{
		{"actor_kind", store.AuditFilter{ActorKind: str("worker")}, 1},
		{"actor_id", store.AuditFilter{ActorID: str("w1")}, 1},
		{"downstream_server_id", store.AuditFilter{DownstreamServerID: str("ds1")}, 1},
		{"route_rule_id", store.AuditFilter{RouteRuleID: str("rr1")}, 1},
		{"client_type", store.AuditFilter{ClientType: str("claude_cli")}, 1},
		{"error_code", store.AuditFilter{ErrorCode: str("E_OOPS")}, 1},
		{"tier", store.AuditFilter{Tier: str("cross_org")}, 1},
		{"min_latency_ms", store.AuditFilter{MinLatencyMs: intp(800)}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.f.Limit = 50
			_, total, err := db.QueryAuditRecords(ctx, c.f)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if total != c.want {
				t.Fatalf("%s total = %d, want %d", c.name, total, c.want)
			}
		})
	}

	// cache_hit true / false split (5 rows total, 1 cache hit).
	cacheTrue := true
	_, total, _ := db.QueryAuditRecords(ctx, store.AuditFilter{CacheHit: &cacheTrue, Limit: 50})
	if total != 1 {
		t.Fatalf("cache_hit=true total = %d, want 1", total)
	}
	cacheFalse := false
	_, total, _ = db.QueryAuditRecords(ctx, store.AuditFilter{CacheHit: &cacheFalse, Limit: 50})
	if total != 4 {
		t.Fatalf("cache_hit=false total = %d, want 4", total)
	}
}

func intp(n int) *int { return &n }

// TestQueryAuditSort drives the sort allowlist.
func TestQueryAuditSort(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	lats := []int{50, 10, 90, 30}
	for i, l := range lats {
		r := &store.AuditRecord{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			ToolName:  "t", Status: "success", LatencyMs: l,
		}
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	check := func(sort string, wantFirst int) {
		recs, _, err := db.QueryAuditRecords(ctx, store.AuditFilter{Sort: sort, Limit: 50})
		if err != nil {
			t.Fatalf("query %s: %v", sort, err)
		}
		if recs[0].LatencyMs != wantFirst {
			t.Fatalf("sort %s first latency = %d, want %d", sort, recs[0].LatencyMs, wantFirst)
		}
	}
	check("time_desc", 30) // last inserted (i=3, lat 30)
	check("time_asc", 50)  // first inserted (i=0, lat 50)
	check("latency_desc", 90)
	check("latency_asc", 10)
	// Unknown sort falls back to time_desc.
	check("bogus", 30)
}

// TestQueryAuditKeysetPagination walks the full set via next_cursor-style
// keyset paging for time_desc and confirms no dupes / no gaps.
func TestQueryAuditKeysetPagination(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	const n = 7
	for i := 0; i < n; i++ {
		r := &store.AuditRecord{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			ToolName:  "t", Status: "success", LatencyMs: 1,
		}
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	var cursorTs *time.Time
	var cursorID string
	pages := 0
	for {
		f := store.AuditFilter{Sort: "time_desc", Limit: 3}
		if cursorTs != nil {
			f.CursorTs = cursorTs
			f.CursorID = cursorID
		}
		recs, _, err := db.QueryAuditRecords(ctx, f)
		if err != nil {
			t.Fatalf("page query: %v", err)
		}
		if len(recs) == 0 {
			break
		}
		pages++
		if pages > 10 {
			t.Fatal("keyset pagination did not terminate")
		}
		for _, r := range recs {
			if seen[r.ID] {
				t.Fatalf("duplicate row across pages: %s", r.ID)
			}
			seen[r.ID] = true
		}
		// Verify monotonic descending timestamps within a page.
		for i := 1; i < len(recs); i++ {
			if recs[i].Timestamp.After(recs[i-1].Timestamp) {
				t.Fatalf("time_desc page not descending")
			}
		}
		if len(recs) < 3 {
			break
		}
		last := recs[len(recs)-1]
		ts := last.Timestamp
		cursorTs = &ts
		cursorID = last.ID
	}
	if len(seen) != n {
		t.Fatalf("keyset walk saw %d unique rows, want %d", len(seen), n)
	}
}
