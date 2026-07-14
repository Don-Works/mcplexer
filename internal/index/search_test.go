package index

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type deterministicCodeEmbedder struct {
	mu     sync.Mutex
	model  string
	inputs []string
	calls  int
}

func (e *deterministicCodeEmbedder) HasModel() bool { return true }

func (e *deterministicCodeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, string, error) {
	e.mu.Lock()
	e.calls++
	e.inputs = append(e.inputs, inputs...)
	e.mu.Unlock()
	out := make([][]float32, len(inputs))
	for i, input := range inputs {
		v := make([]float32, embedVectorDim)
		lower := strings.ToLower(input)
		switch {
		case strings.Contains(lower, "invoice"), strings.Contains(lower, "payment"), strings.Contains(lower, "settlement"):
			v[1] = 1
		case strings.Contains(lower, "auth"), strings.Contains(lower, "session"), strings.Contains(lower, "login"):
			v[2] = 1
		default:
			v[3] = 1
		}
		out[i] = v
	}
	return out, e.model, nil
}

func (e *deterministicCodeEmbedder) snapshot() (int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, append([]string(nil), e.inputs...)
}

func waitUntil(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true before deadline")
}

func TestSearchLexicalReturnsRankedCitation(t *testing.T) {
	svc, _ := testService(t)
	root := t.TempDir()
	writeWorkspaceFile(t, root, "internal/auth/session.go", `package auth

func ValidateSessionToken(token string) bool { return token != "" }
`)
	writeWorkspaceFile(t, root, "internal/billing/invoice.go", `package billing

func SettleInvoice() {}
`)

	result, err := svc.Search(context.Background(), SearchRequest{
		WorkspaceID: "logical-a", Root: root, Query: "ValidateSessionToken", Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("expected lexical source hit")
	}
	got := result.Hits[0]
	if got.Path != "internal/auth/session.go" || got.StartLine <= 0 || got.EndLine < got.StartLine {
		t.Fatalf("bad top citation: %+v", got)
	}
	if got.Citation != "internal/auth/session.go:1-3" {
		t.Fatalf("citation = %q, want stable root-relative lines", got.Citation)
	}
	if result.Mode != "lexical" || len(got.Sources) != 1 || got.Sources[0] != "lexical" {
		t.Fatalf("unexpected retrieval provenance: mode=%q sources=%v", result.Mode, got.Sources)
	}
	if result.Embeddings.State != "disabled" {
		t.Fatalf("default embeddings state = %q, want disabled", result.Embeddings.State)
	}
}

func TestEmbeddingBackfillAndSemanticSearch(t *testing.T) {
	svc, _ := testService(t)
	emb := &deterministicCodeEmbedder{model: "local-code-test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.ConfigureEmbeddings(ctx, emb, emb.model)
	root := t.TempDir()
	writeWorkspaceFile(t, root, "billing/ledger.go", `package billing

// ReconcilePayment records a settlement against an invoice.
func ReconcilePayment() {}
`)
	writeWorkspaceFile(t, root, "auth/session.go", `package auth

func LoginSession() {}
`)
	writeWorkspaceFile(t, root, "node_modules/leak/index.js", "const privateDependency = true")

	build, err := svc.Build(ctx, BuildRequest{WorkspaceID: "logical-a", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	indexID := build.IndexID
	waitUntil(t, func() bool {
		st := svc.embeddingStatus(context.Background(), indexID)
		return st.State == "ready" && st.Total == 2 && st.Embedded == 2
	})

	result, err := svc.Search(context.Background(), SearchRequest{
		WorkspaceID: "logical-b", Root: root, Query: "invoice settlement", Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "hybrid" || len(result.Hits) == 0 {
		t.Fatalf("semantic search not active: mode=%q hits=%d status=%+v", result.Mode, len(result.Hits), result.Embeddings)
	}
	if result.Hits[0].Path != "billing/ledger.go" {
		t.Fatalf("top semantic hit = %q, want billing/ledger.go", result.Hits[0].Path)
	}
	_, captured := emb.snapshot()
	for _, input := range captured {
		if strings.Contains(input, "node_modules") || strings.Contains(input, "privateDependency") {
			t.Fatalf("denied dependency reached embedder: %q", input)
		}
	}
}

func TestEmbeddingBackfillRefusesDeniedLegacyRow(t *testing.T) {
	svc, ms := testService(t)
	emb := &deterministicCodeEmbedder{model: "local-code-test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.ConfigureEmbeddings(ctx, emb, emb.model)
	indexID := "repo-test"
	err := ms.UpsertCodeIndexedFiles(ctx, indexID, []store.IndexedFile{{
		File: store.CodeIndexFile{Path: "node_modules/pkg/index.js", ChunkVersion: chunkSchemaVersion},
		Chunks: []store.CodeIndexChunk{{
			Path: "node_modules/pkg/index.js", Ordinal: 0, Content: "secret dependency source",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	svc.startEmbeddingBackfill(indexID)
	waitUntil(t, func() bool {
		return svc.embeddingStatus(context.Background(), indexID).State == "error"
	})
	calls, _ := emb.snapshot()
	if calls != 0 {
		t.Fatalf("embedder called %d times for a denied legacy row", calls)
	}
}

func TestContextPackIncludesRankedSourceSnippet(t *testing.T) {
	svc, _ := testService(t)
	root := t.TempDir()
	writeWorkspaceFile(t, root, "internal/index/search.go", `package index

func FuseSemanticAndLexicalRanks() {}
`)
	pack, err := svc.ContextPack(context.Background(), ContextRequest{
		WorkspaceID: "logical", Root: root, Query: "semantic lexical fusion", BudgetTokens: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Files) == 0 || len(pack.Files[0].Snippets) == 0 {
		t.Fatalf("context pack omitted source snippets: %+v", pack)
	}
	if pack.Files[0].Snippets[0].Path != "internal/index/search.go" {
		t.Fatalf("snippet path = %q", pack.Files[0].Snippets[0].Path)
	}
}
