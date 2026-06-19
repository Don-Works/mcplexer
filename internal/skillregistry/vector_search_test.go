package skillregistry_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/embedding"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

type mockEmbedder struct {
	vectors [][]float32
	model   string
	called  bool
}

func (m *mockEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, string, error) {
	m.called = true
	if len(inputs) == 0 {
		return nil, m.model, nil
	}
	vecs := make([][]float32, len(inputs))
	for i := range inputs {
		if i < len(m.vectors) {
			vecs[i] = m.vectors[i]
		} else if len(m.vectors) > 0 {
			vecs[i] = m.vectors[0]
		}
	}
	return vecs, m.model, nil
}

func (m *mockEmbedder) HasModel() bool { return m.model != "" }

func TestSearchWithNoEmbedderFallsBackToTFIDF(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	reg.SetEmbedder(embedding.NoopEmbedder{})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "pdf-extract",
		Body: sampleBody("pdf-extract", "Extract text from PDF documents."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "image-resize",
		Body: sampleBody("image-resize", "Resize and compress images."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	hits, err := reg.Search(ctx, skillregistry.GlobalScope(), "extract text from pdf", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if hits[0].Name != "pdf-extract" {
		t.Errorf("expected pdf-extract first, got %s", hits[0].Name)
	}
}

func TestSearchWithEmbedderUsesHybrid(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	mock := &mockEmbedder{
		model: "test-model",
		vectors: [][]float32{
			{0.9, 0.1, 0.1},
			{0.1, 0.9, 0.1},
		},
	}
	reg.SetEmbedder(mock)

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "pdf-extract",
		Body: sampleBody("pdf-extract", "Extract text from PDF documents."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "image-resize",
		Body: sampleBody("image-resize", "Resize and compress images."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	hits, err := reg.Search(ctx, skillregistry.GlobalScope(), "extract text from pdf", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if !mock.called {
		t.Error("expected embedder to be called")
	}
}

func TestSearchStaleIndexRefreshesAfterPublish(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	reg.SetEmbedder(embedding.NoopEmbedder{})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "first",
		Body: sampleBody("first", "First skill."),
	}); err != nil {
		t.Fatalf("publish first: %v", err)
	}

	hits1, err := reg.Search(ctx, skillregistry.GlobalScope(), "first", 5)
	if err != nil {
		t.Fatalf("search 1: %v", err)
	}
	if len(hits1) == 0 {
		t.Fatal("expected hits before invalidation")
	}

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "second",
		Body: sampleBody("second", "Second skill."),
	}); err != nil {
		t.Fatalf("publish second: %v", err)
	}

	hits2, err := reg.Search(ctx, skillregistry.GlobalScope(), "second", 5)
	if err != nil {
		t.Fatalf("search 2: %v", err)
	}
	if len(hits2) == 0 {
		t.Fatal("expected hits after refresh")
	}
}

func TestSearchWorkspaceScoping(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	wsA := "ws-alpha"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name:        "shared",
		Body:        sampleBody("shared", "Workspace A version."),
		WorkspaceID: &wsA,
	}); err != nil {
		t.Fatalf("publish ws: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "global",
		Body: sampleBody("global", "Global skill."),
	}); err != nil {
		t.Fatalf("publish global: %v", err)
	}

	scopeA := store.SkillScope{WorkspaceIDs: []string{wsA}}
	hitsA, err := reg.Search(ctx, scopeA, "workspace", 5)
	if err != nil {
		t.Fatalf("search A: %v", err)
	}
	if len(hitsA) == 0 {
		t.Fatal("expected hits in scope A")
	}

	scopeB := store.SkillScope{WorkspaceIDs: []string{"ws-beta"}}
	hitsB, err := reg.Search(ctx, scopeB, "global", 5)
	if err != nil {
		t.Fatalf("search B: %v", err)
	}
	if len(hitsB) == 0 {
		t.Fatal("expected hits in scope B for global skill")
	}
}

func TestSearchResultBounds(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: name,
			Body: sampleBody(name, "Description for "+name),
		}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
	}

	hits, err := reg.Search(ctx, skillregistry.GlobalScope(), "description", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) > 3 {
		t.Errorf("expected at most 3 hits, got %d", len(hits))
	}
	if len(hits) < 1 {
		t.Error("expected at least 1 hit")
	}
}
