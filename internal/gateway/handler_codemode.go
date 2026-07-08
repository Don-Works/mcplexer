package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/codemode"
	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
	"github.com/don-works/mcplexer/internal/workers/writeclass"
	"github.com/google/uuid"
)

type internalCallKey struct{}
type executionIDKey struct{}
type skillIDKey struct{}
type skillAllowlistKey struct{}
type workerToolAllowlistKey struct{}
type workerCapabilityKey struct{}

var internalCodeModeCallKey = internalCallKey{}

func withInternalCodeModeCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalCodeModeCallKey, true)
}

func isInternalCodeModeCall(ctx context.Context) bool {
	internal, _ := ctx.Value(internalCodeModeCallKey).(bool)
	return internal
}

func withExecutionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, executionIDKey{}, id)
}

func executionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(executionIDKey{}).(string)
	return id
}

// withSkillID tags a context as belonging to an executing skill. Tool calls
// dispatched under this context are subject to the skill's capability
// allowlist (see withSkillAllowlist).
func withSkillID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, skillIDKey{}, id)
}

// skillIDFromContext returns the skill identifier set by withSkillID, or
// the empty string when no skill context is attached.
func skillIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(skillIDKey{}).(string)
	return id
}

// withSkillAllowlist attaches a skill's declared namespace allowlist to the
// context. The list is a copy of the skill manifest's MCPServers names. An
// empty (but non-nil) slice means "no downstream namespaces allowed".
func withSkillAllowlist(ctx context.Context, allowed []string) context.Context {
	if allowed == nil {
		return ctx
	}
	cp := make([]string, len(allowed))
	copy(cp, allowed)
	return context.WithValue(ctx, skillAllowlistKey{}, cp)
}

// skillAllowlistFromContext returns the namespace allowlist set by
// withSkillAllowlist, or nil when no skill context is attached.
func skillAllowlistFromContext(ctx context.Context) []string {
	allow, _ := ctx.Value(skillAllowlistKey{}).([]string)
	return allow
}

// WithWorkerToolAllowlist attaches the configured worker tool patterns to
// the context before a worker invokes mcpx__execute_code. The gateway then
// checks each sandbox-dispatched inner tool call against these patterns.
// Nil means "no worker allowlist configured"; an empty slice means
// "configured to deny every downstream tool".
func WithWorkerToolAllowlist(ctx context.Context, allowed []string) context.Context {
	if allowed == nil {
		return ctx
	}
	cp := make([]string, len(allowed))
	copy(cp, allowed)
	return context.WithValue(ctx, workerToolAllowlistKey{}, cp)
}

func workerToolAllowlistFromContext(ctx context.Context) []string {
	allow, _ := ctx.Value(workerToolAllowlistKey{}).([]string)
	return allow
}

func checkWorkerToolAllowlist(ctx context.Context, toolName string) error {
	allowlist := workerToolAllowlistFromContext(ctx)
	if allowlist == nil {
		return nil
	}
	for _, pattern := range allowlist {
		if toolPatternMatches(pattern, toolName) {
			return nil
		}
	}
	return fmt.Errorf("tool %q is not in worker allowlist", toolName)
}

func filterByWorkerToolAllowlist(ctx context.Context, tools []Tool) []Tool {
	allowlist := workerToolAllowlistFromContext(ctx)
	if allowlist == nil {
		return tools
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		for _, pattern := range allowlist {
			if toolPatternMatches(pattern, tool.Name) {
				out = append(out, tool)
				break
			}
		}
	}
	return out
}

// WithWorkerCapabilityProfile attaches a resolved capability profile to the
// context before a delegate worker invokes mcpx__execute_code. The gateway
// enforces it at the dispatch chokepoint (checkWorkerCapability) and mirrors
// it at discovery (filterByWorkerCapability). A nil profile attaches NOTHING
// — exactly like WithWorkerToolAllowlist's nil contract — so an interactive
// or non-delegate session is never gated (back-compat allow-all).
func WithWorkerCapabilityProfile(ctx context.Context, p *toolgate.CapabilityProfile) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, workerCapabilityKey{}, p)
}

