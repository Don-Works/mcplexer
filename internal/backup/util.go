package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// quoteSQLString wraps a string in single quotes and escapes embedded
// single quotes by doubling them. SQLite-safe for VACUUM INTO targets.
func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// validID rejects path traversal / shenanigans. Backup IDs are
// timestamp + 6-char suffix, so a strict charset check is enough.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

func randomID(n int) string {
	// 32-char alphabet → 256%32==0, so byte%32 is unbiased.
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789" // lowercase, no ambiguous chars
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is effectively fatal; return a valid (if
		// non-random) suffix rather than panic. The timestamp prefix on the
		// caller's ID still provides ordering.
		for i := range b {
			b[i] = alphabet[0]
		}
		return string(b)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
