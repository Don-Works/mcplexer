package skillregistry_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestRegistry(t *testing.T) (*skillregistry.Registry, *sqlite.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.New(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return skillregistry.New(db), db
}

func sampleBody(name, desc string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n# %s\n\nBody content for %s.\n", name, desc, name, name)
}

func TestPublishCreatesAndDedupes(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	body := sampleBody("alpha", "Use when alpha-related work is needed.")

	res, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha", Body: body})
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if res.Version != 1 || res.Action != "created" {
		t.Fatalf("publish 1 unexpected: %+v", res)
	}

	res2, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha", Body: body})
	if err != nil {
		t.Fatalf("publish 2 (dedup): %v", err)
	}
	if res2.Version != 1 || res2.Action != "deduped" {
		t.Fatalf("expected dedup at v1, got %+v", res2)
	}

	body2 := sampleBody("alpha", "Use when alpha-related work is needed (revised).")
	res3, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha", Body: body2})
	if err != nil {
		t.Fatalf("publish 3: %v", err)
	}
	if res3.Version != 2 || res3.Action != "created" {
		t.Fatalf("expected v2 created, got %+v", res3)
	}
}

func TestPublishRejectsBadFrontmatter(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing fence", "no frontmatter\n# heading", ""},
		{"empty desc", "---\nname: x\ndescription: \n---\nbody", ""},
		{"description sequence", "---\nname: x\ndescription:\n  - one\n  - two\n---\nbody", "description: invalid type sequence, expected string"},
		{"name sequence", "---\nname:\n  - x\ndescription: ok\n---\nbody", "name: invalid type sequence, expected string"},
		{"name mismatch", "---\nname: y\ndescription: ok\n---\nbody", ""},
		{"reserved name", "---\nname: anthropic\ndescription: ok\n---\nbody", ""},
		{"bad chars", "---\nname: Foo\ndescription: ok\n---\nbody", ""},
	}
	for _, tc := range cases {
		_, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "x", Body: tc.body})
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error %q, want to contain %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestSearchRanksByDescription(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	skills := map[string]string{
		"pdf-extract":   "Use when the user wants to extract text or tables from a PDF document.",
		"image-resize":  "Use when the user wants to resize, crop, or compress an image file.",
		"json-validate": "Use when the user wants to validate JSON against a schema.",
	}
	for name, desc := range skills {
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: name, Body: sampleBody(name, desc)}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
	}

	hits, err := reg.Search(ctx, skillregistry.GlobalScope(), "I need to pull text out of a PDF", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if hits[0].Name != "pdf-extract" {
		t.Errorf("expected pdf-extract first, got %+v", hits)
	}
}

type fakeSkillEmbedder struct {
	calls int
}

func (f *fakeSkillEmbedder) HasModel() bool { return true }

func (f *fakeSkillEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, string, error) {
	f.calls++
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		s := strings.ToLower(in)
		switch {
		case strings.Contains(s, "postgres") || strings.Contains(s, "database"):
			out[i] = []float32{1, 0}
		case strings.Contains(s, "photo") || strings.Contains(s, "image"):
			out[i] = []float32{0, 1}
		default:
			out[i] = []float32{0.1, 0.1}
		}
	}
	return out, "fake-skill-embedder", nil
}

func TestSearchUsesVectorPathWhenEmbedderConfigured(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	embedder := &fakeSkillEmbedder{}
	reg.SetEmbedder(embedder)

	skills := map[string]string{
		"db-backup":    "Use when the user needs database dump and restore runbooks.",
		"image-resize": "Use when the user needs photo resize and crop operations.",
	}
	for name, desc := range skills {
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: name, Body: sampleBody(name, desc)}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
	}

	hits, err := reg.Search(ctx, skillregistry.GlobalScope(), "postgres WAL archive", 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 || hits[0].Name != "db-backup" {
		t.Fatalf("vector search should surface db-backup first, got %+v", hits)
	}
	if embedder.calls < 2 {
		t.Fatalf("embedder calls = %d, want index + query calls", embedder.calls)
	}
}

