package codemode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/compact"
	"github.com/dop251/goja"
)

// Resource limits for sandboxed execution. Tunable via the Sandbox struct
// for tests; defaults are sized for typical Code Mode workloads.
const (
	defaultMaxCallStack    = 256  // recursion guard
	DefaultMaxHeapGrowthMB = 2048 // abort VM if process heap grows > this since exec start
	HardMaxHeapGrowthMB    = 2048
	defaultWatchdogPeriod  = 50 * time.Millisecond
	// DefaultMaxOutputBytes caps captured print()/console.log output during
	// execution. This prevents a script from building an unbounded MCP result.
	DefaultMaxOutputBytes = 24 * 1024
	// HardMaxOutputBytes is the largest output cap accepted by the sandbox.
	HardMaxOutputBytes = 256 * 1024
	// defaultHeapBreaches is the number of CONSECUTIVE watchdog ticks that
	// must all observe heap growth past the limit before the VM is aborted.
	// HeapAlloc is process-global, so a single over-limit reading can be
	// caused by another goroutine (a concurrent code-mode execution, a
	// downstream dispatch, a DB query, GC timing) rather than the sandbox
	// under test. Requiring sustained growth across several ticks means a
	// genuine runaway allocation (which climbs monotonically) still trips
	// quickly, while a transient spike from unrelated work resets the
	// counter and does not falsely abort innocent code.
	defaultHeapBreaches = 4
)

// ToolCaller abstracts tool invocation so the sandbox can call through
// the full gateway pipeline (routing → auth → approval → cache → dispatch).
type ToolCaller interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// ToolCallRecord captures a single tool invocation for audit trail.
type ToolCallRecord struct {
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	Duration time.Duration   `json:"duration_ms"`
}

// ExecutionResult contains everything produced by a sandbox execution.
type ExecutionResult struct {
	Output             string           `json:"output"`
	OutputTruncated    bool             `json:"output_truncated,omitempty"`
	OutputMaxBytes     int              `json:"output_max_bytes,omitempty"`
	OutputBytesOmitted int              `json:"output_bytes_omitted,omitempty"`
	ToolCalls          []ToolCallRecord `json:"tool_calls"`
	Error              string           `json:"error,omitempty"`

	// OutputRaw is the displayed output WITHOUT the truncation notice, and
	// OutputOverflow the retained over-cap bytes — together the (possibly
	// bounded) full print stream, handed to the gateway so it can stash the
	// original in CCR and leave a retrieval marker instead of a dead-end
	// truncation. OutputOverflowComplete is false when even the retention
	// bound overflowed. Never serialized to the model.
	OutputRaw              string `json:"-"`
	OutputOverflow         []byte `json:"-"`
	OutputOverflowComplete bool   `json:"-"`

	// SessionState is the post-run snapshot of the ephemeral `session`
	// object — the JSON-serializable own properties an agent assigned to it.
	// Populated only on a CLEAN run when session-state is enabled
	// (SetSessionState was called). nil means "leave the persisted state
	// unchanged" (feature off, the run errored, or the snapshot exceeded the
	// cap). The gateway persists it keyed by MCP session for the next call.
	SessionState map[string]json.RawMessage `json:"-"`
	// SessionStateWarning is surfaced to the agent when the snapshot could
	// not be fully persisted (over the size cap, or a value was not
	// JSON-serializable, e.g. a function).
	SessionStateWarning string `json:"session_state_warning,omitempty"`
}

// Sandbox executes JavaScript code with tool functions bound as
// synchronous Go function calls. Each tool call routes through the
// full MCPlexer gateway pipeline.
type Sandbox struct {
	caller          ToolCaller
	timeout         time.Duration
	maxCallStack    int           // recursion guard; 0 = default
	maxHeapGrowthMB uint64        // abort if process heap grows > this in MB during exec
	watchdogPeriod  time.Duration // poll period for the heap-growth watchdog
	heapBreaches    int           // consecutive over-limit ticks before abort; 0 = default
	maxOutputBytes  int           // captured print output cap; 0 = default
	toolNames       []string      // all registered tool names, for did-you-mean suggestions

	// sessionStateEnabled exposes a persistent `session` object to user code.
	// When set, initialSessionState is rehydrated into `session` before the
	// script runs and the object is snapshotted back out after a clean run.
	sessionStateEnabled  bool
	initialSessionState  map[string]json.RawMessage // rehydrated into `session`
	sessionStateMaxBytes int                        // cap on total serialized snapshot
}

// NewSandbox creates a sandbox with the given tool caller and timeout.
func NewSandbox(caller ToolCaller, timeout time.Duration) *Sandbox {
	return &Sandbox{
		caller:          caller,
		timeout:         timeout,
		maxCallStack:    defaultMaxCallStack,
		maxHeapGrowthMB: DefaultMaxHeapGrowthMB,
		watchdogPeriod:  defaultWatchdogPeriod,
		heapBreaches:    defaultHeapBreaches,
		maxOutputBytes:  DefaultMaxOutputBytes,
	}
}

// SetMaxHeapGrowthMB configures the process-heap growth watchdog. Non-positive
// values fall back to the default; values above the hard maximum are clamped so
// one execute_code call cannot reopen daemon-wide OOM risk.
func (s *Sandbox) SetMaxHeapGrowthMB(n int) {
	if n <= 0 {
		s.maxHeapGrowthMB = DefaultMaxHeapGrowthMB
		return
	}
	if n > HardMaxHeapGrowthMB {
		n = HardMaxHeapGrowthMB
	}
	s.maxHeapGrowthMB = uint64(n)
}

// SetMaxOutputBytes configures the captured print()/console.log output cap.
// Non-positive values fall back to the default; values above the hard maximum
// are clamped so a bad setting cannot re-open unbounded output growth.
func (s *Sandbox) SetMaxOutputBytes(n int) {
	s.maxOutputBytes = NormalizeMaxOutputBytes(n)
}

// SetSessionState enables the ephemeral, per-session `session` object. `initial`
// (key -> JSON-encoded value, may be nil/empty) is rehydrated into a global
// `session` object before the script runs; after a CLEAN run the object's
// JSON-serializable own properties are snapshotted back into
// ExecutionResult.SessionState so the gateway can persist them for the next
// execute_code call in the same MCP session. maxBytes caps the total serialized
// snapshot (non-positive leaves the existing cap). Calling this is what makes
// `session` available to user code.
func (s *Sandbox) SetSessionState(initial map[string]json.RawMessage, maxBytes int) {
	s.sessionStateEnabled = true
	s.initialSessionState = initial
	if maxBytes > 0 {
		s.sessionStateMaxBytes = maxBytes
	}
}

