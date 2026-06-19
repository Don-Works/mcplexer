package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/backup"
)

// backupHandler exposes the backup service over HTTP. The service is
// optional: if it isn't wired into RouterDeps the routes are simply
// not registered (see router.go).
type backupHandler struct {
	svc *backup.Service
}

func (h *backupHandler) list(w http.ResponseWriter, _ *http.Request) {
	items, err := h.svc.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list backups: "+err.Error())
		return
	}
	if items == nil {
		items = []backup.Manifest{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *backupHandler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note            string `json:"note"`
		IncludeIdentity *bool  `json:"include_identity"`
	}
	// Optional body — empty POST is fine.
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	// Default on: identity travels with the backup so it's a portable
	// replica. Only an explicit include_identity:false opts out (clone case).
	includeIdentity := body.IncludeIdentity == nil || *body.IncludeIdentity
	mf, err := h.svc.Create(r.Context(), body.Note, includeIdentity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create backup: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mf)
}

func (h *backupHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mf, err := h.svc.Get(id)
	if err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mf)
}

func (h *backupHandler) download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path, err := h.svc.Path(id)
	if err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "open backup: "+err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	filename := strings.ReplaceAll(id+".tar.gz", `"`, `_`)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if info, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	_, _ = io.Copy(w, f)
}

func (h *backupHandler) restore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	preID, err := h.svc.Restore(r.Context(), id)
	if err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"restored_from":           id,
		"pre_restore_snapshot_id": preID,
		"daemon_restart_required": true,
	})
}

func (h *backupHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Delete(id); err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
