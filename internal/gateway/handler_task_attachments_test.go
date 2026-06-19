// handler_task_attachments_test.go — end-to-end coverage of the C2.2
// MCP surface: task__attach, task__list_attachments, task__get_attachment.
// Each test spins a fresh in-memory sqlite store + a per-test temp
// data dir (via MCPLEXER_DATA_DIR) so on-disk blobs are isolated.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// withTempDataDir points the attachments-write helpers at a fresh
// temp directory for the test's lifetime.
func withTempDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MCPLEXER_DATA_DIR", dir)
	return dir
}

func mustAttachJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seedTaskForAttachment(t *testing.T) (*handler, string, string) {
	t.Helper()
	h, db, wsID := newTasksHandler(t)
	// Create a task via the store directly — saves us from the
	// session-binding dance that handleTaskCreate would need. The
	// attachment handlers resolve the workspace from the call args
	// (workspace_id override) so we don't need session-bound state.
	sess := &store.Session{ID: "sess-attachments"}
	if err := db.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	h.sessions.session = sess
	h.sessions.clientPath = "/tmp/ws-admin"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{
		ID:       wsID,
		Name:     "ws-admin",
		RootPath: "/tmp/ws-admin",
	}}

	task := &store.Task{WorkspaceID: wsID, Title: "attachment subject"}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return h, wsID, task.ID
}

func TestTaskAttachHappyPath(t *testing.T) {
	dataDir := withTempDataDir(t)
	h, wsID, taskID := seedTaskForAttachment(t)
	ctx := context.Background()

	body := []byte("hello, attachment\n")
	b64 := base64.StdEncoding.EncodeToString(body)
	req := mustAttachJSON(t, map[string]any{
		"task_id":        taskID,
		"workspace_id":   wsID,
		"filename":       "hello.txt",
		"mime_type":      "text/plain",
		"content_base64": b64,
	})
	resp, rpcErr := h.handleTaskAttach(ctx, req)
	if rpcErr != nil {
		t.Fatalf("attach: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	att, ok := got["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("missing attachment in response: %#v", got)
	}
	if att["filename"] != "hello.txt" {
		t.Errorf("filename = %v", att["filename"])
	}
	if att["mime_type"] != "text/plain" {
		t.Errorf("mime_type = %v", att["mime_type"])
	}
	sum := sha256.Sum256(body)
	wantSha := hex.EncodeToString(sum[:])
	if att["sha256"] != wantSha {
		t.Errorf("sha256 = %v, want %s", att["sha256"], wantSha)
	}

	// Verify the on-disk blob exists at the expected content-addressed
	// path under the temp data dir.
	wantPath := filepath.Join(dataDir, "attachments", wsID, taskID, wantSha)
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("blob not written to %q: %v", wantPath, err)
	}
}

func TestTaskAttachRejectsBothBodySources(t *testing.T) {
	withTempDataDir(t)
	h, wsID, taskID := seedTaskForAttachment(t)
	req := mustAttachJSON(t, map[string]any{
		"task_id":        taskID,
		"workspace_id":   wsID,
		"content_base64": base64.StdEncoding.EncodeToString([]byte("a")),
		"bytes_inline":   "a",
	})
	_, rpcErr := h.handleTaskAttach(context.Background(), req)
	if rpcErr == nil {
		t.Fatal("expected InvalidParams when both body sources are set")
	}
	if !strings.Contains(rpcErr.Message, "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive', got %q", rpcErr.Message)
	}
}

func TestTaskAttachRejectsMissingBody(t *testing.T) {
	withTempDataDir(t)
	h, wsID, taskID := seedTaskForAttachment(t)
	req := mustAttachJSON(t, map[string]any{"task_id": taskID, "workspace_id": wsID})
	_, rpcErr := h.handleTaskAttach(context.Background(), req)
	if rpcErr == nil {
		t.Fatal("expected InvalidParams when no body provided")
	}
}