// Execute runs JavaScript code in a fresh Goja VM with tool namespaces
// registered as global objects. Returns captured output and tool call records.
func (s *Sandbox) Execute(ctx context.Context, code string, tools []ToolDef) (*ExecutionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	vm := goja.New()
	if s.maxCallStack > 0 {
		vm.SetMaxCallStackSize(s.maxCallStack)
	}

	var (
		mu      sync.Mutex
		output  = newOutputCapture(s.maxOutputBytes)
		records []ToolCallRecord
	)

	// Shared print handler used by both print() and console.log.
	printFn := makePrintFunc(vm, &mu, output)

	if err := vm.Set("print", printFn); err != nil {
		return nil, fmt.Errorf("set print: %w", err)
	}

	console := vm.NewObject()
	if err := console.Set("log", printFn); err != nil {
		return nil, fmt.Errorf("set console.log: %w", err)
	}
	if err := vm.Set("console", console); err != nil {
		return nil, fmt.Errorf("set console: %w", err)
	}
	if err := registerBase64Helpers(vm); err != nil {
		return nil, err
	}

	// Group tools by namespace and register each namespace as a global object.
	groups := groupByNamespace(tools)
	for ns, entries := range groups {
		nsObj := vm.NewObject()
		for _, entry := range entries {
			fullName := ns + "__" + entry.name
			fn := s.makeToolFunc(ctx, vm, fullName, &mu, &records, toolSchema{
				inputSchema: entry.schema,
				examples:    entry.examples,
			})
			if err := nsObj.Set(entry.name, fn); err != nil {
				return nil, fmt.Errorf("set %s.%s: %w", ns, entry.name, err)
			}
		}
		if err := vm.Set(ns, nsObj); err != nil {
			return nil, fmt.Errorf("set namespace %s: %w", ns, err)
		}
	}

	// Register tool names for did-you-mean suggestions (populated from the
	// tools passed to Execute so error messages can suggest close matches).
	s.toolNames = make([]string, len(tools))
	for i, t := range tools {
		s.toolNames[i] = t.Name
	}

	// Register parallel() helper for concurrent tool calls.
	parallelFn := s.makeParallelFunc(ctx, vm, &mu, &records)
	if err := vm.Set("parallel", parallelFn); err != nil {
		return nil, fmt.Errorf("set parallel: %w", err)
	}

	// Register compact() helper — prunes nulls/empties and applies
	// columnar compaction to arrays. Returns a new compacted value.
	compactFn := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		exported := call.Arguments[0].Export()
		result := compactValue(exported)
		return vm.ToValue(result)
	}
	if err := vm.Set("compact", compactFn); err != nil {
		return nil, fmt.Errorf("set compact: %w", err)
	}

	sleepFn := func(call goja.FunctionCall) goja.Value {
		var ms int64
		if len(call.Arguments) > 0 {
			switch n := call.Arguments[0].Export().(type) {
			case int64:
				ms = n
			case int:
				ms = int64(n)
			case float64:
				ms = int64(n)
			}
		}
		if ms <= 0 {
			return goja.Undefined()
		}
		if ms > 60000 {
			ms = 60000
		}
		timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			// Interrupt synchronously: returning normally here lets the
			// script race the watchdog goroutine and keep executing past
			// its deadline. Setting the interrupt flag before returning
			// plus panicking out of the Go callback guarantees this sleep
			// call cannot return control to user code after the deadline.
			vm.Interrupt("execution timeout")
			panic(vm.ToValue("execution timeout"))
		}
		return goja.Undefined()
	}
	if err := vm.Set("sleep", sleepFn); err != nil {
		return nil, fmt.Errorf("set sleep: %w", err)
	}

	// Build the ephemeral per-session `session` object (when enabled) BEFORE
	// registering help(), so help() can show the keys the agent already stored
	// this MCP session. Rehydrates JSON-serializable values assigned in a prior
	// execute_code call this session; own enumerable props are re-serialized
	// into result.SessionState after a clean run.
	var sessionObj *goja.Object
	if s.sessionStateEnabled {
		sessionObj = s.buildSessionObject(vm)
		if err := vm.Set("session", sessionObj); err != nil {
			return nil, fmt.Errorf("set session: %w", err)
		}
	}

	// Register help() — in-sandbox introspection so a model (especially a
	// small/local one) can discover the available namespaces and a tool's
	// signature without a search round-trip or guessing. Writes to the same
	// capture buffer as print(), so a bare `help()` surfaces output. Passed the
	// live session object so the index shows what's currently cached.
	helpFn := makeHelpFunc(&mu, output, groups, sessionObj)
	if err := vm.Set("help", helpFn); err != nil {
		return nil, fmt.Errorf("set help: %w", err)
	}

	// Install the Number.prototype.toLocaleString polyfill before user code.
	// goja has no Intl and aliases toLocaleString to toString, so a locale
	// argument is parsed as a radix and throws — this replaces it with a
	// non-throwing, grouped formatter.
	if err := installNumberLocalePolyfill(vm); err != nil {
		return nil, fmt.Errorf("install number locale polyfill: %w", err)
	}

	// Run watchdog: interrupts the VM on (a) ctx cancel/timeout, (b) heap
	// growth past the configured limit. Heap growth is approximate — it
	// observes the entire process heap, not just this VM — so we only abort
	// when growth stays over the limit across `heapBreaches` CONSECUTIVE
	// ticks. A genuine runaway allocation (`while(true) a.push(x)`) climbs
	// monotonically and trips quickly, but a transient spike from a
	// concurrent code-mode execution, a downstream dispatch, or GC timing
	// resets the counter instead of falsely aborting innocent code. It
	// still stops runaways long before the wall-clock timeout fires.
	done := make(chan struct{})
	startMem := readHeapBytes()
	limitBytes := s.maxHeapGrowthMB * 1024 * 1024
	breachLimit := s.heapBreaches
	if breachLimit <= 0 {
		breachLimit = defaultHeapBreaches
	}
	go func() {
		ticker := time.NewTicker(s.watchdogPeriod)
		defer ticker.Stop()
		consecutive := 0
		for {
			select {
			case <-ctx.Done():
				vm.Interrupt("execution timeout")
				return
			case <-done:
				return
			case <-ticker.C:
				if limitBytes == 0 {
					continue
				}
				cur := readHeapBytes()
				if cur > startMem && cur-startMem > limitBytes {
					consecutive++
					if consecutive >= breachLimit {
						vm.Interrupt("execution memory limit exceeded")
						return
					}
				} else {
					consecutive = 0
				}
			}
		}
	}()

	_, err := vm.RunString(code)
	close(done)

	result := &ExecutionResult{
		Output:                 output.String(),
		OutputTruncated:        output.Truncated(),
		OutputMaxBytes:         output.MaxBytes(),
		OutputBytesOmitted:     output.BytesOmitted(),
		OutputRaw:              output.Raw(),
		OutputOverflow:         output.Overflow(),
		OutputOverflowComplete: output.OverflowComplete(),
		ToolCalls:              records,
	}

	if err != nil {
		switch {
		case isMemoryLimitError(err):
			result.Error = "execution exceeded memory limit"
		case isTimeoutError(err):
			result.Error = timeoutErrorMessage(s.timeout)
		default:
			result.Error = annotateRuntimeError(err.Error(), s.toolNames)
		}
		// Do NOT snapshot session state on error/timeout: a partially-mutated
		// `session` object must not clobber the prior good snapshot.
		return result, nil
	}

	// Clean run: snapshot the `session` object so the gateway can persist it
	// for the next call in this MCP session.
	if s.sessionStateEnabled && sessionObj != nil {
		result.SessionState, result.SessionStateWarning =
			snapshotSessionObject(sessionObj, s.sessionStateMaxBytes)
	}

	return result, nil
}

