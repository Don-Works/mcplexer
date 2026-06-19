package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// LoadOrCreateTransferKey returns the persistent age X25519 identity used
// for peer→peer secret transfer. The key lives at keyPath, encrypted by
// master (the daemon's master age encryptor — same one that wraps the
// libp2p identity and mcplexer.db). Generated on first call.
//
// The transfer key is deliberately separate from any other age key on the
// daemon: rotating it later (e.g. on a compromise) does NOT invalidate the
// at-rest secret store or the libp2p peer identity.
func LoadOrCreateTransferKey(keyPath string, master *AgeEncryptor) (*age.X25519Identity, error) {
	if keyPath == "" {
		return nil, errors.New("secrets: transfer key path is empty")
	}
	if master == nil {
		return nil, errors.New("secrets: master encryptor required")
	}
	if cipher, err := os.ReadFile(keyPath); err == nil {
		plain, err := master.Decrypt(cipher)
		if err != nil {
			return nil, fmt.Errorf("secrets: decrypt transfer key: %w", err)
		}
		return parseX25519Identity(string(plain))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("secrets: read transfer key: %w", err)
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("secrets: generate transfer key: %w", err)
	}
	cipher, err := master.Encrypt([]byte(id.String()))
	if err != nil {
		return nil, fmt.Errorf("secrets: encrypt transfer key: %w", err)
	}
	if dir := filepath.Dir(keyPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secrets: mkdir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(keyPath, cipher, 0o600); err != nil {
		return nil, fmt.Errorf("secrets: write transfer key: %w", err)
	}
	return id, nil
}

func parseX25519Identity(s string) (*age.X25519Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(s))
	if err != nil {
		return nil, fmt.Errorf("secrets: parse transfer key: %w", err)
	}
	if len(ids) == 0 {
		return nil, errors.New("secrets: no identities in transfer key file")
	}
	x, ok := ids[0].(*age.X25519Identity)
	if !ok {
		return nil, errors.New("secrets: transfer key is not X25519")
	}
	return x, nil
}

// EncryptToRecipient encrypts plaintext to the recipient (an age1... string).
// Used by mesh__send_secret to wrap a value for a remote peer. The returned
// ciphertext can be safely persisted at rest and shipped over the mesh.
//
// Size cap (64 KB plaintext) prevents accidental abuse — SSH keys, API
// tokens, age keys all fit comfortably below this.
const MaxSecretPlaintextBytes = 64 * 1024

// EncryptToRecipient writes age ciphertext for the given recipient string.
func EncryptToRecipient(recipientStr string, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("secrets: plaintext is empty")
	}
	if len(plaintext) > MaxSecretPlaintextBytes {
		return nil, fmt.Errorf("secrets: plaintext %d bytes exceeds limit %d", len(plaintext), MaxSecretPlaintextBytes)
	}
	rcpt, err := age.ParseX25519Recipient(strings.TrimSpace(recipientStr))
	if err != nil {
		return nil, fmt.Errorf("secrets: parse recipient: %w", err)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		return nil, fmt.Errorf("secrets: age writer: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("secrets: write plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("secrets: close age writer: %w", err)
	}
	return buf.Bytes(), nil
}

// DecryptWithIdentity reads age ciphertext using the given X25519 identity.
// Returned plaintext is never logged or persisted by callers outside the
// secrets store.
func DecryptWithIdentity(id *age.X25519Identity, ciphertext []byte) ([]byte, error) {
	if id == nil {
		return nil, errors.New("secrets: identity required")
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), id)
	if err != nil {
		return nil, fmt.Errorf("secrets: age reader: %w", err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(r, MaxSecretPlaintextBytes+1))
	if err != nil {
		return nil, fmt.Errorf("secrets: read plaintext: %w", err)
	}
	if len(plaintext) > MaxSecretPlaintextBytes {
		return nil, errors.New("secrets: decrypted plaintext exceeds limit")
	}
	return plaintext, nil
}
