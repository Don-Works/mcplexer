// memory_test.go — table-driven coverage of the memory store
// (migration 058). Exercises WriteMemory (fact + note), the bi-temporal
// invalidate-then-insert path, FTS5 search, vec0 KNN, soft delete, and
// forget-by-source. Mirrors the test conventions used by skill_registry.
package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func newMemDB(t *testing.T) *DB {
	t.Helper()
	d, err := New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestWriteMemoryNoteInsertsRow(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	e := &store.MemoryEntry{
		Name:    "note-1",
		Content: "hello world",
	}
	if err := d.WriteMemory(ctx, e); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if e.ID == "" {
		t.Fatal("expected ID to be generated")
	}
	got, err := d.GetMemory(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Name != "note-1" || got.Content != "hello world" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.Kind != store.MemoryKindNote {
		t.Fatalf("expected default kind=note, got %q", got.Kind)
	}
	if got.SourceKind != store.MemorySourceAgent {
		t.Fatalf("expected default source=agent, got %q", got.SourceKind)
	}
}

func TestUpdateMemory(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	e := &store.MemoryEntry{Name: "upd", Content: "before", Kind: store.MemoryKindNote}
	if err := d.WriteMemory(ctx, e); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}

	// In-place rewrite of the human-editable fields.
	e.Content = "after"
	e.Name = "upd-renamed"
	e.Pinned = true
	if err := d.UpdateMemory(ctx, e); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	got, err := d.GetMemory(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Content != "after" || got.Name != "upd-renamed" || !got.Pinned {
		t.Fatalf("update not reflected: %+v", got)
	}

	// Missing id → ErrNotFound (the upsert caller routes to WriteMemory).
	if err := d.UpdateMemory(ctx, &store.MemoryEntry{ID: "01MISSING", Content: "x"}); err != store.ErrNotFound {
		t.Fatalf("UpdateMemory on missing id = %v, want ErrNotFound", err)
	}
}

func TestWriteMemoryFactInvalidatesPrior(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-1"
	first := &store.MemoryEntry{
		Name:        "preferred-editor",
		Kind:        store.MemoryKindFact,
		Content:     "neovim",
		WorkspaceID: &wsID,
	}
	if err := d.WriteMemory(ctx, first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := &store.MemoryEntry{
		Name:        "preferred-editor",
		Kind:        store.MemoryKindFact,
		Content:     "emacs",
		WorkspaceID: &wsID,
	}
	if err := d.WriteMemory(ctx, second); err != nil {
		t.Fatalf("second write: %v", err)
	}
	// Default list excludes invalidated rows — only the new active one.
	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{wsID}},
		Kind:  store.MemoryKindFact,
		Name:  "preferred-editor",
	})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(got) != 1 || got[0].Content != "emacs" {
		t.Fatalf("expected single active row 'emacs', got %d: %+v", len(got), got)
	}
	// Include invalidated to confirm history is preserved.
	all, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:          store.SkillScope{WorkspaceIDs: []string{wsID}},
		Kind:           store.MemoryKindFact,
		Name:           "preferred-editor",
		IncludeInvalid: true,
	})
	if err != nil {
		t.Fatalf("ListMemories include-invalid: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 rows in history, got %d", len(all))
	}
	// The older row should now point at the newer one.
	var historical *store.MemoryEntry
	for i := range all {
		if all[i].TValidEnd != nil {
			historical = &all[i]
		}
	}
	if historical == nil {
		t.Fatal("expected one invalidated row")
	}
	if historical.InvalidatedBy != second.ID {
		t.Fatalf("expected invalidated_by=%s, got %s", second.ID, historical.InvalidatedBy)
	}
}

