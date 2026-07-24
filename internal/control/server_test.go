package control

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/mcpversion"
	"github.com/don-works/mcplexer/internal/store"
)

func TestServerInitializeNegotiatesProtocolVersion(t *testing.T) {
	tests := make(map[string]string)
	for _, version := range mcpversion.Supported() {
		tests[version] = version
	}
	tests[""] = mcpversion.Latest
	tests["2026-01-01"] = mcpversion.Latest
	tests["DRAFT-2026-v1"] = mcpversion.Latest

	for proposed, want := range tests {
		t.Run("proposed_"+proposed, func(t *testing.T) {
			srv := New(newTestDB(t))
			var in, out bytes.Buffer

			writeReq(&in, 1, "initialize", map[string]any{
				"protocolVersion": proposed,
				"capabilities": map[string]any{
					"futureFeature": map[string]any{"enabled": true},
				},
				"clientInfo": map[string]any{"name": "test", "version": "1.0"},
			})

			if err := srv.run(context.Background(), &in, &out); err != nil {
				t.Fatal(err)
			}

			responses := readResponses(t, out.Bytes())
			if len(responses) != 1 {
				t.Fatalf("got %d responses, want 1", len(responses))
			}

			resp := responses[0]
			if resp.Error != nil {
				t.Fatalf("unexpected error: %s", resp.Error.Message)
			}

			var result gateway.InitializeResult
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.ServerInfo.Name != "mcplexer-control" {
				t.Fatalf("server name = %q", result.ServerInfo.Name)
			}
			if result.ProtocolVersion != want {
				t.Fatalf("protocol version = %q, want %q", result.ProtocolVersion, want)
			}
		})
	}
}

func TestServerInitializeRejectsMalformedCapabilities(t *testing.T) {
	srv := New(newTestDB(t))
	var in, out bytes.Buffer
	writeReq(&in, 1, "initialize", map[string]any{
		"protocolVersion": mcpversion.Latest,
		"capabilities":    []string{"not", "an", "object"},
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	})

	if err := srv.run(context.Background(), &in, &out); err != nil {
		t.Fatal(err)
	}
	resp := readResponses(t, out.Bytes())[0]
	if resp.Error == nil || resp.Error.Code != gateway.CodeInvalidParams {
		t.Fatalf(
			"malformed initialize error = %#v, want code %d",
			resp.Error,
			gateway.CodeInvalidParams,
		)
	}
}

func TestServerPing(t *testing.T) {
	srv := New(newTestDB(t))
	var in, out bytes.Buffer

	writeReq(&in, 1, "ping", nil)

	if err := srv.run(context.Background(), &in, &out); err != nil {
		t.Fatal(err)
	}

	responses := readResponses(t, out.Bytes())
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	if responses[0].Error != nil {
		t.Fatalf("unexpected error: %s", responses[0].Error.Message)
	}
}

