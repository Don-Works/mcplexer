package index

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	searchCandidateFloor = 40
	searchCandidateMax   = 100
	searchPerFileCap     = 2
	rrfConstant          = 40.0
	semanticRRFWeight    = 0.85
	searchSnippetLines   = 36
	searchSnippetBytes   = 2600
)

// Search returns citation-ready source chunks. FTS5 always runs; when an
// opted-in local embedding model has completed at least one chunk, vec0 KNN is
// fused with lexical rank using weighted reciprocal-rank fusion.
func (s *Service) Search(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrQueryRequired
	}
	if err := s.ensureBuilt(ctx, req.WorkspaceID, req.Root); err != nil {
		return nil, err
	}
	indexID := indexIDForRoot(req.Root)
	s.startEmbeddingBackfill(indexID)
	return s.searchChunks(ctx, indexID, req.Query, req.Kind, clampLimit(req.Limit, 12, 50))
}

type fusedChunk struct {
	chunk   store.CodeIndexChunk
	score   float64
	sources map[string]bool
}

func (s *Service) searchChunks(
	ctx context.Context, indexID, query, kind string, limit int,
) (*SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, ErrQueryRequired
	}
	limit = clampLimit(limit, 12, 50)
	candidateLimit := limit * 5
	if candidateLimit < searchCandidateFloor {
		candidateLimit = searchCandidateFloor
	}
	if candidateLimit > searchCandidateMax {
		candidateLimit = searchCandidateMax
	}

	lexical, err := s.store.SearchCodeIndexChunks(ctx, store.CodeIndexChunkQuery{
		WorkspaceID: indexID,
		Query:       lexicalQuery(query),
		Kind:        strings.TrimSpace(kind),
		Limit:       candidateLimit,
	})
	if err != nil {
		return nil, err
	}
	status := s.embeddingStatus(ctx, indexID)
	var semantic []store.CodeIndexChunkHit
	if status.Enabled && status.Embedded > 0 {
		emb, model, _, _ := s.embeddingSnapshot()
		vectors, returnedModel, embedErr := emb.Embed(ctx, []string{query})
		if embedErr != nil {
			status.State = "error"
			status.LastError = embedErr.Error()
		} else if returnedModel != "" && returnedModel != model {
			status.State = "error"
			status.LastError = fmt.Sprintf("embedding provider returned model %q, configured %q", returnedModel, model)
		} else if len(vectors) != 1 {
			status.State = "error"
			status.LastError = "embedding provider returned an invalid query vector"
		} else {
			queryVector, normErr := normalizeCodeVector(vectors[0])
			if normErr != nil {
				status.State = "error"
				status.LastError = "embedding provider returned an invalid query vector: " + normErr.Error()
				vectors = nil
			}
			if queryVector == nil {
				semantic = nil
			} else {
				semantic, embedErr = s.store.VectorSearchCodeIndexChunks(
					ctx, indexID, model, codeEmbeddingVersion, queryVector, candidateLimit,
				)
				if embedErr != nil {
					status.State = "error"
					status.LastError = embedErr.Error()
					semantic = nil
				} else if strings.TrimSpace(kind) != "" {
					filtered := semantic[:0]
					for _, hit := range semantic {
						if hit.Chunk.Kind == strings.TrimSpace(kind) {
							filtered = append(filtered, hit)
						}
					}
					semantic = filtered
				}
			}
		}
	}

	fused := make(map[string]*fusedChunk, len(lexical)+len(semantic))
	add := func(hits []store.CodeIndexChunkHit, source string, weight float64) {
		for rank, hit := range hits {
			key := chunkKey(hit.Chunk)
			cand := fused[key]
			if cand == nil {
				cand = &fusedChunk{chunk: hit.Chunk, sources: map[string]bool{}}
				fused[key] = cand
			}
			cand.score += weight / (rrfConstant + float64(rank+1))
			cand.sources[source] = true
		}
	}
	add(lexical, "lexical", 1)
	add(semantic, "semantic", semanticRRFWeight)
	applyExactBoosts(fused, query)

	ordered := make([]*fusedChunk, 0, len(fused))
	for _, cand := range fused {
		ordered = append(ordered, cand)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].chunk.Path != ordered[j].chunk.Path {
			return ordered[i].chunk.Path < ordered[j].chunk.Path
		}
		return ordered[i].chunk.StartLine < ordered[j].chunk.StartLine
	})

	result := &SearchResult{
		IndexID: indexID, Query: query, Mode: "lexical",
		Hits: []ChunkHit{}, Embeddings: status,
	}
	if len(semantic) > 0 {
		result.Mode = "hybrid"
	}
	best := 0.0
	if len(ordered) > 0 {
		best = ordered[0].score
	}
	perFile := map[string]int{}
	for _, cand := range ordered {
		if perFile[cand.chunk.Path] >= searchPerFileCap {
			continue
		}
		perFile[cand.chunk.Path]++
		snippet := snippetForQuery(cand.chunk, query)
		sources := sourceNames(cand.sources)
		score := cand.score
		if best > 0 {
			score /= best
		}
		result.Hits = append(result.Hits, ChunkHit{
			CodeSnippet: snippet,
			Score:       round3(score),
			Sources:     sources,
		})
		if len(result.Hits) >= limit {
			break
		}
	}
	return result, nil
}

