// memory_tags_filter_test.go — regression coverage for the
// tag-filter-after-LIMIT bug: tag matching used to run in Go AFTER the
// SQL LIMIT, so pages came back short (or empty) whenever the newest
// rows didn't carry the tag. Tags now filter in SQL via EXISTS over
// json_each(tags_json); these tests pin that behaviour for ListMemories,
// SearchMemories, and VectorSearchMemories.
package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedTaggedMemories writes 2 newest untagged rows + 3 older rows tagged
// ["ops"] (one of which also carries "db"). Every content mentions
// "deploy" so FTS queries hit all five.
func seedTaggedMemories(t *testing.T, d *DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []struct {
		name string
		tags []string
		age  time.Duration
	}{
		{name: "untagged-new-1", age: 1 * time.Minute},
		{name: "untagged-new-2", age: 2 * time.Minute},
		{name: "ops-1", tags: []string{"ops"}, age: 3 * time.Minute},
		{name: "ops-2", tags: []string{"ops", "db"}, age: 4 * time.Minute},
		{name: "ops-3", tags: []string{"Ops"}, age: 5 * time.Minute},
	}
	for _, r := range rows {
		tagsJSON := json.RawMessage("[]")
		if len(r.tags) > 0 {
			b, err := json.Marshal(r.tags)
			if err != nil {
				t.Fatalf("marshal tags: %v", err)
			}
			tagsJSON = b
		}
		ts := now.Add(-r.age)
		e := &store.MemoryEntry{
			Name:     r.name,
			Content:  fmt.Sprintf("deploy notes for %s", r.name),
			TagsJSON: tagsJSON,
			// Explicit timestamps so ORDER BY updated_at DESC puts the
			// untagged rows first — the exact shape that broke paging.
			CreatedAt: ts, UpdatedAt: ts, TValidStart: ts,
		}
		if err := d.WriteMemory(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", r.name, err)
		}
	}
}

func namesOf(entries []store.MemoryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestListMemoriesTagFilterInSQL(t *testing.T) {
	d := newMemDB(t)
	seedTaggedMemories(t, d)
	ctx := context.Background()

	cases := []struct {
		name      string
		tags      []string
		limit     int
		wantNames map[string]bool
		wantLen   int
	}{
		{
			// The regression: limit 2 with newest rows untagged must
			// still return 2 tagged rows (pre-fix: 0).
			name: "limit_smaller_than_untagged_head",
			tags: []string{"ops"}, limit: 2,
			wantNames: map[string]bool{"ops-1": true, "ops-2": true},
			wantLen:   2,
		},
		{
			name: "all_tagged_no_limit",
			tags: []string{"ops"},
			wantNames: map[string]bool{
				"ops-1": true, "ops-2": true, "ops-3": true,
			},
			wantLen: 3,
		},
		{
			name:      "and_semantics_across_tags",
			tags:      []string{"ops", "db"},
			wantNames: map[string]bool{"ops-2": true},
			wantLen:   1,
		},
		{
			name: "case_insensitive",
			tags: []string{"OPS"},
			wantNames: map[string]bool{
				"ops-1": true, "ops-2": true, "ops-3": true,
			},
			wantLen: 3,
		},
		{
			name:    "no_tags_returns_everything",
			wantLen: 5,
		},
		{
			name: "unknown_tag_matches_nothing",
			tags: []string{"nope"}, wantLen: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.ListMemories(ctx, store.MemoryFilter{
				Scope: store.SkillScope{IncludeAll: true},
				Tags:  tc.tags, Limit: tc.limit,
			})
			if err != nil {
				t.Fatalf("ListMemories: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("want %d rows, got %d: %v", tc.wantLen, len(got), namesOf(got))
			}
			for _, e := range got {
				if tc.wantNames != nil && !tc.wantNames[e.Name] {
					t.Fatalf("unexpected row %q in %v", e.Name, namesOf(got))
				}
			}
		})
	}
}

func TestSearchMemoriesTagFilterInSQL(t *testing.T) {
	d := newMemDB(t)
	seedTaggedMemories(t, d)
	ctx := context.Background()

	// Every row matches "deploy"; the limit is smaller than the number
	// of (BM25-equivalent) untagged rows, so a post-LIMIT tag filter
	// could come back short. SQL-side filtering must return full pages.
	hits, err := d.SearchMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{IncludeAll: true},
		Tags:  []string{"ops"}, Limit: 3,
	}, "deploy")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 tagged hits, got %d", len(hits))
	}
	for _, h := range hits {
		switch h.Entry.Name {
		case "ops-1", "ops-2", "ops-3":
		default:
			t.Fatalf("untagged row %q leaked into tagged search", h.Entry.Name)
		}
	}
}

func TestVectorSearchMemoriesTagFilterInSQL(t *testing.T) {
	d := newMemDB(t)
	seedTaggedMemories(t, d)
	ctx := context.Background()

	// Embed three rows: the two untagged ones closest to the query
	// vector, one tagged further away. Tag filter must exclude the
	// nearer untagged neighbours in SQL.
	unit := func(i int) []float32 {
		v := make([]float32, 1536)
		v[i] = 1
		return v
	}
	byName := func(name string) string {
		t.Helper()
		rows, err := d.ListMemories(ctx, store.MemoryFilter{
			Scope: store.SkillScope{IncludeAll: true}, Name: name,
		})
		if err != nil || len(rows) != 1 {
			t.Fatalf("lookup %s: err=%v n=%d", name, err, len(rows))
		}
		return rows[0].ID
	}
	if err := d.UpsertMemoryEmbedding(ctx, byName("untagged-new-1"), "m", 1, unit(0)); err != nil {
		t.Fatalf("embed u1: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, byName("untagged-new-2"), "m", 1, unit(1)); err != nil {
		t.Fatalf("embed u2: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, byName("ops-1"), "m", 1, unit(2)); err != nil {
		t.Fatalf("embed ops-1: %v", err)
	}

	hits, err := d.VectorSearchMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{IncludeAll: true},
		Tags:  []string{"ops"},
	}, "m", unit(0), 3)
	if err != nil {
		t.Fatalf("VectorSearchMemories: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Name != "ops-1" {
		t.Fatalf("want only ops-1, got %d hits: %+v", len(hits), hits)
	}
}
