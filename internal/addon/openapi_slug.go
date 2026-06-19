package addon

import (
	"regexp"
	"strings"
)

var (
	nonAlnumRE  = regexp.MustCompile(`[^a-z0-9]+`)
	leadDigitRE = regexp.MustCompile(`^[0-9]`)
)

// slugifyName lowercases input, replaces non-alnum runs with underscores, and
// trims leading/trailing underscores. Result conforms to ^[a-z][a-z0-9_]{1,62}$.
// Returns "" if the input has no usable characters.
func slugifyName(in string) string {
	s := nonAlnumRE.ReplaceAllString(strings.ToLower(in), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return ""
	}
	if leadDigitRE.MatchString(s) {
		s = "x_" + s
	}
	if len(s) > 62 {
		s = s[:62]
	}
	return s
}

// slugifyParam keeps the same rules but is more permissive — params that fail
// validation will be flagged later by AddonSpec.Validate().
func slugifyParam(in string) string {
	out := slugifyName(in)
	if out == "" {
		return in
	}
	return out
}

// firstNonEmpty returns the first non-blank string in vals, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// truncate caps s at n characters, appending "..." if it was shortened.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