// buildSessionObject constructs the `session` global, rehydrating any
// JSON-serializable values stored from a prior call in this MCP session.
func (s *Sandbox) buildSessionObject(vm *goja.Runtime) *goja.Object {
	obj := vm.NewObject()
	for k, raw := range s.initialSessionState {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		_ = obj.Set(k, vm.ToValue(v))
	}
	return obj
}

// snapshotSessionObject serializes the own-enumerable properties of the
// `session` object to JSON for persistence. Non-serializable values (e.g.
// functions, closures) are skipped and named in the returned warning. If the
// total serialized size exceeds maxBytes the snapshot is dropped (nil return)
// so the prior good state is preserved, and a warning explains why. An empty
// (non-nil) map is returned when the object has no persistable keys, so a
// fully-cleared `session` is reflected.
func snapshotSessionObject(obj *goja.Object, maxBytes int) (map[string]json.RawMessage, string) {
	if obj == nil {
		return nil, ""
	}
	out := make(map[string]json.RawMessage)
	total := 0
	var skipped []string
	for _, k := range obj.Keys() {
		v := obj.Get(k)
		if v == nil || goja.IsUndefined(v) {
			// undefined isn't valid JSON; null is kept (it round-trips).
			continue
		}
		data, ok := safeMarshalExport(v)
		if !ok {
			skipped = append(skipped, k)
			continue
		}
		total += len(k) + len(data)
		out[k] = json.RawMessage(data)
	}
	if maxBytes > 0 && total > maxBytes {
		return nil, fmt.Sprintf(
			"session state (%d bytes) exceeds the %d-byte cap and was NOT persisted; "+
				"reduce what you keep on `session`, or use kv__set for large/durable values.",
			total, maxBytes)
	}
	if len(skipped) > 0 {
		return out, fmt.Sprintf(
			"session keys not persisted (not JSON-serializable): %s. "+
				"Functions/closures don't survive across calls; store plain data or use kv__set.",
			strings.Join(skipped, ", "))
	}
	return out, ""
}

// safeMarshalExport JSON-marshals a goja value's exported form, guarding
// against Export/Marshal panics (e.g. exotic or cyclic values) so one bad
// property can never crash the snapshot.
func safeMarshalExport(v goja.Value) (data []byte, ok bool) {
	defer func() {
		if recover() != nil {
			data, ok = nil, false
		}
	}()
	b, err := json.Marshal(v.Export())
	if err != nil {
		return nil, false
	}
	return b, true
}

// timeoutErrorMessage surfaces the wall-clock execution timeout and
// reminds the agent about the sleep() per-call clamp so a long sleep()
// call doesn't look like a bug — sleep is hard-clamped at 60 seconds
// per call regardless of the configured execution timeout.
func timeoutErrorMessage(timeout time.Duration) string {
	msg := "execution timed out"
	if timeout > 0 {
		msg = fmt.Sprintf("execution timed out after %s", timeout)
	}
	msg += " (sleep(ms) is clamped to 60s per call; chain multiple sleeps for longer waits, but the whole script must still finish within the execution timeout)"
	return msg
}

// reReferenceError extracts the undefined identifier name from a Goja
// `ReferenceError: foo is not defined` panic. Used by annotateRuntimeError
// to surface namespace-level did-you-mean suggestions at runtime when the
// preflight lint missed the typo (e.g. dynamic identifiers).
var reReferenceError = regexp.MustCompile(`ReferenceError:\s*(\w+)\s+is not defined`)

// reObjectNoMember extracts the missing member name from a Goja
// `TypeError: Object has no member 'foo'` panic. The namespace object
// is unnamed in the error text, so we suggest by short-name lookup
// across every registered tool's post-'__' member.
var reObjectNoMember = regexp.MustCompile(`Object has no member '(\w+)'`)

// reUndefinedProperty extracts the property name from a Goja
// `TypeError: Cannot read property 'X' of undefined or null` panic — what
// goja emits when a model reaches THROUGH a value that doesn't exist, e.g.
// `mcpx.memory.recall()` where `mcpx.memory` is undefined so reading
// `.recall` off it throws. Unlike the bare-identifier ReferenceError case,
// the base object existed; the model just nested too deep (namespaces are
// flat top-level globals, not chained). The alternation also matches the
// modern V8 phrasing `Cannot read properties of undefined (reading 'X')`
// so the annotation keeps working if goja's wording is updated.
var reUndefinedProperty = regexp.MustCompile(`Cannot read propert(?:y '(\w+)' of (?:undefined|null)|ies of (?:undefined|null) \(reading '(\w+)'\))`)

// helpHint points a confused model at the in-sandbox help() introspection.
// Appended to runtime errors that signal the model doesn't know what's
// callable — the moment help() is most useful.
const helpHint = "\nCall help() to list namespaces, or help('namespace') for its tools."

// helpNsHint is the shorter pointer used when an "Available namespaces:" list
// already precedes it — repeating "call help() to list namespaces" right
// after showing that list is noise, so we only advertise the per-namespace
// form (which adds signatures the inline list doesn't).
const helpNsHint = "\nCall help('namespace') for a namespace's tools and signatures."

// sessionNotEnabledHint signposts the one case where `session` is undefined:
// the cross-call session object is only exposed when the connection has a
// stable MCP session id (SetSessionState was called). When enabled, `session`
// is ALWAYS a defined object, so "ReferenceError: session is not defined" can
// only mean the feature is off for this connection — which otherwise reads like
// the agent's own bug rather than an unavailable feature. Point at the durable
// alternative so the agent isn't left at a dead end.
const sessionNotEnabledHint = "\n`session` is not available on this connection — the cross-call session object requires a stable MCP session id. For state that must persist across calls here, use kv.set({key, value}) then kv.get({key}) (durable, workspace-scoped)."