func TestTaskAttachInlineBodyRoundTrip(t *testing.T) {
	withTempDataDir(t)
	h, wsID, taskID := seedTaskForAttachment(t)

	attachReq := mustAttachJSON(t, map[string]any{
		"task_id":      taskID,
		"workspace_id": wsID,
		"filename":     "scratch.md",
		"bytes_inline": "# hello\nworld\n",
	})
	resp, rpcErr := h.handleTaskAttach(context.Background(), attachReq)
	if rpcErr != nil {
		t.Fatalf("attach: %v", rpcErr)
	}
	att := unwrapResult(t, resp)["attachment"].(map[string]any)
	id, _ := att["id"].(string)

	// Now fetch it.
	getReq := mustAttachJSON(t, map[string]any{"id": id})
	resp, rpcErr = h.handleTaskGetAttachment(context.Background(), getReq)
	if rpcErr != nil {
		t.Fatalf("get: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	b64, _ := got["content_base64"].(string)
	body, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if string(body) != "# hello\nworld\n" {
		t.Errorf("body mismatch: got %q", string(body))
	}
}

func TestTaskListAttachmentsReflectsInsertion(t *testing.T) {
	withTempDataDir(t)
	h, wsID, taskID := seedTaskForAttachment(t)
	ctx := context.Background()

	for i, name := range []string{"first.txt", "second.txt"} {
		req := mustAttachJSON(t, map[string]any{
			"task_id":      taskID,
			"workspace_id": wsID,
			"filename":     name,
			"bytes_inline": "blob " + string(rune('A'+i)),
		})
		if _, rpcErr := h.handleTaskAttach(ctx, req); rpcErr != nil {
			t.Fatalf("attach %s: %v", name, rpcErr)
		}
	}

	listReq := mustAttachJSON(t, map[string]any{"task_id": taskID, "workspace_id": wsID})
	resp, rpcErr := h.handleTaskListAttachments(ctx, listReq)
	if rpcErr != nil {
		t.Fatalf("list: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if c, _ := got["count"].(float64); int(c) != 2 {
		t.Errorf("count = %v, want 2", got["count"])
	}
	atts, _ := got["attachments"].([]any)
	if len(atts) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(atts))
	}
}

func TestTaskAttachRejectsCrossWorkspaceTask(t *testing.T) {
	withTempDataDir(t)
	h, _, taskID := seedTaskForAttachment(t)
	other := &store.Workspace{
		Name:     "ws-attachments-other",
		RootPath: "/tmp/ws-attachments-other",
		Tags:     json.RawMessage("[]"),
	}
	if err := h.store.CreateWorkspace(context.Background(), other); err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	h.sessions.wsChain = append(h.sessions.wsChain, routing.WorkspaceAncestor{
		ID:       other.ID,
		Name:     other.Name,
		RootPath: other.RootPath,
	})

	req := mustAttachJSON(t, map[string]any{
		"task_id":      taskID,
		"workspace_id": other.ID,
		"bytes_inline": "x",
	})
	resp, rpcErr := h.handleTaskAttach(context.Background(), req)
	if rpcErr != nil {
		t.Fatalf("rpc err: %v", rpcErr)
	}
	if !isErrResult(resp) {
		t.Fatalf("expected error result for cross-workspace attach, got %s", string(resp))
	}
}

func TestTaskGetAttachmentMissingReturnsErrorResult(t *testing.T) {
	withTempDataDir(t)
	h, _, _ := seedTaskForAttachment(t)
	req := mustAttachJSON(t, map[string]any{"id": "01HZZZZZZZZZZZZZZZZZZZZZZZ"})
	resp, rpcErr := h.handleTaskGetAttachment(context.Background(), req)
	if rpcErr != nil {
		t.Fatalf("rpc err: %v", rpcErr)
	}
	if !isErrResult(resp) {
		t.Fatalf("expected error result for missing attachment, got %s", string(resp))
	}
}

func TestSanitizeFilenameStripsSeparators(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"plain.txt":     "plain.txt",
		"../etc/passwd": "__etc_passwd", // "/" → "_" then ".." → "_"
		"sub/dir/x":     "sub_dir_x",
		"win\\path\\x":  "win_path_x",
		"with\x00null":  "with_null",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeAttachmentBodyEnforcesInlineCap(t *testing.T) {
	// Build a >5 MiB inline payload.
	big := strings.Repeat("a", attachmentInlineCap+1)
	args := map[string]json.RawMessage{
		"bytes_inline": mustAttachJSON(t, big),
	}
	_, rpcErr := decodeAttachmentBody(args)
	if rpcErr == nil {
		t.Fatal("expected InvalidParams for body over inline cap")
	}
	if !strings.Contains(rpcErr.Message, "exceeds inline cap") {
		t.Errorf("error message = %q", rpcErr.Message)
	}
}

// TestTaskAttachFilenameRedactsSecretShapes asserts the MCP path scrubs
// secret-shaped tokens from the filename BEFORE the row is persisted.
// Same defence-in-depth check as the REST handler — both paths must
// land a safe filename on disk + in audit.
func TestTaskAttachFilenameRedactsSecretShapes(t *testing.T) {
	githubPAT := strings.Join([]string{"ghp", "_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, "")
	anthropicKey := strings.Join([]string{"sk", "-ant-", "AAAAAAAAAAAAAAAAAAAAAAAA"}, "")
	awsAccessKey := strings.Join([]string{"AKIA", "IOSFODNN7EXAMPLE"}, "")

	cases := []struct {
		name      string
		filename  string
		mustBlock string // substring that MUST NOT survive in the persisted row
	}{
		{
			name:      "ghp_ token",
			filename:  "leak-" + githubPAT + ".txt",
			mustBlock: githubPAT,
		},
		{
			name:      "sk-ant-api key",
			filename:  "leak-" + anthropicKey + ".txt",
			mustBlock: anthropicKey,
		},
		{
			name:      "AWS access key id",
			filename:  "aws-" + awsAccessKey + ".csv",
			mustBlock: awsAccessKey,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempDataDir(t)
			h, wsID, taskID := seedTaskForAttachment(t)
			req := mustAttachJSON(t, map[string]any{
				"task_id":      taskID,
				"workspace_id": wsID,
				"filename":     tc.filename,
				"mime_type":    "text/plain",
				"bytes_inline": "body",
			})
			resp, rpcErr := h.handleTaskAttach(context.Background(), req)
			if rpcErr != nil {
				t.Fatalf("attach: %v", rpcErr)
			}
			if isErrResult(resp) {
				t.Fatalf("expected ok result, got error: %s", string(resp))
			}
			att := unwrapResult(t, resp)["attachment"].(map[string]any)
			got, _ := att["filename"].(string)
			if got == "" {
				t.Fatalf("missing filename in response: %#v", att)
			}
			if strings.Contains(got, tc.mustBlock) {
				t.Errorf("persisted filename %q still contains banned %q", got, tc.mustBlock)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("persisted filename %q should contain [REDACTED]", got)
			}
		})
	}
}

// TestTaskAttachMIMEDenylistBlocksExecutables asserts the MCP path
// rejects executable MIMEs with a tool-result error carrying the
// attachment_mime_denied code. Mirror of the REST handler test, kept
// here because the MCP path doesn't share a handler.
func TestTaskAttachMIMEDenylistBlocksExecutables(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		mime     string
	}{
		{name: "Mach-O", filename: "mac-tool", mime: "application/x-mach-binary"},
		{name: "ELF", filename: "linux-tool", mime: "application/x-executable"},
		{name: "Windows PE", filename: "setup.exe", mime: "application/vnd.microsoft.portable-executable"},
		{name: "POSIX shell via MIME", filename: "deploy", mime: "application/x-sh"},
		{name: "POSIX shell via extension fallback", filename: "deploy.sh", mime: "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempDataDir(t)
			h, wsID, taskID := seedTaskForAttachment(t)
			req := mustAttachJSON(t, map[string]any{
				"task_id":      taskID,
				"workspace_id": wsID,
				"filename":     tc.filename,
				"mime_type":    tc.mime,
				"bytes_inline": "MZ",
			})
			resp, rpcErr := h.handleTaskAttach(context.Background(), req)
			if rpcErr != nil {
				t.Fatalf("rpc err: %v", rpcErr)
			}
			if !isErrResult(resp) {
				t.Fatalf("expected error result for executable MIME, got %s", string(resp))
			}
			body := string(resp)
			if !strings.Contains(body, "attachment_mime_denied") {
				t.Errorf("expected attachment_mime_denied in body, got %s", body)
			}
		})
	}
}
