package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEphemeralEncryptor_RoundTrip(t *testing.T) {
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("NewEphemeralEncryptor: %v", err)
	}

	plain := []byte("hello secret world")
	cipher, err := enc.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(cipher, plain) {
		t.Fatal("ciphertext equals plaintext — encryption did not run")
	}
	got, err := enc.Decrypt(cipher)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestEphemeralEncryptor_DistinctCiphertexts(t *testing.T) {
	// Each Encrypt of the same plaintext should produce a distinct
	// ciphertext (age uses a random ephemeral key per encryption).
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("NewEphemeralEncryptor: %v", err)
	}

	plain := []byte("the same input every time")
	c1, _ := enc.Encrypt(plain)
	c2, _ := enc.Encrypt(plain)
	if bytes.Equal(c1, c2) {
		t.Fatal("two encryptions of the same plaintext must produce distinct ciphertexts")
	}
}

func TestEnsureKeyFile_GeneratesAndReuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "age.key")

	enc1, err := EnsureKeyFile(path)
	if err != nil {
		t.Fatalf("EnsureKeyFile (first): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}

	plain := []byte("persisted plaintext")
	cipher, err := enc1.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Reload from the same file; must decrypt the previous ciphertext.
	enc2, err := EnsureKeyFile(path)
	if err != nil {
		t.Fatalf("EnsureKeyFile (reload): %v", err)
	}
	got, err := enc2.Decrypt(cipher)
	if err != nil {
		t.Fatalf("Decrypt with reloaded key: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round-trip after reload failed: got %q want %q", got, plain)
	}
}

func TestEnsureKeyFile_NonExistentParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope", "age.key")
	if _, err := EnsureKeyFile(path); err == nil {
		t.Fatal("expected error when parent dir is missing")
	}
}

func TestNewAgeEncryptor_RejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(path, []byte("not a valid age key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgeEncryptor(path); err == nil {
		t.Fatal("expected error for garbage key file")
	}
}

func TestEphemeralEncryptor_KeysAreIndependent(t *testing.T) {
	a, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("crossed wires")
	cipher, _ := a.Encrypt(plain)
	if _, err := b.Decrypt(cipher); err == nil {
		t.Fatal("decryption with a different ephemeral key must fail")
	}
}
