package embedding

import (
	"math"
	"strings"
)

// Document represents a searchable item with a pre-computed TF-IDF vector.
type Document struct {
	ID     string
	Text   string
	vector map[string]float64
}

// Index holds TF-IDF document vectors for semantic search.
type Index struct {
	docs []Document
	idf  map[string]float64
}

// NewIndex builds a TF-IDF index from a set of documents.
// Each document should have a unique ID and searchable text.
func NewIndex(docs []Document) *Index {
	idx := &Index{docs: docs}
	idx.buildIDF()
	idx.buildVectors()
	return idx
}

// Search returns up to maxResults document IDs ranked by cosine similarity
// to the query, along with their scores. Results with score <= 0 are excluded.
func (idx *Index) Search(query string, maxResults int) []SearchResult {
	qVec := idx.queryVector(query)
	if len(qVec) == 0 {
		return nil
	}

	type scored struct {
		id    string
		score float64
	}
	var results []scored

	for _, doc := range idx.docs {
		sim := cosineSimilarity(qVec, doc.vector)
		if sim > 0 {
			results = append(results, scored{id: doc.ID, score: sim})
		}
	}

	// Sort by score descending.
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{ID: r.id, Score: r.score}
	}
	return out
}

// SearchResult holds a document ID and its relevance score.
type SearchResult struct {
	ID    string
	Score float64
}

// buildIDF computes inverse document frequency for all terms across documents.
func (idx *Index) buildIDF() {
	df := make(map[string]int) // document frequency per term
	for _, doc := range idx.docs {
		seen := make(map[string]bool)
		for _, term := range tokenize(doc.Text) {
			if !seen[term] {
				df[term]++
				seen[term] = true
			}
		}
	}

	n := float64(len(idx.docs))
	idx.idf = make(map[string]float64, len(df))
	for term, count := range df {
		idx.idf[term] = math.Log(1 + n/float64(count))
	}
}

// buildVectors computes TF-IDF vectors for all documents.
func (idx *Index) buildVectors() {
	for i := range idx.docs {
		idx.docs[i].vector = idx.tfidfVector(idx.docs[i].Text)
	}
}

// queryVector builds a TF-IDF vector for a search query.
func (idx *Index) queryVector(query string) map[string]float64 {
	return idx.tfidfVector(query)
}

// tfidfVector computes a TF-IDF vector for the given text.
func (idx *Index) tfidfVector(text string) map[string]float64 {
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return nil
	}

	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}

	vec := make(map[string]float64, len(tf))
	for term, count := range tf {
		idf, ok := idx.idf[term]
		if !ok {
			idf = math.Log(1 + float64(len(idx.docs)))
		}
		vec[term] = float64(count) / float64(len(tokens)) * idf
	}
	return vec
}

// tokenize splits text into lowercase tokens, splitting on non-alphanumeric chars.
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	return words
}

// cosineSimilarity computes the cosine similarity between two sparse vectors.
func cosineSimilarity(a, b map[string]float64) float64 {
	var dot, normA, normB float64

	for k, va := range a {
		normA += va * va
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
	}
	for _, vb := range b {
		normB += vb * vb
	}

	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
