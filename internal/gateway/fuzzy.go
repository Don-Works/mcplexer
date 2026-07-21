package gateway

import "strings"

// fuzzyMatchTool attempts to find a tool by normalizing and fuzzy-matching the
// given name against the available tools. Used for code-mode calls where LLMs
// may hallucinate tool names (e.g. "github_create_issue" instead of
// "github__create_issue").
func fuzzyMatchTool(name string, tools []Tool) (Tool, bool) {
	normalized := normalizeName(name)

	// Phase 1: exact match after normalization.
	for _, t := range tools {
		if normalizeName(t.Name) == normalized {
			return t, true
		}
	}

	// Phase 2: Levenshtein distance within threshold.
	threshold := 2
	if len(name) > 8 {
		threshold = 3
	}
	bestDist := threshold + 1
	var bestTool Tool
	found := false

	for _, t := range tools {
		d := levenshtein(normalized, normalizeName(t.Name))
		if d < bestDist {
			bestDist = d
			bestTool = t
			found = true
		}
	}

	if found {
		return bestTool, true
	}
	return Tool{}, false
}

// normalizeName strips underscores and hyphens, lowercases for comparison.
func normalizeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, ch := range strings.ToLower(name) {
		if ch != '_' && ch != '-' {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = curr
	}

	return prev[len(b)]
}
