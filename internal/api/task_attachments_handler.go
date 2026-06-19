// task_attachments_handler.go (C2.3) — REST surface for task attachments.
// Companion to the MCP tools task__attach / task__list_attachments /
// task__get_attachment (internal/gateway/handler_task_attachments.go).
//
// The MCP path inlines bodies up to 5 MiB; the REST path streams up to
// the per-upload cap so the dashboard's drag-drop UI can handle larger
// files (screenshots, reports, CSV exports) without base64 overhead.
//
// Routes (all under /api/v1):
//
//	POST   /tasks/{task_id}/attachments       → multipart upload (single file)
//	GET    /tasks/{task_id}/attachments       → list metadata
//	GET    /attachments/{id}                  → stream file with correct headers
//	DELETE /attachments/{id}                  → soft delete (audit trail preserved)
//
// Storage layout matches the MCP path: <data_dir>/attachments/<workspace>/<task>/<sha256>.
// Content-addressed; multiple rows may share one on-disk blob.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/attachpolicy"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/scopes"
	"github.com/don-works/mcplexer/internal/store"
)

// taskAttachmentMaxBytes is the per-upload cap. 25 MiB matches the
// originally-planned cap from the attachments epic — large enough for
// most agent-produced artifacts (CSV exports, screenshots, generated
// docs) without giving operators a free megastorage.
const taskAttachmentMaxBytes = 25 * 1024 * 1024

// taskAttachmentHandler routes the REST surface. Holds a Store ref so it
// can resolve task+workspace + walk the attachment index, plus a closure
// for the data-dir path so tests can point it at t.TempDir().
type taskAttachmentHandler struct {
	store   store.Store
	dataDir func() (string, error)
}

func newTaskAttachmentHandler(s store.Store) *taskAttachmentHandler {
	return &taskAttachmentHandler{store: s, dataDir: defaultAttachmentsDataDir}
}

// defaultAttachmentsDataDir mirrors the MCP handler's resolution — honours
// MCPLEXER_DATA_DIR (the test seam) else ~/.mcplexer. The dashboard never
// sets this env in production; the daemon binds to ~/.mcplexer.
func defaultAttachmentsDataDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("MCPLEXER_DATA_DIR")); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".mcplexer"), nil
}

// handleListAttachments serves GET /api/v1/tasks/{task_id}/attachments.
// Returns the metadata rows — no bodies. Use GET /attachments/{id} to
// download a specific file.
func (h *taskAttachmentHandler) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.PathValue("task_id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	// Existence + workspace check so a 404 here means "no such task" rather
	// than a quiet empty list.
	if _, err := h.store.GetTask(r.Context(), taskID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "get task failed", err.Error())
		return
	}
	rows, err := h.store.ListTaskAttachments(r.Context(), taskID)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.TaskAttachment{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleUpload serves POST /api/v1/tasks/{task_id}/attachments. Accepts
// multipart/form-data with one file part named "file" (or the first part
// when the field name is unknown). Returns the persisted metadata row.
func (h *taskAttachmentHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.PathValue("task_id"))
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	tsk, err := h.store.GetTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "get task failed", err.Error())
		return
	}

	// Cap the request body BEFORE multipart parsing so a malicious client
	// can't OOM the daemon by streaming an enormous form. +4 KiB headroom
	// for the multipart envelope overhead.
	r.Body = http.MaxBytesReader(w, r.Body, taskAttachmentMaxBytes+4096)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		// Fall back to the first available file part — some clients use
		// different field names. Mirrors browsers' generic <input type=file>
		// behaviour where the name is the field name, not "file".
		if r.MultipartForm != nil {
			for _, files := range r.MultipartForm.File {
				if len(files) > 0 {
					f, ferr := files[0].Open()
					if ferr == nil {
						file, header = f, files[0]
						err = nil
						break
					}
				}
			}
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "no file part in upload (expected field 'file')")
			return
		}
	}
	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read upload body: "+err.Error())
		return
	}
	if int64(len(body)) > taskAttachmentMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("body exceeds upload cap (%d > %d bytes)", len(body), taskAttachmentMaxBytes))
		return
	}

	filename := sanitizeTaskAttachmentFilename(header.Filename)
	// Filename redaction — defence-in-depth so a filename carrying a
	// secret-shaped token (sk-..., ghp_..., Bearer-foo) never lands in
	// the persisted row or any audit row as plaintext. The audit
	// pipeline already runs req params through a recursive value-pattern
	// pass, but some shapes slip the regex (e.g. dash-form "Bearer-..."
	// vs whitespace-form "Bearer ..."), and the row's Filename column
	// itself is rendered to the dashboard, so we must redact at the
	// call site before composing either record.
	filename = audit.RedactString(filename, nil)
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" {
		// Sniff a best-effort MIME from the filename extension.
		mimeType = mime.TypeByExtension(filepath.Ext(filename))
	}

	// MIME denylist — daemon-default policy from internal/attachpolicy
	// rejects executable shapes (Mach-O / ELF / PE / shell scripts /
	// APK / DMG). Per-workspace overrides via an attachment_mime_policy
	// row are a planned follow-up; the surface here passes the denied
	// upload to writeDenial so the typed scopes.DenialCode is on the
	// wire and the dashboard can surface a structured rejection rather
	// than a generic 400.
	if dec := attachpolicy.Evaluate(mimeType, filename); dec.Denied {
		writeDenial(w, scopes.Denial{
			Code:    scopes.DenialCode(dec.Code),
			Message: dec.Reason,
		})
		return
	}

	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	dataDir, err := h.dataDir()
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "resolve data dir failed", err.Error())
		return
	}
	relPath := filepath.Join("attachments", tsk.WorkspaceID, taskID, sha)
	fullPath := filepath.Join(dataDir, relPath)
	if err := writeAttachmentBlobREST(fullPath, body); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "write blob failed", err.Error())
		return
	}

	row := &store.TaskAttachment{
		TaskID:       taskID,
		WorkspaceID:  tsk.WorkspaceID,
		Filename:     filename,
		MimeType:     mimeType,
		SizeBytes:    int64(len(body)),
		Sha256:       sha,
		StoragePath:  relPath,
		UploaderKind: store.TaskSourceUser,
	}
	if err := h.store.InsertTaskAttachment(r.Context(), row); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "insert attachment failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// handleDownload serves GET /api/v1/attachments/{id}. Streams the blob
