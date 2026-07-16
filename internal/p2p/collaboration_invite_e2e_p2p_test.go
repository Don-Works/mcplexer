//go:build p2p

package p2p_test

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestCollaborationInviteBindsLivePeerUsingSSHAgentAndRejectsReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	homeHost := startCollaborationHost(t, "home")
	defer func() { _ = homeHost.Close() }()
	remoteHost := startCollaborationHost(t, "remote")
	defer func() { _ = remoteHost.Close() }()

	homeDB := openCollaborationDB(t)
	remoteDB := openCollaborationDB(t)
	homeTransport := p2p.NewCollaborationInviteService(homeHost, homeDB)
	remoteTransport := p2p.NewCollaborationInviteService(remoteHost, remoteDB)
	manager := collaboration.NewManager(homeDB, homeTransport)
	remoteManager := collaboration.NewManager(remoteDB, remoteTransport)
	owner, err := manager.EnsureLocalOwner(ctx, &store.User{
		UserID: "test-owner", DisplayName: "Test owner", IsSelf: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remoteManager.EnsureLocalOwner(ctx, &store.User{
		UserID: "remote-owner", DisplayName: "Remote owner", IsSelf: true,
	}); err != nil {
		t.Fatal(err)
	}
	workspace := &store.Workspace{Name: "incident-room", RootPath: "/tmp/incidents", Tags: json.RawMessage(`[]`)}
	if err := homeDB.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	shares, err := manager.EnsureWorkspaceShares(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shareID string
	for _, share := range shares {
		if share.LocalWorkspaceID == workspace.ID {
			shareID = share.ShareID
		}
	}
	if shareID == "" {
		t.Fatal("workspace share was not created")
	}

	publicKey := startTestSSHAgent(t)
	created, err := manager.CreateInvitation(ctx, collaboration.CreateInvitationInput{
		Kind: store.PrincipalKindPerson, DisplayName: "Remote collaborator",
		PublicKey: publicKey,
		WorkspaceGrants: []collaboration.InvitationGrantInput{{
			ShareID: shareID,
			Capabilities: []string{
				store.CapabilityWorkspaceView, store.CapabilityTasksRead,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := remoteManager.Join(ctx, p2p.CollaborationJoinOptions{
		Invitation: created.InviteCode, DeviceName: "Remote laptop", DeviceKind: "laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if joined.PrincipalID != created.Principal.ID || joined.Device.PeerID != remoteHost.PeerID() || len(joined.Grants) != 2 {
		t.Fatalf("join result = %#v", joined)
	}
	memberships, err := remoteDB.ListWorkspaceMemberships(ctx)
	if err != nil || len(memberships) != 1 || memberships[0].ShareID != shareID ||
		memberships[0].HomePeerID != homeHost.PeerID() {
		t.Fatalf("installed memberships = %#v, %v", memberships, err)
	}
	if _, err := remoteDB.GetWorkspace(ctx, memberships[0].LocalWorkspaceID); err != nil {
		t.Fatalf("local mirror workspace missing: %v", err)
	}
	device, principal, err := homeDB.ResolveActivePrincipalForPeer(ctx, remoteHost.PeerID())
	if err != nil || device.IdentityKeyID != created.IdentityKey.ID || principal.Status != store.PrincipalStatusActive {
		t.Fatalf("active binding = %#v %#v, %v", device, principal, err)
	}
	for _, capability := range []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead} {
		allowed, err := homeDB.HasWorkspaceCapability(ctx, principal.ID, shareID, capability, time.Now().UTC())
		if err != nil || !allowed {
			t.Fatalf("capability %s = %v, %v", capability, allowed, err)
		}
	}

	if _, err := remoteTransport.Join(ctx, p2p.CollaborationJoinOptions{
		Invitation: created.InviteCode, DeviceName: "Replay", DeviceKind: "laptop",
	}); err == nil {
		t.Fatal("consumed invitation replay succeeded")
	}
	if err := homeDB.RevokePrincipalDevice(ctx, remoteHost.PeerID(), "test revocation", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := homeDB.ResolveActivePrincipalForPeer(ctx, remoteHost.PeerID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked device still resolves: %v", err)
	}
	if owner.Status != store.PrincipalStatusActive {
		t.Fatal("remote revocation affected local owner")
	}
}

func startCollaborationHost(t *testing.T, name string) *p2p.Host {
	t.Helper()
	host, err := p2p.NewHost(context.Background(), p2p.Config{
		Enabled: true, IdentityPath: filepath.Join(t.TempDir(), name+".key"),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		EnableMDNS:  false, EnableHolePunch: false,
		EnableRelayClient: false, EnableAutoNAT: false,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func openCollaborationDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func startTestSSHAgent(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey, Comment: "collaboration-test"}); err != nil {
		t.Fatal(err)
	}
	agentDir, err := os.MkdirTemp("", "mcpx-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(agentDir) })
	socket := filepath.Join(agentDir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close() //nolint:errcheck
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", socket)
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	public, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))) + " collaboration-test"
}
