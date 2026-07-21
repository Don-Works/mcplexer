package brain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// Entity-kind markers stored on index_files.entity_kind so deletes +
// verify can reconcile a file back to its DB row.
const (
	EntityKindTask      = "task"
	EntityKindMemory    = "memory"
	EntityKindWorkspace = "workspace"
	EntityKindPerson    = "person"
)

// Per-workspace subdir names + the legacy central CRM layout.
const (
	taskSubdir        = "tasks"
	memorySubdir      = "memory"
	memoryFactsSubdir = "facts"
	workspaceFile     = "workspace.md"
	crmSubdir         = "crm"
	peopleSubdir      = "people"
)

// Indexer is the inbound half of the sync engine: it turns on-disk
// Markdown files into DB rows via the existing Store methods (so FTS5
// triggers + bi-temporal logic fire unchanged — SPEC §6.6). Files are the
// single inbound writer; the DB is a derived, rebuildable index.
type Indexer struct {
	cfg        Config
	store      store.Store
	log        *slog.Logger
	selfWrites *selfWriteSet
	// invalidate is an optional callback fired after a workspace.md change
	// is applied, so the daemon can bump the route cache's wsVersion and
	// re-resolve sessions (SPEC §9). Nil when unset (M1-only installs).
	invalidate func(workspaceID string)
	// registry maps dynamically-registered repo-local .mcplexer/ dirs to
	// their (workspace, source) (M6 — federation). Constructed eagerly in
	// NewIndexer and never reassigned; only its mutex-guarded slice mutates.
	// Empty until the first RegisterDir. Central-brain paths use the
	// path-derived fallback.
	registry *dirRegistry
	// onRegisterDir fires once per newly-registered repo dir so the daemon
	// can extend the fsnotify watch set. Nil when unset.
	onRegisterDir func(dir string)
	// reEmbed is fired after an indexed memory .md edit rewrites the row's
	// content in place (store.UpdateMemory drops the stale vector); the
	// daemon wires it from memory.Service.ReEmbedAfterUpdate so vector recall
	// recovers. Nil = no-op (FTS-only after edit). See ReEmbedHook.
	reEmbed ReEmbedHook
}

// SetWorkspaceInvalidate wires the route-cache invalidation callback fired
// after a workspace.md change is indexed. Nil-safe.
func (ix *Indexer) SetWorkspaceInvalidate(fn func(workspaceID string)) {
	ix.invalidate = fn
}

// SetReEmbedHook installs the re-embed-on-edit hook post-construction.
// Nil-safe; nil (the default) leaves an edited memory FTS-only until
// something else re-embeds. Wire it from the memory Service in the daemon.
func (ix *Indexer) SetReEmbedHook(h ReEmbedHook) {
	if ix == nil {
		return
	}
	ix.reEmbed = h
}

// NewIndexer constructs an Indexer. The selfWriteSet is shared with the
// Serializer (via SetSelfWrites) so an outbound write's fsnotify echo is
// recognised and skipped.
func NewIndexer(cfg Config, s store.Store, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.Default()
	}
	return &Indexer{
		cfg:        cfg,
		store:      s,
		log:        log,
		selfWrites: newSelfWriteSet(),
		// registry is constructed eagerly (never reassigned) so the
		// session-resolve goroutine (RegisterDir) and the watcher goroutine
		// (resolveSourceAndWorkspace) never race on the pointer field — only
		// the registry's own mutex-guarded slice is mutated thereafter.
		registry: newDirRegistry(),
	}
}

// selfWriteRegistry exposes the shared self-write set so the Serializer
// can Mark its writes against the same instance the Indexer consults.
func (ix *Indexer) selfWriteRegistry() *selfWriteSet { return ix.selfWrites }

// dirRegistry exposes the shared repo-dir registry so the Serializer can
// route an outbound write to a workspace's repo-local canonical root (M6 —
// federation) instead of unconditionally landing in the central brain.
func (ix *Indexer) dirRegistry() *dirRegistry { return ix.registry }