func workerCapabilityFromContext(ctx context.Context) *toolgate.CapabilityProfile {
	p, _ := ctx.Value(workerCapabilityKey{}).(*toolgate.CapabilityProfile)
	return p
}

// checkWorkerCapability is the authoritative call-side gate. A nil profile
// allows everything (back-compat). The write-class flag is computed here via
// the shared writeclass heuristic and passed into toolgate.Allows so the
// gate stays the single source of truth while toolgate stays cycle-free.
func checkWorkerCapability(ctx context.Context, toolName string) error {
	profile := workerCapabilityFromContext(ctx)
	if profile == nil {
		return nil
	}
	allowed, reason := profile.Allows(toolName, writeclass.IsWriteClass(toolName))
	if allowed {
		return nil
	}
	return fmt.Errorf("tool %q blocked by capability profile: %s", toolName, reason)
}

// filterByWorkerCapability is the discovery-side mirror of
// checkWorkerCapability. List-side filtering is intentionally weak (UX/token
// economy); the call-side check is the real boundary. Both delegate to the
// SAME toolgate.Allows so they cannot drift.
func filterByWorkerCapability(ctx context.Context, tools []Tool) []Tool {
	profile := workerCapabilityFromContext(ctx)
	if profile == nil {
		return tools
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if allowed, _ := profile.Allows(tool.Name, writeclass.IsWriteClass(tool.Name)); allowed {
			out = append(out, tool)
		}
	}
	return out
}

func toolPatternMatches(pattern, toolName string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == toolName {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	ok, err := path.Match(pattern, toolName)
	return err == nil && ok
}

// handlerToolCaller adapts the gateway handler to the codemode.ToolCaller
// interface, routing each tool call through the full pipeline.
type handlerToolCaller struct {
	handler *handler
}

func (c *handlerToolCaller) CallTool(
	ctx context.Context, name string, args json.RawMessage,
) (json.RawMessage, error) {
	req := CallToolRequest{
		Name:      name,
		Arguments: args,
	}

	params, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal tool call: %w", err)
	}

	result, rpcErr := c.handler.handleToolsCall(ctx, params)
	if rpcErr != nil {
		return nil, fmt.Errorf("tool %s: %s", name, rpcErr.Message)
	}

	return result, nil
}

