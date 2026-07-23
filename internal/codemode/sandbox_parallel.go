package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
// concurrently and returns results in order (a failed call is null at its
// index; the call never throws). Items use the same call form as the
// sequential path:
//
//	parallel([[ns.tool, {args}], ...])        // tuple shorthand
//	parallel([{tool: ns.tool, args}, ...])    // object form, bound reference
//	parallel([{tool: "ns__name", args}, ...]) // object form, string name
//
// Goja is single-threaded, so we extract descriptors (recovering each tool's
// canonical name), run the actual calls in Go goroutines, collect results, and
// return them as a JS array.
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
//
// Every item may be written two ways, so the concurrent path uses the SAME
// call form as the sequential ns.tool(args) form instead of a separate string
// convention:
//
//	parallel([[github.list_issues, {repo:"x"}], [linear.search, {query:"y"}]])
//	parallel([{tool: github.list_issues, args: {repo:"x"}}, ...])
//	parallel([{tool: "github__list_issues", args: {...}}, ...])   // still works
//
// A bound tool reference (github.list_issues) carries its canonical name in a
// hidden property (see tagToolFunc), which is recovered here; a plain string
// name is accepted verbatim for backward compatibility. Args classification and
// element re-reads go through the live Goja value (not Export) so the function
// tag survives — Export erases it.
func parseParallelArgs(vm *goja.Runtime, call goja.FunctionCall) []parallelCallDescriptor {
	if len(call.Arguments) == 0 {
		panic(newJSError(vm, "parallel() requires an array of tool calls, e.g. parallel([[ns.tool, {args}], ...]) or parallel([{tool, args}, ...])"))
	}

	top := call.Arguments[0]
	items, ok := top.Export().([]any)
	if !ok {
		panic(newJSError(vm, "parallel() argument must be an array"))
	}
	if len(items) > maxParallelCalls {
		panic(newJSError(vm, fmt.Sprintf("parallel() max %d calls, got %d", maxParallelCalls, len(items))))
	}

	arr := top.ToObject(vm)
	descriptors := make([]parallelCallDescriptor, len(items))
	for i, item := range items {
		// Re-read the element from the live array so a tool-function tag (erased
		// by Export) is still recoverable; `item` is used only to classify shape.
		el := arr.Get(strconv.Itoa(i))
		switch item.(type) {
		case []any: // tuple form [toolOrFn, args]
			descriptors[i] = parseParallelTuple(vm, el.ToObject(vm), i)
		case map[string]any: // object form {tool, args}
			descriptors[i] = parseParallelObject(vm, el.ToObject(vm), i)
		default:
			panic(newJSError(vm, fmt.Sprintf(
				"parallel() item %d must be an object {tool, args} or a [tool, args] tuple", i)))
		}
	}
	return descriptors
}

// parseParallelTuple resolves a [toolOrFn, args] element.
func parseParallelTuple(vm *goja.Runtime, el *goja.Object, i int) parallelCallDescriptor {
	name := resolveParallelToolName(vm, el.Get("0"), i, true)
	return parallelCallDescriptor{Tool: name, Args: resolveParallelArgs(vm, el.Get("1"), i)}
}

// parseParallelObject resolves a {tool, args} element.
func parseParallelObject(vm *goja.Runtime, el *goja.Object, i int) parallelCallDescriptor {
	toolVal := el.Get("tool")
	if toolVal == nil || goja.IsUndefined(toolVal) || goja.IsNull(toolVal) {
		panic(newJSError(vm, fmt.Sprintf("parallel() item %d missing 'tool' field", i)))
	}
	name := resolveParallelToolName(vm, toolVal, i, false)
	return parallelCallDescriptor{Tool: name, Args: resolveParallelArgs(vm, el.Get("args"), i)}
}

// resolveParallelToolName accepts either a bound tool function (whose hidden
// canonical name is recovered) or a plain "ns__name" string. tuple reports
// which slot the value came from so the error names the right field.
func resolveParallelToolName(vm *goja.Runtime, v goja.Value, i int, tuple bool) string {
	if name, ok := toolNameOfFunc(vm, v); ok {
		return name
	}
	if _, isFn := goja.AssertFunction(v); isFn {
		panic(newJSError(vm, fmt.Sprintf(
			"parallel() item %d: the function passed is not a namespace tool — pass a tool reference like github.list_issues, or its \"ns__name\" string", i)))
	}
	if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		if name, ok := v.Export().(string); ok && name != "" {
			return name
		}
	}
	slot := "'tool'"
	if tuple {
		slot = "element 0"
	}
	panic(newJSError(vm, fmt.Sprintf(
		"parallel() item %d: %s must be a tool reference (e.g. github.list_issues) or an \"ns__name\" string", i, slot)))
}

// resolveParallelArgs exports the args value to a map. Missing/undefined args
// are allowed (a no-arg tool call); a non-object args value is rejected.
func resolveParallelArgs(vm *goja.Runtime, v goja.Value, i int) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	if m, ok := v.Export().(map[string]any); ok {
		return m
	}
	panic(newJSError(vm, fmt.Sprintf("parallel() item %d: 'args' must be an object", i)))
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

			// Normalize no-arg calls to "{}" to match the sequential path
			// (marshalToolArgs): a nil/empty args map marshals to "null",
			// which a downstream tool expecting an object can reject. Both
			// call forms must send the same shape for the same call.
			argsJSON := json.RawMessage("{}")
			if len(desc.Args) > 0 {
				b, marshalErr := json.Marshal(desc.Args)
				if marshalErr != nil {
					results[i] = parallelCallResult{index: i, err: marshalErr, dur: time.Since(start)}
					return
				}
				argsJSON = b
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
	jsResults := vm.NewArray()
	for _, cr := range results {
		toolName := descriptors[cr.index].Tool
		record := ToolCallRecord{
			Name:     toolName,
			Duration: cr.dur,
		}
		// Mirror the dispatch normalization so the audit record shows the same
		// "{}" shape actually sent for a no-arg call, not "null".
		record.Args = json.RawMessage("{}")
		if len(descriptors[cr.index].Args) > 0 {
			if argsJSON, err := json.Marshal(descriptors[cr.index].Args); err == nil {
				record.Args = argsJSON
			}
		}

		if cr.err != nil {
			record.Error = buildToolErrorMessage(toolName, record.Args, cr.err.Error(), nil, nil, toolNames)
			_ = jsResults.Set(strconv.Itoa(cr.index), nil)
		} else {
			// Exact value, same contract as the sequential path: JS consumers
			// see the true downstream result, never a pruned copy.
			val, errText := parseToolResultValue(cr.result)
			if errText != "" {
				record.Error = buildToolErrorMessage(toolName, record.Args, errText, nil, nil, toolNames)
				record.Result = cr.result
				_ = jsResults.Set(strconv.Itoa(cr.index), nil)
			} else {
				record.Result = cr.result
				_ = jsResults.Set(strconv.Itoa(cr.index), toolValueToGoja(vm, val))
			}
		}

		mu.Lock()
		*records = append(*records, record)
		mu.Unlock()
	}

	return jsResults
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
				if untrusted, ok := parseUntrustedStructuredText(text); ok {
					return untrusted, ""
				}
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
