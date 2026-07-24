package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestWorkerToolSurface_ThreeTools asserts the contract: workers see
// exactly the discovery tool plus the single-call and Code Mode wrappers.
// The test exercises the public Server method that the wiring layer
// uses to populate the worker tool inventory; if this shape changes
// the worker preamble's promise ("your surface is exactly three tools")
// is broken too.
func TestWorkerToolSurface_ThreeTools(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "ws.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := NewServer(db, routing.NewEngine(db), &mockToolLister{}, nil, TransportInternal)

	tools := srv.WorkerToolSurface(context.Background())
	if len(tools) != 3 {
		t.Fatalf("expected exactly 3 tools, got %d", len(tools))
	}
	got := map[string]bool{}
	for _, t := range tools {
		got[t.Name] = true
	}
	for _, want := range []string{"mcpx__search_tools", "mcpx__call_tool", "mcpx__execute_code"} {
		if !got[want] {
			t.Errorf("worker surface missing %q (have %v)", want, got)
		}
	}
}

// TestWorkerPreamble_Stable locks down the preamble's load-bearing
// invariants: it advertises the three-tool surface and names every entrypoint
// by its exact builtin name so a model reading
// the preamble can immediately call them, mentions the memory namespace as
// the persistence story, and — the coordination promises the parent relies
// on — tells the worker to hold its scope and to escalate a genuine block
// rather than loop. Wording can drift but these promises must hold — the
// worker's first turn (and the parent's trust in the result) depends on them.
func TestWorkerPreamble_Stable(t *testing.T) {
	got := WorkerPreamble()
	for _, must := range []string{
		"mcpx__search_tools",
		"mcpx__call_tool",
		"mcpx__execute_code",
		"memory",
		"mcplexer",
		"`brw`/browser tools first",
		"browser skill",
		"scope",   // scope discipline: work to the brief, don't gold-plate
		"blocked", // ambiguity escape hatch: proceed on assumption, escalate only when blocked
	} {
		if !strings.Contains(got, must) {
			t.Errorf("preamble missing required token %q\n--- preamble ---\n%s", must, got)
		}
	}
}

// TestWorkerPreambleCLI_AccurateAndShorter locks the CLI variant's two
// reasons for existing. Accuracy: a CLI-backed adapter runs its own agent
// loop and discards the runner's tool list, so the variant must NOT repeat
// the API preamble's exact-surface claim — it would be a factually wrong instruction the
// worker pays real tokens to read. Cost: it must be materially shorter,
// since that is the entire point on a 100k-window local model.
func TestWorkerPreambleCLI_AccurateAndShorter(t *testing.T) {
	cli := WorkerPreambleCLI()

	// The false claim must be gone.
	for _, mustNot := range []string{"mcpx__search_tools", "mcpx__call_tool", "mcpx__execute_code", "exactly three tools"} {
		if strings.Contains(cli, mustNot) {
			t.Errorf("CLI preamble repeats the API-only claim %q\n--- preamble ---\n%s", mustNot, cli)
		}
	}
	// What remains has to still orient the worker: it keeps its own native
	// tools, holds its scope, and escalates a genuine block instead of looping.
	for _, must := range []string{"mcplexer", "memory", "native tools", "scope", "blocked"} {
		if !strings.Contains(cli, must) {
			t.Errorf("CLI preamble missing required token %q\n--- preamble ---\n%s", must, cli)
		}
	}
	if full := WorkerPreamble(); len(cli) >= len(full) {
		t.Errorf("CLI preamble (%d B) must be shorter than the API preamble (%d B)", len(cli), len(full))
	}
}
