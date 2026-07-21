package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// ctxCapturingBuiltin records the context the dispatcher hands to the gateway
// so we can assert the worker-scoped browser isolation id was stamped before
// the call crosses into the gateway pipeline.
type ctxCapturingBuiltin struct {
	fakeBuiltin
	gotBrowserSession string
}

func (c *ctxCapturingBuiltin) CallBuiltin(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	c.gotBrowserSession = downstream.BrowserSessionIDFromContext(ctx)
	return c.fakeBuiltin.CallBuiltin(ctx, name, args)
}

// TestDispatcherDispatchTool_StampsWorkerBrowserIsolationID verifies that
// in-process worker calls carry a "worker:<WorkerID>" browser isolation id.
// Workers all share one uninitialized gateway session, so without this they
// would all collapse onto one shared browser instance downstream.
func TestDispatcherDispatchTool_StampsWorkerBrowserIsolationID(t *testing.T) {
	db := newDispatcherTestStore(t)
	bt := &ctxCapturingBuiltin{}
	d := newToolDispatcher(db, nil, nil)
	d.SetBuiltinCaller(bt)

	_, err := d.DispatchTool(context.Background(), runner.ToolCallRequest{
		Name:      "mcpx__execute_code",
		InputJSON: `{"code":"print(1)"}`,
		WorkerID:  "w-123",
	})
	if err != nil {
		t.Fatalf("DispatchTool: %v", err)
	}
	if bt.gotBrowserSession != "worker:w-123" {
		t.Fatalf("browser isolation id = %q, want %q", bt.gotBrowserSession, "worker:w-123")
	}
}
