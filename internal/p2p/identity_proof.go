package p2p

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

const (
	// DeviceBindingTranscriptVersion is intentionally part of the signed
	// bytes. A future wire shape must use a new value instead of changing the
	// meaning of signatures already stored in the audit ledger.
	DeviceBindingTranscriptVersion = "MCPLEXER-DEVICE-BINDING-V1"

	// DeviceBindingNonceBytes gives every proof a 256-bit server challenge.
	DeviceBindingNonceBytes = 32

	// MaxDeviceBindingChallengeTTL limits how long a captured, unsigned
	// challenge remains useful. Invitations have their own (longer) expiry;
	// this is the live proof window.
	MaxDeviceBindingChallengeTTL = 5 * time.Minute

	// DeviceBindingClockSkew tolerates small clock differences while still
	// refusing challenges minted materially in the future.
	DeviceBindingClockSkew = 30 * time.Second
)

var (
	ErrInvalidIdentityPublicKey = errors.New("p2p identity: invalid OpenSSH public key")
	ErrUnsupportedIdentityKey   = errors.New("p2p identity: only ssh-ed25519 keys are supported")
	ErrInvalidBindingChallenge  = errors.New("p2p identity: invalid device-binding challenge")
	ErrExpiredBindingChallenge  = errors.New("p2p identity: device-binding challenge expired")
	ErrInvalidBindingSignature  = errors.New("p2p identity: invalid device-binding signature")
)

// PrincipalKind is the durable actor above a libp2p device identity.
type PrincipalKind string

const (
	PrincipalKindPerson  PrincipalKind = "person"
	PrincipalKindMachine PrincipalKind = "machine"
)

// IdentityPublicKey is a canonical, public-only OpenSSH identity key. Key is
// deliberately unexported so JSON/logging cannot accidentally serialize an
// implementation object; AuthorizedKey and Fingerprint are safe to display.
type IdentityPublicKey struct {
	AuthorizedKey string `json:"authorized_key"`
	Fingerprint   string `json:"fingerprint"`
	Algorithm     string `json:"algorithm"`
	Comment       string `json:"comment,omitempty"`

	key ssh.PublicKey
}

// DeviceBindingChallenge is the exact identity claim signed by an invitee.
// The connected libp2p peer IDs on both sides are included so a proof cannot
// be replayed to bind the same principal key to another device or responder.
type DeviceBindingChallenge struct {
	InvitationID           string        `json:"invitation_id"`
	PrincipalID            string        `json:"principal_id"`
	PrincipalKind          PrincipalKind `json:"principal_kind"`
	IdentityKeyFingerprint string        `json:"identity_key_fingerprint"`
	InitiatorPeerID        string        `json:"initiator_peer_id"`
	ResponderPeerID        string        `json:"responder_peer_id"`
	ChallengeID            string        `json:"challenge_id"`
	Nonce                  string        `json:"nonce"`
	IssuedAt               time.Time     `json:"issued_at"`
	ExpiresAt              time.Time     `json:"expires_at"`
}

// ParseIdentityPublicKey validates and canonicalizes one OpenSSH authorized-
// key line. authorized_keys options are rejected: MCPlexer identity is a
// signing-key relationship, not an SSH-login policy. A trailing comment is
// retained only as a display hint and is never identity-bearing.
func ParseIdentityPublicKey(raw string) (IdentityPublicKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 4096 || containsLineBreakOrNUL(trimmed) {
		return IdentityPublicKey{}, ErrInvalidIdentityPublicKey
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(trimmed + "\n"))
	if err != nil || key == nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
		return IdentityPublicKey{}, ErrInvalidIdentityPublicKey
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return IdentityPublicKey{}, ErrUnsupportedIdentityKey
	}
	return IdentityPublicKey{
		AuthorizedKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
		Fingerprint:   ssh.FingerprintSHA256(key),
		Algorithm:     key.Type(),
		Comment:       strings.TrimSpace(comment),
		key:           key,
	}, nil
}

