package main

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// anomalyFixture seeds the minimum real graph RecordMonitoringTriage validates
// against: a workspace, an auth scope, a remote host, a log source and one log
// template per masked shape.
func anomalyFixture(t *testing.T, templates ...string) (*sqlite.DB, string, []string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("auth scope: %v", err)
	}
	host := &store.RemoteHost{WorkspaceID: ws.ID, Name: "ip-prod-1",
		SSHUser: "logwatch", SSHHost: "100.64.0.3", AuthScopeID: scope.ID, Enabled: true}
	if err := db.CreateRemoteHost(ctx, host); err != nil {
		t.Fatalf("remote host: %v", err)
	}
	source := &store.LogSource{WorkspaceID: ws.ID, RemoteHostID: host.ID,
		Name: "api", Selector: "api", Enabled: true}
	if err := db.CreateLogSource(ctx, source); err != nil {
		t.Fatalf("log source: %v", err)
	}
	seen := time.Date(2026, 7, 17, 10, 2, 0, 0, time.UTC)
	ids := make([]string, 0, len(templates))
	for _, masked := range templates {
		id := distill.TemplateID(source.ID, masked)
		tpl := &store.LogTemplate{ID: id, SourceID: source.ID, Masked: masked,
			Severity: store.SeverityError, FirstSeen: seen, LastSeen: seen,
			SampleFirst: masked, SampleLast: masked}
		if _, err := db.UpsertLogTemplate(ctx, tpl, 1); err != nil {
			t.Fatalf("upsert template: %v", err)
		}
		ids = append(ids, id)
	}
	return db, ws.ID, ids
}

func ensureInput(wsID, sourceRelClass string, templateIDs []string, at time.Time) distill.IncidentInput {
	return distill.IncidentInput{
		WorkspaceID: wsID, ClassKey: sourceRelClass, Title: "new error-class log template",
		Body: "Template: db down\nFirst sample: db down", Severity: store.SeverityError,
		TemplateIDs: templateIDs, ObservedAt: at,
	}
}

// TestAnomalyEnsurerConverges proves the real machinery: N EnsureIncident calls
// for one template roll onto ONE incident and ONE task (occurrence ledger grows,
// no sibling filing), while a distinct template gets its own incident and task.
func TestAnomalyEnsurerConverges(t *testing.T) {
	ctx := context.Background()
	db, wsID, ids := anomalyFixture(t, "db down", "cache miss storm")
	ens := newAnomalyIncidentEnsurer(db, tasks.New(db))
	if ens == nil {
		t.Fatal("ensurer not constructed")
	}

	classA := "template:" + ids[0]
	base := time.Date(2026, 7, 17, 10, 5, 0, 0, time.UTC)

	var taskA, incidentA string
	for i := range 5 {
		// Later calls land in a fresh 15-minute occurrence bucket so the ledger
		// visibly rolls rather than merely being idempotent.
		ref, err := ens.EnsureIncident(ctx, ensureInput(wsID, classA, ids[:1], base.Add(time.Duration(i)*20*time.Minute)))
		if err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
		if i == 0 {
			taskA, incidentA = ref.TaskID, ref.IncidentID
			if !ref.NewIncident {
				t.Fatal("first ensure should report a new incident")
			}
			continue
		}
		if ref.TaskID != taskA || ref.IncidentID != incidentA {
			t.Fatalf("ensure %d diverged: task %q/%q incident %q/%q", i, ref.TaskID, taskA, ref.IncidentID, incidentA)
		}
		if ref.NewIncident {
			t.Fatalf("ensure %d re-created the incident", i)
		}
	}

	inc, err := db.GetMonitoringIncidentByClass(ctx, wsID, classA)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if inc.ID != incidentA || inc.OccurrenceCount != 5 {
		t.Fatalf("ledger did not roll: id=%s occurrences=%d want id=%s occurrences=5", inc.ID, inc.OccurrenceCount, incidentA)
	}
	if inc.TaskID != taskA {
		t.Fatalf("incident points at a different task: %q want %q", inc.TaskID, taskA)
	}

	tasksForClass, err := tasks.New(db).List(ctx, store.TaskFilter{
		WorkspaceID: wsID, MetaMatch: map[string]string{"logwatch_class": classA}, Limit: 50,
	})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasksForClass) != 1 {
		t.Fatalf("convergence broken: %d tasks for one class, want 1", len(tasksForClass))
	}

	// A distinct template is a distinct problem: its own incident and task.
	classB := "template:" + ids[1]
	refB, err := ens.EnsureIncident(ctx, ensureInput(wsID, classB, ids[1:2], base))
	if err != nil {
		t.Fatalf("ensure B: %v", err)
	}
	if refB.TaskID == taskA || refB.IncidentID == incidentA {
		t.Fatalf("distinct template merged into A: task=%q incident=%q", refB.TaskID, refB.IncidentID)
	}
}

