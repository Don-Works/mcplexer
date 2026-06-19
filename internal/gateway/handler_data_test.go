package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
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
	if got["data__ingest"] || got["data__drop"] {
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