// annotateRuntimeError inspects a Goja runtime error and, when it
// looks like a typo'd identifier or member access, appends did-you-mean
// suggestions derived from the registered toolNames. Non-typo errors
// pass through unchanged so users still see the original Goja diagnostic.
func annotateRuntimeError(msg string, toolNames []string) string {
	if m := reReferenceError.FindStringSubmatch(msg); len(m) == 2 {
		bad := m[1]
		// `session` undefined is special: it means session-state is disabled
		// for this connection, not a typo. Signpost it (no namespace list needed)
		// even when toolNames is empty.
		if bad == "session" {
			return msg + sessionNotEnabledHint
		}
		if len(toolNames) == 0 {
			return msg
		}
		nsList := namespacesOf(toolNames)
		if len(nsList) == 0 {
			return msg
		}
		var b strings.Builder
		b.WriteString(msg)
		if suggestions := DidYouMean(bad, nsList, 3); len(suggestions) > 0 {
			b.WriteString("\nDid you mean: ")
			b.WriteString(strings.Join(suggestions, ", "))
			b.WriteString("?")
		}
		b.WriteString("\nAvailable namespaces: ")
		b.WriteString(strings.Join(truncateNamespaceList(nsList, 20), ", "))
		if len(nsList) > 20 {
			fmt.Fprintf(&b, " (+%d more)", len(nsList)-20)
		}
		b.WriteString(helpNsHint)
		return b.String()
	}
	if m := reObjectNoMember.FindStringSubmatch(msg); len(m) == 2 && len(toolNames) > 0 {
		bad := m[1]
		shorts, dotted := membersIndexedByShort(toolNames)
		if len(shorts) == 0 {
			return msg
		}
		if suggestions := DidYouMean(bad, shorts, 3); len(suggestions) > 0 {
			rendered := make([]string, 0, len(suggestions))
			seen := make(map[string]struct{})
			for _, s := range suggestions {
				for _, full := range dotted[s] {
					if _, dup := seen[full]; dup {
						continue
					}
					seen[full] = struct{}{}
					rendered = append(rendered, full)
				}
			}
			if len(rendered) > 0 {
				return msg + "\nDid you mean: " + strings.Join(rendered, ", ") + "?" + helpHint
			}
		}
	}
	if m := reUndefinedProperty.FindStringSubmatch(msg); m != nil && len(toolNames) > 0 {
		prop := m[1]
		if prop == "" {
			prop = m[2]
		}
		return annotateUndefinedProperty(msg, prop, toolNames)
	}
	return msg
}

// annotateUndefinedProperty steers a model that nested THROUGH a
// non-existent value (`mcpx.memory.recall()` → reading `.recall` off the
// undefined `mcpx.memory`) back to the flat top-level call form. When the
// reached-for property matches a real tool's short name, the correct
// `ns.member(...)` form is named directly (and we point at help()); otherwise
// we list the available namespaces and use the shorter per-namespace hint so
// we don't tell the model to "list namespaces" right after listing them.
func annotateUndefinedProperty(msg, prop string, toolNames []string) string {
	var b strings.Builder
	b.WriteString(msg)
	_, dotted := membersIndexedByShort(toolNames)
	if forms := dotted[prop]; len(forms) > 0 {
		calls := make([]string, len(forms))
		for i, f := range forms {
			calls[i] = f + "(...)"
		}
		b.WriteString("\nNamespaces are top-level — call ")
		b.WriteString(strings.Join(calls, " or "))
		b.WriteString(" directly, not nested under another object.")
		b.WriteString(helpHint)
		return b.String()
	}
	if nsList := namespacesOf(toolNames); len(nsList) > 0 {
		b.WriteString("\nAvailable namespaces: ")
		b.WriteString(strings.Join(truncateNamespaceList(nsList, 20), ", "))
		if len(nsList) > 20 {
			fmt.Fprintf(&b, " (+%d more)", len(nsList)-20)
		}
		b.WriteString(helpNsHint)
		return b.String()
	}
	b.WriteString(helpHint)
	return b.String()
}

// namespacesOf returns the sorted unique namespace prefixes (the substring
// before the first "__") found across toolNames. Names without a namespace
// separator are skipped so we never suggest a bare tool name as a
// namespace candidate.
func namespacesOf(toolNames []string) []string {
	seen := make(map[string]struct{})
	for _, name := range toolNames {
		ns, _, ok := strings.Cut(name, "__")
		if !ok {
			continue
		}
		seen[ns] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// truncateNamespaceList returns at most max entries from nsList. Keeps
// the slice deterministic by relying on the caller's already-sorted order.
func truncateNamespaceList(nsList []string, max int) []string {
	if len(nsList) <= max {
		return nsList
	}
	return nsList[:max]
}

// membersIndexedByShort splits every "ns__member" tool name and returns
// (sorted unique short names, short name → list of "ns.member" forms).
// A short name shared across multiple namespaces (e.g. `list_issues` in
// github and linear) maps to all of its dotted renderings so suggestions
// stay accurate when the namespace context is unknown at error time.
func membersIndexedByShort(toolNames []string) ([]string, map[string][]string) {
	dotted := make(map[string][]string)
	for _, name := range toolNames {
		ns, member, ok := strings.Cut(name, "__")
		if !ok {
			continue
		}
		dotted[member] = append(dotted[member], ns+"."+member)
	}
	shorts := make([]string, 0, len(dotted))
	for s := range dotted {
		shorts = append(shorts, s)
	}
	sort.Strings(shorts)
	return shorts, dotted
}

// newJSError builds a real JavaScript Error object so sandbox code gets
// idiomatic semantics: catch(e){e.message} is populated and
// e instanceof Error is true. Throwing bare strings left e.message
// undefined, which silently broke standard error handling in agent code.
func newJSError(vm *goja.Runtime, msg string) goja.Value {
	ctor, ok := goja.AssertConstructor(vm.Get("Error"))
	if !ok {
		return vm.ToValue(msg)
	}
	obj, err := ctor(nil, vm.ToValue(msg))
	if err != nil {
		return vm.ToValue(msg)
	}
	return obj
}

func registerBase64Helpers(vm *goja.Runtime) error {
	if err := vm.Set("atob", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		raw := strings.TrimSpace(call.Arguments[0].String())
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			panic(newJSError(vm, fmt.Sprintf("atob: invalid base64: %v", err)))
		}
		return vm.ToValue(latin1String(decoded))
	}); err != nil {
		return fmt.Errorf("set atob: %w", err)
	}
	if err := vm.Set("btoa", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue("")
		}
		data, err := binaryStringBytes(call.Arguments[0].String())
		if err != nil {
			panic(newJSError(vm, "btoa: input contains a character outside Latin-1"))
		}
		return vm.ToValue(base64.StdEncoding.EncodeToString(data))
	}); err != nil {
		return fmt.Errorf("set btoa: %w", err)
	}
	return nil
}

func latin1String(data []byte) string {
	var b strings.Builder
	b.Grow(len(data))
	for _, c := range data {
		b.WriteRune(rune(c))
	}
	return b.String()
}

func binaryStringBytes(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 255 {
			return nil, fmt.Errorf("non Latin-1 rune %U", r)
		}
		out = append(out, byte(r))
	}
	return out, nil
}

// readHeapBytes returns the current Go heap allocation size in bytes. The
// codemode sandbox uses this to abort runaway allocations that would
// otherwise OOM the daemon.
func readHeapBytes() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// toolSchema is the subset of tool definition data passed to makeToolFunc
// so error messages can include parameter hints and examples.
type toolSchema struct {
	inputSchema json.RawMessage
	examples    []string
}

