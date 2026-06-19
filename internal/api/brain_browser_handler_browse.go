package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/brain"
)

// This file holds the brain browser's read/search + reindex/sync surface,
// split from brain_browser_handler.go to keep each file under the 300-line
// cap. All reads come from the derived index; reindex/sync route through the
// indexer + git backplane.

// clients returns the client/org tier (parent workspaces).
func (h *brainBrowserHandler) clients(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	nodes, err := h.editor.Clients(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain clients: "+err.Error())
		return
	}
	if nodes == nil {
		nodes = []brain.ClientNode{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// workspaces returns the child workspaces of a client (or all when client is
// absent), each enriched with source + ancestor chain + counts.
func (h *brainBrowserHandler) workspaces(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	nodes, err := h.editor.Workspaces(r.Context(), r.URL.Query().Get("client"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain workspaces: "+err.Error())
		return
	}
	if nodes == nil {
		nodes = []brain.WorkspaceNode{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// scope returns the agent's literal scope-fusion string for a workspace
// (e.g. "acme-api ∪ acme ∪ global") — the browser's mono footer line.
func (h *brainBrowserHandler) scope(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	fusion, err := h.editor.Scope(r.Context(), r.URL.Query().Get("workspace"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain scope: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"scope": fusion})
}

// records returns the typed record list for a workspace, filtered by kind +
// status + source, each row carrying the live_lease + validation_error flags
// the browser renders as shimmer / pulse markers.
func (h *brainBrowserHandler) records(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	q := r.URL.Query()
	ws := q.Get("workspace")
	kind := q.Get("kind")
	status := q.Get("status")
	source := q.Get("source")
	memoryKind := q.Get("memory_kind")
	switch kind {
	case "", brain.EntityKindTask:
		recs, err := h.editor.ListTasks(r.Context(), ws)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "brain records: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, filterTasks(recs, status, source))
	case brain.EntityKindMemory:
		recs, err := h.editor.ListMemories(r.Context(), ws)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "brain records: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, filterMemories(recs, source, memoryKind))
	default:
		writeError(w, http.StatusBadRequest, "unknown record kind "+kind)
	}
}

// filterTasks narrows the typed task list by status + index source in Go (the
// list is already workspace-scoped + small). A non-nil empty slice keeps the
// JSON an array, never null.
func filterTasks(recs []brain.TaskRecord, status, source string) []brain.TaskRecord {
	out := make([]brain.TaskRecord, 0, len(recs))
	for i := range recs {
		if status != "" && recs[i].Status != status {
			continue
		}
		if source != "" && recs[i].IndexSource != source {
			continue
		}
		out = append(out, recs[i])
	}
	return out
}

// filterMemories narrows the memory list by index source + note/fact kind.
func filterMemories(recs []brain.MemoryRecord, source, memoryKind string) []brain.MemoryRecord {
	out := make([]brain.MemoryRecord, 0, len(recs))
	for i := range recs {
		if source != "" && recs[i].IndexSource != source {
			continue
		}
		if memoryKind != "" && recs[i].Kind != memoryKind {
			continue
		}
		out = append(out, recs[i])
	}
	return out
}

// search powers cmd+K + every in-field typeahead: one frecency-ranked,
// three-tier (exact-prefix -> token -> fuzzy) search over the FTS5 index with
// the ~10k scale-cliff fuzzy fallback. q is raw typed text; kind narrows to
// task|memory|"".
func (h *brainBrowserHandler) search(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := h.editor.Search(r.Context(), q.Get("q"), q.Get("kind"), q.Get("workspace"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain search: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// suppressCandidate records the sticky per-record "never suggest this memory
// candidate again" decision (DESIGN §3.5). Body: {content_hash?}. A blank
// hash suppresses all candidates for the record.
func (h *brainBrowserHandler) suppressCandidate(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := r.PathValue("id")
	var body struct {
		ContentHash string `json:"content_hash"`
	}
	// An absent/empty body is valid (suppress-all); ignore decode errors on
	// empty input.
	_ = decodeJSON(r, &body)
	if err := h.editor.SuppressCandidate(r.Context(), id, strings.TrimSpace(body.ContentHash)); err != nil {
		writeError(w, http.StatusInternalServerError, "brain suppress candidate: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressed": true})
}

// reindex runs a full reindex of the brain tree (cmd+K "> reindex"). The
// derived index is rebuildable, so this is the correctness backstop after an
// out-of-band edit.
func (h *brainBrowserHandler) reindex(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if h.indexer == nil {
		writeError(w, http.StatusServiceUnavailable, "brain indexer not available")
		return
	}
	if err := h.indexer.ReindexAll(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "brain reindex: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reindexed": true})
}

// browserSync runs the brain sync the GUI offers: git pull --rebase
// --autostash, then a full reindex so the freshly-pulled files are queryable
// (DESIGN §6 POST /sync; push stays manual per decision #6). A rebase
// conflict is surfaced (409), never auto-resolved.
func (h *brainBrowserHandler) browserSync(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if h.git == nil || !h.git.Available() {
		writeError(w, http.StatusServiceUnavailable, "brain git backplane not available")
		return
	}
	if err := h.git.PullRebase(r.Context()); err != nil {
		var ce *brain.ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"synced":   false,
				"conflict": true,
				"detail":   ce.Output,
				"note":     "Rebase hit a conflict and was aborted. Resolve the conflicting brain files, commit, then sync again.",
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "brain sync pull --rebase: "+err.Error())
		return
	}
	if h.indexer != nil {
		if err := h.indexer.ReindexAll(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "brain sync reindex: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"synced": true, "conflict": false})
}
