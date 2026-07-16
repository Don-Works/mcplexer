//go:build p2p

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func TestCollaborationReaderPullsOnlyVisibleSafeTasksIntoLocalMirror(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	homeHost := startCollaborationWireHost(t, "reader-home")
	defer func() { _ = homeHost.Close() }()
	readerHost := startCollaborationWireHost(t, "reader-device")
	defer func() { _ = readerHost.Close() }()
	connectCollaborationWireHosts(t, ctx, readerHost, homeHost)

	homeDB, homeWorkspace := newTaskSyncTestDB(t)
	readerDB, readerWorkspace := newTaskSyncTestDB(t)
	seedTaskSyncPrincipal(t, homeDB, homeWorkspace, readerHost.PeerID(), []string{
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
	})
	share, err := homeDB.GetWorkspaceShareByLocalWorkspaceID(ctx, homeWorkspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := readerDB.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: share.ShareID, HomePeerID: homeHost.PeerID(),
		RemoteWorkspaceID: homeWorkspace.ID, LocalWorkspaceID: readerWorkspace.ID,
		WorkspaceName: "Shared operations",
		Capabilities:  []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
		AccessEpoch:   share.AccessEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	homeTasks := tasks.New(homeDB)
	readerTasks := tasks.New(readerDB)
	homeSync := buildTaskSyncService(homeHost, homeDB, homeTasks, nil, nil, nil)
	readerSync := buildTaskSyncService(readerHost, readerDB, readerTasks, nil, nil, nil)
	defer homeSync.Stop()
	defer readerSync.Stop()
	fakeGitHubToken := "ghp_" + "abcdefghijklmnopqrstuvwxyz123456"
	publicTask, err := homeTasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: homeWorkspace.ID, OwnerPrincipalID: "sync-owner",
		Title:       "Investigate " + fakeGitHubToken,
		Description: "Authorization: Bearer secret-that-must-not-cross",
		Meta:        `{"raw_log":"not-for-reader"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := homeDB.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: publicTask.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: "sync-owner", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	privateTask, err := homeTasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: homeWorkspace.ID, OwnerPrincipalID: "sync-owner", Title: "Private task",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := readerSync.ConnectToPeer(ctx, homeHost.PeerID(), []p2p.TaskSyncHelloWorkspace{{
		WorkspaceID: homeWorkspace.ID, LocalWorkspaceID: readerWorkspace.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	mirrored, err := readerDB.GetTask(ctx, publicTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mirrored.WorkspaceID != readerWorkspace.ID || mirrored.Meta != "" ||
		strings.Contains(mirrored.Title+mirrored.Description, "ghp_") ||
		strings.Contains(mirrored.Description, "secret-that-must-not-cross") {
		t.Fatalf("unsafe mirror = %#v", mirrored)
	}
	if _, err := readerDB.GetTask(ctx, privateTask.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("private task reached reader: %v", err)
	}
	membership, err := readerDB.GetWorkspaceMembership(ctx, share.ShareID)
	if err != nil || membership.CursorHLC == "" {
		t.Fatalf("durable membership cursor = %#v, %v", membership, err)
	}

	if _, _, err := homeDB.SetWorkspaceGrants(ctx, store.WorkspaceGrantSet{
		ShareID: share.ShareID, PrincipalID: "sync-reader", Capabilities: nil,
		CreatedByPrincipalID: "sync-owner", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	afterRevoke, err := homeTasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: homeWorkspace.ID, OwnerPrincipalID: "sync-owner", Title: "After revoke",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := homeDB.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: afterRevoke.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: "sync-owner", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := readerSync.ConnectToPeer(ctx, homeHost.PeerID(), []p2p.TaskSyncHelloWorkspace{{
		WorkspaceID: homeWorkspace.ID, LocalWorkspaceID: readerWorkspace.ID,
		SinceHLC: membership.CursorHLC,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := readerDB.GetTask(ctx, afterRevoke.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-revocation task reached reader: %v", err)
	}
}

func TestCollaborationPublisherCanPushSanitizedTaskWithoutReadGrant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	homeHost := startCollaborationWireHost(t, "publisher-home")
	defer func() { _ = homeHost.Close() }()
	publisherHost := startCollaborationWireHost(t, "publisher-device")
	defer func() { _ = publisherHost.Close() }()
	connectCollaborationWireHosts(t, ctx, publisherHost, homeHost)

	homeDB, homeWorkspace := newTaskSyncTestDB(t)
	publisherDB, publisherWorkspace := newTaskSyncTestDB(t)
	seedTaskSyncPrincipal(t, homeDB, homeWorkspace, publisherHost.PeerID(), []string{
		store.CapabilityTasksPublish,
	})
	share, err := homeDB.GetWorkspaceShareByLocalWorkspaceID(ctx, homeWorkspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisherDB.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: share.ShareID, HomePeerID: homeHost.PeerID(),
		RemoteWorkspaceID: homeWorkspace.ID, LocalWorkspaceID: publisherWorkspace.ID,
		WorkspaceName: "Production summaries",
		Capabilities:  []string{store.CapabilityTasksPublish}, AccessEpoch: share.AccessEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	homeTasks := tasks.New(homeDB)
	publisherTasks := tasks.New(publisherDB)
	homeTasks.SetWorkspaceLookup(homeDB)
	publisherTasks.SetWorkspaceLookup(publisherDB)
	homeTasks.SetLocalPeerID(homeHost.PeerID())
	publisherTasks.SetLocalPeerID(publisherHost.PeerID())
	homeShare := buildTaskShareService(homeHost, homeDB, homeTasks, nil, nil, nil)
	publisherShare := buildTaskShareService(publisherHost, publisherDB, publisherTasks, nil, nil, nil)
	homeTasks.SetTaskShare(homeShare)
	publisherTasks.SetTaskShare(publisherShare)

	localTask, err := publisherTasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: publisherWorkspace.ID,
		Title:       "Log alert sk-live-abcdefghijklmnopqrstuvwxyz",
		Description: "Authorization: Bearer publisher-secret",
		Meta:        `{"raw_log":"must stay local"}`,
		SourceKind:  store.TaskSourceSystem,
	})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := publisherTasks.PublishToHome(
		ctx, publisherWorkspace.ID, localTask.ID, "sanitized monitor summary", "monitor-session",
	)
	if err != nil {
		t.Fatal(err)
	}
	if offer.State != store.TaskOfferAutoAccepted {
		t.Fatalf("publisher offer state = %q", offer.State)
	}
	homeOffers, err := homeDB.ListTaskOffers(ctx, store.TaskOfferFilter{
		Direction: "incoming", PeerID: publisherHost.PeerID(), Limit: 10,
	})
	if err != nil || len(homeOffers) != 1 || homeOffers[0].TaskID == "" {
		t.Fatalf("home offers = %#v, %v", homeOffers, err)
	}
	published, err := homeDB.GetTask(ctx, homeOffers[0].TaskID)
	if err != nil {
		t.Fatal(err)
	}
	wireText := published.Title + published.Description + published.Meta
	for _, secret := range []string{"sk-live-", "publisher-secret", "raw_log"} {
		if strings.Contains(wireText, secret) {
			t.Fatalf("published task leaked %q: %#v", secret, published)
		}
	}
	access, err := homeDB.GetTaskAccess(ctx, published.ID)
	if err != nil || access.Visibility != store.TaskVisibilityPrivate {
		t.Fatalf("publisher escaped home visibility policy: %#v, %v", access, err)
	}
	if allowed, err := homeDB.HasWorkspaceCapability(ctx, "sync-reader", share.ShareID, store.CapabilityTasksRead, time.Now()); err != nil || allowed {
		t.Fatalf("publisher gained read access: %v, %v", allowed, err)
	}

	if _, _, err := homeDB.SetWorkspaceGrants(ctx, store.WorkspaceGrantSet{
		ShareID: share.ShareID, PrincipalID: "sync-reader", Capabilities: nil,
		CreatedByPrincipalID: "sync-owner", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := publisherTasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: publisherWorkspace.ID, Title: "Must be rejected",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisherTasks.PublishToHome(
		ctx, publisherWorkspace.ID, second.ID, "", "monitor-session",
	); err == nil {
		t.Fatal("stale cached publisher grant succeeded after home revocation")
	}
}

func TestCollaborationEditorPublishesMirrorEditWithoutChangingVisibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	homeHost := startCollaborationWireHost(t, "editor-home")
	defer func() { _ = homeHost.Close() }()
	editorHost := startCollaborationWireHost(t, "editor-device")
	defer func() { _ = editorHost.Close() }()
	connectCollaborationWireHosts(t, ctx, editorHost, homeHost)

	homeDB, homeWorkspace := newTaskSyncTestDB(t)
	editorDB, editorWorkspace := newTaskSyncTestDB(t)
	capabilities := []string{
		store.CapabilityWorkspaceView,
		store.CapabilityTasksRead,
		store.CapabilityTasksEdit,
	}
	seedTaskSyncPrincipal(t, homeDB, homeWorkspace, editorHost.PeerID(), capabilities)
	share, err := homeDB.GetWorkspaceShareByLocalWorkspaceID(ctx, homeWorkspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := editorDB.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: share.ShareID, HomePeerID: homeHost.PeerID(),
		RemoteWorkspaceID: homeWorkspace.ID, LocalWorkspaceID: editorWorkspace.ID,
		WorkspaceName: "Shared engineering", Capabilities: capabilities,
		AccessEpoch: share.AccessEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	homeTasks := tasks.New(homeDB)
	editorTasks := tasks.New(editorDB)
	homeTasks.SetWorkspaceLookup(homeDB)
	editorTasks.SetWorkspaceLookup(editorDB)
	homeTasks.SetLocalPeerID(homeHost.PeerID())
	editorTasks.SetLocalPeerID(editorHost.PeerID())
	homeShare := buildTaskShareService(homeHost, homeDB, homeTasks, nil, nil, nil)
	editorShare := buildTaskShareService(editorHost, editorDB, editorTasks, nil, nil, nil)
	homeTasks.SetTaskShare(homeShare)
	editorTasks.SetTaskShare(editorShare)
	homeSync := buildTaskSyncService(homeHost, homeDB, homeTasks, nil, nil, nil)
	editorSync := buildTaskSyncService(editorHost, editorDB, editorTasks, nil, nil, nil)
	defer homeSync.Stop()
	defer editorSync.Stop()

	homeTask, err := homeTasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: homeWorkspace.ID, OwnerPrincipalID: "sync-owner",
		Title: "Original title", Description: "Original description",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := homeDB.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: homeTask.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: "sync-owner", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := editorSync.ConnectToPeer(ctx, homeHost.PeerID(), []p2p.TaskSyncHelloWorkspace{{
		WorkspaceID: homeWorkspace.ID, LocalWorkspaceID: editorWorkspace.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	mirror, err := editorDB.GetTask(ctx, homeTask.ID)
	if err != nil || mirror.OriginPeerID != homeHost.PeerID() {
		t.Fatalf("editor mirror = %#v, %v", mirror, err)
	}
	newTitle := "Editor-reviewed title"
	if _, err := editorTasks.Update(ctx, editorWorkspace.ID, mirror.ID, tasks.UpdatePatch{Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	if _, err := editorTasks.PublishToHome(ctx, editorWorkspace.ID, mirror.ID, "reviewed", "editor-session"); err != nil {
		t.Fatal(err)
	}
	updated, err := homeDB.GetTask(ctx, homeTask.ID)
	if err != nil || updated.Title != newTitle {
		t.Fatalf("home task after edit = %#v, %v", updated, err)
	}
	access, err := homeDB.GetTaskAccess(ctx, homeTask.ID)
	if err != nil || access.Visibility != store.TaskVisibilityWorkspace {
		t.Fatalf("content edit changed authoritative visibility: %#v, %v", access, err)
	}
	rows, err := homeDB.ListTasks(ctx, store.TaskFilter{WorkspaceID: homeWorkspace.ID, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("mirror edit duplicated the home task: %#v, %v", rows, err)
	}

	// Pull the accepted revision, edit locally, then race an intervening home
	// edit. Publishing the stale snapshot must surface a typed conflict and
	// leave the canonical home value untouched.
	if err := editorSync.ConnectToPeer(ctx, homeHost.PeerID(), []p2p.TaskSyncHelloWorkspace{{
		WorkspaceID: homeWorkspace.ID, LocalWorkspaceID: editorWorkspace.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	mirror, err = editorDB.GetTask(ctx, homeTask.ID)
	if err != nil || mirror.RemoteBaseHLC == "" {
		t.Fatalf("mirror base revision = %#v, %v", mirror, err)
	}
	staleTitle := "Stale editor title"
	if _, err := editorTasks.Update(ctx, editorWorkspace.ID, mirror.ID, tasks.UpdatePatch{Title: &staleTitle}); err != nil {
		t.Fatal(err)
	}
	homeWins := "Concurrent home title"
	if _, err := homeTasks.Update(ctx, homeWorkspace.ID, homeTask.ID, tasks.UpdatePatch{Title: &homeWins}); err != nil {
		t.Fatal(err)
	}
	if _, err := editorTasks.PublishToHome(ctx, editorWorkspace.ID, mirror.ID, "stale", "editor-session"); !errors.Is(err, p2p.ErrTaskConflict) {
		t.Fatalf("stale publish error = %v, want conflict", err)
	}
	updated, err = homeDB.GetTask(ctx, homeTask.ID)
	if err != nil || updated.Title != homeWins {
		t.Fatalf("stale edit overwrote home: %#v, %v", updated, err)
	}
	conflicts, err := editorDB.ListTaskOffers(ctx, store.TaskOfferFilter{
		Direction: "outgoing", State: store.TaskOfferConflict,
	})
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("outgoing conflict receipt = %#v, %v", conflicts, err)
	}
	if err := editorSync.ConnectToPeer(ctx, homeHost.PeerID(), []p2p.TaskSyncHelloWorkspace{{
		WorkspaceID: homeWorkspace.ID, LocalWorkspaceID: editorWorkspace.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	mirror, err = editorDB.GetTask(ctx, homeTask.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := homeDB.SetWorkspaceGrants(ctx, store.WorkspaceGrantSet{
		ShareID: share.ShareID, PrincipalID: "sync-reader",
		Capabilities:         []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
		CreatedByPrincipalID: "sync-owner", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	secondTitle := "Must not land"
	if _, err := editorTasks.Update(ctx, editorWorkspace.ID, mirror.ID, tasks.UpdatePatch{Title: &secondTitle}); err != nil {
		t.Fatal(err)
	}
	if _, err := editorTasks.PublishToHome(ctx, editorWorkspace.ID, mirror.ID, "", "editor-session"); err == nil {
		t.Fatal("stale cached edit capability succeeded after home revocation")
	}
	if err := editorSync.ConnectToPeer(ctx, homeHost.PeerID(), []p2p.TaskSyncHelloWorkspace{{
		WorkspaceID: homeWorkspace.ID, LocalWorkspaceID: editorWorkspace.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	membership, err := editorDB.GetWorkspaceMembership(ctx, share.ShareID)
	if err != nil || membership.AccessEpoch != share.AccessEpoch+1 ||
		hasCapability(membership.Capabilities, store.CapabilityTasksEdit) {
		t.Fatalf("refreshed membership = %#v, %v", membership, err)
	}
	thirdTitle := "Locally denied after access refresh"
	if _, err := editorTasks.Update(ctx, editorWorkspace.ID, mirror.ID, tasks.UpdatePatch{Title: &thirdTitle}); !errors.Is(err, tasks.ErrWorkspaceMembershipDenied) {
		t.Fatalf("post-refresh local edit error = %v", err)
	}
}

func startCollaborationWireHost(t *testing.T, name string) *p2p.Host {
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

func connectCollaborationWireHosts(t *testing.T, ctx context.Context, from, to *p2p.Host) {
	t.Helper()
	if len(to.Addrs()) == 0 {
		t.Fatal("target host has no listen address")
	}
	if _, err := from.Connect(ctx, fmt.Sprintf("%s/p2p/%s", to.Addrs()[0], to.PeerID())); err != nil {
		t.Fatal(err)
	}
}
