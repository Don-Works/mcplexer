package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func TestTaskShareProviderRequiresBoundOfferAndCurrentTaskAccess(t *testing.T) {
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
	fakeGitHubToken := "ghp_" + "abcdefghijklmnopqrstuvwxyz123456"
	task, err := service.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspace.ID, OwnerPrincipalID: ownerID,
		Title:       "Investigate " + fakeGitHubToken,
		Description: "Authorization: Bearer secret-value-that-must-not-leak",
		Meta:        `{"raw_log":"never send"}`, SourceKind: store.TaskSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	access, err := db.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: ownerID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	share, err := db.GetWorkspaceShareByLocalWorkspaceID(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	offer := &store.TaskOffer{
		ID: "outgoing-offer", TaskID: task.ID, RemoteTaskID: task.ID,
		ShareID: share.ShareID, SenderPrincipalID: ownerID,
		AccessEpoch: share.AccessEpoch, VisibilityEpoch: access.VisibilityEpoch,
		FromPeerID: "peer-home", ToPeerID: "peer-reader",
		RemoteWorkspaceID: workspace.ID, Direction: "outgoing",
		State: store.TaskOfferPending, EnvelopeNonce: "one-use-offer-nonce",
		EnvelopeCreatedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateTaskOffer(ctx, offer); err != nil {
		t.Fatal(err)
	}
	provider := &taskShareProvider{store: db, authorizer: collaboration.NewAuthorizer(db)}
	if _, err := provider.GetTaskPayload(ctx, "peer-reader", "wrong-nonce", task.ID); !errors.Is(err, p2p.ErrTaskNotFound) {
		t.Fatalf("wrong nonce error = %v", err)
	}
	payload, err := provider.GetTaskPayload(ctx, "peer-reader", offer.EnvelopeNonce, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wireText := payload.Title + payload.Description + payload.Meta
	for _, forbidden := range []string{"ghp_", "secret-value", "raw_log"} {
		if strings.Contains(wireText, forbidden) {
			t.Fatalf("payload leaked %q: %#v", forbidden, payload)
		}
	}
	disclosures, err := db.ListTaskDisclosures(ctx, task.ID, 10)
	if err != nil || len(disclosures) != 1 || disclosures[0].RecipientPeerID != "peer-reader" {
		t.Fatalf("disclosures = %#v, %v", disclosures, err)
	}
	readerID := disclosures[0].RecipientPrincipalID
	if _, _, err := db.SetWorkspaceGrants(ctx, store.WorkspaceGrantSet{
		ShareID: share.ShareID, PrincipalID: readerID, Capabilities: nil,
		CreatedByPrincipalID: ownerID, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.GetTaskPayload(ctx, "peer-reader", offer.EnvelopeNonce, task.ID); !errors.Is(err, p2p.ErrTaskNotFound) {
		t.Fatalf("revoked access error = %v", err)
	}
}