// handleCodeExecute runs user-provided code in a Goja sandbox with tool
// namespaces bound as synchronous function calls. When the caller has
// already attached a skill context (via withSkillID + withSkillAllowlist)
// it is preserved and the per-call allowlist check in handleToolsCall
// will gate the dispatched tool calls.
func (h *handler) handleCodeExecute(
	ctx context.Context, code string,
) (json.RawMessage, *RPCError) {
	// Strip TypeScript annotations to produce valid JS.
	jsCode := codemode.StripTypeScript(code)

	if issues := codemode.Preflight(jsCode); len(issues) > 0 {
		result := &codemode.ExecutionResult{
			OutputMaxBytes: h.codeModeMaxOutputBytes(ctx),
			Error:          codemode.FormatPreflightIssues(issues),
		}
		return marshalCodeResult(result), nil
	}

	timeout := h.codeModeTimeout(ctx)

	toolDefs, err := h.codeModeToolDefs(ctx)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("gather tools for code mode: %v", err),
		}
	}

	// Run lint checks before execution to catch common mistakes early.
	// LintWithTools needs the registered tool names so it can flag
	// typo'd namespace/member calls (e.g. `gihub.list_issues(...)`) with
	// did-you-mean suggestions BEFORE Goja sees the code — a bare
	// ReferenceError otherwise dies with no actionable hint.
	toolNames := make([]string, len(toolDefs))
	for i, t := range toolDefs {
		toolNames[i] = t.Name
	}
	lintResult := codemode.LintWithTools(jsCode, toolNames)
	lintText := codemode.FormatLintWarnings(lintResult.Warnings)

	// Generate a unique execution ID to correlate all tool calls
	// from this single execute_code invocation in the audit log.
	execID := uuid.NewString()

	caller := &handlerToolCaller{handler: h}
	sandbox := codemode.NewSandbox(caller, timeout)
	sandbox.SetMaxOutputBytes(h.codeModeMaxOutputBytes(ctx))
	sandbox.SetMaxHeapGrowthMB(h.codeModeMaxHeapGrowthMB(ctx))

	// Enable the ephemeral per-session `session` object when we have a stable
	// MCP session id: rehydrate the prior snapshot so user code can reuse
	// values built in an earlier call this session.
	sessionID := h.sessions.sessionID()
	if sessionID != "" {
		sandbox.SetSessionState(h.loadSessionState(sessionID), h.codeModeSessionStateMaxBytes(ctx))
	}

	// Skill context (if any) flows through ctx already — withSkillID and
	// withSkillAllowlist are set by the API entrypoint before this call.
	execCtx := withExecutionID(withInternalCodeModeCall(ctx), execID)
	result, err := sandbox.Execute(execCtx, jsCode, toolDefs)
	if err != nil {
		return nil, h.trackAndAnnotateError(&RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("code execution failed: %v", err),
		})
	}

	// Persist the post-run `session` snapshot for the next call, and surface
	// any warning (over cap / non-serializable value) to the agent.
	if sessionID != "" {
		h.saveSessionState(sessionID, result.SessionState)
	}
	if result.SessionStateWarning != "" {
		if result.Output != "" {
			result.Output += "\n" + result.SessionStateWarning
		} else {
			result.Output = result.SessionStateWarning
		}
	}

	slog.Info("code mode execution complete",
		"tool_calls", len(result.ToolCalls),
		"output_len", len(result.Output),
		"error", result.Error,
	)

	// Prepend lint warnings to the output when present.
	if lintText != "" {
		if result.Output != "" {
			result.Output = lintText + "\n" + result.Output
		} else {
			result.Output = lintText
		}
	}

	// Make the print-output cap recoverable: stash the full stream in CCR and
	// point the truncation notice at it. Must run BEFORE applyCompression so
	// the kill-switch sees a resolvable marker.
	h.stashOverflowOutput(ctx, result)

	// Format the result as MCP tool output, then compress the OUTPUT the model
	// will read. This is the code-mode compression seam: the downstream results
	// consumed inside the sandbox are intentionally left uncompressed (for JS
	// iterability), but the final execute_code output is fair game and is the
	// bulk of what a slim-surface harness ever puts into model context.
	out := marshalCodeResult(result)
	return h.applyCompressionForTool(ctx, "mcpx__execute_code", out), nil
}

// stashOverflowOutput converts the hard print-output cap into a recoverable
// truncation (T-F, 2026-07 audit): the full print stream (displayed prefix +
// retained overflow) is stashed in CCR and a marker is appended to the
// truncation notice so the model can expand it on demand. Before this, capped
// bytes were counted and discarded — the one hard-lossy seam left in code
// mode. No marker is emitted unless the stash write succeeded.
func (h *handler) stashOverflowOutput(ctx context.Context, result *codemode.ExecutionResult) {
	if result == nil || !result.OutputTruncated || len(result.OutputOverflow) == 0 {
		return
	}
	full := append([]byte(result.OutputRaw), result.OutputOverflow...)
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	key, ok := h.ccrPut(pctx, full)
	if !ok {
		return
	}
	note := "\n" + compression.CCRMarker(key, len(full))
	if !result.OutputOverflowComplete {
		note += " (retention cap reached: the stashed copy holds the first " +
			fmt.Sprintf("%d", len(full)) + " bytes; the rest was discarded)"
	}
	result.Output += note
}

