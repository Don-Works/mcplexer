package admin_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// fakeBridge records every Ensure / Remove call so admin tests can
// assert the bridge is invoked at the right moments.
type fakeBridge struct {
	mu      sync.Mutex
	ensures []ensuredWorker
	removes []string
	err     error
}

type ensuredWorker struct {
	id      string
	spec    string
	enabled bool
}

func (b *fakeBridge) EnsureForWorker(_ context.Context, w *store.Worker) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.ensures = append(b.ensures, ensuredWorker{id: w.ID, spec: w.ScheduleSpec, enabled: w.Enabled})
	return nil
}

func (b *fakeBridge) RemoveForWorker(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.removes = append(b.removes, id)
	return nil
}

func (b *fakeBridge) ensureCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.ensures)
}

func (b *fakeBridge) removeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.removes)
}

// newServiceWithBridge mirrors newTestService but installs a fake
// bridge on the admin Service before returning.
func newServiceWithBridge(t *testing.T) (*admin.Service, *fakeBridge, string, string) {
	t.Helper()
	svc, _, wsID, scopeID := newTestService(t)
	br := &fakeBridge{}
	svc.SetScheduleBridge(br)
	return svc, br, wsID, scopeID
}

func TestServiceCreateInvokesBridge(t *testing.T) {
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if br.ensureCount() != 1 {
		t.Fatalf("ensure called %d times, want 1", br.ensureCount())
	}
	got := br.ensures[0]
	if got.id != w.ID {
		t.Errorf("ensure worker_id = %q, want %q", got.id, w.ID)
	}
	if got.spec != w.ScheduleSpec {
		t.Errorf("ensure schedule_spec = %q, want %q", got.spec, w.ScheduleSpec)
	}
	if !got.enabled {
		t.Error("ensure called with enabled=false, want true")
	}
}

func TestServiceCreateDisabledSkipsBridge(t *testing.T) {
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	ctx := context.Background()
	in := baseCreate(wsID, scopeID)
	disabled := false
	in.Enabled = &disabled
	if _, err := svc.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if br.ensureCount() != 0 {
		t.Errorf("ensure should not fire for disabled-on-create, got %d", br.ensureCount())
	}
}

func TestServiceUpdateScheduleResyncs(t *testing.T) {
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	br.mu.Lock()
	br.ensures = nil
	br.mu.Unlock()

	newSpec := "*/5 * * * *"
	if _, err := svc.Update(ctx, admin.UpdateInput{ID: w.ID, ScheduleSpec: &newSpec}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if br.ensureCount() != 1 {
		t.Fatalf("ensure on update fired %d times, want 1", br.ensureCount())
	}
	if br.ensures[0].spec != newSpec {
		t.Errorf("ensure spec = %q, want %q", br.ensures[0].spec, newSpec)
	}
}

func TestServiceUpdateDisableRemoves(t *testing.T) {
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	br.mu.Lock()
	br.ensures = nil
	br.removes = nil
	br.mu.Unlock()

	off := false
	if _, err := svc.Update(ctx, admin.UpdateInput{ID: w.ID, Enabled: &off}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if br.removeCount() != 1 {
		t.Errorf("remove on disable-via-update fired %d times, want 1", br.removeCount())
	}
}

func TestServiceDeleteRemovesBridge(t *testing.T) {
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if br.removeCount() != 1 {
		t.Errorf("remove on delete fired %d times, want 1", br.removeCount())
	}
	if br.removes[0] != w.ID {
		t.Errorf("remove called with id %q, want %q", br.removes[0], w.ID)
	}
}

func TestServiceSetEnabledTogglesBridge(t *testing.T) {
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Clear the create-time ensure so we can assert pause + resume cleanly.
	br.mu.Lock()
	br.ensures = nil
	br.removes = nil
	br.mu.Unlock()

	if _, err := svc.SetEnabled(ctx, w.ID, false); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if br.removeCount() != 1 {
		t.Errorf("remove on pause = %d, want 1", br.removeCount())
	}
	if _, err := svc.SetEnabled(ctx, w.ID, false); err != nil {
		t.Fatalf("idempotent pause: %v", err)
	}
	if br.removeCount() != 2 {
		t.Errorf("remove on idempotent pause = %d, want 2", br.removeCount())
	}
	if _, err := svc.SetEnabled(ctx, w.ID, true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if br.ensureCount() != 1 {
		t.Errorf("ensure on resume = %d, want 1", br.ensureCount())
	}
	if _, err := svc.SetEnabled(ctx, w.ID, true); err != nil {
		t.Fatalf("idempotent resume: %v", err)
	}
	if br.ensureCount() != 2 {
		t.Errorf("ensure on idempotent resume = %d, want 2", br.ensureCount())
	}
}

func TestServiceCRUDSurvivesBridgeError(t *testing.T) {
	// Bridge errors must not prevent the CRUD call from succeeding —
	// the Worker row is the source of truth, the schedule row is a
	// downstream mirror that the operator can repair via re-update.
	svc, br, wsID, scopeID := newServiceWithBridge(t)
	br.err = errors.New("bridge dead")
	ctx := context.Background()
	if _, err := svc.Create(ctx, baseCreate(wsID, scopeID)); err != nil {
		t.Fatalf("create must succeed despite bridge err: %v", err)
	}
}
