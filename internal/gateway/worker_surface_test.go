package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestWorkerToolSurface_TwoTools asserts the contract: workers see
// exactly mcpx__search_tools + mcpx__execute_code and nothing else.
// The test exercises the public Server method that the wiring layer
// uses to populate the worker tool inventory; if this shape changes
// the worker preamble's promise ("your surface is exactly two tools")
// is broken too.
func TestWorkerToolSurface_TwoTools(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "ws.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := NewServer(db, routing.NewEngine(db), &mockToolLister{}, nil, TransportInternal)

	tools := srv.WorkerToolSurface(context.Background())
	if len(tools) != 2 {
		t.Fatalf("expected exactly 2 tools, got %d", len(tools))
	}
	got := map[string]bool{}
	for _, t := range tools {
		got[t.Name] = true
	}
	for _, want := range []string{"mcpx__search_tools", "mcpx__execute_code"} {
		if !got[want] {
			t.Errorf("worker surface missing %q (have %v)", want, got)
		}
	}
}

// TestWorkerPreamble_Stable locks down the preamble's load-bearing
// invariants: it advertises the two-tool surface, names mcpx__search_tools
// and mcpx__execute_code by their exact builtin names so a model reading
// the preamble can immediately call them, and mentions the memory
// namespace as the persistence story. Wording can drift but these
// promises must hold — the worker's first turn depends on them.
func TestWorkerPreamble_Stable(t *testing.T) {
	got := WorkerPreamble()
	for _, must := range []string{
		"mcpx__search_tools",
		"mcpx__execute_code",
		"memory",
		"mcplexer",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("preamble missing required token %q\n--- preamble ---\n%s", must, got)
		}
	}
}