// gatherCodeModeTools collects all tools available through execute_code.
func (h *handler) gatherCodeModeTools(ctx context.Context) ([]Tool, error) {
	servers, err := h.store.ListDownstreamServers(ctx)
	if err != nil {
		return nil, err
	}

	var (
		staticServers  []store.DownstreamServer
		dynamicServers []store.DownstreamServer
		namespaces     = make(map[string]string, len(servers))
		allTools       []Tool
	)

	for _, srv := range servers {
		// Internal-transport servers expose tools via downstream.InternalBackend;
		// the manager's ListTools handles that uniformly with stdio servers.
		// Previously these were skipped here, which left the mcplexer__* /
		// telegram__* / secret__* namespaces invisible to mcpx__execute_code.
		namespaces[srv.ID] = srv.ToolNamespace
		if srv.Discovery == "dynamic" {
			dynamicServers = append(dynamicServers, srv)
		} else {
			staticServers = append(staticServers, srv)
		}
	}

	collect := func(serverGroup []store.DownstreamServer) error {
		if len(serverGroup) == 0 {
			return nil
		}

		serverIDs := make([]string, 0, len(serverGroup))
		for _, srv := range serverGroup {
			serverIDs = append(serverIDs, srv.ID)
		}

		liveTools, err := h.cachedListToolsForServers(ctx, serverIDs)
		if err != nil {
			return err
		}

		for _, srv := range serverGroup {
			rawResult, ok := liveTools[srv.ID]
			if !ok {
				if len(srv.CapabilitiesCache) > 0 && string(srv.CapabilitiesCache) != "{}" {
					rawResult = srv.CapabilitiesCache
				} else {
					continue
				}
			} else if err := h.store.UpdateCapabilitiesCache(ctx, srv.ID, rawResult); err != nil {
				slog.Warn("failed to update capabilities cache",
					"server", srv.ID, "error", err)
			}

			ns := namespaces[srv.ID]
			tools, err := extractNamespacedTools(ns, rawResult)
			if err != nil {
				slog.Warn("failed to extract code mode tools",
					"server", srv.ID, "error", err)
				continue
			}
			allTools = append(allTools, tools...)
		}

		return nil
	}

	if err := collect(staticServers); err != nil {
		return nil, err
	}
	if err := collect(dynamicServers); err != nil {
		return nil, err
	}

	if h.addonRegistry != nil {
		allTools = append(allTools, addonToolDefinitions(h.addonRegistry)...)
	}
	allTools = append(allTools, h.codeModeBuiltinTools()...)

	seen := make(map[string]struct{})
	var filtered []Tool
	for _, t := range allTools {
		if _, ok := seen[t.Name]; ok {
			continue
		}
		seen[t.Name] = struct{}{}
		filtered = append(filtered, t)
	}

	filtered = h.filterByWorkspaceRoutes(ctx, filtered)
	filtered = h.applyToolHints(ctx, filtered)

	return filtered, nil
}

// applyToolHints appends hint text from settings to matching tool descriptions.
func (h *handler) applyToolHints(ctx context.Context, tools []Tool) []Tool {
	if h.settingsSvc == nil {
		return tools
	}
	hints := h.settingsSvc.Load(ctx).ToolHints
	if len(hints) == 0 {
		return tools
	}
	for i, t := range tools {
		if hint, ok := hints[t.Name]; ok && hint != "" {
			if tools[i].Description != "" {
				tools[i].Description += " " + hint
			} else {
				tools[i].Description = hint
			}
		}
	}
	return tools
}

func (h *handler) codeModeToolDefs(ctx context.Context) ([]codemode.ToolDef, error) {
	tools, err := h.gatherCodeModeTools(ctx)
	if err != nil {
		return nil, err
	}
	// Include searchable builtins so they're callable through execute_code.
	tools = append(tools, h.searchableBuiltins(ctx)...)
	tools = dedupeToolsByName(tools)
	tools = filterByWorkerCapability(ctx, tools)
	tools = h.filterAdminToolsForContext(ctx, tools)
	return toolsToToolDefs(tools), nil
}

// toolsToToolDefs converts gateway Tools to codemode ToolDefs.
func toolsToToolDefs(tools []Tool) []codemode.ToolDef {
	defs := make([]codemode.ToolDef, len(tools))
	for i, t := range tools {
		defs[i] = codemode.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Examples:    extractExamples(t),
		}
	}
	return defs
}

