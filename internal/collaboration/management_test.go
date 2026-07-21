package collaboration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"golang.org/x/crypto/ssh"
)

type fakeInviteTransport struct {
	peerID string
	addrs  []string
}

func (f *fakeInviteTransport) LocalPeerID() string { return f.peerID }
func (f *fakeInviteTransport) LocalAddrs() []string {
	return append([]string(nil), f.addrs...)
}
func (f *fakeInviteTransport) Join(context.Context, p2p.CollaborationJoinOptions) (*p2p.CollaborationJoinResult, error) {
	return nil, errors.New("not used")
}

type managementFixture struct {
	manager   *Manager
	db        *sqlite.DB
	workspace *store.Workspace
	owner     *store.Principal
	share     *store.WorkspaceShare
}

func newManagementFixture(t *testing.T) managementFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &store.Workspace{Name: "operations", RootPath: "/tmp/operations", Tags: json.RawMessage(`[]`)}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, &fakeInviteTransport{
		peerID: "12D3KooWHome", addrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	owner, err := manager.EnsureLocalOwner(ctx, &store.User{
		UserID: "local-owner", DisplayName: "Local owner", IsSelf: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	shares, err := manager.EnsureWorkspaceShares(ctx)
	if err != nil {
		t.Fatalf("EnsureWorkspaceShares = %#v, %v", shares, err)
	}
	var share *store.WorkspaceShare
	for i := range shares {
		if shares[i].LocalWorkspaceID == workspace.ID {
			share = &shares[i]
			break
		}
	}
	if share == nil {
		t.Fatalf("EnsureWorkspaceShares omitted %s: %#v", workspace.ID, shares)
	}
	return managementFixture{manager: manager, db: db, workspace: workspace, owner: owner, share: share}
}

func testAuthorizedKey(t *testing.T, comment string) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + comment
}

func TestCreateInvitationStoresOnlyTokenDigestAndStagesExactGrants(t *testing.T) {
	fixture := newManagementFixture(t)
	ctx := context.Background()
	result, err := fixture.manager.CreateInvitation(ctx, CreateInvitationInput{
		Kind: store.PrincipalKindPerson, DisplayName: "Team member",
		PublicKey: testAuthorizedKey(t, "member@test.invalid"),
		WorkspaceGrants: []InvitationGrantInput{{
			ShareID: fixture.share.ShareID,
			Capabilities: []string{
				store.CapabilityWorkspaceView, store.CapabilityTasksRead,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := p2p.DecodeCollaborationInvitation(result.InviteCode)
	if err != nil {
		t.Fatal(err)
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(payload.Token)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(rawToken)
	stored, staged, err := fixture.db.GetPrincipalInvitation(ctx, result.Invitation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.TokenHash, digest[:]) || bytes.Equal(stored.TokenHash, rawToken) {
		t.Fatal("database did not retain exactly the one-way invitation digest")
	}
	if len(staged) != 2 || staged[0].ShareID != fixture.share.ShareID {
		t.Fatalf("staged grants = %#v", staged)
	}
	serialized, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, []byte("token_hash")) || bytes.Contains(serialized, []byte(payload.Token)) {
		t.Fatalf("invitation JSON exposed bearer material: %s", serialized)
	}
	if result.Principal.Status != store.PrincipalStatusPending || result.IdentityKey.Status != store.PrincipalKeyStatusPending {
		t.Fatalf("identity activated before proof: principal=%s key=%s", result.Principal.Status, result.IdentityKey.Status)
	}
}

func TestCreateMachineInvitationRequiresAnActivePersonController(t *testing.T) {
	fixture := newManagementFixture(t)
	ctx := context.Background()
	if _, err := fixture.manager.CreateInvitation(ctx, CreateInvitationInput{
		Kind: store.PrincipalKindMachine, DisplayName: "Collector",
		ControllingPrincipalID: "missing-person",
		PublicKey:              testAuthorizedKey(t, "collector-invalid@test.invalid"),
	}); err == nil {
		t.Fatal("machine invitation with unknown controller succeeded")
	}
	result, err := fixture.manager.CreateInvitation(ctx, CreateInvitationInput{
		Kind: store.PrincipalKindMachine, DisplayName: "Collector",
		PublicKey: testAuthorizedKey(t, "collector@test.invalid"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Principal.ControllingPrincipalID != fixture.owner.ID {
		t.Fatalf("controller = %q, want local owner", result.Principal.ControllingPrincipalID)
	}
}

func TestRotateKeyInvitationCanRetryAPendingKey(t *testing.T) {
	fixture := newManagementFixture(t)
	ctx := context.Background()
	publicKey := testAuthorizedKey(t, "owner-retry@test.invalid")
	input := CreateInvitationInput{
		Purpose:     store.InvitationPurposeRotateKey,
		PrincipalID: fixture.owner.ID, PublicKey: publicKey,
	}
	first, err := fixture.manager.CreateInvitation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.manager.CreateInvitation(ctx, input)
	if err != nil {
		t.Fatalf("retry pending owner key: %v", err)
	}
	if first.IdentityKey.ID != second.IdentityKey.ID || first.Invitation.ID == second.Invitation.ID {
		t.Fatalf("retry should reuse pending key with a fresh invite: first=%#v second=%#v", first.IdentityKey, second.IdentityKey)
	}
	invites, err := fixture.db.ListPrincipalInvitations(ctx, fixture.owner.ID)
	if err != nil || len(invites) != 2 {
		t.Fatalf("owner invitations = %#v, %v", invites, err)
	}
	if !second.Invitation.ExpiresAt.After(time.Now().UTC()) {
		t.Fatal("retry invitation is not live")
	}
}

func TestRotateKeyReplacementTargetMustBelongToPrincipal(t *testing.T) {
	fixture := newManagementFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	foreign := &store.Principal{
		ID: "foreign-principal", Kind: store.PrincipalKindPerson,
		DisplayName: "Foreign", Status: store.PrincipalStatusActive, CreatedAt: now,
	}
	if err := fixture.db.CreatePrincipal(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	parsed, err := p2p.ParseIdentityPublicKey(testAuthorizedKey(t, "foreign@test.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	foreignKey := &store.PrincipalIdentityKey{
		ID: "foreign-key", PrincipalID: foreign.ID,
		CanonicalPublicKey: parsed.AuthorizedKey, Fingerprint: parsed.Fingerprint,
		Algorithm: parsed.Algorithm, Status: store.PrincipalKeyStatusActive,
		CreatedAt: now, VerifiedAt: &now,
	}
	if err := fixture.db.AddPrincipalIdentityKey(ctx, foreignKey); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.manager.CreateInvitation(ctx, CreateInvitationInput{
		Purpose: store.InvitationPurposeRotateKey, PrincipalID: fixture.owner.ID,
		PublicKey:     testAuthorizedKey(t, "owner-new@test.invalid"),
		ReplacesKeyID: foreignKey.ID,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("cross-principal replacement error = %v", err)
	}
}
