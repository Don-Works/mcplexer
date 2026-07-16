package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestCollaborationWorkspaceGrantsAreExactAndEpochBound(t *testing.T) {
	t.Parallel()
	fixture := newCollaborationFixture(t)
	collaborator := &store.Principal{
		ID: "principal-active", Kind: store.PrincipalKindPerson,
		DisplayName: "Active collaborator", Status: store.PrincipalStatusActive,
		CreatedAt: fixture.now,
	}
	if err := fixture.db.CreatePrincipal(fixture.ctx, collaborator); err != nil {
		t.Fatal(err)
	}
	set := store.WorkspaceGrantSet{
		ShareID: fixture.share.ShareID, PrincipalID: collaborator.ID,
		Capabilities:         []string{store.CapabilityTasksRead},
		ConstraintsJSON:      []byte(`{"redaction":"task-safe-v1"}`),
		CreatedByPrincipalID: fixture.owner.ID, At: fixture.now,
	}
	epoch, grants, err := fixture.db.SetWorkspaceGrants(fixture.ctx, set)
	if err != nil || epoch != 2 || len(grants) != 1 {
		t.Fatalf("first grant = epoch %d, %d grants, %v", epoch, len(grants), err)
	}
	epoch, _, err = fixture.db.SetWorkspaceGrants(fixture.ctx, set)
	if err != nil || epoch != 2 {
		t.Fatalf("idempotent grant epoch = %d, %v", epoch, err)
	}
	set.Capabilities = []string{store.CapabilityTasksComment, store.CapabilityTasksRead}
	set.At = fixture.now.Add(time.Minute)
	epoch, grants, err = fixture.db.SetWorkspaceGrants(fixture.ctx, set)
	if err != nil || epoch != 3 || len(grants) != 2 {
		t.Fatalf("replacement = epoch %d, %d grants, %v", epoch, len(grants), err)
	}
	for _, capability := range []string{store.CapabilityTasksRead, store.CapabilityTasksComment} {
		allowed, err := fixture.db.HasWorkspaceCapability(fixture.ctx, collaborator.ID, fixture.share.ShareID, capability, set.At)
		if err != nil || !allowed {
			t.Fatalf("capability %s = %v, %v", capability, allowed, err)
		}
	}
	allowed, err := fixture.db.HasWorkspaceCapability(fixture.ctx, collaborator.ID, fixture.share.ShareID, store.CapabilityTasksEdit, set.At)
	if err != nil || allowed {
		t.Fatalf("unstated edit capability = %v, %v", allowed, err)
	}
	set.Capabilities = []string{"tasks.*"}
	if _, _, err := fixture.db.SetWorkspaceGrants(fixture.ctx, set); err == nil {
		t.Fatal("wildcard capability was accepted")
	}
	events, err := fixture.db.ListCollaborationAudit(fixture.ctx, fixture.share.ShareID, "principal", collaborator.ID, 10)
	if err != nil || len(events) != 2 || events[0].Event != "workspace.grants.changed" {
		t.Fatalf("grant mutation audit = %#v, %v", events, err)
	}
}

