// task_sync_wire_test.go — coverage for the cmd-layer glue behind the
// /mcplexer/task-sync/1.0.0 gossip protocol: the daemon-side service
// construction (smoke), the pair checker, the per-workspace scope gate,
// and the store-backed event source. Runs in BOTH build flavours — in
// the slim build the constructor returns the stub, which is exactly the
// "daemon constructs the service" contract being pinned.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newTaskSyncTestDB returns an in-memory sqlite store + a seeded
// workspace id.
func newTaskSyncTestDB(t *testing.T) (*sqlite.DB, *store.Workspace) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w := &store.Workspace{Name: "sync-ws", RootPath: "/tmp/sync", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return d, w
}

// TestBuildTaskSyncServiceSmoke pins the wiring contract serve.go
// relies on: a non-nil service in every build mode, even with a nil
// host (p2p disabled), so d.defer_(d.taskSync.Stop) never panics.
func TestBuildTaskSyncServiceSmoke(t *testing.T) {
	d, _ := newTaskSyncTestDB(t)
	svc := buildTaskSyncService(nil, d, tasks.New(d), nil, nil, nil)
	if svc == nil {
		t.Fatalf("buildTaskSyncService returned nil")
	}
	svc.Stop() // must not panic in either build mode
}

// TestTaskSyncPairChecker covers the outer ACL: legacy pairing is not
// identity; only a proof-bound active device is admitted.
func TestTaskSyncPairChecker(t *testing.T) {
	ctx := context.Background()
	d, ws := newTaskSyncTestDB(t)
	checker := &taskSyncPairChecker{authorizer: collaboration.NewAuthorizer(d)}

	if ok, err := checker.IsPaired(ctx, "peer-unknown"); err != nil || ok {
		t.Fatalf("unknown peer: got (%v, %v), want (false, nil)", ok, err)
	}
	if err := d.AddPeer(ctx, &store.P2PPeer{PeerID: "peer-A", DisplayName: "a"}); err != nil {
		t.Fatalf("add peer: %v", err)
	}
	if ok, err := checker.IsPaired(ctx, "peer-A"); err != nil || ok {
		t.Fatalf("legacy paired peer: got (%v, %v), want (false, nil)", ok, err)
	}
	seedTaskSyncPrincipal(t, d, ws, "peer-proof", nil)
	if ok, err := checker.IsPaired(ctx, "peer-proof"); err != nil || !ok {
		t.Fatalf("proof-bound peer: got (%v, %v), want (true, nil)", ok, err)
	}
	if err := d.RevokePrincipalDevice(ctx, "peer-proof", "test revocation", time.Now()); err != nil {
		t.Fatalf("revoke device: %v", err)
	}
	if ok, err := checker.IsPaired(ctx, "peer-proof"); err != nil || ok {
		t.Fatalf("revoked proof-bound peer: got (%v, %v), want (false, nil)", ok, err)
	}
}

// TestTaskSyncScopeChecker_ExactCapabilities proves legacy scopes and
// partial capability sets do not authorize task replication.
func TestTaskSyncScopeChecker_ExactCapabilities(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name         string
		capabilities []string
		legacyScope  string
		want         bool
	}{
		{name: "no grant denies"},
		{name: "view alone denies", capabilities: []string{store.CapabilityWorkspaceView}},
		{name: "read alone denies", capabilities: []string{store.CapabilityTasksRead}},
		{name: "legacy wildcard denies", legacyScope: "task_sync:*"},
		{name: "exact view and read admit", capabilities: []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ws := newTaskSyncTestDB(t)
			seedTaskSyncPrincipal(t, d, ws, "peer-B", tc.capabilities)
			if tc.legacyScope != "" {
				if err := d.AddPeer(ctx, &store.P2PPeer{PeerID: "peer-B", DisplayName: "legacy"}); err != nil {
					t.Fatalf("add legacy peer label: %v", err)
				}
				if err := d.GrantPeerScope(ctx, "peer-B", tc.legacyScope); err != nil {
					t.Fatalf("grant legacy scope: %v", err)
				}
			}
			checker := &taskSyncScopeChecker{authorizer: collaboration.NewAuthorizer(d)}
			ok, err := checker.HasTaskSyncScope(ctx, "peer-B", ws.ID)
			if err != nil {
				t.Fatalf("HasTaskSyncScope: %v", err)
			}
			if ok != tc.want {
				t.Fatalf("HasTaskSyncScope = %v, want %v", ok, tc.want)
			}
		})
	}
}