// makeToolFunc creates a Go function that Goja calls synchronously.
func (s *Sandbox) makeToolFunc(
	ctx context.Context,
	vm *goja.Runtime,
	toolName string,
	mu *sync.Mutex,
	records *[]ToolCallRecord,
	schema toolSchema,
) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		defer recoverToolPanic(vm, toolName)

		start := time.Now()

		argsJSON := marshalToolArgs(vm, toolName, call)

		rawResult, err := s.caller.CallTool(ctx, toolName, argsJSON)
		duration := time.Since(start)

		record := ToolCallRecord{
			Name:     toolName,
			Args:     argsJSON,
			Duration: duration,
		}

		if err != nil {
			record.Error = err.Error()
			mu.Lock()
			*records = append(*records, record)
			mu.Unlock()
			panic(newJSError(vm, buildToolErrorMessage(toolName, argsJSON, err.Error(), schema.inputSchema, schema.examples, s.toolNames)))
		}

		// Surface _meta.cache from the raw result as a VM global.
		setCacheMeta(vm, rawResult)

		// The sandbox receives the EXACT downstream value — no pruning, no
		// pagination-key stripping. JS consumers need `.map` on empty arrays,
		// `=== null` checks, and cursor fields to behave truthfully; token
		// economy happens at print/render time, never on the consumed value.
		val, errText := parseToolResult(vm, rawResult)
		if errText != "" {
			record.Error = errText
			record.Result = rawResult
			mu.Lock()
			*records = append(*records, record)
			mu.Unlock()
			panic(newJSError(vm, buildToolErrorMessage(toolName, argsJSON, errText, schema.inputSchema, schema.examples, s.toolNames)))
		}

		record.Result = rawResult
		mu.Lock()
		*records = append(*records, record)
		mu.Unlock()

		return val
	}
}

// setCacheMeta extracts selected _meta fields from a raw MCP result and exposes
// them as the _lastCallMeta global in the VM. Non-breaking — agents that don't
// check _lastCallMeta are unaffected.
func setCacheMeta(vm *goja.Runtime, raw json.RawMessage) {
	_ = vm.Set("_lastCallMeta", extractCallMeta(raw))
}

func extractCallMeta(raw json.RawMessage) map[string]any {
	out := map[string]any{"cached": false}
	var envelope struct {
		Meta json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Meta) == 0 {
		return out
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Meta, &meta); err != nil {
		return out
	}

	if rawCache := meta["cache"]; len(rawCache) > 0 {
		var cacheMeta struct {
			Cached     bool `json:"cached"`
			AgeSeconds int  `json:"age_seconds"`
		}
		if err := json.Unmarshal(rawCache, &cacheMeta); err == nil {
			out["cached"] = cacheMeta.Cached
			if cacheMeta.Cached {
				out["age_seconds"] = cacheMeta.AgeSeconds
			}
		}
	}

	if rawCorrection := meta["fuzzy_correction"]; len(rawCorrection) > 0 {
		var correction struct {
			Original  string `json:"original"`
			Corrected string `json:"corrected"`
		}
		if err := json.Unmarshal(rawCorrection, &correction); err == nil &&
			correction.Original != "" && correction.Corrected != "" {
			out["fuzzy_correction"] = correction
		}
	}

	return out
}

// extractCacheMeta parses the _meta.cache field from an MCP result envelope.
func extractCacheMeta(raw json.RawMessage) (hit bool, ageSec int) {
	meta := extractCallMeta(raw)
	if cached, ok := meta["cached"].(bool); ok {
		hit = cached
	}
	if age, ok := meta["age_seconds"].(int); ok {
		ageSec = age
	}
	return hit, ageSec
}

// recoverToolPanic re-raises Goja exceptions and wraps other panics.
func recoverToolPanic(vm *goja.Runtime, toolName string) {
	r := recover()
	if r == nil {
		return
	}
	switch v := r.(type) {
	case *goja.Exception:
		panic(v)
	case goja.Value:
		panic(v)
	default:
		panic(newJSError(vm, fmt.Sprintf("tool call %s panicked: %v", toolName, v)))
	}
}

// marshalToolArgs extracts and marshals the first JS argument to JSON.
func marshalToolArgs(vm *goja.Runtime, toolName string, call goja.FunctionCall) json.RawMessage {
	if len(call.Arguments) == 0 {
		return json.RawMessage("{}")
	}
	exported := call.Arguments[0].Export()
	data, err := json.Marshal(exported)
	if err != nil {
		panic(newJSError(vm, fmt.Sprintf("failed to marshal args for %s: %v", toolName, err)))
	}
	return data
}

// parseToolResult converts an MCP CallToolResult to a Goja value.
// If the result is an error (isError: true), returns (Undefined, errorText).
// On success, returns (parsedValue, "").
func parseToolResult(vm *goja.Runtime, raw json.RawMessage) (goja.Value, string) {
	val, errText := parseToolResultValue(raw)
	if errText != "" {
		return goja.Undefined(), errText
	}
	if s, ok := val.(string); ok {
		return vm.ToValue(s), ""
	}
	if m, ok := val.(map[string]any); ok && isTextProjection(m) {
		return textProjectionToObject(vm, m), ""
	}
	return toolValueToGoja(vm, val), ""
}

// textProjectionToObject builds the projected text value as a goja object
// with a deterministic key order (kind, text, bytes, then any extras
// sorted). projectTextValue returns a Go map, and vm.ToValue surfaces Go's
// randomized map iteration order to Object.keys() — so without a fixed order
// the key sequence varies run to run, making agent code that inspects
// Object.keys (and the test that pins it) flaky.
func textProjectionToObject(vm *goja.Runtime, m map[string]any) goja.Value {
	obj := vm.NewObject()
	seen := make(map[string]bool, len(m))
	for _, k := range []string{"kind", "text", "bytes"} {
		if v, ok := m[k]; ok {
			_ = obj.Set(k, v)
			seen[k] = true
		}
	}
	extras := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		_ = obj.Set(k, m[k])
	}
	return obj
}

// maxArgsInError is the max length of serialized arguments included in
// error messages. Beyond this the string is truncated to avoid bloating
// the LLM's context window with huge payloads.
const maxArgsInError = 500

// maxSchemaHintLen is the max length of the schema-based parameter hint
// appended to tool error messages. Prevents a verbose schema from
// drowning out the actual error in the model's context.
const maxSchemaHintLen = 600

// maxSynthExampleLen is the max length of a synthesized usage example.
const maxSynthExampleLen = 200

