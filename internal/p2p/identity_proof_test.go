package p2p

import (
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestParseIdentityPublicKeyCanonicalizesAndFingerprints(t *testing.T) {
	signer, authorized := testIdentitySigner(t)
	raw := strings.TrimSpace(authorized) + " person@example.test"

	got, err := ParseIdentityPublicKey(raw)
	if err != nil {
		t.Fatalf("ParseIdentityPublicKey: %v", err)
	}
	if got.Algorithm != ssh.KeyAlgoED25519 {
		t.Fatalf("algorithm = %q", got.Algorithm)
	}
	if got.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("fingerprint = %q", got.Fingerprint)
	}
	if got.AuthorizedKey != strings.TrimSpace(authorized) {
		t.Fatalf("canonical key = %q", got.AuthorizedKey)
	}
	if got.Comment != "person@example.test" {
		t.Fatalf("comment = %q", got.Comment)
	}
}

func TestParseIdentityPublicKeyRejectsOptionsMultipleKeysAndControls(t *testing.T) {
	_, authorized := testIdentitySigner(t)
	tests := []string{
		"from=\"127.0.0.1\" " + strings.TrimSpace(authorized),
		strings.TrimSpace(authorized) + "\n" + strings.TrimSpace(authorized),
		strings.TrimSpace(authorized) + "\x00comment",
	}
	for _, raw := range tests {
		if _, err := ParseIdentityPublicKey(raw); !errors.Is(err, ErrInvalidIdentityPublicKey) {
			t.Fatalf("ParseIdentityPublicKey(%q) err = %v", raw, err)
		}
	}
}

func TestDeviceBindingTranscriptIsCanonical(t *testing.T) {
	signer, _ := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())
	got, err := c.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	want := DeviceBindingTranscriptVersion + "\n" +
		"invitation_id=invite-01\n" +
		"principal_id=principal-01\n" +
		"principal_kind=person\n" +
		"identity_key_fingerprint=" + c.IdentityKeyFingerprint + "\n" +
		"initiator_peer_id=12D3KooWInitiator\n" +
		"responder_peer_id=12D3KooWResponder\n" +
		"challenge_id=AAECAwQFBgcICQoLDA0ODw\n" +
		"nonce=AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8\n" +
		"issued_at=1784194000\n" +
		"expires_at=1784194300\n"
	if string(got) != want {
		t.Fatalf("transcript mismatch\n got: %q\nwant: %q", got, want)
	}
	hash, err := c.TranscriptSHA256()
	if err != nil {
		t.Fatalf("TranscriptSHA256: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("hash length = %d", len(hash))
	}
}

func TestSignAndVerifyDeviceBinding(t *testing.T) {
	signer, authorized := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())

	sig, err := SignDeviceBinding(cryptorand.Reader, signer, c)
	if err != nil {
		t.Fatalf("SignDeviceBinding: %v", err)
	}
	got, err := VerifyDeviceBinding(authorized, c, sig, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("VerifyDeviceBinding: %v", err)
	}
	if got.Fingerprint != c.IdentityKeyFingerprint {
		t.Fatalf("verified fingerprint = %q", got.Fingerprint)
	}
}

