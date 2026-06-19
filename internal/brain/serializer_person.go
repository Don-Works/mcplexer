package brain

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
)

// OnPersonWrite serializes a person to its canonical .md file. Errors are
// logged, not propagated — a serialize failure must never fail the underlying
// tool call (the DB row is already written; the file is a derived artifact).
func (s *Serializer) OnPersonWrite(ctx context.Context, id string) {
	if id == "" {
		return
	}
	if err := s.WritePerson(ctx, id); err != nil {
		s.log.Warn("brain: serialize person", "id", id, "error", err)
	}
}

// OnPersonDelete removes a person's .md file + index bookkeeping.
func (s *Serializer) OnPersonDelete(ctx context.Context, id string) {
	if err := s.DeletePerson(ctx, id); err != nil {
		s.log.Warn("brain: delete person file", "id", id, "error", err)
	}
}

// WritePerson loads the person row + its entity links and writes the canonical
// person .md file, guarded by the same hash-CAS + atomic write + self-suppress
// machinery as tasks/memories. People land under the owning workspace's
// crm/people/ folder.
func (s *Serializer) WritePerson(ctx context.Context, id string) error {
	p, err := s.store.GetPerson(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // soft-deleted in the same breath — nothing to write.
		}
		return err
	}
	links, err := s.store.ListPersonEntities(ctx, id)
	if err != nil {
		links = nil // best-effort: a link-list failure should not block the body write
	}
	data, err := SerializePerson(p, links)
	if err != nil {
		return err
	}

	previous, _ := s.findIndexByEntity(ctx, EntityKindPerson, p.ID)
	path := s.resolvePersonPath(ctx, p)
	wrote, sha, err := s.guardedWrite(ctx, path, data, EntityKindPerson)
	if err != nil {
		return err
	}
	if wrote {
		workspaceID := normalizePersonWorkspace(p.WorkspaceID)
		s.recordIndexFile(ctx, path, sha, EntityKindPerson, p.ID, workspaceID)
		if previous != nil && previous.Path != "" && previous.Path != path {
			s.removePath(previous.Path)
			_ = s.store.DeleteIndexFile(ctx, previous.Path)
			s.notifyPaths(previous.Path)
		}
	}
	return nil
}

// RenderPersonRow serializes the canonical .md bytes for an in-memory person
// row + its intended entity links WITHOUT reloading from the store. Exposed to
// the Editor's preflight-conflict path so the .conflict sidecar holds the
// user's INTENDED content.
func (s *Serializer) RenderPersonRow(p *store.PersonEntry, refs []store.EntityRef) ([]byte, error) {
	rows := make([]store.PersonEntityRow, 0, len(refs))
	for _, r := range refs {
		role := r.Role
		if role == "" {
			role = store.EntityRoleSubject
		}
		rows = append(rows, store.PersonEntityRow{
			PersonID:   p.ID,
			EntityKind: r.Kind,
			EntityID:   r.ID,
			Role:       role,
		})
	}
	return SerializePerson(p, rows)
}

// DeletePerson removes the person file (preferring the index-recorded path)
// and its index_files row.
func (s *Serializer) DeletePerson(ctx context.Context, id string) error {
	if f, err := s.findIndexByEntity(ctx, EntityKindPerson, id); err == nil && f != nil {
		s.removePath(f.Path)
		_ = s.store.DeleteIndexFile(ctx, f.Path)
		s.notifyPaths(f.Path)
	}
	return nil
}

// PreflightPersonConflict resolves the person's canonical path and reports
// whether an outbound write would hit the hash-CAS gate (mirrors
// PreflightMemoryConflict).
func (s *Serializer) PreflightPersonConflict(ctx context.Context, p *store.PersonEntry) (path string, conflict bool, err error) {
	if p == nil {
		return "", false, errors.New("brain: PreflightPersonConflict: nil person")
	}
	path = s.resolvePersonPath(ctx, p)
	conflict, err = s.casConflict(ctx, path)
	return path, conflict, err
}

// resolvePersonPath returns the canonical .md path for a person. It preserves
// an existing workspace-scoped binding in the same workspace (so a name change
// does not orphan the existing file), falling back to the workspace/name path.
func (s *Serializer) resolvePersonPath(ctx context.Context, p *store.PersonEntry) string {
	workspaceID := safePersonWorkspace(p.WorkspaceID)
	if f, err := s.findIndexByEntity(ctx, EntityKindPerson, p.ID); err == nil && f != nil &&
		f.Path != "" && f.WorkspaceID == workspaceID && workspaceFromPath(f.Path) == workspaceID {
		return f.Path
	}
	return s.personPath(p)
}

// personPath computes the canonical path for a person .md file:
// <Dir>/workspaces/<workspace>/crm/people/<slug>.md. The stem is the
// slugified name (recordStem) — never the raw name, which is free-form text
// and could otherwise traverse out of the flat people dir. The workspace
// component is validated via safePersonWorkspace to reject traversal.
func (s *Serializer) personPath(p *store.PersonEntry) string {
	workspaceID := safePersonWorkspace(p.WorkspaceID)
	return filepath.Join(s.cfg.Dir, "workspaces", workspaceID, crmSubdir, peopleSubdir,
		recordStem(p.Name, p.ID)+".md")
}
