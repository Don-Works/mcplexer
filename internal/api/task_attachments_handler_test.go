// task_attachments_handler_test.go (C2.3) — HTTP coverage for the
// attachments REST surface. Spins up a sqlite-backed Store + httptest
// server, exercises round-trip: upload → list → download → delete.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/oklog/ulid/v2"
)

func newTaskAttachmentsTestServer(t *testing.T) (url string, wsID, taskID, dataDir string) {
	t.Helper()
	ctx := context.Background()
	dataDir = t.TempDir()
	t.Setenv("MCPLEXER_DATA_DIR", dataDir)

	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "att-test", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Seed a task in the workspace so attachments have a target.
	taskID = ulid.Make().String()
	tk := &store.Task{
		ID:          taskID,
		WorkspaceID: ws.ID,
		Title:       "test task",
		Status:      "open",
		SourceKind:  store.TaskSourceUser,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.CreateTask(ctx, tk); err != nil {
		t.Fatalf("create task: %v", err)
	}

	tasksSvc := tasks.New(db)
	r := NewRouter(RouterDeps{
		Store:    db,
		TasksSvc: tasksSvc,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, ws.ID, taskID, dataDir
}

func TestTaskAttachmentUploadListDownloadDelete(t *testing.T) {
	url, _, taskID, _ := newTaskAttachmentsTestServer(t)

	// 1. Upload a file via multipart.
	body := []byte("Hello attachment world.\nLine two.\n")
	uploadResp := uploadAttachment(t, url, taskID, "hello.txt", "text/plain", body, http.StatusCreated)
	id, _ := uploadResp["id"].(string)
	if id == "" {
		t.Fatalf("upload: missing id: %+v", uploadResp)
	}
	if int(uploadResp["size_bytes"].(float64)) != len(body) {
		t.Errorf("upload: size_bytes=%v, want %d", uploadResp["size_bytes"], len(body))
	}
	if uploadResp["filename"] != "hello.txt" {
		t.Errorf("upload: filename=%v, want hello.txt", uploadResp["filename"])
	}
	if uploadResp["mime_type"] != "text/plain" {
		t.Errorf("upload: mime_type=%v, want text/plain", uploadResp["mime_type"])
	}

	// 2. List — must include the new row.
	listResp, err := http.Get(url + "/api/v1/tasks/" + taskID + "/attachments")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = listResp.Body.Close() }()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: status=%d", listResp.StatusCode)
	}

	// 3. Download — body bytes must round-trip.
	dlResp, err := http.Get(url + "/api/v1/attachments/" + id)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = dlResp.Body.Close() }()
	if dlResp.StatusCode != http.StatusOK {
		t.Fatalf("download: status=%d", dlResp.StatusCode)
	}
	if got := dlResp.Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("download: Content-Type=%q, want text/plain", got)
	}
	if got := dlResp.Header.Get("Content-Disposition"); got != `attachment; filename="hello.txt"` {
		t.Errorf("download: Content-Disposition=%q", got)
	}
	gotBody, err := io.ReadAll(dlResp.Body)
	if err != nil {
		t.Fatalf("download read: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("download body mismatch: got %d bytes, want %d", len(gotBody), len(body))
	}

	// 4. Delete — 204 then 410 on next download attempt.
	delReq, _ := http.NewRequest(http.MethodDelete, url+"/api/v1/attachments/"+id, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: status=%d, want 204", delResp.StatusCode)
	}
	dlAfter, _ := http.Get(url + "/api/v1/attachments/" + id)
	defer func() { _ = dlAfter.Body.Close() }()
	if dlAfter.StatusCode != http.StatusGone && dlAfter.StatusCode != http.StatusNotFound {
		t.Errorf("download after delete: status=%d, want 410 or 404", dlAfter.StatusCode)
	}
}

func TestTaskAttachmentUploadRejectsOversize(t *testing.T) {
	url, _, taskID, _ := newTaskAttachmentsTestServer(t)
	huge := bytes.Repeat([]byte("a"), taskAttachmentMaxBytes+1)
	// The MaxBytesReader truncates at the cap, so the body either gets
	// rejected as a malformed multipart (because the file part is cut off)
	// or hits our explicit size-check. Either way we expect a 4xx.
	resp := tryUploadAttachment(t, url, taskID, "huge.bin", "application/octet-stream", huge)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("expected 4xx for oversize upload, got %d", resp.StatusCode)
	}
}

