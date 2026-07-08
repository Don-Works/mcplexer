// Package distill is the Monitoring token-cost engine: it collapses
// raw log lines into masked templates (dedup unit), classifies
// severity, detects novelty, and renders budget-bounded digests. All
// deterministic — no model sees a byte until the digest.
package distill

import (
	"regexp"
	"strings"
)

// Masking rules, applied in order. Each collapses a volatile atom so
// lines differing only in ids/numbers/addresses share one template.
var maskRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// ANSI escapes first so colour codes never split templates.
	{regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`), ""},
	// In-line timestamps (RFC3339 / ISO-ish / clock times).
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`), "<ts>"},
	{regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(\.\d+)?\b`), "<ts>"},
	// UUIDs before generic hex.
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
	// Long hex runs (hashes, ids, addresses).
	{regexp.MustCompile(`\b(0x)?[0-9a-fA-F]{8,}\b`), "<hex>"},
	// IPv4 (optionally with port).
	{regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?\b`), "<ip>"},
	// Durations: 123ms, 4.5s, 2m30s, 1h.
	{regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	// Long quoted payloads.
	{regexp.MustCompile(`"[^"]{25,}"`), `"<...>"`},
	// All integers (after everything above claimed its share) — db-3
	// and db-4 are the same failure shape.
	{regexp.MustCompile(`\b\d+\b`), "<n>"},
}

var spaceRe = regexp.MustCompile(`\s+`)

// Normalize collapses one redacted log line into its template text.
func Normalize(line string) string {
	s := line
	for _, r := range maskRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	s = spaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	const maxTemplateLen = 400
	if len(s) > maxTemplateLen {
		s = s[:maxTemplateLen] + "…"
	}
	return s
}
