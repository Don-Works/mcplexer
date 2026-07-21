package brain

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// This file holds the Editor's read-only browse + detail + search surface:
// the client/workspace hierarchy, the scope-fusion string, per-record detail
// (frontmatter + body + raw .md + on-disk hash), and the single frecency
// ranked intellisense search powering cmd+K and every in-field typeahead.
// All reads come from the derived SQLite index — the file tree is never
// walked here.

// ClientNode is one client/org tier (a parent workspace, SPEC App. C.1).
type ClientNode struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	WorkspaceCt int    `json:"workspace_count"`
}

// WorkspaceNode is a child workspace under a client, carrying its source
// (central|repo) + ancestor chain for the scope picker.
type WorkspaceNode struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	ParentID    string   `json:"parent_id,omitempty"`
	Source      string   `json:"source"` // central|repo
	Chain       []string `json:"chain"`  // self -> parent -> ... (scope ancestry)
	TaskCount   int      `json:"task_count"`
	MemoryCount int      `json:"memory_count"`
}

// Clients returns the client/org tier: workspaces that are a parent of at
// least one other workspace (the hierarchy's top tier). A flat install with
// no parents yields an empty list and the GUI shows a single workspace
// column.
func (e *Editor) Clients(ctx context.Context) ([]ClientNode, error) {
	wss, err := e.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	childCt := make(map[string]int)
	byID := make(map[string]store.Workspace, len(wss))
	for i := range wss {
		byID[wss[i].ID] = wss[i]
		if p := strings.TrimSpace(wss[i].ParentID); p != "" {
			childCt[p]++
		}
	}
	out := make([]ClientNode, 0)
	for id, n := range childCt {
		w := byID[id]
		out = append(out, ClientNode{ID: id, DisplayName: displayName(w), WorkspaceCt: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Workspaces returns the child workspaces of a client (or every workspace
// when client is empty), each enriched with its index source + ancestor
// chain + live counts for the scope picker.
func (e *Editor) Workspaces(ctx context.Context, client string) ([]WorkspaceNode, error) {
	wss, err := e.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.Workspace, len(wss))
	for i := range wss {
		byID[wss[i].ID] = wss[i]
	}
	client = strings.TrimSpace(client)
	out := make([]WorkspaceNode, 0, len(wss))
	for i := range wss {
		w := wss[i]
		if client != "" && w.ParentID != client {
			continue
		}
		node := WorkspaceNode{
			ID:          w.ID,
			DisplayName: displayName(w),
			ParentID:    w.ParentID,
			Source:      e.workspaceSource(ctx, w.ID),
			Chain:       chainFor(w.ID, byID),
		}
		if tasks, terr := e.store.ListTasks(ctx, store.TaskFilter{WorkspaceID: w.ID, Limit: 10000}); terr == nil {
			node.TaskCount = len(tasks)
		}
		if mems, merr := e.store.ListMemories(ctx, store.MemoryFilter{
			Scope: store.SkillScope{WorkspaceIDs: []string{w.ID}}, Limit: 10000,
		}); merr == nil {
			node.MemoryCount = countForWorkspace(mems, w.ID)
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Scope returns the agent's literal scope-fusion string for a workspace,
// e.g. "acme-api ∪ acme ∪ global" — the workspace, its ancestor chain, then
// global. This is the verbatim POV an agent recalls under, shown as the
// browser's mono footer line.
func (e *Editor) Scope(ctx context.Context, workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || workspace == "global" {
		return "global", nil
	}
	wss, err := e.store.ListWorkspaces(ctx)
	if err != nil {
		return "", err
	}
	byID := make(map[string]store.Workspace, len(wss))
	for i := range wss {
		byID[wss[i].ID] = wss[i]
	}
	parts := chainFor(workspace, byID)
	parts = append(parts, "global")
	return strings.Join(parts, " ∪ "), nil
}

// chainFor builds the workspace's scope ancestry: self, parent, grandparent
// (cycle-guarded). global is appended by the caller.
func chainFor(ws string, byID map[string]store.Workspace) []string {
	var chain []string
	seen := make(map[string]struct{})
	for cur := ws; cur != ""; {
		if _, dup := seen[cur]; dup {
			break
		}
		seen[cur] = struct{}{}
		chain = append(chain, cur)
		cur = byID[cur].ParentID
	}
	return chain
}

// workspaceSource reports whether a workspace's canonical brain is a
// repo-local .mcplexer/ ("repo") or the central tree ("central"), read from
// the index_files source column (any repo-sourced row wins).
func (e *Editor) workspaceSource(ctx context.Context, ws string) string {
	if e.ser == nil {
		return store.IndexSourceCentral
	}
	files, err := e.store.ListIndexFiles(ctx, ws)
	if err != nil {
		return store.IndexSourceCentral
	}
	for i := range files {
		if files[i].Source == store.IndexSourceRepo {
			return store.IndexSourceRepo
		}
	}
	return store.IndexSourceCentral
}

// GetTaskDetail returns one task enriched with its verbatim on-disk .md
// (the FileTruthDisclosure) on top of the browse projection.
func (e *Editor) GetTaskDetail(ctx context.Context, id string) (*TaskRecord, error) {
	rec, err := e.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.ser != nil && rec.Path != "" {
		rec.Raw, _ = e.ser.ReadRaw(rec.Path)
	}
	return rec, nil
}

// GetMemoryDetail is the memory counterpart of GetTaskDetail.
func (e *Editor) GetMemoryDetail(ctx context.Context, id string) (*MemoryRecord, error) {
	rec, err := e.GetMemory(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.ser != nil && rec.Path != "" {
		rec.Raw, _ = e.ser.ReadRaw(rec.Path)
	}
	return rec, nil
}

// enrichRecordIndex fills the index-derived fields (canonical path, source,
// last-indexed CAS sha, standing validation error/field) for a record. Best
// effort: a missing serializer or index row leaves the pointers untouched.
func (e *Editor) enrichRecordIndex(ctx context.Context, kind, id string, path, source, hash, vErr, vField *string) {
	if e.ser == nil {
		return
	}
	p, sha, src := e.ser.IndexedSha(ctx, kind, id)
	if p != "" {
		*path = p
		*hash = sha
		if src != "" {
			*source = src
		}
	}
	// Surface a standing validation error keyed to this record's path so the
	// browser can flag "your agent cannot see this record yet".
	if p == "" {
		return
	}
	errs, err := e.store.ListBrainErrors(ctx)
	if err != nil {
		return
	}
	for _, be := range errs {
		if be.Path == p && be.Field != "_file" {
			*vErr = be.Reason
			*vField = be.Field
			return
		}
	}
}

// taskHoldsLease reports whether a task currently holds an unexpired lease —
// an agent is touching it right now (the browser's shimmer row). A task with
// an assignee and a future lease_expires_at is live; pre-lease rows (nil
// LeaseExpiresAt) are never live (their staleness proxy is updated_at, not a
// lease).
func taskHoldsLease(t *store.Task, now time.Time) bool {
	if t.LeaseExpiresAt == nil {
		return false
	}
	if t.AssigneeSessionID == "" && t.AssigneePeerID == "" {
		return false
	}
	return t.LeaseExpiresAt.After(now)
}

// taskComposes extracts the promoted composes child-id list from a task's
// meta JSON (reusing the serialize-side extractor), returning nil when none.
func taskComposes(t *store.Task) []string {
	return metaComposesList(t.Meta)
}

// SuppressCandidate records the sticky per-record "never suggest this memory
// candidate again" decision (DESIGN §3.5). A blank contentHash suppresses ALL
// candidates for the record.
func (e *Editor) SuppressCandidate(ctx context.Context, recordID, contentHash string) error {
	return e.store.SuppressCandidate(ctx, recordID, contentHash)
}