// NewDeviceBindingChallenge creates a fresh, short-lived challenge. The
// caller must persist the challenge ID and a nonce/transcript hash so the
// eventual consume operation can be single-use and transactional.
func NewDeviceBindingChallenge(
	random io.Reader,
	now time.Time,
	invitationID string,
	principalID string,
	principalKind PrincipalKind,
	identityKeyFingerprint string,
	initiatorPeerID string,
	responderPeerID string,
) (DeviceBindingChallenge, error) {
	if random == nil {
		random = rand.Reader
	}
	challengeIDBytes := make([]byte, 16)
	nonce := make([]byte, DeviceBindingNonceBytes)
	if _, err := io.ReadFull(random, challengeIDBytes); err != nil {
		return DeviceBindingChallenge{}, fmt.Errorf("generate challenge id: %w", err)
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return DeviceBindingChallenge{}, fmt.Errorf("generate challenge nonce: %w", err)
	}
	now = now.UTC().Truncate(time.Second)
	c := DeviceBindingChallenge{
		InvitationID:           invitationID,
		PrincipalID:            principalID,
		PrincipalKind:          principalKind,
		IdentityKeyFingerprint: identityKeyFingerprint,
		InitiatorPeerID:        initiatorPeerID,
		ResponderPeerID:        responderPeerID,
		ChallengeID:            base64.RawURLEncoding.EncodeToString(challengeIDBytes),
		Nonce:                  base64.RawURLEncoding.EncodeToString(nonce),
		IssuedAt:               now,
		ExpiresAt:              now.Add(MaxDeviceBindingChallengeTTL),
	}
	if err := c.validateStructure(); err != nil {
		return DeviceBindingChallenge{}, err
	}
	return c, nil
}