// ReindexAll walks every workspace's task directory and indexes each
// file. It is the correctness backstop (SPEC §6.7): cheap, disposable,
// always-correct. Per-file errors are logged + recorded as brain_errors
// but do not abort the sweep.
func (ix *Indexer) ReindexAll(ctx context.Context) error {
	root := filepath.Join(ix.cfg.Dir, "workspaces")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no brain repo yet — nothing to index
		}
		return fmt.Errorf("brain: reindex read workspaces: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wsDir := filepath.Join(root, e.Name())
		// workspace.md first so the workspace row exists before its tasks/
		// memories reference it.
		wsPath := filepath.Join(wsDir, workspaceFile)
		if _, statErr := os.Stat(wsPath); statErr == nil {
			if err := ix.IndexFile(ctx, wsPath); err != nil {
				ix.log.Warn("brain: reindex workspace.md", "workspace", e.Name(), "error", err)
			}
		}
		if err := ix.reindexDir(ctx, filepath.Join(wsDir, taskSubdir)); err != nil {
			ix.log.Warn("brain: reindex workspace tasks", "workspace", e.Name(), "error", err)
		}
		// Memory notes live flat under memory/; facts under memory/facts/.
		// The .history/ retention subdir is deliberately skipped (superseded
		// versions are archive-only, never indexed).
		memDir := filepath.Join(wsDir, memorySubdir)
		if err := ix.reindexDir(ctx, memDir, memoryFactsSubdir); err != nil {
			ix.log.Warn("brain: reindex workspace memory", "workspace", e.Name(), "error", err)
		}
		if err := ix.reindexDir(ctx, filepath.Join(memDir, memoryFactsSubdir)); err != nil {
			ix.log.Warn("brain: reindex workspace facts", "workspace", e.Name(), "error", err)
		}
		if err := ix.reindexDir(ctx, filepath.Join(wsDir, crmSubdir, peopleSubdir)); err != nil {
			ix.log.Warn("brain: reindex workspace crm people", "workspace", e.Name(), "error", err)
		}
	}
	// Legacy CRM people lived at <Dir>/crm/people/<name>.md. Keep indexing
	// that folder so old files are imported into the restrictive default CRM
	// workspace, then canonical outbound writes move them under workspaces/crm/.
	crmDir := filepath.Join(ix.cfg.Dir, crmSubdir, peopleSubdir)
	if err := ix.reindexDir(ctx, crmDir); err != nil {
		ix.log.Warn("brain: reindex crm people", "error", err)
	}
	ix.pruneOrphanedErrors(ctx)
	if err := ix.WriteVaultIndex(ctx); err != nil {
		ix.log.Warn("brain: write vault INDEX.md", "error", err)
	}
	return nil
}

// pruneOrphanedErrors clears brain_errors rows whose path is no longer a
// legitimate brain-record location — outside the central brain Dir AND outside
// every registered repo-local dir. It drains stale errors recorded before a
// fix narrowed what the indexer touches: the gateway data dir (~/.mcplexer)
// was briefly mis-discovered as a repo brain, so its memory-exports/*.md
// digests got indexed as phantom tasks and left error rows the watcher will
// never revisit. A repo path whose dir is not currently registered is
// re-recorded on that repo's next RegisterDir reindex, so transient pruning is
// self-correcting. Best-effort: every failure is logged, never fatal.
func (ix *Indexer) pruneOrphanedErrors(ctx context.Context) {
	errs, err := ix.store.ListBrainErrors(ctx)
	if err != nil {
		ix.log.Warn("brain: list errors for prune", "error", err)
		return
	}
	root := filepath.Clean(ix.cfg.Dir)
	for i := range errs {
		p := filepath.Clean(errs[i].Path)
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			continue // under the central brain — a legitimate record path
		}
		if ix.registry != nil {
			if _, ok := ix.registry.resolve(p); ok {
				continue // under a registered repo-local brain dir
			}
		}
		if err := ix.store.ClearBrainErrorsForPath(ctx, p); err != nil {
			ix.log.Warn("brain: prune orphaned error", "path", p, "error", err)
		}
	}
}

// reindexDir indexes every *.md file directly under dir (non-recursive;
// record files are flat). A missing dir is not an error. Subdirectories
// are checked against allowedSubdirs: expected ones (e.g. facts/ under
// memory/) and dot-dirs are skipped, anything else is logged loudly and
// repaired — its nested .md files are relocated to sanitized stems
// directly under dir and re-indexed (see indexer_repair.go), so a record
// whose raw name once contained '/' is recovered instead of staying
// permanently invisible to the flat walk.
func (ix *Indexer) reindexDir(ctx context.Context, dir string, allowedSubdirs ...string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			ix.checkSubdir(ctx, dir, e.Name(), allowedSubdirs)
			continue
		}
		if !isMarkdown(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := ix.IndexFile(ctx, path); err != nil {
			ix.log.Warn("brain: index file", "path", path, "error", err)
		}
	}
	return nil
}

