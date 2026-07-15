// service_mesh_test.go — end-to-end confirmation that Service
// mutations fire the mesh task_event emitter alongside the legacy
// SSE Bus. The two paths are complementary: bus drives the dashboard,
// emitter drives mesh peers + worker triggers.
package tasks_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// recordingSender satisfies tasks.MeshSender by stashing every Send
// call. The sync.Mutex isn't required for the single-threaded tests
// below but guards against the day someone parallelises the suite.
type recordingSender struct {
	mu    sync.Mutex
	calls []mesh.SendRequest
}

func (r *recordingSender) Send(_ context.Context, _ mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	return &store.MeshMessage{ID: "synth", Kind: req.Kind, Tags: req.Tags}, nil
}

func (r *recordingSender) snapshot() []mesh.SendRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]mesh.SendRequest, len(r.calls))
	copy(out, r.calls)
	return out
}

func newSvcWithEmitter(t *testing.T) (*tasks.Service, *sqlite.DB, string, *recordingSender) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w := &store.Workspace{Name: "ws1", RootPath: "/tmp/ws1", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	svc := tasks.New(d)
	sender := &recordingSender{}
	svc.SetEmitter(tasks.NewEmitter(sender))
	return svc, d, w.ID, sender
}

func TestServiceCreate_FiresTaskEventCreated(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	_, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Wire it up", CreatedBySessionID: "agent-a",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 emit, got %d", len(calls))
	}
	if calls[0].Kind != "task_event" {
		t.Errorf("Kind = %q, want task_event", calls[0].Kind)
	}
	if !strings.Contains(calls[0].Tags, "task_event:created") {
		t.Errorf("Tags missing created: %q", calls[0].Tags)
	}
}

func TestServiceCreate_WithAssigneeFiresBothCreatedAndAssigned(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	_, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              "Hand-off",
		CreatedBySessionID: "agent-a",
		Assignee:           &tasks.Assignee{SessionID: "morgan"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 emits (created + assigned), got %d: %+v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Tags, "task_event:created") {
		t.Errorf("first emit not created: %q", calls[0].Tags)
	}
	if !strings.Contains(calls[1].Tags, "task_event:assigned") {
		t.Errorf("second emit not assigned: %q", calls[1].Tags)
	}
	if !calls[1].NotifyUser {
		t.Errorf("assigned to different session must notify, got NotifyUser=false")
	}
}

func TestServiceUpdate_StatusChangedFiresStatusEvent(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "t", CreatedBySessionID: "agent-a",
	})
	sender.mu.Lock()
	sender.calls = nil // discard the created emit
	sender.mu.Unlock()
	doing := "doing"
	if _, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status:             &doing,
		UpdatedBySessionID: "agent-a",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Tags, "task_event:status_changed") {
		t.Errorf("missing status_changed tag: %q", calls[0].Tags)
	}
	if calls[0].NotifyUser {
		t.Errorf("status_changed must not notify (locked decision #5)")
	}
}

func TestSweepExpiredNonWorkingLeaseDoesNotEmitStatusChanged(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "blocked lease", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "owner-session", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	blocked := "blocked"
	if _, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status: &blocked, UpdatedBySessionID: "owner-session",
	}); err != nil {
		t.Fatalf("update blocked: %v", err)
	}
	if _, err := db.HeartbeatTask(ctx, t1.ID, "owner-session", -1*time.Hour); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	if swept, err := svc.SweepExpiredLeases(ctx); err != nil || swept != 1 {
		t.Fatalf("SweepExpiredLeases = (%d, %v), want (1, nil)", swept, err)
	}

	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected one mesh emit for lease cleanup, got %d: %+v", len(calls), calls)
	}
	if strings.Contains(calls[0].Tags, "task_event:status_changed") {
		t.Fatalf("non-working lease cleanup must not emit status_changed: %+v", calls[0])
	}
	if !strings.Contains(calls[0].Tags, "task_event:updated") {
		t.Fatalf("expected generic updated event, got %+v", calls[0])
	}
}

