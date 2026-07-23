package eval

import (
	"context"
	"hash/fnv"
	"math"

	"github.com/don-works/mcplexer/internal/memory"
)

// hashEmbedder is a deterministic, offline, network-free embedding provider:
// a classic hashed bag-of-words projection. Each lowercased token is hashed
// into one of EmbedDim buckets with a sign derived from a second hash, the
// counts are accumulated, and the vector is L2-normalized.
//
// This is a genuine (if crude) semantic model, not a placeholder: documents
// that share vocabulary land near each other in cosine space, and documents
// that share none are near-orthogonal. That is exactly the property the fused
// scenario needs — the vector arm must rank the gold document highly for its
// query so that any subsequent demotion is attributable to ranking, not to a
// vector arm that never found the answer.
//
// It is intentionally NOT a stand-in for a real embedding model. It has no
// synonymy and no word order, so it must never be used to make claims about
// absolute retrieval quality — only about which code path runs and how
// rerankHits reorders a pool that already contains the right answer.
type hashEmbedder struct{}

func (hashEmbedder) HasModel() bool { return true }

func (e hashEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, string, error) {
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		out[i] = e.vector(in)
	}
	return out, "eval-hash-bow-v1", nil
}

func (hashEmbedder) vector(text string) []float32 {
	v := make([]float32, memory.EmbedDim)
	for _, tok := range tokens(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		sum := h.Sum32()
		idx := int(sum % uint32(memory.EmbedDim))
		if sum&0x80000000 != 0 {
			v[idx] -= 1
		} else {
			v[idx] += 1
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		// A vec0 MATCH needs a non-zero vector; an all-stopword document would
		// otherwise be silently unindexable.
		v[memory.EmbedDim-1] = 1
		return v
	}
	inv := float32(1.0 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v
}
