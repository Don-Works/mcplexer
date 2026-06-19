package brain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ToTask is the inbound inverse of SerializeTask: it builds a store.Task
// (the DB-bound model) from a parsed task frontmatter struct and prose
// body. Consumed by the indexer (M1); defined here in M0 so round-trip
// tests cover both directions.
//
// The trailing `## Notes` section is split off the body (its canonical
// home is the append-only task_notes rows, re-rendered on serialize); only
// the prose description is stored on the row, so a re-serialize re-attaches
// the notes section and the round-trip is byte-stable.
func (fm TaskFrontmatter) ToTask(body string) (*store.Task, error) {
	description, _ := SplitBodyNotes(body)
	t := &store.Task{
		ID:          fm.ID,
		WorkspaceID: fm.Workspace,
		Title:       fm.Title,
		Description: description,
		Status:      fm.Status,
		Priority:    fm.Priority,
		DueAt:       fm.DueAt,
		Pinned:      fm.Pinned,
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
	}

	tags, err := encodeStringSlice(fm.Tags)
	if err != nil {
		return nil, fmt.Errorf("brain: encode task tags: %w", err)
	}
	t.TagsJSON = tags

	if fm.Assignee != nil {
		t.AssigneeOriginKind = fm.Assignee.OriginKind
		t.AssigneeSessionID = fm.Assignee.SessionID
		t.AssigneePeerID = fm.Assignee.PeerID
	}
	if fm.Source != nil {
		t.SourceKind = fm.Source.Kind
		t.SourceSessionID = fm.Source.SessionID
		t.SourceToolCallID = fm.Source.ToolCallID
	}

	meta, err := composesToMeta(fm.Composes, fm.Meta)
	if err != nil {
		return nil, fmt.Errorf("brain: encode task meta: %w", err)
	}
	t.Meta = meta

	hist, err := statusHistoryToJSON(fm.StatusHistory)
	if err != nil {
		return nil, fmt.Errorf("brain: encode task status_history: %w", err)
	}
	t.StatusHistoryJSON = hist
	return t, nil
}

// statusHistoryToJSON re-encodes the frontmatter status-history slice
// into the DB's append-only JSON column. An empty slice yields nil.
func statusHistoryToJSON(events []StatusEventFM) (json.RawMessage, error) {
	if len(events) == 0 {
		return nil, nil
	}
	entries := make([]store.TaskStatusHistoryEntry, 0, len(events))
	for _, e := range events {
		entries = append(entries, store.TaskStatusHistoryEntry{
			At:        e.At,
			BySession: e.BySession,
			ByPeer:    e.ByPeer,
			Evt:       e.Evt,
			From:      e.From,
			To:        e.To,
			Note:      e.Note,
		})
	}
	return json.Marshal(entries)
}

// composesToMeta rebuilds the task meta JSON object from the preserved
// non-composes meta map plus the promoted composes id list. The result
// is emitted with sorted keys (matching tasks.encodeMetaJSON) so the DB
// meta column is byte-stable across a brain round-trip. An empty map +
// empty composes yields "" (the "no metadata" sentinel).
func composesToMeta(composes []string, preserved map[string]any) (string, error) {
	obj := make(map[string]any, len(preserved)+1)
	for k, v := range preserved {
		if k == metaComposesKey {
			continue // composes is owned by its own field, never duplicated
		}
		obj[k] = v
	}
	if len(composes) > 0 {
		// Match the tasks-service shape: single child = scalar, many = array.
		if len(composes) == 1 {
			obj[metaComposesKey] = composes[0]
		} else {
			obj[metaComposesKey] = composes
		}
	}
	if len(obj) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		kj, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		vj, err := json.Marshal(obj[k])
		if err != nil {
			return "", err
		}
		pairs = append(pairs, string(kj)+":"+string(vj))
	}
	return "{" + strings.Join(pairs, ",") + "}", nil
}

// ToMemory is the inbound inverse of SerializeMemory.
func (fm MemoryFrontmatter) ToMemory(body string) (*store.MemoryEntry, []store.EntityRef, error) {
	m := &store.MemoryEntry{
		ID:        fm.ID,
		Name:      fm.Name,
		Kind:      fm.Kind,
		Content:   strings.TrimRight(body, "\n"),
		Pinned:    fm.Pinned,
		CreatedAt: fm.CreatedAt,
		UpdatedAt: fm.UpdatedAt,
	}
	if ws := strings.TrimSpace(fm.Workspace); ws != "" && ws != "global" {
		w := ws
		m.WorkspaceID = &w
	}

	tags, err := encodeStringSlice(fm.Tags)
	if err != nil {
		return nil, nil, fmt.Errorf("brain: encode memory tags: %w", err)
	}
	m.TagsJSON = tags

	if fm.Source != nil {
		m.SourceKind = fm.Source.Kind
		m.SourceSessionID = fm.Source.SessionID
		m.SourceToolCallID = fm.Source.ToolCallID
	}

	if fm.Kind == MemoryKindFact && fm.TValidStart != nil {
		m.TValidStart = *fm.TValidStart
		m.TValidEnd = fm.TValidEnd
		m.InvalidatedBy = fm.InvalidatedBy
	}

	var refs []store.EntityRef
	if len(fm.Entities) > 0 {
		refs = make([]store.EntityRef, 0, len(fm.Entities))
		for _, e := range fm.Entities {
			refs = append(refs, store.EntityRef{Kind: e.Kind, ID: e.ID, Role: e.Role})
		}
	}
	return m, refs, nil
}

// ToWorkspace is the inbound inverse of SerializeWorkspace.
func (fm WorkspaceFrontmatter) ToWorkspace() (*store.Workspace, error) {
	w := &store.Workspace{
		ID:            fm.ID,
		Name:          fm.Name,
		RootPath:      fm.RootPath,
		ParentID:      fm.Parent,
		DefaultPolicy: fm.DefaultPolicy,
		Source:        fm.Source,
		CreatedAt:     fm.CreatedAt,
		UpdatedAt:     fm.UpdatedAt,
	}
	tags, err := encodeStringSlice(fm.Tags)
	if err != nil {
		return nil, fmt.Errorf("brain: encode workspace tags: %w", err)
	}
	w.Tags = tags
	return w, nil
}

// encodeStringSlice marshals a string slice into a json.RawMessage. An
// empty/nil slice yields nil (so the column stays NULL/empty), matching
// the omitempty serialize side.
func encodeStringSlice(s []string) (json.RawMessage, error) {
	if len(s) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
