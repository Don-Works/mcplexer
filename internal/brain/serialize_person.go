package brain

import (
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// SerializePerson maps a store.PersonEntry plus its entity links into a CRM
// person Markdown document. The notes are the body; the structured fields
// live in frontmatter. Key order is deterministic (struct field order), so
// re-serializing identical input is byte-stable.
func SerializePerson(p *store.PersonEntry, entities []store.PersonEntityRow) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("brain: SerializePerson: nil person")
	}
	fm, err := personToFrontmatter(p, entities)
	if err != nil {
		return nil, err
	}
	return marshalDoc(fm, p.Notes)
}

// personToFrontmatter builds the on-disk frontmatter struct from a DB row.
func personToFrontmatter(p *store.PersonEntry, entities []store.PersonEntityRow) (PersonFrontmatter, error) {
	fm := PersonFrontmatter{
		ID:        p.ID,
		Schema:    SchemaPersonV1,
		Workspace: normalizePersonWorkspace(p.WorkspaceID),
		Name:      p.Name,
		Email:     p.Email,
		Phone:     p.Phone,
		Company:   p.Company,
		Role:      p.Role,
		Pinned:    p.Pinned,
		CreatedAt: p.CreatedAt.UTC(),
		UpdatedAt: p.UpdatedAt.UTC(),
	}
	tags, err := decodeStringSlice(p.TagsJSON)
	if err != nil {
		return PersonFrontmatter{}, fmt.Errorf("brain: decode person tags: %w", err)
	}
	fm.Tags = tags

	if s := (SourceFM{Kind: p.SourceKind, SessionID: p.SourceSessionID, ToolCallID: p.SourceToolCallID}); !s.IsZero() {
		fm.Source = &s
	}

	if len(entities) > 0 {
		links := make([]EntityLinkFM, 0, len(entities))
		for _, e := range entities {
			links = append(links, EntityLinkFM{Kind: e.EntityKind, ID: e.EntityID, Role: e.Role})
		}
		fm.Entities = links
	}
	return fm, nil
}

// ToPerson is the inbound inverse of SerializePerson: it builds a
// store.PersonEntry + entity refs from a parsed frontmatter struct + notes
// body. Consumed by the indexer; the trailing newline is trimmed off the
// notes so a re-serialize is byte-stable.
func (fm PersonFrontmatter) ToPerson(body string) (*store.PersonEntry, []store.EntityRef, error) {
	p := &store.PersonEntry{
		ID:          fm.ID,
		WorkspaceID: normalizePersonWorkspace(fm.Workspace),
		Name:        fm.Name,
		Email:       fm.Email,
		Phone:       fm.Phone,
		Company:     fm.Company,
		Role:        fm.Role,
		Notes:       strings.TrimRight(body, "\n"),
		Pinned:      fm.Pinned,
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
	}
	tags, err := encodeStringSlice(fm.Tags)
	if err != nil {
		return nil, nil, fmt.Errorf("brain: encode person tags: %w", err)
	}
	p.TagsJSON = tags

	if fm.Source != nil {
		p.SourceKind = fm.Source.Kind
		p.SourceSessionID = fm.Source.SessionID
		p.SourceToolCallID = fm.Source.ToolCallID
	}

	var refs []store.EntityRef
	if len(fm.Entities) > 0 {
		refs = make([]store.EntityRef, 0, len(fm.Entities))
		for _, e := range fm.Entities {
			refs = append(refs, store.EntityRef{Kind: e.Kind, ID: e.ID, Role: e.Role})
		}
	}
	return p, refs, nil
}
