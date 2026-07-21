// brain_hook_test.go — the dual-write BrainHook (M1) must fire on EVERY
// observable task mutation so the canonical .md files never silently
// drift from the DB. The plan calls this out as load-bearing: a hook that
// only fires on Create/Update lets compose/decompose/claim/lease changes
// rot the on-disk files. This locks in that every mutator reaches the
// hook (via the single publish funnel) and that compose/decompose — which
// deliberately stay quiet on the event bus — still serialize via the
// brain-only path.
package tasks_test

import (
	"context"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// fakeBrainHook records the task ids it was asked to (re)serialize and
// delete, so a test can assert which mutators reached the hook.
type fakeBrainHook struct {
	mu      sync.Mutex
	writes  map[string]int
	deletes map[string]int
}

func newFakeBrainHook() *fakeBrainHook {
	return &fakeBrainHook{writes: map[string]int{}, deletes: map[string]int{}}
}

func (f *fakeBrainHook) OnTaskWrite(_ context.Context, t *store.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes[t.ID]++
}

func (f *fakeBrainHook) OnTaskDelete(_ context.Context, id, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes[id]++
}

func (f *fakeBrainHook) writeCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[id]
}

func (f *fakeBrainHook) deleteCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes[id]
}

func TestBrainHook_FiresOnEveryMutator(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	hook := newFakeBrainHook()
	svc.SetBrainHook(hook)

	// Create — must serialize.
	parent, err := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	child, err := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Child"})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	if hook.writeCount(parent.ID) == 0 || hook.writeCount(child.ID) == 0 {
		t.Fatalf("Create did not fire OnTaskWrite (parent=%d child=%d)",
			hook.writeCount(parent.ID), hook.writeCount(child.ID))
	}

	// Update — must serialize the updated row.
	before := hook.writeCount(parent.ID)
	st := "doing"
	if _, err := svc.Update(ctx, wsID, parent.ID, tasks.UpdatePatch{Status: &st, UpdatedBySessionID: "agent-a"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if hook.writeCount(parent.ID) <= before {
		t.Errorf("Update did not fire OnTaskWrite")
	}

	// Compose — mutates BOTH parent (meta.composes) and child
	// (meta.composed_by) but is QUIET on the event bus; the brain-only
	// path must still serialize both files or they drift.
	pBefore, cBefore := hook.writeCount(parent.ID), hook.writeCount(child.ID)
	if err := svc.Compose(ctx, wsID, parent.ID, child.ID, "agent-a"); err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if hook.writeCount(parent.ID) <= pBefore {
		t.Errorf("Compose did not re-serialize the parent (meta.composes drift)")
	}
	if hook.writeCount(child.ID) <= cBefore {
		t.Errorf("Compose did not re-serialize the child (meta.composed_by drift)")
	}

	// Decompose — same: both sides change, both must serialize.
	pBefore, cBefore = hook.writeCount(parent.ID), hook.writeCount(child.ID)
	if err := svc.Decompose(ctx, wsID, parent.ID, child.ID, "agent-a"); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if hook.writeCount(parent.ID) <= pBefore {
		t.Errorf("Decompose did not re-serialize the parent")
	}
	if hook.writeCount(child.ID) <= cBefore {
		t.Errorf("Decompose did not re-serialize the child")
	}

	// Delete — must route to OnTaskDelete, not OnTaskWrite.
	if err := svc.Delete(ctx, wsID, child.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if hook.deleteCount(child.ID) == 0 {
		t.Errorf("Delete did not fire OnTaskDelete")
	}
}

// TestBrainHook_NilIsNoOp asserts the default (no hook wired) path never
// panics — flag-off behaviour is byte-for-byte today's behaviour.
func TestBrainHook_NilIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t) // no SetBrainHook call
	p, err := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Child"})
	if err := svc.Compose(ctx, wsID, p.ID, c.ID, "agent-a"); err != nil {
		t.Fatalf("Compose with nil hook: %v", err)
	}
	if err := svc.Delete(ctx, wsID, p.ID); err != nil {
		t.Fatalf("Delete with nil hook: %v", err)
	}
}