func TestCollaborationPrincipalRevocationStopsFutureWorkspaceAccess(t *testing.T) {
	t.Parallel()
	fixture := newCollaborationFixture(t)
	machine := &store.Principal{
		ID: "principal-monitor", Kind: store.PrincipalKindMachine,
		DisplayName: "Log monitor", Status: store.PrincipalStatusActive,
		ControllingPrincipalID: fixture.owner.ID, CreatedAt: fixture.now,
	}
	if err := fixture.db.CreatePrincipal(fixture.ctx, machine); err != nil {
		t.Fatal(err)
	}
	set := store.WorkspaceGrantSet{
		ShareID: fixture.share.ShareID, PrincipalID: machine.ID,
		Capabilities:         []string{store.CapabilityTasksPublish},
		ConstraintsJSON:      []byte(`{"forced_visibility":"workspace","profile":"log-summary-v1"}`),
		CreatedByPrincipalID: fixture.owner.ID, At: fixture.now,
	}
	if _, _, err := fixture.db.SetWorkspaceGrants(fixture.ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.RevokePrincipal(fixture.ctx, machine.ID, "server retired", fixture.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.RevokePrincipal(fixture.ctx, machine.ID, "server retired", fixture.now.Add(2*time.Minute)); err != nil {
		t.Fatalf("principal revocation should be idempotent: %v", err)
	}
	allowed, err := fixture.db.HasWorkspaceCapability(fixture.ctx, machine.ID, fixture.share.ShareID, store.CapabilityTasksPublish, fixture.now.Add(2*time.Minute))
	if err != nil || allowed {
		t.Fatalf("revoked publisher capability = %v, %v", allowed, err)
	}
	share, err := fixture.db.GetWorkspaceShare(fixture.ctx, fixture.share.ShareID)
	if err != nil || share.AccessEpoch != 3 {
		t.Fatalf("revocation epoch = %#v, %v; want 3", share, err)
	}
	if err := fixture.db.RevokePrincipal(fixture.ctx, fixture.owner.ID, "should fail", fixture.now); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("local owner revocation error = %v", err)
	}
	events, err := fixture.db.ListCollaborationAudit(fixture.ctx, "", "principal", machine.ID, 10)
	if err != nil || len(events) == 0 || events[0].Event != "principal.revoked" {
		t.Fatalf("principal revocation audit = %#v, %v", events, err)
	}
}

func TestCollaborationPublicationPolicyAndAuditFiltering(t *testing.T) {
	t.Parallel()
	fixture := newCollaborationFixture(t)
	policy := &store.WorkspacePublicationPolicy{
		ShareID: fixture.share.ShareID, DefaultVisibility: store.TaskVisibilityPrivate,
		AgentVisibilityCeiling:   store.TaskVisibilityWorkspace,
		WideningRequiresApproval: true, EgressProfile: "log-summary-v1",
		UpdatedByPrincipalID: fixture.owner.ID, UpdatedAt: fixture.now,
	}
	if err := fixture.db.PutWorkspacePublicationPolicy(fixture.ctx, policy); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.db.GetWorkspacePublicationPolicy(fixture.ctx, fixture.share.ShareID)
	if err != nil || got.EgressProfile != policy.EgressProfile || !got.WideningRequiresApproval {
		t.Fatalf("policy = %#v, %v", got, err)
	}
	invalid := *policy
	invalid.DefaultVisibility = store.TaskVisibilityWorkspace
	invalid.AgentVisibilityCeiling = store.TaskVisibilityPrivate
	if err := fixture.db.PutWorkspacePublicationPolicy(fixture.ctx, &invalid); err == nil {
		t.Fatal("default visibility above ceiling was accepted")
	}
	remoteEvidence := *policy
	remoteEvidence.AllowRemoteEvidence = true
	if err := fixture.db.PutWorkspacePublicationPolicy(fixture.ctx, &remoteEvidence); err == nil {
		t.Fatal("unimplemented remote evidence policy was accepted")
	}
	policyEvents, err := fixture.db.ListCollaborationAudit(fixture.ctx, fixture.share.ShareID, "workspace", fixture.share.ShareID, 10)
	if err != nil || len(policyEvents) != 1 || policyEvents[0].Event != "workspace.policy.changed" {
		t.Fatalf("policy audit = %#v, %v", policyEvents, err)
	}
	events := []*store.CollaborationAuditEvent{
		{ShareID: fixture.share.ShareID, Event: "grant.changed", SubjectKind: "principal", SubjectID: "one", CreatedAt: fixture.now},
		{Event: "device.changed", SubjectKind: "device", SubjectID: "two", CreatedAt: fixture.now.Add(time.Second)},
	}
	for _, event := range events {
		if err := fixture.db.AppendCollaborationAudit(fixture.ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	filtered, err := fixture.db.ListCollaborationAudit(fixture.ctx, fixture.share.ShareID, "principal", "one", 10)
	if err != nil || len(filtered) != 1 || filtered[0].SubjectID != "one" {
		t.Fatalf("share-filtered audit = %#v, %v", filtered, err)
	}
}

func TestCollaborationTaskVisibilityIntersectsAudienceAndWorkspaceGrants(t *testing.T) {
	t.Parallel()
	fixture := newCollaborationFixture(t)
	reader := &store.Principal{
		ID: "principal-reader", Kind: store.PrincipalKindPerson,
		DisplayName: "Workspace reader", Status: store.PrincipalStatusActive,
		CreatedAt: fixture.now,
	}
	outsider := &store.Principal{
		ID: "principal-outsider", Kind: store.PrincipalKindPerson,
		DisplayName: "No access", Status: store.PrincipalStatusActive,
		CreatedAt: fixture.now,
	}
	for _, principal := range []*store.Principal{reader, outsider} {
		if err := fixture.db.CreatePrincipal(fixture.ctx, principal); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := fixture.db.SetWorkspaceGrants(fixture.ctx, store.WorkspaceGrantSet{
		ShareID: fixture.share.ShareID, PrincipalID: reader.ID,
		Capabilities:         []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
		CreatedByPrincipalID: fixture.owner.ID, At: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ID: "task-visible", WorkspaceID: fixture.share.LocalWorkspaceID,
		Title: "Investigate an incident", OwnerPrincipalID: fixture.owner.ID,
		SourceKind: store.TaskSourceUser, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.CreateTask(fixture.ctx, task); err != nil {
		t.Fatal(err)
	}
	if task.Visibility != store.TaskVisibilityPrivate || task.VisibilityEpoch != 1 {
		t.Fatalf("new task visibility = %s/%d", task.Visibility, task.VisibilityEpoch)
	}
	allowed, err := fixture.db.CanPrincipalReadTask(fixture.ctx, reader.ID, task.ID, fixture.now)
	if err != nil || allowed {
		t.Fatalf("reader saw private task = %v, %v", allowed, err)
	}
	access, err := fixture.db.SetTaskVisibility(fixture.ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: fixture.owner.ID, At: fixture.now.Add(time.Minute),
	})
	if err != nil || access.VisibilityEpoch != 2 {
		t.Fatalf("workspace visibility = %#v, %v", access, err)
	}
	allowed, err = fixture.db.CanPrincipalReadTask(fixture.ctx, reader.ID, task.ID, fixture.now.Add(time.Minute))
	if err != nil || !allowed {
		t.Fatalf("reader workspace access = %v, %v", allowed, err)
	}
	access, err = fixture.db.SetTaskVisibility(fixture.ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityRestricted,
		AudiencePrincipalIDs: []string{reader.ID}, ActorPrincipalID: fixture.owner.ID,
		At: fixture.now.Add(2 * time.Minute),
	})
	if err != nil || access.VisibilityEpoch != 3 || len(access.AudiencePrincipalIDs) != 1 {
		t.Fatalf("restricted visibility = %#v, %v", access, err)
	}
	if _, err := fixture.db.SetTaskVisibility(fixture.ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityRestricted,
		AudiencePrincipalIDs: []string{outsider.ID}, ActorPrincipalID: fixture.owner.ID,
		At: fixture.now.Add(3 * time.Minute),
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ungranted restricted audience error = %v", err)
	}
	access, err = fixture.db.SetTaskVisibility(fixture.ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityPrivate,
		ActorPrincipalID: fixture.owner.ID, At: fixture.now.Add(4 * time.Minute),
	})
	if err != nil || access.VisibilityEpoch != 4 {
		t.Fatalf("narrow private = %#v, %v", access, err)
	}
	loaded, err := fixture.db.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Visibility = store.TaskVisibilityWorkspace
	if err := fixture.db.UpdateTask(fixture.ctx, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = fixture.db.GetTask(fixture.ctx, task.ID)
	if err != nil || loaded.Visibility != store.TaskVisibilityPrivate {
		t.Fatalf("generic update changed visibility = %#v, %v", loaded, err)
	}
}