func TestServiceUpdate_TerminalFiresClosed(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "t", CreatedBySessionID: "agent-a",
		Assignee: &tasks.Assignee{SessionID: "owner"},
	})
	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	terminal := true
	done := "done"
	if _, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status: &done, Terminal: &terminal, UpdatedBySessionID: "other",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Tags, "task_event:closed") {
		t.Errorf("expected task_event:closed, got %q", calls[0].Tags)
	}
	if !calls[0].NotifyUser {
		t.Errorf("close by non-owner must notify")
	}
}

func TestServiceClaim_FiresClaimedEvent(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Up for grabs", CreatedBySessionID: "system",
	})
	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "alice", ""); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Update fires assigned + status_changed merged into one event;
	// then Claim itself fires task_event:claimed. Find the claimed.
	calls := sender.snapshot()
	found := false
	for _, c := range calls {
		if strings.Contains(c.Tags, "task_event:claimed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected at least one task_event:claimed in %+v", calls)
	}
}

func TestServiceDelete_FiresDeletedEvent(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Doomed", CreatedBySessionID: "agent-a",
	})
	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	if err := svc.Delete(ctx, wsID, t1.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || !strings.Contains(calls[0].Tags, "task_event:deleted") {
		t.Fatalf("expected single deleted emit, got %+v", calls)
	}
}

func TestServiceAppendNote_FiresNoteAppended(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "with notes", CreatedBySessionID: "agent-a",
	})
	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	if _, err := svc.AppendNote(ctx, wsID, t1.ID, "first note", "agent-a", store.TaskSourceAgent); err != nil {
		t.Fatalf("AppendNote: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || !strings.Contains(calls[0].Tags, "task_event:note_appended") {
		t.Fatalf("expected note_appended emit, got %+v", calls)
	}
}

// TestServiceUpdate_NoOpStillEmitsUpdated confirms a patch with no
// recognised changes still fires task_event:updated so dashboards that
// poll on the mesh don't miss touched_at-style refreshes.
func TestServiceUpdate_NoOpStillEmitsUpdated(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "noop test", CreatedBySessionID: "agent-a",
	})
	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	title := "noop test renamed"
	if _, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Title: &title, UpdatedBySessionID: "agent-a",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || !strings.Contains(calls[0].Tags, "task_event:updated") {
		t.Fatalf("expected single updated emit, got %+v", calls)
	}
}

// TestServiceUpdate_ChainDepthFromTriggeringPropagates wires a
// triggering MeshMessage with chain-depth:2 onto an Update and asserts
// the emission stamps chain-depth:3.
func TestServiceUpdate_ChainDepthFromTriggeringPropagates(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "chained", CreatedBySessionID: "agent-a",
	})
	sender.mu.Lock()
	sender.calls = nil
	sender.mu.Unlock()
	doing := "doing"
	if _, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status:             &doing,
		UpdatedBySessionID: "agent-a",
		Triggering:         &store.MeshMessage{Tags: "chain-depth:2"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 emit, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Tags, "chain-depth:3") {
		t.Errorf("expected chain-depth:3 propagation, got %q", calls[0].Tags)
	}
}

// TestServiceCreate_ActorKindLandsOnEmission confirms CreateOptions.ActorKind
// flows through to the mesh.SendRequest, so the daemon can later
// label REST callers (user) vs MCP callers (agent) vs worker subs.
func TestServiceCreate_ActorKindLandsOnEmission(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID, sender := newSvcWithEmitter(t)
	if _, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              "REST birth",
		CreatedBySessionID: "rest",
		ActorKind:          "user",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 emit, got %d", len(calls))
	}
	if calls[0].ActorKind != "user" {
		t.Errorf("ActorKind = %q, want user", calls[0].ActorKind)
	}
}
