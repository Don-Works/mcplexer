package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"
)

// helperKeypair generates a fresh keypair for tests. We don't use
// GenerateKeypair (which requires a passphrase) here because in-memory keys
// don't need encryption — that's a SavePrivateKey concern.
func helperKeypair(t *testing.T) (*minisign.PublicKey, *minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return &pub, &priv
}

func TestSignVerify_Table(t *testing.T) {
	pub, priv := helperKeypair(t)

	tests := []struct {
		name    string
		bundle  []byte
		comment string
	}{
		{name: "tiny", bundle: []byte("x"), comment: "skill=acme/hello version=0.1.0"},
		{name: "binary", bundle: bytes.Repeat([]byte{0xfe, 0x00, 0x42}, 1024), comment: "skill=foo version=2.3.4"},
		{name: "empty comment", bundle: []byte("hello world"), comment: ""},
		{name: "with prefix in comment", bundle: []byte("ok"), comment: "mcskill skill=x version=1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig, err := Sign(tt.bundle, priv, tt.comment)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if err := Verify(tt.bundle, sig, pub); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

func TestVerify_TamperedBundleRejected(t *testing.T) {
	pub, priv := helperKeypair(t)
	bundle := []byte("legitimate bundle bytes")
	sig, err := Sign(bundle, priv, "skill=x version=1.0.0")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	tampered := bytes.Clone(bundle)
	tampered[0] ^= 0xff

	err = Verify(tampered, sig, pub)
	if err == nil {
		t.Fatal("Verify accepted tampered bundle")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("got %v, want wraps ErrInvalidSignature", err)
	}
}

func TestVerify_WrongPubkeyRejected(t *testing.T) {
	_, priv := helperKeypair(t)
	otherPub, _ := helperKeypair(t)
	bundle := []byte("bundle bytes")
	sig, err := Sign(bundle, priv, "skill=x version=1.0.0")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	err = Verify(bundle, sig, otherPub)
	if err == nil {
		t.Fatal("Verify accepted signature under wrong public key")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("got %v, want wraps ErrInvalidSignature", err)
	}
}

// TestVerify_ReplayRejection covers the replay-protection guarantee from
// ADR 0002: a signature taken from bundle A cannot be reused on bundle B
// even if B has the same skill id + version, because the trusted comment
// (which is signed) carries the bundle's sha256 digest.
func TestVerify_ReplayRejection(t *testing.T) {
	pub, priv := helperKeypair(t)
	bundleA := []byte("contents of release v1.0.0")
	bundleB := []byte("contents of release v1.0.0 (malicious copy)")

	sigA, err := Sign(bundleA, priv, "skill=x version=1.0.0")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	err = Verify(bundleB, sigA, pub)
	if err == nil {
		t.Fatal("Verify accepted replayed signature on different bundle")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("got %v, want wraps ErrInvalidSignature", err)
	}
}

func TestVerify_NilInputs(t *testing.T) {
	pub, priv := helperKeypair(t)
	good, err := Sign([]byte("x"), priv, "skill=x version=1.0.0")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(nil, good, pub); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("nil bundle: %v", err)
	}
	if err := Verify([]byte("x"), nil, pub); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("nil sig: %v", err)
	}
	if err := Verify([]byte("x"), good, nil); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("nil pub: %v", err)
	}
}

func TestSign_NilPrivateKey(t *testing.T) {
	if _, err := Sign([]byte("x"), nil, ""); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("got %v, want wraps ErrInvalidSignature", err)
	}
}