func TestServerToolsList(t *testing.T) {
	t.Run("read-write", func(t *testing.T) {
		srv := New(newTestDB(t), false)
		var in, out bytes.Buffer
		writeReq(&in, 1, "tools/list", nil)

		if err := srv.run(context.Background(), &in, &out); err != nil {
			t.Fatal(err)
		}

		var result struct {
			Tools []gateway.Tool `json:"tools"`
		}
		if err := json.Unmarshal(readResponses(t, out.Bytes())[0].Result, &result); err != nil {
			t.Fatal(err)
		}
		// The standalone Server advertises ONLY the tools it can actually
		// dispatch via the `handlers` map (handleToolsCall). Tools that the
		// InternalBackend dispatches with extra deps (workers, backup,
		// brain, oauth, skill imports, memory import, spawn_subagent) are
		// undispatchable here, so they must NOT appear in tools/list. The
		// expected count is therefore the size of the `handlers` map, derived
		// here so it can't silently drift from the map.
		if len(result.Tools) != len(handlers) {
			t.Fatalf("got %d tools, want %d (all of handlers map)", len(result.Tools), len(handlers))
		}

		names := make(map[string]bool)
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}
		for _, want := range []string{"list_servers", "status", "create_server", "query_audit"} {
			if !names[want] {
				t.Errorf("missing tool: %s", want)
			}
		}
		// Tools dispatched only by InternalBackend must NOT be advertised by
		// the standalone Server — they are undispatchable here.
		for _, undispatchable := range []string{
			"list_workers", "create_worker", "run_worker_now", "worker_cost_aggregate",
			"create_backup", "list_backups", "restore_backup", "delete_backup",
			"brain_push", "brain_status", "brain_init", "brain_migrate_secrets",
			"create_oauth_provider", "import_skill_registry_dir", "import_skill_registry_git",
			"memory_import_claude_cli", "spawn_subagent",
		} {
			if names[undispatchable] {
				t.Errorf("undispatchable tool %q should not be advertised by standalone Server", undispatchable)
			}
		}
	})

	t.Run("read-only", func(t *testing.T) {
		srv := New(newTestDB(t)) // default: read-only
		var in, out bytes.Buffer
		writeReq(&in, 1, "tools/list", nil)

		if err := srv.run(context.Background(), &in, &out); err != nil {
			t.Fatal(err)
		}

		var result struct {
			Tools []gateway.Tool `json:"tools"`
		}
		if err := json.Unmarshal(readResponses(t, out.Bytes())[0].Result, &result); err != nil {
			t.Fatal(err)
		}
		// In read-only mode the standalone Server advertises the dispatchable
		// (handlers map) tools MINUS the admin-gated ones. Derive the expected
		// count the same way the server does so it can't drift.
		wantReadOnly := 0
		for name := range handlers {
			if !isAdminTool(name) {
				wantReadOnly++
			}
		}
		if len(result.Tools) != wantReadOnly {
			t.Fatalf("got %d tools, want %d (read-only dispatchable)", len(result.Tools), wantReadOnly)
		}

		// Admin tools should be absent.
		names := make(map[string]bool)
		for _, tool := range result.Tools {
			names[tool.Name] = true
		}
		if names["create_server"] {
			t.Error("create_server should not be listed in read-only mode")
		}
	})
}

// TestServerAdvertisedToolsAreDispatchable codifies the tools/list-vs-
// tools/call contract for the standalone Server (issue #2): every tool the
// Server advertises in tools/list MUST be dispatchable via tools/call — it
// must NOT come back as CodeMethodNotFound 'unknown tool'. This is the
// regression guard for the server.go gap where ~40 advertised worker/backup/
// brain/oauth tools were undispatchable because handleToolsCall only consults
// the `handlers` map while tools/list advertised the full allTools() set.
func TestServerAdvertisedToolsAreDispatchable(t *testing.T) {
	for _, ro := range []struct {
		name     string
		readOnly bool
	}{
		{"read-write", false},
		{"read-only", true},
	} {
		ro := ro
		t.Run(ro.name, func(t *testing.T) {
			srv := New(newTestDB(t), ro.readOnly)

			// 1. Pull the advertised tool list.
			var listIn, listOut bytes.Buffer
			writeReq(&listIn, 1, "tools/list", nil)
			if err := srv.run(context.Background(), &listIn, &listOut); err != nil {
				t.Fatal(err)
			}
			var listResult struct {
				Tools []gateway.Tool `json:"tools"`
			}
			if err := json.Unmarshal(readResponses(t, listOut.Bytes())[0].Result, &listResult); err != nil {
				t.Fatal(err)
			}
			if len(listResult.Tools) == 0 {
				t.Fatal("tools/list returned no tools")
			}

			// 2. Call every advertised tool with empty args and assert the
			//    response is NOT CodeMethodNotFound 'unknown tool'. We don't
			//    care whether the call succeeds (most fail on missing/invalid
			//    args — surfaced as an isError tool result, NOT an RPC error);
			//    we only assert the tool is reachable by the dispatcher.
			for _, tool := range listResult.Tools {
				tool := tool
				t.Run(tool.Name, func(t *testing.T) {
					var callIn, callOut bytes.Buffer
					writeReq(&callIn, 1, "tools/call", map[string]any{
						"name":      tool.Name,
						"arguments": map[string]any{},
					})
					if err := srv.run(context.Background(), &callIn, &callOut); err != nil {
						t.Fatal(err)
					}
					resp := readResponses(t, callOut.Bytes())[0]
					if resp.Error != nil && resp.Error.Code == gateway.CodeMethodNotFound {
						t.Fatalf("advertised tool %q is undispatchable: %s",
							tool.Name, resp.Error.Message)
					}
				})
			}
		})
	}
}

