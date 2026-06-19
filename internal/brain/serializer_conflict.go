package brain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
)

// This file holds the Editor-facing preflight-conflict surface: the GUI save
// path runs a CAS pre-check BEFORE persisting the DB row, so a conflict
// aborts without diverging the index from the canonical .md (the
// files-are-canonical invariant). On a conflict the would-be content is
// rendered and diverted to a .conflict sidecar for the user to reconcile.

// PreflightTaskConflict resolves the task's canonical path and reports
// whether an outbound write would hit the hash-CAS gate (the on-disk file
// diverged from the last-indexed sha). The Editor calls this BEFORE
// persisting the DB row so a conflict aborts the save without diverging the
// index from the canonical file (a CAS conflict must not leave the DB
// reflecting an edit the .md never received).
func (s *Serializer) PreflightTaskConflict(ctx context.Context, t *store.Task) (path string, conflict bool, err error) {
	if t == nil {
		return "", false, errors.New("brain: PreflightTaskConflict: nil task")
	}
	path, err = s.resolveTaskPath(ctx, t)
	if err != nil {
		return "", false, err
	}
	conflict, err = s.casConflict(ctx, path)
	return path, conflict, err
}

// PreflightMemoryConflict is the memory counterpart of
// PreflightTaskConflict. For an existing record the canonical path comes from
// the index row; for a brand-new record the path is name-derived and a
// missing file is never a conflict.
func (s *Serializer) PreflightMemoryConflict(ctx context.Context, m *store.MemoryEntry) (path string, conflict bool, err error) {
	if m == nil {
		return "", false, errors.New("brain: PreflightMemoryConflict: nil memory")
	}
	path, err = s.resolveMemoryPath(ctx, m)
	if err != nil {
		return "", false, err
	}
	conflict, err = s.casConflict(ctx, path)
	return path, conflict, err
}

// RecordConflict diverts data to a .conflict sidecar and records the
// brain_errors row WITHOUT persisting anything to the canonical file. The
// Editor calls this when a preflight conflict aborts a save so the user's
// intended content is preserved for reconciliation (mirroring the inline
// guardedWrite divert path, minus the DB row write the editor already
// skipped).
func (s *Serializer) RecordConflict(ctx context.Context, path string, data []byte, kind string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("brain: mkdir %s: %w", filepath.Dir(path), err)
	}
	return s.writeConflictSidecar(ctx, path, data, kind)
}

// RenderTask exposes renderTask to the Editor so a preflight conflict can
// write the would-be content to a .conflict sidecar byte-identically to a
// normal outbound write.
func (s *Serializer) RenderTask(ctx context.Context, t *store.Task) ([]byte, error) {
	return s.renderTask(ctx, t)
}

// IndexedSha returns the last-indexed content sha of the entity's canonical
// .md file (the CAS token the editor surfaces as if_hash), or "" when the
// entity has no index_files binding yet (a brand-new record). The path is
// returned too so the caller can read the raw bytes for the file disclosure.
func (s *Serializer) IndexedSha(ctx context.Context, kind, id string) (path, sha, source string) {
	f, err := s.findIndexByEntity(ctx, kind, id)
	if err != nil || f == nil {
		return "", "", ""
	}
	return f.Path, f.Sha, f.Source
}

// ReadRaw returns the verbatim on-disk bytes of a canonical .md (the
// FileTruthDisclosure "this is exactly what your agent reads") plus the
// hash of those bytes. A missing file yields empty content with no error so
// the detail read degrades to the rendered projection.
func (s *Serializer) ReadRaw(path string) (raw, sha string) {
	if path == "" {
		return "", ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return string(b), hashBytes(b)
}

// CurrentDiskSha returns the hash of the on-disk bytes at the entity's
// canonical path RIGHT NOW (not the last-indexed sha). The if_hash CAS gate
// compares the editor's submitted token against this so a divergence since
// the editor loaded the record is caught even before the serializer's own
// last-indexed gate. "" when the file is absent (a create, never a conflict).
func (s *Serializer) CurrentDiskSha(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashBytes(b)
}

// renderTask composes the task body (prose + rendered notes) and serializes
// the frontmatter+body to the canonical .md bytes. Shared by WriteTask and
// the Editor's preflight-conflict sidecar path so both emit byte-identical
// content.
func (s *Serializer) renderTask(ctx context.Context, t *store.Task) ([]byte, error) {
	notes, err := s.store.ListTaskNotes(ctx, t.ID, 0)
	if err != nil {
		return nil, fmt.Errorf("brain: list notes %s: %w", t.ID, err)
	}
	body := composeTaskBody(t.Description, oldestFirst(notes))
	data, err := SerializeTask(t, body)
	if err != nil {
		return nil, fmt.Errorf("brain: render task %s: %w", t.ID, err)
	}
	return data, nil
}
