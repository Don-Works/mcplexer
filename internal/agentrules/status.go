package agentrules

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// Status inspects path and reports whether the marker block is
// installed and up-to-date relative to version (typically
// CurrentVersion). Never mutates the file — safe to call from polling
// dashboards / CLI checks.
//
//   - present=true when a MCPLEXER:BEGIN..END pair is found.
//   - currentVersion reflects the installed BEGIN marker's vN (0 if
//     absent, -1 if malformed).
//   - upToDate=true when present AND the installed body's hash matches
//     the rendered body for version AND installed version == version.
//
// Missing file is not an error — present=false, currentVersion=0,
// upToDate=false, err=nil. Other I/O errors are returned.
func Status(path string, version int) (present bool, currentVersion int, upToDate bool, err error) {
	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return false, 0, false, nil
		}
		return false, 0, false, fmt.Errorf("read %s: %w", path, readErr)
	}

	match := markerRegex.FindSubmatchIndex(existing)
	if match == nil {
		return false, 0, false, nil
	}

	installedVersion := parseVersion(existing[match[2]:match[3]])
	installedBody := existing[match[4]:match[5]]

	wantBody := renderContent(version)
	wantHash := sha256.Sum256([]byte(normalizeBody([]byte(wantBody))))
	gotHash := sha256.Sum256([]byte(normalizeBody(installedBody)))

	return true, installedVersion, installedVersion == version && wantHash == gotHash, nil
}
