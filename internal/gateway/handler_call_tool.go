package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const callToolName = "mcpx__call_tool"

// callToolDefinition declares the single-call path that complements Code Mode.
// It is intentionally narrow: models should use it only after discovery and
// only when no composition, filtering, polling, or transformation is needed.
func callToolDefinition() Tool {
	return Tool{
		Name: callToolName,
		Description: "Invoke exactly one discovered tool and return its MCP result envelope directly. " +
			"Use this only for one small, independent call whose result needs no filtering, aggregation, " +
			"polling, branching, or transformation. First discover the exact tool name and schema with " +
			"mcpx__search_tools. Use mcpx__execute_code instead for multiple calls, dependent calls, " +
			"parallel work, retries/polling, or any result processing. The target traverses the same " +
			"routing, worker/skill gates, admin and scope policy, approvals, downstream dispatch, " +
			"sanitization, compression, and audit pipeline as a Code Mode inner call. This wrapper " +
			"cannot invoke itself and does not enable direct top-level downstream calls.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"name": {
					"type": "string",
					"minLength": 1,
					"description": "Exact canonical tool name returned by mcpx__search_tools, for example task__get or github__list_issues."
				},
				"arguments": {
					"type": "object",
					"description": "Arguments for the target tool, matching its discovered input schema. Pass {} when it takes no arguments."
				}
			},
			"required": ["name", "arguments"]
		}`),
	}
}

type callToolArgs struct {
	Name      string
	Arguments json.RawMessage
}

func decodeCallToolArgs(raw json.RawMessage) (callToolArgs, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return callToolArgs{}, fmt.Errorf("invalid call_tool input: %w", err)
	}
	for key := range fields {
		if key != "name" && key != "arguments" {
			return callToolArgs{}, fmt.Errorf("unknown call_tool field %q", key)
		}
	}

	var args callToolArgs
	if nameRaw, ok := fields["name"]; ok {
		if err := json.Unmarshal(nameRaw, &args.Name); err != nil {
			return callToolArgs{}, fmt.Errorf("name must be a string")
		}
	}
	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return callToolArgs{}, fmt.Errorf(
			"name is required. Example: {\"name\":\"task__get\",\"arguments\":{\"id\":\"...\"}}",
		)
	}

	arguments, ok := fields["arguments"]
	if !ok {
		return callToolArgs{}, fmt.Errorf(
			"arguments is required and must be an object; pass {} when the target takes no arguments",
		)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &object); err != nil || object == nil {
		return callToolArgs{}, fmt.Errorf("arguments must be a JSON object")
	}
	args.Arguments = arguments
	return args, nil
}

func (h *handler) handleCallTool(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := decodeCallToolArgs(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	target := resolveHarnessToolName(args.Name)
	if target == callToolName {
		return nil, &RPCError{
			Code:    CodeInvalidParams,
			Message: "mcpx__call_tool cannot invoke itself; call the discovered target directly through this wrapper",
		}
	}

	// Mark only the target call as trusted internal dispatch. Unlike Code Mode,
	// this does not set the sandbox marker, so model-facing target results still
	// receive the normal compression pass. Every other policy remains in the
	// shared handleToolsCall pipeline.
	return h.callToolThroughPipeline(withInternalToolCall(ctx), target, args.Arguments)
}

// callToolThroughPipeline is the single adapter into handleToolsCall for both
// Code Mode and mcpx__call_tool. It preserves the raw MCP result envelope and
// the RPC error without reimplementing any dispatch policy.
func (h *handler) callToolThroughPipeline(
	ctx context.Context, name string, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	params, err := json.Marshal(CallToolRequest{Name: name, Arguments: args})
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("marshal tool call: %v", err),
		}
	}
	return h.handleToolsCall(ctx, params)
}
