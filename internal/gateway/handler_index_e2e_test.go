package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/index"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestIndexE2ESmoke indexes this repository through the real dispatch path and
// asserts the headline queries answer correctly.
func TestIndexE2ESmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping index e2e in short mode")
	}
	root := repoRootForTest(t)
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.store = db
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-repo", RootPath: root}}
	h.codeIndex = index.NewService(db, nil)

	// Scope the build to two packages to keep the smoke test fast while still
	// having cross-package import edges (cmd/mcplexer imports internal/gateway).
	if buildOut := indexOK(t, h, "index__build", `{"paths":["internal/gateway","cmd/mcplexer"]}`); !strings.Contains(buildOut, "files_indexed") {
		t.Fatalf("build result missing counters: %s", buildOut)
	}

	// Symbol search word-splits camelCase, so "dispatch kv tool" surfaces dispatchKVTool.
	if symOut := indexOK(t, h, "index__symbols", `{"query":"dispatch kv tool"}`); !strings.Contains(symOut, "dispatchKVTool") {
		t.Fatalf("symbols did not find dispatchKVTool: %s", symOut)
	}

	// Importers of a gateway file resolve via its package dir: cmd/mcplexer
	// files import internal/gateway, so the list must name them.
	if depOut := indexOK(t, h, "index__deps", `{"file":"internal/gateway/handler_kv.go","direction":"importers"}`); !strings.Contains(depOut, "cmd/mcplexer") {
		t.Fatalf("deps importers missing cmd/mcplexer edge: %s", depOut)
	}

	// The context pack must respect its token budget.
	ctxOut := indexOK(t, h, "index__context", `{"query":"dispatch kv tool","budget_tokens":2000}`)
	var pack struct {
		BudgetTokens int `json:"budget_tokens"`
		UsedTokens   int `json:"used_tokens"`
	}
	mustUnmarshalToolText(t, ctxOut, &pack)
	if pack.UsedTokens > pack.BudgetTokens {
		t.Fatalf("context pack over budget: used=%d budget=%d", pack.UsedTokens, pack.BudgetTokens)
	}

	// Hybrid search retrieves citation-ready source: FTS5 always runs (no
	// embeddings wired here, so mode stays lexical) and the top snippet for the
	// dispatch query must cite the gateway source that implements it.
	searchOut := indexOK(t, h, "index__search", `{"query":"dispatch kv tool"}`)
	if !strings.Contains(searchOut, `"mode":"lexical"`) {
		t.Fatalf("expected lexical mode without embeddings, got %s", searchOut)
	}
	if !strings.Contains(searchOut, "dispatchKVTool") || !strings.Contains(searchOut, "handler_kv.go") {
		t.Fatalf("search did not cite the dispatchKVTool source: %s", searchOut)
	}
}

// indexOK dispatches an index tool expecting a non-error tool result and returns
// its text payload.
func indexOK(t *testing.T, h *handler, name, args string) string {
	t.Helper()
	raw, rpcErr, handled := h.dispatchIndexTool(context.Background(), name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("%s rpc error: %v", name, rpcErr)
	}
	var env struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	if env.IsError {
		t.Fatalf("%s returned error: %s", name, env.Content[0].Text)
	}
	if len(env.Content) == 0 {
		return ""
	}
	return env.Content[0].Text
}

func mustUnmarshalToolText(t *testing.T, text string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatalf("unmarshal tool text: %v (text=%s)", err, text)
	}
}

// repoRootForTest walks up from the test's working directory to the module root
// (the directory containing go.mod).
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above working dir")
		}
		dir = parent
	}
}