// TestAnomalyEnsurerMarkNotifiedAndClose covers the two loop-closing operations
// the distiller drives: stamping the notification clock and closing on recovery.
func TestAnomalyEnsurerMarkNotifiedAndClose(t *testing.T) {
	ctx := context.Background()
	db, wsID, ids := anomalyFixture(t, "db down")
	ens := newAnomalyIncidentEnsurer(db, tasks.New(db))
	class := "template:" + ids[0]
	at := time.Date(2026, 7, 17, 10, 5, 0, 0, time.UTC)

	ref, err := ens.EnsureIncident(ctx, ensureInput(wsID, class, ids[:1], at))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := ens.MarkNotified(ctx, ref.IncidentID, store.SeverityError, at); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	inc, err := db.GetMonitoringIncidentByClass(ctx, wsID, class)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if inc.LastNotifiedAt == nil {
		t.Fatal("MarkNotified did not stamp last_notified_at; renotify sweep would double-fire")
	}

	// A blank incident id (the task-only rate-spike path) is a safe no-op.
	if err := ens.MarkNotified(ctx, "", store.SeverityError, at); err != nil {
		t.Fatalf("blank MarkNotified must no-op: %v", err)
	}

	if err := ens.CloseIncident(ctx, wsID, class, "recovered"); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed, err := tasks.New(db).Get(ctx, wsID, ref.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if closed.ClosedAt == nil {
		t.Fatal("CloseIncident left the canonical task open")
	}
}

// shareAnomalyWorkspace makes wsID a collaboration-shared workspace with a local
// owner, the minimum for a minted task to be widenable for peer replication.
func shareAnomalyWorkspace(t *testing.T, db *sqlite.DB, wsID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	owner := &store.Principal{ID: "local-owner", Kind: store.PrincipalKindPerson,
		DisplayName: "Owner", Status: store.PrincipalStatusActive, IsLocalOwner: true, CreatedAt: now}
	if err := db.CreatePrincipal(ctx, owner); err != nil {
		t.Fatalf("owner principal: %v", err)
	}
	share := &store.WorkspaceShare{ShareID: "anomaly-share", LocalWorkspaceID: wsID,
		HomePeerID: "self-peer", OwnerPrincipalID: owner.ID, CreatedAt: now}
	if err := db.CreateWorkspaceShare(ctx, share); err != nil {
		t.Fatalf("workspace share: %v", err)
	}
}

// TestAnomalyEnsurerWidensSharedIncidentTask: in a SHARED workspace, the minted
// incident task ends up workspace-visible so it replicates to mirrored peers —
// and stays widened across the ensurer's convergence path.
func TestAnomalyEnsurerWidensSharedIncidentTask(t *testing.T) {
	ctx := context.Background()
	db, wsID, ids := anomalyFixture(t, "db down")
	shareAnomalyWorkspace(t, db, wsID)
	svc := tasks.New(db)
	ens := newAnomalyIncidentEnsurer(db, svc)
	class := "template:" + ids[0]
	at := time.Date(2026, 7, 20, 12, 5, 0, 0, time.UTC)

	ref, err := ens.EnsureIncident(ctx, ensureInput(wsID, class, ids[:1], at))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	task, err := svc.Get(ctx, wsID, ref.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Visibility != store.TaskVisibilityWorkspace {
		t.Fatalf("incident task must be widened to workspace to replicate, got %q", task.Visibility)
	}

	// Convergence re-run: same class, still one task, still widened.
	if _, err := ens.EnsureIncident(ctx, ensureInput(wsID, class, ids[:1], at.Add(time.Hour))); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	task, _ = svc.Get(ctx, wsID, ref.TaskID)
	if task.Visibility != store.TaskVisibilityWorkspace {
		t.Fatalf("converged task must stay widened, got %q", task.Visibility)
	}
}

// TestAnomalyEnsurerNonSharedIncidentStaysPrivate: with no share there is no
// peer to replicate to, so the minted task is left private (no needless widening).
func TestAnomalyEnsurerNonSharedIncidentStaysPrivate(t *testing.T) {
	ctx := context.Background()
	db, wsID, ids := anomalyFixture(t, "db down")
	svc := tasks.New(db)
	ens := newAnomalyIncidentEnsurer(db, svc)
	at := time.Date(2026, 7, 20, 12, 5, 0, 0, time.UTC)

	ref, err := ens.EnsureIncident(ctx, ensureInput(wsID, "template:"+ids[0], ids[:1], at))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	task, err := svc.Get(ctx, wsID, ref.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Visibility != store.TaskVisibilityPrivate {
		t.Fatalf("non-shared incident task must stay private, got %q", task.Visibility)
	}
}