func TestSearchVectorIndexRefreshesAfterPublish(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	embedder := &fakeSkillEmbedder{}
	reg.SetEmbedder(embedder)

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "image-resize", Body: sampleBody("image-resize", "Use when editing photo assets."),
	}); err != nil {
		t.Fatalf("publish image: %v", err)
	}
	if _, err := reg.Search(ctx, skillregistry.GlobalScope(), "photo crop", 5); err != nil {
		t.Fatalf("initial search: %v", err)
	}
	callsAfterFirstSearch := embedder.calls

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "db-backup", Body: sampleBody("db-backup", "Use when handling database backups."),
	}); err != nil {
		t.Fatalf("publish db: %v", err)
	}
	hits, err := reg.Search(ctx, skillregistry.GlobalScope(), "postgres WAL archive", 5)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if embedder.calls <= callsAfterFirstSearch+1 {
		t.Fatalf("index was not rebuilt after publish; calls %d -> %d",
			callsAfterFirstSearch, embedder.calls)
	}
	if len(hits) == 0 || hits[0].Name != "db-backup" {
		t.Fatalf("refreshed vector index should find new skill, got %+v", hits)
	}
}

func TestGetLatestStableExplicit(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		body := sampleBody("foo", fmt.Sprintf("Use when foo v%d is needed.", i))
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "foo", Body: body}); err != nil {
			t.Fatalf("publish v%d: %v", i, err)
		}
	}

	// latest = v3
	got, err := reg.Get(ctx, skillregistry.GlobalScope(), "foo", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if got.Version != 3 {
		t.Errorf("expected v3 latest, got v%d", got.Version)
	}

	// pin v2 as stable
	if err := reg.SetTag(ctx, skillregistry.GlobalScope(), "foo", "@stable", 2, "test"); err != nil {
		t.Fatalf("set tag: %v", err)
	}
	got, err = reg.Get(ctx, skillregistry.GlobalScope(), "foo", skillregistry.VersionRef{Stable: true})
	if err != nil {
		t.Fatalf("get stable: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("expected v2 stable, got v%d", got.Version)
	}

	// explicit v1
	got, err = reg.Get(ctx, skillregistry.GlobalScope(), "foo", skillregistry.VersionRef{Version: 1})
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("expected v1 explicit, got v%d", got.Version)
	}

	// missing
	_, err = reg.Get(ctx, skillregistry.GlobalScope(), "foo", skillregistry.VersionRef{Version: 99})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ptr is a small helper for *string literals.
func ptr(s string) *string { return &s }

// publish is a test helper that publishes name@desc into the given
// workspace scope (nil = global) and fails the test on error.
func publish(t *testing.T, reg *skillregistry.Registry, ws *string, name, desc string) {
	t.Helper()
	ctx := context.Background()
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name:        name,
		Body:        sampleBody(name, desc),
		WorkspaceID: ws,
	}); err != nil {
		t.Fatalf("publish %s (ws=%v): %v", name, ws, err)
	}
}

