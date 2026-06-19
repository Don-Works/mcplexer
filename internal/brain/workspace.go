package brain

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/store"
)

// indexWorkspaceFile parses, validates, and upserts a single workspace.md
// file. On success the route cache is invalidated (when a callback is
// wired) so sessions re-resolve against the updated workspace row (SPEC
// §9). Validation failures are recorded as brain_errors and the row is NOT
// upserted.
func (ix *Indexer) indexWorkspaceFile(ctx context.Context, path string, data []byte, sha string, info os.FileInfo) error {
	fm, _, err := ParseWorkspace(data)
	if err != nil {
		ix.recordError(ctx, path, EntityKindWorkspace, err)
		return fmt.Errorf("brain: parse workspace %s: %w", path, err)
	}
	if err := ValidateWorkspace(fm); err != nil {
		ix.recordError(ctx, path, EntityKindWorkspace, err)
		return fmt.Errorf("brain: validate workspace %s: %w", path, err)
	}

	ws, err := fm.ToWorkspace()
	if err != nil {
		ix.recordError(ctx, path, EntityKindWorkspace, err)
		return fmt.Errorf("brain: convert workspace %s: %w", path, err)
	}
	// The file is the brain's canonical writer for this row.
	if ws.Source == "" {
		ws.Source = "brain"
	}

	if err := ix.upsertWorkspace(ctx, ws); err != nil {
		return fmt.Errorf("brain: upsert workspace %s: %w", path, err)
	}

	_ = ix.store.ClearBrainErrorsForPath(ctx, path)
	ix.recordIndexFile(ctx, path, sha, info, EntityKindWorkspace, ws.ID)

	// Re-resolve sessions/routes against the changed workspace (e.g. a
	// default_policy edit). Best-effort; nil when not wired.
	if ix.invalidate != nil {
		ix.invalidate(ws.ID)
	}
	return nil
}

// upsertWorkspace creates or updates the workspace row. Existing id →
// UpdateWorkspace (preserving created_at), else CreateWorkspace.
func (ix *Indexer) upsertWorkspace(ctx context.Context, w *store.Workspace) error {
	existing, err := ix.store.GetWorkspace(ctx, w.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ix.store.CreateWorkspace(ctx, w)
	case err != nil:
		return err
	default:
		if w.CreatedAt.IsZero() {
			w.CreatedAt = existing.CreatedAt
		}
		return ix.store.UpdateWorkspace(ctx, w)
	}
}
