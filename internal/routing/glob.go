package routing

import "strings"

// GlobMatch checks if path matches the glob pattern.
// Supports:
//
//	"*"  — matches any single path segment (no slashes)
//	"**" — matches zero or more path segments (including slashes)
//
// All other characters are matched literally.
func GlobMatch(pattern, path string) bool {
	return globMatch(
		strings.Split(pattern, "/"),
		strings.Split(path, "/"),
	)
}

func globMatch(pat, seg []string) bool {
	for len(pat) > 0 {
		p := pat[0]
		pat = pat[1:]

		if p == "**" {
			// "**" at the end matches everything remaining.
			if len(pat) == 0 {
				return true
			}
			// Try matching the rest of the pattern at every
			// position in the remaining segments.
			for i := 0; i <= len(seg); i++ {
				if globMatch(pat, seg[i:]) {
					return true
				}
			}
			return false
		}

		if len(seg) == 0 {
			return false
		}

		if !segmentMatch(p, seg[0]) {
			return false
		}
		seg = seg[1:]
	}

	return len(seg) == 0
}

// segmentMatch checks if a single segment matches a pattern segment.
// "*" matches any single segment.
func segmentMatch(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == segment
}

// GlobSpecificity returns a score for how specific a glob is.
// Higher = more specific. Non-wildcard segments score more.
func GlobSpecificity(pattern string) int {
	parts := strings.Split(pattern, "/")
	score := 0
	for _, p := range parts {
		switch p {
		case "**":
			// Least specific wildcard, no points.
		case "*":
			score += 1
		default:
			score += 10
		}
	}
	return score
}
