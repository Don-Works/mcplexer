// dashboard_activity_handler.go — rolling cross-workspace activity
// streams that back the dashboard's "what's going on" tiles.
//
// Routes:
//
//	GET /api/v1/dashboard/activity/tasks    → recent open + updated tasks
//	GET /api/v1/dashboard/activity/memories → recent memory writes
//
// Both endpoints are read-only aggregators on top of existing
// service layers. They collapse cross-workspace rows + project the
// shape the dashboard tiles need, so the frontend doesn't have to
// chase per-row enrichment (workspace name, last status_history
// event, etc.) across multiple requests.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// dashboardActivityHandler aggregates cross-workspace activity for the
// dashboard tiles. Optional deps: when a service is nil the matching
// route returns 503 so the SPA can degrade gracefully.
type dashboardActivityHandler struct {
	tasksSvc  *tasks.Service
	memorySvc *memory.Service
	wsStore   store.WorkspaceStore
}

// TaskActivityRow is the projected shape returned to the dashboard
// Tasks tile. Decoupled from store.Task on purpose — we control the
// payload size and the field set the tile actually needs.
type TaskActivityRow struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	WorkspaceName   string `json:"workspace_name"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Priority        string `json:"priority"`
	AssigneeDisplay string `json:"assignee_display"`
	UpdatedAt       string `json:"updated_at"`
	LastEvent       string `json:"last_event"`
}

type taskActivityResponse struct {
	Tasks []TaskActivityRow `json:"tasks"`
}

// handleTasksActivity serves GET /api/v1/dashboard/activity/tasks.
//
// Returns the most recently-updated cross-workspace tasks (limit ~20,
// hard cap 50), ordered by updated_at DESC. The session has implicit
// visibility into every local workspace — no scope filter for v1.
func (h *dashboardActivityHandler) handleTasksActivity(w http.ResponseWriter, r *http.Request) {
	if h.tasksSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "tasks service not wired")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 20, 50)

	rows, err := h.tasksSvc.List(r.Context(), store.TaskFilter{Limit: limit})
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list recent tasks", err.Error())
		return
	}

	resp := taskActivityResponse{Tasks: make([]TaskActivityRow, 0, len(rows))}
	wsNames := h.workspaceNameMap(r)
	for i := range rows {
		resp.Tasks = append(resp.Tasks, projectTaskActivity(&rows[i], wsNames))
	}
	writeJSON(w, http.StatusOK, resp)
}

// projectTaskActivity flattens a store.Task into the dashboard-tile
// shape. `last_event` is the evt of the most recent status_history
// entry — empty string when the row has no history yet.
func projectTaskActivity(t *store.Task, wsNames map[string]string) TaskActivityRow {
	row := TaskActivityRow{
		ID:              t.ID,
		WorkspaceID:     t.WorkspaceID,
		WorkspaceName:   wsNames[t.WorkspaceID],
		Title:           t.Title,
		Status:          t.Status,
		Priority:        t.Priority,
		AssigneeDisplay: taskAssigneeDisplay(t),
		UpdatedAt:       t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		LastEvent:       latestHistoryEvent(t.StatusHistoryJSON),
	}
	if row.WorkspaceName == "" {
		row.WorkspaceName = t.WorkspaceID
	}
	return row
}

// taskAssigneeDisplay collapses session/peer assignee fields into a
// single string for the tile. Empty string = unassigned.
func taskAssigneeDisplay(t *store.Task) string {
	if t.AssigneePeerID != "" {
		if t.AssigneeSessionID == "" {
			return "peer:" + t.AssigneePeerID
		}
		return "peer:" + t.AssigneePeerID + "/" + t.AssigneeSessionID
	}
	return t.AssigneeSessionID
}

// latestHistoryEvent decodes the last entry of status_history and
// returns its evt label. Best-effort — malformed history JSON returns
// empty so the tile renders a clean dash.
func latestHistoryEvent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var entries []store.TaskStatusHistoryEntry
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1].Evt
}

// workspaceNameMap returns id→name for every workspace, or nil on
// error (callers fall back to the raw id). Cheap query: the workspace
// table is tiny in practice.
func (h *dashboardActivityHandler) workspaceNameMap(r *http.Request) map[string]string {
	if h.wsStore == nil {
		return map[string]string{}
	}
	wss, err := h.wsStore.ListWorkspaces(r.Context())
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(wss))
	for _, ws := range wss {
		out[ws.ID] = ws.Name
	}
	return out
}

// parseLimit clamps a user-supplied limit query param to [1, max].
// Default is returned when the input is empty or unparseable.
func parseLimit(raw string, defaultN, max int) int {
	if raw == "" {
		return defaultN
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultN
	}
	if n > max {
		return max
	}
	return n
}

// MemoryActivityRow is the projected shape for the dashboard Memory
// tile. Decoupled from store.MemoryEntry — we surface a one-sentence
// summary + agent + workspace + scope so the tile reads "what was
// just learned" instead of a database dump.
type MemoryActivityRow struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Summary       string `json:"summary"`       // first sentence of content
	Body          string `json:"body"`          // full content for expand affordance
	AgentDisplay  string `json:"agent_display"` // session/worker/peer that saved it
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	ScopeLabel    string `json:"scope_label"` // "global" | "<workspace>"
	SourceKind    string `json:"source_kind"`
	CreatedAt     string `json:"created_at"`
	Pinned        bool   `json:"pinned,omitempty"`
}

type memoryActivityResponse struct {
	Memories []MemoryActivityRow `json:"memories"`
}

// handleMemoriesActivity serves GET /api/v1/dashboard/activity/memories.
//
// Returns recently-written memory entries, formatted for the "what was
// just learned" tile. Scope is admin-broad (IncludeAll) so the session
// sees everything it would see in the full /memory view — gating
// happens at the memory.Service layer, which already enforces the
// SkillScope check the user can read.
func (h *dashboardActivityHandler) handleMemoriesActivity(w http.ResponseWriter, r *http.Request) {
	if h.memorySvc == nil {
		writeError(w, http.StatusServiceUnavailable, "memory service not wired")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 8, 30)

	rows, err := h.memorySvc.List(r.Context(), store.MemoryFilter{
		Scope: store.SkillScope{IncludeAll: true},
		Limit: limit,
	})
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list recent memories", err.Error())
		return
	}

	wsNames := h.workspaceNameMap(r)
	resp := memoryActivityResponse{Memories: make([]MemoryActivityRow, 0, len(rows))}
	for i := range rows {
		resp.Memories = append(resp.Memories, projectMemoryActivity(&rows[i], wsNames))
	}
	writeJSON(w, http.StatusOK, resp)
}

// projectMemoryActivity flattens a store.MemoryEntry into the
// dashboard-tile shape. The "summary" is the first sentence of the
// content (cut at the first sentence-end punctuation OR at ~140 chars
// for runaway prose), which gives the tile its "what just got
// learned" feel.
func projectMemoryActivity(m *store.MemoryEntry, wsNames map[string]string) MemoryActivityRow {
	row := MemoryActivityRow{
		ID:           m.ID,
		Kind:         m.Kind,
		Name:         m.Name,
		Summary:      firstSentence(m.Content, 140),
		Body:         m.Content,
		AgentDisplay: memoryAgentDisplay(m),
		SourceKind:   m.SourceKind,
		CreatedAt:    m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Pinned:       m.Pinned,
	}
	if m.WorkspaceID != nil && *m.WorkspaceID != "" {
		row.WorkspaceID = *m.WorkspaceID
		row.WorkspaceName = wsNames[*m.WorkspaceID]
		if row.WorkspaceName == "" {
			row.WorkspaceName = *m.WorkspaceID
		}
		row.ScopeLabel = row.WorkspaceName
	} else {
		row.ScopeLabel = "global"
	}
	return row
}

// memoryAgentDisplay returns "worker:<id>" for worker writes,
// "peer:<short>" for mesh-arrived rows, "session:<short>" for
// human-or-agent writes, or the source_kind label as a fallback.
func memoryAgentDisplay(m *store.MemoryEntry) string {
	if m.WorkerID != "" {
		return "worker:" + m.WorkerID
	}
	if m.OriginPeerID != "" {
		return "peer:" + shortID(m.OriginPeerID)
	}
	if m.SourcePeerID != "" {
		return "peer:" + shortID(m.SourcePeerID)
	}
	if m.SourceSessionID != "" {
		return "session:" + shortID(m.SourceSessionID)
	}
	if m.SourceKind != "" {
		return m.SourceKind
	}
	return ""
}

// shortID trims an opaque ULID/UUID/libp2p-peer-id to the last 8
// characters so the tile's per-row "who wrote this" chip stays
// scannable. Idempotent on already-short ids.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// firstSentence returns the first sentence-like prefix of s, capped
// at maxLen runes. A sentence boundary is `. `, `! `, `? `, or the
// hard cap. Trailing whitespace is trimmed.
func firstSentence(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip leading markdown heading markers + bullets so the tile
	// doesn't render `# ` clutter. Best-effort, not exhaustive.
	s = strings.TrimLeft(s, "#-*> \t")
	// Find first sentence terminator followed by whitespace.
	end := -1
	for i := 0; i < len(s)-1; i++ {
		c := s[i]
		if c == '.' || c == '!' || c == '?' {
			next := s[i+1]
			if next == ' ' || next == '\n' || next == '\t' {
				end = i + 1
				break
			}
		}
	}
	if end > 0 && end <= maxLen {
		return strings.TrimSpace(s[:end])
	}
	// No sentence end inside the cap — hard-truncate with an ellipsis
	// so the tile doesn't show a chopped mid-word.
	if len(s) > maxLen {
		// Trim back to the last space inside the cap so we don't cut
		// mid-word. Falls back to a hard slice when no space exists.
		cut := maxLen
		if idx := strings.LastIndex(s[:maxLen], " "); idx > maxLen/2 {
			cut = idx
		}
		return strings.TrimSpace(s[:cut]) + "…"
	}
	return s
}
