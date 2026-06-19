package brain

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/store"
)

// indexPersonFile parses, validates, and upserts a single CRM person .md file.
// New files live under workspaces/<workspace>/crm/people/; legacy central
// crm/people/ files are imported into the default CRM workspace. Validation
// failures are recorded as brain_errors and the row is NOT upserted.
func (ix *Indexer) indexPersonFile(ctx context.Context, path string, data []byte, sha string, info os.FileInfo) error {
	fm, body, err := ParsePerson(data)
	if err != nil {
		ix.recordError(ctx, path, EntityKindPerson, err)
		return fmt.Errorf("brain: parse person %s: %w", path, err)
	}
	fm.Workspace = personWorkspaceForPath(path, fm.Workspace)
	if err := ValidatePerson(fm, baseName(path)); err != nil {
		ix.recordError(ctx, path, EntityKindPerson, err)
		return fmt.Errorf("brain: validate person %s: %w", path, err)
	}

	p, refs, err := fm.ToPerson(body)
	if err != nil {
		ix.recordError(ctx, path, EntityKindPerson, err)
		return fmt.Errorf("brain: convert person %s: %w", path, err)
	}

	if err := ix.upsertPerson(ctx, p, refs); err != nil {
		return fmt.Errorf("brain: upsert person %s: %w", path, err)
	}

	_ = ix.store.ClearBrainErrorsForPath(ctx, path)
	ix.recordIndexFile(ctx, path, sha, info, EntityKindPerson, p.ID)
	return nil
}

// upsertPerson creates or updates the person row and re-derives its entity
// links from the file's `entities:` frontmatter (the file is canonical).
func (ix *Indexer) upsertPerson(ctx context.Context, p *store.PersonEntry, refs []store.EntityRef) error {
	_, err := ix.store.GetPerson(ctx, p.ID)
	switch {
	case p.ID != "" && err == nil:
		if uErr := ix.store.UpdatePerson(ctx, p); uErr != nil {
			return uErr
		}
	case p.ID == "" || errors.Is(err, store.ErrNotFound):
		if wErr := ix.store.WritePerson(ctx, p); wErr != nil {
			return wErr
		}
	default:
		return err
	}
	return ix.reconcilePersonEntities(ctx, p.ID, refs)
}

// reconcilePersonEntities re-derives the person_entities join rows so they
// match the file's `entities:` list exactly: every ref is linked (idempotent),
// any link the file no longer carries is unlinked. The file is the single
// writer of the link set. Mirrors reconcileEntities (memory).
func (ix *Indexer) reconcilePersonEntities(ctx context.Context, personID string, refs []store.EntityRef) error {
	want := make(map[string]store.EntityRef, len(refs))
	for _, r := range refs {
		if r.Kind == "" || r.ID == "" {
			continue
		}
		want[entityKey(normalizeEntityRef(r))] = r
		if err := ix.store.LinkPersonEntity(ctx, personID, r, ""); err != nil {
			return fmt.Errorf("brain: link person entity %s:%s: %w", r.Kind, r.ID, err)
		}
	}
	existing, err := ix.store.ListPersonEntities(ctx, personID)
	if err != nil {
		return fmt.Errorf("brain: list person entities %s: %w", personID, err)
	}
	for _, row := range existing {
		ref := store.EntityRef{Kind: row.EntityKind, ID: row.EntityID, Role: row.Role}
		if _, keep := want[entityKey(normalizeEntityRef(ref))]; keep {
			continue
		}
		if err := ix.store.UnlinkPersonEntity(ctx, personID, ref); err != nil {
			return fmt.Errorf("brain: unlink person entity %s:%s: %w", row.EntityKind, row.EntityID, err)
		}
	}
	return nil
}
