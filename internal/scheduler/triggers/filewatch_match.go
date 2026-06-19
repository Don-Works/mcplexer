package triggers

import (
	"path/filepath"
	"strings"
)

// fixedPrefixDir returns the deepest directory of `spec` that contains
// no wildcards. For a bare filename (no globs) returns its parent.
func fixedPrefixDir(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	idx := strings.IndexAny(spec, "*?[")
	if idx < 0 {
		if d := filepath.Dir(spec); d != "" {
			return d
		}
		return "."
	}
	prefix := spec[:idx]
	dir := filepath.Dir(prefix)
	if dir == "" {
		return "."
	}
	return dir
}

// matchingJobs returns every jobID whose spec matches `path`. Supports
// "**" (cross-separator) by collapsing it before delegating to
// filepath.Match. M4 globs are intentionally narrow — extended
// patterns land in a later milestone.
func matchingJobs(specs map[string]string, path string) []string {
	var out []string
	for id, spec := range specs {
		if globMatch(spec, path) {
			out = append(out, id)
		}
	}
	return out
}

// globMatch supports `**` as "anything including separators" by
// flattening it to `*` and matching against the basename-or-full-path
// variants. Conservative on purpose: false positives are tolerable
// here (we run the job; approval gates the real damage), false
// negatives are not.
func globMatch(spec, path string) bool {
	if spec == path {
		return true
	}
	if strings.Contains(spec, "**") {
		return doubleGlobMatch(spec, path)
	}
	if ok, _ := filepath.Match(spec, path); ok {
		return true
	}
	if ok, _ := filepath.Match(spec, filepath.Base(path)); ok {
		return true
	}
	return false
}

// doubleGlobMatch is the `**` variant pulled out so globMatch fits in
// the 50-line cap. Tries the flat match, then the prefix-plus-suffix
// match for specs of the form `<prefix>/**/<suffix>`.
func doubleGlobMatch(spec, path string) bool {
	flat := strings.ReplaceAll(spec, "**", "*")
	if ok, _ := filepath.Match(flat, path); ok {
		return true
	}
	if ok, _ := filepath.Match(flat, filepath.Base(path)); ok {
		return true
	}
	parts := strings.SplitN(spec, "**", 2)
	if len(parts) != 2 {
		return false
	}
	prefix := strings.TrimRight(parts[0], string(filepath.Separator))
	suffix := strings.TrimLeft(parts[1], string(filepath.Separator))
	if prefix != "" && !strings.HasPrefix(path, prefix) {
		return false
	}
	return suffixMatch(suffix, path)
}

// suffixMatch checks whether any tail of `path` satisfies `pattern`.
func suffixMatch(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	parts := strings.Split(path, string(filepath.Separator))
	for i := range parts {
		tail := strings.Join(parts[i:], string(filepath.Separator))
		if ok, _ := filepath.Match(pattern, tail); ok {
			return true
		}
	}
	if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
		return true
	}
	return false
}
