// Package distill is the Monitoring token-cost engine: it collapses
// raw log lines into masked templates (dedup unit), classifies
// severity, detects novelty, and renders budget-bounded digests. All
// deterministic — no model sees a byte until the digest.
package distill

import (
	"fmt"
	"regexp"
	"strings"
)

// MaskedValue records one volatile value removed from a line. Field is a
// nearby structured key/prefix when one is available (orderNum, request_id,
// SO), otherwise the mask class. Digests use this for bounded cardinality
// evidence; values have already passed collector redaction.
type MaskedValue struct {
	Field string
	Value string
}

// Masking rules, applied in order. Each collapses a volatile atom so
// lines differing only in ids/numbers/addresses share one template.
var maskRules = []struct {
	name string
	re   *regexp.Regexp
	repl string
}{
	// ANSI escapes first so colour codes never split templates.
	{"", regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`), ""},
	// In-line timestamps (RFC3339 / ISO-ish / clock times).
	{"timestamp", regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`), "<ts>"},
	{"timestamp", regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(\.\d+)?\b`), "<ts>"},
	// UUIDs before generic hex.
	{"uuid", regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
	// Long hex runs (hashes, ids, addresses).
	{"hex", regexp.MustCompile(`\b(0x)?[0-9a-fA-F]{8,}\b`), "<hex>"},
	// IPv4 (optionally with port).
	{"ip", regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?\b`), "<ip>"},
	// Human/business identifiers such as SO-12345. Capture the whole value
	// under its structured field name so cardinality evidence keeps the useful
	// discriminator while the template still masks the varying suffix.
	{"identifier", regexp.MustCompile(`(?i)\b([a-z]{2,10})-\d+\b`), "$1-<n>"},
	// Durations: 123ms, 4.5s, 2m30s, 1h.
	{"duration", regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	// Long quoted payloads.
	{"quoted", regexp.MustCompile(`"[^"]{25,}"`), `"<...>"`},
	// All integers (after everything above claimed its share) — db-3
	// and db-4 are the same failure shape.
	{"integer", regexp.MustCompile(`\b\d+\b`), "<n>"},
}

var spaceRe = regexp.MustCompile(`\s+`)
var fieldBeforeValueRe = regexp.MustCompile(`(?i)([a-z_][a-z0-9_.-]*)\s*[=:]\s*["']?$`)
var prefixBeforeValueRe = regexp.MustCompile(`(?i)([a-z][a-z0-9_]*)[-:/]$`)

// Taxonomy tokens identify the class of failure and must survive masking.
// Values vary per occurrence; constraint/index/schema names, SQLSTATE/error
// codes, and exception types are the dimensions operators group by.
var taxonomyRules = []*regexp.Regexp{
	// Code locations are the monitor's strongest deterministic correlation
	// key. A line number is an identifier here, not a varying numeric value.
	regexp.MustCompile(`\b[A-Za-z0-9_./-]+\.(?:go|rs|py|php|js|jsx|ts|tsx|java|cs|rb|kt|swift|c|cc|cpp|h|hpp):\d+\b`),
	regexp.MustCompile(`(?i)\b(?:constraint|index|table|column|relation|schema)\s+(?:"[^"]+"|'[^']+'|[a-z_][a-z0-9_.$:-]*)`),
	regexp.MustCompile(`(?i)\b(?:sqlstate|error[ _-]?code|errno)\s*(?:=|:)?\s*["']?[a-z0-9_.:-]+["']?`),
	regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_.]*(?:Error|Exception)\b`),
}

var unsafeInlineValueRe = regexp.MustCompile(`(?i)^(?:[0-9a-f]{8}-[0-9a-f-]{27,}|(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?|(?:0x)?[0-9a-f]{12,})$`)

// Normalize collapses one redacted log line into its template text.
func Normalize(line string) string {
	masked, _ := NormalizeWithValues(line)
	return masked
}

// NormalizeWithValues returns the same stable template as Normalize plus the
// volatile values that were removed. Taxonomy identifiers are protected
// before generic value masking and restored afterwards.
func NormalizeWithValues(line string) (string, []MaskedValue) {
	s, taxonomy := protectTaxonomy(line)
	values := make([]MaskedValue, 0, 4)
	for _, r := range maskRules {
		if r.name != "" {
			values = append(values, maskedValues(s, r.name, r.re)...)
		}
		s = r.re.ReplaceAllString(s, r.repl)
	}
	s = restoreTaxonomy(s, taxonomy)
	s = spaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	const maxTemplateLen = 400
	if len(s) > maxTemplateLen {
		s = s[:maxTemplateLen] + "…"
	}
	return s, values
}

func protectTaxonomy(line string) (string, []string) {
	protected := []string{}
	for _, re := range taxonomyRules {
		line = re.ReplaceAllStringFunc(line, func(match string) string {
			token := fmt.Sprintf("__MCPLEXERTAXONOMY_%s__", alphaToken(len(protected)))
			protected = append(protected, match)
			return token
		})
	}
	return line, protected
}

func restoreTaxonomy(line string, protected []string) string {
	for i, value := range protected {
		line = strings.ReplaceAll(line,
			fmt.Sprintf("__MCPLEXERTAXONOMY_%s__", alphaToken(i)), value)
	}
	return line
}

func alphaToken(n int) string {
	var out [8]byte
	i := len(out)
	for {
		i--
		out[i] = byte('A' + n%26)
		n = n/26 - 1
		if n < 0 {
			return string(out[i:])
		}
	}
}

func maskedValues(line, fallback string, re *regexp.Regexp) []MaskedValue {
	indices := re.FindAllStringIndex(line, -1)
	values := make([]MaskedValue, 0, len(indices))
	seen := map[string]bool{}
	for _, index := range indices {
		value := strings.Trim(line[index[0]:index[1]], `"'`)
		field := fieldBefore(line, index[0], fallback)
		key := field + "\x00" + value
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		values = append(values, MaskedValue{Field: field, Value: value})
	}
	return values
}

func fieldBefore(line string, start int, fallback string) string {
	from := start - 64
	if from < 0 {
		from = 0
	}
	prefix := line[from:start]
	if match := fieldBeforeValueRe.FindStringSubmatch(prefix); match != nil {
		return strings.ToLower(match[1])
	}
	if match := prefixBeforeValueRe.FindStringSubmatch(prefix); match != nil {
		return strings.ToLower(match[1])
	}
	return fallback
}
