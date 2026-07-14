package index

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func seedContextFile(t *testing.T, ms *memStore, ws, path, doc string, syms []store.CodeIndexSymbol, edges []store.CodeIndexEdge) {
	t.Helper()
	for i := range syms {
		syms[i].NameTokens = tokenString(syms[i].Name)
	}
	err := ms.UpsertCodeIndexedFiles(context.Background(), ws, []store.IndexedFile{{
		File: store.CodeIndexFile{
			WorkspaceID: ws, Path: path, PathTokens: tokenString(path),
			Language: "go", DocSummary: doc,
		},
		Symbols: syms,
		Edges:   edges,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func seedKVWorkspace(t *testing.T, ms *memStore, ws string) {
	seedContextFile(t, ms, ws, "internal/kv/handler.go", "KV request handler.",
		[]store.CodeIndexSymbol{
			{Name: "HandleKVSet", Kind: "func", Exported: true, StartLine: 10},
			{Name: "HandleKVGet", Kind: "func", Exported: true, StartLine: 20},
		},
		[]store.CodeIndexEdge{{Kind: "import", ToPath: "internal/kv/store.go"}})
	seedContextFile(t, ms, ws, "internal/kv/store.go", "KV storage backend.",
		[]store.CodeIndexSymbol{{Name: "kvStore", Kind: "type", StartLine: 5}}, nil)
	seedContextFile(t, ms, ws, "internal/audit/log.go", "Audit log.",
		[]store.CodeIndexSymbol{{Name: "WriteAudit", Kind: "func", Exported: true, StartLine: 3}}, nil)
}

func TestContextPackRanking(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedKVWorkspace(t, ms, "ws")

	git := newGitRunner(t.TempDir(), svc.logger) // non-repo: git calls degrade to empty
	pack, err := svc.contextPack(context.Background(),
		ContextRequest{WorkspaceID: "ws", Query: "kv set", BudgetTokens: 16000}, git, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Files) == 0 {
		t.Fatal("expected ranked files")
	}
	if pack.Files[0].Path != "internal/kv/handler.go" {
		t.Errorf("top file = %q, want internal/kv/handler.go", pack.Files[0].Path)
	}
	// The KV files should outrank the unrelated audit file.
	if got := paths(pack.Files); !contains(got, "internal/kv/store.go") {
		t.Errorf("kv/store.go should appear (symbol + graph proximity); got %v", got)
	}
	for _, f := range pack.Files {
		if len(f.Symbols) == 0 && f.Path == "internal/kv/handler.go" {
			t.Error("handler.go should carry its matched symbols")
		}
	}
}

func TestContextPackBudget(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedKVWorkspace(t, ms, "ws")

	git := newGitRunner(t.TempDir(), svc.logger)
	pack, err := svc.contextPack(context.Background(),
		ContextRequest{WorkspaceID: "ws", Query: "kv set", BudgetTokens: 40}, git, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Tiny budget: only the top-ranked file is force-included.
	if len(pack.Files) != 1 {
		t.Errorf("tiny budget should yield exactly 1 file, got %d", len(pack.Files))
	}
	if pack.UsedTokens <= 0 {
		t.Error("UsedTokens should be reported")
	}
}

func TestContextPackGoPackageDirProximity(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedContextFile(t, ms, "ws", "internal/kv/handler.go", "KV handler.",
		[]store.CodeIndexSymbol{{Name: "HandleKVSet", Kind: "func", Exported: true, StartLine: 3}},
		[]store.CodeIndexEdge{{Kind: "import", ToPath: "internal/downstream"}})
	seedContextFile(t, ms, "ws", "internal/downstream/manager.go", "Downstream manager.",
		[]store.CodeIndexSymbol{{Name: "StartAll", Kind: "func", Exported: true, StartLine: 5}}, nil)
	seedContextFile(t, ms, "ws", "internal/downstream/helper.go", "Downstream helper.",
		[]store.CodeIndexSymbol{{Name: "helper", Kind: "func", StartLine: 2}}, nil)

	git := newGitRunner(t.TempDir(), svc.logger)
	pack, err := svc.contextPack(context.Background(),
		ContextRequest{WorkspaceID: "ws", Query: "HandleKVSet", BudgetTokens: 16000}, git, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := paths(pack.Files)
	if !contains(got, "internal/downstream/manager.go") {
		t.Fatalf("package-dir import should expand to indexed files; got %v", got)
	}
}

func TestContextPackEndToEnd(t *testing.T) {
	ms := newMemStore()
	svc, _ := testService(t)
	svc.store = ms
	seedKVWorkspace(t, ms, "ws")
	if err := ms.PutCodeIndexBuild(context.Background(), &store.CodeIndexBuild{
		WorkspaceID: "ws", BuiltAt: time.Now(), FileCount: 3, SymbolCount: 4,
	}); err != nil {
		t.Fatal(err)
	}
	pack, err := svc.ContextPack(context.Background(),
		ContextRequest{WorkspaceID: "ws", Root: t.TempDir(), Query: "kv set"})
	if err != nil {
		t.Fatal(err)
	}
	if pack.BudgetTokens != 4000 {
		t.Errorf("default budget = %d, want 4000", pack.BudgetTokens)
	}
	if pack.Files == nil {
		t.Error("Files should be non-nil (empty slice at worst)")
	}
}

func paths(files []ContextFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
