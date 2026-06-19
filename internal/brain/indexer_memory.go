package brain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// historySubdir holds superseded fact versions so the bi-temporal trace
// survives in git even after the live file is rewritten (SPEC §5).
const historySubdir = ".history"

// indexMemoryFile parses, validates, and upserts a single memory .md file
// (a note under memory/ or a fact under memory/facts/). Validation
// failures are recorded as brain_errors and the row is NOT upserted. On a
// fact whose content supersedes a prior active version, the prior version
// is archived to memory/facts/.history/ before the upsert so the
// bi-temporal trail is retained on disk.
func (ix *Indexer) indexMemoryFile(ctx context.Context, path string, data []byte, sha string, info os.FileInfo) error {
	fm, body, err := ParseMemory(data)
	if err != nil {
		ix.recordError(ctx, path, EntityKindMemory, err)
		return fmt.Errorf("brain: parse memory %s: %w", path, err)
	}
	if err := ValidateMemory(fm, baseName(path)); err != nil {
		ix.recordError(ctx, path, EntityKindMemory, err)
		return fmt.Errorf("brain: validate memory %s: %w", path, err)
	}

	mem, refs, err := fm.ToMemory(body)
	if err != nil {
		ix.recordError(ctx, path, EntityKindMemory, err)
		return fmt.Errorf("brain: convert memory %s: %w", path, err)
	}

	if err := ix.archiveSupersededFact(ctx, path, mem); err != nil {
		ix.log.Warn("brain: archive superseded fact", "path", path, "error", err)
	}

	if err := ix.upsertMemory(ctx, mem, refs); err != nil {
		return fmt.Errorf("brain: upsert memory %s: %w", path, err)
	}

	_ = ix.store.ClearBrainErrorsForPath(ctx, path)
	ix.recordIndexFile(ctx, path, sha, info, EntityKindMemory, mem.ID)
	return nil
}

// upsertMemory creates or updates the memory row and re-derives its entity
// links from the file's `entities:` frontmatter. Existing id → UpdateMemory
// (in-place rewrite, FTS update trigger fires); else WriteMemory (insert,
// FTS insert trigger fires). Then the entity-link set is reconciled to
// match the file exactly (the file is canonical for `entities:`).
func (ix *Indexer) upsertMemory(ctx context.Context, m *store.MemoryEntry, refs []store.EntityRef) error {
	prior, err := ix.store.GetMemory(ctx, m.ID)
	switch {
	case m.ID != "" && err == nil:
		if uErr := ix.store.UpdateMemory(ctx, m); uErr != nil {
			return uErr
		}
		// store.UpdateMemory drops the stale memories_vec row on a content
		// change; re-embed the new content so KNN recovers (best-effort,
		// nil-safe). Skip metadata-only edits — their vector is still valid.
		fireReEmbedIfContentChanged(ctx, ix.reEmbed, prior, m)
	case m.ID == "" || errors.Is(err, store.ErrNotFound):
		if wErr := ix.store.WriteMemory(ctx, m); wErr != nil {
			return wErr
		}
	default:
		return err
	}
	return ix.reconcileEntities(ctx, m.ID, refs)
}

// reconcileEntities re-derives the memory_entities join rows so they match
// the file's `entities:` list exactly: every ref in the file is linked
// (idempotent), and any link in the DB that the file no longer carries is
// unlinked. The file is the single writer of the link set.
func (ix *Indexer) reconcileEntities(ctx context.Context, memoryID string, refs []store.EntityRef) error {
	want := make(map[string]store.EntityRef, len(refs))
	for _, r := range refs {
		if r.Kind == "" || r.ID == "" {
			continue
		}
		// The store defaults an empty role to "subject" and lowercases the
		// id; mirror that here so the want-key matches the row the store
		// persists. Without this, a role-omitted (or differently-cased)
		// `entities:` link is created then immediately unlinked below
		// because want would never contain the canonicalised existing row.
		want[entityKey(normalizeEntityRef(r))] = r
		if err := ix.store.LinkMemoryEntity(ctx, memoryID, r, ""); err != nil {
			return fmt.Errorf("brain: link entity %s:%s: %w", r.Kind, r.ID, err)
		}
	}
	existing, err := ix.store.ListMemoryEntities(ctx, memoryID)
	if err != nil {
		return fmt.Errorf("brain: list entities %s: %w", memoryID, err)
	}
	for _, row := range existing {
		ref := store.EntityRef{Kind: row.EntityKind, ID: row.EntityID, Role: row.Role}
		if _, keep := want[entityKey(normalizeEntityRef(ref))]; keep {
			continue
		}
		if err := ix.store.UnlinkMemoryEntity(ctx, memoryID, ref); err != nil {
			return fmt.Errorf("brain: unlink entity %s:%s: %w", row.EntityKind, row.EntityID, err)
		}
	}
	return nil
}

