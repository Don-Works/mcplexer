package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/don-works/mcplexer/internal/store"
)

// notesHeading is the body section under which task notes are appended
// (Appendix B decision #4: notes inline in the task body for M1).
const notesHeading = "## Notes"

// frontmatterDelim is the YAML frontmatter fence.
const frontmatterDelim = "---"

// metaComposesKey is the meta-object key holding the child-task id list.
const metaComposesKey = "composes"

// SerializeTask maps a store.Task plus a prose body into a task
// Markdown document. The returned bytes are a YAML frontmatter block
// fenced by `---`, a blank line, then the body. Key order is
// deterministic (struct field order), so re-serializing identical input
// is byte-stable.
//
// body is the human-authored description; the task's status-history and
// structured fields live in frontmatter, not the body. (Inline notes are
// folded in by the caller in M1; M0 keeps body verbatim.)
func SerializeTask(t *store.Task, body string) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("brain: SerializeTask: nil task")
	}
	fm, err := taskToFrontmatter(t)
	if err != nil {
		return nil, err
	}
	return marshalDoc(fm, body)
}

// taskToFrontmatter builds the on-disk frontmatter struct from a DB row.
func taskToFrontmatter(t *store.Task) (TaskFrontmatter, error) {
	schema := SchemaTaskV1
	fm := TaskFrontmatter{
		ID:        t.ID,
		Schema:    schema,
		Workspace: t.WorkspaceID,
		Title:     t.Title,
		Status:    t.Status,
		Priority:  t.Priority,
		DueAt:     t.DueAt,
		Pinned:    t.Pinned,
		CreatedAt: t.CreatedAt.UTC(),
		UpdatedAt: t.UpdatedAt.UTC(),
	}

	tags, err := decodeStringSlice(t.TagsJSON)
	if err != nil {
		return TaskFrontmatter{}, fmt.Errorf("brain: decode task tags: %w", err)
	}
	fm.Tags = tags

	fm.Composes = metaComposesList(t.Meta)
	fm.Meta = metaWithoutComposes(t.Meta)

	if a := assigneeFromTask(t); a != nil {
		fm.Assignee = a
	}
	if s := sourceFromTask(t); s != nil {
		fm.Source = s
	}

	hist, err := statusHistoryFromTask(t)
	if err != nil {
		return TaskFrontmatter{}, err
	}
	fm.StatusHistory = hist
	return fm, nil
}

// assigneeFromTask lifts the flattened assignee columns into the nested
// frontmatter block, returning nil when no assignee information exists.
func assigneeFromTask(t *store.Task) *AssigneeFM {
	a := AssigneeFM{
		OriginKind: t.AssigneeOriginKind,
		SessionID:  t.AssigneeSessionID,
		PeerID:     t.AssigneePeerID,
	}
	if a.IsZero() {
		return nil
	}
	return &a
}

// sourceFromTask lifts the flattened source columns into the nested
// frontmatter block, returning nil when empty.
func sourceFromTask(t *store.Task) *SourceFM {
	s := SourceFM{
		Kind:       t.SourceKind,
		SessionID:  t.SourceSessionID,
		ToolCallID: t.SourceToolCallID,
	}
	if s.IsZero() {
		return nil
	}
	return &s
}

// statusHistoryFromTask decodes the append-only status-history JSON into
// the frontmatter event slice. nil/empty JSON yields a nil slice.
func statusHistoryFromTask(t *store.Task) ([]StatusEventFM, error) {
	if len(bytes.TrimSpace(t.StatusHistoryJSON)) == 0 {
		return nil, nil
	}
	var entries []store.TaskStatusHistoryEntry
	if err := json.Unmarshal(t.StatusHistoryJSON, &entries); err != nil {
		return nil, fmt.Errorf("brain: decode task status_history: %w", err)
	}
	out := make([]StatusEventFM, 0, len(entries))
	for _, e := range entries {
		out = append(out, StatusEventFM{
			At:        e.At.UTC(),
			Evt:       e.Evt,
			From:      e.From,
			To:        e.To,
			BySession: e.BySession,
			ByPeer:    e.ByPeer,
			Note:      e.Note,
		})
	}
	return out, nil
}