// extractExamples pulls x-examples from a tool's Extras map.
// Supports both a JSON array of strings and a single string.
func extractExamples(t Tool) []string {
	if t.Extras == nil {
		return nil
	}
	raw, ok := t.Extras["x-examples"]
	if !ok {
		return nil
	}
	var examples []string
	if err := json.Unmarshal(raw, &examples); err == nil && len(examples) > 0 {
		return examples
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	return nil
}

// splitNamespace splits "namespace__name" into its parts.
func splitNamespace(name string) (string, string, bool) {
	for i := 0; i < len(name)-1; i++ {
		if name[i] == '_' && name[i+1] == '_' {
			return name[:i], name[i+2:], true
		}
	}
	return "", name, false
}

// marshalCodeResult formats an ExecutionResult as MCP CallToolResult.
func marshalCodeResult(result *codemode.ExecutionResult) json.RawMessage {
	var content []ToolContent

	if result.Output != "" {
		out := result.Output
		if !result.OutputTruncated {
			out = codemode.TruncateText(out, outputLimitForResult(result), "code-mode output")
		}
		content = append(content, ToolContent{
			Type: "text",
			Text: out,
		})
	}

	if result.Error != "" {
		content = append(content, ToolContent{
			Type: "text",
			Text: fmt.Sprintf("Error: %s",
				codemode.TruncateText(
					result.Error, outputLimitForResult(result), "code-mode error")),
		})
	}

	// Add summary of tool calls.
	if len(result.ToolCalls) > 0 {
		summary := formatToolCallSummary(result.ToolCalls, outputLimitForResult(result))
		content = append(content, ToolContent{
			Type: "text",
			Text: summary,
		})
	}

	if len(content) == 0 {
		content = append(content, ToolContent{
			Type: "text",
			Text: "Code executed successfully with no output.",
		})
	}

	callResult := CallToolResult{
		Content: content,
		IsError: result.Error != "",
	}

	data, _ := json.Marshal(callResult)
	return data
}

// maxArgsInSummary is the max length of serialized arguments shown in the
// tool call summary for failed calls.
const maxArgsInSummary = 200

// formatToolCallSummary creates a compact summary of tool calls.
// Failed calls include arguments and error details so the LLM can
// identify what went wrong without re-running the script.
func formatToolCallSummary(calls []codemode.ToolCallRecord, textLimit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n--- %d tool call(s) executed ---", len(calls))
	for i, call := range calls {
		if call.Error != "" {
			args := string(call.Args)
			if len(args) > maxArgsInSummary {
				args = args[:maxArgsInSummary] + "..."
			}
			fmt.Fprintf(&b, "\n%d. %s FAILED (%dms)",
				i+1, call.Name, call.Duration.Milliseconds())
			fmt.Fprintf(&b, "\n   args: %s", args)
			fmt.Fprintf(&b, "\n   error: %s",
				codemode.TruncateText(call.Error, textLimit, "failed tool error"))
		} else {
			fmt.Fprintf(&b, "\n%d. %s (ok, %dms)",
				i+1, call.Name, call.Duration.Milliseconds())
		}
	}
	return b.String()
}

func outputLimitForResult(result *codemode.ExecutionResult) int {
	if result != nil && result.OutputMaxBytes > 0 {
		return result.OutputMaxBytes
	}
	return codemode.DefaultMaxOutputBytes
}

// codeModeTimeout returns the configured timeout for code execution.
func (h *handler) codeModeTimeout(ctx context.Context) time.Duration {
	timeout := 30 // default
	if h.settingsSvc != nil {
		if t := h.settingsSvc.Load(ctx).CodeModeTimeoutSec; t > 0 {
			timeout = t
		}
	}
	return time.Duration(timeout) * time.Second
}

func (h *handler) codeModeMaxOutputBytes(ctx context.Context) int {
	limit := codemode.DefaultMaxOutputBytes
	if h.settingsSvc != nil {
		if n := h.settingsSvc.Load(ctx).CodeModeMaxOutputBytes; n > 0 {
			limit = n
		}
	}
	return codemode.NormalizeMaxOutputBytes(limit)
}

func (h *handler) codeModeMaxHeapGrowthMB(ctx context.Context) int {
	limit := codemode.DefaultMaxHeapGrowthMB
	if h.settingsSvc != nil {
		if n := h.settingsSvc.Load(ctx).CodeModeMaxHeapGrowthMB; n > 0 {
			limit = n
		}
	}
	if limit > codemode.HardMaxHeapGrowthMB {
		return codemode.HardMaxHeapGrowthMB
	}
	return limit
}

// codeModeSessionStateMaxBytes caps the total serialized size of the ephemeral
// per-session `session` object snapshotted between execute_code calls.
func (h *handler) codeModeSessionStateMaxBytes(ctx context.Context) int {
	limit := 4 * 1024 * 1024
	if h.settingsSvc != nil {
		if n := h.settingsSvc.Load(ctx).CodeModeSessionStateMaxBytes; n > 0 {
			limit = n
		}
	}
	return limit
}

// loadSessionState returns a copy of the ephemeral `session` state for an MCP
// session, or nil when none exists. The copy isolates the gateway's map from
// sandbox mutation.
func (h *handler) loadSessionState(sessionID string) map[string]json.RawMessage {
	if sessionID == "" {
		return nil
	}
	h.sessionStateMu.RLock()
	defer h.sessionStateMu.RUnlock()
	cur := h.sessionState[sessionID]
	if len(cur) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(cur))
	for k, v := range cur {
		out[k] = v
	}
	return out
}