// entityKey is the dedup key for an entity link (kind:id:role).
func entityKey(r store.EntityRef) string {
	return r.Kind + ":" + r.ID + ":" + r.Role
}

// normalizeEntityRef canonicalises a ref to match how the store persists
// it: kind+id lower-cased and trimmed, role defaulted to "subject". This
// keeps the reconcile want-set keyed identically to the rows ListMemoryEntities
// returns, so a role-omitted or differently-cased link is not spuriously
// unlinked on the next index pass.
func normalizeEntityRef(r store.EntityRef) store.EntityRef {
	kind := strings.ToLower(strings.TrimSpace(r.Kind))
	id := strings.ToLower(strings.TrimSpace(r.ID))
	role := strings.ToLower(strings.TrimSpace(r.Role))
	if role == "" {
		role = "subject"
	}
	return store.EntityRef{Kind: kind, ID: id, Role: role}
}

// archiveSupersededFact preserves the prior active version of a fact on disk
// before the incoming file's content takes over, keeping the bi-temporal
// trail in git (SPEC §5). It handles BOTH supersession shapes:
//
//   - SAME id (in-place edit of the fact file): archive the current DB row's
//     rendered form when its content differs from the incoming file.
//   - NEW id, SAME name+workspace (the spec'd "write a new fact file to
//     supersede"): the store's WriteMemory will stamp t_valid_end +
//     invalidated_by on the prior active row when the new row is inserted
//     (atomic invalidate-then-insert). Here we additionally archive the prior
//     active fact's FILE to .history/ and REMOVE it from disk + index, so a
//     subsequent reindex of that now-historical file cannot resurrect it as
//     active (UpdateMemory would otherwise clear its t_valid_end). This is
//     the leak the bi-temporal finding flagged.
//
// No-op for notes, for a genuine first write, or when nothing is superseded.
func (ix *Indexer) archiveSupersededFact(ctx context.Context, path string, incoming *store.MemoryEntry) error {
	if incoming.Kind != MemoryKindFact {
		return nil
	}

	// Same-id in-place edit.
	if cur, err := ix.store.GetMemory(ctx, incoming.ID); err == nil {
		if cur.Content == incoming.Content {
			return nil // unchanged
		}
		return ix.archiveFactSnapshot(ctx, path, cur)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	// New-id supersession: find the existing active fact with the same
	// name+workspace. WriteMemory will invalidate its row; we archive +
	// retire its on-disk file so it never re-indexes back to active.
	prior, err := ix.findActiveFactByName(ctx, incoming)
	if err != nil || prior == nil || prior.ID == incoming.ID {
		return err
	}
	if archErr := ix.archiveFactSnapshot(ctx, path, prior); archErr != nil {
		return archErr
	}
	// Retire the prior fact's on-disk file ONLY when it is a DIFFERENT file
	// from the one being indexed. The common shape — a same-name fact rewrites
	// memory/facts/<name>.md in place with a new id (the validator requires
	// name==filename stem, so a same-name supersession reuses the same path) —
	// must NOT delete the file we are about to (re)bind to the new id; the
	// recordIndexFile after upsert simply rebinds the path. Only an orphaned
	// distinct file (rare) needs retiring so it cannot resurrect to active.
	ix.retireSupersededFactFile(ctx, prior.ID, path)
	return nil
}

// findActiveFactByName returns the currently-active fact (t_valid_end IS
// NULL) matching incoming's name within the same workspace bucket, or nil
// when none exists. Scope is built from the incoming row's workspace so a
// global fact (nil workspace) matches global facts only.
func (ix *Indexer) findActiveFactByName(ctx context.Context, incoming *store.MemoryEntry) (*store.MemoryEntry, error) {
	if strings.TrimSpace(incoming.Name) == "" {
		return nil, nil
	}
	f := store.MemoryFilter{
		Kind:  MemoryKindFact,
		Name:  incoming.Name,
		Limit: 2,
	}
	if incoming.WorkspaceID != nil && *incoming.WorkspaceID != "" {
		f.Scope = store.SkillScope{WorkspaceIDs: []string{*incoming.WorkspaceID}}
		f.ScopeFilter = "workspace_only"
	} else {
		f.ScopeFilter = "global_only"
	}
	rows, err := ix.store.ListMemories(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("brain: list active fact %q: %w", incoming.Name, err)
	}
	for i := range rows {
		if rows[i].TValidEnd == nil {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// archiveFactSnapshot renders a fact row to memory/facts/.history/<name>-<stamp>.md
// so its prior bi-temporal state survives in git after the live file changes.
func (ix *Indexer) archiveFactSnapshot(ctx context.Context, path string, fact *store.MemoryEntry) error {
	links, _ := ix.store.ListMemoryEntities(ctx, fact.ID)
	data, err := SerializeMemory(fact, links)
	if err != nil {
		return err
	}
	histDir := filepath.Join(filepath.Dir(path), historySubdir)
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	// recordStem, not the raw name: a fact name containing '/' must not be
	// able to traverse out of (or nest inside) the .history/ archive dir.
	histPath := filepath.Join(histDir, fmt.Sprintf("%s-%s.md", recordStem(fact.Name, fact.ID), stamp))
	return atomicWrite(histPath, data)
}

// retireSupersededFactFile removes the on-disk file of a fact that has just
// been superseded by a new-id fact, so a later reindex cannot resurrect the
// historical row to active. The archived .history/ copy (written first)
// preserves the trail; this only drops the live file + its index binding.
// Best-effort: the prior row is already invalidated in the DB regardless.
func (ix *Indexer) retireSupersededFactFile(ctx context.Context, priorID, incomingPath string) {
	f, err := ix.findIndexByEntity(ctx, EntityKindMemory, priorID)
	if err != nil || f == nil || f.Path == "" {
		return
	}
	if filepath.Clean(f.Path) == filepath.Clean(incomingPath) {
		// Same file rewritten in place with a new id — do NOT delete it; the
		// post-upsert recordIndexFile rebinds the path to the new id. (The
		// prior row is invalidated by WriteMemory; dropping the stale index
		// binding here keeps the new id authoritative.)
		if delErr := ix.store.DeleteIndexFile(ctx, f.Path); delErr != nil {
			ix.log.Warn("brain: rebind superseded fact index", "path", f.Path, "error", delErr)
		}
		return
	}
	if rmErr := os.Remove(f.Path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		ix.log.Warn("brain: retire superseded fact file", "path", f.Path, "error", rmErr)
		return
	}
	// Drop the index binding so the impending fsnotify remove event resolves
	// to a no-op (RemoveFile finds no row) and a later reindex cannot
	// resurrect the historical fact to active. The DB row is already
	// invalidated (t_valid_end stamped) by WriteMemory's insert path.
	if delErr := ix.store.DeleteIndexFile(ctx, f.Path); delErr != nil {
		ix.log.Warn("brain: delete superseded fact index", "path", f.Path, "error", delErr)
	}
}

// findIndexByEntity scans index_files for the row materialising a given
// entity. Returns nil (no error) when none is found. Mirrors the
// Serializer's helper of the same name (the Indexer cannot reach that
// method's receiver).
func (ix *Indexer) findIndexByEntity(ctx context.Context, kind, id string) (*store.IndexFile, error) {
	files, err := ix.store.ListIndexFiles(ctx, "")
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
