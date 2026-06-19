package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/toolgate"
)

func newHandlerWithDataDB(t *testing.T) *handler {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.store = db
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-data", RootPath: "/tmp/ws-data"}}
	return h
}

func dataResult(t *testing.T, h *handler, name, args string) string {
	t.Helper()
	raw, rpcErr, handled := h.dispatchDataTool(context.Background(), name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	return rawResultText(t, raw)
}

func TestDataToolsDispatchRoundTrip(t *testing.T) {
	h := newHandlerWithDataDB(t)
	ingest := dataResult(t, h, "data__ingest", `{
		"name":"issues",
		"tags":["run-1"],
		"rows":[
			{"state":"open","title":"urgent customer outage"},
			{"state":"done","title":"routine cleanup"}
		]
	}`)
	if !strings.Contains(ingest, `"row_count": 2`) {
		t.Fatalf("ingest body missing row count: %s", ingest)
	}

	query := dataResult(t, h, "data__query",
		`{"name":"issues","sql":"SELECT state, COUNT(*) AS c FROM data GROUP BY state"}`)
	if !strings.Contains(query, `"state": "open"`) {
		t.Fatalf("query body missing open row: %s", query)
	}

	search := dataResult(t, h, "data__search", `{"name":"issues","query":"urgent"}`)
	if !strings.Contains(search, `"count": 1`) {
		t.Fatalf("search body missing hit: %s", search)
	}

	drop := dataResult(t, h, "data__drop", `{"name":"issues"}`)
	if !strings.Contains(drop, `"ok": true`) {
		t.Fatalf("drop body = %s", drop)
	}
}

func TestDataToolsAdvertisedAndResearcherReadOnly(t *testing.T) {
	h := newHandlerWithDataDB(t)
	names := map[string]bool{}
	for _, tool := range h.codeModeBuiltinTools() {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"data__ingest", "data__describe", "data__list",
		"data__query", "data__search", "data__drop",
		"data__harvest_harness_context",
	} {
		if !names[name] {
			t.Fatalf("%s missing from code-mode builtins", name)
		}
	}

	ctx := WithWorkerCapabilityProfile(context.Background(), toolgate.Researcher())
	filtered := filterByWorkerCapability(ctx, dataToolDefinitions())
	got := map[string]bool{}
	for _, tool := range filtered {
		got[tool.Name] = true
	}
	if got["data__ingest"] || got["data__drop"] || got["data__harvest_harness_context"] {
		t.Fatalf("researcher can see write tools: %+v", got)
	}
	for _, name := range []string{"data__describe", "data__list", "data__query", "data__search"} {
		if !got[name] {
			t.Fatalf("researcher missing read tool %s: %+v", name, got)
		}
	}
}

func TestDataIngestAuditScrubsPayload(t *testing.T) {
	params := json.RawMessage(`{
		"name":"issues",
		"rows":[{"secret":"ordinary payload should not be logged"}],
		"documents":[{"text":"doc payload should not be logged"}],
		"text":"csv,payload\n1,should-not-log"
	}`)
	got := string(scrubAuditParams("data__ingest", params))
	if strings.Contains(got, "ordinary payload") || strings.Contains(got, "doc payload") ||
		strings.Contains(got, "should-not-log") {
		t.Fatalf("payload leaked into audit params: %s", got)
	}
	for _, want := range []string{`"payload_redacted":true`, `"rows_count":1`, `"documents_count":1`} {
		if !strings.Contains(got, want) {
			t.Fatalf("audit params missing %s: %s", want, got)
		}
	}
}

func TestDataHarvestHarnessContext(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"),
		[]byte("# Instructions\n\nBe helpful."), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".cursor", "rules"), 0o755); err != nil {
		t.Fatalf("mkdir cursor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, ".cursor", "rules", "go.mdc"),
		[]byte("# Go Rules\n\nUse table tests."), 0o644); err != nil {
		t.Fatalf("write cursor: %v", err)
	}
	h := newHandlerWithDataDB(t)
	args := map[string]any{
		"name":      "ctx-test",
		"harnesses": []string{"all"},
		"home_dir":  home,
		"work_dir":  work,
	}
	rawArgs, _ := json.Marshal(args)
	raw, rpcErr, handled := h.dispatchDataTool(context.Background(),
		"data__harvest_harness_context",
		rawArgs)
	if !handled {
		t.Fatal("data__harvest_harness_context not handled")
	}
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	result := rawResultText(t, raw)
	if !strings.Contains(result, `"ok": true`) {
		t.Fatalf("harvest result missing ok: %s", result)
	}
	if !strings.Contains(result, `"collection_name": "ctx-test"`) {
		t.Fatalf("harvest result missing collection_name: %s", result)
	}
	if !strings.Contains(result, `"total_ingested": 2`) {
		t.Fatalf("harvest result missing ingested count: %s", result)
	}
	cols, err := h.store.ListDataCollections(context.Background(), store.DataCollectionFilter{
		WorkspaceID: "ws-data",
	})
	if err != nil {
		t.Fatalf("ListDataCollections: %v", err)
	}
	hasHarvest := false
	for _, c := range cols {
		if c.Name == "ctx-test" && c.DocCount == 2 {
			hasHarvest = true
			break
		}
	}
	if !hasHarvest {
		t.Fatal("ctx-test collection with two docs not found after harvest")
	}
}

