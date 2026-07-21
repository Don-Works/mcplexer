// handlers_workers_extras_test.go (M0.7) — coverage for the MCP-only
// parity tools added so an MCP admin agent can do every workflow the
// PWA exposes (cost rollup, tool discovery) without dropping to HTTP.
package control

import (
	"strings"
	"testing"
)

func TestWorkerCostAggregateMCPDispatch(t *testing.T) {
	b, _, wsID, _ := newWorkerBackend(t)
	// Default rollup (no args) should not error and should return the
	// expected envelope keys.
	out, isErr := callWorkerTool(t, b, "worker_cost_aggregate", nil)
	if isErr {
		t.Fatalf("worker_cost_aggregate errored: %s", out)
	}
	if !strings.Contains(out, "\"days\"") || !strings.Contains(out, "\"workers\"") {
		t.Fatalf("missing expected fields: %s", out)
	}

	// With a workspace filter, the days knob, and zero workers, the
	// payload still serialises cleanly.
	out, isErr = callWorkerTool(t, b, "worker_cost_aggregate", map[string]any{
		"workspace_id": wsID,
		"days":         7,
	})
	if isErr {
		t.Fatalf("worker_cost_aggregate with args errored: %s", out)
	}
	if !strings.Contains(out, "\"days\": 7") {
		t.Fatalf("days knob not honoured: %s", out)
	}
}

func TestListAvailableToolsMCPDispatch(t *testing.T) {
	b, _, _, _ := newWorkerBackend(t)
	out, isErr := callWorkerTool(t, b, "list_available_tools", nil)
	if isErr {
		t.Fatalf("list_available_tools errored: %s", out)
	}
	// Empty downstream catalogue yields an empty array, not an error.
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("expected JSON array, got %s", out)
	}
}
