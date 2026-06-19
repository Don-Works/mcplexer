package tasks_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// helper — create a task with the given touches_files + status. Returns the created task.
func mkTouchTask(t *testing.T, svc *tasks.Service, wsID, title, status string, paths []string) *store.Task {
	t.Helper()
	metaObj := map[string]any{tasks.TouchesFilesMetaKey: paths}
	b, _ := json.Marshal(metaObj)
	got, err := svc.Create(context.Background(), tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              title,
		Status:             status,
		Meta:               string(b),
		CreatedBySessionID: "test-session",
	})
	if err != nil {
		t.Fatalf("Create %q: %v", title, err)
	}
	return got
}

func TestCheckCoordinationOverlap_NoOverlap_ReturnsNothing(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	mkTouchTask(t, svc, wsID, "first", "doing", []string{"a.go", "b.go"})
	mine := mkTouchTask(t, svc, wsID, "me", "doing", []string{"c.go"})

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("no overlap expected, got %+v", warns)
	}
}

func TestCheckCoordinationOverlap_SingleOverlap_ReturnsOne(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	other := mkTouchTask(t, svc, wsID, "the other", "doing", []string{"shared.go", "other-only.go"})
	mine := mkTouchTask(t, svc, wsID, "me", "doing", []string{"shared.go", "mine-only.go"})

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warns), warns)
	}
	if warns[0].TaskID != other.ID {
		t.Errorf("expected warning for %q, got %q", other.ID, warns[0].TaskID)
	}
	if len(warns[0].OverlappingPaths) != 1 || warns[0].OverlappingPaths[0] != "shared.go" {
		t.Errorf("expected overlapping paths [shared.go], got %v", warns[0].OverlappingPaths)
	}
}

func TestCheckCoordinationOverlap_SelfExcluded(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	mine := mkTouchTask(t, svc, wsID, "me", "doing", []string{"shared.go"})

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("self should be excluded, got %+v", warns)
	}
}

func TestCheckCoordinationOverlap_DoneTasksIgnored(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	// Closed task touching the same file shouldn't generate a warning —
	// the collision risk window is in-progress work only.
	closed := mkTouchTask(t, svc, wsID, "closed", "doing", []string{"shared.go"})
	if _, err := svc.Update(ctx, wsID, closed.ID, tasks.UpdatePatch{
		Status: ptr("done"), Terminal: ptr(true), UpdatedBySessionID: "test-session",
	}); err != nil {
		t.Fatalf("close: %v", err)
	}

	mine := mkTouchTask(t, svc, wsID, "me", "doing", []string{"shared.go"})

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("done tasks should be ignored, got %+v", warns)
	}
}

func TestCheckCoordinationOverlap_MyTaskNotWorking_ReturnsNothing(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	mkTouchTask(t, svc, wsID, "other", "doing", []string{"shared.go"})
	// Mine is just "open", not "doing" — collision check is gated on
	// "I'm about to start" (working status), not on every list.
	mine := mkTouchTask(t, svc, wsID, "me", "open", []string{"shared.go"})

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("non-working caller should skip check, got %+v", warns)
	}
}

func TestCheckCoordinationOverlap_NoTouchesFiles_ReturnsNothing(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	mkTouchTask(t, svc, wsID, "other", "doing", []string{"shared.go"})
	// No touches_files declared — caller didn't opt in to the check.
	mine, err := svc.Create(context.Background(), tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              "me",
		Status:             "doing",
		CreatedBySessionID: "test-session",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("no touches_files should skip check, got %+v", warns)
	}
}

func TestCheckCoordinationOverlap_MultipleOverlappingPathsCollapsed(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)

	other := mkTouchTask(t, svc, wsID, "other", "doing", []string{"a.go", "b.go", "c.go"})
	mine := mkTouchTask(t, svc, wsID, "me", "doing", []string{"a.go", "b.go", "d.go"})

	warns, err := svc.CheckCoordinationOverlap(ctx, mine)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(warns) != 1 {
		t.Fatalf("expected one task in warnings, got %d", len(warns))
	}
	if warns[0].TaskID != other.ID {
		t.Errorf("expected %q, got %q", other.ID, warns[0].TaskID)
	}
	got := warns[0].OverlappingPaths
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Errorf("expected overlapping paths [a.go, b.go] sorted, got %v", got)
	}
}

func ptr[T any](v T) *T { return &v }