func TestSearchMemoriesFTS(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-search"
	writes := []store.MemoryEntry{
		{Name: "go-tip", Content: "use context.Context as first arg", WorkspaceID: &wsID},
		{Name: "react-tip", Content: "prefer functional components", WorkspaceID: &wsID},
		{Name: "sql-tip", Content: "always use parameterised queries", WorkspaceID: &wsID},
		{Name: "global-tip", Content: "context-aware functions everywhere"}, // global
	}
	for i := range writes {
		if err := d.WriteMemory(ctx, &writes[i]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	hits, err := d.SearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"context")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected at least 2 hits for 'context', got %d", len(hits))
	}
	names := map[string]bool{}
	for _, h := range hits {
		names[h.Entry.Name] = true
		if h.Source != "fts" {
			t.Fatalf("expected source=fts, got %q", h.Source)
		}
	}
	if !names["go-tip"] || !names["global-tip"] {
		t.Fatalf("expected go-tip + global-tip in hits, got %v", names)
	}
	if names["react-tip"] {
		t.Fatal("did not expect react-tip to match 'context'")
	}
}

// TestSearchMemoriesByTag — FTS5 search must match a tag (e.g.
// 'editor') even though tags are stored as JSON ['editor','cli'].
// The fix: tokenize the FTS5 'tags' column with a separator-rich
// tokenizer config so brackets/quotes/commas are token boundaries.
func TestSearchMemoriesByTag(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-tag-search"
	tags, _ := json.Marshal([]string{"editor", "cli"})
	if err := d.WriteMemory(ctx, &store.MemoryEntry{
		Name:        "vim-tip",
		Content:     "jk escapes insert mode",
		WorkspaceID: &wsID,
		TagsJSON:    json.RawMessage(tags),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	hits, err := d.SearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"editor")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected match by tag 'editor', got 0 hits")
	}
	// Same query expressed against the tags column explicitly. Validates
	// that the column tokenization (not just any-column MATCH) works,
	// which is what callers using `tags:foo` syntax rely on.
	hits, err = d.SearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"tags:editor")
	if err != nil {
		t.Fatalf("search tags:editor: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected tags:editor to match, got 0 hits")
	}
	// Hyphenated tag — unicode61 splits on the hyphen by default, and
	// raw user input containing '-' is interpreted by FTS5 as a column-
	// restrict NOT operator (`merge-freeze` → "col merge AND NOT freeze")
	// which yields "no such column" errors. SearchMemories now sanitizes
	// its input so any reasonable agent query keeps working.
	hyphenTags, _ := json.Marshal([]string{"merge-freeze"})
	if err := d.WriteMemory(ctx, &store.MemoryEntry{
		Name:        "release-policy",
		Content:     "no non-critical merges after Thursday",
		WorkspaceID: &wsID,
		TagsJSON:    json.RawMessage(hyphenTags),
	}); err != nil {
		t.Fatalf("write hyphen-tag row: %v", err)
	}
	for _, q := range []string{"merge", "freeze", "merge-freeze"} {
		got, err := d.SearchMemories(ctx,
			store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
			q)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		found := false
		for _, h := range got {
			if h.Entry.Name == "release-policy" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query %q did not match hyphenated tag 'merge-freeze' (got %d hits)", q, len(got))
		}
	}
	// Other punctuation an agent might pass: parens, colons, asterisks.
	// All of these are FTS5 metacharacters that, unsanitized, would crash
	// the query.
	for _, q := range []string{"editor:vim", "(editor)", "editor*"} {
		_, err := d.SearchMemories(ctx,
			store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
			q)
		if err != nil {
			t.Fatalf("punctuated query %q errored: %v", q, err)
		}
	}
}

func TestSearchMemoriesScopeIsolation(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA, wsB := "ws-a", "ws-b"
	if err := d.WriteMemory(ctx, &store.MemoryEntry{
		Name: "shhh", Content: "alpha bravo", WorkspaceID: &wsA,
	}); err != nil {
		t.Fatalf("write ws-a: %v", err)
	}
	if err := d.WriteMemory(ctx, &store.MemoryEntry{
		Name: "shhh", Content: "alpha bravo", WorkspaceID: &wsB,
	}); err != nil {
		t.Fatalf("write ws-b: %v", err)
	}
	hits, err := d.SearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsA}}},
		"alpha")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	for _, h := range hits {
		if h.Entry.WorkspaceID == nil || *h.Entry.WorkspaceID != wsA {
			t.Fatalf("scope leak: hit from %v", h.Entry.WorkspaceID)
		}
	}
}

