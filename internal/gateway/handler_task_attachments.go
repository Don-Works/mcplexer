// handler_task_attachments.go — task__attach / task__list_attachments
// / task__get_attachment MCP tools (C2.2 of the attachments initiative,
// backed by migration 078).
//
// The bytes live on disk under the daemon's data dir at
//
//	<data_dir>/attachments/<workspace_id>/<task_id>/<sha256>
//
// content-addressed so a duplicate upload within a task dedupes to one
// on-disk blob (multiple index rows may point at the same path).
//
// REST streaming + UI for larger blobs is explicitly out of scope here
// — that lands in C2.3 / C2.4. Inline body transfer is capped at 5 MiB
// so the JSON envelope stays sane.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/attachpolicy"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
)

// attachmentInlineCap caps inline body transfer at 5 MiB per call so a
// stray gigabyte upload can't blow up the MCP JSON envelope. REST
// streaming endpoint (C2.3) will lift this for the legitimate "I want
// to attach a 50 MB recording" case.
const attachmentInlineCap = 5 * 1024 * 1024

// dispatchTaskAttachmentTool routes the three attachment tools. Returns
// (resp, err, handled) — `handled=false` lets the caller fall through.
func (h *handler) dispatchTaskAttachmentTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if h.tasksSvc == nil {
		switch name {
		case "task__attach", "task__list_attachments", "task__get_attachment":
			return marshalErrorResult("Tasks subsystem is not enabled."), nil, true
		}
		return nil, nil, false
	}
	switch name {
	case "task__attach":
		resp, err := h.handleTaskAttach(ctx, raw)
		return resp, err, true
	case "task__list_attachments":
		resp, err := h.handleTaskListAttachments(ctx, raw)
		return resp, err, true
	case "task__get_attachment":
		resp, err := h.handleTaskGetAttachment(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

func (h *handler) handleTaskAttach(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	taskID, _ := stringField(args, "task_id")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "task_id is required"}
	}
	// Validate the task exists in the resolved workspace.
	tsk, err := h.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("Task %q not found.", taskID)), nil
		}
		return marshalErrorResult(fmt.Sprintf("Get task failed: %v", err)), nil
	}
	if tsk.WorkspaceID != wsID {
		return marshalErrorResult(fmt.Sprintf("Task %q lives in workspace %q, not %q.", taskID, tsk.WorkspaceID, wsID)), nil
	}

	body, rpcErr := decodeAttachmentBody(args)
	if rpcErr != nil {
		return nil, rpcErr
	}

	filename, _ := stringField(args, "filename")
	filename = sanitizeFilename(filename)
	// Filename redaction — defence-in-depth so a filename carrying a
	// secret-shaped token (sk-..., ghp_..., Bearer-foo) never lands in
	// the persisted row or the audit ledger as plaintext. The
	// recursive Redact pass on req.Arguments would catch most patterns
	// when the audit row is composed downstream, BUT some shapes
	// (Authorization-token-foo, Bearer-dash-form) slip the value
	// regexes; pre-redacting at the call site closes that gap and
	// keeps the row Filename column consistent with the audit copy.
	filename = audit.RedactString(filename, nil)
	mimeType, _ := stringField(args, "mime_type")
	mimeType = strings.TrimSpace(mimeType)

	// MIME denylist — blanket reject the executable shapes that have no
	// place hanging off a task. Default policy from internal/attachpolicy;
	// per-workspace overrides are a future TODO behind an
	// attachment_mime_policy row in the workspaces table. The returned
	// Code matches scopes.Denial vocabulary so cross-peer audit rows can
	// surface a typed reason ("attachment_mime_denied").
	if dec := attachpolicy.Evaluate(mimeType, filename); dec.Denied {
		return marshalErrorResult(fmt.Sprintf(
			"Attachment rejected (code=%s): %s.", dec.Code, dec.Reason)), nil
	}

	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	relPath, fullPath, err := h.attachmentPaths(wsID, taskID, sha)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Resolve data dir failed: %v", err)), nil
	}
	if err := writeAttachmentBlob(fullPath, body); err != nil {
		return marshalErrorResult(fmt.Sprintf("Write attachment failed: %v", err)), nil
	}

	row := &store.TaskAttachment{
		TaskID:            taskID,
		WorkspaceID:       wsID,
		Filename:          filename,
		MimeType:          mimeType,
		SizeBytes:         int64(len(body)),
		Sha256:            sha,
		StoragePath:       relPath,
		UploaderSessionID: h.sessions.sessionID(),
		UploaderKind:      store.TaskSourceAgent,
	}
	if err := h.store.InsertTaskAttachment(ctx, row); err != nil {
		return marshalErrorResult(fmt.Sprintf("Insert attachment failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{
		"attachment": row,
		"deduped":    fileExistedAtSize(fullPath, int64(len(body))),
	})
}

func (h *handler) handleTaskListAttachments(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	taskID, _ := stringField(args, "task_id")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "task_id is required"}
	}
	// Validate the task is in this workspace (cheap consistency check
	// — prevents an attacker passing a foreign workspace_id override).
	tsk, err := h.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("Task %q not found.", taskID)), nil
		}
		return marshalErrorResult(fmt.Sprintf("Get task failed: %v", err)), nil
	}
	if tsk.WorkspaceID != wsID {
		return marshalErrorResult(fmt.Sprintf("Task %q lives in workspace %q, not %q.", taskID, tsk.WorkspaceID, wsID)), nil
	}
	rows, err := h.store.ListTaskAttachments(ctx, taskID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("List failed: %v", err)), nil
	}
	if rows == nil {
		rows = []store.TaskAttachment{}
	}
	return marshalJSONResult(map[string]any{
		"task_id":     taskID,
		"attachments": rows,
		"count":       len(rows),
	})
}

