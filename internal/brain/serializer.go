package brain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
)

// slugMaxLen bounds the kebab slug appended to a task filename so a long
// title cannot blow the OS path limit.
const slugMaxLen = 60

// Serializer is the outbound half of the sync engine: it writes a DB row
// back to its canonical .md file. This is the only place real conflict
// exists, so it is guarded by a content-hash CAS gate (don't clobber a
// concurrent human edit), an atomic temp+rename write, and self-write
// suppression (the resulting fsnotify event is recognised + skipped).
//
// Serializer implements the tasks.BrainHook interface (OnTaskWrite /
// OnTaskDelete) so the task service can dual-write on every mutation.
type Serializer struct {
	cfg        Config
	store      store.Store
	selfWrites *selfWriteSet
	log        *slog.Logger
	notify     func(paths []string) // optional autocommit hook (M2); nil when off
	// registry is the indexer's dynamically-registered repo-local .mcplexer/
	// dir set (M6 — federation). Shared via ShareSelfWrites so the outbound
	// path can route a write to the repo-local canonical root when the
	// workspace has one registered, instead of unconditionally landing in the
	// central brain. Nil until shared (central-only behaviour).
	registry *dirRegistry
}

// NewSerializer constructs a Serializer sharing the Indexer's self-write
// set so an outbound write's echo is suppressed inbound. Pass the
// Indexer's registry via SetSelfWrites after construction, or use
// NewSerializerWith.
func NewSerializer(cfg Config, s store.Store, log *slog.Logger) *Serializer {
	if log == nil {
		log = slog.Default()
	}
	return &Serializer{
		cfg:        cfg,
		store:      s,
		selfWrites: newSelfWriteSet(),
		log:        log,
	}
}

// ShareSelfWrites points the serializer at the indexer's self-write set
// so Mark (serializer) and IsSelf (indexer) operate on the same
// instance. MUST be called when both are wired or self-suppression fails.
func (s *Serializer) ShareSelfWrites(ix *Indexer) {
	if ix != nil {
		s.selfWrites = ix.selfWriteRegistry()
		s.registry = ix.dirRegistry()
	}
}

// SetCommitNotify wires the autocommit callback (M2). Nil disables it.
func (s *Serializer) SetCommitNotify(fn func(paths []string)) { s.notify = fn }

// notifyPaths signals the autocommitter about touched paths (writes AND
// deletes), so a serializer-driven mutation is committed even if the
// fsnotify watcher is unavailable. No-op when the autocommit hook is unset.
func (s *Serializer) notifyPaths(paths ...string) {
	if s.notify != nil && len(paths) > 0 {
		s.notify(paths)
	}
}

// OnTaskWrite serializes a task to its .md file. It is the BrainHook
// method the task service calls on every mutator. Errors are logged, not
// propagated — a serialize failure must never fail the underlying tool
// call (the DB row is already written; the file is a derived artifact).
func (s *Serializer) OnTaskWrite(ctx context.Context, t *store.Task) {
	if t == nil || t.DeletedAt != nil {
		return
	}
	if err := s.WriteTask(ctx, t); err != nil {
		s.log.Warn("brain: serialize task", "id", t.ID, "error", err)
	}
}

// OnTaskDelete removes a task's .md file + index bookkeeping.
func (s *Serializer) OnTaskDelete(ctx context.Context, id, workspaceID string) {
	if err := s.DeleteTask(ctx, id, workspaceID); err != nil {
		s.log.Warn("brain: delete task file", "id", id, "error", err)
	}
}

// WriteTask renders the task (frontmatter + prose + rendered notes) and
// writes it to <Dir>/workspaces/<ws>/tasks/<id>-<slug>.md, guarded by the
// hash-CAS gate. On a CAS mismatch (a human edited since last index) the
// write is aborted, a .conflict sidecar is written, and a brain_errors
// row is recorded — never a silent last-write-wins.
func (s *Serializer) WriteTask(ctx context.Context, t *store.Task) error {
	if t == nil {
		return errors.New("brain: WriteTask: nil task")
	}
	data, err := s.renderTask(ctx, t)
	if err != nil {
		return err
	}

	path, err := s.resolveTaskPath(ctx, t)
	if err != nil {
		return err
	}
	wrote, sha, err := s.guardedWrite(ctx, path, data, EntityKindTask)
	if err != nil {
		return err
	}
	if wrote {
		// Record the index_files row immediately so the entity↔path binding
		// is authoritative right after an outbound write (the inbound
		// indexer would skip this file as self-induced, so it must not be
		// the one to establish the binding).
		s.recordIndexFile(ctx, path, sha, EntityKindTask, t.ID, t.WorkspaceID)
	}
	return nil
}

