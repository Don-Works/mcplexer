package brain

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
)

// OnMemoryWrite serializes a memory to its canonical .md file. It is the
// memory.BrainHook method the memory service calls on every mutation
// (write, pin, invalidate, entity link/unlink). Errors are logged, not
// propagated — a serialize failure must never fail the underlying tool
// call (the DB row is already written; the file is a derived artifact).
func (s *Serializer) OnMemoryWrite(ctx context.Context, id string) {
	if id == "" {
		return
	}
	if err := s.WriteMemory(ctx, id); err != nil {
		s.log.Warn("brain: serialize memory", "id", id, "error", err)
	}
}

// OnMemoryDelete removes a memory's .md file + index bookkeeping.
func (s *Serializer) OnMemoryDelete(ctx context.Context, id string) {
	if err := s.DeleteMemory(ctx, id); err != nil {
		s.log.Warn("brain: delete memory file", "id", id, "error", err)
	}
}

// WriteMemory loads the memory row + its entity links and writes the
// canonical memory .md file, guarded by the same hash-CAS + atomic write +
// self-suppress machinery as tasks. Notes land at memory/<name>.md; facts
// at memory/facts/<name>.md.
func (s *Serializer) WriteMemory(ctx context.Context, id string) error {
	m, err := s.store.GetMemory(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Invalidated/soft-deleted in the same breath — nothing to write.
			return nil
		}
		return err
	}
	links, err := s.store.ListMemoryEntities(ctx, id)
	if err != nil {
		links = nil // best-effort: a link-list failure should not block the body write
	}
	data, err := SerializeMemory(m, links)
	if err != nil {
		return err
	}

	path, err := s.resolveMemoryPath(ctx, m)
	if err != nil {
		return err
	}
	wrote, sha, err := s.guardedWrite(ctx, path, data, EntityKindMemory)
	if err != nil {
		return err
	}
	if wrote {
		s.recordIndexFile(ctx, path, sha, EntityKindMemory, m.ID, ptrOrEmpty(m.WorkspaceID))
	}
	return nil
}

// RenderMemoryRow serializes the canonical .md bytes for an in-memory memory
// row + its intended entity links, WITHOUT reloading from the store. Exposed
// to the Editor's preflight-conflict path so the .conflict sidecar holds the
// user's INTENDED content (the not-yet-persisted edit), not the stale DB row.
func (s *Serializer) RenderMemoryRow(m *store.MemoryEntry, refs []store.EntityRef) ([]byte, error) {
	rows := make([]store.MemoryEntityRow, 0, len(refs))
	for _, r := range refs {
		role := r.Role
		if role == "" {
			role = store.EntityRoleSubject
		}
		rows = append(rows, store.MemoryEntityRow{
			MemoryID:   m.ID,
			EntityKind: r.Kind,
			EntityID:   r.ID,
			Role:       role,
		})
	}
	return SerializeMemory(m, rows)
}

// DeleteMemory removes the memory file (preferring the index-recorded
// path) and its index_files row.
func (s *Serializer) DeleteMemory(ctx context.Context, id string) error {
	if f, err := s.findIndexByEntity(ctx, EntityKindMemory, id); err == nil && f != nil {
		s.removePath(f.Path)
		_ = s.store.DeleteIndexFile(ctx, f.Path)
		s.notifyPaths(f.Path)
	}
	return nil
}

// resolveMemoryPath returns the canonical .md path for a memory. It prefers
// the path already recorded in index_files (so a workspace/kind change does
// not orphan the existing file), falling back to the name-derived path.
func (s *Serializer) resolveMemoryPath(ctx context.Context, m *store.MemoryEntry) (string, error) {
	if f, err := s.findIndexByEntity(ctx, EntityKindMemory, m.ID); err == nil && f != nil && f.Path != "" {
		return f.Path, nil
	}
	return s.memoryPath(m)
}

// memoryPath computes the canonical path for a memory .md file. Global
// memories (nil workspace) land under the "global" workspace folder so the
// watcher's workspaces/ tree picks them up uniformly. The filename stem is
// the slugified name (recordStem) — never the raw name, which is free-form
// text and once carried a '/' that created an unindexable subdirectory.
func (s *Serializer) memoryPath(m *store.MemoryEntry) (string, error) {
	ws := ptrOrEmpty(m.WorkspaceID)
	var root string
	if ws == "" {
		// Global memory has no repo-local home; it always lands under the
		// central brain's "global" workspace folder.
		var err error
		root, err = s.cfg.WorkspaceDir("global")
		if err != nil {
			return "", err
		}
	} else {
		// Workspace-scoped memory follows the workspace's canonical root —
		// the repo-local .mcplexer/ when registered, else the central tree.
		var err error
		root, err = s.canonicalWorkspaceDir(ws)
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(root, memorySubdir)
	if m.Kind == MemoryKindFact {
		dir = filepath.Join(dir, memoryFactsSubdir)
	}
	return filepath.Join(dir, recordStem(m.Name, m.ID)+".md"), nil
}

// ptrOrEmpty dereferences a *string, returning "" for nil.
func ptrOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
