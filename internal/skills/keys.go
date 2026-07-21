// Package skills — keypair management for the M2.4 signing scheme.
//
// Private keys are persisted in upstream-minisign-compatible format
// (encrypted with the same scrypt-derived stream cipher used by the
// minisign CLI), so a developer can swap between mcplexer and the
// upstream `minisign` tool without re-keying. See ADR 0002 for context.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aead.dev/minisign"
)

// PubKeyPrefix is the human-readable prefix surfaced in the mcplexer UI.
// The verifier strips it before parsing, so a vanilla `minisign -G` key
// works as-is. See ADR 0002 § "Pubkey representation".
const PubKeyPrefix = "mcskill:"

// Sentinel errors for keypair management.
var (
	// ErrInvalidPassphrase indicates the passphrase used to decrypt a private
	// key file did not produce a valid plaintext (wrong password or corrupt
	// keyfile).
	ErrInvalidPassphrase = errors.New("invalid passphrase or corrupt keyfile")

	// ErrEmptyPassphrase indicates a caller tried to save a key without a
	// passphrase. mcplexer always encrypts at rest — this is intentional.
	ErrEmptyPassphrase = errors.New("passphrase required (private keys are always encrypted at rest)")
)

// GenerateKeypair creates a fresh Ed25519 minisign keypair. The returned
// pointers are owned by the caller — the private key is unencrypted in
// memory. Persist it via SavePrivateKey to encrypt at rest.
func GenerateKeypair(passphrase string) (*minisign.PublicKey, *minisign.PrivateKey, error) {
	if passphrase == "" {
		return nil, nil, ErrEmptyPassphrase
	}
	pub, priv, err := minisign.GenerateKey(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("generate keypair: %w", err)
	}
	return &pub, &priv, nil
}

// SavePrivateKey writes priv to path encrypted with passphrase, in the
// upstream minisign keyfile format (scrypt + XOR-stream + BLAKE2b tag).
// Existing files are overwritten.
//
// Mode 0600 is enforced on the resulting file. Parent directories are
// created on demand with mode 0700.
func SavePrivateKey(path, passphrase string, priv *minisign.PrivateKey) error {
	if priv == nil {
		return errors.New("save private key: nil key")
	}
	if passphrase == "" {
		return ErrEmptyPassphrase
	}
	encoded, err := minisign.EncryptKey(passphrase, *priv)
	if err != nil {
		return fmt.Errorf("encrypt key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// LoadPrivateKey reads and decrypts a minisign-format keyfile at path.
// A wrong passphrase or tampered file surfaces as ErrInvalidPassphrase.
func LoadPrivateKey(path, passphrase string) (*minisign.PrivateKey, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	priv, err := minisign.PrivateKeyFromFile(passphrase, path)
	if err != nil {
		// minisign returns its package-private errDecrypt for both wrong-
		// password and tampered-file cases. Map both to ErrInvalidPassphrase
		// so callers can react with a single sentinel.
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load private key: %w", err)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidPassphrase, err)
	}
	return &priv, nil
}

// FormatPublicKey returns the canonical 56-char `RWR…` minisign string for
// pk. This is the form intended for copy-paste into chat, READMEs, or the
// trust-store CLI. The optional `mcskill:` prefix used in the UI is added
// by FormatPublicKeyWithPrefix.
func FormatPublicKey(pk *minisign.PublicKey) string {
	if pk == nil {
		return ""
	}
	return pk.String()
}

// FormatPublicKeyWithPrefix returns the UI-facing form `mcskill:RWR…`.
func FormatPublicKeyWithPrefix(pk *minisign.PublicKey) string {
	if pk == nil {
		return ""
	}
	return PubKeyPrefix + pk.String()
}

// ParsePublicKey parses a public key in either canonical minisign form
// (56-char base64), the mcplexer-prefixed form `mcskill:…`, or the multi-
// line "untrusted comment:" form produced by `minisign -G`.
func ParsePublicKey(s string) (*minisign.PublicKey, error) {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, PubKeyPrefix)
	if t == "" {
		return nil, errors.New("parse public key: empty input")
	}
	var pk minisign.PublicKey
	if err := pk.UnmarshalText([]byte(t)); err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return &pk, nil
}

// PublicKeyID returns the 16-char uppercase hex form of pk's 64-bit key id.
// This is the short reference used in commit messages, dialogues, and the
// trusted_signers table primary key.
func PublicKeyID(pk *minisign.PublicKey) string {
	if pk == nil {
		return ""
	}
	return fmt.Sprintf("%016X", pk.ID())
}
