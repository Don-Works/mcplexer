package index

import (
	"encoding/json"
	"sort"
	"strings"
)

// sortStable stably sorts s in place by the given less predicate.
func sortStable[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}

// firstLine returns the first line of s, trimmed — used to keep multi-line
// parse errors to one line in a build warning.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// warningsJSON marshals build warnings for the build row, always yielding a
// valid JSON array ("[]" when empty).
func warningsJSON(warnings []string) string {
	if len(warnings) == 0 {
		return "[]"
	}
	b, err := json.Marshal(warnings)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// parseWarnings unmarshals a build row's WarningsJSON back into a slice,
// returning nil on empty or malformed input.
func parseWarnings(raw string) []string {
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
