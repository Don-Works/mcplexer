package gateway

import (
	"sort"
	"strings"
)

// Tunable weights for the hybrid keyword + semantic ranker.
//
// keywordWeight scales the BM25-ish keyword score (`scoreMatch`) once it has
// been normalised to a [0..1] range. semanticWeight scales the cosine
// similarity from the TF-IDF index. Together they sum to the final score
// returned to the caller.
const (
	keywordWeight  = 0.6
	semanticWeight = 0.4
	// keywordNorm is the divisor used to map raw `scoreMatch` ints into
	// roughly [0..1]. The maximum currently observed is around 200 (exact
	// name match plus several tokens), so 200 keeps the head saturated.
	keywordNorm = 200.0
)

// rankedTool is the ranked-search output: a tool plus its blended score and
// component scores for transparency.
type rankedTool struct {
	Tool     Tool
	Score    float64
	Keyword  float64
	Semantic float64
}

// hybridSearch returns tools ranked by a weighted combination of the existing
// keyword scorer and TF-IDF cosine similarity. The result is capped at
// maxResults.
//
// allTools is the candidate pool (already filtered by workspace + namespace).
// query is the original user query (unmodified — synonyms are applied
// downstream).
func hybridSearch(
	allTools []Tool, query string, idx *semanticIndex, maxResults int,
) []rankedTool {
	queryLower := strings.ToLower(query)

	semScores := make(map[string]float64, len(allTools))
	for _, s := range idx.searchScored(query, maxResults*4) {
		semScores[s.tool.Name] = s.cos
	}

	scored := make([]rankedTool, 0, len(allTools))
	for _, t := range allTools {
		kw := float64(scoreMatch(t, queryLower)) / keywordNorm
		if kw > 1 {
			kw = 1
		}
		sem := semScores[t.Name]
		if kw == 0 && sem == 0 {
			continue
		}
		final := keywordWeight*kw + semanticWeight*sem
		scored = append(scored, rankedTool{
			Tool: t, Score: final, Keyword: kw, Semantic: sem,
		})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > maxResults {
		scored = scored[:maxResults]
	}
	return scored
}

// filterByNamespaces returns only the tools whose namespace prefix is in
// allowed. An empty allowed list returns the input unchanged.
func filterByNamespaces(tools []Tool, allowed []string) []Tool {
	if len(allowed) == 0 {
		return tools
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		allow[strings.ToLower(strings.TrimSpace(ns))] = struct{}{}
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		ns, _, ok := splitNamespace(t.Name)
		if !ok {
			ns = t.Name
		}
		if _, hit := allow[strings.ToLower(ns)]; hit {
			out = append(out, t)
		}
	}
	return out
}

// groupByNamespace groups ranked results by their namespace prefix while
// preserving the score-descending order within each group.
func groupByNamespace(results []rankedTool) []namespaceGroup {
	order := make([]string, 0)
	groups := make(map[string][]rankedTool)
	for _, r := range results {
		ns, _, ok := splitNamespace(r.Tool.Name)
		if !ok {
			ns = "(other)"
		}
		if _, seen := groups[ns]; !seen {
			order = append(order, ns)
		}
		groups[ns] = append(groups[ns], r)
	}
	out := make([]namespaceGroup, 0, len(order))
	for _, ns := range order {
		out = append(out, namespaceGroup{Namespace: ns, Hits: groups[ns]})
	}
	return out
}

// namespaceGroup buckets ranked hits under their namespace.
type namespaceGroup struct {
	Namespace string
	Hits      []rankedTool
}

// snippet returns the first ~120 chars of the tool description, single-line.
func snippet(desc string) string {
	if idx := strings.Index(desc, "\n\n"); idx > 0 {
		desc = desc[:idx]
	}
	desc = strings.ReplaceAll(desc, "\n", " ")
	desc = strings.TrimSpace(desc)
	if len(desc) > 120 {
		desc = desc[:117] + "..."
	}
	return desc
}
