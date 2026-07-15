// monitoring_query_handler_test.go — workspace-ownership coverage for
// the REST surface behind the Monitoring page's query endpoints.
// POST /api/v1/monitoring/templates/{id}/ack and POST
// /api/v1/monitoring/notify (with remote_host_id) must reject objects
// outside the workspace the caller is acting in. Same-workspace
// behaviour must continue to work and the response shape must not
// leak the existence of a foreign object.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// captureNotifier records every Notification handed to it so the
// notify tests can assert that cross-workspace calls were dropped at
// the ownership gate rather than reaching the dispatcher.
type captureNotifier struct {
	notes []distill.Notification
}

func (c *captureNotifier) Notify(_ context.Context, n distill.Notification) error {
	c.notes = append(c.notes, n)
	return nil
}

// newMonitoringQueryHandler wires a real SQLite store with two
// workspaces and the matching distill.Query + captureNotifier onto a
// monitoringQueryHandler. Returns the handler, the db, and the
// notifier so tests can seed rows and assert behavior.
func newMonitoringQueryHandler(t *testing.T) (*monitoringQueryHandler, *sqlite.DB, *captureNotifier) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "monitoring_query.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	wsA := &store.Workspace{ID: "ws-A", Name: "Workspace A", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, wsA); err != nil {
		t.Fatalf("create ws-A: %v", err)
	}
	wsB := &store.Workspace{ID: "ws-B", Name: "Workspace B", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, wsB); err != nil {
		t.Fatalf("create ws-B: %v", err)
	}
	scopeA := &store.AuthScope{Name: "scope-A", Type: "env"}
	if err := db.CreateAuthScope(ctx, scopeA); err != nil {
		t.Fatalf("create scope-A: %v", err)
	}
	scopeB := &store.AuthScope{Name: "scope-B", Type: "env"}
	if err := db.CreateAuthScope(ctx, scopeB); err != nil {
		t.Fatalf("create scope-B: %v", err)
	}

	hostA := &store.RemoteHost{
		WorkspaceID: "ws-A", Name: "ip-prod-A", SSHUser: "logwatch",
		SSHHost: "100.64.0.3", AuthScopeID: scopeA.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, hostA); err != nil {
		t.Fatalf("create host-A: %v", err)
	}
	hostB := &store.RemoteHost{
		WorkspaceID: "ws-B", Name: "ip-prod-B", SSHUser: "logwatch",
		SSHHost: "100.64.0.4", AuthScopeID: scopeB.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, hostB); err != nil {
		t.Fatalf("create host-B: %v", err)
	}

	srcA := &store.LogSource{
		WorkspaceID: "ws-A", RemoteHostID: hostA.ID, Name: "api-A",
		Kind: store.LogSourceKindDocker, Selector: "api-a", Enabled: true,
		RetentionDays: 7, RetentionMB: 50,
	}
	if err := db.CreateLogSource(ctx, srcA); err != nil {
		t.Fatalf("create source-A: %v", err)
	}
	srcB := &store.LogSource{
		WorkspaceID: "ws-B", RemoteHostID: hostB.ID, Name: "api-B",
		Kind: store.LogSourceKindDocker, Selector: "api-b", Enabled: true,
		RetentionDays: 7, RetentionMB: 50,
	}
	if err := db.CreateLogSource(ctx, srcB); err != nil {
		t.Fatalf("create source-B: %v", err)
	}

	now := time.Now().UTC()
	tplA := &store.LogTemplate{
		ID: "tpl-A", SourceID: srcA.ID, Masked: "GET / <n>",
		Severity: store.SeverityInfo, FirstSeen: now, LastSeen: now,
	}
	if _, err := db.UpsertLogTemplate(ctx, tplA, 1); err != nil {
		t.Fatalf("upsert tpl-A: %v", err)
	}
	tplB := &store.LogTemplate{
		ID: "tpl-B", SourceID: srcB.ID, Masked: "GET / <n>",
		Severity: store.SeverityInfo, FirstSeen: now, LastSeen: now,
	}
	if _, err := db.UpsertLogTemplate(ctx, tplB, 1); err != nil {
		t.Fatalf("upsert tpl-B: %v", err)
	}

	notifier := &captureNotifier{}
	h := &monitoringQueryHandler{
		store:    db,
		query:    distill.NewQuery(db),
		notifier: notifier,
	}
	return h, db, notifier
}

// doAck exercises POST /api/v1/monitoring/templates/{id}/ack?workspace_id=…
// directly through the handler, bypassing router-level auth so the
// ownership gate is the only code path under test.
func doAck(t *testing.T, h *monitoringQueryHandler, templateID, wsID string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/monitoring/templates/"+templateID+"/ack?workspace_id="+wsID,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", templateID)
	rec := httptest.NewRecorder()
	h.ack(rec, req)
	return rec
}