// TestWorkspaceShadowingAndScopeGrant is the load-bearing coverage that
// was previously absent: the same skill name published to global +
// workspace A + workspace B at differing versions, asserting the scope
// visibility + shadowing + IncludeAll-bypass rules across Get / ListHeads
// / Search. These cases would have caught the IncludeAll blind spots in
// Get()'s explicit-version and @stable paths.
func TestWorkspaceShadowingAndScopeGrant(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	const (
		wsA = "ws-alpha"
		wsB = "ws-beta"
	)
	// Global "shared" at v1 (then v2 so global head = v2).
	publish(t, reg, nil, "shared", "Global shared v1.")
	publish(t, reg, nil, "shared", "Global shared v2.")
	// Workspace A pins "shared" at its own v1 — shadows global for A.
	publish(t, reg, ptr(wsA), "shared", "Workspace A shared v1.")
	// Workspace B never publishes "shared" — sees global only.
	// A global-only skill nobody shadows.
	publish(t, reg, nil, "globalonly", "Only ever global.")

	scopeA := store.SkillScope{WorkspaceIDs: []string{wsA}}
	scopeB := store.SkillScope{WorkspaceIDs: []string{wsB}}

	t.Run("workspace head shadows global", func(t *testing.T) {
		cases := []struct {
			name      string
			scope     store.SkillScope
			wantWS    *string // expected WorkspaceID of head
			wantVer   int
			wantScope string
		}{
			{"A sees its own shadow", scopeA, ptr(wsA), 1, wsA},
			{"B sees global head", scopeB, nil, 2, "global"},
			{"global scope sees global head", skillregistry.GlobalScope(), nil, 2, "global"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e, err := reg.Get(ctx, tc.scope, "shared", skillregistry.VersionRef{Latest: true})
				if err != nil {
					t.Fatalf("get head: %v", err)
				}
				if e.Version != tc.wantVer {
					t.Errorf("version: got %d want %d", e.Version, tc.wantVer)
				}
				if (e.WorkspaceID == nil) != (tc.wantWS == nil) ||
					(e.WorkspaceID != nil && tc.wantWS != nil && *e.WorkspaceID != *tc.wantWS) {
					t.Errorf("workspace: got %v want %v", e.WorkspaceID, tc.wantWS)
				}
			})
		}
	})

	t.Run("ListHeads is per-scope", func(t *testing.T) {
		// Workspace A: shared shadowed to A-v1, globalonly visible.
		heads, err := reg.ListHeads(ctx, scopeA, 0)
		if err != nil {
			t.Fatalf("list A: %v", err)
		}
		got := map[string]*store.SkillRegistryEntry{}
		for i := range heads {
			got[heads[i].Name] = &heads[i]
		}
		if e := got["shared"]; e == nil || e.WorkspaceID == nil || *e.WorkspaceID != wsA {
			t.Errorf("A shared head not shadowed to wsA: %+v", e)
		}
		if got["globalonly"] == nil {
			t.Error("A should still see globalonly")
		}
		// Workspace B: shared is global v2, globalonly visible.
		headsB, err := reg.ListHeads(ctx, scopeB, 0)
		if err != nil {
			t.Fatalf("list B: %v", err)
		}
		gotB := map[string]*store.SkillRegistryEntry{}
		for i := range headsB {
			gotB[headsB[i].Name] = &headsB[i]
		}
		if e := gotB["shared"]; e == nil || e.WorkspaceID != nil || e.Version != 2 {
			t.Errorf("B shared head should be global v2: %+v", e)
		}
	})

	t.Run("Search is per-scope", func(t *testing.T) {
		hitsA, err := reg.Search(ctx, scopeA, "shared", 5)
		if err != nil {
			t.Fatalf("search A: %v", err)
		}
		foundA := false
		for _, h := range hitsA {
			if h.Name == "shared" {
				foundA = true
				if h.Scope != wsA {
					t.Errorf("A search shared scope: got %q want %q", h.Scope, wsA)
				}
			}
		}
		if !foundA {
			t.Error("A search did not return shared")
		}
		hitsB, err := reg.Search(ctx, scopeB, "shared", 5)
		if err != nil {
			t.Fatalf("search B: %v", err)
		}
		for _, h := range hitsB {
			if h.Name == "shared" && h.Scope != "global" {
				t.Errorf("B search shared scope: got %q want global", h.Scope)
			}
		}
	})

	t.Run("AdminScope explicit version honours IncludeAll", func(t *testing.T) {
		// The regression: under AdminScope the workspace A row at v1 must
		// be fetchable by explicit version, shadowing the global v1.
		e, err := reg.Get(ctx, skillregistry.AdminScope(), "shared", skillregistry.VersionRef{Version: 1})
		if err != nil {
			t.Fatalf("admin get v1: %v", err)
		}
		if e.WorkspaceID == nil || *e.WorkspaceID != wsA {
			t.Errorf("admin v1 should shadow to wsA row, got ws=%v", e.WorkspaceID)
		}
		// v2 only exists globally, so admin sees the global row.
		e2, err := reg.Get(ctx, skillregistry.AdminScope(), "shared", skillregistry.VersionRef{Version: 2})
		if err != nil {
			t.Fatalf("admin get v2: %v", err)
		}
		if e2.WorkspaceID != nil {
			t.Errorf("admin v2 should be global, got ws=%v", e2.WorkspaceID)
		}
	})

	t.Run("AdminScope @stable honours IncludeAll", func(t *testing.T) {
		// Pin @stable to v1 (the workspace-A-shadowed version).
		if err := reg.SetTag(ctx, skillregistry.AdminScope(), "shared", "@stable", 1, "test"); err != nil {
			t.Fatalf("set tag: %v", err)
		}
		e, err := reg.Get(ctx, skillregistry.AdminScope(), "shared", skillregistry.VersionRef{Stable: true})
		if err != nil {
			t.Fatalf("admin get @stable: %v", err)
		}
		if e.Version != 1 {
			t.Errorf("admin @stable version: got %d want 1", e.Version)
		}
		if e.WorkspaceID == nil || *e.WorkspaceID != wsA {
			t.Errorf("admin @stable should shadow to wsA row, got ws=%v", e.WorkspaceID)
		}
	})
}

