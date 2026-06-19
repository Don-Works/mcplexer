package downstream

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestListAllTools_SkipsAutoStartUnsafeServers pins the automatic discovery
// contract: ListAllTools must not probe rows that are disabled or backed by
// stdio. Disabled rows generate noisy health failures if touched; stdio rows
// spawn child processes without user intent.
func TestListAllTools_SkipsAutoStartUnsafeServers(t *testing.T) {
	prev := PerServerListToolsTimeout
	t.Cleanup(func() { PerServerListToolsTimeout = prev })
	PerServerListToolsTimeout = 2 * time.Second // /bin/true exits fast; keep the test snappy

	ctx := context.Background()
	m := newTestManager(t)

	registerInternalServer(t, m, "srv-enabled", &fakeInternal{
		delay:  10 * time.Millisecond,
		result: json.RawMessage(`{"tools":[{"name":"ok"}]}`),
	})
	stdio := &store.DownstreamServer{
		ID: "srv-stdio", Name: "stdio", Transport: "stdio",
		Command: "/bin/true", Args: json.RawMessage(`[]`),
		ToolNamespace: "stdio", Discovery: "static", Source: "test",
	}
	disabled := &store.DownstreamServer{
		ID: "srv-disabled", Name: "disabled", Transport: "stdio",
		Command: "/bin/true", Args: json.RawMessage(`[]`),
		ToolNamespace: "disabled", Discovery: "static", Source: "test",
		Disabled: true,
	}
	playwright := &store.DownstreamServer{
		ID: "srv-playwright", Name: "playwright", Transport: "stdio",
		Command: "/bin/true", Args: json.RawMessage(`["-y","@playwright/mcp@latest"]`),
		ToolNamespace: "playwright", Discovery: "static", Source: "test",
	}
	if err := m.store.CreateDownstreamServer(ctx, stdio); err != nil {
		t.Fatalf("create stdio: %v", err)
	}
	if err := m.store.CreateDownstreamServer(ctx, disabled); err != nil {
		t.Fatalf("create disabled: %v", err)
	}
	if err := m.store.CreateDownstreamServer(ctx, playwright); err != nil {
		t.Fatalf("create playwright: %v", err)
	}

	got, err := m.ListAllTools(ctx)
	if err != nil {
		t.Fatalf("ListAllTools: %v", err)
	}
	if _, ok := got["srv-enabled"]; !ok {
		t.Fatalf("enabled internal server was not listed: %v", got)
	}

	// Disabled server must never have been probed -> no failure recorded.
	disSnap := m.Health().Snapshot(disabled.ID, time.Now())
	if !disSnap.LastFailureAt.IsZero() || disSnap.ConsecutiveFailures != 0 {
		t.Fatalf("disabled server was probed: failures=%d lastFailure=%v",
			disSnap.ConsecutiveFailures, disSnap.LastFailureAt)
	}
	stdioSnap := m.Health().Snapshot(stdio.ID, time.Now())
	if !stdioSnap.LastFailureAt.IsZero() || stdioSnap.ConsecutiveFailures != 0 {
		t.Fatalf("stdio server was probed: failures=%d lastFailure=%v",
			stdioSnap.ConsecutiveFailures, stdioSnap.LastFailureAt)
	}
	pwSnap := m.Health().Snapshot(playwright.ID, time.Now())
	if !pwSnap.LastFailureAt.IsZero() || pwSnap.ConsecutiveFailures != 0 {
		t.Fatalf("playwright server was probed: failures=%d lastFailure=%v",
			pwSnap.ConsecutiveFailures, pwSnap.LastFailureAt)
	}
}
