package collectors

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/don-works/mcplexer/internal/store"
)

var (
	claudeANSI     = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	claudeOSC      = regexp.MustCompile(`\x1b\][^\x07]*(?:\x07|\x1b\\)`)
	claudePercent  = regexp.MustCompile(`(\d+)%`)
	claudeResetRE  = regexp.MustCompile(`(?i)^Resets\s+(.+?)\s+\(([^)]+)\)\s*$`)
	claudeDateTime = regexp.MustCompile(`(?i)^([A-Za-z]{3})\s+(\d{1,2})\s+at\s+(\d{1,2}(?::\d{2})?\s*(?:am|pm))$`)
	claudeTimeOnly = regexp.MustCompile(`(?i)^(\d{1,2}(?::\d{2})?\s*(?:am|pm))$`)
)

type claudeParsed struct {
	windows []store.UsageWindow
	plan    string
	errors  []string
}

func parseClaudeSubscriptionPlan(output []byte) string {
	var status struct {
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(output, &status) != nil {
		return ""
	}
	plan := strings.ToLower(strings.TrimSpace(status.SubscriptionType))
	switch plan {
	case "max":
		return "Max"
	case "pro":
		return "Pro"
	case "team":
		return "Team"
	case "enterprise":
		return "Enterprise"
	default:
		return ""
	}
}

func parseClaudeUsageOutput(output []byte, now time.Time) claudeParsed {
	lines := claudeUsageLines(string(output))
	windows := make([]store.UsageWindow, 0, 4)
	for index, line := range lines {
		if !isClaudeWindowLabel(line) {
			continue
		}
		if window, ok := claudeWindowFromLines(lines, index, now); ok {
			windows = append(windows, window)
		}
	}
	return claudeParsed{windows: dedupeClaudeWindows(windows)}
}

func claudeUsageLines(raw string) []string {
	raw = claudeOSC.ReplaceAllString(raw, "")
	raw = claudeANSI.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r", "")
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = normalizeClaudeWindowLabel(claudeStripControls(line))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func normalizeClaudeWindowLabel(line string) string {
	lower := strings.ToLower(line)
	for _, label := range []string{"current session", "current week"} {
		if index := strings.Index(lower, label); index >= 0 {
			return strings.TrimSpace(line[index:])
		}
	}
	return line
}

func claudeStripControls(line string) string {
	var b strings.Builder
	for _, r := range line {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func isClaudeWindowLabel(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "current session") ||
		strings.HasPrefix(lower, "current week")
}

func claudeWindowFromLines(lines []string, index int, now time.Time) (store.UsageWindow, bool) {
	label := lines[index]
	window := store.UsageWindow{
		ID: identifier("claude", label), Label: label, Unit: store.UnitPercent,
		DurationMinutes: claudeWindowMinutes(label),
	}
	limit := index + 6
	if limit > len(lines) {
		limit = len(lines)
	}
	for _, line := range lines[index+1 : limit] {
		if isClaudeWindowLabel(line) {
			break
		}
		if window.UsedPercent == nil {
			if percent := claudeParsePercent(line); percent != nil {
				window.UsedPercent = percent
			}
		}
		if window.ResetsAt == nil {
			if reset := claudeParseResetLine(line, now); reset != nil {
				window.ResetsAt = reset
			}
		}
	}
	return window, window.UsedPercent != nil || window.ResetsAt != nil
}

func claudeWindowMinutes(label string) int {
	lower := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.HasPrefix(lower, "current session"):
		return 5 * 60
	case strings.HasPrefix(lower, "current week"):
		return 7 * 24 * 60
	default:
		return 0
	}
}

func claudeParsePercent(line string) *float64 {
	match := claudePercent.FindStringSubmatch(line)
	if len(match) < 2 {
		return nil
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	return numberPtr(value)
}

func claudeParseResetLine(line string, now time.Time) *time.Time {
	match := claudeResetRE.FindStringSubmatch(strings.TrimSpace(line))
	if len(match) < 3 {
		return nil
	}
	return parseClaudeReset(match[1], match[2], now)
}

func parseClaudeReset(raw, tzName string, now time.Time) *time.Time {
	loc, err := time.LoadLocation(strings.TrimSpace(tzName))
	if err != nil {
		loc = time.UTC
	}
	localNow := now.In(loc)
	raw = strings.TrimSpace(raw)
	if reset := claudeParseDateTimeReset(raw, localNow, loc); reset != nil {
		return reset
	}
	return claudeParseTimeOnlyReset(raw, localNow, loc)
}

func claudeParseDateTimeReset(raw string, localNow time.Time, loc *time.Location) *time.Time {
	match := claudeDateTime.FindStringSubmatch(strings.TrimSpace(raw))
	if len(match) < 4 {
		return nil
	}
	when, err := claudeParseClock(strings.TrimSpace(match[3]))
	if err != nil {
		return nil
	}
	month, err := claudeParseMonth(match[1])
	if err != nil {
		return nil
	}
	day, err := strconv.Atoi(match[2])
	if err != nil || day < 1 || day > 31 {
		return nil
	}
	candidate := time.Date(localNow.Year(), month, day, when.Hour(), when.Minute(), 0, 0, loc)
	if candidate.Before(localNow) {
		candidate = candidate.AddDate(1, 0, 0)
	}
	utc := candidate.UTC()
	return &utc
}

func claudeParseTimeOnlyReset(raw string, localNow time.Time, loc *time.Location) *time.Time {
	match := claudeTimeOnly.FindStringSubmatch(strings.TrimSpace(raw))
	if len(match) < 2 {
		return nil
	}
	when, err := claudeParseClock(strings.TrimSpace(match[1]))
	if err != nil {
		return nil
	}
	candidate := time.Date(
		localNow.Year(), localNow.Month(), localNow.Day(),
		when.Hour(), when.Minute(), 0, 0, loc,
	)
	if !candidate.After(localNow) {
		candidate = candidate.Add(24 * time.Hour)
	}
	utc := candidate.UTC()
	return &utc
}

func claudeParseClock(raw string) (time.Time, error) {
	clean := strings.TrimSpace(raw)
	for _, layout := range []string{"3:04pm", "3pm", "15:04"} {
		if parsed, err := time.Parse(layout, clean); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized clock")
}

func claudeParseMonth(raw string) (time.Month, error) {
	table := map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
		"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
		"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
	}
	key := strings.ToLower(strings.TrimSpace(raw))
	if len(key) > 3 {
		key = key[:3]
	}
	month, ok := table[key]
	if !ok {
		return 0, fmt.Errorf("unrecognized month")
	}
	return month, nil
}

func dedupeClaudeWindows(windows []store.UsageWindow) []store.UsageWindow {
	result := make([]store.UsageWindow, 0, len(windows))
	positions := make(map[string]int, len(windows))
	for _, window := range windows {
		if index, ok := positions[window.ID]; ok {
			result[index] = window
			continue
		}
		positions[window.ID] = len(result)
		result = append(result, window)
	}
	return result
}
