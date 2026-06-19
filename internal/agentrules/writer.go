package agentrules

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// markerRegex matches the BEGIN/END pair non-greedily. (?m) so ^/$
// anchor to line starts/ends; (?s) so . matches newlines (used for the
// body capture). Captures: [1] version int, [2] body between markers.
//
// The body capture includes any leading/trailing whitespace inside the
// markers — we intentionally normalize on rewrite to a single blank
// line on each side. Anything outside the markers is preserved
// byte-for-byte.
var markerRegex = regexp.MustCompile(`(?ms)^<!-- MCPLEXER:BEGIN v(\d+) -->\s*\n(.*?)\n?<!-- MCPLEXER:END -->\s*$`)

// Sync writes / refreshes the marker-bounded block in path. Returns
// changed=true when the file's bytes are different after the call.
//
// Algorithm:
//   - Missing file: create it (0600) containing just the block.
//   - No markers in existing file: append "\n\n" + block.
//   - Markers present (any version): replace the inter-marker body with
//     the new rendered body, leave everything outside untouched.
//
// Idempotency: the body's sha256 is compared to what's already
// installed. When they match AND the BEGIN marker's version is the one
// we're targeting, the function is a no-op and returns changed=false.
// Calling Sync twice with the same version is guaranteed cheap.
func Sync(path string, version int) (changed bool, err error) {
	rendered := Render(version)
	newBody := renderContent(version)
	newHash := sha256.Sum256([]byte(normalizeBody([]byte(newBody))))

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		// File missing: create parent dir + write the block fresh.
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return false, fmt.Errorf("mkdir parent: %w", err)
		}
		if err := writeRulesFile(path, []byte(rendered)); err != nil {
			return false, err
		}
		return true, nil
	}

	match := markerRegex.FindSubmatchIndex(existing)
	if match == nil {
		trimmed := bytes.TrimRight(existing, " \t\n\r")
		var buf bytes.Buffer
		buf.Write(trimmed)
		if len(trimmed) > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(rendered)
		if err := writeRulesFile(path, buf.Bytes()); err != nil {
			return false, err
		}
		return true, nil
	}

	installedVersion := parseVersion(existing[match[2]:match[3]])
	installedBody := existing[match[4]:match[5]]
	installedHash := sha256.Sum256([]byte(normalizeBody(installedBody)))

	if installedVersion == version && installedHash == newHash {
		return false, nil
	}

	replacement := []byte(rendered)
	replacement = bytes.TrimRight(replacement, "\n")

	var buf bytes.Buffer
	buf.Write(existing[:match[0]])
	buf.Write(replacement)
	buf.Write(existing[match[1]:])

	if err := writeRulesFile(path, buf.Bytes()); err != nil {
		return false, err
	}
	return true, nil
}

// ErrMarkersMissing is returned when the file exists but contains no
// MCPLEXER:BEGIN..END markers and force was not set.
var ErrMarkersMissing = errors.New("no mcplexer markers found in file — use --force to append")

// DryRunResult holds the outcome of a dry-run computation.
type DryRunResult struct {
	// WouldChange is true when Sync would write to the file.
	WouldChange bool
	// MarkersFound is true when the existing file contained markers.
	MarkersFound bool
	// OldContent is the existing file content (empty if file missing).
	OldContent string
	// NewContent is what Sync would write (empty if no change).
	NewContent string
}

// DryRun computes what Sync would do without writing anything.
func DryRun(path string, version int) (DryRunResult, error) {
	rendered := Render(version)
	newBody := renderContent(version)
	newHash := sha256.Sum256([]byte(normalizeBody([]byte(newBody))))

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return DryRunResult{}, fmt.Errorf("read %s: %w", path, err)
		}
		// File missing: would create.
		return DryRunResult{
			WouldChange: true,
			OldContent:  "",
			NewContent:  rendered,
		}, nil
	}

	match := markerRegex.FindSubmatchIndex(existing)
	if match == nil {
		// File exists, no markers: would append.
		trimmed := bytes.TrimRight(existing, " \t\n\r")
		var buf bytes.Buffer
		buf.Write(trimmed)
		if len(trimmed) > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(rendered)
		return DryRunResult{
			WouldChange:  true,
			MarkersFound: false,
			OldContent:   string(existing),
			NewContent:   buf.String(),
		}, nil
	}

	installedVersion := parseVersion(existing[match[2]:match[3]])
	installedBody := existing[match[4]:match[5]]
	installedHash := sha256.Sum256([]byte(normalizeBody(installedBody)))

	if installedVersion == version && installedHash == newHash {
		return DryRunResult{
			MarkersFound: true,
			OldContent:   string(existing),
		}, nil
	}

	replacement := []byte(rendered)
	replacement = bytes.TrimRight(replacement, "\n")
	var buf bytes.Buffer
	buf.Write(existing[:match[0]])
	buf.Write(replacement)
	buf.Write(existing[match[1]:])

	return DryRunResult{
		WouldChange:  true,
		MarkersFound: true,
		OldContent:   string(existing),
		NewContent:   buf.String(),
	}, nil
}

func writeRulesFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// parseVersion turns the regex capture "1" / "12" into an int.
// Returns -1 on parse failure (treated as "unknown installed
// version", which forces a rewrite).
func parseVersion(b []byte) int {
	var n int
	for _, c := range b {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// normalizeBody strips a single leading/trailing newline so the hash
// comparison ignores formatting drift from older Sync calls that may
// have emitted slightly different whitespace. Keeps interior newlines
// intact — only the boundary whitespace is normalized.
func normalizeBody(b []byte) string {
	return string(bytes.TrimSpace(b))
}