// TestSoftDeleteAndTagReconcile covers the previously-untested destructive
// path: single-version delete keeps head, version=0 delete removes the
// name, and a @stable tag pointing at a deleted version is reconciled so
// Get(@stable) self-heals to head rather than returning ErrNotFound.
func TestSoftDeleteAndTagReconcile(t *testing.T) {
	t.Run("single version delete keeps head and 404s the deleted version", func(t *testing.T) {
		reg, _ := newTestRegistry(t)
		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			publish(t, reg, nil, "del", fmt.Sprintf("Use when del v%d.", i))
		}
		if err := reg.SoftDelete(ctx, nil, "del", 2); err != nil {
			t.Fatalf("soft delete v2: %v", err)
		}
		head, err := reg.Get(ctx, skillregistry.GlobalScope(), "del", skillregistry.VersionRef{Latest: true})
		if err != nil {
			t.Fatalf("head after delete: %v", err)
		}
		if head.Version != 3 {
			t.Errorf("head should remain v3, got v%d", head.Version)
		}
		if _, err := reg.Get(ctx, skillregistry.GlobalScope(), "del", skillregistry.VersionRef{Version: 2}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("deleted v2 should be ErrNotFound, got %v", err)
		}
	})

	t.Run("version=0 delete removes the name from heads", func(t *testing.T) {
		reg, _ := newTestRegistry(t)
		ctx := context.Background()
		publish(t, reg, nil, "wipe", "Use when wipe.")
		publish(t, reg, nil, "keep", "Use when keep.")
		if err := reg.SoftDelete(ctx, nil, "wipe", 0); err != nil {
			t.Fatalf("soft delete all: %v", err)
		}
		heads, err := reg.ListHeads(ctx, skillregistry.GlobalScope(), 0)
		if err != nil {
			t.Fatalf("list heads: %v", err)
		}
		for _, h := range heads {
			if h.Name == "wipe" {
				t.Errorf("wipe should be gone from heads, found %+v", h)
			}
		}
		if _, err := reg.Get(ctx, skillregistry.GlobalScope(), "wipe", skillregistry.VersionRef{Latest: true}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("wipe head should be ErrNotFound, got %v", err)
		}
	})

	t.Run("deleting the @stable-pinned version self-heals to head", func(t *testing.T) {
		reg, _ := newTestRegistry(t)
		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			publish(t, reg, nil, "pinned", fmt.Sprintf("Use when pinned v%d.", i))
		}
		if err := reg.SetTag(ctx, skillregistry.GlobalScope(), "pinned", "@stable", 2, "test"); err != nil {
			t.Fatalf("set tag: %v", err)
		}
		// Sanity: @stable resolves to v2 before deletion.
		if e, err := reg.Get(ctx, skillregistry.GlobalScope(), "pinned", skillregistry.VersionRef{Stable: true}); err != nil || e.Version != 2 {
			t.Fatalf("pre-delete @stable: e=%+v err=%v", e, err)
		}
		// Delete v2 — the pinned version.
		if err := reg.SoftDelete(ctx, nil, "pinned", 2); err != nil {
			t.Fatalf("soft delete v2: %v", err)
		}
		// @stable must NOT 404 forever; it falls back to head (v3).
		e, err := reg.Get(ctx, skillregistry.GlobalScope(), "pinned", skillregistry.VersionRef{Stable: true})
		if err != nil {
			t.Fatalf("post-delete @stable should self-heal, got err=%v", err)
		}
		if e.Version != 3 {
			t.Errorf("post-delete @stable should resolve to head v3, got v%d", e.Version)
		}
	})

	t.Run("deleting an unrelated version leaves @stable intact", func(t *testing.T) {
		reg, _ := newTestRegistry(t)
		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			publish(t, reg, nil, "stable3", fmt.Sprintf("Use when stable3 v%d.", i))
		}
		if err := reg.SetTag(ctx, skillregistry.GlobalScope(), "stable3", "@stable", 2, "test"); err != nil {
			t.Fatalf("set tag: %v", err)
		}
		// Delete v1 (not the pinned version).
		if err := reg.SoftDelete(ctx, nil, "stable3", 1); err != nil {
			t.Fatalf("soft delete v1: %v", err)
		}
		e, err := reg.Get(ctx, skillregistry.GlobalScope(), "stable3", skillregistry.VersionRef{Stable: true})
		if err != nil {
			t.Fatalf("@stable after unrelated delete: %v", err)
		}
		if e.Version != 2 {
			t.Errorf("@stable should still be v2, got v%d", e.Version)
		}
	})
}