// recordIndexFile upserts the index_files bookkeeping row after an
// outbound write, binding the entity to its on-disk path. Best-effort:
// a failure is logged, never fatal (the next inbound reindex reconciles).
func (s *Serializer) recordIndexFile(ctx context.Context, path, sha, kind, id, workspaceID string) {
	info, err := os.Stat(path)
	if err != nil {
		s.log.Warn("brain: stat after write", "path", path, "error", err)
		return
	}
	f := &store.IndexFile{
		Path:        path,
		WorkspaceID: workspaceID,
		EntityKind:  kind,
		EntityID:    id,
		Source:      s.resolveSource(path, workspaceID),
		Sha:         sha,
		Mtime:       taskFileMtime(info.ModTime()),
		Size:        info.Size(),
	}
	if err := s.store.UpsertIndexFile(ctx, f); err != nil {
		s.log.Warn("brain: upsert index after write", "path", path, "error", err)
	}
}

// resolveTaskPath returns the canonical .md path for a task. It prefers
// the path already recorded in index_files (so a title change does not
// orphan the existing file by relocating it), falling back to the
// title-derived path for a first write.
func (s *Serializer) resolveTaskPath(ctx context.Context, t *store.Task) (string, error) {
	if f, err := s.findIndexByEntity(ctx, EntityKindTask, t.ID); err == nil && f != nil && f.Path != "" {
		return f.Path, nil
	}
	return s.taskPath(t)
}

// DeleteTask removes the task file (best-effort across either filename
// form) and its index_files row.
func (s *Serializer) DeleteTask(ctx context.Context, id, workspaceID string) error {
	// The index row is the authoritative path mapping; prefer it.
	if f, err := s.findIndexByEntity(ctx, EntityKindTask, id); err == nil && f != nil {
		s.removePath(f.Path)
		_ = s.store.DeleteIndexFile(ctx, f.Path)
		s.notifyPaths(f.Path)
		return nil
	}
	// Fall back to recomputing the path from id+slug (best effort — slug
	// may differ, so also glob the prefix).
	root, err := s.canonicalWorkspaceDir(workspaceID)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, taskSubdir)
	if matches, _ := filepath.Glob(filepath.Join(dir, id+"*.md")); len(matches) > 0 {
		for _, m := range matches {
			s.removePath(m)
			_ = s.store.DeleteIndexFile(ctx, m)
			s.notifyPaths(m)
		}
	}
	return nil
}

// guardedWrite performs the CAS gate + atomic write + self-mark for one
// file. A CAS mismatch writes a .conflict sidecar and records an error
// instead of clobbering.
// guardedWrite returns wrote=true with the written content sha when the
// file was (re)written, or wrote=false when a CAS conflict diverted the
// content to a .conflict sidecar instead of clobbering.
func (s *Serializer) guardedWrite(ctx context.Context, path string, data []byte, kind string) (wrote bool, sha string, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, "", fmt.Errorf("brain: mkdir %s: %w", filepath.Dir(path), err)
	}

	if conflict, err := s.casConflict(ctx, path); err != nil {
		return false, "", err
	} else if conflict {
		return false, "", s.writeConflictSidecar(ctx, path, data, kind)
	}

	sha = hashBytes(data)
	s.selfWrites.Mark(path, sha) // mark BEFORE the write so the echo is suppressed
	if err := atomicWrite(path, data); err != nil {
		return false, "", fmt.Errorf("brain: write %s: %w", path, err)
	}
	// A clean write supersedes any prior conflict for this path: clear the
	// stale _file brain_errors row so the dashboard list and the Editor's
	// conflictRecorded probe don't report a resolved conflict as live.
	if cerr := s.store.ClearBrainErrorsForPath(ctx, path); cerr != nil {
		s.log.Warn("brain: clear errors after write", "path", path, "error", cerr)
	}
	s.notifyPaths(path)
	return true, sha, nil
}

