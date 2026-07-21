package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/codemode"
	"github.com/don-works/mcplexer/internal/store"
)

func TestMarshalCodeResult_TruncatesLargeOutput(t *testing.T) {
	const limit = 1024
	bigOutput := strings.Repeat("x", limit+1000)

	result := &codemode.ExecutionResult{
		Output:         bigOutput,
		OutputMaxBytes: limit,
	}

	raw := marshalCodeResult(result)

	var callResult CallToolResult
	if err := json.Unmarshal(raw, &callResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(callResult.Content) == 0 {
		t.Fatal("expected at least one content block")
	}

	text := callResult.Content[0].Text
	if !strings.Contains(text, "[truncated") {
		t.Error("expected truncation message in output")
	}
	if !strings.Contains(text, "code-mode output exceeded 1 KiB") {
		t.Errorf("expected configured-limit marker, got %q", text)
	}
	if len(text) > limit+250 {
		t.Errorf("output too large after truncation: %d bytes", len(text))
	}
}

func TestMarshalCodeResult_SmallOutputUnchanged(t *testing.T) {
	result := &codemode.ExecutionResult{
		Output: "hello world",
	}

	raw := marshalCodeResult(result)

	var callResult CallToolResult
	if err := json.Unmarshal(raw, &callResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if callResult.Content[0].Text != "hello world" {
		t.Errorf("expected unchanged output, got %q", callResult.Content[0].Text)
	}
}

func TestMarshalCodeResult_TruncatesErrorAndFailedSummary(t *testing.T) {
	const limit = 1024
	result := &codemode.ExecutionResult{
		Error:          strings.Repeat("e", limit+100),
		OutputMaxBytes: limit,
		ToolCalls: []codemode.ToolCallRecord{{
			Name:  "svc__fail",
			Args:  json.RawMessage(`{"q":"x"}`),
			Error: strings.Repeat("f", limit+100),
		}},
	}

	raw := marshalCodeResult(result)

	var callResult CallToolResult
	if err := json.Unmarshal(raw, &callResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	joined := callResult.Content[0].Text + "\n" + callResult.Content[1].Text
	if !strings.Contains(joined, "code-mode error exceeded 1 KiB") {
		t.Fatalf("missing execution error truncation marker: %q", joined)
	}
	if !strings.Contains(joined, "failed tool error exceeded 1 KiB") {
		t.Fatalf("missing failed-call truncation marker: %q", joined)
	}
	if strings.Contains(joined, strings.Repeat("e", limit+1)) {
		t.Fatal("execution error was not capped")
	}
	if strings.Contains(joined, strings.Repeat("f", limit+1)) {
		t.Fatal("failed-call error was not capped")
	}
}

func TestMarshalCodeResult_SuccessSummaryOmitsResultPayload(t *testing.T) {
	result := &codemode.ExecutionResult{
		ToolCalls: []codemode.ToolCallRecord{{
			Name:     "svc__large",
			Result:   json.RawMessage(`{"content":[{"type":"text","text":"secret-payload"}]}`),
			Duration: 3,
		}},
	}

	raw := marshalCodeResult(result)

	var callResult CallToolResult
	if err := json.Unmarshal(raw, &callResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	text := callResult.Content[0].Text
	if strings.Contains(text, "secret-payload") {
		t.Fatalf("successful result leaked into summary: %q", text)
	}
	if !strings.Contains(text, "svc__large (ok") {
		t.Fatalf("summary missing successful call line: %q", text)
	}
}

// TestHandleCodeExecute_LintWarnsOnTypo asserts the gateway pipeline
// runs LintWithTools BEFORE handing the code to Goja, so a typo'd
// namespace surfaces an actionable lint warning even when the script
// would have died with a bare ReferenceError at runtime. This is the
// glue check between handler_codemode.go and codemode.LintWithTools.
func TestHandleCodeExecute_LintWarnsOnTypo(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{
				Name:        "create_issue",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
			}),
		},
	}
	h, _ := newTestHandler(lister, []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	})

	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code": "gihub.create_issue({title:'x'});"}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %s", rpcErr.Message)
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	joined := joinContent(tr.Content)
	if !strings.Contains(joined, "Lint warnings") {
		t.Fatalf("expected lint section in output, got:\n%s", joined)
	}
	if !strings.Contains(joined, "github") || !strings.Contains(joined, "Did you mean") {
		t.Fatalf("expected did-you-mean github in lint output, got:\n%s", joined)
	}
}

