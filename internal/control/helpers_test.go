package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("new test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedServer creates a downstream server with default values and returns it.
func seedServer(t *testing.T, db *sqlite.DB) *store.DownstreamServer {
	t.Helper()
	return seedServerNamed(t, db, "test-server", "echo", "test")
}

// seedServerNamed creates a downstream server with the given name, command, and namespace.
func seedServerNamed(t *testing.T, db *sqlite.DB, name, cmd, ns string) *store.DownstreamServer {
	t.Helper()
	srv := &store.DownstreamServer{
		Name:          name,
		Transport:     "stdio",
		Command:       cmd,
		ToolNamespace: ns,
		RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return srv
}

// seedWorkspace creates a workspace with default values and returns it.
func seedWorkspace(t *testing.T, db *sqlite.DB) *store.Workspace {
	t.Helper()
	ws := &store.Workspace{
		Name:          "test-ws",
		RootPath:      "/tmp/test",
		DefaultPolicy: "allow",
	}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return ws
}

// parseToolResult extracts text and isError from a handler's raw CallToolResult JSON.
func parseToolResult(t *testing.T, data json.RawMessage) (string, bool) {
	t.Helper()
	var result gateway.CallToolResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse tool result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("empty tool result content")
	}
	return result.Content[0].Text, result.IsError
}

func writeReq(buf *bytes.Buffer, id int, method string, params any) {
	req := gateway.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf("%d", id)),
		Method:  method,
	}
	if params != nil {
		req.Params, _ = json.Marshal(params)
	}
	data, _ := json.Marshal(req)
	buf.Write(append(data, '\n'))
}

func writeNotification(buf *bytes.Buffer, method string) {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	data, _ := json.Marshal(req)
	buf.Write(append(data, '\n'))
}

func readResponses(t *testing.T, data []byte) []gateway.Response {
	t.Helper()
	var responses []gateway.Response
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var resp gateway.Response
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		responses = append(responses, resp)
	}
	return responses
}