// casConflict reports whether the on-disk file diverges from the
// last-indexed sha (a human edited it since). A missing file or a missing
// index row means "no conflict" (first write).
func (s *Serializer) casConflict(ctx context.Context, path string) (bool, error) {
	cur, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // file not present yet — safe to create
		}
		return false, fmt.Errorf("brain: cas read %s: %w", path, err)
	}
	idx, err := s.store.GetIndexFile(ctx, path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// File exists but we never indexed it — treat as a conflict so
			// we don't blow away content we didn't author.
			return true, nil
		}
		return false, fmt.Errorf("brain: cas get index %s: %w", path, err)
	}
	return hashBytes(cur) != idx.Sha, nil
}

// writeConflictSidecar writes the would-be content to a .conflict file and
// records a brain_errors row so the dashboard surfaces it. The original
// file is left untouched.
func (s *Serializer) writeConflictSidecar(ctx context.Context, path string, data []byte, kind string) error {
	sidecar := path + ".conflict"
	if err := atomicWrite(sidecar, data); err != nil {
		return fmt.Errorf("brain: write conflict sidecar %s: %w", sidecar, err)
	}
	be := &store.BrainError{
		Path:       path,
		EntityKind: kind,
		Field:      "_file",
		Reason:     "outbound write conflicted with a concurrent edit; wrote " + filepath.Base(sidecar),
	}
	if err := s.store.RecordBrainError(ctx, be); err != nil {
		s.log.Warn("brain: record conflict error", "path", path, "error", err)
	}
	return nil
}

// idempotentExport writes data to path as a ONE-WAY derived projection: a
// no-op when the file already holds identical bytes, an atomic overwrite when
// it differs or is absent. Unlike guardedWrite it never treats an existing
// file as a conflict — the SOURCE (e.g. the skill registry) is canonical, not
// the projected file, and there is no write-back path — so it neither diverts
// to a .conflict sidecar nor records a brain_errors row. This is the right
// semantics for the skill export, whose guarded predecessor self-conflicted on
// every daemon restart (the never-indexed projection looked like a foreign
// edit to the CAS gate) and flooded brain_errors. Any stale conflict
// bookkeeping from that old path is healed so a single restart drains it.
func (s *Serializer) idempotentExport(ctx context.Context, path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("brain: mkdir %s: %w", filepath.Dir(path), err)
	}
	sha := hashBytes(data)
	if cur, err := os.ReadFile(path); err == nil && hashBytes(cur) == sha {
		s.healStaleConflict(ctx, path) // already current — just drain old conflict noise
		return nil
	}
	s.selfWrites.Mark(path, sha) // mark BEFORE the write so the fsnotify echo is suppressed
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("brain: write %s: %w", path, err)
	}
	s.healStaleConflict(ctx, path)
	s.notifyPaths(path)
	return nil
}

// healStaleConflict clears any stale _file conflict brain_errors row and
// removes the orphaned .conflict sidecar left by the previous guarded
// skill-export path, so re-exporting a projection self-heals the
// false-positive conflict spam. Best-effort: failures are logged, not fatal.
func (s *Serializer) healStaleConflict(ctx context.Context, path string) {
	if err := s.store.ClearBrainErrorsForPath(ctx, path); err != nil {
		s.log.Warn("brain: clear stale conflict", "path", path, "error", err)
	}
	if err := os.Remove(path + ".conflict"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("brain: remove stale conflict sidecar", "path", path, "error", err)
	}
}

// taskPath computes the canonical path for a task .md file.
func (s *Serializer) taskPath(t *store.Task) (string, error) {
	name := t.ID
	if slug := slugify(t.Title); slug != "" {
		name = t.ID + "-" + slug
	}
	root, err := s.canonicalWorkspaceDir(t.WorkspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, taskSubdir, name+".md"), nil
}

// findIndexByEntity scans index_files for the row materialising a given
// entity. Returns nil (no error) when none is found.
func (s *Serializer) findIndexByEntity(ctx context.Context, kind, id string) (*store.IndexFile, error) {
	files, err := s.store.ListIndexFiles(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range files {
		if files[i].EntityKind == kind && files[i].EntityID == id {
			return &files[i], nil
		}
	}
	return nil, nil
}

// removePath deletes a file, tolerating absence.
func (s *Serializer) removePath(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("brain: remove file", "path", path, "error", err)
	}
}
