package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const callToolName = "mcpx__call_tool"

// callToolLargeResultBytes is the size at which a direct call_tool result
// gets a soft advisory. Below this, the whole envelope is fine as model
// context; above it, Code Mode + print-filter almost always wins.
const callToolLargeResultBytes = 12 * 1024

// callToolDefinition declares the single-call path that complements Code Mode.
// It is intentionally narrow: models should use it only after discovery and
// only when no composition, filtering, polling, or transformation is needed.
func callToolDefinition() Tool {
	return Tool{
		Name: callToolName,
		Description: "Invoke exactly one discovered tool and return its MCP result envelope directly. " +
			"Use this ONLY when the entire result is the answer — no filtering, field-picking, aggregation, " +
			"polling, branching, or transformation. " +
			"Rule of thumb: if you would map/filter/pick fields on the result, use mcpx__execute_code and print only what you need (call_tool has no print()). " +
			"Good: index__status, memory__save, task__create/update (compact post-write), mesh__receive with low max_results, skill_search with limit. " +
			"Prefer execute_code for hydrates (task__get notes/history unless you need every note — use task__get with full:true only then), " +
			"list→get chains, multi-call batches, parallel fan-out, or any large body you will not quote in full. " +
			"First discover the exact tool name and schema with mcpx__search_tools. " +
			"The target traverses the same routing, worker/skill gates, admin and scope policy, approvals, downstream dispatch, " +
			"sanitization, compression, and audit pipeline as a Code Mode inner call. " +
			"This wrapper cannot invoke itself or mcpx__execute_code and does not enable direct top-level downstream calls.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"name": {
					"type": "string",
					"minLength": 1,
					"description": "Exact canonical tool name returned by mcpx__search_tools, for example index__status or memory__save. Prefer small independent tools; for hydrates (task__get) and lists you will filter, use mcpx__execute_code instead."
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
			"name is required. Example: {\"name\":\"index__status\",\"arguments\":{}}",
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
	if target == codeExecuteToolName {
		return nil, &RPCError{
			Code:    CodeInvalidParams,
			Message: "mcpx__call_tool cannot invoke mcpx__execute_code; call mcpx__execute_code directly at the top level",
		}
	}

	// Mark only the target call as trusted internal dispatch. Unlike Code Mode,
	// this does not set the sandbox marker, so model-facing target results still
	// receive the normal compression pass. Every other policy remains in the
	// shared handleToolsCall pipeline.
	result, rpcErr := h.callToolThroughPipeline(withInternalToolCall(ctx), target, args.Arguments)
	if rpcErr != nil {
		return result, rpcErr
	}
	return annotateLargeCallToolResult(result, target), nil
}

// annotateLargeCallToolResult appends a soft advisory when a direct call_tool
// target returns a large envelope. Does not reshape the payload — only teaches
// the model to prefer execute_code + print-filter next time.
func annotateLargeCallToolResult(result json.RawMessage, target string) json.RawMessage {
	if len(result) < callToolLargeResultBytes || envelopeHasError(result) {
		return result
	}

	var parsed CallToolResult
	if err := json.Unmarshal(result, &parsed); err != nil || len(parsed.Content) == 0 {
		return result
	}

	hint := fmt.Sprintf(
		"\n\n[mcpx__call_tool hint] Large direct result (~%dkB from %s). "+
			"If you only needed a subset of fields, prefer mcpx__execute_code next time and print only those fields "+
			"(call_tool returns the full envelope with no filtering).",
		len(result)/1024, target,
	)

	// Prefer embedding the hint inside a JSON object body so structured
	// parsers keep a single object; fall back to a text footer otherwise.
	for i := range parsed.Content {
		if parsed.Content[i].Type != "text" || parsed.Content[i].Text == "" {
			continue
		}
		text := parsed.Content[i].Text
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &obj); err == nil && obj != nil {
			hintJSON, err := json.Marshal(strings.TrimSpace(hint))
			if err != nil {
				break
			}
			obj["_call_tool_hint"] = hintJSON
			rewritten, err := json.Marshal(obj)
			if err != nil {
				break
			}
			parsed.Content[i].Text = string(rewritten)
		} else {
			parsed.Content[i].Text = text + hint
		}
		out, err := json.Marshal(parsed)
		if err != nil {
			return result
		}
		return out
	}
	return result
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