// saveSessionState replaces the ephemeral `session` state for an MCP session
// with the post-run snapshot. A nil snapshot is ignored (prior state kept); an
// empty non-nil snapshot clears the session's state (all keys were deleted).
func (h *handler) saveSessionState(sessionID string, snapshot map[string]json.RawMessage) {
	if sessionID == "" || snapshot == nil {
		return
	}
	h.sessionStateMu.Lock()
	defer h.sessionStateMu.Unlock()
	if len(snapshot) == 0 {
		delete(h.sessionState, sessionID)
		return
	}
	if h.sessionState == nil {
		h.sessionState = make(map[string]map[string]json.RawMessage)
	}
	h.sessionState[sessionID] = snapshot
}

// clearSessionState drops all ephemeral `session` state for an MCP session.
// Called on disconnect so memory isn't retained for dead sessions.
func (h *handler) clearSessionState(sessionID string) {
	if sessionID == "" {
		return
	}
	h.sessionStateMu.Lock()
	defer h.sessionStateMu.Unlock()
	delete(h.sessionState, sessionID)
}

func (h *handler) codeModeBuiltinTools() []Tool {
	var tools []Tool
	tools = append(tools, searchToolsDefinition())
	tools = append(tools, reloadServerToolDefinition())
	tools = append(tools, whoamiToolDefinition())
	if h.addonCreator != nil {
		tools = append(tools, createAddonToolDefinition())
	}
	tools = append(tools, importOpenAPIToolDefinition())
	if h.approvals != nil {
		tools = append(tools, approvalToolDefinitions()...)
	}
	if h.mesh != nil {
		tools = append(tools, meshToolDefinitions()...)
	}
	if h.skillShare != nil {
		tools = append(tools, skillShareToolDefinitions()...)
	}
	if h.registryShare != nil {
		tools = append(tools, hubSyncToolDefinitions()...)
		tools = append(tools, skillPushToolDefinitions()...)
	}
	if h.memorySvc != nil {
		tools = append(tools, memoryToolDefinitions(h.memoryToolCapabilities())...)
	}
	if h.store != nil {
		tools = append(tools, dataToolDefinitions()...)
		tools = append(tools, kvToolDefinitions()...)
		tools = append(tools, indexToolDefinitions()...)
	}
	if h.tasksSvc != nil {
		tools = append(tools, taskToolDefinitions()...)
	}
	if h.monitoringQry != nil {
		tools = append(tools, monitoringNamespaceToolDefinitions()...)
	}
	if h.brainEditor != nil {
		tools = append(tools, brainToolDefinitions()...)
	}
	if h.conciergeSvc != nil {
		tools = append(tools, conciergeToolDefinitions()...)
	}
	if h.secretPrompts != nil {
		tools = append(tools, secretPromptToolDefinition())
	}
	if h.secretsManager != nil {
		tools = append(tools, secretListRefsToolDefinition())
	}
	if _, ok := h.manager.(CachingCaller); ok {
		tools = append(tools, flushCacheToolDefinition())
	}
	if _, ok := h.manager.(downstreamEventReader); ok {
		tools = append(tools, downstreamEventToolDefinitions()...)
	}
	return tools
}

