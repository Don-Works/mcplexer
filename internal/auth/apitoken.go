package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// APITokenBytes is the length in bytes of a generated API token.
	APITokenBytes = 32

	// APITokenFileMode is the permission mode for the token file (owner-only).
	APITokenFileMode = 0o600
)

// LoadOrCreateAPIToken reads the API token from path, or generates and persists
// a new one if the file does not yet exist. The directory is created if needed.
// Returned tokens are 64-character lowercase hex strings.
//
// The file is written with 0600 permissions; if an existing file has looser
// permissions LoadOrCreateAPIToken tightens them in place.
func LoadOrCreateAPIToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("auth: empty token path")
	}

	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		if !validHexToken(token) {
			return "", fmt.Errorf("auth: token file %s does not contain a valid token", path)
		}
		// Best-effort permission tightening — ignore errors for read-only filesystems.
		_ = os.Chmod(path, APITokenFileMode)
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("auth: read token: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth: create token dir: %w", err)
	}

	buf := make([]byte, APITokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read random: %w", err)
	}
	token := hex.EncodeToString(buf)

	if err := os.WriteFile(path, []byte(token+"\n"), APITokenFileMode); err != nil {
		return "", fmt.Errorf("auth: write token: %w", err)
	}

	return token, nil
}

// DefaultAPITokenPath returns the conventional location for the API token file.
// It defers to the caller's home dir lookup; an empty home returns "".
func DefaultAPITokenPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".mcplexer", "api-key")
}

func validHexToken(s string) bool {
	if len(s) < 32 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
