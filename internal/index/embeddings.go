package index

import "context"

// Embedder is the deliberately tiny boundary between the code index and a
// vector provider. The daemon only wires a loopback provider after explicit
// code-index opt-in; tests can supply deterministic fakes. It intentionally
// mirrors memory.EmbedProvider without coupling the index package to memory.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) (vectors [][]float32, model string, err error)
	HasModel() bool
}

// NoopEmbedder keeps the lexical source-chunk index fully operational without
// any network call. It is the default for every code index service.
type NoopEmbedder struct{}

func (NoopEmbedder) Embed(context.Context, []string) ([][]float32, string, error) {
	return nil, "", nil
}

func (NoopEmbedder) HasModel() bool { return false }