// Transcript returns the byte-exact, domain-separated statement signed by
// the identity key. Do not JSON-marshal this structure for signing: JSON field
// order and timestamp formatting are not the protocol.
func (c DeviceBindingChallenge) Transcript() ([]byte, error) {
	if err := c.validateStructure(); err != nil {
		return nil, err
	}
	lines := []string{
		DeviceBindingTranscriptVersion,
		"invitation_id=" + c.InvitationID,
		"principal_id=" + c.PrincipalID,
		"principal_kind=" + string(c.PrincipalKind),
		"identity_key_fingerprint=" + c.IdentityKeyFingerprint,
		"initiator_peer_id=" + c.InitiatorPeerID,
		"responder_peer_id=" + c.ResponderPeerID,
		"challenge_id=" + c.ChallengeID,
		"nonce=" + c.Nonce,
		fmt.Sprintf("issued_at=%d", c.IssuedAt.UTC().Unix()),
		fmt.Sprintf("expires_at=%d", c.ExpiresAt.UTC().Unix()),
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// TranscriptSHA256 returns the lowercase audit/storage hash of the canonical
// transcript. It is not a replacement for signature verification.
func (c DeviceBindingChallenge) TranscriptSHA256() (string, error) {
	transcript, err := c.Transcript()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(transcript)
	return hex.EncodeToString(sum[:]), nil
}

// SignDeviceBinding produces the compact SSH wire signature used by the
// identity protocol. ssh.Signer can be backed by ssh-agent, an encrypted local
// key loader, or a hardware-backed OpenSSH key implementation.
func SignDeviceBinding(random io.Reader, signer ssh.Signer, c DeviceBindingChallenge) ([]byte, error) {
	if signer == nil || signer.PublicKey() == nil {
		return nil, ErrInvalidIdentityPublicKey
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, ErrUnsupportedIdentityKey
	}
	transcript, err := c.Transcript()
	if err != nil {
		return nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	sig, err := signer.Sign(random, transcript)
	if err != nil {
		return nil, fmt.Errorf("sign device binding: %w", err)
	}
	return MarshalIdentitySignature(sig)
}

// VerifyDeviceBinding verifies the challenge window, fingerprint pin, exact
// SSH signature format, and signature over the canonical transcript. Replay
// state is checked by the invitation store when it atomically consumes the
// challenge; this pure function intentionally performs no persistence.
func VerifyDeviceBinding(
	rawPublicKey string,
	c DeviceBindingChallenge,
	signatureWire []byte,
	now time.Time,
) (IdentityPublicKey, error) {
	identityKey, err := ParseIdentityPublicKey(rawPublicKey)
	if err != nil {
		return IdentityPublicKey{}, err
	}
	if identityKey.Fingerprint != c.IdentityKeyFingerprint {
		return IdentityPublicKey{}, ErrInvalidBindingSignature
	}
	if err := c.ValidateAt(now); err != nil {
		return IdentityPublicKey{}, err
	}
	sig, err := UnmarshalIdentitySignature(signatureWire)
	if err != nil {
		return IdentityPublicKey{}, err
	}
	transcript, err := c.Transcript()
	if err != nil {
		return IdentityPublicKey{}, err
	}
	if err := identityKey.key.Verify(transcript, sig); err != nil {
		return IdentityPublicKey{}, ErrInvalidBindingSignature
	}
	return identityKey, nil
}

// ValidateAt applies the live proof window on top of structural validation.
func (c DeviceBindingChallenge) ValidateAt(now time.Time) error {
	if err := c.validateStructure(); err != nil {
		return err
	}
	now = now.UTC()
	if now.Before(c.IssuedAt.UTC().Add(-DeviceBindingClockSkew)) {
		return fmt.Errorf("%w: issued in the future", ErrInvalidBindingChallenge)
	}
	if now.After(c.ExpiresAt.UTC().Add(DeviceBindingClockSkew)) {
		return ErrExpiredBindingChallenge
	}
	return nil
}

// MarshalIdentitySignature encodes the two RFC 4251 SSH strings without any
// trailing bytes. Keeping this small shape avoids accepting an SSHSIG file or
// another container with different namespace/hash semantics by accident.
func MarshalIdentitySignature(sig *ssh.Signature) ([]byte, error) {
	if sig == nil || sig.Format != ssh.KeyAlgoED25519 || len(sig.Blob) != 64 || len(sig.Rest) != 0 {
		return nil, ErrInvalidBindingSignature
	}
	wire := struct {
		Format string
		Blob   []byte
	}{Format: sig.Format, Blob: append([]byte(nil), sig.Blob...)}
	return ssh.Marshal(&wire), nil
}

// UnmarshalIdentitySignature is the strict inverse of
// MarshalIdentitySignature. Extra bytes and non-Ed25519 formats are rejected.
func UnmarshalIdentitySignature(raw []byte) (*ssh.Signature, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, ErrInvalidBindingSignature
	}
	var wire struct {
		Format string
		Blob   []byte
		Rest   []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(raw, &wire); err != nil || len(wire.Rest) != 0 {
		return nil, ErrInvalidBindingSignature
	}
	if wire.Format != ssh.KeyAlgoED25519 || len(wire.Blob) != 64 {
		return nil, ErrInvalidBindingSignature
	}
	return &ssh.Signature{Format: wire.Format, Blob: append([]byte(nil), wire.Blob...)}, nil
}

func (c DeviceBindingChallenge) validateStructure() error {
	fields := []struct {
		name  string
		value string
		max   int
	}{
		{"invitation_id", c.InvitationID, 256},
		{"principal_id", c.PrincipalID, 256},
		{"identity_key_fingerprint", c.IdentityKeyFingerprint, 256},
		{"initiator_peer_id", c.InitiatorPeerID, 256},
		{"responder_peer_id", c.ResponderPeerID, 256},
		{"challenge_id", c.ChallengeID, 256},
		{"nonce", c.Nonce, 256},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" || len(field.value) > field.max || containsProtocolControl(field.value) {
			return fmt.Errorf("%w: %s", ErrInvalidBindingChallenge, field.name)
		}
	}
	if c.PrincipalKind != PrincipalKindPerson && c.PrincipalKind != PrincipalKindMachine {
		return fmt.Errorf("%w: principal_kind", ErrInvalidBindingChallenge)
	}
	if !strings.HasPrefix(c.IdentityKeyFingerprint, "SHA256:") {
		return fmt.Errorf("%w: identity_key_fingerprint", ErrInvalidBindingChallenge)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(c.Nonce)
	if err != nil || len(nonce) != DeviceBindingNonceBytes {
		return fmt.Errorf("%w: nonce", ErrInvalidBindingChallenge)
	}
	challengeID, err := base64.RawURLEncoding.DecodeString(c.ChallengeID)
	if err != nil || len(challengeID) != 16 {
		return fmt.Errorf("%w: challenge_id", ErrInvalidBindingChallenge)
	}
	issued := c.IssuedAt.UTC()
	expires := c.ExpiresAt.UTC()
	if issued.IsZero() || expires.IsZero() || !expires.After(issued) || expires.Sub(issued) > MaxDeviceBindingChallengeTTL {
		return fmt.Errorf("%w: time window", ErrInvalidBindingChallenge)
	}
	if issued.Nanosecond() != 0 || expires.Nanosecond() != 0 {
		return fmt.Errorf("%w: timestamps must have whole-second precision", ErrInvalidBindingChallenge)
	}
	return nil
}

func containsLineBreakOrNUL(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}

func containsProtocolControl(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == 0 || unicode.IsControl(r) {
			return true
		}
	}
	return false
}