// TestDeleteTag covers DeleteTag: a removed @stable tag makes Get(@stable)
// fall back to head, and re-fetching the tag errors.
func TestDeleteTag(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		publish(t, reg, nil, "tagged", fmt.Sprintf("Use when tagged v%d.", i))
	}
	if err := reg.SetTag(ctx, skillregistry.GlobalScope(), "tagged", "@stable", 1, "test"); err != nil {
		t.Fatalf("set tag: %v", err)
	}
	if err := reg.DeleteTag(ctx, "tagged", "@stable"); err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	// With no @stable tag, Get(@stable) surfaces the underlying tag
	// lookup error (ErrNotFound from GetSkillRegistryTag).
	if _, err := reg.Get(ctx, skillregistry.GlobalScope(), "tagged", skillregistry.VersionRef{Stable: true}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get(@stable) after DeleteTag should be ErrNotFound, got %v", err)
	}
	// Deleting a non-existent tag is a no-op error (checkRowsAffected).
	if err := reg.DeleteTag(ctx, "tagged", "@stable"); err == nil {
		t.Error("deleting already-removed tag should error (no rows affected)")
	}
}

func TestConcurrentPublishProducesContiguousVersions(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	const N = 50
	var wg sync.WaitGroup
	versions := make([]int, N)
	errs := make([]error, N)

	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := sampleBody("race", fmt.Sprintf("Use when race goroutine %d publishes.", i))
			res, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "race", Body: body})
			if err != nil {
				errs[i] = err
				return
			}
			versions[i] = res.Version
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish error: %v", err)
		}
	}
	seen := make(map[int]bool, N)
	for _, v := range versions {
		if v <= 0 || v > N {
			t.Fatalf("version out of range: %d", v)
		}
		if seen[v] {
			t.Fatalf("duplicate version: %d", v)
		}
		seen[v] = true
	}
}

func TestSeedPopulatesEmptyRegistry(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	if err := skillregistry.Seed(ctx, reg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	heads, err := reg.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if err != nil {
		t.Fatalf("list heads: %v", err)
	}
	if len(heads) < 2 {
		t.Errorf("expected ≥2 seeded heads, got %d", len(heads))
	}
	authorBuckets := map[string]int{}
	seededNames := map[string]bool{}
	for _, h := range heads {
		// Authors are derived from the seed dir layout: top-level seeds
		// get "system", grouped seeds (seeds/<group>/*.md) get "<group>".
		if h.Author != "system" && h.Author != "ai-coding" {
			t.Errorf("seed %s unexpected author=%q", h.Name, h.Author)
		}
		if h.Version != 1 {
			t.Errorf("seed %s version=%d (want 1)", h.Name, h.Version)
		}
		authorBuckets[h.Author]++
		seededNames[h.Name] = true
	}
	for _, name := range []string{"skill-creator", "review-skills", "using-mcplexer"} {
		if !seededNames[name] {
			t.Errorf("missing governance seed %q", name)
		}
	}
	if authorBuckets["system"] < 2 {
		t.Errorf("expected ≥2 system seeds, got %d", authorBuckets["system"])
	}
	if authorBuckets["ai-coding"] < 10 {
		t.Errorf("expected ≥10 ai-coding seeds, got %d", authorBuckets["ai-coding"])
	}

	// Idempotent: seeding again must be a no-op (no error, same count).
	before := len(heads)
	if err := skillregistry.Seed(ctx, reg); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	heads2, _ := reg.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if len(heads2) != before {
		t.Errorf("seed not idempotent: %d -> %d", before, len(heads2))
	}
}

func TestUsingMCPlexerSeedGuidesAdaptiveInvocation(t *testing.T) {
	body, err := skillregistry.SeedBody("using-mcplexer")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"`mcpx__call_tool`",
		"Rule of thumb",
		"map/filter/pick fields",
		"index__status",
		"memory__save",
		"More than one call",
		"`mcpx__call_tool` absent",
		"Do not turn a batch into repeated `mcpx__call_tool` round trips",
		"const mesh = mesh.receive",
		"task__get",
		"_call_tool_hint",
	} {
		if !strings.Contains(body, contract) {
			t.Errorf("using-mcplexer seed missing adaptive guidance %q", contract)
		}
	}
	if strings.Contains(body, `{"name":"task__get","arguments":{"id":"..."}`) {
		t.Error("using-mcplexer still uses task__get as the primary call_tool example")
	}
}
