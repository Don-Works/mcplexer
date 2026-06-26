// tasks_handler.go — REST surface for the tasks subsystem (migration
// 061). The dashboard uses this to list / read / write / update /
// claim / delete tasks and manage the per-workspace status
// vocabulary. Mirrors the memory handler's shape.
//
// Routes:
//
//	GET    /api/v1/tasks                            → list (querystring filters)
//	GET    /api/v1/tasks/count                      → counts by status for one workspace
//	GET    /api/v1/tasks/statuses                   → distinct status counts for filter UI
//	POST   /api/v1/tasks                            → create
//	GET    /api/v1/tasks/{id}                       → fetch one
//	POST   /api/v1/tasks/{id}/update                → patch (single-row form of task__update)
//	POST   /api/v1/tasks/{id}/claim                 → atomic assign-to-session + status flip
//	POST   /api/v1/tasks/{id}/heartbeat             → bump lease window (assignee only)
//	POST   /api/v1/tasks/{id}/notes                 → append note
//	GET    /api/v1/tasks/{id}/notes                 → list notes for one task
//	GET    /api/v1/tasks/{id}/history               → full edit/action history
//	POST   /api/v1/tasks/{id}/rollback              → restore to a history revision
//	DELETE /api/v1/tasks/{id}                       → soft-delete
//	GET    /api/v1/task-status-vocabulary           → list per-workspace status vocab
//	POST   /api/v1/task-status-vocabulary           → upsert one vocab entry
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

type tasksHandler struct {
	svc   *tasks.Service
	store store.TaskStore
}

type taskStatusCounter interface {
	CountTaskStatuses(ctx context.Context, workspaceID, state string) (map[string]int, error)
}

func newTasksHandler(svc *tasks.Service, s store.TaskStore) *tasksHandler {
	return &tasksHandler{svc: svc, store: s}
}

