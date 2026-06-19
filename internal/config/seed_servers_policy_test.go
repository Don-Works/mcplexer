package config

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestSeedDefault_ExternalServersDisabled asserts that on fresh-DB seeding,
// every external (non-internal) server is created with Disabled=true.
// Internal builtins (transport=internal) remain enabled. Connecting an MCP
// client must not trigger 30+ external processes / OAuth flows on first launch.
func TestSeedDefault_ExternalServersDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := SeedDefaultDownstreamServers(ctx, db); err != nil {
		t.Fatalf("SeedDefaultDownstreamServers: %v", err)
	}

	servers, err := db.ListDownstreamServers(ctx)
	if err != nil {
		t.Fatalf("ListDownstreamServers: %v", err)
	}
	if len(servers) == 0 {
		t.Fatal("no servers seeded")
	}

	// Policy: external (non-internal) servers MUST seed disabled. Internal
	// servers preserve the catalog's Disabled value (most are enabled, but
	// some — e.g. aikido, freeagent — are explicitly Disabled until the
	// user supplies API keys).
	var enabledExternal, anyInternalEnabled int
	for _, srv := range servers {
		if srv.Transport != "internal" && !srv.Disabled {
			enabledExternal++
			t.Errorf("external server %q (transport=%s) seeded enabled — should be disabled by default", srv.ID, srv.Transport)
		}
		if srv.Transport == "internal" && !srv.Disabled {
			anyInternalEnabled++
		}
	}

	if enabledExternal > 0 {
		t.Errorf("%d external servers seeded enabled (expected 0)", enabledExternal)
	}
	if anyInternalEnabled == 0 {
		t.Error("no internal builtins seeded enabled — at least mcpx/mesh/secret should be on")
	}
}

func TestDefaultSlackServerUsesNativeHTTP(t *testing.T) {
	t.Parallel()
	srv, ok := defaultDownstreamServerByID("slack")
	if !ok {
		t.Fatal("missing slack default server")
	}
	if srv.Transport != "http" {
		t.Fatalf("Transport = %q, want http", srv.Transport)
	}
	if srv.URL == nil || *srv.URL != "https://mcp.slack.com/mcp" {
		t.Fatalf("URL = %v, want https://mcp.slack.com/mcp", srv.URL)
	}
	if srv.Command != "" || len(srv.Args) != 0 {
		t.Fatalf("Slack default should not use mcp-remote command=%q args=%s", srv.Command, srv.Args)
	}
}
