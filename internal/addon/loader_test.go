package addon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testResolver returns a NamespaceResolver that maps known server IDs.
func testResolver(known map[string]string) NamespaceResolver {
	return func(serverID string) (string, error) {
		ns, ok := known[serverID]
		if !ok {
			return "", fmt.Errorf("unknown server %q", serverID)
		}
		return ns, nil
	}
}

func writeYAML(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDir(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		known     map[string]string
		wantTools int
		wantErr   string
	}{
		{
			name: "valid file with two tools",
			yaml: `
parent_server: clickup-server
tools:
  - name: get_chat_messages
    description: Get messages from a ClickUp chat channel
    method: GET
    url: https://api.clickup.com/api/v3/chat/{{channel_id}}/message
    input_schema:
      type: object
      properties:
        channel_id:
          type: string
  - name: send_chat_message
    description: Send a message to a ClickUp chat channel
    method: POST
    url: https://api.clickup.com/api/v3/chat/{{channel_id}}/message
    input_schema:
      type: object
      properties:
        channel_id:
          type: string
        content:
          type: string
`,
			known:     map[string]string{"clickup-server": "clickup"},
			wantTools: 2,
		},
		{
			name: "tool with all optional fields",
			yaml: `
parent_server: clickup-server
tools:
  - name: list_items
    description: List items with filters
    method: GET
    url: https://api.example.com/items
    query_params:
      status: "{{status}}"
      limit: "{{limit}}"
    headers:
      X-Custom: my-value
    body_mapping: none
    input_schema:
      type: object
      properties:
        status:
          type: string
    annotations:
      readOnlyHint: true
`,
			known:     map[string]string{"clickup-server": "clickup"},
			wantTools: 1,
		},
		{
			name: "missing parent_server",
			yaml: `
tools:
  - name: test_tool
    description: A test tool
    method: GET
    url: https://example.com
    input_schema:
      type: object
`,
			known:   map[string]string{},
			wantErr: "parent_server is required",
		},
		{
			name: "unknown parent_server",
			yaml: `
parent_server: nonexistent
tools:
  - name: test_tool
    description: A test tool
    method: GET
    url: https://example.com
    input_schema:
      type: object
`,
			known:   map[string]string{"other": "other"},
			wantErr: "resolve parent_server",
		},
		{
			name: "invalid method",
			yaml: `
parent_server: clickup-server
tools:
  - name: bad_method
    description: Tool with bad method
    method: YEET
    url: https://example.com
    input_schema:
      type: object
`,
			known:   map[string]string{"clickup-server": "clickup"},
			wantErr: "invalid method",
		},
		{
			name: "missing tool name",
			yaml: `
parent_server: clickup-server
tools:
  - description: Tool without a name
    method: GET
    url: https://example.com
    input_schema:
      type: object
`,
			known:   map[string]string{"clickup-server": "clickup"},
			wantErr: "name is required",
		},
		{
			name: "missing input_schema",
			yaml: `
parent_server: clickup-server
tools:
  - name: no_schema
    description: Tool without schema
    method: GET
    url: https://example.com
`,
			known:   map[string]string{"clickup-server": "clickup"},
			wantErr: "input_schema is required",
		},
		{
			name: "input_schema wrong type",
			yaml: `
parent_server: clickup-server
tools:
  - name: bad_schema
    description: Tool with wrong schema type
    method: GET
    url: https://example.com
    input_schema:
      type: array
`,
			known:   map[string]string{"clickup-server": "clickup"},
			wantErr: "input_schema.type must be",
		},
		{
			name: "invalid body_mapping",
			yaml: `
parent_server: clickup-server
tools:
  - name: bad_body
    description: Tool with bad body mapping
    method: POST
    url: https://example.com
    body_mapping: invalid
    input_schema:
      type: object
`,
			known:   map[string]string{"clickup-server": "clickup"},
			wantErr: "invalid body_mapping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeYAML(t, dir, "addon.yaml", tt.yaml)

			reg, err := LoadDir(dir, testResolver(tt.known))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(reg.AllTools()); got != tt.wantTools {
				t.Errorf("got %d tools, want %d", got, tt.wantTools)
			}
		})
	}
}