// GET /api/v1/tasks?workspace_id=&status=&state=&tag=&assignee=&limit=&offset=&q=
func (h *tasksHandler) handleList(w http.ResponseWriter, r *http.Request) {
	f, q := parseTaskFilter(r)
	var rows []store.Task
	var err error
	if strings.TrimSpace(q) != "" {
		rows, err = h.svc.Search(r.Context(), f, q)
	} else {
		rows, err = h.svc.List(r.Context(), f)
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.Task{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// GET /api/v1/tasks/milestones?workspace_id=
//
// Returns one MilestoneBurndown per milestone-tagged epic (tag includes
// "milestone" + due_at set) with children rollup and burndown series.
// Ordered by due_at ASC.
func (h *tasksHandler) handleListMilestones(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	rows, err := h.store.ListMilestonesWithBurndown(r.Context(), wsID)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list milestones failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.MilestoneBurndown{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// GET /api/v1/tasks/count?workspace_id=
func (h *tasksHandler) handleCount(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	counts, err := h.svc.CountByStatus(r.Context(), wsID)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "count failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

type taskStatusCountResponseRow struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// GET /api/v1/tasks/statuses?workspace_id=&state=open|closed|all
func (h *tasksHandler) handleListStatuses(w http.ResponseWriter, r *http.Request) {
	counter, ok := h.store.(taskStatusCounter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "task status counts are not supported by this store")
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		state = "open"
	}
	counts, err := counter.CountTaskStatuses(
		r.Context(),
		r.URL.Query().Get("workspace_id"),
		state,
	)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "status count failed", err.Error())
		return
	}
	rows := make([]taskStatusCountResponseRow, 0, len(counts))
	for status, count := range counts {
		rows = append(rows, taskStatusCountResponseRow{Status: status, Count: count})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Status < rows[j].Status
	})
	writeJSON(w, http.StatusOK, map[string]any{"statuses": rows})
}

// POST /api/v1/tasks
//
// `assignee.user_id` is the human-assignee field added with migration 105;
// setting it alone (with session_id/peer_id empty) routes the task to a
// human user. The server stores it as assignee_user_id with
// assignee_origin_kind=human.
type createTaskRequest struct {
	WorkspaceID string     `json:"workspace_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	Tags        []string   `json:"tags"`
	Meta        string     `json:"meta"`
	ComposeInto string     `json:"compose_into"`
	Assignee    *struct {
		SessionID string `json:"session_id"`
		PeerID    string `json:"peer_id"`
		UserID    string `json:"user_id"`
	} `json:"assignee,omitempty"`
}

func (h *tasksHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body createTaskRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if strings.TrimSpace(body.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	opts := tasks.CreateOptions{
		WorkspaceID: body.WorkspaceID,
		Title:       body.Title,
		Description: body.Description,
		Status:      body.Status,
		Priority:    body.Priority,
		DueAt:       body.DueAt,
		Tags:        body.Tags,
		Meta:        body.Meta,
		ComposeInto: body.ComposeInto,
		SourceKind:  store.TaskSourceUser,
		// Phase 2 plumbing: dashboard / REST callers are "user" — the
		// notify-suppression gate treats them like a human at the keyboard.
		ActorKind: "user",
	}
	if body.Assignee != nil {
		opts.Assignee = &tasks.Assignee{
			SessionID: body.Assignee.SessionID,
			PeerID:    body.Assignee.PeerID,
			UserID:    body.Assignee.UserID,
		}
	}
	t, err := h.svc.Create(r.Context(), opts)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "create failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// GET /api/v1/tasks/{id}?workspace_id=
//
// workspace_id is required so the dashboard can't accidentally cross
// workspace boundaries via URL tampering. Cross-workspace ids are
// reported as 404 (not 403) — matches the service's no-existence-leak
// semantics.
func (h *tasksHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	t, err := h.svc.Get(r.Context(), wsID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "fetch failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// POST /api/v1/tasks/{id}/update
//
// `assignee` follows the same shape as the create body and lets the
// dashboard re-assign a task to a human user (user_id) / local session
// (session_id) / peer (peer_id) in one call. Sending `{"user_id":"…"}`
// is the supported way to set a human assignee through the REST surface;
// `clear: ["assignee"]` clears any of those and resets
// assignee_origin_kind to "local".
type updateTaskRequest struct {
	Title       *string    `json:"title,omitempty"`
	Description *string    `json:"description,omitempty"`
	Status      *string    `json:"status,omitempty"`
	Priority    *string    `json:"priority,omitempty"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	Tags        *[]string  `json:"tags,omitempty"`
	Meta        *string    `json:"meta,omitempty"`
	Terminal    *bool      `json:"terminal,omitempty"`
	Pinned      *bool      `json:"pinned,omitempty"`
	Clear       []string   `json:"clear,omitempty"`
	Assignee    *struct {
		SessionID string `json:"session_id"`
		PeerID    string `json:"peer_id"`
		UserID    string `json:"user_id"`
	} `json:"assignee,omitempty"`
}

func (h *tasksHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var body updateTaskRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	patch := tasks.UpdatePatch{
		Title: body.Title, Description: body.Description, Status: body.Status,
		Priority: body.Priority, DueAt: body.DueAt, Tags: body.Tags,
		Meta: body.Meta, Terminal: body.Terminal, Pinned: body.Pinned,
		Clear:     body.Clear,
		ActorKind: "user",
	}
	if body.Assignee != nil {
		patch.Assignee = &tasks.Assignee{
			SessionID: body.Assignee.SessionID,
			PeerID:    body.Assignee.PeerID,
			UserID:    body.Assignee.UserID,
		}
	}
	t, err := h.svc.Update(r.Context(), wsID, id, patch)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "update failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// POST /api/v1/tasks/{id}/claim
type claimTaskRequest struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Note      string `json:"note"`
}

func (h *tasksHandler) handleClaim(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var body claimTaskRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	t, err := h.svc.Claim(r.Context(), wsID, id, body.Status, body.SessionID, body.Note, tasks.MutationContext{
		ActorKind: "user",
		SessionID: body.SessionID,
	})
	if errors.Is(err, tasks.ErrTaskAlreadyClaimed) {
		writeError(w, http.StatusConflict, "task already claimed")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "claim failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// POST /api/v1/tasks/{id}/heartbeat
//
//nolint:unused // retained with the dormant heartbeat endpoint handler.
type heartbeatTaskRequest struct {
	SessionID string `json:"session_id"`
}

// handleHeartbeat bumps the row's lease window when the caller is the
// current assignee. Silent no-op (200 with same body) for non-
// assignees so dashboard sessions across tabs can fire heartbeats
// without coordinating who owns the lease.
//
//nolint:unused // dormant endpoint until task lease heartbeats are routed again.
func (h *tasksHandler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var body heartbeatTaskRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if err := h.svc.Heartbeat(r.Context(), wsID, id, body.SessionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "heartbeat failed", err.Error())
		return
	}
	// Always return the canonical post-heartbeat row so the UI can
	// re-render the lease chip even on no-op (e.g. another tab owns
	// the lease — we still want the caller to see the current
	// expires_at).
	t, err := h.svc.Get(r.Context(), wsID, id)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "fetch after heartbeat failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// POST /api/v1/tasks/{id}/notes
type appendNoteRequest struct {
	Body            string `json:"body"`
	AuthorSessionID string `json:"author_session_id"`
	AuthorKind      string `json:"author_kind"`
}

func (h *tasksHandler) handleAppendNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var body appendNoteRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		writeError(w, http.StatusBadRequest, "body is required")
		return
	}
	actorKind := body.AuthorKind
	if actorKind == "" {
		actorKind = "user"
	}
	n, err := h.svc.AppendNote(r.Context(), wsID, id, body.Body, body.AuthorSessionID, body.AuthorKind, tasks.MutationContext{
		ActorKind: actorKind,
		SessionID: body.AuthorSessionID,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "append note failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

// GET /api/v1/tasks/{id}/notes?workspace_id=
func (h *tasksHandler) handleListNotes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	notes, err := h.svc.ListNotes(r.Context(), wsID, id, limit)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list notes failed", err.Error())
		return
	}
	if notes == nil {
		notes = []store.TaskNote{}
	}
	writeJSON(w, http.StatusOK, notes)
}

// GET /api/v1/tasks/{id}/history?workspace_id=&limit=
func (h *tasksHandler) handleListHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.svc.ListHistory(r.Context(), wsID, id, limit)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list history failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": rows})
}

// POST /api/v1/tasks/{id}/rollback
type rollbackTaskRequest struct {
	Revision  int    `json:"revision"`
	SessionID string `json:"session_id"`
	ActorKind string `json:"actor_kind"`
	Note      string `json:"note"`
}

func (h *tasksHandler) handleRollback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var body rollbackTaskRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Revision <= 0 {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	actorKind := body.ActorKind
	if actorKind == "" {
		actorKind = "user"
	}
	t, err := h.svc.Rollback(r.Context(), wsID, id, tasks.RollbackOptions{
		Revision:  body.Revision,
		ActorKind: actorKind,
		SessionID: body.SessionID,
		Note:      body.Note,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "rollback failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DELETE /api/v1/tasks/{id}?workspace_id=
func (h *tasksHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if err := h.svc.Delete(r.Context(), wsID, id, tasks.MutationContext{ActorKind: "user"}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "delete failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/tasks/{id}/work_context — set the structured work-context
// pointers on a task (worktree / branch / pr / commits / peer /
// session / linear / mesh_thread). Empty string in any field clears
// that key; absent keys leave the existing value alone. Returns the
// post-mutation task.
type setWorkContextRequest struct {
	Worktree   *string  `json:"worktree,omitempty"`
	Branch     *string  `json:"branch,omitempty"`
	PR         *string  `json:"pr,omitempty"`
	Commits    *string  `json:"commits,omitempty"`
	Peer       *string  `json:"peer,omitempty"`
	Session    *string  `json:"session,omitempty"`
	Linear     *string  `json:"linear,omitempty"`
	MeshThread *string  `json:"mesh_thread,omitempty"`
	SessionID  string   `json:"session_id"` // who's making the edit; used for status_history
	Clear      []string `json:"clear,omitempty"`
}

func (h *tasksHandler) handleSetWorkContext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	var body setWorkContextRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	patch := tasks.WorkContext{}
	clears := append([]string{}, body.Clear...)
	apply := func(key string, src *string, dst *string) {
		if src == nil {
			return
		}
		if *src == "" {
			clears = append(clears, key)
			return
		}
		*dst = *src
	}
	apply("worktree", body.Worktree, &patch.Worktree)
	apply("branch", body.Branch, &patch.Branch)
	apply("pr", body.PR, &patch.PR)
	apply("commits", body.Commits, &patch.Commits)
	apply("peer", body.Peer, &patch.Peer)
	apply("session", body.Session, &patch.Session)
	apply("linear", body.Linear, &patch.Linear)
	apply("mesh_thread", body.MeshThread, &patch.MeshThread)
	t, err := h.svc.SetWorkContext(r.Context(), wsID, id, patch, clears, body.SessionID, tasks.MutationContext{
		ActorKind: "user",
		SessionID: body.SessionID,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "set work context failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// GET /api/v1/task-status-vocabulary?workspace_id=
func (h *tasksHandler) handleListVocab(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	vocab, err := h.store.ListTaskStatusVocab(r.Context(), wsID)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list vocab failed", err.Error())
		return
	}
	if vocab == nil {
		vocab = []store.TaskStatusVocab{}
	}
	writeJSON(w, http.StatusOK, vocab)
}

// POST /api/v1/task-status-vocabulary
func (h *tasksHandler) handleUpsertVocab(w http.ResponseWriter, r *http.Request) {
	var v store.TaskStatusVocab
	if err := decodeJSON(r, &v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if v.WorkspaceID == "" || v.StatusText == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and status_text are required")
		return
	}
	if err := h.store.UpsertTaskStatusVocab(r.Context(), &v); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "upsert vocab failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// GET /api/v1/tasks/stream?workspace_id=
//
// SSE stream of task mutation events. Mirrors approvalSSEHandler.stream:
// per-connection subscriber, 15s keepalive, ctx-cancel teardown. When
// workspace_id is set, only events for that workspace are forwarded —
// other events are filtered server-side so the client doesn't see
// cross-workspace traffic.
func (h *tasksHandler) handleStream(w http.ResponseWriter, r *http.Request) {
	bus := h.svc.Bus()
	if bus == nil {
		writeError(w, http.StatusBadRequest, "task event bus not enabled")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	wsFilter := r.URL.Query().Get("workspace_id")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	ch, unsub := bus.Subscribe()
	defer unsub()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if wsFilter != "" && evt.WorkspaceID != wsFilter {
				continue
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Kind, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":\n\n")
			flusher.Flush()
		}
	}
}

// Offer + filter helpers live in tasks_offers.go and tasks_filter.go.
