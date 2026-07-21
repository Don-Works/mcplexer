package brain

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/store"
)

// indexTaskFile parses, validates, and upserts a single task .md file.
// Validation failures are recorded as brain_errors and the row is NOT
// upserted (an index that lies is worse than a missing record — SPEC
// §6.5). On success the index_files bookkeeping is updated and any stale
// brain_errors for the path are cleared.
func (ix *Indexer) indexTaskFile(ctx context.Context, path string, data []byte, sha string, info os.FileInfo) error {
	fm, body, err := ParseTask(data)
	if err != nil {
		ix.recordError(ctx, path, EntityKindTask, err)
		return fmt.Errorf("brain: parse task %s: %w", path, err)
	}

	vocab := ix.statusVocab(ctx, fm.Workspace)
	if err := ValidateTask(fm, baseName(path), vocab); err != nil {
		ix.recordError(ctx, path, EntityKindTask, err)
		return fmt.Errorf("brain: validate task %s: %w", path, err)
	}

	task, err := fm.ToTask(body)
	if err != nil {
		ix.recordError(ctx, path, EntityKindTask, err)
		return fmt.Errorf("brain: convert task %s: %w", path, err)
	}

	if err := ix.upsertTask(ctx, task); err != nil {
		return fmt.Errorf("brain: upsert task %s: %w", path, err)
	}

	_ = ix.store.ClearBrainErrorsForPath(ctx, path)
	ix.recordIndexFile(ctx, path, sha, info, EntityKindTask, task.ID)
	return nil
}

// upsertTask creates or updates the task row. Existing id → UpdateTask
// (so FTS5 au-trigger fires), else CreateTask. The write goes through the
// existing Store methods unchanged so all derived logic (FTS, generated
// columns) stays consistent.
func (ix *Indexer) upsertTask(ctx context.Context, t *store.Task) error {
	existing, err := ix.store.GetTask(ctx, t.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ix.store.CreateTask(ctx, t)
	case err != nil:
		return err
	default:
		// Preserve fields the file does not own so an inbound edit of
		// frontmatter never clobbers lease/closed bookkeeping the service
		// layer maintains. The file is canonical for the prose +
		// human-editable frontmatter; these are derived/operational.
		t.ClosedAt = existing.ClosedAt
		t.LeaseExpiresAt = existing.LeaseExpiresAt
		t.DeletedAt = existing.DeletedAt
		if t.CreatedAt.IsZero() {
			t.CreatedAt = existing.CreatedAt
		}
		return ix.store.UpdateTask(ctx, t)
	}
}

// statusVocab returns the workspace's known status values for validation,
// or nil when the vocab is empty/unavailable (skips the status-in-vocab
// check rather than failing closed before the vocab is configured).
func (ix *Indexer) statusVocab(ctx context.Context, workspaceID string) []string {
	if workspaceID == "" {
		return nil
	}
	rows, err := ix.store.ListTaskStatusVocab(ctx, workspaceID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.StatusText)
	}
	return out
}
