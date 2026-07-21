package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// brainBrowserHandler backs the dashboard's Notion-like Brain record editor
// (M7, Appendix C.3). It exposes the workspace browser tree, typed record
// lists, single-record reads, and create/edit endpoints. Every write
// funnels through brain.Editor -> the SAME outbound Serializer the M1/M4
// dual-write engine uses, so a GUI save is byte-identical to an agent tool
// write or a VSCode edit: hash-CAS, atomic temp+rename, self-suppression,
// and autocommit all apply. The non-technical user never sees YAML or git.
//
// These routes are localhost dashboard reads/writes over already-indexed
// data, same trust level as /api/v1/tasks — no CWD-gating required (that
// gates only the admin MCP tools).
type brainBrowserHandler struct {
	editor  *brain.Editor  // nil when brain disabled
	indexer *brain.Indexer // nil when brain disabled — backs reindex/sync
	git     *brain.Git     // nil when git unavailable — backs sync
	store   store.Store
	enabled bool
}

// tree returns the client -> workspace -> entity-kind navigation tree.
func (h *brainBrowserHandler) tree(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	nodes, err := h.editor.Tree(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain tree: "+err.Error())
		return
	}
	if nodes == nil {
		nodes = []brain.TreeNode{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// listTasks returns the editable task records for one workspace.
func (h *brainBrowserHandler) listTasks(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	ws := r.PathValue("ws")
	recs, err := h.editor.ListTasks(r.Context(), ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain list tasks: "+err.Error())
		return
	}
	if recs == nil {
		recs = []brain.TaskRecord{}
	}
	writeJSON(w, http.StatusOK, recs)
}

// listMemories returns the editable memory records for one workspace.
func (h *brainBrowserHandler) listMemories(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	ws := r.PathValue("ws")
	recs, err := h.editor.ListMemories(r.Context(), ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain list memories: "+err.Error())
		return
	}
	if recs == nil {
		recs = []brain.MemoryRecord{}
	}
	writeJSON(w, http.StatusOK, recs)
}

// getRecord returns one record (task or memory) for the editor form.
func (h *brainBrowserHandler) getRecord(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	switch kind {
	case brain.EntityKindTask:
		rec, err := h.editor.GetTaskDetail(r.Context(), id)
		if h.handleGetErr(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, rec)
	case brain.EntityKindMemory:
		rec, err := h.editor.GetMemoryDetail(r.Context(), id)
		if h.handleGetErr(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, rec)
	default:
		writeError(w, http.StatusBadRequest, "unknown record kind "+kind)
	}
}

// saveRecord handles both create (POST, empty id) and edit (PUT, id in
// path) by routing on kind + merging the path id into the body. The write
// goes through brain.Editor: validation failures -> 422, CAS conflicts ->
// 409 (with the saved record so the editor can show "your edit is in a
// .conflict sidecar"), success -> 200.
func (h *brainBrowserHandler) saveRecord(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	kind := r.PathValue("kind")
	id := r.PathValue("id") // empty on POST (create)
	switch kind {
	case brain.EntityKindTask:
		h.saveTask(w, r, id)
	case brain.EntityKindMemory:
		h.saveMemory(w, r, id)
	default:
		writeError(w, http.StatusBadRequest, "unknown record kind "+kind)
	}
}

func (h *brainBrowserHandler) saveTask(w http.ResponseWriter, r *http.Request, id string) {
	var rec brain.TaskRecord
	if err := decodeJSON(r, &rec); err != nil {
		writeError(w, http.StatusBadRequest, "decode task: "+err.Error())
		return
	}
	if id != "" {
		rec.ID = id
	}
	vocab := h.statusVocab(r, rec.Workspace)
	saved, err := h.editor.SaveTask(r.Context(), rec, vocab)
	h.writeSaveResult(w, r, rec.Workspace, saved, err)
}

func (h *brainBrowserHandler) saveMemory(w http.ResponseWriter, r *http.Request, id string) {
	var rec brain.MemoryRecord
	if err := decodeJSON(r, &rec); err != nil {
		writeError(w, http.StatusBadRequest, "decode memory: "+err.Error())
		return
	}
	if id != "" {
		rec.ID = id
	}
	saved, err := h.editor.SaveMemory(r.Context(), rec)
	h.writeSaveResult(w, r, rec.Workspace, saved, err)
}

// writeSaveResult maps the editor's typed errors onto HTTP status codes:
// validation -> 422 (carrying field + allowed-vocab so the GUI renders the
// error inline at the offending control), if_hash conflict -> 409 (carrying
// the fresh on-disk record + named writer for the field-level reconciler),
// CAS-sidecar conflict -> 409 (the legacy divert), other -> 500, success ->
// 200.
func (h *brainBrowserHandler) writeSaveResult(w http.ResponseWriter, r *http.Request, workspace string, saved any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, saved)
		return
	}
	var ve *brain.ValidationError
	switch {
	case errors.As(err, &ve):
		body := map[string]any{
			"error": err.Error(),
			"field": ve.Field,
		}
		// For a status-vocab failure, ship the allowed set so the editor can
		// render the inline one-click fix (DESIGN §3.7) without a second call.
		if ve.Field == "status" {
			if vocab := h.statusVocab(r, workspace); len(vocab) > 0 {
				body["allowed"] = vocab
			}
		}
		writeJSON(w, http.StatusUnprocessableEntity, body)
	case h.writeConflictDetail(w, err):
		// 409 with the structured reconciler payload was written.
	case errors.Is(err, brain.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "your edit conflicted with a concurrent change; it was saved to a .conflict sidecar — reload to merge",
			"conflict": true,
			"record":   saved,
		})
	default:
		writeError(w, http.StatusInternalServerError, "brain save: "+err.Error())
	}
}

// writeConflictDetail emits the structured 409 reconciler payload when err
// carries a brain.ConflictDetail (the if_hash CAS mismatch). Reports whether
// it handled the error.
func (h *brainBrowserHandler) writeConflictDetail(w http.ResponseWriter, err error) bool {
	det, ok := brain.AsConflictDetail(err)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":    "this record changed on disk while you were editing — reconcile your changes",
		"conflict": true,
		"detail":   det,
	})
	return true
}

// statusVocab loads the workspace's task-status vocabulary so a GUI status
// edit is validated against the same allowed set the indexer uses. An
// empty vocab (none configured) skips the status check (operational
// default), matching ValidateTask's contract.
func (h *brainBrowserHandler) statusVocab(r *http.Request, workspace string) []string {
	if h.store == nil || strings.TrimSpace(workspace) == "" {
		return nil
	}
	rows, err := h.store.ListTaskStatusVocab(r.Context(), workspace)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, v := range rows {
		out = append(out, v.StatusText)
	}
	return out
}

// handleGetErr writes a 404 on ErrNotFound, 500 otherwise, and reports
// whether the caller should stop (true when an error was written).
func (h *brainBrowserHandler) handleGetErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "record not found")
		return true
	}
	writeError(w, http.StatusInternalServerError, "brain get record: "+err.Error())
	return true
}

// ready guards every endpoint: when the brain is disabled or the editor is
// unwired it 503s so the SPA renders an opt-in hint instead of crashing.
func (h *brainBrowserHandler) ready(w http.ResponseWriter) bool {
	if !h.enabled || h.editor == nil {
		writeError(w, http.StatusServiceUnavailable, "brain not enabled")
		return false
	}
	return true
}