// doNotify exercises POST /api/v1/monitoring/notify directly.
func doNotify(t *testing.T, h *monitoringQueryHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/monitoring/notify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.notify(rec, req)
	return rec
}

func TestMonitoringQueryWorkspaceOwnership(t *testing.T) {
	h, db, notifier := newMonitoringQueryHandler(t)
	ctx := context.Background()

	hostA, err := db.ListRemoteHosts(ctx, "ws-A")
	if err != nil || len(hostA) == 0 {
		t.Fatalf("ListRemoteHosts ws-A: %v", err)
	}
	hostB, err := db.ListRemoteHosts(ctx, "ws-B")
	if err != nil || len(hostB) == 0 {
		t.Fatalf("ListRemoteHosts ws-B: %v", err)
	}

	t.Run("ack same-workspace succeeds", func(t *testing.T) {
		rec := doAck(t, h, "tpl-A", "ws-A", `{"note":"by ws-A"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"acked":true`) {
			t.Fatalf("body missing acked:true: %s", rec.Body.String())
		}
		// Verify the template in ws-A actually got the note.
		tpl, err := db.GetLogTemplate(ctx, "tpl-A")
		if err != nil {
			t.Fatalf("GetLogTemplate tpl-A: %v", err)
		}
		if !tpl.Acked || tpl.AckNote != "by ws-A" {
			t.Fatalf("tpl-A not acked correctly: %+v", tpl)
		}
	})

	t.Run("ack cross-workspace rejected", func(t *testing.T) {
		rec := doAck(t, h, "tpl-B", "ws-A", `{"note":"sneak"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), store.ErrLogTemplateNotFound.Error()) {
			t.Fatalf("body missing not-found sentinel: %s", rec.Body.String())
		}
		// Verify the template in ws-B is untouched.
		tpl, err := db.GetLogTemplate(ctx, "tpl-B")
		if err != nil {
			t.Fatalf("GetLogTemplate tpl-B: %v", err)
		}
		if tpl.Acked {
			t.Fatalf("cross-workspace ack leaked into ws-B template: %+v", tpl)
		}
	})

	t.Run("ack unknown id rejected", func(t *testing.T) {
		rec := doAck(t, h, "missing", "ws-A", `{"note":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), store.ErrLogTemplateNotFound.Error()) {
			t.Fatalf("body missing not-found sentinel: %s", rec.Body.String())
		}
	})

	t.Run("ack missing workspace_id is bad request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost,
			"/api/v1/monitoring/templates/tpl-A/ack", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", "tpl-A")
		rec := httptest.NewRecorder()
		h.ack(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d want 400, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("notify same-workspace succeeds", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"workspace_id":   "ws-A",
			"severity":       "info",
			"title":          "ws-A ping",
			"body":           "from ws-A",
			"remote_host_id": hostA[0].ID,
		})
		before := len(notifier.notes)
		rec := doNotify(t, h, string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"dispatched":true`) {
			t.Fatalf("body missing dispatched:true: %s", rec.Body.String())
		}
		if len(notifier.notes) != before+1 {
			t.Fatalf("expected exactly one notification, got %d", len(notifier.notes)-before)
		}
		got := notifier.notes[len(notifier.notes)-1]
		if got.RemoteHostName != "ip-prod-A" || got.RemoteHostAddr != "100.64.0.3" {
			t.Fatalf("notify did not pick up ws-A host: %+v", got)
		}
	})

	t.Run("notify cross-workspace with host-B rejected", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"workspace_id":   "ws-A",
			"severity":       "info",
			"title":          "sneak",
			"body":           "from ws-A to ws-B host",
			"remote_host_id": hostB[0].ID,
		})
		before := len(notifier.notes)
		rec := doNotify(t, h, string(body))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), store.ErrRemoteHostNotFound.Error()) {
			t.Fatalf("body missing not-found sentinel: %s", rec.Body.String())
		}
		if len(notifier.notes) != before {
			t.Fatalf("cross-workspace notify reached the dispatcher: %+v", notifier.notes[before:])
		}
	})

	t.Run("notify unknown remote_host_id rejected", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"workspace_id":   "ws-A",
			"severity":       "info",
			"title":          "sneak",
			"body":           "",
			"remote_host_id": "missing",
		})
		rec := doNotify(t, h, string(body))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), store.ErrRemoteHostNotFound.Error()) {
			t.Fatalf("body missing not-found sentinel: %s", rec.Body.String())
		}
	})

	t.Run("notify without remote_host_id still succeeds in same workspace", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"workspace_id": "ws-A",
			"severity":     "warn",
			"title":        "manual",
			"body":         "no host",
		})
		rec := doNotify(t, h, string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"dispatched":true`) {
			t.Fatalf("body missing dispatched:true: %s", rec.Body.String())
		}
	})
}