func TestServerToolsCall_ListServers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedServer(t, db)

	srv := New(db)
	var in, out bytes.Buffer
	writeReq(&in, 1, "tools/call", map[string]any{"name": "list_servers"})

	if err := srv.run(ctx, &in, &out); err != nil {
		t.Fatal(err)
	}

	resp := readResponses(t, out.Bytes())[0]
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %s", resp.Error.Message)
	}

	text, isErr := parseToolResult(t, resp.Result)
	if isErr {
		t.Fatalf("tool error: %s", text)
	}

	var servers []store.DownstreamServer
	if err := json.Unmarshal([]byte(text), &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if servers[0].Name != "test-server" {
		t.Fatalf("server name = %q, want %q", servers[0].Name, "test-server")
	}
}

func TestServerToolsCall_Status(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedServer(t, db)

	srv := New(db)
	var in, out bytes.Buffer
	writeReq(&in, 1, "tools/call", map[string]any{"name": "status"})

	if err := srv.run(ctx, &in, &out); err != nil {
		t.Fatal(err)
	}

	resp := readResponses(t, out.Bytes())[0]
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %s", resp.Error.Message)
	}

	text, isErr := parseToolResult(t, resp.Result)
	if isErr {
		t.Fatalf("tool error: %s", text)
	}

	var status map[string]int
	if err := json.Unmarshal([]byte(text), &status); err != nil {
		t.Fatal(err)
	}
	if status["downstream_servers"] != 1 {
		t.Fatalf("downstream_servers = %d, want 1", status["downstream_servers"])
	}
	if status["workspaces"] != 1 {
		t.Fatalf("workspaces = %d, want 1", status["workspaces"])
	}
}

func TestServerToolsCall_CreateServer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	srv := New(db, false) // read-write mode

	var in, out bytes.Buffer
	writeReq(&in, 1, "tools/call", map[string]any{
		"name": "create_server",
		"arguments": map[string]any{
			"name":           "new-server",
			"command":        "python",
			"tool_namespace": "py",
		},
	})

	if err := srv.run(ctx, &in, &out); err != nil {
		t.Fatal(err)
	}

	resp := readResponses(t, out.Bytes())[0]
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %s", resp.Error.Message)
	}

	text, isErr := parseToolResult(t, resp.Result)
	if isErr {
		t.Fatalf("tool error: %s", text)
	}

	// Verify server was created in DB.
	servers, err := db.ListDownstreamServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if servers[0].Name != "new-server" {
		t.Fatalf("name = %q, want %q", servers[0].Name, "new-server")
	}
}

func TestServerNotification(t *testing.T) {
	srv := New(newTestDB(t))
	var in, out bytes.Buffer

	writeNotification(&in, "notifications/initialized")
	writeReq(&in, 1, "ping", nil)

	if err := srv.run(context.Background(), &in, &out); err != nil {
		t.Fatal(err)
	}

	responses := readResponses(t, out.Bytes())
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1 (notification should not produce response)", len(responses))
	}
}

func TestServerUnknownMethod(t *testing.T) {
	srv := New(newTestDB(t))
	var in, out bytes.Buffer

	writeReq(&in, 1, "unknown/method", nil)

	if err := srv.run(context.Background(), &in, &out); err != nil {
		t.Fatal(err)
	}

	responses := readResponses(t, out.Bytes())
	if len(responses) != 1 {
		t.Fatalf("got %d responses", len(responses))
	}
	if responses[0].Error == nil {
		t.Fatal("expected error response")
	}
	if responses[0].Error.Code != gateway.CodeMethodNotFound {
		t.Fatalf("error code = %d, want %d", responses[0].Error.Code, gateway.CodeMethodNotFound)
	}
}

func TestServerInvalidJSON(t *testing.T) {
	srv := New(newTestDB(t))
	var out bytes.Buffer
	in := bytes.NewBufferString("this is not json\n")

	if err := srv.run(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}

	responses := readResponses(t, out.Bytes())
	if len(responses) != 1 {
		t.Fatalf("got %d responses", len(responses))
	}
	if responses[0].Error == nil {
		t.Fatal("expected parse error")
	}
	if responses[0].Error.Code != gateway.CodeParseError {
		t.Fatalf("error code = %d, want %d", responses[0].Error.Code, gateway.CodeParseError)
	}
}

func TestServerUnknownTool(t *testing.T) {
	srv := New(newTestDB(t))
	var in, out bytes.Buffer

	writeReq(&in, 1, "tools/call", map[string]any{"name": "nonexistent_tool"})

	if err := srv.run(context.Background(), &in, &out); err != nil {
		t.Fatal(err)
	}

	responses := readResponses(t, out.Bytes())
	if responses[0].Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if responses[0].Error.Code != gateway.CodeMethodNotFound {
		t.Fatalf("error code = %d", responses[0].Error.Code)
	}
}
