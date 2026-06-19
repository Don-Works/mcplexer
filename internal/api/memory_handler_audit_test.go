// memory_handler_audit_test.go — REST↔MCP audit-parity coverage for bug
// F053JE (01KSM6D2FSHZCNP5PPT6F053JE). The REST memory mutation surface
// (/api/v1/memory POST/DELETE/{id}/pin/unpin/invalidate/entities) MUST
// emit audit_records rows with tool_name=memory__* mirroring the MCP
// path. Secret-shaped substrings in `content` MUST be redacted by the
// audit.Logger.Record pipeline before the row is persisted.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newMemoryAuditTestServer wires a fresh sqlite-backed memory.Service +
// audit.Logger into an httptest.Server. Returns the server, the
// underlying store (for direct audit assertions), and the memory.Service.
func newMemoryAuditTestServer(t *testing.T) (*httptest.Server, *sqlite.DB, *memory.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := memory.NewService(db, nil, nil)
	auditor := audit.NewLogger(db, db, nil)
	r := NewRouter(RouterDeps{
		APIToken:  "",
		Store:     db,
		MemorySvc: svc,
		Auditor:   auditor,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

// findAuditRowByTool returns the first audit row with the given
// tool_name (most-recent first per store ordering). Fails the test if
// no row is found.
func findAuditRowByTool(t *testing.T, db *sqlite.DB, toolName string) store.AuditRecord {
	t.Helper()
	tn := toolName
	rows, _, err := db.QueryAuditRecords(context.Background(), store.AuditFilter{
		ToolName: &tn,
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("QueryAuditRecords: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("expected an audit row with tool_name=%s, got 0", toolName)
	}
	return rows[0]
}

// TestMemoryRESTAuditEmits_Create asserts POST /api/v1/memory lands a
// tool_name=memory__save row AND that the redactor scrubs the embedded
// sk- key from `content`.
func TestMemoryRESTAuditEmits_Create(t *testing.T) {
	srv, db, _ := newMemoryAuditTestServer(t)

	secret := strings.Join([]string{"sk", "-proj-", "abcdefghij1234567890abcdefghij1234567890abcd"}, "")
	body := map[string]any{
		"name":    "leaky-memory",
		"content": "rotate creds: " + secret + " by EOQ",
		"kind":    "fact",
		"tags":    []string{"audit-parity"},
	}
	created := postJSON(t, srv.URL+"/api/v1/memory", body, http.StatusCreated)
	if _, ok := created["id"].(string); !ok {
		t.Fatalf("expected id on response: %v", created)
	}

	row := findAuditRowByTool(t, db, "memory__save")
	if row.Status != "success" {
		t.Errorf("status=%q want success", row.Status)
	}
	if row.ClientType != "api" {
		t.Errorf("client_type=%q want api", row.ClientType)
	}
	if row.ActorKind != "api" {
		t.Errorf("actor_kind=%q want api", row.ActorKind)
	}
	// Redactor MUST have scrubbed the secret out of ParamsRedacted.
	if strings.Contains(string(row.ParamsRedacted), secret) {
		t.Errorf("audit ParamsRedacted leaked secret: %s", string(row.ParamsRedacted))
	}
	if !strings.Contains(string(row.ParamsRedacted), "REDACTED") {
		t.Errorf("expected [REDACTED] in ParamsRedacted, got: %s", string(row.ParamsRedacted))
	}
	// Sanity: the memory row itself still holds the secret — the
	// redactor only scrubs the audit ledger.
	var params map[string]any
	if err := json.Unmarshal(row.ParamsRedacted, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params["name"] != "leaky-memory" {
		t.Errorf("name=%v want leaky-memory", params["name"])
	}
}

// TestMemoryRESTAuditEmits_Invalidate asserts the invalidate path
// produces tool_name=memory__invalidate on success.
func TestMemoryRESTAuditEmits_Invalidate(t *testing.T) {
	srv, db, svc := newMemoryAuditTestServer(t)
	id := seedMemory(t, svc, "rotating-secret", "version one secret body", store.MemoryKindFact, nil)

	postJSONMethod(t, http.MethodPost, srv.URL+"/api/v1/memory/"+id+"/invalidate",
		map[string]any{}, http.StatusNoContent)

	row := findAuditRowByTool(t, db, "memory__invalidate")
	if row.Status != "success" {
		t.Errorf("status=%q want success", row.Status)
	}
	var params map[string]any
	if err := json.Unmarshal(row.ParamsRedacted, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params["id"] != id {
		t.Errorf("params.id=%v want %s", params["id"], id)
	}
}

// TestMemoryRESTAuditEmits_Delete asserts DELETE /api/v1/memory/{id}
// emits tool_name=memory__forget.
func TestMemoryRESTAuditEmits_Delete(t *testing.T) {
	srv, db, svc := newMemoryAuditTestServer(t)
	id := seedMemory(t, svc, "to-forget", "ephemeral", store.MemoryKindNote, nil)

	deleteReq(t, srv.URL+"/api/v1/memory/"+id, http.StatusNoContent)

	row := findAuditRowByTool(t, db, "memory__forget")
	if row.Status != "success" {
		t.Errorf("status=%q want success", row.Status)
	}
}

// TestMemoryRESTAuditEmits_Pin asserts the pin/unpin paths emit
// memory__pin / memory__unpin respectively.
func TestMemoryRESTAuditEmits_Pin(t *testing.T) {
	srv, db, svc := newMemoryAuditTestServer(t)
	id := seedMemory(t, svc, "pinned-mem", "pin me memory body", store.MemoryKindNote, nil)

	postJSONMethod(t, http.MethodPost, srv.URL+"/api/v1/memory/"+id+"/pin",
		map[string]any{}, http.StatusNoContent)
	pinRow := findAuditRowByTool(t, db, "memory__pin")
	if pinRow.Status != "success" {
		t.Errorf("pin status=%q want success", pinRow.Status)
	}

	postJSONMethod(t, http.MethodPost, srv.URL+"/api/v1/memory/"+id+"/unpin",
		map[string]any{}, http.StatusNoContent)
	unpinRow := findAuditRowByTool(t, db, "memory__unpin")
	if unpinRow.Status != "success" {
		t.Errorf("unpin status=%q want success", unpinRow.Status)
	}
}

// TestMemoryRESTAuditEmits_ForgetBySource asserts the source-purge path
// emits tool_name=memory__forget_by_source.
func TestMemoryRESTAuditEmits_ForgetBySource(t *testing.T) {
	srv, db, _ := newMemoryAuditTestServer(t)

	// Seed one row tagged with a known source_session_id so the purge
	// has something to delete.
	if err := db.WriteMemory(context.Background(), &store.MemoryEntry{
		Name:            "to-purge",
		Kind:            store.MemoryKindNote,
		Content:         "ssh",
		SourceKind:      store.MemorySourceAgent,
		SourceSessionID: "evil-session-99",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out := postJSON(t, srv.URL+"/api/v1/memory/forget-by-source",
		map[string]any{"source_session_id": "evil-session-99"}, http.StatusOK)
	if int(out["count"].(float64)) != 1 {
		t.Fatalf("expected count=1, got %v", out["count"])
	}

	row := findAuditRowByTool(t, db, "memory__forget_by_source")
	if row.Status != "success" {
		t.Errorf("status=%q want success", row.Status)
	}
	// Note: source_session_id is scrubbed by the global key-based
	// redactor (anything containing "session") — that's expected
	// redactor behaviour, not a regression. The point of this test is
	// the row EXISTS with the right tool_name, not that the value
	// round-trips verbatim.
	var params map[string]any
	if err := json.Unmarshal(row.ParamsRedacted, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if _, ok := params["source_session_id"]; !ok {
		t.Errorf("expected source_session_id key in params, got: %s", string(row.ParamsRedacted))
	}
}

// TestMemoryRESTAuditEmits_LinkUnlinkEntity asserts the
// /api/v1/memory/{id}/entities POST + DELETE paths emit
// memory__link_entity / memory__unlink_entity rows.
func TestMemoryRESTAuditEmits_LinkUnlinkEntity(t *testing.T) {
	srv, db, svc := newMemoryAuditTestServer(t)
	memID := seedMemory(t, svc, "with-entity", "entity link memory body", store.MemoryKindFact, nil)

	// Link.
	resp, err := http.Post(srv.URL+"/api/v1/memory/"+memID+"/entities",
		"application/json",
		strings.NewReader(`{"kind":"customer","id":"acme","role":"subject"}`))
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("link status=%d want 204", resp.StatusCode)
	}
	linkRow := findAuditRowByTool(t, db, "memory__link_entity")
	if linkRow.Status != "success" {
		t.Errorf("link status=%q want success", linkRow.Status)
	}

	// Unlink (the DELETE-with-body shape).
	req, err := http.NewRequest(http.MethodDelete,
		srv.URL+"/api/v1/memory/"+memID+"/entities",
		strings.NewReader(`{"kind":"customer","id":"acme","role":"subject"}`))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink status=%d want 204", resp2.StatusCode)
	}
	unlinkRow := findAuditRowByTool(t, db, "memory__unlink_entity")
	if unlinkRow.Status != "success" {
		t.Errorf("unlink status=%q want success", unlinkRow.Status)
	}
}