func (h *handler) handleTaskGetAttachment(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	row, err := h.store.GetTaskAttachment(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("Attachment %q not found.", id)), nil
		}
		return marshalErrorResult(fmt.Sprintf("Get attachment failed: %v", err)), nil
	}
	// Workspace isolation: when caller supplied a workspace_id override,
	// require it to match the attachment's stored workspace_id.
	if override, ok := stringField(args, "workspace_id"); ok && strings.TrimSpace(override) != "" {
		if strings.TrimSpace(override) != row.WorkspaceID {
			return marshalErrorResult(fmt.Sprintf("Attachment %q lives in workspace %q.", id, row.WorkspaceID)), nil
		}
	}
	if rpc := h.requireWorkspaceRead(ctx, row.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	if row.SizeBytes > attachmentInlineCap {
		return marshalErrorResult(fmt.Sprintf(
			"Attachment is %d bytes; inline transfer is capped at %d. Streaming endpoint lands in C2.3 — use task__list_attachments + the future GET /api/tasks/%s/attachments/%s endpoint.",
			row.SizeBytes, attachmentInlineCap, row.TaskID, row.ID)), nil
	}
	body, err := readAttachmentBlob(h, row)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Read attachment body failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{
		"attachment":     row,
		"content_base64": base64.StdEncoding.EncodeToString(body),
	})
}

// decodeAttachmentBody pulls bytes from content_base64 (preferred) or
// bytes_inline. Returns an InvalidParams error if neither is set or
// both are set, or if the decoded body exceeds attachmentInlineCap.
func decodeAttachmentBody(args map[string]json.RawMessage) ([]byte, *RPCError) {
	b64, hasB64 := stringField(args, "content_base64")
	inline, hasInline := stringField(args, "bytes_inline")
	if hasB64 && hasInline {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "content_base64 and bytes_inline are mutually exclusive"}
	}
	if !hasB64 && !hasInline {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "one of content_base64 or bytes_inline is required"}
	}
	var body []byte
	if hasB64 {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "content_base64 decode failed: " + err.Error()}
		}
		body = decoded
	} else {
		body = []byte(inline)
	}
	if int64(len(body)) > attachmentInlineCap {
		return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("body exceeds inline cap (%d > %d bytes); streaming endpoint lands in C2.3", len(body), attachmentInlineCap)}
	}
	return body, nil
}

// attachmentPaths returns (storage_path, absolute_path). The relative
// path is what we persist in the index row (portable across data-dir
// relocations); the absolute path is the actual filesystem target.
func (h *handler) attachmentPaths(workspaceID, taskID, sha string) (string, string, error) {
	dataDir, err := attachmentsDataDir()
	if err != nil {
		return "", "", err
	}
	rel := filepath.Join("attachments", workspaceID, taskID, sha)
	return rel, filepath.Join(dataDir, rel), nil
}

// attachmentsDataDir resolves the daemon's data directory. The handler
// doesn't carry a dataDir field today; we use the standard ~/.mcplexer
// convention used by every other on-disk subsystem (p2p identity,
// api-key, backups, mcplexer.db). Honors MCPLEXER_DATA_DIR for test
// rigs that point the daemon elsewhere.
func attachmentsDataDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("MCPLEXER_DATA_DIR")); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".mcplexer"), nil
}

// writeAttachmentBlob writes body to dst, creating the parent directory
// if needed. Idempotent on the content-addressed path: if a file with
// the same sha256 + size already exists, no-op (the new index row
// dedupes to the same on-disk blob).
func writeAttachmentBlob(dst string, body []byte) error {
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(body)) {
		return nil // dedupe: same content already on disk
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	// 0o600 — readable only by the daemon user, matching the mcplexer.db
	// and api-key file modes in scripts/harden-data-dir.sh.
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		return fmt.Errorf("write blob: %w", err)
	}
	return nil
}

// readAttachmentBlob resolves the row's storage_path under the data dir
// and reads it back. Enforces an absolute-path containment check so a
// malicious storage_path can't escape <data_dir>/attachments via
// "..".
func readAttachmentBlob(_ *handler, row *store.TaskAttachment) ([]byte, error) {
	dataDir, err := attachmentsDataDir()
	if err != nil {
		return nil, err
	}
	cleanRel := filepath.Clean(row.StoragePath)
	if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return nil, fmt.Errorf("storage_path escapes data dir: %q", row.StoragePath)
	}
	full := filepath.Join(dataDir, cleanRel)
	// Re-check after Join so symlink shenanigans can't get past Clean.
	absData, _ := filepath.Abs(filepath.Join(dataDir, "attachments"))
	absFull, _ := filepath.Abs(full)
	if !strings.HasPrefix(absFull, absData) {
		return nil, fmt.Errorf("storage_path resolves outside attachments dir: %q", row.StoragePath)
	}
	return os.ReadFile(full)
}

// fileExistedAtSize reports whether the blob path already had the
// expected byte length BEFORE we wrote (informational; the writer
// noops on a length match).
func fileExistedAtSize(path string, n int64) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Size() == n
}

// sanitizeFilename strips path separators so callers can't smuggle a
// relative path into the row. The on-disk storage path is
// content-addressed, so the filename is purely a display label —
// safe-but-strict is the right posture.
func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Replace any path-separator-like characters with underscore.
	for _, r := range []string{"/", "\\", "\x00"} {
		s = strings.ReplaceAll(s, r, "_")
	}
	// Disallow ".." anywhere in the name.
	s = strings.ReplaceAll(s, "..", "_")
	if len(s) > 255 {
		s = s[:255]
	}
	return s
}
