package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type collaborationFixture struct {
	db    *DB
	ctx   context.Context
	now   time.Time
	owner *store.Principal
	share *store.WorkspaceShare
}

func newCollaborationFixture(t *testing.T) collaborationFixture {
	t.Helper()
	ctx := context.Background()
	db := newTestDB(t, "collaboration.db")
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	workspace := &store.Workspace{ID: "workspace-ops", Name: "Operations"}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	owner := &store.Principal{
		ID: "principal-owner", Kind: store.PrincipalKindPerson,
		DisplayName: "Local owner", Status: store.PrincipalStatusActive,
		IsLocalOwner: true, CreatedAt: now,
	}
	if err := db.CreatePrincipal(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	share := &store.WorkspaceShare{
		ShareID: "share-ops", LocalWorkspaceID: workspace.ID,
		HomePeerID: "peer-home", OwnerPrincipalID: owner.ID,
		CreatedAt: now,
	}
	if err := db.CreateWorkspaceShare(ctx, share); err != nil {
		t.Fatalf("create share: %v", err)
	}
	return collaborationFixture{db: db, ctx: ctx, now: now, owner: owner, share: share}
}

func TestCollaborationInvitationActivationIsProofBoundAndSingleUse(t *testing.T) {
	t.Parallel()
	fixture := newCollaborationFixture(t)
	principal := &store.Principal{
		ID: "principal-collaborator", Kind: store.PrincipalKindPerson,
		DisplayName: "Collaborator", Status: store.PrincipalStatusPending,
		CreatedAt: fixture.now,
	}
	if err := fixture.db.CreatePrincipal(fixture.ctx, principal); err != nil {
		t.Fatal(err)
	}
	identityKey := &store.PrincipalIdentityKey{
		ID: "key-collaborator", PrincipalID: principal.ID,
		CanonicalPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnly",
		Fingerprint:        "SHA256:test-collaborator", Algorithm: "ssh-ed25519",
		Status: store.PrincipalKeyStatusPending, AddedByPrincipalID: fixture.owner.ID,
		CreatedAt: fixture.now,
	}
	if err := fixture.db.AddPrincipalIdentityKey(fixture.ctx, identityKey); err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256([]byte("one-use-invitation-secret"))
	invitation := &store.PrincipalInvitation{
		ID: "invitation-1", TokenHash: tokenHash[:],
		Purpose:     store.InvitationPurposeNewPrincipal,
		PrincipalID: principal.ID, IdentityKeyID: identityKey.ID,
		CreatedByPrincipalID: fixture.owner.ID, CreatedAt: fixture.now,
		ExpiresAt: fixture.now.Add(time.Hour),
	}
	staged := []store.InvitationWorkspaceGrant{
		{ShareID: fixture.share.ShareID, Capability: store.CapabilityWorkspaceView},
		{ShareID: fixture.share.ShareID, Capability: store.CapabilityTasksRead},
	}
	if err := fixture.db.CreatePrincipalInvitation(fixture.ctx, invitation, staged); err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	nonceHash := sha256.Sum256([]byte("nonce"))
	transcriptHash := sha256.Sum256([]byte("canonical transcript"))
	challenge := &store.PrincipalIdentityChallenge{
		ID: "challenge-1", InvitationID: invitation.ID,
		InitiatorPeerID: "peer-collaborator-laptop", ResponderPeerID: "peer-home",
		NonceHash: nonceHash[:], TranscriptHash: stringHex(transcriptHash[:]),
		IssuedAt: fixture.now.Add(time.Minute), ExpiresAt: fixture.now.Add(6 * time.Minute),
	}
	if err := fixture.db.CreatePrincipalIdentityChallenge(fixture.ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	activation := store.InvitedDeviceActivation{
		InvitationID: invitation.ID, InvitationTokenHash: tokenHash[:],
		ChallengeID: challenge.ID, PeerID: challenge.InitiatorPeerID,
		ResponderPeerID: challenge.ResponderPeerID, DisplayName: "Work laptop",
		DeviceKind: "laptop", BindingVersion: store.DeviceBindingVersionV1,
		BindingTranscriptHash: challenge.TranscriptHash,
		BindingSignature:      []byte("verified-signature-wire"),
		At:                    fixture.now.Add(2 * time.Minute),
	}
	wrongToken := sha256.Sum256([]byte("wrong"))
	wrong := activation
	wrong.InvitationTokenHash = wrongToken[:]
	if _, _, err := fixture.db.ActivateInvitedDevice(fixture.ctx, wrong); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong token error = %v, want not found", err)
	}
	device, grants, err := fixture.db.ActivateInvitedDevice(fixture.ctx, activation)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if device.PeerID != activation.PeerID || len(grants) != 2 {
		t.Fatalf("activation result = device %#v, %d grants", device, len(grants))
	}
	resolvedDevice, resolvedPrincipal, err := fixture.db.ResolveActivePrincipalForPeer(fixture.ctx, activation.PeerID)
	if err != nil {
		t.Fatalf("resolve active peer: %v", err)
	}
	if resolvedDevice.IdentityKeyID != identityKey.ID || resolvedPrincipal.ID != principal.ID {
		t.Fatalf("resolved wrong binding: %#v %#v", resolvedDevice, resolvedPrincipal)
	}
	if _, _, err := fixture.db.ActivateInvitedDevice(fixture.ctx, activation); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("replay error = %v, want conflict", err)
	}
	canRead, err := fixture.db.HasWorkspaceCapability(fixture.ctx, principal.ID, fixture.share.ShareID, store.CapabilityTasksRead, activation.At)
	if err != nil || !canRead {
		t.Fatalf("read capability = %v, %v", canRead, err)
	}
	share, err := fixture.db.GetWorkspaceShare(fixture.ctx, fixture.share.ShareID)
	if err != nil || share.AccessEpoch != 2 {
		t.Fatalf("share epoch = %#v, %v; want 2", share, err)
	}
	rotatedKey := &store.PrincipalIdentityKey{
		ID: "key-collaborator-rotated", PrincipalID: principal.ID,
		CanonicalPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIRotatedTestOnly",
		Fingerprint:        "SHA256:test-collaborator-rotated", Algorithm: "ssh-ed25519",
		Status: store.PrincipalKeyStatusPending, ReplacesKeyID: identityKey.ID,
		AddedByPrincipalID: fixture.owner.ID, CreatedAt: fixture.now.Add(3 * time.Minute),
	}
	if err := fixture.db.AddPrincipalIdentityKey(fixture.ctx, rotatedKey); err != nil {
		t.Fatal(err)
	}
	rotatedToken := sha256.Sum256([]byte("one-use-rotation-secret"))
	rotatedInvitation := &store.PrincipalInvitation{
		ID: "invitation-rotate", TokenHash: rotatedToken[:],
		Purpose: store.InvitationPurposeRotateKey, PrincipalID: principal.ID,
		IdentityKeyID: rotatedKey.ID, CreatedByPrincipalID: fixture.owner.ID,
		CreatedAt: fixture.now.Add(3 * time.Minute), ExpiresAt: fixture.now.Add(time.Hour),
	}
	if err := fixture.db.CreatePrincipalInvitation(fixture.ctx, rotatedInvitation, nil); err != nil {
		t.Fatal(err)
	}
	rotatedNonce := sha256.Sum256([]byte("rotated nonce"))
	rotatedTranscript := sha256.Sum256([]byte("rotated canonical transcript"))
	rotatedChallenge := &store.PrincipalIdentityChallenge{
		ID: "challenge-rotate", InvitationID: rotatedInvitation.ID,
		InitiatorPeerID: activation.PeerID, ResponderPeerID: activation.ResponderPeerID,
		NonceHash: rotatedNonce[:], TranscriptHash: stringHex(rotatedTranscript[:]),
		IssuedAt: fixture.now.Add(3 * time.Minute), ExpiresAt: fixture.now.Add(8 * time.Minute),
	}
	if err := fixture.db.CreatePrincipalIdentityChallenge(fixture.ctx, rotatedChallenge); err != nil {
		t.Fatal(err)
	}
	rotatedActivation := activation
	rotatedActivation.InvitationID = rotatedInvitation.ID
	rotatedActivation.InvitationTokenHash = rotatedToken[:]
	rotatedActivation.ChallengeID = rotatedChallenge.ID
	rotatedActivation.BindingTranscriptHash = rotatedChallenge.TranscriptHash
	rotatedActivation.BindingSignature = []byte("rotated-verified-signature")
	rotatedActivation.At = fixture.now.Add(4 * time.Minute)
	rotatedDevice, _, err := fixture.db.ActivateInvitedDevice(fixture.ctx, rotatedActivation)
	if err != nil || rotatedDevice.ID != device.ID || rotatedDevice.IdentityKeyID != rotatedKey.ID {
		t.Fatalf("same-device key rotation = %#v, %v", rotatedDevice, err)
	}
	if err := fixture.db.RevokePrincipalDevice(fixture.ctx, device.PeerID, "laptop retired", rotatedActivation.At.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err := fixture.db.ListCollaborationAudit(fixture.ctx, "", "device", device.ID, 10)
	if err != nil || len(events) < 1 || events[0].Event != "device.revoked" {
		t.Fatalf("device revocation audit = %#v, %v", events, err)
	}
}

func TestCollaborationIdentityRevocationAndDuplicateKey(t *testing.T) {
	t.Parallel()
	fixture := newCollaborationFixture(t)
	verifiedAt := fixture.now
	first := &store.PrincipalIdentityKey{
		ID: "key-first", PrincipalID: fixture.owner.ID,
		CanonicalPublicKey: "ssh-ed25519 AAAATestFirst", Fingerprint: "SHA256:duplicate",
		Algorithm: "ssh-ed25519", Status: store.PrincipalKeyStatusActive,
		CreatedAt: fixture.now, VerifiedAt: &verifiedAt,
	}
	if err := fixture.db.AddPrincipalIdentityKey(fixture.ctx, first); err != nil {
		t.Fatal(err)
	}
	duplicate := *first
	duplicate.ID = "key-second"
	if err := fixture.db.AddPrincipalIdentityKey(fixture.ctx, &duplicate); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate key error = %v", err)
	}
	if err := fixture.db.RevokePrincipalIdentityKey(fixture.ctx, first.ID, fixture.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.RevokePrincipalIdentityKey(fixture.ctx, first.ID, fixture.now.Add(2*time.Minute)); err != nil {
		t.Fatalf("key revocation should be idempotent: %v", err)
	}
	got, err := fixture.db.GetPrincipalIdentityKey(fixture.ctx, first.ID)
	if err != nil || got.Status != store.PrincipalKeyStatusRevoked {
		t.Fatalf("revoked key = %#v, %v", got, err)
	}
	events, err := fixture.db.ListCollaborationAudit(fixture.ctx, "", "identity_key", first.ID, 10)
	if err != nil || len(events) != 1 || events[0].Event != "identity_key.revoked" {
		t.Fatalf("identity-key revocation audit = %#v, %v", events, err)
	}
}

func stringHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for i, b := range value {
		result[i*2], result[i*2+1] = digits[b>>4], digits[b&0x0f]
	}
	return string(result)
}
