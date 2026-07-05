package index

import (
	"math"
	"strings"
	"unicode"
)

// splitIdent breaks an identifier or path into lowercase word tokens for FTS.
// It splits on separators (/ . _ - space and other non-alphanumerics) and on
// camelCase / acronym boundaries, so "HandleKVSet" -> [handle kv set] and
// "internal/api/foo_bar.go" -> [internal api foo bar go]. Order is preserved
// and adjacent duplicates collapsed; the result feeds the *_tokens FTS columns
// so a query like "kv set" matches "HandleKVSet".
func splitIdent(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	var last string
	for _, f := range fields {
		for _, w := range splitCamel(f) {
			lw := strings.ToLower(w)
			if lw == "" || lw == last {
				continue
			}
			out = append(out, lw)
			last = lw
		}
	}
	return out
}

// tokenString is splitIdent joined into the single space-delimited string the
// store persists in a *_tokens column.
func tokenString(s string) string {
	return strings.Join(splitIdent(s), " ")
}

// splitCamel splits one alphanumeric run on camelCase and acronym boundaries.
// A boundary is a lower/digit -> upper transition, or an acronym tail where an
// uppercase run is followed by an uppercase+lowercase pair ("HTTPServer" ->
// [HTTP Server]).
func splitCamel(s string) []string {
	runes := []rune(s)
	if len(runes) < 2 {
		return []string{s}
	}
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		boundary := false
		if (unicode.IsLower(prev) || unicode.IsNumber(prev)) && unicode.IsUpper(cur) {
			boundary = true
		} else if unicode.IsUpper(prev) && unicode.IsUpper(cur) &&
			i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
			boundary = true
		}
		if boundary {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	return append(words, string(runes[start:]))
}

// estimateTokens approximates the LLM token count of a rendered string. Code
// tokenizes denser than English, so ~3.5 chars/token (P5).
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return int(math.Ceil(float64(len(s)) / 3.5))
}
