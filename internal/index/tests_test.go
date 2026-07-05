package index

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func seedFile(t *testing.T, ms *memStore, ws, path string, isTest bool, edges ...store.CodeIndexEdge) {
	t.Helper()
	err := ms.UpsertCodeIndexedFiles(context.Background(), ws, []store.IndexedFile{{
		File:  store.CodeIndexFile{WorkspaceID: ws, Path: path, IsTest: isTest, PathTokens: tokenString(path)},
		Edges: edges,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func pathSet(t *testing.T, ms *memStore, ws string) map[string]bool {
	t.Helper()
	stats, _ := ms.ListCodeIndexFileStats(context.Background(), ws)
	set := map[string]bool{}
	for _, s := range stats {
		set[s.Path] = true
	}
	return set
}

func ownerFor(owners []TestOwner, path string) (TestOwner, bool) {
	for _, o := range owners {
		if o.Path == path {
			return o, true
		}
	}
	return TestOwner{}, false
}

func TestOwnerTestsGo(t *testing.T) {
	ms := newMemStore()
	for _, p := range []string{
		"internal/foo/bar.go", "internal/foo/bar_test.go",
		"internal/foo/other_test.go", "internal/foo/helper_test.go",
	} {
		seedFile(t, ms, "ws", p, isTestPath(p))
	}
	owners := ownerTests(context.Background(), ms, "ws", "internal/foo/bar.go", pathSet(t, ms, "ws"))

	if o, ok := ownerFor(owners, "internal/foo/bar_test.go"); !ok || o.Confidence != "high" {
		t.Errorf("bar_test.go should be high-confidence owner, got %+v ok=%v", o, ok)
	}
	if o, ok := ownerFor(owners, "internal/foo/other_test.go"); !ok || o.Confidence != "medium" {
		t.Errorf("other_test.go should be medium owner, got %+v ok=%v", o, ok)
	}
	if _, ok := ownerFor(owners, "internal/foo/helper_test.go"); ok {
		t.Error("helper_test.go should be excluded from ownership (P6)")
	}
}

func TestOwnerTestsTS(t *testing.T) {
	ms := newMemStore()
	widget := "web/src/Widget.tsx"
	seedFile(t, ms, "ws", widget, false)
	seedFile(t, ms, "ws", "web/src/Widget.test.tsx", true)
	seedFile(t, ms, "ws", "web/src/__tests__/Widget.spec.tsx", true)
	seedFile(t, ms, "ws", "web/src/unrelated.test.tsx", true)
	seedFile(t, ms, "ws", "web/src/consumer.test.tsx", true,
		store.CodeIndexEdge{Kind: "import", ToPath: widget})

	owners := ownerTests(context.Background(), ms, "ws", widget, pathSet(t, ms, "ws"))

	if o, ok := ownerFor(owners, "web/src/Widget.test.tsx"); !ok || o.Confidence != "high" {
		t.Errorf("Widget.test.tsx should be high, got %+v ok=%v", o, ok)
	}
	if o, ok := ownerFor(owners, "web/src/__tests__/Widget.spec.tsx"); !ok || o.Confidence != "medium" {
		t.Errorf("__tests__ spec should be medium, got %+v ok=%v", o, ok)
	}
	if o, ok := ownerFor(owners, "web/src/consumer.test.tsx"); !ok || o.Reason != "imports this file" {
		t.Errorf("consumer test should own via import edge, got %+v ok=%v", o, ok)
	}
	if _, ok := ownerFor(owners, "web/src/unrelated.test.tsx"); ok {
		t.Error("basename-mismatched test with no edge should not be an owner")
	}
}

func TestOwnerTestsUnknownLanguage(t *testing.T) {
	ms := newMemStore()
	seedFile(t, ms, "ws", "docs/readme.md", false)
	owners := ownerTests(context.Background(), ms, "ws", "docs/readme.md", pathSet(t, ms, "ws"))
	if len(owners) != 0 {
		t.Errorf("unknown language should yield no owners, got %v", owners)
	}
}
