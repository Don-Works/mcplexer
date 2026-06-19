// Package skills — signing primitives for .mcskill bundles.
//
// This file implements the M2.4 signing scheme decided in
// docs/adr/0002-skill-signing.md: minisign (Ed25519, raw keys) via
// aead.dev/minisign, with the trusted-comment slot used to bind the
// signature to a specific skill id, version, and SHA-256 digest. That
// trusted comment is part of the signed payload, so any attempt to
// re-use a signature against a different bundle (replay) fails verification.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"aead.dev/minisign"
)

// Sentinel errors returned by Sign and Verify. Callers match with errors.Is.
var (
	// ErrInvalidSignature indicates the signature blob is malformed or fails
	// cryptographic verification (wrong content, tampered bundle, malformed
	// trusted comment, etc.).
	ErrInvalidSignature = errors.New("invalid signature")

	// ErrUntrustedSigner indicates verification succeeded cryptographically
	// but the signing key is not in the local trust store.
	ErrUntrustedSigner = errors.New("untrusted signer")
)

// trustedCommentPrefix is the canonical prefix we place in the signed
// trusted-comment slot. The fields after it (sigid/version/sha256) bind the
// signature to a specific bundle and prevent replay.
const trustedCommentPrefix = "mcskill"

// Sign produces a minisign signature over bundleBytes. The trusted comment
// embeds the skill id, version, and SHA-256 of the bundle, so the signature
// cannot be re-used against a different bundle.
//
// comment is a free-form line in the form "skill=<id> version=<semver>"; if
// empty, callers must guarantee the bundleBytes already encode that information
// elsewhere. The bundle digest is appended automatically.
func Sign(bundleBytes []byte, secretKey *minisign.PrivateKey, comment string) ([]byte, error) {
	if secretKey == nil {
		return nil, fmt.Errorf("%w: nil private key", ErrInvalidSignature)
	}
	if len(bundleBytes) == 0 {
		return nil, fmt.Errorf("%w: empty bundle", ErrInvalidSignature)
	}
	digest := sha256.Sum256(bundleBytes)
	trusted := buildTrustedComment(comment, digest[:])
	untrusted := fmt.Sprintf("signed by mcplexer key %X", secretKey.ID())
	return minisign.SignWithComments(*secretKey, bundleBytes, trusted, untrusted), nil
}

// Verify checks signature over bundleBytes with pubKey. It returns nil on
// success and a chain that wraps ErrInvalidSignature otherwise.
//
// Verify also checks that the trusted comment carries the canonical mcskill
// prefix and a sha256= field that matches the bundle digest. This is belt-
// and-braces (minisign already authenticates the comment + content) but
// makes replay attempts surface as ErrInvalidSignature with a useful reason.
func Verify(bundleBytes []byte, signature []byte, pubKey *minisign.PublicKey) error {
	if pubKey == nil {
		return fmt.Errorf("%w: nil public key", ErrInvalidSignature)
	}
	if len(bundleBytes) == 0 {
		return fmt.Errorf("%w: empty bundle", ErrInvalidSignature)
	}
	if len(signature) == 0 {
		return fmt.Errorf("%w: empty signature", ErrInvalidSignature)
	}
	var sig minisign.Signature
	if err := sig.UnmarshalText(signature); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	if !minisign.Verify(*pubKey, bundleBytes, signature) {
		return fmt.Errorf("%w: cryptographic verification failed", ErrInvalidSignature)
	}
	if err := checkTrustedComment(sig.TrustedComment, bundleBytes); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	return nil
}

// buildTrustedComment composes the canonical trusted-comment line.
//
//	mcskill skill=<id> version=<v> sha256=<hex>
//
// The first token is a fixed prefix so that downstream code can quickly
// distinguish mcplexer-issued signatures from generic minisign output.
func buildTrustedComment(comment string, digest []byte) string {
	c := strings.TrimSpace(comment)
	c = strings.TrimPrefix(c, trustedCommentPrefix)
	c = strings.TrimSpace(c)
	if c == "" {
		return fmt.Sprintf("%s sha256=%s", trustedCommentPrefix, hex.EncodeToString(digest))
	}
	return fmt.Sprintf("%s %s sha256=%s",
		trustedCommentPrefix, c, hex.EncodeToString(digest))
}

// checkTrustedComment enforces the mcskill prefix and a matching sha256= field
// on the (already cryptographically authenticated) trusted comment.
func checkTrustedComment(trusted string, bundleBytes []byte) error {
	t := strings.TrimSpace(trusted)
	if !strings.HasPrefix(t, trustedCommentPrefix) {
		return fmt.Errorf("trusted comment missing %q prefix", trustedCommentPrefix)
	}
	got, ok := extractField(t, "sha256=")
	if !ok {
		return errors.New("trusted comment missing sha256= field")
	}
	want := sha256.Sum256(bundleBytes)
	if !strings.EqualFold(got, hex.EncodeToString(want[:])) {
		return errors.New("trusted comment sha256 mismatch (replay)")
	}
	return nil
}

// extractField pulls the value of a "<key>=<value>" pair from a space-
// separated string. Returns ok=false when the key is absent.
func extractField(s, key string) (string, bool) {
	for _, tok := range strings.Fields(s) {
		if strings.HasPrefix(tok, key) {
			return strings.TrimPrefix(tok, key), true
		}
	}
	return "", false
}

// IsValidSignatureBlob reports whether s is a syntactically valid minisign
// signature. It does not check the cryptographic signature itself — only that
// the on-the-wire format parses. Used by manifest.Validate to surface
// malformed signature fields early.
func IsValidSignatureBlob(s string) bool {
	if s == "" {
		return false
	}
	var sig minisign.Signature
	return sig.UnmarshalText([]byte(s)) == nil
}