func TestVectorSearchMemoriesRoundtrip(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-vec"
	writes := []store.MemoryEntry{
		{Name: "v1", Content: "first vector memory", WorkspaceID: &wsID},
		{Name: "v2", Content: "second vector memory", WorkspaceID: &wsID},
	}
	for i := range writes {
		if err := d.WriteMemory(ctx, &writes[i]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// Synthetic 4-dim vectors are fine here — the table dim is 1536 so
	// we need to test with the right dimensions. Use small repeated
	// patterns to keep the test compact.
	dim := 1536
	v1 := makeVec(dim, 0.1)
	v2 := makeVec(dim, 0.9)
	if err := d.UpsertMemoryEmbedding(ctx, writes[0].ID, "test-model", 1, v1); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, writes[1].ID, "test-model", 1, v2); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}
	query := makeVec(dim, 0.09)
	hits, err := d.VectorSearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"test-model", query, 5)
	if err != nil {
		t.Fatalf("VectorSearchMemories: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one vector hit")
	}
	if hits[0].Entry.ID != writes[0].ID {
		t.Fatalf("expected nearest=v1 (closer to 0.09), got %s", hits[0].Entry.Name)
	}
	if hits[0].Source != "vec" {
		t.Fatalf("expected source=vec, got %q", hits[0].Source)
	}
}

func TestListMemoriesValidAtPointInTime(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-asof"

	// v1 valid from a fixed instant T0. We use an explicit TValidStart so
	// the as-of window is deterministic regardless of wall-clock.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v1 := &store.MemoryEntry{
		Name: "policy", Kind: store.MemoryKindFact, Content: "v1 belief",
		WorkspaceID: &wsID, TValidStart: t0,
	}
	if err := d.WriteMemory(ctx, v1); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	// Capture v1's id (WriteMemory generated it).
	v1ID := v1.ID

	// v2 supersedes v1 at a later instant T1; WriteMemory's fact path stamps
	// v1.t_valid_end = now and inserts v2 active. Give v2 an explicit
	// TValidStart at T1 so its window is clean.
	t1 := t0.AddDate(0, 0, 30)
	v2 := &store.MemoryEntry{
		Name: "policy", Kind: store.MemoryKindFact, Content: "v2 belief",
		WorkspaceID: &wsID, TValidStart: t1,
	}
	if err := d.WriteMemory(ctx, v2); err != nil {
		t.Fatalf("write v2: %v", err)
	}

	// Read back v1 to learn the actual t_valid_end the supersession stamped.
	allHist, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}, IncludeInvalid: true,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	var v1End *time.Time
	for i := range allHist {
		if allHist[i].ID == v1ID {
			v1End = allHist[i].TValidEnd
		}
	}
	if v1End == nil {
		t.Fatal("v1 was not invalidated by v2 supersession")
	}

	// As-of a point inside v1's window (after t0, before t_valid_end) → v1.
	asOfV1 := t0.Add(time.Hour)
	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:   store.SkillScope{WorkspaceIDs: []string{wsID}},
		Name:    "policy",
		ValidAt: &asOfV1,
	})
	if err != nil {
		t.Fatalf("list as-of v1: %v", err)
	}
	if len(got) != 1 || got[0].ID != v1ID {
		t.Fatalf("as-of v1 window: want only v1 (%s), got %+v", v1ID, got)
	}

	// As-of a point after the supersession → only the active v2.
	asOfV2 := v1End.Add(time.Hour)
	got2, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:   store.SkillScope{WorkspaceIDs: []string{wsID}},
		Name:    "policy",
		ValidAt: &asOfV2,
	})
	if err != nil {
		t.Fatalf("list as-of v2: %v", err)
	}
	if len(got2) != 1 || got2[0].ID != v2.ID {
		t.Fatalf("as-of v2 window: want only v2 (%s), got %+v", v2.ID, got2)
	}

	// As-of BEFORE t0 → nothing was valid yet.
	before := t0.Add(-time.Hour)
	got3, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:   store.SkillScope{WorkspaceIDs: []string{wsID}},
		Name:    "policy",
		ValidAt: &before,
	})
	if err != nil {
		t.Fatalf("list as-of before: %v", err)
	}
	if len(got3) != 0 {
		t.Fatalf("as-of before t0: want empty, got %+v", got3)
	}
}

