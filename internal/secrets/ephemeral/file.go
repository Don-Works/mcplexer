package ephemeral

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// writeSecretFile generates a 256-bit random filename and atomically writes
// secretBytes to it with 0600 owner-only perms. Returns the absolute path.
// Uses O_EXCL to defend against the (cryptographically negligible) chance of
// a collision producing a file overwrite.
func (m *Manager) writeSecretFile(secretBytes []byte) (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	name := hex.EncodeToString(buf[:])
	path := filepath.Join(m.dir, name)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create secret file: %w", err)
	}
	if _, err := f.Write(secretBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write secret: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close secret file: %w", err)
	}
	return path, nil
}