// SerializeWorkspace maps a store.Workspace into a workspace.md document.
// The body is empty (workspaces carry no prose in M0).
func SerializeWorkspace(w *store.Workspace) ([]byte, error) {
	if w == nil {
		return nil, fmt.Errorf("brain: SerializeWorkspace: nil workspace")
	}
	fm := WorkspaceFrontmatter{
		ID:            w.ID,
		Schema:        SchemaWorkspaceV1,
		Name:          w.Name,
		RootPath:      w.RootPath,
		Parent:        w.ParentID,
		DefaultPolicy: w.DefaultPolicy,
		Source:        w.Source,
		CreatedAt:     w.CreatedAt.UTC(),
		UpdatedAt:     w.UpdatedAt.UTC(),
	}
	tags, err := decodeStringSlice(w.Tags)
	if err != nil {
		return nil, fmt.Errorf("brain: decode workspace tags: %w", err)
	}
	fm.Tags = tags
	return marshalDoc(fm, "")
}

// marshalDoc renders a frontmatter struct + body into the canonical
// `--- yaml --- \n\n body` document. yaml.v3 encodes struct fields in
// declaration order, which is what makes the output deterministic.
func marshalDoc(fm any, body string) ([]byte, error) {
	var ybuf bytes.Buffer
	enc := yaml.NewEncoder(&ybuf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("brain: encode frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("brain: close encoder: %w", err)
	}

	var doc bytes.Buffer
	doc.WriteString(frontmatterDelim)
	doc.WriteByte('\n')
	doc.Write(ybuf.Bytes()) // yaml.Encoder output already ends with a newline.
	doc.WriteString(frontmatterDelim)
	doc.WriteByte('\n')

	trimmed := strings.TrimRight(body, "\n")
	if trimmed != "" {
		doc.WriteByte('\n')
		doc.WriteString(trimmed)
		doc.WriteByte('\n')
	}
	return doc.Bytes(), nil
}

// decodeStringSlice decodes a json.RawMessage holding a JSON array of
// strings (the DB's tags_json shape) into a Go slice. nil/empty/"null"
// yields a nil slice (omitempty-friendly).
func decodeStringSlice(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// metaWithoutComposes decodes a task's meta JSON object and returns a
// copy with the composes key removed (it is promoted to its own
// frontmatter field). Returns nil for empty/legacy/non-object meta so
// the frontmatter field stays omitempty. This is what keeps the
// brain round-trip lossless for composed_by / rollup_to / work_context /
// worktree and any user-supplied meta keys.
func metaWithoutComposes(meta string) map[string]any {
	trimmed := strings.TrimSpace(meta)
	if trimmed == "" || trimmed[0] != '{' {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return nil
	}
	delete(obj, metaComposesKey)
	if len(obj) == 0 {
		return nil
	}
	return obj
}

// metaComposesList extracts the child-task id list from a task's meta
// JSON object. It tolerates the legacy/empty shapes by treating anything
// that isn't a well-formed JSON object as "no composes".
func metaComposesList(meta string) []string {
	trimmed := strings.TrimSpace(meta)
	if trimmed == "" || trimmed[0] != '{' {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return nil
	}
	raw, ok := obj[metaComposesKey]
	if !ok {
		return nil
	}
	// composes may be a scalar string (single child) or an array.
	var asSlice []string
	if err := json.Unmarshal(raw, &asSlice); err == nil {
		if len(asSlice) == 0 {
			return nil
		}
		return asSlice
	}
	var asScalar string
	if err := json.Unmarshal(raw, &asScalar); err == nil && asScalar != "" {
		return []string{asScalar}
	}
	return nil
}