func TestGetMemoryEmbeddingRoundtrip(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-embed"
	e := store.MemoryEntry{Name: "ge", Content: "embedding roundtrip", WorkspaceID: &wsID}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Use a varied (non-constant) vector so a blob-decode bug that returns
	// the wrong dimension or byte order is caught — a uniform vector would
	// pass even with several decode mistakes.
	dim := 1536
	want := make([]float32, dim)
	for i := range want {
		want[i] = float32(i%17) * 0.013
	}
	if err := d.UpsertMemoryEmbedding(ctx, e.ID, "test-model", 3, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	model, got, err := d.GetMemoryEmbedding(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetMemoryEmbedding: %v", err)
	}
	if model != "test-model" {
		t.Fatalf("model = %q, want test-model", model)
	}
	if len(got) != dim {
		t.Fatalf("vector dim = %d, want %d", len(got), dim)
	}
	for i := range want {
		// vectorToJSON formats with 7 significant digits; allow a tiny
		// float32 round-trip tolerance.
		diff := got[i] - want[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > 1e-4 {
			t.Fatalf("vector[%d] = %v, want ~%v (diff %v)", i, got[i], want[i], diff)
		}
	}
}

func TestGetMemoryEmbeddingNotFound(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	// Memory with no vector row.
	e := store.MemoryEntry{Name: "novec", Content: "no vector here"}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := d.GetMemoryEmbedding(ctx, e.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no-vector memory: want ErrNotFound, got %v", err)
	}
	// Unknown id.
	if _, _, err := d.GetMemoryEmbedding(ctx, "01KNOSUCHID"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id: want ErrNotFound, got %v", err)
	}
}

func TestUpdateMemoryContentChangeClearsStaleVector(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-stale"
	e := store.MemoryEntry{
		Name: "drift", Content: "original meaning", Kind: store.MemoryKindNote,
		WorkspaceID: &wsID,
	}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, e.ID, "test-model", 1, makeVec(1536, 0.5)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Sanity: vector + pointer present before the edit.
	if _, _, err := d.GetMemoryEmbedding(ctx, e.ID); err != nil {
		t.Fatalf("pre-edit embedding: %v", err)
	}

	// Edit the CONTENT — the stale vector must be retired.
	e.Content = "completely different meaning"
	if err := d.UpdateMemory(ctx, &e); err != nil {
		t.Fatalf("UpdateMemory (content change): %v", err)
	}
	if _, _, err := d.GetMemoryEmbedding(ctx, e.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale vector not cleared: want ErrNotFound, got %v", err)
	}
	// The in-memory entry must reflect the cleared pointer.
	if e.EmbedModel != "" || e.EmbedVersion != 0 {
		t.Fatalf("embed pointer not cleared on entry: model=%q version=%d", e.EmbedModel, e.EmbedVersion)
	}
	// The persisted row's embed_model must be NULL so KNN excludes it.
	got, err := d.GetMemory(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.EmbedModel != "" || got.EmbedVersion != 0 {
		t.Fatalf("persisted embed pointer not cleared: model=%q version=%d", got.EmbedModel, got.EmbedVersion)
	}
	// KNN must no longer surface this row (no vector + cleared model).
	hits, err := d.VectorSearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"test-model", makeVec(1536, 0.5), 5)
	if err != nil {
		t.Fatalf("VectorSearch: %v", err)
	}
	for _, h := range hits {
		if h.Entry.ID == e.ID {
			t.Fatalf("stale-vector row still surfaced by KNN: %+v", h)
		}
	}
}

func TestUpdateMemoryMetadataOnlyKeepsVector(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-keep"
	e := store.MemoryEntry{
		Name: "keep", Content: "stable content", Kind: store.MemoryKindNote,
		WorkspaceID: &wsID,
	}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, e.ID, "test-model", 1, makeVec(1536, 0.5)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Edit only the name + pin flag; content unchanged → vector preserved.
	e.Name = "keep-renamed"
	e.Pinned = true
	if err := d.UpdateMemory(ctx, &e); err != nil {
		t.Fatalf("UpdateMemory (metadata only): %v", err)
	}
	model, vec, err := d.GetMemoryEmbedding(ctx, e.ID)
	if err != nil {
		t.Fatalf("embedding should survive a content-stable edit: %v", err)
	}
	if model != "test-model" || len(vec) != 1536 {
		t.Fatalf("vector mangled by metadata-only edit: model=%q dim=%d", model, len(vec))
	}
}

func TestVectorSearchEmbedModelMismatchExcludes(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-mismatch"
	e := store.MemoryEntry{Name: "x", Content: "x", WorkspaceID: &wsID}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, e.ID, "model-A", 1, makeVec(1536, 0.5)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	hits, err := d.VectorSearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"model-B", makeVec(1536, 0.5), 5)
	if err != nil {
		t.Fatalf("VectorSearch: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected zero hits on model mismatch, got %d", len(hits))
	}
}

func TestForgetMemoryBySourceDeletesVectorToo(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-poison"
	poisonSession := "sess-poison"
	good := store.MemoryEntry{
		Name: "good", Content: "good", WorkspaceID: &wsID,
		SourceSessionID: "sess-good",
	}
	bad := store.MemoryEntry{
		Name: "bad", Content: "bad", WorkspaceID: &wsID,
		SourceSessionID: poisonSession,
	}
	if err := d.WriteMemory(ctx, &good); err != nil {
		t.Fatalf("write good: %v", err)
	}
	if err := d.WriteMemory(ctx, &bad); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, bad.ID, "model-X", 1, makeVec(1536, 0.3)); err != nil {
		t.Fatalf("upsert bad vec: %v", err)
	}
	n, err := d.ForgetMemoryBySource(ctx, poisonSession, store.SkillScope{IncludeAll: true})
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 forgotten, got %d", n)
	}
	if _, err := d.GetMemory(ctx, bad.ID); err == nil {
		t.Fatal("expected bad memory to be deleted")
	}
	// Vector row should also be gone — KNN returns nothing.
	hits, err := d.VectorSearchMemories(ctx,
		store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{wsID}}},
		"model-X", makeVec(1536, 0.3), 5)
	if err != nil {
		t.Fatalf("vec search post-forget: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected vec rows gone after forget, got %d", len(hits))
	}
}

