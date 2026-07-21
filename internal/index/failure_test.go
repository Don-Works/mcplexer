package index

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func seedSymbol(t *testing.T, ms *memStore, ws, path, name, kind string, isTest bool) {
	t.Helper()
	err := ms.UpsertCodeIndexedFiles(context.Background(), ws, []store.IndexedFile{{
		File: store.CodeIndexFile{WorkspaceID: ws, Path: path, IsTest: isTest, PathTokens: tokenString(path)},
		Symbols: []store.CodeIndexSymbol{{
			Name: name, NameTokens: tokenString(name), Kind: kind, StartLine: 10, Exported: true,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

const goTestFailure = `--- FAIL: TestBuildCold (0.01s)
    build_test.go:52: FilesIndexed = 1, want 2
    runBuild returned the wrong count
panic: runtime error
	internal/index/build.go:99 +0x1a
FAIL	github.com/don-works/mcplexer/internal/index	0.512s
`

func TestMapFailureRanksMentionedFile(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedSymbol(t, ms, "ws", "internal/index/build.go", "runBuild", "func", false)
	seedSymbol(t, ms, "ws", "internal/index/build_test.go", "TestBuildCold", "func", true)

	cands, err := svc.mapFailure(context.Background(), "ws", "", goTestFailure, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 {
		t.Fatal("expected candidates from the failure text")
	}
	if cands[0].Path != "internal/index/build.go" {
		t.Fatalf("top candidate = %q, want internal/index/build.go\nall: %+v", cands[0].Path, cands)
	}
	// build.go should accumulate several distinct reasons (path, frame, package,
	// reverse test ownership).
	if len(cands[0].Reasons) < 3 {
		t.Errorf("top candidate reasons = %v, want >= 3 distinct signals", cands[0].Reasons)
	}
	if _, ok := candByPath(cands, "internal/index/build_test.go"); !ok {
		t.Error("the failing test file should also be a candidate")
	}
}

func TestMapFailureEmptyOnNoMatch(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedSymbol(t, ms, "ws", "internal/index/build.go", "runBuild", "func", false)
	cands, err := svc.mapFailure(context.Background(), "ws", "", "some unrelated prose with no paths", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Errorf("expected no candidates, got %+v", cands)
	}
}

func candByPath(cands []FailureCandidate, path string) (FailureCandidate, bool) {
	for _, c := range cands {
		if c.Path == path {
			return c, true
		}
	}
	return FailureCandidate{}, false
}