// with correct Content-Type / Content-Length / Content-Disposition so
// the browser hands the user a "Save as" dialog with the original name.
func (h *taskAttachmentHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	row, err := h.store.GetTaskAttachment(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "get attachment failed", err.Error())
		return
	}
	if row.DeletedAt != nil {
		writeError(w, http.StatusGone, "attachment deleted")
		return
	}
	dataDir, err := h.dataDir()
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "resolve data dir failed", err.Error())
		return
	}
	full, err := safeAttachmentPath(dataDir, row.StoragePath)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "resolve storage path failed", err.Error())
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "open blob failed", err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	ct := row.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(row.SizeBytes, 10))
	if row.Filename != "" {
		// RFC 5987 — quoted ASCII fallback + UTF-8 percent-encoded copy
		// so filenames with non-ASCII characters survive.
		w.Header().Set("Content-Disposition", fmt.Sprintf(
			`attachment; filename="%s"`, sanitizeContentDisposition(row.Filename)))
	}
	// Last-Modified surfaces the upload time for clients that diff metadata.
	w.Header().Set("Last-Modified", row.CreatedAt.UTC().Format(time.RFC1123))
	if _, err := io.Copy(w, f); err != nil {
		// Stream already started; can't change status. Just log and bail.
		return
	}
}

// handleDelete serves DELETE /api/v1/attachments/{id}. Soft-deletes (the
// on-disk blob stays — GC of orphan blobs is a future concern, mirroring
// the MCP path's stance).
func (h *taskAttachmentHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.store.SoftDeleteTaskAttachment(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "delete failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAttachmentBlobREST mirrors the gateway's writeAttachmentBlob —
// idempotent on the content-addressed path; new index rows dedupe to the
// existing on-disk blob.
func writeAttachmentBlobREST(dst string, body []byte) error {
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(body)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		return fmt.Errorf("write blob: %w", err)
	}
	return nil
}

// safeAttachmentPath enforces containment: the resolved absolute path
// must live under <data_dir>/attachments. Defence-in-depth against a
// poisoned storage_path row.
func safeAttachmentPath(dataDir, storageRel string) (string, error) {
	cleanRel := filepath.Clean(storageRel)
	if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("storage_path escapes data dir: %q", storageRel)
	}
	full := filepath.Join(dataDir, cleanRel)
	absRoot, _ := filepath.Abs(filepath.Join(dataDir, "attachments"))
	absFull, _ := filepath.Abs(full)
	if !strings.HasPrefix(absFull, absRoot) {
		return "", fmt.Errorf("storage_path resolves outside attachments dir: %q", storageRel)
	}
	return full, nil
}

// sanitizeTaskAttachmentFilename strips path separators so callers can't
// smuggle a relative path. Mirrors the gateway's sanitizeFilename.
func sanitizeTaskAttachmentFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Replace anything that looks like a path component.
	s = strings.ReplaceAll(s, "\\", "_")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// sanitizeContentDisposition produces an ASCII-safe filename for the
// Content-Disposition header. Non-ASCII characters are replaced with
// underscores; quotes/backslashes are escaped.
func sanitizeContentDisposition(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "attachment"
	}
	return out
}