// buildToolErrorMessage creates a detailed error message that helps the
// LLM understand exactly which tool call failed, what arguments were
// sent, what the downstream server reported, and what parameters the
// tool expects (from its InputSchema). Parameter hints are especially
// helpful for cheap models that frequently pass wrong argument shapes.
// When the error suggests the tool was not found and toolNames are
// provided, it includes did-you-mean suggestions for close matches.
func buildToolErrorMessage(toolName string, args json.RawMessage, errText string, schema json.RawMessage, examples []string, toolNames []string) string {
	var b strings.Builder
	b.WriteString("Tool call failed: ")
	b.WriteString(toolName)
	b.WriteString("\nArguments: ")
	argsStr := string(args)
	if len(argsStr) > maxArgsInError {
		argsStr = argsStr[:maxArgsInError] + "...(truncated)"
	}
	b.WriteString(argsStr)
	b.WriteString("\nError: ")
	b.WriteString(errText)

	hintExamples := examples
	if len(hintExamples) == 0 && len(schema) > 0 {
		if synth := synthesizeExample(toolName, schema); synth != "" {
			hintExamples = []string{synth}
		}
	}

	if hint := schemaParamHint(schema, hintExamples); hint != "" {
		b.WriteString("\n")
		b.WriteString(hint)
	}

	// Add did-you-mean suggestions when the tool was not found.
	if looksLikeToolNotFound(errText) && len(toolNames) > 0 {
		if suggestions := DidYouMean(toolName, toolNames, 3); len(suggestions) > 0 {
			b.WriteString("\nDid you mean: ")
			for i, s := range suggestions {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(s)
			}
			b.WriteString("?")
		}
	}

	return b.String()
}

// looksLikeToolNotFound checks whether an error text suggests the tool
// was not found by the routing layer, which is when did-you-mean
// suggestions are most helpful. The phrases here must be specific —
// matching a bare word like "route" would attach suggestions to
// unrelated errors (e.g. a downstream "route table corrupted" or a
// validation message mentioning "the route is required"). 'no matching
// route' is the exact gateway phrase emitted when the router fails to
// resolve a downstream tool, so it's safe to key on it verbatim.
func looksLikeToolNotFound(errText string) bool {
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no route") ||
		strings.Contains(lower, "no matching route") ||
		strings.Contains(lower, "unknown tool") ||
		strings.Contains(lower, "unrecognized") ||
		strings.Contains(lower, "no tool")
}

// schemaParamHint extracts a compact parameter summary from a tool's
// JSON Schema. Includes field names, types, required/optional status,
// descriptions (when short enough), and a usage example if available.
// Returns empty string when the schema has no properties or on parse error.
//
// The hint is budgeted under maxSchemaHintLen — and crucially RESERVES
// enough space for the example BEFORE walking properties, so a schema
// with 50 fields never starves the example out of the cap (the example
// is the highest-leverage hint we can give a cheap model).
func schemaParamHint(schema json.RawMessage, examples []string) string {
	if len(schema) == 0 {
		return ""
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties) == 0 {
		return ""
	}

	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}

	ex := firstExample(examples)
	exampleBlock := ""
	if ex != "" {
		exampleBlock = "\nExample: " + ex
	}
	// Reserve room for the example so a noisy schema can't crowd it out.
	propsBudget := maxSchemaHintLen - len(exampleBlock)
	if propsBudget < 0 {
		propsBudget = 0
	}

	var b strings.Builder
	b.WriteString("Expected parameters:")

	names := orderedSchemaProps(s.Properties, s.Required)
	truncated := false

	for i, name := range names {
		raw := s.Properties[name]
		typeName := extractSchemaType(raw)
		desc := extractSchemaDescription(raw)

		req := "optional"
		if reqSet[name] {
			req = "required"
		}

		line := fmt.Sprintf("\n  - %s (%s, %s)", name, typeName, req)
		if desc != "" && len(desc) < 80 {
			line += " - " + desc
		}
		if b.Len()+len(line) > propsBudget {
			truncated = i < len(names)
			break
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("\n  ...")
	}

	if exampleBlock != "" {
		b.WriteString(exampleBlock)
	}

	result := b.String()
	if len(result) > maxSchemaHintLen {
		prefix := safeUTF8Prefix(result, maxSchemaHintLen)
		result = prefix + "..."
	}
	return result
}

// extractSchemaType returns the human-readable type name from a schema
// property node. Handles string, number, integer, boolean, array, object,
// and enum types.
func extractSchemaType(raw json.RawMessage) string {
	var s struct {
		Type json.RawMessage   `json:"type"`
		Enum []json.RawMessage `json:"enum"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "any"
	}
	if len(s.Enum) > 0 {
		vals := make([]string, 0, len(s.Enum))
		for _, v := range s.Enum {
			var sv string
			if json.Unmarshal(v, &sv) == nil {
				vals = append(vals, sv)
			} else {
				vals = append(vals, string(v))
			}
		}
		if len(vals) <= 4 {
			return "enum(" + strings.Join(vals, "|") + ")"
		}
		return "enum"
	}
	t := parseSchemaType(s.Type)
	switch t {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "any"
	}
}

// parseSchemaType extracts the type name from a JSON Schema "type" field
// which can be a string or an array (e.g. ["string", "null"]).
func parseSchemaType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, t := range arr {
			if t != "null" {
				return t
			}
		}
		if len(arr) > 0 {
			return arr[0]
		}
	}
	return ""
}

// extractSchemaDescription returns the description from a schema property
// node, or empty string if absent.
func extractSchemaDescription(raw json.RawMessage) string {
	var s struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s.Description
}

// firstExample returns the first example from a slice, or empty string.
func firstExample(examples []string) string {
	if len(examples) == 0 {
		return ""
	}
	return examples[0]
}

// synthesizeExample generates a usage example from a JSON Schema when no
// explicit tool examples are provided. Cheap models benefit from seeing the
// expected argument shape spelled out inline when their call fails. Required
// fields are emitted FIRST so the example shows the minimum viable call
// before optional knobs steal byte budget. Returns empty string when the
// schema has no properties or on parse error.
//
// Display rules for the JS call form:
//   - "github__list_issues" → "github.list_issues"
//   - "ns__tool__sub"        → "ns.tool__sub" (split on the FIRST "__"
//     only — every namespace has exactly one prefix, the rest of the
//     name is the tool's own member which may itself contain "__").
//   - "no_ns_separator"      → "_global.no_ns_separator" (matches the
//     typegen.go _global bucket so a model looking at the synthesized
//     example knows the helper is a top-level binding).
func synthesizeExample(toolName string, schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties) == 0 {
		return ""
	}

	displayName := renderJSCallName(toolName)

	ordered := orderedSchemaProps(s.Properties, s.Required)

	args := make([]string, 0, len(ordered))
	total := 0
	for _, name := range ordered {
		ph := schemaPlaceholder(s.Properties[name])
		kv := name + ": " + ph
		if total+len(kv) > maxSynthExampleLen {
			break
		}
		args = append(args, kv)
		total += len(kv) + 2 // +2 for ", "
	}

	if len(args) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(displayName)
	b.WriteString("({")
	b.WriteString(strings.Join(args, ", "))
	b.WriteString("})")
	return b.String()
}

// renderJSCallName converts an MCP-namespaced tool name into the JS form
// the agent would actually type. Splits on the FIRST "__" only so a
// member name like `tool__sub` is preserved verbatim — `ns.tool.sub`
// would be a different identifier in JS and break copy-paste.
func renderJSCallName(toolName string) string {
	if ns, member, ok := strings.Cut(toolName, "__"); ok {
		return ns + "." + member
	}
	return "_global." + toolName
}

// orderedSchemaProps returns property names with required fields first
// (preserving the declared required[] order) and optional fields after
// (sorted alphabetically). A schema with no required[] list collapses
// to a plain alphabetical order so output stays deterministic.
func orderedSchemaProps(props map[string]json.RawMessage, required []string) []string {
	out := make([]string, 0, len(props))
	seen := make(map[string]struct{}, len(props))
	for _, r := range required {
		if _, ok := props[r]; ok {
			if _, dup := seen[r]; dup {
				continue
			}
			seen[r] = struct{}{}
			out = append(out, r)
		}
	}
	optional := make([]string, 0, len(props))
	for name := range props {
		if _, ok := seen[name]; ok {
			continue
		}
		optional = append(optional, name)
	}
	sort.Strings(optional)
	return append(out, optional...)
}

// schemaPlaceholder returns a placeholder value for a schema property node.
// Uses the first enum value when available, otherwise a type-appropriate default.
func schemaPlaceholder(raw json.RawMessage) string {
	if len(raw) == 0 {
		return `"..."`
	}
	var s struct {
		Type json.RawMessage   `json:"type"`
		Enum []json.RawMessage `json:"enum"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return `"..."`
	}
	if len(s.Enum) > 0 {
		var sv string
		if json.Unmarshal(s.Enum[0], &sv) == nil {
			return fmt.Sprintf("%q", sv)
		}
		return string(s.Enum[0])
	}
	typeName := parseSchemaType(s.Type)
	switch typeName {
	case "string":
		return `"..."`
	case "number", "integer":
		return "0"
	case "boolean":
		return "true"
	default:
		return `"..."`
	}
}

