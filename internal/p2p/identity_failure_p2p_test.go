//go:build p2p

package p2p

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubEncryptor is a fake Encryptor whose Decrypt always returns a fixed
// plaintext (and never errors). It lets a test drive the "decryptable but
// the plaintext is NOT a valid libp2p key" path of loadOrCreateEncryptedIdentity
// without needing real age ciphertext.
type stubEncryptor struct {
	plain []byte
}

func (s stubEncryptor) Encrypt(p []byte) ([]byte, error) { return p, nil }
func (s stubEncryptor) Decrypt(_ []byte) ([]byte, error) { return s.plain, nil }

// TestLoadIdentityEncryptedFailurePaths is the regression test for the
// load-bearing "don't silently rotate my stable peer identity" guarantee.
// A damaged on-disk key MUST fail loudly with a wrapped error — never
// silently generate a fresh key, which would change the host's peer ID and
// break every existing pairing. Two failure modes are covered:
//
//  1. A corrupt/garbage .age file that the Encryptor cannot decrypt → the
//     wrapped "decrypt identity" error.
//  2. An .age file that decrypts cleanly but whose plaintext is not a valid
//     libp2p protobuf key → the wrapped "unmarshal identity" error.
//
// In BOTH cases loadIdentity must return a non-nil error and must NOT have
// produced a fresh, different key.
func TestLoadIdentityEncryptedFailurePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// enc returns the Encryptor to use; a nil enc field on the result
		// means "use the real ephemeral age encryptor".
		enc        func(t *testing.T) Encryptor
		fileBytes  []byte
		wantErrSub string
	}{
		{
			name: "corrupt age file fails to decrypt",
			// Real age encryptor: it cannot decrypt arbitrary garbage, so
			// the decrypt step errors.
			enc:        func(t *testing.T) Encryptor { return newTestEncryptor(t) },
			fileBytes:  []byte("this is not a valid age ciphertext blob"),
			wantErrSub: "decrypt identity",
		},
		{
			name: "decryptable but non-libp2p plaintext fails to unmarshal",
			// Stub encryptor: Decrypt succeeds and yields bytes that are not
			// a valid libp2p protobuf private key, so UnmarshalPrivateKey
			// errors at the unmarshal step.
			enc:        func(_ *testing.T) Encryptor { return stubEncryptor{plain: []byte("garbage-not-a-key")} },
			fileBytes:  []byte("ciphertext-content-irrelevant-to-stub"),
			wantErrSub: "unmarshal identity",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "identity.key")
			agePath := keyPath + ".age"
			if err := os.WriteFile(agePath, tt.fileBytes, 0o600); err != nil {
				t.Fatalf("write bogus .age file: %v", err)
			}

			key, err := loadIdentity(keyPath, tt.enc(t))
			if err == nil {
				t.Fatalf("loadIdentity returned nil error for %s — a damaged key must fail loudly, not silently rotate the peer ID (got key=%v)", tt.name, key != nil)
			}
			if key != nil {
				t.Fatalf("loadIdentity returned a non-nil key alongside an error — must never fabricate a fresh identity on a damaged file")
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErrSub)
			}

			// The file must be untouched: loadIdentity must NOT have
			// overwritten the damaged key with a freshly generated one.
			got, rerr := os.ReadFile(agePath)
			if rerr != nil {
				t.Fatalf("re-read .age file: %v", rerr)
			}
			if string(got) != string(tt.fileBytes) {
				t.Fatalf("loadIdentity rewrote the .age file on failure — a damaged key was silently replaced (data-loss-equivalent peer ID rotation)")
			}

			// And no cleartext fallback key should have been created either.
			if _, serr := os.Stat(keyPath); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("unexpected cleartext key created at %s (err=%v)", keyPath, serr)
			}
		})
	}
}
