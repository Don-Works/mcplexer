// memory_graph_builder.go — graph construction split out of
// memory_graph_handler.go to keep both files under the 300-line cap.
// See that handler for routing + the response shape; this file is
// purely the math.
package api

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// wikilinkRe matches [[Some Name]] tokens. Newlines are excluded so a
// stray double-bracket doesn't gobble the rest of the file.
var wikilinkRe = regexp.MustCompile(`\[\[([^\[\]\n]{1,200})\]\]`)

// buildGraph turns a flat memory list into the node/edge response shape.
func buildGraph(rows []store.MemoryEntry) graphResponse {
	rows, truncated := sampleNodes(rows)

	tags := make([][]string, len(rows))
	nameToIdx := make(map[string]int, len(rows))
	for i, e := range rows {
		tags[i] = decodeTags(e.TagsJSON)
		key := strings.ToLower(strings.TrimSpace(e.Name))
		if key != "" {
			if _, exists := nameToIdx[key]; !exists {
				nameToIdx[key] = i
			}
		}
	}

	candidates := buildCandidateEdges(rows, tags, nameToIdx)
	edges := pruneTopKEdges(candidates, rows)

	degree := make(map[string]int, len(rows))
	for _, e := range edges {
		degree[e.Source]++
		degree[e.Target]++
	}

	nodes := make([]graphNode, len(rows))
	for i, m := range rows {
		nodes[i] = graphNode{
			ID:        m.ID,
			Title:     m.Name,
			Kind:      m.Kind,
			Tags:      tags[i],
			CreatedAt: m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Size:      degree[m.ID],
			Pinned:    m.Pinned,
		}
	}

	return graphResponse{
		Nodes:     nodes,
		Edges:     edges,
		Truncated: truncated,
		NodeCap:   maxGraphNodes,
	}
}

// sampleNodes caps the row count at maxGraphNodes. When over budget,
// pinned rows go in first (operator's manual signal of importance), then
// rows in input order (ListMemories already returns newest-first).
func sampleNodes(rows []store.MemoryEntry) ([]store.MemoryEntry, bool) {
	if len(rows) <= maxGraphNodes {
		return rows, false
	}
	out := make([]store.MemoryEntry, 0, maxGraphNodes)
	for _, r := range rows {
		if r.Pinned {
			out = append(out, r)
			if len(out) >= maxGraphNodes {
				return out, true
			}
		}
	}
	for _, r := range rows {
		if len(out) >= maxGraphNodes {
			break
		}
		if !r.Pinned {
			out = append(out, r)
		}
	}
	return out, true
}

// edgeKey is a canonical undirected-edge key (lo, hi index pair).
type edgeKey struct{ a, b int }

// buildCandidateEdges fuses co-tag + wikilink edges into one deduped map.
// Wikilink reason wins over co_tag when both fire on the same pair, and
// the higher weight survives.
func buildCandidateEdges(rows []store.MemoryEntry, tags [][]string, nameToIdx map[string]int) map[edgeKey]*graphEdge {
	candidates := make(map[edgeKey]*graphEdge, len(rows)*2)
	addOrMax := func(i, j int, w float64, reason string) {
		if i == j {
			return
		}
		lo, hi := i, j
		if hi < lo {
			lo, hi = hi, lo
		}
		k := edgeKey{lo, hi}
		existing, ok := candidates[k]
		if !ok {
			candidates[k] = &graphEdge{
				Source: rows[lo].ID,
				Target: rows[hi].ID,
				Weight: w,
				Reason: reason,
			}
			return
		}
		if w > existing.Weight {
			existing.Weight = w
		}
		if reason == "wikilink" {
			existing.Reason = "wikilink"
		}
	}

	// 1. Co-tag edges via tag inversion (tag → [memory indices]).
	tagIndex := make(map[string][]int, 64)
	for i, ts := range tags {
		for _, t := range ts {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" {
				continue
			}
			tagIndex[t] = append(tagIndex[t], i)
		}
	}
	for _, idxs := range tagIndex {
		if len(idxs) < 2 {
			continue
		}
		// Bound work on degenerate huge buckets — a tag attached to
		// thousands of memories isn't useful for graph structure anyway.
		if len(idxs) > 256 {
			idxs = idxs[:256]
		}
		for i := 0; i < len(idxs); i++ {
			ti := len(tags[idxs[i]])
			if ti == 0 {
				continue
			}
			for j := i + 1; j < len(idxs); j++ {
				tj := len(tags[idxs[j]])
				if tj == 0 {
					continue
				}
				shared := countSharedTags(tags[idxs[i]], tags[idxs[j]])
				maxT := ti
				if tj > maxT {
					maxT = tj
				}
				weight := float64(shared) / float64(maxT)
				if weight <= 0 {
					continue
				}
				addOrMax(idxs[i], idxs[j], weight, "co_tag")
			}
		}
	}

	// 2. Wikilinks ([[Name]]) → resolve to a memory by lowercase-name.
	// Weight 1.0; strongest reason wins ties.
	for i, e := range rows {
		matches := wikilinkRe.FindAllStringSubmatch(e.Content, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(m[1]))
			if key == "" {
				continue
			}
			tgt, ok := nameToIdx[key]
			if !ok || tgt == i {
				continue
			}
			addOrMax(i, tgt, 1.0, "wikilink")
		}
	}
	return candidates
}

// pruneTopKEdges keeps at most maxEdgesPerNode edges per node. An edge
// survives if it's in either endpoint's top-K — the union rule. Returns a
// stably-ordered slice (weight desc, then source/target lex for ties).
func pruneTopKEdges(candidates map[edgeKey]*graphEdge, rows []store.MemoryEntry) []graphEdge {
	all := make([]graphEdge, 0, len(candidates))
	for _, e := range candidates {
		all = append(all, *e)
	}
	byNode := make(map[string][]int, len(rows))
	for i, e := range all {
		byNode[e.Source] = append(byNode[e.Source], i)
		byNode[e.Target] = append(byNode[e.Target], i)
	}
	keep := make(map[int]bool, len(all))
	for _, idxs := range byNode {
		sort.Slice(idxs, func(a, b int) bool {
			return all[idxs[a]].Weight > all[idxs[b]].Weight
		})
		n := maxEdgesPerNode
		if len(idxs) < n {
			n = len(idxs)
		}
		for _, i := range idxs[:n] {
			keep[i] = true
		}
	}
	out := make([]graphEdge, 0, len(keep))
	for i := range keep {
		out = append(out, all[i])
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Weight != out[b].Weight {
			return out[a].Weight > out[b].Weight
		}
		if out[a].Source != out[b].Source {
			return out[a].Source < out[b].Source
		}
		return out[a].Target < out[b].Target
	})
	return out
}

// decodeTags pulls the tag array out of a MemoryEntry's TagsJSON. Returns
// nil on missing/invalid — we never want a single bad row to blow up the
// whole graph build.
func decodeTags(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// countSharedTags counts elements that appear in both lists. Both lists
// are short (<10 in practice), so a nested-loop comparison is fine.
func countSharedTags(a, b []string) int {
	n := 0
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				n++
				break
			}
		}
	}
	return n
}