// extractToolErrorText pulls human-readable text from an MCP error content array.
func extractToolErrorText(content []map[string]any, raw json.RawMessage) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if text, ok := item["text"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if len(raw) > 0 {
		return string(raw)
	}
	return "tool returned error"
}

// outputCapture stores print output up to a byte budget and tracks how much
// formatted text was omitted after the cap was hit. When the last formatted
// argument was a map, its sorted top-level keys are recorded so the
// truncation notice can hand the agent a cheap shape hint of the lost
// value rather than just saying "your stuff overflowed."
// overflowRetainMaxBytes bounds how much over-cap print output is retained
// for CCR stashing (vs. counted-and-discarded). Keeps one runaway print()
// from holding unbounded memory while making the common truncation case
// fully recoverable via mcpx__retrieve.
const overflowRetainMaxBytes = 1024 * 1024

type outputCapture struct {
	buf          strings.Builder
	maxBytes     int
	truncated    bool
	bytesOmitted int
	lastShape    string // formatted "[keyA, keyB, keyC]" of the most recent map arg
	// overflow retains over-cap bytes (up to overflowRetainMaxBytes) so the
	// gateway can stash the full output in CCR; overflowDropped counts bytes
	// beyond even that retention bound.
	overflow        strings.Builder
	overflowDropped int
}

func newOutputCapture(maxBytes int) *outputCapture {
	return &outputCapture{maxBytes: NormalizeMaxOutputBytes(maxBytes)}
}

// NormalizeMaxOutputBytes applies the sandbox's output-budget defaults and
// hard safety cap.
func NormalizeMaxOutputBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	if maxBytes > HardMaxOutputBytes {
		return HardMaxOutputBytes
	}
	return maxBytes
}

func (o *outputCapture) WriteString(s string) {
	remaining := o.maxBytes - o.buf.Len()
	if remaining <= 0 {
		o.truncated = true
		o.bytesOmitted += len(s)
		o.retainOverflow(s)
		return
	}
	if len(s) <= remaining {
		o.buf.WriteString(s)
		return
	}

	prefix := safeUTF8Prefix(s, remaining)
	o.buf.WriteString(prefix)
	o.truncated = true
	o.bytesOmitted += len(s) - len(prefix)
	o.retainOverflow(s[len(prefix):])
}

// retainOverflow keeps omitted bytes for CCR stashing, up to the retention
// bound; anything past it is counted so the notice can say so.
func (o *outputCapture) retainOverflow(s string) {
	room := overflowRetainMaxBytes - o.overflow.Len()
	if room <= 0 {
		o.overflowDropped += len(s)
		return
	}
	if len(s) <= room {
		o.overflow.WriteString(s)
		return
	}
	prefix := safeUTF8Prefix(s, room)
	o.overflow.WriteString(prefix)
	o.overflowDropped += len(s) - len(prefix)
}

func (o *outputCapture) writeByte(b byte) {
	o.WriteString(string([]byte{b}))
}

func (o *outputCapture) String() string {
	out := o.buf.String()
	if !o.truncated {
		return out
	}
	return strings.TrimRight(out, "\n") + outputTruncationNotice(o.maxBytes, o.bytesOmitted, o.lastShape)
}

// recordShape stores a compact representation of the top-level keys of a
// value the agent was trying to print. Used purely as a context-cheap
// hint inside the truncation notice — never affects the captured output.
func (o *outputCapture) recordShape(v any) {
	if hint := shapeHint(v); hint != "" {
		o.lastShape = hint
	}
}

// shapeHint returns a "[k1, k2, k3]" rendering of a map's top-level keys,
// or "[len=N]" for arrays. Empty for anything else. Used inside truncation
// notices so a model trying to print(bigObject) gets back a useful hint
// about what to drill into instead of the full payload.
func shapeHint(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("[text, bytes=%d]", len(val))
	case map[string]any:
		if isTextProjection(val) {
			if b, ok := val["bytes"].(int); ok {
				return fmt.Sprintf("[kind, text, bytes=%d]", b)
			}
			if b, ok := val["bytes"].(float64); ok {
				return fmt.Sprintf("[kind, text, bytes=%d]", int(b))
			}
			return "[kind, text, bytes]"
		}
		if len(val) == 0 {
			return ""
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		const max = 8
		if len(keys) > max {
			keys = append(keys[:max:max], fmt.Sprintf("+%d more", len(val)-max))
		}
		return "[" + strings.Join(keys, ", ") + "]"
	case []any:
		return fmt.Sprintf("[len=%d]", len(val))
	}
	return ""
}

func (o *outputCapture) Truncated() bool {
	return o.truncated
}

func (o *outputCapture) MaxBytes() int {
	return o.maxBytes
}

func (o *outputCapture) BytesOmitted() int {
	return o.bytesOmitted
}

// Raw returns the captured (displayed) output without the truncation notice.
func (o *outputCapture) Raw() string { return o.buf.String() }

// Overflow returns the retained over-cap bytes (empty when not truncated).
func (o *outputCapture) Overflow() []byte { return []byte(o.overflow.String()) }