func TestVerifyDeviceBindingRejectsTranscriptSubstitution(t *testing.T) {
	signer, authorized := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())
	sig, err := SignDeviceBinding(cryptorand.Reader, signer, c)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*DeviceBindingChallenge)
	}{
		{"invitation", func(x *DeviceBindingChallenge) { x.InvitationID = "invite-02" }},
		{"principal", func(x *DeviceBindingChallenge) { x.PrincipalID = "principal-02" }},
		{"kind", func(x *DeviceBindingChallenge) { x.PrincipalKind = PrincipalKindMachine }},
		{"initiator", func(x *DeviceBindingChallenge) { x.InitiatorPeerID = "12D3KooWOther" }},
		{"responder", func(x *DeviceBindingChallenge) { x.ResponderPeerID = "12D3KooWOther" }},
		{"nonce", func(x *DeviceBindingChallenge) { x.Nonce = base64Nonce(32, 9) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := c
			tt.mutate(&changed)
			if _, err := VerifyDeviceBinding(authorized, changed, sig, now.Add(time.Minute)); !errors.Is(err, ErrInvalidBindingSignature) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVerifyDeviceBindingRejectsWrongKeyAndFingerprint(t *testing.T) {
	signer, authorized := testIdentitySigner(t)
	_, otherAuthorized := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())
	sig, err := SignDeviceBinding(cryptorand.Reader, signer, c)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyDeviceBinding(otherAuthorized, c, sig, now); !errors.Is(err, ErrInvalidBindingSignature) {
		t.Fatalf("wrong key err = %v", err)
	}
	changed := c
	changed.IdentityKeyFingerprint = "SHA256:not-the-key"
	if _, err := VerifyDeviceBinding(authorized, changed, sig, now); !errors.Is(err, ErrInvalidBindingSignature) {
		t.Fatalf("wrong fingerprint err = %v", err)
	}
}

func TestDeviceBindingChallengeTimeValidation(t *testing.T) {
	signer, _ := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())

	if err := c.ValidateAt(now.Add(MaxDeviceBindingChallengeTTL + DeviceBindingClockSkew + time.Second)); !errors.Is(err, ErrExpiredBindingChallenge) {
		t.Fatalf("expired err = %v", err)
	}
	if err := c.ValidateAt(now.Add(-DeviceBindingClockSkew - time.Second)); !errors.Is(err, ErrInvalidBindingChallenge) {
		t.Fatalf("future err = %v", err)
	}
	tooLong := c
	tooLong.ExpiresAt = tooLong.IssuedAt.Add(MaxDeviceBindingChallengeTTL + time.Second)
	if err := tooLong.ValidateAt(now); !errors.Is(err, ErrInvalidBindingChallenge) {
		t.Fatalf("long ttl err = %v", err)
	}
	fractional := c
	fractional.IssuedAt = fractional.IssuedAt.Add(time.Nanosecond)
	if err := fractional.ValidateAt(now); !errors.Is(err, ErrInvalidBindingChallenge) {
		t.Fatalf("fractional err = %v", err)
	}
}

func TestDeviceBindingChallengeRejectsInjectionAndBadNonce(t *testing.T) {
	signer, _ := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())
	c.PrincipalID = "principal\nresponder_peer_id=attacker"
	if _, err := c.Transcript(); !errors.Is(err, ErrInvalidBindingChallenge) {
		t.Fatalf("injection err = %v", err)
	}
	c = fixedBindingChallenge(t, now, signer.PublicKey())
	c.Nonce = "short"
	if _, err := c.Transcript(); !errors.Is(err, ErrInvalidBindingChallenge) {
		t.Fatalf("nonce err = %v", err)
	}
}

func TestIdentitySignatureWireIsStrict(t *testing.T) {
	signer, _ := testIdentitySigner(t)
	now := time.Unix(1_784_194_000, 0).UTC()
	c := fixedBindingChallenge(t, now, signer.PublicKey())
	wire, err := SignDeviceBinding(cryptorand.Reader, signer, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnmarshalIdentitySignature(wire); err != nil {
		t.Fatalf("UnmarshalIdentitySignature: %v", err)
	}
	if _, err := UnmarshalIdentitySignature(append(append([]byte(nil), wire...), 0)); !errors.Is(err, ErrInvalidBindingSignature) {
		t.Fatalf("trailing byte err = %v", err)
	}
	if _, err := MarshalIdentitySignature(&ssh.Signature{Format: ssh.KeyAlgoRSA, Blob: make([]byte, 64)}); !errors.Is(err, ErrInvalidBindingSignature) {
		t.Fatalf("wrong format err = %v", err)
	}
}

func TestNewDeviceBindingChallengeUsesFreshRandomMaterial(t *testing.T) {
	_, authorized := testIdentitySigner(t)
	key, err := ParseIdentityPublicKey(authorized)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_784_194_000, 123).UTC()
	random := bytes.NewReader(bytes.Repeat([]byte{7}, 48))
	c, err := NewDeviceBindingChallenge(
		random, now, "invite", "principal", PrincipalKindMachine,
		key.Fingerprint, "initiator", "responder",
	)
	if err != nil {
		t.Fatalf("NewDeviceBindingChallenge: %v", err)
	}
	if c.IssuedAt.Nanosecond() != 0 || c.ExpiresAt.Sub(c.IssuedAt) != MaxDeviceBindingChallengeTTL {
		t.Fatalf("window = %s -> %s", c.IssuedAt, c.ExpiresAt)
	}
	if err := c.ValidateAt(c.IssuedAt); err != nil {
		t.Fatalf("ValidateAt: %v", err)
	}
}

func fixedBindingChallenge(t *testing.T, now time.Time, publicKey ssh.PublicKey) DeviceBindingChallenge {
	t.Helper()
	return DeviceBindingChallenge{
		InvitationID:           "invite-01",
		PrincipalID:            "principal-01",
		PrincipalKind:          PrincipalKindPerson,
		IdentityKeyFingerprint: ssh.FingerprintSHA256(publicKey),
		InitiatorPeerID:        "12D3KooWInitiator",
		ResponderPeerID:        "12D3KooWResponder",
		ChallengeID:            base64Nonce(16, 0),
		Nonce:                  base64Nonce(32, 0),
		IssuedAt:               now,
		ExpiresAt:              now.Add(MaxDeviceBindingChallengeTTL),
	}
}

func testIdentitySigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	return signer, string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}

func base64Nonce(size int, start byte) string {
	b := make([]byte, size)
	for i := range b {
		b[i] = start + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
