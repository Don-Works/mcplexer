package gateway

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed search_synonyms.txt
var rawSynonyms string

// synonymTable maps each known term to the set of every term in its cluster
// (including the term itself). Lookup is O(1).
type synonymTable struct {
	clusters map[string][]string
}

var (
	synonymOnce  sync.Once
	synonymCache *synonymTable
)

// defaultSynonyms returns the lazily-parsed embedded synonym table.
func defaultSynonyms() *synonymTable {
	synonymOnce.Do(func() {
		synonymCache = parseSynonyms(rawSynonyms)
	})
	return synonymCache
}

// parseSynonyms parses the embedded synonyms.txt format.
func parseSynonyms(raw string) *synonymTable {
	table := &synonymTable{clusters: make(map[string][]string)}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := splitAndClean(line)
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts {
			table.clusters[p] = parts
		}
	}
	return table
}

// splitAndClean splits a comma-separated cluster line into normalised tokens.
func splitAndClean(line string) []string {
	rawParts := strings.Split(line, ",")
	out := make([]string, 0, len(rawParts))
	seen := make(map[string]struct{}, len(rawParts))
	for _, p := range rawParts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// expandTerm returns the synonym cluster for a term, or just [term] if the
// term has no known synonyms.
func (s *synonymTable) expandTerm(term string) []string {
	if cluster, ok := s.clusters[term]; ok {
		return cluster
	}
	return []string{term}
}

// expandTokens returns the union of synonym expansions for every input token,
// preserving order and dropping duplicates. The original tokens are always
// kept first, with newly-introduced synonyms appended afterward.
func (s *synonymTable) expandTokens(tokens []string) []string {
	if s == nil {
		return tokens
	}
	seen := make(map[string]struct{}, len(tokens)*2)
	out := make([]string, 0, len(tokens)*2)
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range tokens {
		for _, syn := range s.expandTerm(t) {
			if _, ok := seen[syn]; ok {
				continue
			}
			seen[syn] = struct{}{}
			out = append(out, syn)
		}
	}
	return out
}

// expandText splits text into tokens, expands them, and rejoins. Used when
// indexing tool documents so that "make_X" docs also match "create" queries.
func (s *synonymTable) expandText(text string) string {
	if s == nil {
		return text
	}
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	expanded := s.expandTokens(tokens)
	return strings.Join(expanded, " ")
}