// OverflowComplete reports whether Overflow holds ALL omitted bytes.
func (o *outputCapture) OverflowComplete() bool { return o.overflowDropped == 0 }

// outputTruncationNotice is intentionally plain and loud: it is part of the
// text returned to the model, not just logs. code_mode_max_output_bytes is
// a gateway-side setting (not a per-call argument), so we tell the agent
// to slim its print() calls instead of pointing it at a knob it can't
// touch. When the most recent printed value was a map/array, its
// top-level shape is appended so the agent can pick which fields to keep.
func outputTruncationNotice(limit, omitted int, shape string) string {
	advice := "print only the fields you need (use compact() to prune, then print top-N or counts instead of the full payload)."
	if shape != "" {
		advice += " Lost value top-level shape: " + shape
	}
	return truncationNotice("code-mode print output", limit, omitted, advice)
}

// TruncateText returns s capped to maxBytes with an explicit truncation notice.
// It is shared by the gateway's final response formatter for non-print fields
// such as execution errors and failed-call summaries.
func TruncateText(s string, maxBytes int, label string) string {
	maxBytes = NormalizeMaxOutputBytes(maxBytes)
	if len(s) <= maxBytes {
		return s
	}
	prefix := safeUTF8Prefix(s, maxBytes)
	return strings.TrimRight(prefix, "\n") + truncationNotice(
		label, maxBytes, len(s)-len(prefix),
		"Reduce the returned text (print only the fields you need).",
	)
}

func truncationNotice(label string, limit, omitted int, advice string) string {
	return fmt.Sprintf(
		"\n... [truncated: %s exceeded %s (%d bytes); omitted at least %s (%d bytes). %s]",
		label, formatByteCount(limit), limit, formatByteCount(omitted), omitted, advice,
	)
}

func formatByteCount(n int) string {
	if n%1024 == 0 {
		return fmt.Sprintf("%d KiB", n/1024)
	}
	return fmt.Sprintf("%d bytes", n)
}

func safeUTF8Prefix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if maxBytes >= len(s) {
		return s
	}
	for maxBytes > 0 && isUTF8ContinuationByte(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func isUTF8ContinuationByte(b byte) bool {
	return b&0xC0 == 0x80
}

// makePrintFunc returns a Goja-compatible function that captures output.
func makePrintFunc(vm *goja.Runtime, mu *sync.Mutex, output *outputCapture) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		mu.Lock()
		for i, arg := range call.Arguments {
			if i > 0 {
				output.WriteString(" ")
			}
			// Record a shape hint BEFORE formatting so a truncated
			// large object still surfaces its top-level keys to the
			// agent in the truncation notice.
			if arg != nil && !goja.IsUndefined(arg) && !goja.IsNull(arg) {
				output.recordShape(arg.Export())
			}
			output.WriteString(formatPrintArg(vm, arg))
		}
		output.writeByte('\n')
		mu.Unlock()
		return goja.Undefined()
	}
}

// formatPrintArg converts a Goja value to a readable string.
func formatPrintArg(vm *goja.Runtime, arg goja.Value) string {
	if arg == nil || goja.IsUndefined(arg) || goja.IsNull(arg) {
		return arg.String()
	}
	if source, trust, ok := untrustedMetaForValue(vm, arg); ok {
		exported := arg.Export()
		data, err := json.Marshal(exported)
		if err != nil {
			return envelopeUntrustedForPrint(source, trust, arg.String())
		}
		return envelopeUntrustedForPrint(source, trust, string(data))
	}
	exported := arg.Export()
	switch v := exported.(type) {
	case string:
		if len(v) > textPreviewMax {
			return formatTextProjection(projectTextValue(v))
		}
		return arg.String()
	case float64, int64, bool:
		return arg.String()
	case map[string]any:
		if isTextProjection(v) {
			return formatTextProjection(v)
		}
		return formatMapCompact(v)
	case []any:
		if s := formatArrayAsTable(v); s != "" {
			return s
		}
		data, err := json.Marshal(exported)
		if err != nil {
			return arg.String()
		}
		return string(data)
	default:
		data, err := json.Marshal(exported)
		if err != nil {
			return arg.String()
		}
		return string(data)
	}
}

// formatArrayAsTable converts arrays of objects to pipe-delimited tables.
func formatArrayAsTable(items []any) string {
	if len(items) < 3 {
		return ""
	}
	maps := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return ""
		}
		maps = append(maps, m)
	}
	columnar := compact.CompactArray(maps)
	return compact.FormatColumnar(columnar)
}

// formatMapCompact renders a map for print(): nested large arrays become
// tables, everything else is exact JSON. It does NOT prune — printed output
// is what the model reads, and silently hiding null/empty/cursor fields
// misleads it (a printed page without next_cursor looks like the last page).
// Agents that want pruning call the documented compact() helper explicitly.
func formatMapCompact(m map[string]any) string {
	// Check if any value is a large array — if so, use sectioned format.
	hasLargeArray := false
	for _, v := range m {
		if arr, ok := v.([]any); ok && len(arr) >= 3 {
			hasLargeArray = true
			break
		}
	}

	if !hasLargeArray {
		data, err := json.Marshal(m)
		if err != nil {
			return fmt.Sprintf("%v", m)
		}
		return string(data)
	}

	// Sectioned format: render each key as a labeled section.
	var b strings.Builder
	for k, v := range m {
		switch arr := v.(type) {
		case []any:
			if tbl := formatArrayAsTable(arr); tbl != "" {
				fmt.Fprintf(&b, "## %s\n%s\n", k, tbl)
				continue
			}
		}
		data, err := json.Marshal(v)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", k, string(data))
	}
	return b.String()
}

// compactValue prunes and applies columnar compaction to a JS value.
// Used by the compact() sandbox helper.
func compactValue(exported any) any {
	switch v := exported.(type) {
	case map[string]any:
		pruned := compact.PruneObject(v)
		// Recursively compact nested values.
		for k, val := range pruned {
			pruned[k] = compactValue(val)
		}
		return pruned
	case []any:
		// Try columnar compaction for arrays of objects.
		if len(v) >= 3 {
			maps := make([]map[string]any, 0, len(v))
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					maps = append(maps, compact.PruneObject(m))
				} else {
					// Mixed array — just prune elements.
					out := make([]any, len(v))
					for i, el := range v {
						out[i] = compactValue(el)
					}
					return out
				}
			}
			return compact.CompactArray(maps)
		}
		out := make([]any, len(v))
		for i, el := range v {
			out[i] = compactValue(el)
		}
		return out
	case string:
		return projectTextValue(v)
	default:
		return v
	}
}

// isTimeoutError checks if a Goja error was caused by an interrupt.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "execution timeout") ||
		strings.Contains(msg, "context deadline exceeded")
}

// isMemoryLimitError checks whether the error came from the memory watchdog.
func isMemoryLimitError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "execution memory limit exceeded")
}
