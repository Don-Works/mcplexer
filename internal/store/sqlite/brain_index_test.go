package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestIndexFileCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Missing path -> ErrNotFound.
	if _, err := db.GetIndexFile(ctx, "/nope.md"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetIndexFile(missing): want ErrNotFound, got %v", err)
	}

	f := &store.IndexFile{
		Path:        "/brain/workspaces/ws/tasks/01-foo.md",
		WorkspaceID: "ws",
		EntityKind:  "task",
		EntityID:    "01",
		Sha:         "abc123",
		Mtime:       1000,
		Size:        42,
	}
	if err := db.UpsertIndexFile(ctx, f); err != nil {
		t.Fatalf("UpsertIndexFile: %v", err)
	}
	if f.IndexedAt.IsZero() {
		t.Fatal("UpsertIndexFile should default IndexedAt")
	}

	got, err := db.GetIndexFile(ctx, f.Path)
	if err != nil {
		t.Fatalf("GetIndexFile: %v", err)
	}
	if got.Sha != "abc123" || got.WorkspaceID != "ws" || got.EntityKind != "task" ||
		got.EntityID != "01" || got.Mtime != 1000 || got.Size != 42 {
		t.Fatalf("GetIndexFile roundtrip mismatch: %+v", got)
	}

	// Upsert overwrites sha (same path).
	f.Sha = "def456"
	f.Size = 99
	if err := db.UpsertIndexFile(ctx, f); err != nil {
		t.Fatalf("UpsertIndexFile(overwrite): %v", err)
	}
	got, err = db.GetIndexFile(ctx, f.Path)
	if err != nil {
		t.Fatalf("GetIndexFile(after overwrite): %v", err)
	}
	if got.Sha != "def456" || got.Size != 99 {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	// A second file in a different workspace, then scope ListIndexFiles.
	other := &store.IndexFile{Path: "/brain/global/x.md", WorkspaceID: "", Sha: "g1"}
	if err := db.UpsertIndexFile(ctx, other); err != nil {
		t.Fatalf("UpsertIndexFile(global): %v", err)
	}
	wsRows, err := db.ListIndexFiles(ctx, "ws")
	if err != nil {
		t.Fatalf("ListIndexFiles(ws): %v", err)
	}
	if len(wsRows) != 1 || wsRows[0].Path != f.Path {
		t.Fatalf("ListIndexFiles(ws): want 1 row for ws, got %+v", wsRows)
	}
	allRows, err := db.ListIndexFiles(ctx, "")
	if err != nil {
		t.Fatalf("ListIndexFiles(all): %v", err)
	}
	if len(allRows) != 2 {
		t.Fatalf("ListIndexFiles(all): want 2, got %d", len(allRows))
	}

	// Delete is idempotent.
	if err := db.DeleteIndexFile(ctx, f.Path); err != nil {
		t.Fatalf("DeleteIndexFile: %v", err)
	}
	if err := db.DeleteIndexFile(ctx, f.Path); err != nil {
		t.Fatalf("DeleteIndexFile(idempotent): %v", err)
	}
	if _, err := db.GetIndexFile(ctx, f.Path); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetIndexFile(after delete): want ErrNotFound, got %v", err)
	}
}

