// Package clistats parses the output of AI CLI stat commands
// (opencode stats --days N --models, mimo stats --days N --models).
// Output contains ANSI escape codes, thousands separators, and
// K/M/B suffixes that must be stripped before aggregation.
package clistats

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ANSI regex matches ANSI escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// thousandsSep matches comma-separated numbers (e.g. "1,234,567").
var thousandsSep = regexp.MustCompile(`,`)

// ModelStats holds parsed per-model stats from CLI output.
type ModelStats struct {
	Model        string
	Requests     int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// StripANSI removes ANSI escape codes from a string.
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ParseNumericSuffix converts strings like "1.5K", "2M", "1,234" to
// an integer. Handles K (thousands), M (millions), B (billions) suffixes
// and comma-separated thousands.
func ParseNumericSuffix(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}

	// Strip ANSI first.
	s = StripANSI(s)

	// Check for suffix.
	multiplier := 1.0
	upper := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(upper, "B"):
		multiplier = 1_000_000_000
		s = strings.TrimSuffix(upper, "B")
	case strings.HasSuffix(upper, "M"):
		multiplier = 1_000_000
		s = strings.TrimSuffix(upper, "M")
	case strings.HasSuffix(upper, "K"):
		multiplier = 1_000
		s = strings.TrimSuffix(upper, "K")
	}

	// Remove commas.
	s = thousandsSep.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(math.Round(f * multiplier))
}

// ParseUSD converts a dollar string like "$1.23" or "1.23" to float64.
func ParseUSD(s string) float64 {
	s = strings.TrimSpace(s)
	s = StripANSI(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// ParseModelStatsTable parses a table of model stats from CLI output.
// Each row has: Model | Requests | Input Tokens | Output Tokens | Cost
// Returns the parsed stats. Lines are ANSI-stripped before parsing.
func ParseModelStatsTable(lines []string) []ModelStats {
	var results []ModelStats
	for _, line := range lines {
		stripped := StripANSI(line)
		cols := splitTableColumns(stripped)
		if len(cols) < 5 {
			continue
		}
		model := strings.TrimSpace(cols[0])
		if model == "" || model == "Model" || model == "TOTAL" {
			continue
		}
		if strings.HasPrefix(model, "---") || strings.HasPrefix(model, "===") {
			continue
		}
		results = append(results, ModelStats{
			Model:        model,
			Requests:     ParseNumericSuffix(cols[1]),
			InputTokens:  ParseNumericSuffix(cols[2]),
			OutputTokens: ParseNumericSuffix(cols[3]),
			CostUSD:      ParseUSD(cols[4]),
		})
	}
	return results
}

// splitTableColumns splits a table row by | separator.
func splitTableColumns(line string) []string {
	parts := strings.Split(line, "|")
	var out []string
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}
