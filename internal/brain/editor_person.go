package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// SavePerson persists a CRM person record (create when ID is empty, update
// otherwise) then serializes it to its .md file via the shared outbound path.
// Mirrors SaveMemory: CAS pre-checks abort a stale-base edit before any
// mutation, diverting the intended content to a .conflict sidecar.
func (e *Editor) SavePerson(ctx context.Context, rec PersonRecord) (*PersonRecord, error) {
	if e.ser == nil {
		return nil, errors.New("brain: editor has no serializer (brain disabled)")
	}
	p, refs, err := e.personRecordToRow(ctx, rec)
	if err != nil {
		return nil, err
	}
	if err := validatePersonRow(p); err != nil {
		return nil, err
	}

	if cErr := e.checkPersonIfHash(ctx, p.ID, rec.IfHash); cErr != nil {
		return nil, cErr
	}

	path, conflict, err := e.ser.PreflightPersonConflict(ctx, p)
	if err != nil {
		return nil, err
	}
	if conflict {
		data, rErr := e.ser.RenderPersonRow(p, refs)
		if rErr != nil {
			return nil, rErr
		}
		if cErr := e.ser.RecordConflict(ctx, path, data, EntityKindPerson); cErr != nil {
			return nil, cErr
		}
		return nil, ErrConflict
	}

	if err := e.persistPerson(ctx, p); err != nil {
		return nil, err
	}
	if err := e.reconcilePersonEntities(ctx, p.ID, refs); err != nil {
		return nil, err
	}

	writeErr := e.ser.WritePerson(ctx, p.ID)
	saved := e.personRowToRecord(ctx, p.ID)
	if saved == nil {
		saved = &rec
		saved.ID = p.ID
	}
	if writeErr != nil {
		return saved, writeErr
	}
	if e.conflictRecorded(ctx, EntityKindPerson, p.ID) {
		return saved, ErrConflict
	}
	return saved, nil
}

// personRecordToRow builds the store.PersonEntry + entity refs from the
// submitted record, preserving server-owned provenance on update.
func (e *Editor) personRecordToRow(ctx context.Context, rec PersonRecord) (*store.PersonEntry, []store.EntityRef, error) {
	var base *store.PersonEntry
	if rec.ID != "" {
		existing, err := e.store.GetPerson(ctx, rec.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("brain editor: load person %s: %w", rec.ID, err)
		}
		base = existing
	} else {
		now := time.Now().UTC()
		base = &store.PersonEntry{ID: newID(), CreatedAt: now, SourceKind: store.PersonSourceUser}
	}

	existingWorkspace := base.WorkspaceID
	base.Name = strings.TrimSpace(rec.Name)
	if strings.TrimSpace(rec.Workspace) != "" {
		base.WorkspaceID = normalizePersonWorkspace(rec.Workspace)
	} else {
		base.WorkspaceID = normalizePersonWorkspace(existingWorkspace)
	}
	base.Email = strings.TrimSpace(rec.Email)
	base.Phone = strings.TrimSpace(rec.Phone)
	base.Company = strings.TrimSpace(rec.Company)
	base.Role = strings.TrimSpace(rec.Role)
	base.Notes = strings.TrimRight(rec.Notes, "\n")
	base.Pinned = rec.Pinned
	base.UpdatedAt = time.Now().UTC()

	tags, err := encodeStringSlice(normalizeTags(rec.Tags))
	if err != nil {
		return nil, nil, fmt.Errorf("brain editor: encode tags: %w", err)
	}
	base.TagsJSON = tags

	var refs []store.EntityRef
	for _, l := range rec.Entities {
		if strings.TrimSpace(l.Kind) == "" || strings.TrimSpace(l.ID) == "" {
			continue
		}
		refs = append(refs, store.EntityRef{Kind: l.Kind, ID: l.ID, Role: l.Role})
	}
	return base, refs, nil
}

// persistPerson routes to WritePerson (insert) vs UpdatePerson depending on
// existence. WritePerson is INSERT-only, so an existing row must go through
// UpdatePerson to avoid a PK conflict (mirrors persistMemory).
func (e *Editor) persistPerson(ctx context.Context, p *store.PersonEntry) error {
	_, err := e.store.GetPerson(ctx, p.ID)
	switch {
	case err == nil:
		if uerr := e.store.UpdatePerson(ctx, p); uerr != nil {
			return fmt.Errorf("brain editor: update person: %w", uerr)
		}
	case errors.Is(err, store.ErrNotFound):
		if werr := e.store.WritePerson(ctx, p); werr != nil {
			return fmt.Errorf("brain editor: write person: %w", werr)
		}
	default:
		return fmt.Errorf("brain editor: probe person: %w", err)
	}
	return nil
}

