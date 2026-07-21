package compact

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultClusterMaxItems       = 500
	defaultClusterMaxGroups      = 100
	defaultClusterMaxExamples    = 2
	defaultClusterMaxTextBytes   = 2048
	defaultClusterMaxExampleText = 240
	defaultClusterMinTokens      = 2
	defaultJaccardThreshold      = 0.82
	defaultContainmentThreshold  = 0.92
)

// LexicalItem is the minimal shape needed to dedupe a search/list result.
// Text is used for clustering; Label and ID are carried only as bounded
// examples for later explicit get/hydrate calls.
type LexicalItem struct {
	ID    string
	Label string
	Text  string
}

// LexicalExample is a bounded representative returned for a group.
type LexicalExample struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label,omitempty"`
	Text  string `json:"text,omitempty"`
}

// LexicalGroup is one clustered display row. Count is the total number of
// represented items in the group; Examples is capped by MaxExamples; Omitted is
// Count - len(Examples).
type LexicalGroup struct {
	Key      string           `json:"key"`
	Count    int              `json:"count"`
	Omitted  int              `json:"omitted,omitempty"`
	Examples []LexicalExample `json:"examples"`
}

// LexicalClusterResult is the compact grouped result. Groups are ordered by
// first occurrence so callers can preserve relevance/list ordering.
type LexicalClusterResult struct {
	Total     int            `json:"total"`
	Clustered int            `json:"clustered"`
	Truncated int            `json:"truncated,omitempty"`
	Groups    []LexicalGroup `json:"groups"`
}

// LexicalClusterOptions bounds lexical dedupe work and output size.
type LexicalClusterOptions struct {
	// MinClusterSize controls the minimum count that contributes to Clustered.
	// Groups below it are still returned as count=1 display rows.
	MinClusterSize int
	MaxItems       int
	MaxGroups      int
	MaxExamples    int
	MaxTextBytes   int
	MaxExampleText int
	MinTokens      int

	// SimilarityThreshold is a token Jaccard threshold. ContainmentThreshold
	// catches cases where one phrase is a short subset of a longer repeat.
	SimilarityThreshold  float64
	ContainmentThreshold float64
}

// DefaultLexicalClusterOptions returns conservative caps for list/search feeds.
func DefaultLexicalClusterOptions() LexicalClusterOptions {
	return LexicalClusterOptions{
		MinClusterSize:       2,
		MaxItems:             defaultClusterMaxItems,
		MaxGroups:            defaultClusterMaxGroups,
		MaxExamples:          defaultClusterMaxExamples,
		MaxTextBytes:         defaultClusterMaxTextBytes,
		MaxExampleText:       defaultClusterMaxExampleText,
		MinTokens:            defaultClusterMinTokens,
		SimilarityThreshold:  defaultJaccardThreshold,
		ContainmentThreshold: defaultContainmentThreshold,
	}
}

// ClusterLexical groups near-duplicate items using bounded lexical matching.
// It removes volatile identifier/timestamp-like tokens, compares token sets,
// and returns only examples plus counts. Hidden duplicate payloads are not
// returned; callers should expose explicit get/hydrate tools for expansion.
func ClusterLexical(items []LexicalItem, opts LexicalClusterOptions) LexicalClusterResult {
	opts = normalizeClusterOptions(opts)
	total := len(items)
	if len(items) > opts.MaxItems {
		items = items[:opts.MaxItems]
	}

	groups := make([]lexicalWorkGroup, 0, min(len(items), opts.MaxGroups))
	exact := make(map[string]int)
	truncated := total - len(items)

	for i, item := range items {
		tokens := lexicalTokenSet(item.Text, opts.MaxTextBytes)
		if len(tokens) == 0 && item.Label != "" {
			tokens = lexicalTokenSet(item.Label, opts.MaxTextBytes)
		}
		key := lexicalKey(tokens)
		if key == "" {
			key = "unkeyed:" + strconv.Itoa(i)
		}

		if idx, ok := exact[key]; ok {
			addLexicalItem(&groups[idx], item, opts)
			continue
		}

		if idx := findSimilarGroup(groups, tokens, opts); idx >= 0 {
			addLexicalItem(&groups[idx], item, opts)
			continue
		}

		if len(groups) >= opts.MaxGroups {
			truncated++
			continue
		}
		groups = append(groups, newLexicalGroup(key, tokens, item, opts))
		exact[key] = len(groups) - 1
	}

	out := LexicalClusterResult{
		Total:     total,
		Truncated: truncated,
		Groups:    make([]LexicalGroup, 0, len(groups)),
	}
	for _, g := range groups {
		if g.count >= opts.MinClusterSize {
			out.Clustered += g.count
		}
		out.Groups = append(out.Groups, LexicalGroup{
			Key:      g.key,
			Count:    g.count,
			Omitted:  max(g.count-len(g.examples), 0),
			Examples: g.examples,
		})
	}
	return out
}

type lexicalWorkGroup struct {
	key      string
	tokens   []string
	count    int
	examples []LexicalExample
}

func newLexicalGroup(
	key string, tokens []string, item LexicalItem, opts LexicalClusterOptions,
) lexicalWorkGroup {
	g := lexicalWorkGroup{key: key, tokens: tokens}
	addLexicalItem(&g, item, opts)
	return g
}