// IndexFile is the per-file inbound path: stat → read → sha256 → compare
// the recorded index_files row → skip-if-unchanged; else parse → validate
// → upsert via the Store. Unchanged files (matching sha) are a no-op so
// the watcher can fire liberally. Self-induced writes (recorded by the
// Serializer) are skipped so an outbound write does not loop back.
func (ix *Indexer) IndexFile(ctx context.Context, path string) error {
	if isIgnoredPath(path) {
		// Under a dot-dir (.history/, .git/, .cache/) — archive/derived
		// data, never a canonical record. Skip silently (no error row).
		return nil
	}
	if ix.isVaultIndexPath(path) {
		// The vault-root INDEX.md is a generated projection (WriteVaultIndex),
		// never a canonical record — indexing it would record a phantom task.
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ix.RemoveFile(ctx, path)
		}
		return fmt.Errorf("brain: stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("brain: read %s: %w", path, err)
	}
	sha := hashBytes(data)

	if ix.selfWrites.IsSelf(path, sha) {
		// Our own outbound write — the Serializer already recorded the
		// index_files row (entity binding + sha) before the write, so the
		// echo is a clean no-op. Skipping here is what breaks the
		// write→index→write loop (SPEC §6 outbound step 4).
		return nil
	}

	prev, err := ix.store.GetIndexFile(ctx, path)
	switch {
	case err == nil && prev.Sha == sha:
		return nil // unchanged — incremental fast-path
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("brain: get index file %s: %w", path, err)
	}

	switch kindForPath(path) {
	case EntityKindPerson:
		return ix.indexPersonFile(ctx, path, data, sha, info)
	case EntityKindMemory:
		return ix.indexMemoryFile(ctx, path, data, sha, info)
	case EntityKindWorkspace:
		return ix.indexWorkspaceFile(ctx, path, data, sha, info)
	default:
		return ix.indexTaskFile(ctx, path, data, sha, info)
	}
}

// RemoveFile reconciles a deleted file: soft-delete the materialised row
// (when known) and drop the index_files + brain_errors bookkeeping.
func (ix *Indexer) RemoveFile(ctx context.Context, path string) error {
	prev, err := ix.store.GetIndexFile(ctx, path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // never indexed — nothing to reconcile
		}
		return fmt.Errorf("brain: get index file %s: %w", path, err)
	}
	if prev.EntityID != "" {
		switch prev.EntityKind {
		case EntityKindTask:
			if err := ix.store.SoftDeleteTask(ctx, prev.EntityID); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("brain: soft-delete task %s: %w", prev.EntityID, err)
			}
		case EntityKindMemory:
			if err := ix.store.SoftDeleteMemory(ctx, prev.EntityID); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("brain: soft-delete memory %s: %w", prev.EntityID, err)
			}
		case EntityKindPerson:
			if err := ix.store.SoftDeletePerson(ctx, prev.EntityID); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("brain: soft-delete person %s: %w", prev.EntityID, err)
			}
		case EntityKindWorkspace:
			// A removed workspace.md does NOT delete the workspace row — the
			// workspace may still be referenced by live sessions/routes.
			// We only drop the index binding (below) so a re-created file
			// re-indexes cleanly.
		}
	}
	if err := ix.store.ClearBrainErrorsForPath(ctx, path); err != nil {
		ix.log.Warn("brain: clear errors on remove", "path", path, "error", err)
	}
	if err := ix.store.DeleteIndexFile(ctx, path); err != nil {
		return fmt.Errorf("brain: delete index file %s: %w", path, err)
	}
	return nil
}

// recordIndexFile upserts the index_files bookkeeping row for path. The
// owning workspace + source are resolved registry-first (a repo-local
// .mcplexer/ dir records its workspace + source=repo) then fall back to the
// central path-derived workspace + source=central (M6 — federation).
func (ix *Indexer) recordIndexFile(ctx context.Context, path, sha string, info os.FileInfo, kind, id string) {
	wsID, source := ix.resolveSourceAndWorkspace(path)
	f := &store.IndexFile{
		Path:        path,
		WorkspaceID: wsID,
		EntityKind:  kind,
		EntityID:    id,
		Source:      source,
		Sha:         sha,
		Mtime:       taskFileMtime(info.ModTime()),
		Size:        info.Size(),
	}
	if err := ix.store.UpsertIndexFile(ctx, f); err != nil {
		ix.log.Warn("brain: upsert index file", "path", path, "error", err)
	}
}

// recordError clears prior errors for the path and records a fresh one,
// keying the field/reason off a *ValidationError when present.
func (ix *Indexer) recordError(ctx context.Context, path, kind string, cause error) {
	_ = ix.store.ClearBrainErrorsForPath(ctx, path)
	field, reason := "", cause.Error()
	var ve *ValidationError
	if errors.As(cause, &ve) {
		field = ve.Field
		reason = ve.Reason
	}
	be := &store.BrainError{Path: path, EntityKind: kind, Field: field, Reason: reason}
	if err := ix.store.RecordBrainError(ctx, be); err != nil {
		ix.log.Warn("brain: record brain error", "path", path, "error", err)
	}
}
