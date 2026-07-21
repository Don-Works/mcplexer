// Package clistats parses output from AI CLI stat commands.
package clistats

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	ansiRe       = regexp.MustCompile(`\x1b(?:\][^\x07\x1b]*(?:\x07|\x1b\\)|\[[0-?]*[ -/]*[@-~]|[@-_])`)
	thousandsSep = regexp.MustCompile(`,`)
)

// ModelStats holds parsed per-model stats from CLI output.
type ModelStats struct {
	Model            string
	Requests         int
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
}

// StripANSI removes CSI, OSC, and short ANSI escape sequences.
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ParseNumericSuffix converts values such as 1.5K, 2M, and 1,234 to an integer.
func ParseNumericSuffix(s string) int {
	s = strings.TrimSpace(StripANSI(s))
	if s == "" || s == "-" {
		return 0
	}
	multiplier := 1.0
	upper := strings.ToUpper(s)
	for suffix, multiple := range map[string]float64{
		"B": 1_000_000_000, "M": 1_000_000, "K": 1_000,
	} {
		if strings.HasSuffix(upper, suffix) {
			multiplier = multiple
			s = strings.TrimSpace(upper[:len(upper)-1])
			break
		}
	}
	s = thousandsSep.ReplaceAllString(s, "")
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int(math.Round(f * multiplier))
}

// ParseUSD converts a dollar value to float64.
func ParseUSD(s string) float64 {
	s = strings.TrimSpace(StripANSI(s))
	s = strings.TrimSpace(strings.TrimPrefix(s, "$"))
	s = thousandsSep.ReplaceAllString(s, "")
	if s == "" || s == "-" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// ParseModelStatsTable parses box-drawing model blocks and legacy pipe rows.
func ParseModelStatsTable(lines []string) []ModelStats {
	if hasModelUsageSection(lines) {
		return parseModelBlocks(lines, false)
	}
	if stats := parseLegacyRows(lines); len(stats) > 0 {
		return stats
	}
	return parseModelBlocks(lines, true)
}

func hasModelUsageSection(lines []string) bool {
	for _, line := range lines {
		if boxContent(line) == "MODEL USAGE" {
			return true
		}
	}
	return false
}

func parseModelBlocks(lines []string, inside bool) []ModelStats {
	var results []ModelStats
	var current *ModelStats
	fields := 0
	flush := func() {
		if current != nil && fields > 0 {
			results = append(results, *current)
		}
		current, fields = nil, 0
	}
	for _, line := range lines {
		content := boxContent(line)
		if content == "MODEL USAGE" {
			inside = true
			continue
		}
		if !inside || content == "" {
			continue
		}
		if isBorder(line) {
			flush()
			continue
		}
		if isOtherSection(content) {
			flush()
			break
		}
		label, value, ok := splitMetric(content)
		if ok {
			if current != nil {
				applyMetric(current, label, value)
				fields++
			}
			continue
		}
		flush()
		if validModelName(content) {
			current = &ModelStats{Model: content}
		}
	}
	flush()
	return results
}

func splitMetric(content string) (string, string, bool) {
	labels := []string{"Input Tokens", "Output Tokens", "Cache Read", "Cache Write", "Messages", "Cost"}
	for _, label := range labels {
		if !strings.HasPrefix(content, label) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(content, label))
		return label, value, value != ""
	}
	return "", "", false
}

func applyMetric(stat *ModelStats, label, value string) {
	switch label {
	case "Messages":
		stat.Requests = ParseNumericSuffix(value)
	case "Input Tokens":
		stat.InputTokens = ParseNumericSuffix(value)
	case "Output Tokens":
		stat.OutputTokens = ParseNumericSuffix(value)
	case "Cache Read":
		stat.CacheReadTokens = ParseNumericSuffix(value)
	case "Cache Write":
		stat.CacheWriteTokens = ParseNumericSuffix(value)
	case "Cost":
		stat.CostUSD = ParseUSD(value)
	}
}

func boxContent(line string) string {
	s := strings.TrimSpace(StripANSI(strings.TrimSuffix(line, "\r")))
	s = strings.TrimSpace(strings.TrimPrefix(s, "│"))
	s = strings.TrimSpace(strings.TrimSuffix(s, "│"))
	return s
}

func isBorder(line string) bool {
	s := strings.TrimSpace(StripANSI(line))
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("┌┐└┘├┤┬┴┼─═╭╮╰╯", r) {
			return false
		}
	}
	return true
}

func isOtherSection(content string) bool {
	return content == "TOOL USAGE" || content == "OVERVIEW" || content == "COST & TOKENS"
}

func validModelName(content string) bool {
	if content == "" || strings.EqualFold(content, "model") || strings.EqualFold(content, "total") {
		return false
	}
	for _, r := range content {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func parseLegacyRows(lines []string) []ModelStats {
	var results []ModelStats
	for _, line := range lines {
		cols := strings.Split(StripANSI(line), "|")
		if len(cols) < 5 {
			continue
		}
		model := strings.TrimSpace(cols[0])
		if !validModelName(model) || model == "Model" || model == "TOTAL" {
			continue
		}
		results = append(results, ModelStats{
			Model: model, Requests: ParseNumericSuffix(cols[1]),
			InputTokens: ParseNumericSuffix(cols[2]), OutputTokens: ParseNumericSuffix(cols[3]),
			CostUSD: ParseUSD(cols[4]),
		})
	}
	return results
}