func addLexicalItem(g *lexicalWorkGroup, item LexicalItem, opts LexicalClusterOptions) {
	g.count++
	if len(g.examples) >= opts.MaxExamples {
		return
	}
	g.examples = append(g.examples, LexicalExample{
		ID:    item.ID,
		Label: item.Label,
		Text:  truncateClusterText(item.Text, opts.MaxExampleText),
	})
}

func findSimilarGroup(groups []lexicalWorkGroup, tokens []string, opts LexicalClusterOptions) int {
	if len(tokens) < opts.MinTokens {
		return -1
	}
	for i, g := range groups {
		if len(g.tokens) < opts.MinTokens {
			continue
		}
		if lexicalSetsSimilar(tokens, g.tokens, opts) {
			return i
		}
	}
	return -1
}

func lexicalSetsSimilar(a, b []string, opts LexicalClusterOptions) bool {
	common := lexicalCommonCount(a, b)
	if common < opts.MinTokens {
		return false
	}
	union := len(a) + len(b) - common
	if union > 0 && float64(common)/float64(union) >= opts.SimilarityThreshold {
		return true
	}
	shorter := min(len(a), len(b))
	return shorter > 0 && float64(common)/float64(shorter) >= opts.ContainmentThreshold
}

func lexicalCommonCount(a, b []string) int {
	i, j, n := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch strings.Compare(a[i], b[j]) {
		case 0:
			n++
			i++
			j++
		case -1:
			i++
		default:
			j++
		}
	}
	return n
}

func lexicalTokenSet(text string, maxBytes int) []string {
	if maxBytes > 0 && len(text) > maxBytes {
		text = text[:maxBytes]
	}
	seen := make(map[string]struct{})
	var tokens []string
	var b strings.Builder

	flush := func() {
		if b.Len() == 0 {
			return
		}
		tok := normalizeLexicalToken(b.String())
		b.Reset()
		if tok == "" {
			return
		}
		if _, ok := seen[tok]; ok {
			return
		}
		seen[tok] = struct{}{}
		tokens = append(tokens, tok)
	}

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	sort.Strings(tokens)
	return tokens
}

func normalizeLexicalToken(tok string) string {
	if len(tok) < 3 {
		return ""
	}
	if lexicalStopWords[tok] || isVariableLexicalToken(tok) {
		return ""
	}
	if len(tok) > 6 && strings.HasSuffix(tok, "ing") {
		tok = tok[:len(tok)-3]
	} else if len(tok) > 5 && strings.HasSuffix(tok, "ed") {
		tok = tok[:len(tok)-2]
	} else if len(tok) > 5 && strings.HasSuffix(tok, "es") {
		tok = tok[:len(tok)-2]
	} else if len(tok) > 4 &&
		strings.HasSuffix(tok, "s") &&
		!strings.HasSuffix(tok, "ss") &&
		!strings.HasSuffix(tok, "us") {
		tok = tok[:len(tok)-1]
	}
	if len(tok) < 3 || lexicalStopWords[tok] || isVariableLexicalToken(tok) {
		return ""
	}
	return tok
}

func isVariableLexicalToken(tok string) bool {
	if allRunes(tok, unicode.IsDigit) {
		return true
	}
	if len(tok) >= 24 {
		return true
	}
	if len(tok) >= 12 && hasDigit(tok) {
		return true
	}
	if len(tok) >= 8 && isHexToken(tok) {
		return true
	}
	return false
}

func lexicalKey(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " ")
}

func normalizeClusterOptions(opts LexicalClusterOptions) LexicalClusterOptions {
	def := DefaultLexicalClusterOptions()
	if opts.MinClusterSize <= 0 {
		opts.MinClusterSize = def.MinClusterSize
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = def.MaxItems
	}
	if opts.MaxGroups <= 0 {
		opts.MaxGroups = def.MaxGroups
	}
	if opts.MaxExamples <= 0 {
		opts.MaxExamples = def.MaxExamples
	}
	if opts.MaxTextBytes <= 0 {
		opts.MaxTextBytes = def.MaxTextBytes
	}
	if opts.MaxExampleText <= 0 {
		opts.MaxExampleText = def.MaxExampleText
	}
	if opts.MinTokens <= 0 {
		opts.MinTokens = def.MinTokens
	}
	if opts.SimilarityThreshold <= 0 {
		opts.SimilarityThreshold = def.SimilarityThreshold
	}
	if opts.ContainmentThreshold <= 0 {
		opts.ContainmentThreshold = def.ContainmentThreshold
	}
	return opts
}

func truncateClusterText(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 3 {
		return s[:maxBytes]
	}
	return s[:maxBytes-3] + "..."
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func isHexToken(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') &&
			(r < 'a' || r > 'f') &&
			(r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func allRunes(s string, pred func(rune) bool) bool {
	for _, r := range s {
		if !pred(r) {
			return false
		}
	}
	return s != ""
}

var lexicalStopWords = map[string]bool{
	"about": true, "after": true, "again": true, "all": true,
	"also": true, "and": true, "any": true, "are": true,
	"before": true, "been": true, "being": true, "but": true,
	"can": true, "could": true, "did": true, "does": true,
	"for": true, "from": true, "get": true, "had": true,
	"has": true, "have": true, "into": true, "may": true,
	"might": true, "not": true, "now": true, "off": true,
	"one": true, "only": true, "onto": true, "out": true,
	"over": true, "own": true, "per": true, "the": true,
	"then": true, "this": true, "through": true, "too": true,
	"use": true, "was": true, "were": true, "when": true,
	"with": true, "would": true,
}