func TestRegistry_GetTool(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "addon.yaml", `
parent_server: clickup-server
tools:
  - name: get_messages
    description: Get messages
    method: GET
    url: https://api.clickup.com/api/v3/chat/{{channel_id}}/message
    input_schema:
      type: object
      properties:
        channel_id:
          type: string
`)

	reg, err := LoadDir(dir, testResolver(map[string]string{"clickup-server": "clickup"}))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("found", func(t *testing.T) {
		tool := reg.GetTool("clickup__get_messages")
		if tool == nil {
			t.Fatal("expected tool, got nil")
			return
		}
		if tool.FullName != "clickup__get_messages" {
			t.Errorf("FullName = %q, want %q", tool.FullName, "clickup__get_messages")
		}
		if tool.ParentServerID != "clickup-server" {
			t.Errorf("ParentServerID = %q, want %q", tool.ParentServerID, "clickup-server")
		}
		if tool.Namespace != "clickup" {
			t.Errorf("Namespace = %q, want %q", tool.Namespace, "clickup")
		}
	})

	t.Run("not found", func(t *testing.T) {
		tool := reg.GetTool("clickup__nonexistent")
		if tool != nil {
			t.Errorf("expected nil, got %v", tool)
		}
	})
}

func TestRegistry_ToolsForNamespace(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "clickup.yaml", `
parent_server: clickup-server
tools:
  - name: tool_a
    description: Tool A
    method: GET
    url: https://api.clickup.com/a
    input_schema:
      type: object
  - name: tool_b
    description: Tool B
    method: POST
    url: https://api.clickup.com/b
    input_schema:
      type: object
`)
	writeYAML(t, dir, "github.yaml", `
parent_server: github-server
tools:
  - name: tool_c
    description: Tool C
    method: GET
    url: https://api.github.com/c
    input_schema:
      type: object
`)

	reg, err := LoadDir(dir, testResolver(map[string]string{
		"clickup-server": "clickup",
		"github-server":  "github",
	}))
	if err != nil {
		t.Fatal(err)
	}

	clickupTools := reg.ToolsForNamespace("clickup")
	if len(clickupTools) != 2 {
		t.Errorf("clickup tools = %d, want 2", len(clickupTools))
	}

	githubTools := reg.ToolsForNamespace("github")
	if len(githubTools) != 1 {
		t.Errorf("github tools = %d, want 1", len(githubTools))
	}

	emptyTools := reg.ToolsForNamespace("nonexistent")
	if len(emptyTools) != 0 {
		t.Errorf("nonexistent namespace tools = %d, want 0", len(emptyTools))
	}

	allTools := reg.AllTools()
	if len(allTools) != 3 {
		t.Errorf("all tools = %d, want 3", len(allTools))
	}
}

func TestLoadDir_SkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "addon.yaml", `
parent_server: srv
tools:
  - name: tool_a
    description: A
    method: GET
    url: https://example.com
    input_schema:
      type: object
`)
	// Write a non-YAML file that should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := LoadDir(dir, testResolver(map[string]string{"srv": "ns"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reg.AllTools()); got != 1 {
		t.Errorf("got %d tools, want 1", got)
	}
}

func TestLoadDir_DuplicateToolName(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "a.yaml", `
parent_server: srv
tools:
  - name: dupe
    description: First
    method: GET
    url: https://example.com
    input_schema:
      type: object
`)
	writeYAML(t, dir, "b.yaml", `
parent_server: srv
tools:
  - name: dupe
    description: Second
    method: GET
    url: https://example.com
    input_schema:
      type: object
`)

	_, err := LoadDir(dir, testResolver(map[string]string{"srv": "ns"}))
	if err == nil {
		t.Fatal("expected error for duplicate tool name")
	}
	if !contains(err.Error(), "duplicate tool name") {
		t.Errorf("error = %q, want containing 'duplicate tool name'", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