// buildCodeExecuteTool generates the execute_code tool definition with a
// compact namespace summary. Never embeds the full TypeScript API — agents
// should call search_tools for signatures. Returns the tool and false
// (API is never embedded).
func (h *handler) buildCodeExecuteTool(ctx context.Context) (Tool, bool) {
	nsSummary := h.buildNamespaceSummary(ctx)

	var description string
	if nsSummary == "" {
		description = "Execute JavaScript code that batches multiple tool calls into one invocation. " +
			"Use it for downstream MCP calls and lightweight JavaScript compute such as calculations, " +
			"data transforms, parsing, and polling loops. Calls are synchronous (no await). " +
			"sleep(ms) performs a context-aware wait (clamped to 60s per call). print() returns output.\n\n" +
			"IMPORTANT — batching and results:\n" +
			"- ALWAYS batch related calls into a single script. Never make multiple " +
			"execute_code calls when one script can do the job.\n" +
			"- Results are auto-unwrapped: an MCP `{content:[{text:JSON}],isError:false}` " +
			"envelope returns the parsed object directly. Read `result.id`, NEVER " +
			"`JSON.parse(result.content[0].text)`. Non-JSON text comes back as a string; " +
			"isError throws.\n" +
			"- NEVER print raw API responses. Filter, map, or summarize inside the " +
			"script before printing. Extract only the fields you need.\n" +
			"- For lists: print counts, top-N items, or key fields — not full arrays.\n" +
			"- Use compact(obj) to prune nulls/empties from large objects.\n" +
			"- Build an expensive dataset once, then reuse it across calls instead of recomputing: " +
			"assign to the `session` object (session.x = ...) to reuse it in a LATER call in THIS " +
			"session (in-memory, lost on disconnect/restart; only JSON-serializable values survive, " +
			"and top-level const/let do NOT persist — assign to session.x). For DURABLE, " +
			"workspace-scoped values that survive restarts and other sessions, use the kv namespace — " +
			"kv.set({key, value}) then kv.get({key}).\n" +
			"- print()/console.log output is capped server-side and marked when truncated.\n\n" +
			"Safety limits: execution has a wall-clock timeout, output cap, and a heap-growth watchdog " +
			"so one script cannot exhaust daemon memory. Use paging/filtering instead of materializing " +
			"large raw result sets.\n\n" +
			"Calling patterns:\n" +
			"- Sequential (default): calls return values directly, so you can " +
			"daisy-chain — pass the result of one call straight into the next.\n" +
			"- Concurrent: use parallel([{tool,args},...]) when calls are independent (max 20 per call). " +
			"Failed entries in a parallel() batch surface as `null` at their index — the call itself does NOT throw.\n\n" +
			"Search search_tools for function signatures before writing code, or call help() in the " +
			"script to list namespaces and help('namespace') for a namespace's tool signatures (no search round-trip).\n" +
			"Errors throw real `Error` objects. A typo on a namespace or member yields a `ReferenceError` " +
			"(e.g. `gihub is not defined`) annotated with a did-you-mean. A successful dispatch that the " +
			"downstream rejects throws `\"Tool call failed: {tool}\\nArguments: ...\\nError: ...\"`. " +
			"Wrap risky calls in try/catch. Execution itself has a wall-clock timeout (default 30s), " +
			"and chained sleep(ms) calls still count against that budget."
	} else {
		description = "Execute JavaScript code that batches multiple tool calls into one invocation. " +
			"Use it for downstream MCP calls and lightweight JavaScript compute such as calculations, " +
			"data transforms, parsing, and polling loops. Calls are synchronous (no await). " +
			"sleep(ms) performs a context-aware wait (clamped to 60s per call). print() returns output.\n" +
			"Available: " + nsSummary + ".\n\n" +
			"IMPORTANT — batching and results:\n" +
			"- ALWAYS batch related calls into a single script. Never make multiple " +
			"execute_code calls when one script can do the job.\n" +
			"- Results are auto-unwrapped: an MCP `{content:[{text:JSON}],isError:false}` " +
			"envelope returns the parsed object directly. Read `result.id`, NEVER " +
			"`JSON.parse(result.content[0].text)`. Non-JSON text comes back as a string; " +
			"isError throws.\n" +
			"- NEVER print raw API responses. Filter, map, or summarize inside the " +
			"script before printing. Extract only the fields you need.\n" +
			"- For lists: print counts, top-N items, or key fields — not full arrays.\n" +
			"- Use compact(obj) to prune nulls/empties from large objects.\n" +
			"- Build an expensive dataset once, then reuse it across calls instead of recomputing: " +
			"assign to the `session` object (session.x = ...) to reuse it in a LATER call in THIS " +
			"session (in-memory, lost on disconnect/restart; only JSON-serializable values survive, " +
			"and top-level const/let do NOT persist — assign to session.x). For DURABLE, " +
			"workspace-scoped values that survive restarts and other sessions, use the kv namespace — " +
			"kv.set({key, value}) then kv.get({key}).\n" +
			"- print()/console.log output is capped server-side and marked when truncated.\n\n" +
			"Safety limits: execution has a wall-clock timeout, output cap, and a heap-growth watchdog " +
			"so one script cannot exhaust daemon memory. Use paging/filtering instead of materializing " +
			"large raw result sets.\n\n" +
			"Calling patterns:\n" +
			"- Sequential (default): calls return values directly, so you can " +
			"daisy-chain — pass the result of one call straight into the next.\n" +
			"  e.g. const repo = github.get_repo({owner, repo}); const issues = github.list_issues({owner, repo: repo.name})\n" +
			"- Concurrent: use parallel([{tool,args},...]) when calls are independent (max 20 per call) " +
			"and don't depend on each other's results. Returns an array of results; failed entries surface " +
			"as `null` at their index — parallel() itself does NOT throw.\n\n" +
			"Search search_tools for function signatures before writing code, or call help() in the " +
			"script to list namespaces and help('namespace') for a namespace's tool signatures (no search round-trip).\n" +
			"Errors throw real `Error` objects. A typo on a namespace or member yields a `ReferenceError` " +
			"(e.g. `gihub is not defined`) annotated with a did-you-mean. A successful dispatch that the " +
			"downstream rejects throws `\"Tool call failed: {tool}\\nArguments: ...\\nError: ...\"`. " +
			"Wrap risky calls in try/catch. Execution itself has a wall-clock timeout (default 30s), " +
			"and chained sleep(ms) calls still count against that budget."
	}

	return Tool{
		Name:        "mcpx__execute_code",
		Description: description,
		InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"code": {
						"type": "string",
						"description": "JavaScript code to execute. Use for batched tool calls and lightweight compute (calculations, transforms, parsing, polling loops). ALWAYS batch ALL related tool calls into one script — never make multiple execute_code calls when one script can do the job. Calls are synchronous (no await), return values directly, and can be daisy-chained. sleep(ms) waits without busy-looping, is clamped to 60s per call, and respects the wall-clock execution timeout. NEVER print raw API responses — filter, map, or summarize before printing. For lists: print counts, top-N, or key fields only. Use compact(obj) to prune nulls/empties. print()/console.log output is capped server-side and marked when truncated. Typos in namespace/member names surface as ReferenceError with a did-you-mean. parallel() failures surface as null entries — it never throws."
					}
				},
			"required": ["code"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Execute Code",
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		}),
	}, false
}

// buildNamespaceSummary produces a compact, sorted "github, memory, slack"
// summary of available tool namespaces. Deliberately NO per-namespace tool
// counts: Anthropic-style prompt caches invalidate on ANY byte change to a
// tool definition, so embedding live counts meant one downstream tool
// appearing anywhere re-billed the whole cached prefix. Names change only
// when a namespace is genuinely added or removed — a change worth paying
// for. Exact counts remain available in-sandbox via help().
func (h *handler) buildNamespaceSummary(ctx context.Context) string {
	tools, err := h.gatherCodeModeTools(ctx)
	if err != nil || len(tools) == 0 {
		return ""
	}

	seen := make(map[string]bool)
	names := make([]string, 0, 32)
	for _, t := range tools {
		ns, _, ok := splitNamespace(t.Name)
		if !ok {
			ns = t.Name
		}
		if !seen[ns] {
			seen[ns] = true
			names = append(names, ns)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
