package brain

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
)

// WriteWorkspace renders a workspace row to its canonical workspace.md and
// writes it under <Dir>/workspaces/<id>/workspace.md, guarded by the same
// hash-CAS + atomic write + self-suppress machinery as tasks/memories. The
// gateway calls this when a workspace is created/updated via the API so the
// brain repo reflects it (SPEC §9). Best-effort: errors are returned to the
// caller but the workspace row is already written.
func (s *Serializer) WriteWorkspace(ctx context.Context, w *store.Workspace) error {
	if w == nil {
		return errors.New("brain: WriteWorkspace: nil workspace")
	}
	data, err := SerializeWorkspace(w)
	if err != nil {
		return err
	}
	path, err := s.workspacePath(w.ID)
	if err != nil {
		return err
	}
	wrote, sha, err := s.guardedWrite(ctx, path, data, EntityKindWorkspace)
	if err != nil {
		return err
	}
	if wrote {
		s.recordIndexFile(ctx, path, sha, EntityKindWorkspace, w.ID, w.ID)
	}
	return nil
}

// workspacePath returns the canonical workspace.md path for a workspace id.
// It honours a registered repo-local .mcplexer/ root (M6 — federation) so
// the workspace.md lands in the repo when that is the workspace's canonical
// brain, falling back to the central tree otherwise.
func (s *Serializer) workspacePath(id string) (string, error) {
	root, err := s.canonicalWorkspaceDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, workspaceFile), nil
}
