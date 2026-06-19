//go:build p2p

package p2p

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// loadIdentity returns a persistent Ed25519 private key.
//
// When enc is non-nil the key is read from / written to keyPath + ".age"
// (encrypted with age). When enc is nil the key is stored in cleartext at
// keyPath. The encrypted form is preferred for production; cleartext is
// retained for tests and for the migration path from the R0.1 spike.
//
// Files are created with 0600; parent directories with 0700.
func loadIdentity(keyPath string, enc Encryptor) (crypto.PrivKey, error) {
	if keyPath == "" {
		return nil, errors.New("p2p: identity key path is empty")
	}
	if enc != nil {
		return loadOrCreateEncryptedIdentity(keyPath+".age", enc)
	}
	return LoadOrCreateIdentity(keyPath)
}

// LoadOrCreateIdentity reads (or creates) a cleartext libp2p identity key.
// Exposed for tests and for callers that explicitly opt in to cleartext
// storage. Production code should use NewHost with an Encryptor.
func LoadOrCreateIdentity(keyPath string) (crypto.PrivKey, error) {
	if keyPath == "" {
		return nil, errors.New("p2p: identity key path is empty")
	}
	if data, err := os.ReadFile(keyPath); err == nil {
		key, err := crypto.UnmarshalPrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("p2p: unmarshal identity %q: %w", keyPath, err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("p2p: read identity %q: %w", keyPath, err)
	}
	return generateAndWriteIdentity(keyPath)
}

// generateAndWriteIdentity creates a new Ed25519 key and writes it to
// keyPath in cleartext.
func generateAndWriteIdentity(keyPath string) (crypto.PrivKey, error) {
	priv, data, err := newEd25519KeyMaterial()
	if err != nil {
		return nil, err
	}
	if err := writeKeyFile(keyPath, data); err != nil {
		return nil, err
	}
	return priv, nil
}

// loadOrCreateEncryptedIdentity reads an age-encrypted libp2p identity key
// from agePath (decrypting on read), creating one if absent.
func loadOrCreateEncryptedIdentity(agePath string, enc Encryptor) (crypto.PrivKey, error) {
	if data, err := os.ReadFile(agePath); err == nil {
		plain, err := enc.Decrypt(data)
		if err != nil {
			return nil, fmt.Errorf("p2p: decrypt identity %q: %w", agePath, err)
		}
		key, err := crypto.UnmarshalPrivateKey(plain)
		if err != nil {
			return nil, fmt.Errorf("p2p: unmarshal identity %q: %w", agePath, err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("p2p: read identity %q: %w", agePath, err)
	}

	priv, plain, err := newEd25519KeyMaterial()
	if err != nil {
		return nil, err
	}
	cipher, err := enc.Encrypt(plain)
	if err != nil {
		return nil, fmt.Errorf("p2p: encrypt identity: %w", err)
	}
	if err := writeKeyFile(agePath, cipher); err != nil {
		return nil, err
	}
	return priv, nil
}

// newEd25519KeyMaterial generates a fresh Ed25519 keypair and returns both
// the parsed PrivKey and its libp2p protobuf-marshaled bytes.
func newEd25519KeyMaterial() (crypto.PrivKey, []byte, error) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("p2p: generate identity: %w", err)
	}
	data, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("p2p: marshal identity: %w", err)
	}
	return priv, data, nil
}

// writeKeyFile writes data to path with 0600, creating parent dirs (0700).
func writeKeyFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("p2p: mkdir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("p2p: write identity %q: %w", path, err)
	}
	return nil
}
