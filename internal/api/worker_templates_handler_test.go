// worker_templates_handler_test.go (M3) — HTTP-level integration tests
// for the publishable Worker template surface. Spins up a real sqlite-
// backed admin.Service + skill registry so the tests cover the exact
// code paths the PWA hits in production.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// newTemplatesTestServer mirrors newWorkersTestServer but wires a skill
// registry too — the M3 endpoints only register when both deps are set.
func newTemplatesTestServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "templates.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "workers", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	reg := workertemplates.New(db)
	svc := workersadmin.New(db, workersadmin.Options{Workspaces: db})
	svc.SetTemplatePublisher(reg)
	r := NewRouter(RouterDeps{
		APIToken:               "",
		Store:                  db,
		WorkerAdmin:            svc,
		WorkerTemplateRegistry: reg,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, ws.ID, scope.ID
}

func TestWorkerTemplatesPublishListInstallRoundTrip(t *testing.T) {
	srv, wsID, scopeID := newTemplatesTestServer(t)

	// Create a Worker that will be the publish source.
	createBody := map[string]any{
		"name":                "reddit-reviewer",
		"description":         "Reviews Reddit ads",
		"model_provider":      "anthropic",
		"model_id":            "claude-opus-4-7",
		"secret_scope_id":     scopeID,
		"prompt_template":     "Review {subreddit} for {brand}.",
		"schedule_spec":       "0 9 * * *",
		"workspace_id":        wsID,
		"tool_allowlist_json": `["reddit__list_ads"]`,
	}
	created := postJSON(t, srv.URL+"/api/v1/workers", createBody, http.StatusCreated)
	workerID, _ := created["id"].(string)
	if workerID == "" {
		t.Fatalf("missing worker id in %+v", created)
	}

	// Publish as template.
	pub := postJSON(t, srv.URL+"/api/v1/workers/"+workerID+"/publish",
		map[string]any{}, http.StatusOK)
	if pub["name"] != "reddit-reviewer" {
		t.Fatalf("expected template name=reddit-reviewer, got %v", pub["name"])
	}

	// List should include the template.
	resp, err := http.Get(srv.URL + "/api/v1/worker-templates")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	// Migrations 052/064/084 ship bundled templates (daily-status-digest,
	// audit-summary, cost-watcher, hello-world, task-status-consolidator,
	// telegram-responder, slack-status-notify). Filter to the one we just
	// published.
	var published map[string]any
	for _, row := range rows {
		if row["name"] == "reddit-reviewer" {
			published = row
			break
		}
	}
	if published == nil {
		t.Fatalf("expected reddit-reviewer in template list, got %+v", rows)
	}
	if published["parameter_count"].(float64) != 2 {
		t.Fatalf("expected 2 params, got %v", published["parameter_count"])
	}

	// Install — supply both required params + the secret scope.
	install := map[string]any{
		"template_name":   "reddit-reviewer",
		"worker_name":     "reddit-reviewer-clone",
		"workspace_id":    wsID,
		"secret_scope_id": scopeID,
		"parameters":      map[string]string{"subreddit": "r/SaaS", "brand": "Mcplexer"},
	}
	installed := postJSON(t, srv.URL+"/api/v1/worker-templates/install",
		install, http.StatusCreated)
	if installed["source_template_name"] != "reddit-reviewer" {
		t.Errorf("source_template_name mismatch: %v", installed["source_template_name"])
	}
	if installed["source_template_version"].(float64) != 1 {
		t.Errorf("source_template_version mismatch: %v", installed["source_template_version"])
	}
	if installed["name"] != "reddit-reviewer-clone" {
		t.Errorf("worker name mismatch: %v", installed["name"])
	}
}

func TestWorkerTemplateGetReturnsFullBody(t *testing.T) {
	srv, wsID, scopeID := newTemplatesTestServer(t)

	createBody := map[string]any{
		"name":            "tiny",
		"model_provider":  "anthropic",
		"model_id":        "claude-haiku",
		"secret_scope_id": scopeID,
		"prompt_template": "Hi {who}.",
		"schedule_spec":   "@hourly",
		"workspace_id":    wsID,
	}
	created := postJSON(t, srv.URL+"/api/v1/workers", createBody, http.StatusCreated)
	workerID, _ := created["id"].(string)

	pub := postJSON(t, srv.URL+"/api/v1/workers/"+workerID+"/publish",
		map[string]any{}, http.StatusOK)
	name, _ := pub["name"].(string)

	resp, err := http.Get(srv.URL + "/api/v1/worker-templates/" + name + "/latest")
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["entry"]; !ok {
		t.Fatalf("missing entry: %+v", out)
	}
	tmpl, ok := out["template"].(map[string]any)
	if !ok {
		t.Fatalf("template not a map: %+v", out)
	}
	if tmpl["prompt_template"] != "Hi {who}." {
		t.Fatalf("prompt mismatch: %v", tmpl["prompt_template"])
	}
}
