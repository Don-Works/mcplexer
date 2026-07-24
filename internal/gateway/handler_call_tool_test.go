package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
)

func callToolRequest(t *testing.T, name string, arguments json.RawMessage) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(CallToolRequest{
		Name: callToolName,
		Arguments: mustJSON(t, map[string]any{
			"name":      name,
			"arguments": arguments,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCallToolDefinitionIsPreciseAndObjectOnly(t *testing.T) {
	def := callToolDefinition()
	if def.Name != callToolName {
		t.Fatalf("name = %q, want %q", def.Name, callToolName)
	}
	for _, guidance := range []string{
		"one small, independent call",
		"mcpx__search_tools",
		"mcpx__execute_code",
		"filtering",
		"polling",
		"cannot invoke itself",
	} {
		if !strings.Contains(def.Description, guidance) {
			t.Errorf("description missing %q", guidance)
		}
	}

	var schema struct {
		AdditionalProperties bool `json:"additionalProperties"`
		Properties           map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties {
		t.Error("call_tool schema must reject unknown wrapper fields")
	}
	if schema.Properties["name"].Type != "string" {
		t.Errorf("name type = %q, want string", schema.Properties["name"].Type)
	}
	if schema.Properties["arguments"].Type != "object" {
		t.Errorf("arguments type = %q, want object", schema.Properties["arguments"].Type)
	}
	if len(schema.Required) != 2 {
		t.Fatalf("required = %v, want name + arguments", schema.Required)
	}
}

func TestDecodeCallToolArgsRejectsAmbiguousInputs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"missing name", `{"arguments":{}}`, "name is required"},
		{"blank name", `{"name":"  ","arguments":{}}`, "name is required"},
		{"missing arguments", `{"name":"task__get"}`, "arguments is required"},
		{"null arguments", `{"name":"task__get","arguments":null}`, "arguments must be a JSON object"},
		{"array arguments", `{"name":"task__get","arguments":[]}`, "arguments must be a JSON object"},
		{"unknown wrapper field", `{"name":"task__get","arguments":{},"args":{}}`, "unknown call_tool field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeCallToolArgs(json.RawMessage(tc.raw)); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("decode error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandleToolsCall_CallToolPreservesTargetErrorEnvelope(t *testing.T) {
	targetEnvelope := json.RawMessage(
		`{"content":[{"type":"text","text":"target rejected the request"}],"isError":true}`,
	)
	h, lister := newArrayResultHandler(t, targetEnvelope)

	result, rpcErr := h.handleToolsCall(
		context.Background(),
		callToolRequest(t, "profiles__list", json.RawMessage(`{"limit":1}`)),
	)
	if rpcErr != nil {
		t.Fatalf("call_tool: %v", rpcErr)
	}
	if lister.callCount != 1 {
		t.Fatalf("target calls = %d, want 1", lister.callCount)
	}

	var envelope CallToolResult
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatalf("target result is not an MCP envelope: %v\n%s", err, result)
	}
	if !envelope.IsError {
		t.Fatalf("target isError was lost: %s", result)
	}
	if len(envelope.Content) != 1 || envelope.Content[0].Text != "target rejected the request" {
		t.Fatalf("target content was rewrapped or altered: %+v", envelope.Content)
	}
}

func TestHandleToolsCall_CallToolRejectsRecursiveAliases(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	for _, target := range []string{
		"mcpx__call_tool",
		"call_tool",
		"mcplexer__call_tool",
		"mcplexer__mcpx__call_tool",
	} {
		t.Run(target, func(t *testing.T) {
			_, rpcErr := h.handleToolsCall(
				context.Background(),
				callToolRequest(t, target, json.RawMessage(`{}`)),
			)
			if rpcErr == nil || rpcErr.Code != CodeInvalidParams {
				t.Fatalf("recursive target error = %+v, want invalid params", rpcErr)
			}
			if !strings.Contains(rpcErr.Message, "cannot invoke itself") {
				t.Fatalf("recursive target error = %q", rpcErr.Message)
			}
		})
	}
}

func TestHandleToolsCall_CallToolDoesNotEnableDirectTargetCalls(t *testing.T) {
	h, lister := newArrayResultHandler(t, json.RawMessage(
		`{"content":[{"type":"text","text":"unexpected"}],"isError":false}`,
	))
	params, err := json.Marshal(CallToolRequest{
		Name:      "profiles__list",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("direct block should be a tool error envelope: %v", rpcErr)
	}
	if lister.callCount != 0 {
		t.Fatalf("direct hidden target bypassed wrapper: calls=%d", lister.callCount)
	}
	var envelope CallToolResult
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.IsError || len(envelope.Content) == 0 ||
		!strings.Contains(envelope.Content[0].Text, "mcpx__call_tool") {
		t.Fatalf("missing direct-call recovery guidance: %+v", envelope)
	}
}

func TestHandleToolsCall_CallToolRegatesWorkerTarget(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "allowlist",
			ctx:  WithWorkerToolAllowlist(context.Background(), []string{"task__*"}),
			want: "not in worker allowlist",
		},
		{
			name: "capability",
			ctx:  WithWorkerCapabilityProfile(context.Background(), toolgate.Minimal()),
			want: "capability profile",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, lister := newArrayResultHandler(t, json.RawMessage(
				`{"content":[{"type":"text","text":"must not run"}],"isError":false}`,
			))
			_, rpcErr := h.handleToolsCall(
				tc.ctx,
				callToolRequest(t, "profiles__list", json.RawMessage(`{}`)),
			)
			if rpcErr == nil || !strings.Contains(rpcErr.Message, tc.want) {
				t.Fatalf("target gate error = %+v, want %q", rpcErr, tc.want)
			}
			if lister.callCount != 0 {
				t.Fatalf("denied target executed %d times", lister.callCount)
			}
		})
	}
}

func TestHandleToolsCall_CallToolRegatesSkillTarget(t *testing.T) {
	h, lister := newArrayResultHandler(t, json.RawMessage(
		`{"content":[{"type":"text","text":"must not run"}],"isError":false}`,
	))
	ctx := withSkillID(
		withSkillAllowlist(context.Background(), []string{"memory"}),
		"test-skill",
	)
	_, rpcErr := h.handleToolsCall(
		ctx,
		callToolRequest(t, "profiles__list", json.RawMessage(`{}`)),
	)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "skill") {
		t.Fatalf("skill target gate error = %+v", rpcErr)
	}
	if lister.callCount != 0 {
		t.Fatalf("skill-denied target executed %d times", lister.callCount)
	}
}

func TestCallToolAuditCorrelatesWrapperAndTarget(t *testing.T) {
	h, ms := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.auditor = audit.NewLogger(ms, noopScopeStore{}, nil)

	if _, rpcErr := h.handleToolsCall(
		context.Background(),
		callToolRequest(t, "mcpx__whoami", json.RawMessage(`{}`)),
	); rpcErr != nil {
		t.Fatalf("call_tool: %v", rpcErr)
	}

	var wrapper, target *store.AuditRecord
	for i := range ms.auditRecords {
		record := &ms.auditRecords[i]
		switch record.ToolName {
		case callToolName:
			wrapper = record
		case "mcpx__whoami":
			target = record
		}
	}
	if wrapper == nil || target == nil {
		t.Fatalf("missing wrapper/target audit rows: %+v", ms.auditRecords)
	}
	if wrapper.ExecutionID == "" || wrapper.ExecutionID != target.ExecutionID {
		t.Fatalf("execution ids do not correlate: wrapper=%q target=%q",
			wrapper.ExecutionID, target.ExecutionID)
	}
	if wrapper.CorrelationID != wrapper.ExecutionID ||
		target.CorrelationID != target.ExecutionID {
		t.Fatalf("correlation ids do not mirror execution ids: wrapper=%+v target=%+v",
			wrapper, target)
	}
}
