package brain

import (
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// SerializeMemory maps a store.MemoryEntry plus its entity links into a
// memory Markdown document. The content is the body; bi-temporal fields
// are emitted only for kind=fact.
func SerializeMemory(m *store.MemoryEntry, entities []store.MemoryEntityRow) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("brain: SerializeMemory: nil memory")
	}
	fm, err := memoryToFrontmatter(m, entities)
	if err != nil {
		return nil, err
	}
	return marshalDoc(fm, m.Content)
}

// memoryToFrontmatter builds the on-disk frontmatter struct from a DB row.
func memoryToFrontmatter(m *store.MemoryEntry, entities []store.MemoryEntityRow) (MemoryFrontmatter, error) {
	fm := MemoryFrontmatter{
		ID:        m.ID,
		Schema:    SchemaMemoryV1,
		Kind:      m.Kind,
		Name:      m.Name,
		Pinned:    m.Pinned,
		CreatedAt: m.CreatedAt.UTC(),
		UpdatedAt: m.UpdatedAt.UTC(),
	}
	if m.WorkspaceID != nil {
		fm.Workspace = *m.WorkspaceID
	}

	tags, err := decodeStringSlice(m.TagsJSON)
	if err != nil {
		return MemoryFrontmatter{}, fmt.Errorf("brain: decode memory tags: %w", err)
	}
	fm.Tags = tags

	if s := (SourceFM{Kind: m.SourceKind, SessionID: m.SourceSessionID, ToolCallID: m.SourceToolCallID}); !s.IsZero() {
		fm.Source = &s
	}

	if len(entities) > 0 {
		links := make([]EntityLinkFM, 0, len(entities))
		for _, e := range entities {
			links = append(links, EntityLinkFM{Kind: e.EntityKind, ID: e.EntityID, Role: e.Role})
		}
		fm.Entities = links
	}

	// Bi-temporal fields only for facts (SPEC §5).
	if m.Kind == MemoryKindFact {
		start := m.TValidStart.UTC()
		fm.TValidStart = &start
		if m.TValidEnd != nil {
			end := m.TValidEnd.UTC()
			fm.TValidEnd = &end
		}
		fm.InvalidatedBy = m.InvalidatedBy
	}
	return fm, nil
}