func TestDataHarvestHarnessContextEmpty(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	h := newHandlerWithDataDB(t)
	args := map[string]any{
		"name":      "empty-harvest",
		"harnesses": []string{"codex"},
		"home_dir":  home,
		"work_dir":  work,
	}
	rawArgs, _ := json.Marshal(args)
	raw, rpcErr, handled := h.dispatchDataTool(context.Background(),
		"data__harvest_harness_context",
		rawArgs)
	if !handled {
		t.Fatal("data__harvest_harness_context not handled")
	}
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	result := rawResultText(t, raw)
	if !strings.Contains(result, `"ok": true`) {
		t.Fatalf("empty harvest should succeed: %s", result)
	}
	if !strings.Contains(result, `"total_ingested": 0`) {
		t.Fatalf("empty harvest should report zero ingested: %s", result)
	}
	if _, err := h.store.GetDataCollection(context.Background(), "ws-data", "empty-harvest"); err == nil {
		t.Fatal("empty harvest should not replace/create collection")
	}
}

func TestHarvestToolInAdvertisedTools(t *testing.T) {
	h := newHandlerWithDataDB(t)
	names := map[string]bool{}
	for _, tool := range h.codeModeBuiltinTools() {
		names[tool.Name] = true
	}
	if !names["data__harvest_harness_context"] {
		t.Fatal("data__harvest_harness_context missing from code-mode builtins")
	}
}

func TestHarvestToolResearcherFiltered(t *testing.T) {
	ctx := WithWorkerCapabilityProfile(context.Background(), toolgate.Researcher())
	filtered := filterByWorkerCapability(ctx, dataToolDefinitions())
	got := map[string]bool{}
	for _, tool := range filtered {
		got[tool.Name] = true
	}
	if got["data__harvest_harness_context"] {
		t.Fatal("researcher should not see harvest tool (it writes)")
	}
}

func TestHarvestIdempotentReplace(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"),
		[]byte("# First"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h := newHandlerWithDataDB(t)
	args := map[string]any{
		"name":      "ctx-repeat",
		"harnesses": []string{"codex"},
		"home_dir":  home,
		"work_dir":  work,
	}
	rawArgs, _ := json.Marshal(args)
	_, _, _ = h.dispatchDataTool(context.Background(),
		"data__harvest_harness_context",
		rawArgs)
	colsBefore, _ := h.store.ListDataCollections(context.Background(), store.DataCollectionFilter{
		WorkspaceID: "ws-data",
	})
	_, _, _ = h.dispatchDataTool(context.Background(),
		"data__harvest_harness_context",
		rawArgs)
	colsAfter, _ := h.store.ListDataCollections(context.Background(), store.DataCollectionFilter{
		WorkspaceID: "ws-data",
	})
	if len(colsAfter) != len(colsBefore) {
		t.Fatalf("re-harvest should not create new collections: before=%d after=%d",
			len(colsBefore), len(colsAfter))
	}
}