func TestForgetMemoryBySourceHonorsScope(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	source := "sess-shared"
	wsA := "ws-a"
	wsB := "ws-b"
	global := store.MemoryEntry{Name: "global", Content: "global", SourceSessionID: source}
	a := store.MemoryEntry{Name: "a", Content: "a", WorkspaceID: &wsA, SourceSessionID: source}
	b := store.MemoryEntry{Name: "b", Content: "b", WorkspaceID: &wsB, SourceSessionID: source}
	for _, entry := range []*store.MemoryEntry{&global, &a, &b} {
		if err := d.WriteMemory(ctx, entry); err != nil {
			t.Fatalf("write %s: %v", entry.Name, err)
		}
	}

	n, err := d.ForgetMemoryBySource(ctx, source, store.SkillScope{WorkspaceIDs: []string{wsA}})
	if err != nil {
		t.Fatalf("forget scoped: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected global + ws-a purge count 2, got %d", n)
	}
	for _, id := range []string{global.ID, a.ID} {
		if _, err := d.GetMemory(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected %s to be purged, err=%v", id, err)
		}
	}
	if _, err := d.GetMemory(ctx, b.ID); err != nil {
		t.Fatalf("expected ws-b memory to survive scoped purge: %v", err)
	}
}

func TestSetMemoryPinnedRoundtrip(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	e := store.MemoryEntry{Name: "important", Content: "important"}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if e.Pinned {
		t.Fatal("expected default unpinned")
	}
	if err := d.SetMemoryPinned(ctx, e.ID, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	got, err := d.GetMemory(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Pinned {
		t.Fatal("expected pinned=true after SetMemoryPinned(true)")
	}
	if err := d.SetMemoryPinned(ctx, e.ID, false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	got, err = d.GetMemory(ctx, e.ID)
	if err != nil {
		t.Fatalf("get after unpin: %v", err)
	}
	if got.Pinned {
		t.Fatal("expected pinned=false after SetMemoryPinned(false)")
	}
	// Missing id → ErrNotFound.
	if err := d.SetMemoryPinned(ctx, "no-such-id", true); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestSoftDeleteHidesMemory(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	e := store.MemoryEntry{Name: "doomed", Content: "doomed"}
	if err := d.WriteMemory(ctx, &e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := d.SoftDeleteMemory(ctx, e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := d.GetMemory(ctx, e.ID); err == nil {
		t.Fatal("expected ErrNotFound after soft delete")
	}
}

func TestCountMemoriesByScope(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-count"
	writes := []store.MemoryEntry{
		{Name: "n1", Content: "n1", Kind: store.MemoryKindNote, WorkspaceID: &wsID},
		{Name: "n2", Content: "n2", Kind: store.MemoryKindNote, WorkspaceID: &wsID},
		{Name: "f1", Content: "v1", Kind: store.MemoryKindFact, WorkspaceID: &wsID},
	}
	for i := range writes {
		if err := d.WriteMemory(ctx, &writes[i]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	facts, notes, err := d.CountMemories(ctx,
		store.SkillScope{WorkspaceIDs: []string{wsID}})
	if err != nil {
		t.Fatalf("CountMemories: %v", err)
	}
	if facts != 1 || notes != 2 {
		t.Fatalf("expected (1,2), got (%d,%d)", facts, notes)
	}
}

func TestMemoryTagFilter(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := "ws-tags"
	withTags := func(tags ...string) json.RawMessage {
		b, _ := json.Marshal(tags)
		return b
	}
	writes := []store.MemoryEntry{
		{Name: "a", Content: "a", WorkspaceID: &wsID, TagsJSON: withTags("go", "test")},
		{Name: "b", Content: "b", WorkspaceID: &wsID, TagsJSON: withTags("go")},
		{Name: "c", Content: "c", WorkspaceID: &wsID, TagsJSON: withTags("react")},
	}
	for i := range writes {
		if err := d.WriteMemory(ctx, &writes[i]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{wsID}},
		Tags:  []string{"go", "test"},
	})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("expected single 'a', got %d: %+v", len(got), got)
	}
}

func TestMemoryOfferLifecycle(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	offer := store.MemoryOffer{
		PeerID: "peer-1", PeerName: "alpha",
		RemoteID: "remote-mem-1",
		Name:     "shared-note",
		Kind:     store.MemoryKindNote,
		Preview:  "this is offered to you",
	}
	if err := d.UpsertMemoryOffer(ctx, &offer); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Duplicate is a no-op.
	if err := d.UpsertMemoryOffer(ctx, &store.MemoryOffer{
		PeerID: "peer-1", RemoteID: "remote-mem-1", Name: "x",
	}); err != nil {
		t.Fatalf("duplicate upsert: %v", err)
	}
	offers, err := d.ListMemoryOffers(ctx, store.MemoryOfferFilter{PendingOnly: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("expected 1 pending offer, got %d", len(offers))
	}
	if err := d.AcceptMemoryOffer(ctx, offer.ID, "local-mem-1"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	pending, err := d.ListMemoryOffers(ctx, store.MemoryOfferFilter{PendingOnly: true})
	if err != nil {
		t.Fatalf("list post-accept: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected zero pending after accept, got %d", len(pending))
	}
}

// makeVec returns a deterministic 1536-dim vector with all entries set
// to val. Sufficient to test ordering — closer base values rank higher.
func makeVec(dim int, val float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = val
	}
	return v
}

// guard against accidental import drift — assert at compile time that
// the time import is reachable (we use it transitively via store types).
var _ = time.Now
