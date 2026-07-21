package p2p

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCollaborationInvitationRoundTripAndValidation(t *testing.T) {
	payload := CollaborationInvitationPayload{
		Version: CollaborationInviteVersion, InvitationID: "invite-01",
		Token:                  base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		HomePeerID:             "12D3KooWHome",
		HomeAddrs:              []string{"/ip4/127.0.0.1/tcp/4001"},
		IdentityKeyFingerprint: "SHA256:key-fingerprint",
		ExpiresAt:              time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	}
	encoded, err := EncodeCollaborationInvitation(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, collaborationInvitePrefix) || strings.Contains(encoded, "{") {
		t.Fatalf("invitation is not copy-safe: %q", encoded)
	}
	decoded, err := DecodeCollaborationInvitation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.InvitationID != payload.InvitationID || decoded.Token != payload.Token ||
		decoded.HomePeerID != payload.HomePeerID || decoded.IdentityKeyFingerprint != payload.IdentityKeyFingerprint {
		t.Fatalf("decoded payload = %#v", decoded)
	}

	bad := []string{
		"", "mcplexer-invite-v2:not-supported", collaborationInvitePrefix + "%%%",
	}
	for _, input := range bad {
		if _, err := DecodeCollaborationInvitation(input); !errors.Is(err, ErrInvalidCollaborationInvite) {
			t.Fatalf("DecodeCollaborationInvitation(%q) = %v", input, err)
		}
	}
	payload.Token = base64.RawURLEncoding.EncodeToString(make([]byte, 31))
	if _, err := EncodeCollaborationInvitation(payload); !errors.Is(err, ErrInvalidCollaborationInvite) {
		t.Fatalf("short token error = %v", err)
	}
	payload.Token = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	payload.HomeAddrs = make([]string, 33)
	if _, err := EncodeCollaborationInvitation(payload); !errors.Is(err, ErrInvalidCollaborationInvite) {
		t.Fatalf("address cap error = %v", err)
	}
}
