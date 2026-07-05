package index

import (
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// Extraction is one file's language-specific parse result, later folded into a
// store.IndexedFile by build.go (which fills WorkspaceID / FileID / *_tokens
// and resolves imports into edges). Symbols carry only the fields an extractor
// can know from the file alone.
type Extraction struct {
	Language   string
	Package    string
	DocSummary string
	LineCount  int
	Symbols    []store.CodeIndexSymbol
	Imports    []string // raw import specifiers, resolved to edges in build.go
	ParseError string   // non-empty when the parse degraded (build appends a warning)
}

// languageForPath maps a file extension to the indexer's language label. The
// empty string means "record the file row but extract no symbols".
func languageForPath(rel string) string {
	switch {
	case strings.HasSuffix(rel, ".go"):
		return "go"
	case strings.HasSuffix(rel, ".ts"), strings.HasSuffix(rel, ".tsx"):
		return "typescript"
	case strings.HasSuffix(rel, ".js"), strings.HasSuffix(rel, ".jsx"),
		strings.HasSuffix(rel, ".mjs"), strings.HasSuffix(rel, ".cjs"):
		return "javascript"
	default:
		return ""
	}
}

// extractFile dispatches to the per-language extractor. Unknown languages yield
// a file-only Extraction (line count, no symbols).
func extractFile(rel string, src []byte) *Extraction {
	switch languageForPath(rel) {
	case "go":
		return extractGo(rel, src)
	case "typescript":
		return extractTS(rel, src, "typescript")
	case "javascript":
		return extractTS(rel, src, "javascript")
	default:
		return &Extraction{Language: "", LineCount: countLines(src)}
	}
}

// countLines returns the number of source lines (0 for empty input).
func countLines(src []byte) int {
	if len(src) == 0 {
		return 0
	}
	n := strings.Count(string(src), "\n")
	if !strings.HasSuffix(string(src), "\n") {
		n++
	}
	return n
}

var wsCollapse = regexp.MustCompile(`\s+`)

// collapseWS trims and collapses runs of whitespace to a single space, then
// caps the result at max runes (0 = no cap).
func collapseWS(s string, max int) string {
	s = strings.TrimSpace(wsCollapse.ReplaceAllString(s, " "))
	if max > 0 {
		if r := []rune(s); len(r) > max {
			return strings.TrimSpace(string(r[:max]))
		}
	}
	return s
}

// firstSentence returns the first sentence (up to ". ", "\n\n", or end) of a
// doc comment, whitespace-collapsed and capped at max runes.
func firstSentence(doc string, max int) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	if i := strings.Index(doc, "\n\n"); i >= 0 {
		doc = doc[:i]
	}
	if i := strings.Index(doc, ". "); i >= 0 {
		doc = doc[:i+1]
	}
	return collapseWS(doc, max)
}
