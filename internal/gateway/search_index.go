package gateway

import (
	"sync"

	"github.com/don-works/mcplexer/internal/embedding"
)

// semanticIndex is a lazy-built TF-IDF index over all tool descriptions.
// At index time each document's text is augmented with synonym clusters so
// that, e.g., a `customer__create_customer` tool also retrieves under
// "make customer" or "add customer".
type semanticIndex struct {
	mu       sync.Mutex
	index    *embedding.Index
	tools    map[string]Tool
	synonyms *synonymTable
}

// rebuild recreates the TF-IDF index from the given tools.
func (si *semanticIndex) rebuild(tools []Tool) {
	si.mu.Lock()
	defer si.mu.Unlock()

	if si.synonyms == nil {
		si.synonyms = defaultSynonyms()
	}
	docs := make([]embedding.Document, len(tools))
	toolMap := make(map[string]Tool, len(tools))
	for i, t := range tools {
		text := si.synonyms.expandText(buildSearchText(t))
		docs[i] = embedding.Document{ID: t.Name, Text: text}
		toolMap[t.Name] = t
	}
	si.index = embedding.NewIndex(docs)
	si.tools = toolMap
}

// scoredTool pairs a Tool with its raw cosine similarity from the TF-IDF index.
type scoredTool struct {
	tool Tool
	cos  float64
}

// searchScored returns tools ranked by TF-IDF cosine similarity to the query
// along with their raw similarity scores. Caller is responsible for blending.
func (si *semanticIndex) searchScored(query string, maxResults int) []scoredTool {
	si.mu.Lock()
	idx := si.index
	tools := si.tools
	syn := si.synonyms
	si.mu.Unlock()

	if idx == nil {
		return nil
	}
	if syn != nil {
		query = syn.expandText(query)
	}
	results := idx.Search(query, maxResults)
	out := make([]scoredTool, 0, len(results))
	for _, r := range results {
		if t, ok := tools[r.ID]; ok {
			out = append(out, scoredTool{tool: t, cos: r.Score})
		}
	}
	return out
}
