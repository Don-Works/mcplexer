package brain

import (
	"context"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TreeNode is one node in the client -> workspace -> entity-kind browser
// tree the GUI renders down the left rail. Counts are best-effort live
// totals from the index so the tree shows "Tasks (12)" without a second
// round-trip.
type TreeNode struct {
	Workspace   string `json:"workspace"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
	TaskCount   int    `json:"task_count"`
	MemoryCount int    `json:"memory_count"`
}

// Tree returns the workspace browser tree. Each workspace surfaces its
// parent (the client/org tier from M6) plus live task/memory counts so the
// non-technical browser can render a grouped, counted navigation rail.
func (e *Editor) Tree(ctx context.Context) ([]TreeNode, error) {
	wss, err := e.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TreeNode, 0, len(wss))
	for i := range wss {
		w := wss[i]
		node := TreeNode{
			Workspace:   w.ID,
			ParentID:    w.ParentID,
			DisplayName: displayName(w),
		}
		if tasks, terr := e.store.ListTasks(ctx, store.TaskFilter{WorkspaceID: w.ID, Limit: 10000}); terr == nil {
			node.TaskCount = len(tasks)
		}
		if mems, merr := e.store.ListMemories(ctx, store.MemoryFilter{
			Scope: store.SkillScope{WorkspaceIDs: []string{w.ID}},
			Limit: 10000,
		}); merr == nil {
			node.MemoryCount = countForWorkspace(mems, w.ID)
		}
		out = append(out, node)
	}
	return out, nil
}

// displayName prefers the workspace's human Name, falling back to its id.
func displayName(w store.Workspace) string {
	if n := strings.TrimSpace(w.Name); n != "" {
		return n
	}
	return w.ID
}

// countForWorkspace counts only memories whose workspace_id equals ws (the
// scoped ListMemories also returns global rows; the tree count is the
// per-workspace subset so a global memory isn't tallied under every node).
func countForWorkspace(mems []store.MemoryEntry, ws string) int {
	n := 0
	for i := range mems {
		if mems[i].WorkspaceID != nil && *mems[i].WorkspaceID == ws {
			n++
		}
	}
	return n
}

// ListTasks returns the editable task records for one workspace, newest
// first (the store orders by updated_at DESC).
func (e *Editor) ListTasks(ctx context.Context, workspace string) ([]TaskRecord, error) {
	rows, err := e.store.ListTasks(ctx, store.TaskFilter{WorkspaceID: workspace, Limit: 1000})
	if err != nil {
		return nil, err
	}
	index := e.loadRecordIndexCache(ctx)
	out := make([]TaskRecord, 0, len(rows))
	for i := range rows {
		out = append(out, *e.taskRowToRecordValueWithIndex(ctx, &rows[i], index))
	}
	return out, nil
}

// ListMemories returns the editable memory records for one workspace. When
// workspace is "global" or empty, the global (nil-workspace) memories are
// returned.
func (e *Editor) ListMemories(ctx context.Context, workspace string) ([]MemoryRecord, error) {
	filter := store.MemoryFilter{Limit: 1000}
	wantGlobal := workspace == "" || workspace == "global"
	if !wantGlobal {
		filter.Scope = store.SkillScope{WorkspaceIDs: []string{workspace}}
	}
	rows, err := e.store.ListMemories(ctx, filter)
	if err != nil {
		return nil, err
	}
	index := e.loadRecordIndexCache(ctx)
	out := make([]MemoryRecord, 0, len(rows))
	for i := range rows {
		m := &rows[i]
		isGlobal := m.WorkspaceID == nil
		if wantGlobal != isGlobal {
			continue
		}
		if !wantGlobal && *m.WorkspaceID != workspace {
			continue
		}
		out = append(out, *e.memoryRowToRecordValue(ctx, m, index))
	}
	return out, nil
}

// GetTask returns one editable task record.
func (e *Editor) GetTask(ctx context.Context, id string) (*TaskRecord, error) {
	t, err := e.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return e.taskRowToRecordValue(ctx, t), nil
}

// GetMemory returns one editable memory record.
func (e *Editor) GetMemory(ctx context.Context, id string) (*MemoryRecord, error) {
	rec := e.memoryRowToRecord(ctx, id)
	if rec == nil {
		return nil, store.ErrNotFound
	}
	return rec, nil
}

// taskRowToRecordValue projects a store.Task into the GUI record shape,
// decoding tags and enriching it with the canonical on-disk path, provenance
// (source/assignee/composes), the live-lease flag, the last-indexed CAS sha,
// and any standing validation error (so the browser can flag a record the
// agent cannot see). The detail read additionally fills Raw.
func (e *Editor) taskRowToRecordValue(ctx context.Context, t *store.Task) *TaskRecord {
	return e.taskRowToRecordValueWithIndex(ctx, t, nil)
}

func (e *Editor) taskRowToRecordValueWithIndex(ctx context.Context, t *store.Task, index *recordIndexCache) *TaskRecord {
	tags, _ := decodeStringSlice(t.TagsJSON)
	if tags == nil {
		tags = []string{}
	}
	rec := &TaskRecord{
		ID:          t.ID,
		Workspace:   t.WorkspaceID,
		Title:       t.Title,
		Status:      t.Status,
		Priority:    t.Priority,
		Tags:        tags,
		DueAt:       t.DueAt,
		Pinned:      t.Pinned,
		Description: t.Description,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		LiveLease:   taskHoldsLease(t, time.Now()),
		Composes:    taskComposes(t),
	}
	if sk := strings.TrimSpace(t.SourceKind); sk != "" {
		rec.Source = &SourceRecord{Kind: sk, SessionID: t.SourceSessionID}
	}
	if t.AssigneeOriginKind != "" || t.AssigneeSessionID != "" || t.AssigneePeerID != "" {
		rec.Assignee = &AssigneeRecord{
			OriginKind: t.AssigneeOriginKind,
			SessionID:  t.AssigneeSessionID,
			PeerID:     t.AssigneePeerID,
		}
	}
	if index != nil {
		index.enrich(EntityKindTask, t.ID, &rec.Path, &rec.IndexSource, &rec.OnDiskHash, &rec.ValidationError, &rec.ValidationField)
	} else {
		e.enrichRecordIndex(ctx, EntityKindTask, t.ID, &rec.Path, &rec.IndexSource, &rec.OnDiskHash, &rec.ValidationError, &rec.ValidationField)
	}
	return rec
}

// memoryRowToRecord loads a memory + its entity links and projects them
// into the GUI record shape. Returns nil when the memory is absent.
func (e *Editor) memoryRowToRecord(ctx context.Context, id string) *MemoryRecord {
	m, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return nil
	}
	return e.memoryRowToRecordValue(ctx, m, nil)
}

func (e *Editor) memoryRowToRecordValue(ctx context.Context, m *store.MemoryEntry, index *recordIndexCache) *MemoryRecord {
	tags, _ := decodeStringSlice(m.TagsJSON)
	if tags == nil {
		tags = []string{}
	}
	rec := &MemoryRecord{
		ID:        m.ID,
		Kind:      m.Kind,
		Name:      m.Name,
		Tags:      tags,
		Pinned:    m.Pinned,
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	if sk := strings.TrimSpace(m.SourceKind); sk != "" {
		rec.Source = &SourceRecord{Kind: sk, SessionID: m.SourceSessionID}
	}
	if m.WorkspaceID != nil {
		rec.Workspace = *m.WorkspaceID
	}
	if m.Kind == MemoryKindFact && !m.TValidStart.IsZero() {
		ts := m.TValidStart
		rec.TValidStart = &ts
	}
	if links, lerr := e.store.ListMemoryEntities(ctx, m.ID); lerr == nil {
		for _, l := range links {
			rec.Entities = append(rec.Entities, EntityLinkFM{Kind: l.EntityKind, ID: l.EntityID, Role: l.Role})
		}
	}
	if index != nil {
		index.enrich(EntityKindMemory, m.ID, &rec.Path, &rec.IndexSource, &rec.OnDiskHash, &rec.ValidationError, &rec.ValidationField)
	} else {
		e.enrichRecordIndex(ctx, EntityKindMemory, m.ID, &rec.Path, &rec.IndexSource, &rec.OnDiskHash, &rec.ValidationError, &rec.ValidationField)
	}
	return rec
}

// validateTaskRow runs the same Astro/Zod-style checks the indexer applies,
// so a GUI save can never produce a record the indexer would reject. It
// validates the FRONTMATTER projection (id/title/status/vocab) without the
// filename-prefix check (the GUI never names files).
func validateTaskRow(t *store.Task, vocab []string) error {
	fm := TaskFrontmatter{ID: t.ID, Title: t.Title, Status: t.Status, Workspace: t.WorkspaceID}
	// filename "" skips the id==filename-prefix check (the GUI owns no path).
	return ValidateTask(fm, "", vocab)
}

// validateMemoryRow runs the memory invariants against the GUI projection,
// skipping the filename-stem check (the GUI never names files).
func validateMemoryRow(m *store.MemoryEntry) error {
	fm := MemoryFrontmatter{ID: m.ID, Name: m.Name, Kind: m.Kind}
	if m.Kind == MemoryKindFact && !m.TValidStart.IsZero() {
		ts := m.TValidStart
		fm.TValidStart = &ts
	}
	return ValidateMemory(fm, "")
}
