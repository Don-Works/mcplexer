// task_vocab_kind_test.go — pins the review-kind vocabulary contract
// (migration 099 + the ensureTaskStatusVocabKind self-heal seed):
// the 'review' suggested default classifies as kind='review', not the
// migration-070-era kind='blocked'.
package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestEnsureTaskStatusVocabKindSeedsReviewKind drops the kind column
// (simulating a pre-070 schema) and re-runs the self-heal, asserting
// the six suggested defaults land on their canonical kinds — review
// included.
func TestEnsureTaskStatusVocabKindSeedsReviewKind(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := &store.Workspace{Name: "ws-vocab", RootPath: "/tmp/ws-vocab", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	// Seed the six suggested defaults, then strip the kind column so the
	// self-heal has something to re-add + re-seed.
	for _, status := range []string{"open", "doing", "blocked", "review", "done", "cancelled"} {
		if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
			WorkspaceID: ws.ID, StatusText: status,
		}); err != nil {
			t.Fatalf("seed vocab %q: %v", status, err)
		}
	}
	if _, err := d.q.ExecContext(ctx, `DROP INDEX IF EXISTS idx_task_status_vocab_kind`); err != nil {
		t.Fatalf("drop kind index: %v", err)
	}
	if _, err := d.q.ExecContext(ctx, `ALTER TABLE task_status_vocabulary DROP COLUMN kind`); err != nil {
		t.Fatalf("drop kind column: %v", err)
	}

	if err := ensureTaskStatusVocabKind(ctx, d.db); err != nil {
		t.Fatalf("ensureTaskStatusVocabKind: %v", err)
	}

	rows, err := d.ListTaskStatusVocab(ctx, ws.ID)
	if err != nil {
		t.Fatalf("ListTaskStatusVocab: %v", err)
	}
	want := map[string]string{
		"open": "open", "doing": "working", "blocked": "blocked",
		"review": "review", "done": "done", "cancelled": "cancelled",
	}
	got := map[string]string{}
	for _, v := range rows {
		got[v.StatusText] = v.Kind
	}
	for status, kind := range want {
		if got[status] != kind {
			t.Errorf("self-heal seeded %q kind=%q, want %q", status, got[status], kind)
		}
	}
}

// TestMigration099RewritesBlockedReviewRows applies the migration-099
// UPDATE semantics: rows still carrying the 070-era review→blocked
// classification rewrite to kind='review'; deliberate re-classifications
// are left alone.
func TestMigration099RewritesBlockedReviewRows(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := &store.Workspace{Name: "ws-099", RootPath: "/tmp/ws-099", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	ws2 := &store.Workspace{Name: "ws-099b", RootPath: "/tmp/ws-099b", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(ctx, ws2); err != nil {
		t.Fatalf("create workspace 2: %v", err)
	}
	// ws: the 070-era default. ws2: a deliberate user choice.
	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: ws.ID, StatusText: "review", Kind: "blocked",
	}); err != nil {
		t.Fatalf("seed ws vocab: %v", err)
	}
	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: ws2.ID, StatusText: "review", Kind: "working",
	}); err != nil {
		t.Fatalf("seed ws2 vocab: %v", err)
	}

	migrationSQL, err := migrationsFS.ReadFile("migrations/099_task_status_vocab_review_kind.sql")
	if err != nil {
		t.Fatalf("read migration 099: %v", err)
	}
	if _, err := d.q.ExecContext(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("apply migration 099: %v", err)
	}

	check := func(wsID, want string) {
		t.Helper()
		rows, err := d.ListTaskStatusVocab(ctx, wsID)
		if err != nil {
			t.Fatalf("ListTaskStatusVocab: %v", err)
		}
		for _, v := range rows {
			if v.StatusText == "review" {
				if v.Kind != want {
					t.Errorf("workspace %s review kind = %q, want %q", wsID, v.Kind, want)
				}
				return
			}
		}
		t.Fatalf("workspace %s has no review vocab row", wsID)
	}
	check(ws.ID, "review")
	check(ws2.ID, "working")
}