func TestHandleCodeExecute_PreflightSyntaxStopsBeforeTools(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{
				Name:        "create_issue",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
			}),
		},
	}
	h, _ := newTestHandler(lister, []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	})

	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code": "github.create_issue({title:'bug'); print('ok');"}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %s", rpcErr.Message)
	}
	if lister.callCount != 0 {
		t.Fatalf("preflight must stop before downstream calls, got %d", lister.callCount)
	}
	if len(lister.listRequests) != 0 {
		t.Fatalf("preflight must stop before tool discovery, got %d requests", len(lister.listRequests))
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !tr.IsError {
		t.Fatal("expected execute_code error result")
	}
	joined := joinContent(tr.Content)
	if !strings.Contains(joined, "preflight failed before execution") ||
		!strings.Contains(joined, "no tool calls were dispatched") ||
		!strings.Contains(joined, "syntax error") {
		t.Fatalf("missing preflight syntax details:\n%s", joined)
	}
	if strings.Contains(joined, "tool call(s) executed") {
		t.Fatalf("preflight result should not report inner tool calls:\n%s", joined)
	}
}

func TestHandleCodeExecute_PreflightPolicyStopsBeforeTools(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{
				Name:        "create_issue",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
			}),
		},
	}
	h, _ := newTestHandler(lister, []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	})

	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code": "eval(\"github.create_issue({title:'bug'})\");"}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %s", rpcErr.Message)
	}
	if lister.callCount != 0 {
		t.Fatalf("preflight must stop before downstream calls, got %d", lister.callCount)
	}
	if len(lister.listRequests) != 0 {
		t.Fatalf("preflight must stop before tool discovery, got %d requests", len(lister.listRequests))
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	joined := joinContent(tr.Content)
	if !tr.IsError ||
		!strings.Contains(joined, "eval is not allowed in code mode") ||
		!strings.Contains(joined, "no tool calls were dispatched") {
		t.Fatalf("missing preflight policy details:\n%s", joined)
	}
}

// joinContent concatenates every text block in an MCP CallToolResult.
// Lives in the test file to keep the helper close to its only use site.
func joinContent(content []ToolContent) string {
	var b strings.Builder
	for _, c := range content {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// TestExecuteCodeToolDescription_AccurateErrorContract is the truth-fix
// regression: every claim in mcpx__execute_code's description has to
// match runtime behaviour. The old description told agents that typos
// throw "Tool call failed" — they actually throw ReferenceError. Pin
// the new wording so a future drive-by re-introducing the lie fails CI.
func TestExecuteCodeToolDescription_AccurateErrorContract(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	tool, _ := h.buildCodeExecuteTool(context.Background())

	checks := []struct {
		name string
		want string
	}{
		{name: "mentions ReferenceError", want: "ReferenceError"},
		{name: "mentions did-you-mean", want: "did-you-mean"},
		{name: "parallel does NOT throw", want: "does NOT throw"},
		{name: "sleep clamped to 60s", want: "clamped to 60s"},
		{name: "wall-clock timeout", want: "wall-clock timeout"},
		{name: "no per-call max output bytes arg", want: "capped server-side"},
	}
	for _, c := range checks {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(tool.Description, c.want) {
				t.Fatalf("description missing %q\n---\n%s", c.want, tool.Description)
			}
		})
	}

	// Pin negative: the InputSchema description must NOT misrepresent
	// `code_mode_max_output_bytes` as a per-call argument.
	if strings.Contains(string(tool.InputSchema), `"code_mode_max_output_bytes"`) {
		t.Fatalf("InputSchema must not advertise code_mode_max_output_bytes as an argument:\n%s",
			string(tool.InputSchema))
	}
}
