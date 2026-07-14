package index

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestRepositorySearchQuality is a live golden-query regression against this
// repository. It catches the failure mode where the storage/index plumbing is
// green but natural-language retrieval is not actually useful to an agent.
func TestRepositorySearchQuality(t *testing.T) {
	if testing.Short() {
		t.Skip("repository-scale retrieval evaluation")
	}
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	svc, _ := testService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	build, err := svc.Build(ctx, BuildRequest{WorkspaceID: "quality-eval", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if build.ChunkCount < 100 {
		t.Fatalf("evaluation indexed only %d chunks; corpus unexpectedly incomplete", build.ChunkCount)
	}
	// Do not let the evaluator's own literal golden queries win BM25.
	if err := svc.store.DeleteCodeIndexFiles(ctx, build.IndexID, []string{"internal/index/quality_test.go"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		query string
		want  string
	}{
		{"weighted reciprocal rank lexical semantic fusion", "internal/index/search.go"},
		{"loopback only code embedding endpoint privacy", "cmd/mcplexer/code_index_embedder.go"},
		{"source chunks citation overlap line boundaries", "internal/index/chunk.go"},
		{"canonical repository root shared workspace physical index", "internal/index/service.go"},
		{"embedding backfill stale vectors batch retry", "internal/index/embeddings.go"},
		{"context pack graph proximity token budget snippets", "internal/index/contextpack.go"},
		{"map pasted test failure stack trace to candidate files", "internal/index/failure.go"},
		{"git dirty source freshness status", "internal/index/sourcestate.go"},
	}

	found, reciprocalRank := 0, 0.0
	for _, tc := range cases {
		result, err := svc.Search(ctx, SearchRequest{
			WorkspaceID: "another-logical-workspace", Root: root, Query: tc.query, Limit: 5,
		})
		if err != nil {
			t.Errorf("search %q: %v", tc.query, err)
			continue
		}
		rank := 0
		for i, hit := range result.Hits {
			if hit.Path == tc.want {
				rank = i + 1
				break
			}
		}
		if rank > 0 {
			found++
			reciprocalRank += 1 / float64(rank)
			t.Logf("hit rank=%d query=%q want=%s", rank, tc.query, tc.want)
		} else {
			var got []string
			for _, hit := range result.Hits {
				got = append(got, hit.Path)
			}
			t.Logf("miss query=%q want=%s top5=%v", tc.query, tc.want, got)
		}
	}
	recallAt5 := float64(found) / float64(len(cases))
	mrr := reciprocalRank / float64(len(cases))
	t.Logf("code-index quality: recall@5=%.3f MRR=%.3f chunks=%d", recallAt5, mrr, build.ChunkCount)
	if recallAt5 < 0.875 || mrr < 0.65 {
		t.Fatalf("retrieval quality below floor: recall@5=%.3f MRR=%.3f", recallAt5, mrr)
	}
}