// reconcilePersonEntities makes the DB link set match the submitted set: link
// every submitted ref, unlink any DB link the caller dropped. Mirrors
// reconcileEntities (memory editor).
func (e *Editor) reconcilePersonEntities(ctx context.Context, personID string, want []store.EntityRef) error {
	have, err := e.store.ListPersonEntities(ctx, personID)
	if err != nil {
		return fmt.Errorf("brain editor: list person entities: %w", err)
	}
	wantSet := make(map[string]store.EntityRef, len(want))
	for _, r := range want {
		role := r.Role
		if role == "" {
			role = store.EntityRoleSubject
		}
		wantSet[r.Kind+"\x00"+r.ID+"\x00"+role] = store.EntityRef{Kind: r.Kind, ID: r.ID, Role: role}
		if err := e.store.LinkPersonEntity(ctx, personID, store.EntityRef{Kind: r.Kind, ID: r.ID, Role: role}, "user"); err != nil {
			return fmt.Errorf("brain editor: link person entity: %w", err)
		}
	}
	for _, h := range have {
		key := h.EntityKind + "\x00" + h.EntityID + "\x00" + h.Role
		if _, keep := wantSet[key]; keep {
			continue
		}
		if err := e.store.UnlinkPersonEntity(ctx, personID, store.EntityRef{Kind: h.EntityKind, ID: h.EntityID, Role: h.Role}); err != nil {
			return fmt.Errorf("brain editor: unlink person entity: %w", err)
		}
	}
	return nil
}

// ListPeople returns one workspace's CRM person records, newest first.
func (e *Editor) ListPeople(ctx context.Context, workspace string, limit int) ([]PersonRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := e.store.ListPeople(ctx, store.PersonFilter{
		WorkspaceID: normalizePersonWorkspace(workspace),
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PersonRecord, 0, len(rows))
	for i := range rows {
		if rec := e.personRowToRecord(ctx, rows[i].ID); rec != nil {
			out = append(out, *rec)
		}
	}
	return out, nil
}

// GetPerson returns one editable person record.
func (e *Editor) GetPerson(ctx context.Context, id string) (*PersonRecord, error) {
	rec := e.personRowToRecord(ctx, id)
	if rec == nil {
		return nil, store.ErrNotFound
	}
	return rec, nil
}

// GetPersonDetail is the person counterpart of GetMemoryDetail: it adds the
// verbatim on-disk .md (the FileTruthDisclosure) on top of the projection.
func (e *Editor) GetPersonDetail(ctx context.Context, id string) (*PersonRecord, error) {
	rec, err := e.GetPerson(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.ser != nil && rec.Path != "" {
		rec.Raw, _ = e.ser.ReadRaw(rec.Path)
	}
	return rec, nil
}

// personRowToRecord loads a person + its entity links and projects them into
// the record shape. Returns nil when the person is absent.
func (e *Editor) personRowToRecord(ctx context.Context, id string) *PersonRecord {
	p, err := e.store.GetPerson(ctx, id)
	if err != nil {
		return nil
	}
	tags, _ := decodeStringSlice(p.TagsJSON)
	if tags == nil {
		tags = []string{}
	}
	rec := &PersonRecord{
		ID:        p.ID,
		Workspace: normalizePersonWorkspace(p.WorkspaceID),
		Name:      p.Name,
		Email:     p.Email,
		Phone:     p.Phone,
		Company:   p.Company,
		Role:      p.Role,
		Tags:      tags,
		Pinned:    p.Pinned,
		Notes:     p.Notes,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	if sk := strings.TrimSpace(p.SourceKind); sk != "" {
		rec.Source = &SourceRecord{Kind: sk, SessionID: p.SourceSessionID}
	}
	if links, lerr := e.store.ListPersonEntities(ctx, id); lerr == nil {
		for _, l := range links {
			rec.Entities = append(rec.Entities, EntityLinkFM{Kind: l.EntityKind, ID: l.EntityID, Role: l.Role})
		}
	}
	e.enrichRecordIndex(ctx, EntityKindPerson, p.ID, &rec.Path, &rec.IndexSource, &rec.OnDiskHash, &rec.ValidationError, &rec.ValidationField)
	return rec
}

// DeletePerson soft-deletes the person row and removes its canonical .md file
// (and index_files binding). Idempotent: a missing row/file is not an error, so
// it also cleans up an orphaned index row whose file was already removed.
func (e *Editor) DeletePerson(ctx context.Context, id string) error {
	if e.ser == nil {
		return errors.New("brain: editor has no serializer (brain disabled)")
	}
	if err := e.store.SoftDeletePerson(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("brain editor: soft-delete person: %w", err)
	}
	if err := e.ser.DeletePerson(ctx, id); err != nil {
		return fmt.Errorf("brain editor: remove person file: %w", err)
	}
	return nil
}

// validatePersonRow runs the person invariants against the projection,
// skipping the filename-stem check (the GUI/agent never names files).
func validatePersonRow(p *store.PersonEntry) error {
	fm := PersonFrontmatter{
		ID:        p.ID,
		Workspace: normalizePersonWorkspace(p.WorkspaceID),
		Name:      p.Name,
	}
	return ValidatePerson(fm, "")
}
