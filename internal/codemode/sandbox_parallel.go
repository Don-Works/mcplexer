package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const maxParallelCalls = 20

// parallelCallDescriptor describes a single tool call for the parallel() helper.
type parallelCallDescriptor struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// makeParallelFunc creates the parallel() global that runs tool calls
// concurrently. API: parallel([{tool: "ns__name", args: {...}}, ...]) => any[]
// Goja is single-threaded, so we extract descriptors, run Go goroutines,
// collect results, and return them as a JS array.
func (s *Sandbox) makeParallelFunc(
	ctx context.Context,
	vm *goja.Runtime,
	mu *sync.Mutex,
	records *[]ToolCallRecord,
) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		descriptors := parseParallelArgs(vm, call)
		results := s.executeParallel(ctx, descriptors)
		return collectParallelResults(vm, mu, records, descriptors, results, s.toolNames)
	}
}

// parseParallelArgs validates and extracts descriptors from the JS arguments.
func parseParallelArgs(vm *goja.Runtime, call goja.FunctionCall) []parallelCallDescriptor {
	if len(call.Arguments) == 0 {
		panic(newJSError(vm, "parallel() requires an array of {tool, args} descriptors"))
	}

	exported := call.Arguments[0].Export()
	items, ok := exported.([]any)
	if !ok {
		panic(newJSError(vm, "parallel() argument must be an array"))
	}
	if len(items) > maxParallelCalls {
		panic(newJSError(vm, fmt.Sprintf("parallel() max %d calls, got %d", maxParallelCalls, len(items))))
	}

	descriptors := make([]parallelCallDescriptor, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			panic(newJSError(vm, fmt.Sprintf("parallel() item %d must be an object with {tool, args}", i)))
		}
		toolName, _ := m["tool"].(string)
		if toolName == "" {
			panic(newJSError(vm, fmt.Sprintf("parallel() item %d missing 'tool' field", i)))
		}
		descriptors[i].Tool = toolName
		if args, ok := m["args"].(map[string]any); ok {
			descriptors[i].Args = args
		}
	}
	return descriptors
}

// parallelCallResult holds one result from a concurrent tool call.
type parallelCallResult struct {
	index  int
	result json.RawMessage
	err    error
	dur    time.Duration
}

// executeParallel runs all tool calls concurrently and returns results.
func (s *Sandbox) executeParallel(
	ctx context.Context, descriptors []parallelCallDescriptor,
) []parallelCallResult {
	results := make([]parallelCallResult, len(descriptors))

	var wg sync.WaitGroup
	for i, desc := range descriptors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()

			argsJSON, marshalErr := json.Marshal(desc.Args)
			if marshalErr != nil {
				results[i] = parallelCallResult{index: i, err: marshalErr, dur: time.Since(start)}
				return
			}
			if argsJSON == nil {
				argsJSON = json.RawMessage("{}")
			}

			res, callErr := s.caller.CallTool(ctx, desc.Tool, argsJSON)
			results[i] = parallelCallResult{
				index:  i,
				result: res,
				err:    callErr,
				dur:    time.Since(start),
			}
		}()
	}
	wg.Wait()
	return results
}

// collectParallelResults records tool calls and builds the JS result array.
// toolNames threads through so a failed entry's record.Error can carry
// the same did-you-mean treatment as a single-call site — without it,
// parallel() callers see bare transport errors and never know that the
// member they typed was a one-letter miss away from a real one.
func collectParallelResults(
	vm *goja.Runtime,
	mu *sync.Mutex,
	records *[]ToolCallRecord,
	descriptors []parallelCallDescriptor,
	results []parallelCallResult,
	toolNames []string,
) goja.Value {
	jsResults := make([]any, len(results))
	for _, cr := range results {
		toolName := descriptors[cr.index].Tool
		record := ToolCallRecord{
			Name:     toolName,
			Duration: cr.dur,
		}
		argsJSON, _ := json.Marshal(descriptors[cr.index].Args)
		if argsJSON != nil {
			record.Args = argsJSON
		}

		if cr.err != nil {
			record.Error = buildToolErrorMessage(toolName, argsJSON, cr.err.Error(), nil, nil, toolNames)
			jsResults[cr.index] = nil
		} else {
			compacted := compactForSandbox(cr.result)
			val, errText := parseToolResultValue(compacted)
			if errText != "" {
				record.Error = buildToolErrorMessage(toolName, argsJSON, errText, nil, nil, toolNames)
				record.Result = cr.result
				jsResults[cr.index] = nil
			} else {
				record.Result = cr.result
				jsResults[cr.index] = val
			}
		}

		mu.Lock()
		*records = append(*records, record)
		mu.Unlock()
	}

	return vm.ToValue(jsResults)
}

// parseToolResultValue converts an MCP CallToolResult to a Go value
// suitable for vm.ToValue(). Returns (value, errorText). If errorText is
// non-empty the tool returned an isError result.
//
// Unwrap precedence:
//  1. isError → return error text.
//  2. structuredContent (MCP-spec parsed payload) → return it parsed.
//  3. content[0] is the canonical payload — unwrap its text (JSON if
//     parseable, else string) or resource. Subsequent content blocks
//     are decorations (commonly the mesh-notice footer added by
//     piggybackMeshNotice when there are pending mesh messages) that
//     user code shouldn't have to pattern-match around. Without this
//     rule, every chained builtin pattern (e.g. `task.create({...}).id`)
//     breaks whenever the mesh queue is non-empty — the bug filed as
//     01KSGKJDHZ2HS87TRK8BTQQ89S.
//  4. Fall back to returning the full envelope as a parsed object.
func parseToolResultValue(raw json.RawMessage) (any, string) {
	var envelope struct {
		Content           []map[string]any `json:"content"`
		IsError           bool             `json:"isError"`
		StructuredContent json.RawMessage  `json:"structuredContent"`
	}

	if err := json.Unmarshal(raw, &envelope); err == nil {
		if envelope.IsError {
			return nil, extractToolErrorText(envelope.Content, raw)
		}

		// MCP-spec structured payload wins when populated.
		if len(envelope.StructuredContent) > 0 &&
			string(envelope.StructuredContent) != "null" {
			var parsed any
			if err := json.Unmarshal(envelope.StructuredContent, &parsed); err == nil {
				return parsed, ""
			}
		}

		if len(envelope.Content) > 0 {
			item := envelope.Content[0]
			if text, ok := item["text"].(string); ok {
				var parsed any
				if err := json.Unmarshal([]byte(text), &parsed); err == nil {
					return parsed, ""
				}
				return projectTextValue(text), ""
			}
			if parsed := parseResourceContent(item); parsed != nil {
				return parsed, ""
			}
		}
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		return parsed, ""
	}
	return string(raw), ""
}

// parseResourceContent extracts JSON from a resource content item.
func parseResourceContent(item map[string]any) any {
	typ, _ := item["type"].(string)
	if typ != "resource" {
		return nil
	}
	res, ok := item["resource"].(map[string]any)
	if !ok {
		return nil
	}
	mime, _ := res["mimeType"].(string)
	if mime != "application/json" {
		return nil
	}
	text, ok := res["text"].(string)
	if !ok {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed
	}
	return nil
}
