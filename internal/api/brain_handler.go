package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// brainHandler backs the dashboard Brain tile (M5). It surfaces git status
// (ahead/behind/dirty/branch/last-commit), the live validation errors from
// brain_errors, and a manual Push action (Appendix B decision #6: AUTO
// local commit, MANUAL push). It mirrors the mcplexer__brain_* admin tools
// over HTTP so the PWA hits identical code paths.
//
// Every dep is optional: when the brain is disabled the handler reports
// enabled:false and the tile renders an opt-in hint instead of crashing.
type brainHandler struct {
	cfg     brain.Config
	git     *brain.Git // nil when brain disabled / git binary absent
	store   store.Store
	enabled bool
}

// brainStatusResponse is the dashboard tile payload: enable flag, repo dir
// (for the open-in-VSCode link), git status, and validation-error count.
type brainStatusResponse struct {
	Enabled    bool             `json:"enabled"`
	Dir        string           `json:"dir"`
	Git        *brain.GitStatus `json:"git,omitempty"`
	GitErr     string           `json:"git_error,omitempty"`
	ErrorCount int              `json:"error_count"`
}

// status reports the brain tile payload. Read-only.
func (h *brainHandler) status(w http.ResponseWriter, r *http.Request) {
	resp := brainStatusResponse{Enabled: h.enabled, Dir: h.cfg.Dir}
	if h.git != nil && h.git.Available() {
		st, err := h.git.Status(r.Context())
		if err != nil {
			resp.GitErr = err.Error()
		} else {
			resp.Git = &st
		}
	} else if h.enabled {
		resp.GitErr = "git backplane not available — git binary missing or repo not initialised"
	}
	if h.store != nil {
		if errs, err := h.store.ListBrainErrors(r.Context()); err == nil {
			resp.ErrorCount = len(errs)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// errors lists the live validation errors (brain_errors) for the tile's
// "files that failed to index" list. Read-only.
func (h *brainHandler) errors(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusOK, []store.BrainError{})
		return
	}
	errs, err := h.store.ListBrainErrors(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list brain errors: "+err.Error())
		return
	}
	if errs == nil {
		errs = []store.BrainError{}
	}
	writeJSON(w, http.StatusOK, errs)
}

// push performs the manual sync: git pull --rebase --autostash then push. A
// rebase conflict is surfaced (409, not auto-resolved). Honours
// deploy-hygiene — push is manual, never on a timer.
func (h *brainHandler) push(w http.ResponseWriter, r *http.Request) {
	if h.git == nil || !h.git.Available() {
		writeError(w, http.StatusServiceUnavailable, "brain git backplane not available — brain disabled or git binary not on PATH")
		return
	}
	if err := h.git.PullRebase(r.Context()); err != nil {
		var ce *brain.ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"pushed":   false,
				"conflict": true,
				"detail":   ce.Output,
				"note":     "Rebase hit a conflict and was aborted. Resolve the conflicting brain files in VSCode, commit, then push again.",
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "brain pull --rebase: "+err.Error())
		return
	}
	if err := h.git.Push(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "brain push: "+err.Error())
		return
	}
	st, err := h.git.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain status after push: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pushed": true, "conflict": false, "status": st})
}

// sync re-derives every indexed file's row and diffs it against the live DB
// (brain.Verify), reporting drift without mutating anything. Backs the
// tile's "verify" / drift-check button. Read-only.
func (h *brainHandler) sync(w http.ResponseWriter, r *http.Request) {
	if !h.enabled || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "brain not enabled")
		return
	}
	rep, err := brain.Verify(r.Context(), h.cfg, h.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brain verify: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            rep.OK(),
		"files_checked": rep.FilesChecked,
		"drifts":        rep.Drifts,
	})
}
