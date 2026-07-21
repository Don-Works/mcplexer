package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// SaveMemory persists a memory record then serializes it. Create vs update
// mirrors SaveTask. Entity links are reconciled from the submitted set
// (the file's entities frontmatter is canonical for the link set, per M4).
func (e *Editor) SaveMemory(ctx context.Context, rec MemoryRecord) (*MemoryRecord, error) {
	if e.ser == nil {
		return nil, errors.New("brain: editor has no serializer (brain disabled)")
	}
	m, refs, err := e.memoryRecordToRow(ctx, rec)
	if err != nil {
		return nil, err
	}
	if err := validateMemoryRow(m); err != nil {
		return nil, err
	}

	// if_hash CAS pre-check (see SaveTask): a stale-base edit aborts with a
	// structured 409 for the reconciler before any mutation.
	if cErr := e.checkMemoryIfHash(ctx, m.ID, rec.IfHash); cErr != nil {
		return nil, cErr
	}

	// CAS pre-check BEFORE persisting (see SaveTask for the rationale): a
	// conflict aborts the save, diverts the intended content to a .conflict
	// sidecar, and returns ErrConflict without diverging the index from the
	// canonical file.
	path, conflict, err := e.ser.PreflightMemoryConflict(ctx, m)
	if err != nil {
		return nil, err
	}
	if conflict {
		data, rErr := e.ser.RenderMemoryRow(m, refs)
		if rErr != nil {
			return nil, rErr
		}
		if cErr := e.ser.RecordConflict(ctx, path, data, EntityKindMemory); cErr != nil {
			return nil, cErr
		}
		return nil, ErrConflict
	}

	if err := e.persistMemory(ctx, m); err != nil {
		return nil, err
	}
	if err := e.reconcileEntities(ctx, m.ID, refs); err != nil {
		return nil, err
	}

	writeErr := e.ser.WriteMemory(ctx, m.ID)
	saved := e.memoryRowToRecord(ctx, m.ID)
	if saved == nil {
		saved = &rec
		saved.ID = m.ID
	}
	if writeErr != nil {
		return saved, writeErr
	}
	if e.conflictRecorded(ctx, EntityKindMemory, m.ID) {
		return saved, ErrConflict
	}
	return saved, nil
}

// memoryRecordToRow builds the store.MemoryEntry + entity refs from the
// submitted record, preserving server-owned fields on update.
func (e *Editor) memoryRecordToRow(ctx context.Context, rec MemoryRecord) (*store.MemoryEntry, []store.EntityRef, error) {
	var base *store.MemoryEntry
	if rec.ID != "" {
		existing, err := e.store.GetMemory(ctx, rec.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("brain editor: load memory %s: %w", rec.ID, err)
		}
		base = existing
	} else {
		now := time.Now().UTC()
		base = &store.MemoryEntry{ID: newID(), CreatedAt: now, SourceKind: "user"}
	}

	base.Name = strings.TrimSpace(rec.Name)
	base.Kind = strings.TrimSpace(rec.Kind)
	if base.Kind == "" {
		base.Kind = MemoryKindNote
	}
	base.Content = strings.TrimRight(rec.Content, "\n")
	base.Pinned = rec.Pinned
	base.UpdatedAt = time.Now().UTC()
	if ws := strings.TrimSpace(rec.Workspace); ws != "" && ws != "global" {
		w := ws
		base.WorkspaceID = &w
	} else {
		base.WorkspaceID = nil
	}

	tags, err := encodeStringSlice(normalizeTags(rec.Tags))
	if err != nil {
		return nil, nil, fmt.Errorf("brain editor: encode tags: %w", err)
	}
	base.TagsJSON = tags

	if base.Kind == MemoryKindFact && rec.TValidStart != nil {
		base.TValidStart = *rec.TValidStart
	}
	if base.Kind == MemoryKindFact && base.TValidStart.IsZero() {
		base.TValidStart = base.UpdatedAt
	}

	var refs []store.EntityRef
	for _, l := range rec.Entities {
		if strings.TrimSpace(l.Kind) == "" || strings.TrimSpace(l.ID) == "" {
			continue
		}
		refs = append(refs, store.EntityRef{Kind: l.Kind, ID: l.ID, Role: l.Role})
	}
	return base, refs, nil
}

// persistMemory routes to WriteMemory (insert) vs UpdateMemory depending on
// existence. WriteMemory is INSERT-only, so an existing row must go through
// UpdateMemory to avoid a PK conflict (mirrors the indexer's M4 logic).
//
// On a content-changing in-place update, store.UpdateMemory drops the stale
// memories_vec row; we fire the re-embed hook (best-effort, nil-safe) so the
// memory Service rebuilds the vector from the new content. Detect the change
// against the row we already probed so we don't re-embed on a metadata-only
// edit (which leaves the vector valid).
func (e *Editor) persistMemory(ctx context.Context, m *store.MemoryEntry) error {
	prior, err := e.store.GetMemory(ctx, m.ID)
	switch {
	case err == nil:
		if uerr := e.store.UpdateMemory(ctx, m); uerr != nil {
			return fmt.Errorf("brain editor: update memory: %w", uerr)
		}
		fireReEmbedIfContentChanged(ctx, e.reEmbed, prior, m)
	case errors.Is(err, store.ErrNotFound):
		if werr := e.store.WriteMemory(ctx, m); werr != nil {
			return fmt.Errorf("brain editor: write memory: %w", werr)
		}
	default:
		return fmt.Errorf("brain editor: probe memory: %w", err)
	}
	return nil
}

// reconcileEntities makes the DB link set match the submitted set: link
// every submitted ref, unlink any DB link the GUI dropped (the file is
// canonical for the link set, per M4).
func (e *Editor) reconcileEntities(ctx context.Context, memoryID string, want []store.EntityRef) error {
	have, err := e.store.ListMemoryEntities(ctx, memoryID)
	if err != nil {
		return fmt.Errorf("brain editor: list entities: %w", err)
	}
	wantSet := make(map[string]store.EntityRef, len(want))
	for _, r := range want {
		role := r.Role
		if role == "" {
			role = store.EntityRoleSubject
		}
		wantSet[r.Kind+"\x00"+r.ID+"\x00"+role] = store.EntityRef{Kind: r.Kind, ID: r.ID, Role: role}
		if err := e.store.LinkMemoryEntity(ctx, memoryID, store.EntityRef{Kind: r.Kind, ID: r.ID, Role: role}, "user"); err != nil {
			return fmt.Errorf("brain editor: link entity: %w", err)
		}
	}
	for _, h := range have {
		key := h.EntityKind + "\x00" + h.EntityID + "\x00" + h.Role
		if _, keep := wantSet[key]; keep {
			continue
		}
		if err := e.store.UnlinkMemoryEntity(ctx, memoryID, store.EntityRef{Kind: h.EntityKind, ID: h.EntityID, Role: h.Role}); err != nil {
			return fmt.Errorf("brain editor: unlink entity: %w", err)
		}
	}
	return nil
}