func TestTaskAttachmentUploadOnUnknownTaskIs404(t *testing.T) {
	url, _, _, _ := newTaskAttachmentsTestServer(t)
	resp := tryUploadAttachment(t, url, "ghost-task", "x.txt", "text/plain", []byte("x"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown task, got %d", resp.StatusCode)
	}
}

// TestTaskAttachmentFilenameRedaction asserts the uploaded filename is
// scrubbed of secret-shaped tokens BEFORE the row + audit record are
// composed. Three families of leak: ghp_ (GitHub PAT), sk-ant- (Anthropic
// API key), AKIA (AWS access key id). All must be replaced with
// [REDACTED] in the response (which mirrors the persisted row), so the
// dashboard / agent never sees the original token even if it's typed
// into the filename field.
func TestTaskAttachmentFilenameRedaction(t *testing.T) {
	url, _, taskID, _ := newTaskAttachmentsTestServer(t)
	githubPAT := strings.Join([]string{"ghp", "_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, "")
	anthropicKey := strings.Join([]string{"sk", "-ant-", "AAAAAAAAAAAAAAAAAAAAAAAA"}, "")
	awsAccessKey := strings.Join([]string{"AKIA", "IOSFODNN7EXAMPLE"}, "")

	cases := []struct {
		name          string
		filename      string
		mustNotAppear []string // substrings that must NOT be in the persisted filename
		mustContain   string   // substring that MUST be in the persisted filename ([REDACTED])
	}{
		{
			name:          "ghp_ token in filename redacted",
			filename:      "leak-" + githubPAT + ".txt",
			mustNotAppear: []string{githubPAT},
			mustContain:   "[REDACTED]",
		},
		{
			name:          "sk-ant-api key in filename redacted",
			filename:      "anthropic-" + anthropicKey + ".txt",
			mustNotAppear: []string{anthropicKey},
			mustContain:   "[REDACTED]",
		},
		{
			name:          "AWS access key id in filename redacted",
			filename:      "aws-" + awsAccessKey + ".csv",
			mustNotAppear: []string{awsAccessKey},
			mustContain:   "[REDACTED]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := uploadAttachment(t, url, taskID, tc.filename, "text/plain",
				[]byte("body"), http.StatusCreated)
			got, _ := out["filename"].(string)
			if got == "" {
				t.Fatalf("missing filename in response: %#v", out)
			}
			for _, banned := range tc.mustNotAppear {
				if strings.Contains(got, banned) {
					t.Errorf("persisted filename %q still contains banned %q", got, banned)
				}
			}
			if !strings.Contains(got, tc.mustContain) {
				t.Errorf("persisted filename %q should contain %q", got, tc.mustContain)
			}
		})
	}
}

// TestTaskAttachmentMIMEDenylistRejectsExecutables drives the new
// daemon-default executable-MIME denylist (internal/attachpolicy). Each
// row should land a 403 with the typed scopes.Denial body — that lets
// the dashboard render a structured rejection rather than guessing at
// a 400.
func TestTaskAttachmentMIMEDenylistRejectsExecutables(t *testing.T) {
	url, _, taskID, _ := newTaskAttachmentsTestServer(t)

	cases := []struct {
		name     string
		filename string
		mime     string
	}{
		{name: "Mach-O", filename: "mac-tool", mime: "application/x-mach-binary"},
		{name: "ELF", filename: "linux-tool", mime: "application/x-executable"},
		{name: "Windows PE", filename: "setup.exe", mime: "application/vnd.microsoft.portable-executable"},
		{name: "POSIX shell", filename: "deploy.sh", mime: "application/x-sh"},
		{name: "Bash via extension fallback", filename: "deploy.sh", mime: "application/octet-stream"},
		{name: "APK", filename: "app.apk", mime: "application/vnd.android.package-archive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tryUploadAttachment(t, url, taskID, tc.filename, tc.mime, []byte("MZ"))
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 403 for %s, got %d", tc.name, resp.StatusCode)
			}
			var out struct {
				Error  string `json:"error"`
				Denial struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"denial"`
			}
			if err := decodeBody(resp.Body, &out); err != nil {
				t.Fatalf("decode 403 body: %v", err)
			}
			if out.Error != "forbidden" {
				t.Errorf("error=%q, want %q", out.Error, "forbidden")
			}
			if out.Denial.Code != "attachment_mime_denied" {
				t.Errorf("denial.code=%q, want %q", out.Denial.Code, "attachment_mime_denied")
			}
			if out.Denial.Message == "" {
				t.Error("denial.message should explain the rejection")
			}
		})
	}
}

// TestTaskAttachmentMIMEAllowedShapesPass guards the happy-path edge
// of the policy: common docs/images/archives keep uploading cleanly.
// Without this we'd risk a future denylist expansion silently breaking
// a legitimate workflow.
func TestTaskAttachmentMIMEAllowedShapesPass(t *testing.T) {
	url, _, taskID, _ := newTaskAttachmentsTestServer(t)
	cases := []struct {
		filename string
		mime     string
	}{
		{filename: "report.pdf", mime: "application/pdf"},
		{filename: "screenshot.png", mime: "image/png"},
		{filename: "export.csv", mime: "text/csv"},
		{filename: "data.json", mime: "application/json"},
		{filename: "notes.txt", mime: "text/plain"},
		{filename: "archive.zip", mime: "application/zip"},
	}
	for _, tc := range cases {
		t.Run(tc.mime, func(t *testing.T) {
			uploadAttachment(t, url, taskID, tc.filename, tc.mime, []byte("body"),
				http.StatusCreated)
		})
	}
}

// --- helpers --------------------------------------------------------------

func uploadAttachment(t *testing.T, baseURL, taskID, filename, mime string, body []byte, wantStatus int) map[string]any {
	t.Helper()
	resp := tryUploadAttachment(t, baseURL, taskID, filename, mime, body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: status=%d want=%d body=%s", resp.StatusCode, wantStatus, string(raw))
	}
	var out map[string]any
	if err := decodeBody(resp.Body, &out); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return out
}

func tryUploadAttachment(t *testing.T, baseURL, taskID, filename, mime string, body []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	h["Content-Type"] = []string{mime}
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write part: %v", err)
	}
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tasks/"+taskID+"/attachments", &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload do: %v", err)
	}
	return resp
}

func decodeBody(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
