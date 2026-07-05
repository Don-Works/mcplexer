package index

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// countingStore wraps memStore to count symbol-search calls (perf regression
// guard for failure mapping).
type countingStore struct {
	*memStore
	symbolSearches int
}

func (c *countingStore) SearchCodeIndexSymbols(ctx context.Context, q store.CodeIndexSymbolQuery) ([]store.CodeIndexSymbolHit, error) {
	c.symbolSearches++
	return c.memStore.SearchCodeIndexSymbols(ctx, q)
}

func TestDepsRejectsUnknownDirection(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedKVWorkspace(t, ms, "ws")
	_, err := svc.Deps(context.Background(), DepsRequest{
		WorkspaceID: "ws", Root: t.TempDir(), File: "internal/kv/handler.go", Direction: "sideways",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown direction") {
		t.Fatalf("want unknown-direction error, got %v", err)
	}
}

func TestCapMediumsKeepsHighAndRanksByAffinity(t *testing.T) {
	owners := []TestOwner{{Path: "p/handler_test.go", Confidence: "high", Reason: "direct"}}
	for _, base := range []string{
		"aaa", "bbb", "ccc", "ddd", "eee", "fff", "ggg", "hhh", "iii", "jjj", "kkk", "code_index_query",
	} {
		owners = append(owners, TestOwner{Path: "p/" + base + "_test.go", Confidence: "medium", Reason: "same-package test"})
	}
	got := capMediums("p/code_index.go", owners)
	if len(got) != 1+maxMediumOwners {
		t.Fatalf("len = %d, want %d", len(got), 1+maxMediumOwners)
	}
	if got[0].Confidence != "high" {
		t.Fatalf("high owner must survive first, got %+v", got[0])
	}
	// The name-affine sibling must outrank the alphabet fillers.
	found := false
	for _, o := range got {
		if o.Path == "p/code_index_query_test.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("affine medium owner was capped away: %+v", got)
	}
}

func TestMapFailureSuffixResolvesToolRelativePaths(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedKVWorkspace(t, ms, "ws")
	// vitest reports web/-relative paths; the index stores web/src/....
	// seedKVWorkspace indexes internal/kv/handler.go — mention it as if the
	// tool ran from inside internal/.
	cands, err := svc.mapFailure(context.Background(), "ws", t.TempDir(),
		"FAIL kv/handler.go:12 assertion failed", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 || cands[0].Path != "internal/kv/handler.go" {
		t.Fatalf("suffix resolution missed: %+v", cands)
	}
}

func TestMapFailureDedupesRepeatedFailLines(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	cs := &countingStore{memStore: ms}
	svc.store = cs
	seedKVWorkspace(t, ms, "ws")
	blob := strings.Repeat("--- FAIL: TestHandleKVSet (0.01s)\n", 3000)
	if _, err := svc.mapFailure(context.Background(), "ws", t.TempDir(), blob, 5); err != nil {
		t.Fatal(err)
	}
	// 1 deduped FAIL-name lookup + <=10 identifier-harvest lookups.
	if cs.symbolSearches > 15 {
		t.Fatalf("symbol searches = %d for repeated FAIL lines; dedupe/cap regressed", cs.symbolSearches)
	}
}

func TestContextPackNeverEmitsDirectories(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedKVWorkspace(t, ms, "ws")
	// A Go-style edge whose target is a package DIRECTORY, not a file: graph
	// proximity must not surface it as a pack entry.
	seedContextFile(t, ms, "ws", "internal/kv/dirimport.go", "Imports a package dir.",
		[]store.CodeIndexSymbol{{Name: "HandleKVDir", Kind: "func", Exported: true, StartLine: 3}},
		[]store.CodeIndexEdge{{Kind: "import", ToPath: "internal/downstream"}})
	git := newGitRunner(t.TempDir(), svc.logger)
	pack, err := svc.contextPack(context.Background(),
		ContextRequest{WorkspaceID: "ws", Query: "kv set handler", BudgetTokens: 16000}, git, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	files, _ := ms.ListCodeIndexFileStats(context.Background(), "ws")
	indexed := map[string]bool{}
	for _, f := range files {
		indexed[f.Path] = true
	}
	for _, f := range pack.Files {
		if !indexed[f.Path] {
			t.Errorf("pack entry %q is not an indexed file (directory leak)", f.Path)
		}
	}
}