func chunkKey(c store.CodeIndexChunk) string {
	if c.ID > 0 {
		return fmt.Sprintf("id:%d", c.ID)
	}
	return fmt.Sprintf("%s\x00%d\x00%s", c.Path, c.Ordinal, c.ContentHash)
}

func applyExactBoosts(cands map[string]*fusedChunk, query string) {
	phrase := strings.ToLower(strings.TrimSpace(query))
	tokens := meaningfulQueryTokens(query)
	testIntent := hasAnyToken(tokens, "test", "tests", "spec", "assert", "fixture")
	for _, cand := range cands {
		pathLower := strings.ToLower(cand.chunk.Path)
		symbolLower := strings.ToLower(cand.chunk.SymbolName)
		contentLower := strings.ToLower(cand.chunk.Content)
		if phrase != "" && symbolLower == phrase {
			cand.score += 0.025
		} else if phrase != "" && strings.Contains(symbolLower, phrase) {
			cand.score += 0.012
		}
		if phrase != "" && strings.Contains(pathLower, phrase) {
			cand.score += 0.01
		}
		if len(phrase) >= 4 && strings.Contains(contentLower, phrase) {
			cand.score += 0.004
		}
		matched := 0
		for _, token := range tokens {
			if strings.Contains(symbolLower, token) || strings.Contains(pathLower, token) {
				matched++
			}
		}
		if len(tokens) > 0 {
			cand.score += 0.004 * float64(matched) / float64(len(tokens))
		}
		// Natural-language implementation queries otherwise tend to rank the
		// corresponding *_test file first because tests repeat identifiers and
		// prose. Keep tests fully competitive when the query asks for them, but
		// give production source a small deterministic prior for other intents.
		if !testIntent && !isTestPath(cand.chunk.Path) {
			cand.score += 0.007
		}
	}
}

func hasAnyToken(tokens []string, wanted ...string) bool {
	set := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		set[token] = true
	}
	for _, token := range wanted {
		if set[token] {
			return true
		}
	}
	return false
}

var queryStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "for": true,
	"from": true, "how": true, "in": true, "is": true, "of": true,
	"on": true, "or": true, "the": true, "this": true, "to": true,
	"what": true, "where": true, "which": true, "with": true,
}

var codeQuerySynonyms = map[string][]string{
	"auth":      {"authentication", "authorization"},
	"config":    {"configuration", "settings"},
	"db":        {"database", "store"},
	"delete":    {"remove"},
	"error":     {"failure", "err"},
	"fetch":     {"get", "load"},
	"handler":   {"endpoint", "route"},
	"index":     {"search", "lookup"},
	"save":      {"store", "persist"},
	"test":      {"spec", "assert"},
	"workspace": {"repo", "repository"},
}

func meaningfulQueryTokens(query string) []string {
	all := splitIdent(query)
	seen := map[string]bool{}
	out := make([]string, 0, len(all))
	for _, token := range all {
		if len(token) < 2 || queryStopwords[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func lexicalQuery(query string) string {
	tokens := meaningfulQueryTokens(query)
	seen := map[string]bool{}
	expanded := make([]string, 0, len(tokens)*2)
	add := func(token string) {
		if token == "" || seen[token] || len(expanded) >= 32 {
			return
		}
		seen[token] = true
		expanded = append(expanded, token)
	}
	for _, token := range tokens {
		add(token)
	}
	for _, token := range tokens {
		for _, synonym := range codeQuerySynonyms[token] {
			add(synonym)
		}
	}
	return strings.Join(expanded, " ")
}

func snippetForQuery(chunk store.CodeIndexChunk, query string) CodeSnippet {
	// A terminal newline is a separator, not an extra source line.
	contentForLines := strings.TrimSuffix(chunk.Content, "\n")
	lines := strings.Split(contentForLines, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	tokens := meaningfulQueryTokens(query)
	bestLine, bestMatches := 0, -1
	for i, line := range lines {
		lower := strings.ToLower(line)
		matches := 0
		for _, token := range tokens {
			if strings.Contains(lower, token) {
				matches++
			}
		}
		if matches > bestMatches {
			bestLine, bestMatches = i, matches
		}
	}
	start := bestLine - 6
	if start < 0 {
		start = 0
	}
	end := start + searchSnippetLines
	if end > len(lines) {
		end = len(lines)
		start = end - searchSnippetLines
		if start < 0 {
			start = 0
		}
	}
	content := strings.Join(lines[start:end], "\n")
	content = truncateUTF8(content, searchSnippetBytes)
	// Truncation can remove trailing lines; derive the citation from what is
	// actually returned rather than from the larger source chunk.
	returnedLines := 1 + strings.Count(content, "\n")
	startLine := chunk.StartLine + start
	endLine := startLine + returnedLines - 1
	return CodeSnippet{
		Path: chunk.Path, StartLine: startLine, EndLine: endLine,
		Citation: fmt.Sprintf("%s:%d-%d", chunk.Path, startLine, endLine),
		Kind:     chunk.Kind, SymbolName: chunk.SymbolName, Content: content,
	}
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return strings.TrimRight(s[:end], "\n")
}

func sourceNames(set map[string]bool) []string {
	var out []string
	for _, source := range []string{"lexical", "semantic"} {
		if set[source] {
			out = append(out, source)
		}
	}
	return out
}