func TestIndexFileValidation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name string
		f    *store.IndexFile
	}{
		{"nil", nil},
		{"empty path", &store.IndexFile{Sha: "x"}},
		{"empty sha", &store.IndexFile{Path: "/x.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.UpsertIndexFile(ctx, tc.f); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestBrainErrorsCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if rows, err := db.ListBrainErrors(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("ListBrainErrors(empty): rows=%v err=%v", rows, err)
	}

	path := "/brain/workspaces/ws/tasks/01-bad.md"
	e := &store.BrainError{
		Path:       path,
		EntityKind: "task",
		Field:      "status",
		Reason:     "not in vocab",
	}
	if err := db.RecordBrainError(ctx, e); err != nil {
		t.Fatalf("RecordBrainError: %v", err)
	}
	if e.ID == "" || e.CreatedAt.IsZero() {
		t.Fatalf("RecordBrainError should default id+created_at: %+v", e)
	}

	rows, err := db.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors: %v", err)
	}
	if len(rows) != 1 || rows[0].Field != "status" || rows[0].Reason != "not in vocab" {
		t.Fatalf("ListBrainErrors roundtrip mismatch: %+v", rows)
	}

	// Clear by path removes it.
	if err := db.ClearBrainErrorsForPath(ctx, path); err != nil {
		t.Fatalf("ClearBrainErrorsForPath: %v", err)
	}
	rows, err = db.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors(after clear): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListBrainErrors(after clear): want 0, got %d", len(rows))
	}

	// RecordBrainError requires a path.
	if err := db.RecordBrainError(ctx, &store.BrainError{Reason: "x"}); err == nil {
		t.Fatal("RecordBrainError(no path): expected error")
	}

	// CreatedAt ordering: newest first.
	_ = db.RecordBrainError(ctx, &store.BrainError{Path: path, Reason: "old", CreatedAt: time.Unix(100, 0)})
	_ = db.RecordBrainError(ctx, &store.BrainError{Path: path, Reason: "new", CreatedAt: time.Unix(200, 0)})
	rows, _ = db.ListBrainErrors(ctx)
	if len(rows) != 2 || rows[0].Reason != "new" {
		t.Fatalf("ListBrainErrors ordering: %+v", rows)
	}
}

// TestCandidateSuppressionValidation covers the only authoritative (non-rebuildable)
// brain state: SuppressCandidate / IsCandidateSuppressed (brain_index.go).
// It pins the suppress-all blank-hash predicate, INSERT OR IGNORE
// idempotency, and the empty-record-id validation guard.
func TestCandidateSuppressionValidation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		recordID string
	}{
		{"empty record id", ""},
		{"whitespace record id", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.SuppressCandidate(ctx, tc.recordID, "h"); err == nil {
				t.Fatal("SuppressCandidate: expected error, got nil")
			}
		})
	}
}

func TestCandidateSuppression(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	const (
		recExact = "task-01"
		recAll   = "task-02"
		recNone  = "task-03"
		hashA    = "sha-aaa"
		hashB    = "sha-bbb"
	)

	// Nothing suppressed yet.
	if got, err := db.IsCandidateSuppressed(ctx, recExact, hashA); err != nil || got {
		t.Fatalf("IsCandidateSuppressed(empty): got=%v err=%v", got, err)
	}

	// Exact (record, hash) suppress + detect.
	if err := db.SuppressCandidate(ctx, recExact, hashA); err != nil {
		t.Fatalf("SuppressCandidate(exact): %v", err)
	}
	// Suppress-all marker (blank hash) on a different record.
	if err := db.SuppressCandidate(ctx, recAll, ""); err != nil {
		t.Fatalf("SuppressCandidate(all): %v", err)
	}

	cases := []struct {
		name     string
		recordID string
		hash     string
		want     bool
	}{
		{"exact match", recExact, hashA, true},
		{"exact non-matching hash same record", recExact, hashB, false},
		{"suppress-all matches any hash A", recAll, hashA, true},
		{"suppress-all matches any hash B", recAll, hashB, true},
		{"suppress-all matches blank hash", recAll, "", true},
		{"unsuppressed record", recNone, hashA, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.IsCandidateSuppressed(ctx, tc.recordID, tc.hash)
			if err != nil {
				t.Fatalf("IsCandidateSuppressed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("IsCandidateSuppressed(%q,%q) = %v, want %v",
					tc.recordID, tc.hash, got, tc.want)
			}
		})
	}

	// Idempotent re-suppress (INSERT OR IGNORE) — no error, still detected.
	if err := db.SuppressCandidate(ctx, recExact, hashA); err != nil {
		t.Fatalf("SuppressCandidate(re-suppress): %v", err)
	}
	if got, err := db.IsCandidateSuppressed(ctx, recExact, hashA); err != nil || !got {
		t.Fatalf("IsCandidateSuppressed(after re-suppress): got=%v err=%v", got, err)
	}
}
