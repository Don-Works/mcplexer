// task_attachment_test.go — coverage of the task_attachments store
// layer (migration 078). Mirrors task_test.go conventions: in-memory
// sqlite via newMemDB, seedWorkspace helper, table-driven where it
// helps.
package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedTask creates a workspace + a task and returns both ids.
func seedTask(t *testing.T, d *DB) (workspaceID, taskID string) {
	t.Helper()
	wsID := seedWorkspace(t, d, "ws-attachments")
	task := &store.Task{WorkspaceID: wsID, Title: "owns attachments"}
	if err := d.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return wsID, task.ID
}

func TestInsertTaskAttachmentValidatesRequired(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	cases := []struct {
		name string
		row  store.TaskAttachment
	}{
		{"missing task_id", store.TaskAttachment{WorkspaceID: wsID, Sha256: "a", StoragePath: "p", SizeBytes: 1}},
		{"missing workspace_id", store.TaskAttachment{TaskID: taskID, Sha256: "a", StoragePath: "p", SizeBytes: 1}},
		{"missing sha256", store.TaskAttachment{TaskID: taskID, WorkspaceID: wsID, StoragePath: "p", SizeBytes: 1}},
		{"missing storage_path", store.TaskAttachment{TaskID: taskID, WorkspaceID: wsID, Sha256: "a", SizeBytes: 1}},
		{"negative size", store.TaskAttachment{TaskID: taskID, WorkspaceID: wsID, Sha256: "a", StoragePath: "p", SizeBytes: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.row
			if err := d.InsertTaskAttachment(ctx, &row); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestInsertTaskAttachmentDefaults(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	row := &store.TaskAttachment{
		TaskID:      taskID,
		WorkspaceID: wsID,
		Sha256:      "deadbeef",
		StoragePath: "attachments/" + wsID + "/" + taskID + "/deadbeef",
		SizeBytes:   42,
		Filename:    "notes.txt",
	}
	if err := d.InsertTaskAttachment(ctx, row); err != nil {
		t.Fatalf("InsertTaskAttachment: %v", err)
	}
	if row.ID == "" {
		t.Fatal("expected ID to be auto-generated")
	}
	if row.MimeType != "application/octet-stream" {
		t.Fatalf("expected default mime_type, got %q", row.MimeType)
	}
	if row.UploaderKind != store.TaskSourceAgent {
		t.Fatalf("expected default uploader_kind=agent, got %q", row.UploaderKind)
	}
	if row.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be populated")
	}
}

func TestGetTaskAttachmentRoundTrip(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	row := &store.TaskAttachment{
		TaskID:            taskID,
		WorkspaceID:       wsID,
		Sha256:            "abc123",
		StoragePath:       "attachments/x",
		SizeBytes:         128,
		Filename:          "report.pdf",
		MimeType:          "application/pdf",
		UploaderSessionID: "sess-1",
		UploaderKind:      "user",
	}
	if err := d.InsertTaskAttachment(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := d.GetTaskAttachment(ctx, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Filename != "report.pdf" || got.MimeType != "application/pdf" {
		t.Fatalf("filename/mime mismatch: %+v", got)
	}
	if got.SizeBytes != 128 || got.Sha256 != "abc123" {
		t.Fatalf("size/sha mismatch: %+v", got)
	}
	if got.UploaderSessionID != "sess-1" || got.UploaderKind != "user" {
		t.Fatalf("uploader mismatch: %+v", got)
	}
}

func TestGetTaskAttachmentMissingReturnsNotFound(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	_, err := d.GetTaskAttachment(ctx, "01HZZZZZZZZZZZZZZZZZZZZZZZ")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListTaskAttachmentsOrdersNewestFirst(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	now := time.Now().UTC()
	for i, name := range []string{"old.txt", "mid.txt", "new.txt"} {
		row := &store.TaskAttachment{
			TaskID:      taskID,
			WorkspaceID: wsID,
			Sha256:      name,
			StoragePath: "p/" + name,
			SizeBytes:   int64(len(name)),
			Filename:    name,
			CreatedAt:   now.Add(time.Duration(i) * time.Second),
		}
		if err := d.InsertTaskAttachment(ctx, row); err != nil {
			t.Fatalf("Insert %s: %v", name, err)
		}
	}

	rows, err := d.ListTaskAttachments(ctx, taskID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].Filename != "new.txt" || rows[2].Filename != "old.txt" {
		t.Fatalf("order wrong: %v", []string{rows[0].Filename, rows[1].Filename, rows[2].Filename})
	}
}

func TestSoftDeleteTaskAttachmentHidesRow(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	row := &store.TaskAttachment{
		TaskID:      taskID,
		WorkspaceID: wsID,
		Sha256:      "abc",
		StoragePath: "p",
		SizeBytes:   1,
	}
	if err := d.InsertTaskAttachment(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := d.SoftDeleteTaskAttachment(ctx, row.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := d.GetTaskAttachment(ctx, row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	rows, _ := d.ListTaskAttachments(ctx, taskID)
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", len(rows))
	}

	// Second delete should also report not-found (idempotent at the
	// "still soft-deletable" boundary — ErrNotFound is the signal).
	if err := d.SoftDeleteTaskAttachment(ctx, row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestListTaskAttachmentsRequiresTaskID(t *testing.T) {
	d := newMemDB(t)
	if _, err := d.ListTaskAttachments(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty task_id")
	}
}

func TestWorkspaceCascadeSoftDeletesAttachments(t *testing.T) {
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	row := &store.TaskAttachment{
		TaskID:      taskID,
		WorkspaceID: wsID,
		Sha256:      "cascade-test",
		StoragePath: "p/cascade",
		SizeBytes:   7,
	}
	if err := d.InsertTaskAttachment(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := d.DeleteWorkspace(ctx, wsID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	// Attachment should no longer surface in standard reads.
	if _, err := d.GetTaskAttachment(ctx, row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected attachment soft-deleted after workspace cascade, got err=%v", err)
	}
	rows, _ := d.ListTaskAttachments(ctx, taskID)
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after workspace cascade, got %d", len(rows))
	}
}

func TestSameSha256DedupesByContentAddress(t *testing.T) {
	// Two rows with the same sha256 + storage_path is legitimate: a user
	// uploads the same file under two different filenames in one task,
	// and the service-layer dedupe-by-content lets both rows share the
	// underlying blob. The DB layer doesn't forbid the duplication.
	d := newMemDB(t)
	ctx := context.Background()
	wsID, taskID := seedTask(t, d)

	for _, name := range []string{"first.bin", "second.bin"} {
		row := &store.TaskAttachment{
			TaskID:      taskID,
			WorkspaceID: wsID,
			Sha256:      "shared-hash",
			StoragePath: "attachments/" + wsID + "/" + taskID + "/shared-hash",
			SizeBytes:   3,
			Filename:    name,
		}
		if err := d.InsertTaskAttachment(ctx, row); err != nil {
			t.Fatalf("Insert %s: %v", name, err)
		}
	}
	rows, err := d.ListTaskAttachments(ctx, taskID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows sharing sha256, got %d", len(rows))
	}
	if rows[0].StoragePath != rows[1].StoragePath {
		t.Fatalf("expected shared storage_path, got %q vs %q", rows[0].StoragePath, rows[1].StoragePath)
	}
}