func seedTaskSyncPrincipal(t *testing.T, d *sqlite.DB, ws *store.Workspace, peerID string, capabilities []string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	owner := &store.Principal{ID: "sync-owner", Kind: store.PrincipalKindPerson, DisplayName: "Owner", Status: store.PrincipalStatusActive, IsLocalOwner: true, CreatedAt: now}
	if err := d.CreatePrincipal(ctx, owner); err != nil {
		t.Fatal(err)
	}
	share := &store.WorkspaceShare{ShareID: "sync-share", LocalWorkspaceID: ws.ID, HomePeerID: "peer-home", OwnerPrincipalID: owner.ID, CreatedAt: now}
	if err := d.CreateWorkspaceShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	principal := &store.Principal{ID: "sync-reader", Kind: store.PrincipalKindPerson, DisplayName: "Reader", Status: store.PrincipalStatusActive, CreatedAt: now}
	if err := d.CreatePrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	verified := now
	key := &store.PrincipalIdentityKey{ID: "sync-key", PrincipalID: principal.ID, CanonicalPublicKey: "ssh-ed25519 AAAASyncTest", Fingerprint: "SHA256:sync-test", Algorithm: "ssh-ed25519", Status: store.PrincipalKeyStatusActive, CreatedAt: now, VerifiedAt: &verified}
	if err := d.AddPrincipalIdentityKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	token := sha256.Sum256([]byte("sync-invitation"))
	invite := &store.PrincipalInvitation{ID: "sync-invite", TokenHash: token[:], Purpose: store.InvitationPurposeAddDevice, PrincipalID: principal.ID, IdentityKeyID: key.ID, CreatedByPrincipalID: owner.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := d.CreatePrincipalInvitation(ctx, invite, nil); err != nil {
		t.Fatal(err)
	}
	nonce := sha256.Sum256([]byte("sync-nonce"))
	transcript := sha256.Sum256([]byte("sync-transcript"))
	challenge := &store.PrincipalIdentityChallenge{ID: "sync-challenge", InvitationID: invite.ID, InitiatorPeerID: peerID, ResponderPeerID: "peer-home", NonceHash: nonce[:], TranscriptHash: hex.EncodeToString(transcript[:]), IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := d.CreatePrincipalIdentityChallenge(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	_, _, err := d.ActivateInvitedDevice(ctx, store.InvitedDeviceActivation{InvitationID: invite.ID, InvitationTokenHash: token[:], ChallengeID: challenge.ID, PeerID: peerID, ResponderPeerID: "peer-home", DisplayName: "Device", DeviceKind: "laptop", BindingVersion: store.DeviceBindingVersionV1, BindingTranscriptHash: challenge.TranscriptHash, BindingSignature: []byte("verified"), At: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(capabilities) > 0 {
		if _, _, err := d.SetWorkspaceGrants(ctx, store.WorkspaceGrantSet{ShareID: share.ShareID, PrincipalID: principal.ID, Capabilities: capabilities, CreatedByPrincipalID: owner.ID, At: now}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTaskSyncSource_ListAndWatermark pins the store→wire conversion:
// rows page out in HLC order as decodable events, the since-watermark
// filters already-seen rows, and MaxHLCForWorkspace tracks the newest
// stamp.
func TestTaskSyncSource_ListAndWatermark(t *testing.T) {
	ctx := context.Background()
	d, ws := newTaskSyncTestDB(t)
	seedTaskSyncPrincipal(t, d, ws, "peer-reader", []string{
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
	})
	svc := tasks.New(d)

	first, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: ws.ID, OwnerPrincipalID: "sync-owner",
		Title: "first", CreatedBySessionID: "s",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: ws.ID, OwnerPrincipalID: "sync-owner",
		Title: "second", CreatedBySessionID: "s",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	for _, taskID := range []string{first.ID, second.ID} {
		if _, err := d.SetTaskVisibility(ctx, store.TaskVisibilityChange{
			TaskID: taskID, Visibility: store.TaskVisibilityWorkspace,
			ActorPrincipalID: "sync-owner", At: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("publish %s: %v", taskID, err)
		}
	}
	src := &taskSyncSource{
		store: d, authorizer: collaboration.NewAuthorizer(d), selfPeerID: "self-peer",
	}
	evts, err := src.ListTasksSinceHLC(ctx, "peer-reader", ws.ID, "", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evts) != 2 {
		t.Fatalf("got %d events, want 2", len(evts))
	}
	if evts[0].TaskID != first.ID || evts[1].TaskID != second.ID {
		t.Fatalf("HLC order wrong: %s, %s", evts[0].TaskID, evts[1].TaskID)
	}
	var patch tasks.RemoteTaskPatch
	if err := json.Unmarshal(evts[0].FieldPatchesJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch.Title != "first" {
		t.Fatalf("patch title = %q, want first", patch.Title)
	}
	if evts[0].ByPeer != "self-peer" {
		t.Fatalf("by_peer = %q, want self-peer (local rows attribute to self)", evts[0].ByPeer)
	}

	// Watermark filtering — only rows newer than the first stamp.
	tail, err := src.ListTasksSinceHLC(ctx, "peer-reader", ws.ID, evts[0].HLC, 100)
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(tail) != 1 || tail[0].TaskID != second.ID {
		t.Fatalf("since-filter wrong: %+v", tail)
	}

	max, err := src.MaxHLCForWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("max hlc: %v", err)
	}
	if max != evts[1].HLC {
		t.Fatalf("max = %q, want %q", max, evts[1].HLC)
	}
}

func TestTaskSyncSourceFiltersPrivateTasksAndRecordsSafeDisclosure(t *testing.T) {
	ctx := context.Background()
	db, workspace := newTaskSyncTestDB(t)
	seedTaskSyncPrincipal(t, db, workspace, "peer-reader", []string{
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
	})
	principals, err := db.ListPrincipals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := ""
	for _, principal := range principals {
		if principal.IsLocalOwner {
			ownerID = principal.ID
		}
	}
	service := tasks.New(db)
	task, err := service.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspace.ID, OwnerPrincipalID: ownerID,
		Title: "Private incident", Description: "Authorization: Bearer hidden-secret-value",
		Meta: `{"raw":"private"}`, SourceKind: store.TaskSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := collaboration.NewAuthorizer(db)
	source := &taskSyncSource{store: db, authorizer: authorizer, selfPeerID: "peer-home"}
	events, err := source.ListTasksSinceHLC(ctx, "peer-reader", workspace.ID, "", 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("private task events = %#v, %v", events, err)
	}
	if _, err := db.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: ownerID, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	events, err = source.ListTasksSinceHLC(ctx, "peer-reader", workspace.ID, "", 100)
	if err != nil || len(events) != 1 {
		t.Fatalf("workspace task events = %#v, %v", events, err)
	}
	text := string(events[0].FieldPatchesJSON)
	for _, forbidden := range []string{"hidden-secret-value", "raw"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sync projection leaked %q: %s", forbidden, text)
		}
	}
	disclosures, err := db.ListTaskDisclosures(ctx, task.ID, 10)
	if err != nil || len(disclosures) != 1 {
		t.Fatalf("sync disclosures = %#v, %v", disclosures, err)
	}
}

func TestTaskSyncSinkRequiresMembershipHomeAndAppliesHomeAuthoritatively(t *testing.T) {
	ctx := context.Background()
	db, workspace := newTaskSyncTestDB(t)
	if err := db.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: "remote-share", HomePeerID: "peer-home",
		RemoteWorkspaceID: "remote-workspace", LocalWorkspaceID: workspace.ID,
		WorkspaceName: "Shared operations",
		Capabilities:  []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
		AccessEpoch:   1,
	}); err != nil {
		t.Fatal(err)
	}
	service := tasks.New(db)
	// Seed directly: a reader membership correctly forbids creating this
	// state through the service, but the authoritative-apply test needs an
	// existing row to model an older/corrupt accidental local edit.
	task := &store.Task{WorkspaceID: workspace.ID, Title: "local accidental edit"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	patch, _ := json.Marshal(tasks.RemoteTaskPatch{Title: "home canonical value", Status: "open"})
	event := p2p.TaskSyncEvent{
		TaskID: task.ID, WorkspaceID: workspace.ID,
		HLC: "00000000000000000000000000000001", ByPeer: "peer-home",
		FieldPatchesJSON: patch,
	}
	sink := &taskSyncSink{memberships: db, tasks: service}
	if err := sink.ApplyRemoteEvent(ctx, "peer-attacker", event); !errors.Is(err, collaboration.ErrDenied) {
		t.Fatalf("non-home apply error = %v", err)
	}
	if err := sink.ApplyRemoteEvent(ctx, "peer-home", event); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "home canonical value" || got.HlcAt != event.HLC {
		t.Fatalf("authoritative task = %#v", got)
	}
	membership, err := db.GetWorkspaceMembership(ctx, "remote-share")
	if err != nil || membership.CursorHLC != event.HLC {
		t.Fatalf("cursor = %#v, %v", membership, err)
	}
}

func TestTaskSyncAccessRefreshesEpochCapabilitiesAndRevocation(t *testing.T) {
	ctx := context.Background()
	homeDB, homeWorkspace := newTaskSyncTestDB(t)
	seedTaskSyncPrincipal(t, homeDB, homeWorkspace, "peer-reader", []string{
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
	})
	source := &taskSyncSource{store: homeDB, authorizer: collaboration.NewAuthorizer(homeDB)}
	access, err := source.WorkspaceAccess(ctx, "peer-reader", homeWorkspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if access.AccessEpoch < 1 || len(access.Capabilities) != 2 {
		t.Fatalf("initial access = %#v", access)
	}

	remoteDB, remoteWorkspace := newTaskSyncTestDB(t)
	if err := remoteDB.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: access.ShareID, HomePeerID: "peer-home",
		RemoteWorkspaceID: homeWorkspace.ID, LocalWorkspaceID: remoteWorkspace.ID,
		WorkspaceName: "Shared operations", Capabilities: []string{store.CapabilityTasksRead},
		AccessEpoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	sink := &taskSyncSink{memberships: remoteDB, tasks: tasks.New(remoteDB)}
	access.LocalWorkspaceID = remoteWorkspace.ID
	if err := sink.ApplyWorkspaceAccess(ctx, "peer-home", *access); err != nil {
		t.Fatal(err)
	}
	got, err := remoteDB.GetWorkspaceMembership(ctx, access.ShareID)
	if err != nil || got.AccessEpoch != access.AccessEpoch || len(got.Capabilities) != 2 {
		t.Fatalf("refreshed membership = %#v, %v", got, err)
	}

	access.Status = store.WorkspaceShareStatusRevoked
	if err := sink.ApplyWorkspaceAccess(ctx, "peer-home", *access); err != nil {
		t.Fatal(err)
	}
	got, err = remoteDB.GetWorkspaceMembership(ctx, access.ShareID)
	if err != nil || got.Status != store.WorkspaceShareStatusRevoked {
		t.Fatalf("revoked membership = %#v, %v", got, err)
	}
}