// TestRoundTrip_KeygenSignVerify is the integration check: generate, persist,
// load, sign, verify — all against the on-disk encrypted keyfile format.
func TestRoundTrip_KeygenSignVerify(t *testing.T) {
	tmp := t.TempDir()
	keypath := filepath.Join(tmp, "subdir", "id_mcskill.key")
	const passphrase = "correct horse battery staple"

	pub, priv, err := GenerateKeypair(passphrase)
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if err := SavePrivateKey(keypath, passphrase, priv); err != nil {
		t.Fatalf("SavePrivateKey: %v", err)
	}
	info, err := os.Stat(keypath)
	if err != nil {
		t.Fatalf("stat keyfile: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("keyfile mode = %o, want 0600", mode)
	}

	loaded, err := LoadPrivateKey(keypath, passphrase)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	if loaded.ID() != priv.ID() {
		t.Errorf("ID mismatch: got %x want %x", loaded.ID(), priv.ID())
	}

	bundle := []byte("a real .mcskill tarball would go here")
	sig, err := Sign(bundle, loaded, "skill=acme/hello version=0.1.0")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(bundle, sig, pub); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Confirm the trusted comment carries the bundle digest as expected.
	var parsed minisign.Signature
	if err := parsed.UnmarshalText(sig); err != nil {
		t.Fatalf("unmarshal sig: %v", err)
	}
	digest := sha256.Sum256(bundle)
	wantHex := hex.EncodeToString(digest[:])
	if !strings.Contains(parsed.TrustedComment, "sha256="+wantHex) {
		t.Errorf("trusted comment missing sha256=%s: %q", wantHex, parsed.TrustedComment)
	}
	if !strings.HasPrefix(parsed.TrustedComment, trustedCommentPrefix) {
		t.Errorf("trusted comment missing %q prefix: %q", trustedCommentPrefix, parsed.TrustedComment)
	}
}

func TestLoadPrivateKey_WrongPassphrase(t *testing.T) {
	tmp := t.TempDir()
	keypath := filepath.Join(tmp, "id.key")
	_, priv, err := GenerateKeypair("rightpass")
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if err := SavePrivateKey(keypath, "rightpass", priv); err != nil {
		t.Fatalf("SavePrivateKey: %v", err)
	}
	_, err = LoadPrivateKey(keypath, "wrongpass")
	if !errors.Is(err, ErrInvalidPassphrase) {
		t.Errorf("got %v, want wraps ErrInvalidPassphrase", err)
	}
}

func TestGenerateKeypair_RequiresPassphrase(t *testing.T) {
	_, _, err := GenerateKeypair("")
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Errorf("got %v, want ErrEmptyPassphrase", err)
	}
}

func TestPublicKeyFormatting(t *testing.T) {
	pub, _ := helperKeypair(t)
	canon := FormatPublicKey(pub)
	if got := len(canon); got != 56 {
		t.Errorf("FormatPublicKey len = %d, want 56", got)
	}
	// minisign encodes the algorithm marker (0x6445 LE) plus the key id
	// before the public key bytes; the first two base64 chars are always
	// "RW" with the third varying with the random key id.
	if !strings.HasPrefix(canon, "RW") {
		t.Errorf("FormatPublicKey missing RW prefix: %q", canon)
	}
	prefixed := FormatPublicKeyWithPrefix(pub)
	if !strings.HasPrefix(prefixed, PubKeyPrefix) {
		t.Errorf("FormatPublicKeyWithPrefix missing prefix: %q", prefixed)
	}
	if !strings.HasSuffix(prefixed, canon) {
		t.Errorf("FormatPublicKeyWithPrefix missing canonical suffix: %q", prefixed)
	}
	if id := PublicKeyID(pub); len(id) != 16 {
		t.Errorf("PublicKeyID len = %d, want 16: %q", len(id), id)
	}
}

func TestParsePublicKey(t *testing.T) {
	pub, _ := helperKeypair(t)
	canon := FormatPublicKey(pub)
	prefixed := FormatPublicKeyWithPrefix(pub)
	multiline, err := pub.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}

	cases := []string{canon, prefixed, string(multiline)}
	for i, in := range cases {
		got, err := ParsePublicKey(in)
		if err != nil {
			t.Fatalf("ParsePublicKey[%d]: %v", i, err)
		}
		if got.ID() != pub.ID() {
			t.Errorf("ParsePublicKey[%d]: id mismatch", i)
		}
	}

	if _, err := ParsePublicKey(""); err == nil {
		t.Error("ParsePublicKey(\"\"): expected error")
	}
	if _, err := ParsePublicKey("not a key"); err == nil {
		t.Error("ParsePublicKey(garbage): expected error")
	}
}

func TestIsValidSignatureBlob(t *testing.T) {
	_, priv := helperKeypair(t)
	good, err := Sign([]byte("hello"), priv, "skill=x version=1.0.0")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !IsValidSignatureBlob(string(good)) {
		t.Error("good signature rejected")
	}
	if IsValidSignatureBlob("") {
		t.Error("empty string accepted")
	}
	if IsValidSignatureBlob("not a signature") {
		t.Error("garbage accepted")
	}
}
