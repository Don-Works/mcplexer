package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
)

func (h *handler) handleToolsList(
	ctx context.Context,
) (json.RawMessage, *RPCError) {
	// Pure Mode drops the entire advertised MCP surface so clients fall
	// back to their native local file/shell tools.
	if h.pureModeEnabled(ctx) {
		return marshalEmptyToolsList(), nil
	}

	// tools/list returns only builtins (execute_code + search_tools + approval/cache).
	// Downstream tools are accessible through execute_code's sandbox.
	tools := h.buildAllBuiltinTools(ctx)

	tools = dedupeToolsByName(tools)

	// Slim-surface filter: keep only the 4 hand-picked entrypoints in
	// the static tools/list response. Everything else remains callable
	// (handleToolsCall dispatches by prefix) and discoverable (via
	// searchableBuiltins → mcpx__search_tools).
	if h.slimSurfaceEnabled(ctx) {
		tools = filterToSlimSurface(tools)
	}

	// Only advertise tools the current session can actually route to.
	tools = h.filterByWorkspaceRoutes(ctx, tools)

	// Hide admin tools when the session's CWD is outside the data dir.
	// See AdminCWDGate for the rule and rationale.
	if h.adminGate != nil {
		tools = h.adminGate.FilterAdminTools(tools, h.sessions.clientRoot(), h.sessions.workspaceRoots())
	}

	// Apply description overrides from settings.
	tools = h.applyDescriptionOverrides(ctx, tools)

	// Minify schemas to reduce context window consumption.
	if h.slimToolsEnabled(ctx) {
		tools = minifyToolSchemas(tools)
	}

	// Server-prefixed harnesses (Grok, Cursor, …) qualify tools as
	// {server}__{name}. Slim-surface keepers already contain "__", so
	// advertise single-segment aliases for those clients only.
	tools = applyHarnessToolListNames(h.harnessProfile(), tools)

	result := map[string]any{"tools": tools}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return data, nil
}

// extractNamespacedTools parses a tools/list result and prefixes tool names.
func extractNamespacedTools(namespace string, toolsResult json.RawMessage) ([]Tool, error) {
	if len(toolsResult) == 0 || string(toolsResult) == "{}" {
		return nil, nil
	}

	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(toolsResult, &result); err != nil {
		return nil, err
	}

	out := make([]Tool, 0, len(result.Tools))
	for _, t := range result.Tools {
		t.Name = namespace + "__" + t.Name
		out = append(out, t)
	}
	return out, nil
}

// cachedListToolsForServers uses the tools/list cache to avoid hammering
// downstream servers on rapid tools/list calls. The cache has a 15s TTL.
//
// On the very first call (cold cache), it tries to return DB-cached
// CapabilitiesCache instantly and refreshes live data in the background,
// sending a tools/list_changed notification when done.
func (h *handler) cachedListToolsForServers(ctx context.Context, serverIDs []string) (map[string]json.RawMessage, error) {
	key := strings.Join(serverIDs, ",")

	cached, err := h.toolsListCache.GetOrLoad(key, func() (json.RawMessage, error) {
		// Try DB-cached capabilities for instant response on startup.
		dbCached := h.buildFromDBCache(ctx, serverIDs)
		if len(dbCached) > 0 {
			// Per-key once gating ensures static and dynamic groups each
			// get their own first-shot refresh; the async refresh picks up
			// any newly-seeded servers missing from the DB cache and emits
			// tools/list_changed when done.
			h.triggerBackgroundRefresh(key, serverIDs)
			data, err := json.Marshal(dbCached)
			return data, err
		}

		// No DB cache (first-ever run) — query only transports that are safe
		// to probe without spawning local child processes.
		liveIDs, skippedAutoStart := h.liveCatalogDiscoveryServerIDs(ctx, serverIDs)
		if skippedAutoStart > 0 {
			slog.Info("tools/list cold refresh: skipping auto-start-unsafe servers",
				"cache_key", key,
				"skipped_auto_start_unsafe", skippedAutoStart,
			)
		}
		if len(liveIDs) == 0 {
			data, err := json.Marshal(map[string]json.RawMessage{})
			return data, err
		}
		result, err := h.manager.ListToolsForServers(ctx, liveIDs)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(result)
		return data, err
	})
	if err != nil {
		return nil, err
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(cached, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// buildFromDBCache returns CapabilitiesCache from the DB for each server
// that has a non-empty cache. Returns nil if no servers have cached data.
func (h *handler) buildFromDBCache(ctx context.Context, serverIDs []string) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(serverIDs))
	for _, id := range serverIDs {
		srv, err := h.store.GetDownstreamServer(ctx, id)
		if err != nil || srv == nil {
			continue
		}
		if len(srv.CapabilitiesCache) > 0 && string(srv.CapabilitiesCache) != "{}" {
			result[id] = srv.CapabilitiesCache
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (h *handler) liveCatalogDiscoveryServerIDs(ctx context.Context, serverIDs []string) ([]string, int) {
	liveIDs := make([]string, 0, len(serverIDs))
	skippedAutoStart := 0
	for _, id := range serverIDs {
		srv, err := h.store.GetDownstreamServer(ctx, id)
		if err != nil || srv == nil {
			continue
		}
		if srv.Disabled {
			continue
		}
		if downstream.IsAutoStartUnsafeServer(*srv) {
			skippedAutoStart++
			continue
		}
		liveIDs = append(liveIDs, id)
	}
	return liveIDs, skippedAutoStart
}

// toolInputStringFields returns the set of top-level input-schema fields
// declared as type: "string" for a given downstream tool, using the cached
// tools/list catalog. Returns nil for built-ins or when the schema is
// unavailable — callers should fall back to legacy unconditional coercion.
func (h *handler) toolInputStringFields(ctx context.Context, serverID, originalToolName string) map[string]bool {
	if serverID == "" || originalToolName == "" {
		return nil
	}
	// Built-in namespaces aren't in the downstream catalog; skip.
	if _, ok := builtinDownstreamIDs[serverID]; ok {
		return nil
	}

	catalog, err := h.cachedListToolsForServers(ctx, []string{serverID})
	if err != nil {
		return nil
	}
	raw, ok := catalog[serverID]
	if !ok || len(raw) == 0 {
		return nil
	}
	var listing struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil
	}
	for _, t := range listing.Tools {
		if t.Name == originalToolName {
			return stringFieldsFromInputSchema(t.InputSchema)
		}
	}
	return nil
}

// backgroundRefreshInterval bounds how often a given server-group catalog is
// re-introspected from live downstreams. The first trigger for a key fires
// immediately (upgrading the instant DB-cache response to live data); subsequent
// triggers wait out this interval. This is what lets a redeployed/restarted
// downstream's new tools become visible without a manual reload_server, while
// keeping introspection load bounded.
const backgroundRefreshInterval = 60 * time.Second

// triggerBackgroundRefresh queries downstream servers live, updates the DB and
// in-memory caches, and sends a tools/list_changed notification ONLY when the
// live catalog differs from the cached one. It re-arms on backgroundRefreshInterval
// (not once-ever) so a downstream that changes its tool surface after restart is
// picked up; bgRefreshInFlight collapses concurrent triggers for the same key.
func (h *handler) triggerBackgroundRefresh(cacheKey string, serverIDs []string) {
	h.bgRefreshMu.Lock()
	if h.bgRefreshInFlight[cacheKey] {
		h.bgRefreshMu.Unlock()
		return
	}
	if last, ok := h.bgRefreshAt[cacheKey]; ok && time.Since(last) < backgroundRefreshInterval {
		h.bgRefreshMu.Unlock()
		return
	}
	h.bgRefreshInFlight[cacheKey] = true
	h.bgRefreshAt[cacheKey] = time.Now()
	h.bgRefreshMu.Unlock()

	h.bgCtxMu.RLock()
	ctx := h.bgCtx
	h.bgCtxMu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	h.bgWg.Add(1)
	go func() {
		defer h.bgWg.Done()
		defer func() {
			h.bgRefreshMu.Lock()
			h.bgRefreshInFlight[cacheKey] = false
			h.bgRefreshMu.Unlock()
		}()

		liveIDs, skippedAutoStart := h.liveCatalogDiscoveryServerIDs(ctx, serverIDs)
		if len(liveIDs) == 0 {
			if skippedAutoStart > 0 {
				slog.Info("background refresh: skipped auto-start-unsafe servers",
					"cache_key", cacheKey,
					"skipped_auto_start_unsafe", skippedAutoStart,
				)
			}
			return
		}
		slog.Info("background refresh: querying downstream servers",
			"cache_key", cacheKey,
			"servers", len(liveIDs),
			"skipped_auto_start_unsafe", skippedAutoStart,
		)

		started := time.Now()
		result, err := h.manager.ListToolsForServers(ctx, liveIDs)
		elapsed := time.Since(started)
		if err != nil {
			slog.Warn("background refresh failed",
				"error", err,
				"elapsed_ms", elapsed.Milliseconds(),
			)
			return
		}

		changed := false
		for serverID, rawResult := range result {
			if !h.capabilitiesUnchanged(ctx, serverID, rawResult) {
				changed = true
			}
			if err := h.store.UpdateCapabilitiesCache(ctx, serverID, rawResult); err != nil {
				slog.Warn("background refresh: failed to update capabilities cache",
					"server", serverID, "error", err)
			}
		}

		h.toolsListCache.Flush()
		// Only nudge the client to re-fetch when the surface actually moved, so a
		// steady catalog does not spam tools/list_changed every interval.
		if changed {
			h.sendToolsListChanged()
		}

		// Catalog refresh visibility: log the wall-clock cost alongside how many
		// servers actually responded vs. were asked. The gap between `servers`
		// (input) and `responded` (output) is the signal that one or more
		// downstreams timed out.
		slog.Info("background refresh complete",
			"cache_key", cacheKey,
			"asked", len(liveIDs),
			"responded", len(result),
			"changed", changed,
			"elapsed_ms", elapsed.Milliseconds(),
		)
	}()
}

// capabilitiesUnchanged reports whether a server's freshly-introspected tool
// surface matches what is already in the DB CapabilitiesCache. It compares an
// order-insensitive signature of the tool entries so reordering does not count as
// a change while an added/removed tool or an altered input schema does.
func (h *handler) capabilitiesUnchanged(ctx context.Context, serverID string, newRaw json.RawMessage) bool {
	srv, err := h.store.GetDownstreamServer(ctx, serverID)
	if err != nil || srv == nil {
		return false
	}
	return toolsetSignature(srv.CapabilitiesCache) == toolsetSignature(newRaw)
}

// toolsetSignature returns an order-insensitive signature of a {"tools":[...]}
// payload: the tool entries sorted and joined. Falls back to the raw bytes when
// the shape is unexpected.
func toolsetSignature(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return string(raw)
	}
	entries := make([]string, 0, len(parsed.Tools))
	for _, t := range parsed.Tools {
		entries = append(entries, string(t))
	}
	sort.Strings(entries)
	return strings.Join(entries, "\x1f")
}

// sendToolsListChanged sends a tools/list_changed notification if a notifier is available.
func (h *handler) sendToolsListChanged() {
	h.notifierMu.RLock()
	n := h.notifier
	h.notifierMu.RUnlock()
	if n == nil {
		return
	}
	if err := n.Notify("notifications/tools/list_changed", nil); err != nil {
		slog.Warn("failed to send tools/list_changed notification", "error", err)
	}
}

// ToolsListStats returns cache statistics for the tools/list cache.
func (h *handler) ToolsListStats() cache.Stats {
	return h.toolsListCache.Stats()
}

func (h *handler) handleToolsCall(
	ctx context.Context, params json.RawMessage,
) (json.RawMessage, *RPCError) {
	start := time.Now()

	var req CallToolRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	if h.pureModeEnabled(ctx) {
		rpcErr := &RPCError{
			Code:    CodeInvalidRequest,
			Message: "mcplexer is in Pure Mode (MCP tools disabled). Use the harness's native Read/Edit/Bash/Glob/Grep tools, or turn Pure Mode off in settings.",
		}
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, nil, rpcErr, start)
		return nil, rpcErr
	}

	// Normalize harness-qualified names (mcplexer__execute_code,
	// mcplexer__mcpx__execute_code, …) and legacy mcplexer__ aliases.
	req.Name = resolveHarnessToolName(req.Name)

	// Block direct external calls to downstream tools — only the sandbox
	// (internal code-mode calls) and builtins are allowed.
	if !isInternalCodeModeCall(ctx) && !isBuiltinTool(req.Name) {
		result := marshalErrorResult(
			"Direct tool calls are disabled. Use mcpx__execute_code to call downstream tools.",
		)
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, result, nil, start)
		return result, nil
	}

	// Worker allowlist enforcement: workers expose only search_tools and
	// execute_code at the model layer, so the configured downstream allowlist
	// must be checked here on the sandbox's inner tool calls.
	if isInternalCodeModeCall(ctx) {
		if err := checkWorkerToolAllowlist(ctx, req.Name); err != nil {
			rpcErr := &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, nil, rpcErr, start)
			return nil, rpcErr
		}
		// Capability profile enforcement (delegation scoping): the real
		// gate on a delegate's reachable surface. Composes with the
		// allowlist above — both must pass, so a profile can only narrow.
		if err := checkWorkerCapability(ctx, req.Name); err != nil {
			rpcErr := &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, nil, rpcErr, start)
			return nil, rpcErr
		}
	}

	// Defence-in-depth: even if a tool call slipped past tools/list filtering
	// (e.g. agent had a stale tool inventory cached, or hand-crafted a
	// JSON-RPC call), refuse admin tools when the session's CWD is outside
	// the data directory. The tools/list filter is the primary gate; this is
	// the belt to that's braces. In-process worker calls bypass — they're
	// not coming from an external client crossing the JSON-RPC boundary
	// the gate is designed to police; they're dispatched by the trusted
	// runner inside the daemon, with the operator's per-worker allowlist
	// already applied.
	if h.adminGate != nil &&
		IsAdminTool(req.Name) &&
		!IsInProcessWorkerCall(ctx) &&
		!h.sessions.isAdminTrusted() &&
		!h.adminGate.IsAdminContext(h.sessions.clientRoot(), h.sessions.workspaceRoots()) {
		rpcErr := &RPCError{
			Code: CodeInvalidRequest,
			Message: fmt.Sprintf(
				"tool %q is admin-only and must be invoked from inside the MCPlexer data directory (e.g. ~/.mcplexer)",
				req.Name,
			),
		}
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, nil, rpcErr, start)
		return nil, rpcErr
	}

	// Skill capability enforcement: when the call originates from inside a
	// skill (skill_id attached to ctx), reject any namespace not declared in
	// the manifest. Built-in mcpx__/mesh__ namespaces are always permitted.
	if err := checkSkillAllowlist(ctx, req.Name); err != nil {
		rpcErr := &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, nil, rpcErr, start)
		h.recordSkillInvocation(ctx, req.Name, false)
		return nil, rpcErr
	}
	// Allowed call from inside a skill — log it so the per-skill activity
	// feed shows everything the skill attempted, not just denials.
	if skillIDFromContext(ctx) != "" {
		h.recordSkillInvocation(ctx, req.Name, true)
	}

	// Extract namespace from tool name (namespace__toolname).
	originalTool := extractOriginalToolName(req.Name)
	fuzzyOriginal := ""

	// Route ALL tools through the engine (including built-ins).
	routeResult, err := h.engine.RouteWithFallback(ctx, routing.RouteContext{
		ToolName: req.Name,
	}, h.routingClientRoot(ctx), h.routingWorkspaceAncestors(ctx))
	if err != nil {
		// For internal code-mode calls, try fuzzy matching on route failure.
		if isInternalCodeModeCall(ctx) {
			if corrected, ok := h.tryFuzzyToolRecovery(ctx, req.Name); ok {
				slog.Info("fuzzy tool name recovery",
					"original", req.Name, "corrected", corrected)
				// SECURITY: fuzzy recovery rewrites req.Name to a real tool
				// AFTER the initial allowlist + capability gates ran on the
				// (typo'd) original name. A restrictive profile/allowlist must
				// re-gate the CORRECTED name here, before re-routing/dispatch —
				// otherwise a delegate could reach a denied tool (e.g.
				// mcpx__delegate_worker) via a near-miss typo that the
				// restrictive gate never saw. Both gates compose (allowlist AND
				// capability); either denial returns immediately, no dispatch.
				if denyErr := checkWorkerToolAllowlist(ctx, corrected); denyErr != nil {
					rpcErr := &RPCError{Code: CodeInvalidParams, Message: denyErr.Error()}
					h.recordAuditBlocked(ctx, corrected, req.Arguments, nil, nil, rpcErr, start)
					return nil, rpcErr
				}
				if denyErr := checkWorkerCapability(ctx, corrected); denyErr != nil {
					rpcErr := &RPCError{Code: CodeInvalidParams, Message: denyErr.Error()}
					h.recordAuditBlocked(ctx, corrected, req.Arguments, nil, nil, rpcErr, start)
					return nil, rpcErr
				}
				fuzzyOriginal = req.Name
				req.Name = corrected
				originalTool = extractOriginalToolName(corrected)
				routeResult, err = h.engine.RouteWithFallback(ctx, routing.RouteContext{
					ToolName: corrected,
				}, h.routingClientRoot(ctx), h.routingWorkspaceAncestors(ctx))
			}
		}
		if err != nil {
			rpcErr := mapRouteError(err)
			h.recordAuditBlocked(ctx, req.Name, req.Arguments, nil, nil, rpcErr, start)
			return nil, rpcErr
		}
	}

	if rpcErr := h.enforceWorkerRouteAccess(ctx, req.Name, originalTool, routeResult); rpcErr != nil {
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
		return nil, rpcErr
	}

	// Coerce stringified JSON arguments (LLMs often pass objects as strings).
	// Respect the downstream tool's input schema: any field declared as
	// type: "string" is left alone, so tools like Excalidraw's create_view
	// (whose `elements` is a JSON-array-string by contract) round-trip
	// correctly instead of being silently re-parsed into an array.
	stringFields := h.toolInputStringFields(ctx, routeResult.DownstreamServerID, originalTool)
	req.Arguments = coerceStringifiedArgs(req.Arguments, stringFields)

	// Dispatch based on whether it's a built-in or downstream tool.
	if _, ok := builtinDownstreamIDs[routeResult.DownstreamServerID]; ok {
		result, rpcErr := h.handleBuiltinCall(ctx, req)
		if rpcErr == nil {
			if fuzzyOriginal != "" {
				result = injectFuzzyCorrectionMeta(result, fuzzyOriginal, req.Name)
			}
			// structuredContent lift: skip for trusted builtins. Their
			// text body IS already the canonical payload (a JSON-stringified
			// object the agent will parse), so duplicating it as
			// structuredContent doubles the wire size without giving the
			// agent anything new — the text path is already cheap and
			// clean (no envelope, no HTML entities). The lifter targets
			// downstream tools whose text is bloated by HTML escaping.
			if !isTrustedBuiltinResult(req.Name) {
				result = surfaceStructuredContent(result)
			}
			// Piggyback mesh notices on successful builtin results (skip mesh tools themselves).
			if !isMeshTool(req.Name) {
				result = h.piggybackMeshNotice(ctx, result)
			}
		}
		h.recordAudit(ctx, req.Name, req.Arguments, routeResult, result, rpcErr, start)
		return result, rpcErr
	}

	// Generic scope policy enforcement from route allowlists.
	if extractor := h.scopeRegistry.Get(req.Name); extractor != nil {
		policy, err := NewScopePolicy(routeResult.ScopePolicy)
		if err != nil {
			rpcErr := &RPCError{
				Code:    CodeInvalidParams,
				Message: fmt.Sprintf("invalid route scope policy: %v", err),
			}
			h.recordAudit(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
			return nil, rpcErr
		}
		if policy.Enabled() {
			extracted := extractor.Extract(req.Arguments)
			if err := policy.Enforce(extracted); err != nil {
				rpcErr := &RPCError{
					Code:    CodeInvalidParams,
					Message: err.Error(),
				}
				h.recordAudit(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
				return nil, rpcErr
			}
		}
	}

	// Look up server name for clearer error messages.
	serverName := routeResult.DownstreamServerID
	if serverName == "" {
		rpcErr := &RPCError{
			Code:    CodeInternalError,
			Message: "matched route has no downstream server configured",
		}
		h.recordAudit(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
		return nil, rpcErr
	}
	if srv, err := h.store.GetDownstreamServer(ctx, routeResult.DownstreamServerID); err == nil && srv != nil {
		serverName = srv.Name
	}

	// Two-phase approval interception.
	if routeResult.ApprovalMode != "" && routeResult.ApprovalMode != "none" && h.approvals != nil {
		needsApproval := true
		if routeResult.ApprovalMode == "write" {
			needsApproval = !h.isReadOnlyTool(ctx, routeResult.DownstreamServerID, originalTool)
		}
		if needsApproval {
			result, rpcErr := h.handleApprovalGate(ctx, req, routeResult, start)
			if result != nil || rpcErr != nil {
				return result, rpcErr
			}
			// Approval granted — fall through to dispatch.
		}
	}

	// Intercept addon tool calls — execute as direct REST API calls
	// instead of forwarding to the downstream MCP server.
	if h.addonRegistry != nil && h.addonExecutor != nil {
		if addonTool := h.addonRegistry.GetTool(req.Name); addonTool != nil {
			// Use the addon's own auth scope if configured, otherwise fall back to route's.
			addonAuthScope := routeResult.AuthScopeID
			if addonTool.AuthScopeID != "" {
				addonAuthScope = addonTool.AuthScopeID
			}
			result, callErr := h.addonExecutor.Execute(
				ctx, addonTool, addonAuthScope, req.Arguments,
			)
			if callErr != nil {
				rpcErr := &RPCError{
					Code:    CodeProcessError,
					Message: fmt.Sprintf("addon %s: %v", req.Name, callErr),
				}
				h.recordAudit(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
				return nil, rpcErr
			}
			h.recordAudit(ctx, req.Name, req.Arguments, routeResult, result, nil, start)
			return result, nil
		}
	}

	// Stamp the per-agent isolation id so browser-automation downstreams
	// (ShouldIsolatePerSession) get their own process per logical agent
	// instead of all sessions sharing one stateful browser. Interactive and
	// socket clients carry the MCP session id here; in-process worker calls
	// already set a "worker:<id>" value upstream (the worker-bound gateway's
	// session is uninitialized), so only fill the gap — never clobber it.
	if downstream.BrowserSessionIDFromContext(ctx) == "" {
		if sid := h.sessions.sessionID(); sid != "" {
			ctx = downstream.WithBrowserSessionID(ctx, sid)
		}
	}

	// Extract _cache_bust from arguments if present.
	cacheBust := extractAndRemoveCacheBust(&req.Arguments)

	// Dispatch to downstream, with cache hit detection.
	var result json.RawMessage
	var cacheHit bool
	var cacheAge time.Duration

	if cc, ok := h.manager.(CachingCaller); ok {
		cr, callErr := cc.CallWithMeta(
			ctx,
			routeResult.DownstreamServerID,
			routeResult.AuthScopeID,
			originalTool,
			req.Arguments,
			cacheBust,
		)
		if callErr != nil {
			rpcErr := h.trackAndAnnotateError(&RPCError{
				Code:    CodeProcessError,
				Message: formatDownstreamError(serverName, callErr),
			})
			h.recordAudit(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
			return nil, rpcErr
		}
		result = cr.Data
		cacheHit = cr.CacheHit
		cacheAge = cr.CacheAge
	} else {
		var callErr error
		result, callErr = h.manager.Call(
			ctx,
			routeResult.DownstreamServerID,
			routeResult.AuthScopeID,
			originalTool,
			req.Arguments,
		)
		if callErr != nil {
			rpcErr := h.trackAndAnnotateError(&RPCError{
				Code:    CodeProcessError,
				Message: formatDownstreamError(serverName, callErr),
			})
			h.recordAudit(ctx, req.Name, req.Arguments, routeResult, nil, rpcErr, start)
			return nil, rpcErr
		}
	}

	// Inject cache metadata into the tool result.
	result = injectCacheMeta(result, cacheHit, cacheAge)
	if fuzzyOriginal != "" {
		result = injectFuzzyCorrectionMeta(result, fuzzyOriginal, req.Name)
	}

	// Sanitize untrusted tool output (M1 Guards): scan for injection
	// markers, wrap in <untrusted-content> envelope on hit. Must run
	// BEFORE compaction — compacting an enveloped string is fine, but
	// compaction can alter text in ways that would break the IsEnveloped
	// short-circuit on a subsequent pass.
	result = h.sanitizeToolResult(ctx, result, req.Name)

	// Compact verbose JSON responses to reduce token consumption.
	//
	// Skip for internal code-mode calls: CompactToolResult turns an
	// array-of-objects into the columnar {_cols,_rows,_fixed} shape, which is
	// great for a model reading a tools/call result directly but breaks JS
	// consumers inside mcpx__execute_code — `result.map`, `result.filter`,
	// `result[i].field`, and `Array.isArray(result)` all fail on a columnar
	// object. The sandbox must receive a naturally iterable plain array. Token
	// economy on the code-mode path is handled downstream and on demand:
	// `compactForSandbox` still prunes nulls/empties (more aggressively — it
	// also strips pagination keys) before the value reaches JS, and `print()`
	// / the opt-in `compact()` helper re-derive the columnar table from the
	// plain value at render time. So skipping here costs no tokens the agent
	// didn't ask to spend; it only keeps the consumed VALUE iterable.
	if h.compactResponseEnabled(ctx) && !isInternalCodeModeCall(ctx) {
		result = h.compactor.CompactToolResult(result)
	}

	// Lift JSON-shaped text into structuredContent BEFORE piggyback
	// (piggyback adds a 2nd content block which would defeat the
	// "exactly one text block" gate in the lifter). Skipped automatically
	// when the text was enveloped by the sanitize stage above — the
	// envelope marker is load-bearing and must stay in the text slot.
	result = surfaceStructuredContent(result)

	// Token-compression pipeline (measure-first). In shadow/dry-run mode this
	// only MEASURES each candidate transform's would-be saving and returns the
	// result unchanged — zero accuracy/latency risk to the answer; in on mode
	// it applies proven lossless transforms. Skipped for internal code-mode
	// calls under the same iterability contract as the compactor above.
	if h.compression != nil && !isInternalCodeModeCall(ctx) {
		compressed, obs := h.compression.Process(h.compressionMode(ctx), result)
		result = compressed
		h.recordCompression(obs)
		h.persistCompression(ctx, obs)
	}

	// Piggyback mesh notices on successful downstream results.
	result = h.piggybackMeshNotice(ctx, result)

	// Record success to reset the error struggle tracker.
	h.errTracker.RecordSuccess()

	h.recordAuditWithCache(ctx, req.Name, req.Arguments, routeResult, result, nil, start, cacheHit)
	return result, nil
}

// compactResponseEnabled checks settings to decide whether to compact
// verbose tool responses.
func (h *handler) compactResponseEnabled(ctx context.Context) bool {
	if h.settingsSvc != nil {
		return h.settingsSvc.Load(ctx).CompactResponses
	}
	return true
}

// slimToolsEnabled checks settings (then env var fallback) to decide
// whether to minify tool schemas.
func (h *handler) slimToolsEnabled(ctx context.Context) bool {
	if h.settingsSvc != nil {
		return h.settingsSvc.Load(ctx).SlimTools
	}
	return slimToolsEnabled()
}

// slimSurfaceEnabled checks settings (then env var fallback) to decide
// whether to restrict the static tools/list response to the keep-list.
// Defaults true. When on, ~58 mcplexer-namespaced workflow tools are
// removed from tools/list and become discoverable only via mcpx__search_tools.
func (h *handler) slimSurfaceEnabled(ctx context.Context) bool {
	if h.settingsSvc != nil {
		return h.settingsSvc.Load(ctx).SlimSurface
	}
	return slimSurfaceEnvEnabled()
}

// pureModeEnabled checks settings (then env var fallback) to decide
// whether to hide the MCP tool surface and deny every dispatch.
func (h *handler) pureModeEnabled(ctx context.Context) bool {
	if h.settingsSvc != nil {
		return h.settingsSvc.Load(ctx).PureMode
	}
	return pureModeEnvEnabled()
}

func marshalEmptyToolsList() json.RawMessage {
	data, _ := json.Marshal(map[string]any{"tools": []Tool{}})
	return data
}

// buildAllBuiltinTools assembles the full set of mcplexer built-in tool
// definitions (the unfiltered tools/list payload). Extracted so both
// handleToolsList (which may slim it) and searchableBuiltins (which uses
// it to enumerate slim-mode deferred tools for discovery) can share one
// source of truth.
func (h *handler) buildAllBuiltinTools(ctx context.Context) []Tool {
	tools := make([]Tool, 0, 64)

	execTool, _ := h.buildCodeExecuteTool(ctx)
	tools = append(tools, execTool)
	tools = append(tools, searchToolsDefinition())
	tools = append(tools, recipeSearchToolDef())
	tools = append(tools, recipeStatsToolDef())
	tools = append(tools, contextCostStatsToolDefinition())
	tools = append(tools, reloadServerToolDefinition())
	if h.addonCreator != nil {
		tools = append(tools, createAddonToolDefinition())
	}
	tools = append(tools, importOpenAPIToolDefinition())
	if h.addonCreator != nil && h.secretPrompts != nil && h.secretsManager != nil {
		tools = append(tools, provisionMCPToolDefinition())
	}

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
	if h.skillRegistry != nil {
		tools = append(tools, skillRegistryToolDefinitions()...)
	}
	if h.memorySvc != nil {
		tools = append(tools, memoryToolDefinitions(h.memoryToolCapabilities())...)
	}
	if h.store != nil {
		tools = append(tools, dataToolDefinitions()...)
	}
	if h.tasksSvc != nil {
		tools = append(tools, taskToolDefinitions()...)
		tools = append(tools, taskAdminToolDefinitions()...)
	}
	if h.workerAdmin != nil {
		tools = append(tools, delegationToolDefinitions()...)
	}
	if h.conciergeSvc != nil {
		tools = append(tools, conciergeToolDefinitions()...)
	}
	if h.brainEditor != nil {
		tools = append(tools, brainToolDefinitions()...)
	}
	// Skill telemetry tools (W2). Always-on — the SkillRunStore is part
	// of the universal store surface and these tools only write to it
	// plus optionally to the task service.
	tools = append(tools, skillRunsToolDefinitions()...)
	// Skill refinement tools (W3). Always-on for the same reason:
	// SkillRefinementStore is part of the universal store surface, and
	// the only mutation the tool performs is appending a proposal row
	// (plus an optional mesh-finding broadcast when quorum hits).
	tools = append(tools, skillRefinementToolDefinitions()...)
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

// filterToSlimSurface keeps only the slim-surface keep-list. See
// slimSurfaceKeepers (schema.go) for the rationale of which 4 tools
// stay in the static tools/list response.
func filterToSlimSurface(tools []Tool) []Tool {
	out := make([]Tool, 0, len(slimSurfaceKeepers))
	for _, t := range tools {
		if isSlimSurfaceKeeper(t.Name) {
			out = append(out, t)
		}
	}
	return out
}

func dedupeToolsByName(tools []Tool) []Tool {
	seen := make(map[string]struct{}, len(tools))
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if _, ok := seen[t.Name]; ok {
			continue
		}
		seen[t.Name] = struct{}{}
		out = append(out, t)
	}
	return out
}

// applyDescriptionOverrides applies refined descriptions from the version
// table, then overlays admin overrides from settings (admin always wins).
func (h *handler) applyDescriptionOverrides(ctx context.Context, tools []Tool) []Tool {
	// Layer 1: apply model-refined descriptions.
	refined := h.loadRefinedDescriptions(ctx)
	for i, t := range tools {
		if desc, ok := refined[t.Name]; ok && desc != "" {
			tools[i].Description = desc
		}
	}

	// Layer 2: admin overrides from settings take precedence.
	if h.settingsSvc == nil {
		return tools
	}
	overrides := h.settingsSvc.Load(ctx).ToolDescriptionOverrides
	for i, t := range tools {
		if desc, ok := overrides[t.Name]; ok && desc != "" {
			tools[i].Description = desc
		}
	}
	return tools
}

// loadRefinedDescriptions returns cached active descriptions, refreshing on first call.
func (h *handler) loadRefinedDescriptions(ctx context.Context) map[string]string {
	h.refinedDescsMu.RLock()
	if h.refinedDescs != nil {
		descs := h.refinedDescs
		h.refinedDescsMu.RUnlock()
		return descs
	}
	h.refinedDescsMu.RUnlock()

	h.refinedDescsMu.Lock()
	defer h.refinedDescsMu.Unlock()
	// Double-check after acquiring write lock.
	if h.refinedDescs != nil {
		return h.refinedDescs
	}

	descs, err := h.store.GetActiveDescriptions(ctx)
	if err != nil {
		slog.Warn("load refined descriptions", "error", err)
		descs = map[string]string{}
	}
	h.refinedDescs = descs
	return descs
}

// invalidateRefinedDescriptions clears the cached refined descriptions so
// the next tools/list call re-reads from the database.
func (h *handler) invalidateRefinedDescriptions() {
	h.refinedDescsMu.Lock()
	h.refinedDescs = nil
	h.refinedDescsMu.Unlock()
}

// extractOriginalToolName strips the namespace prefix.
func extractOriginalToolName(namespacedTool string) string {
	if _, after, ok := strings.Cut(namespacedTool, "__"); ok {
		return after
	}
	return namespacedTool
}

// isBuiltinTool returns true if the tool is a MCPlexer builtin (mcpx__,
// mesh__, secret__, email__, memory__, or task__ prefix). secret__ is a
// built-in even though it isn't dispatched through CodeMode — it must
// work outside the sandbox so that the agent's request synchronously
// blocks on user input. memory__ and task__ are built-ins so the
// universal cross-workspace surfaces work both as direct MCP dispatches
// AND inside the execute_code sandbox.
func isBuiltinTool(name string) bool {
	normalized := normalizeBuiltinName(name)
	return strings.HasPrefix(normalized, BuiltinPrefix) ||
		strings.HasPrefix(normalized, MeshPrefix) ||
		strings.HasPrefix(normalized, SecretPrefix) ||
		strings.HasPrefix(normalized, EmailPrefix) ||
		strings.HasPrefix(normalized, MemoryPrefix) ||
		strings.HasPrefix(normalized, TaskPrefix) ||
		strings.HasPrefix(normalized, SkillPrefix)
}

// builtinDownstreamIDs is the set of synthetic downstream_server IDs
// used to mark a route_rule as resolving to an in-process builtin
// dispatcher rather than an external MCP server. Kept here so the
// dispatch switch + the string-field exemption switch stay in sync.
var builtinDownstreamIDs = map[string]struct{}{
	"mcpx-builtin":   {},
	"mesh-builtin":   {},
	"secret-builtin": {},
	"email-builtin":  {},
	"memory-builtin": {},
	"task-builtin":   {},
	"skill-builtin":  {},
	"brain-builtin":  {},
	"data-builtin":   {},
	"kv-builtin":     {},
}

// isMeshTool returns true if the tool is a mesh tool (mesh__ prefix).
func isMeshTool(name string) bool {
	return strings.HasPrefix(name, MeshPrefix)
}

// trackAndAnnotateError records an error in the struggle tracker and appends
// pattern-specific guidance to the RPC error message if the threshold is exceeded.
func (h *handler) trackAndAnnotateError(rpcErr *RPCError) *RPCError {
	if rpcErr == nil {
		return nil
	}
	if h.errTracker.RecordError(rpcErr.Message) {
		rpcErr.Message += h.errTracker.Guidance()
	}
	return rpcErr
}

// formatDownstreamError produces a human-readable error message for downstream
// failures, including the server name and actionable hints where possible.
func formatDownstreamError(serverName string, err error) string {
	msg := err.Error()

	// Per-call deadline path: surface a stable "stuck server" message
	// regardless of which transport produced the cancel (HTTP context
	// cancellation, stdio response-channel closure, etc).
	if errors.Is(err, downstream.ErrCallTimeout) {
		return fmt.Sprintf("%s server did not respond within the per-call deadline. The server may be wedged; mcplexer will auto-reload it shortly. Retry in a few seconds.", serverName)
	}

	// Extract the root cause from wrapped error chains.
	root := msg
	for _, prefix := range []string{
		"get or start instance: ",
		"start instance: ",
		"start process: ",
		"initialize: ",
	} {
		if idx := strings.LastIndex(root, prefix); idx >= 0 {
			root = root[idx+len(prefix):]
		}
	}

	// Provide actionable hints for common failures.
	switch {
	case strings.Contains(root, "exec:"):
		// e.g. exec: "npx": executable file not found in $PATH
		return fmt.Sprintf("%s server failed to start: %s — ensure the required command is installed and in PATH", serverName, root)
	case strings.Contains(root, "no initialize response"):
		return fmt.Sprintf("%s server started but did not respond (process may have crashed). Check that any required services (e.g. database, Docker) are running.", serverName)
	case strings.Contains(root, "timed out"):
		return fmt.Sprintf("%s server did not respond within the timeout period. The server may be slow to start or unable to connect to its backend.", serverName)
	case strings.Contains(root, "connection refused"):
		return fmt.Sprintf("%s server could not connect to its backend service. Ensure the service is running and accessible.", serverName)
	default:
		return fmt.Sprintf("%s server error: %s", serverName, root)
	}
}

// tryFuzzyToolRecovery attempts to find the correct tool name when the
// LLM hallucinated a close-but-wrong name. Returns the corrected name and
// true if a match is found.
func (h *handler) tryFuzzyToolRecovery(ctx context.Context, name string) (string, bool) {
	allTools, err := h.gatherCodeModeTools(ctx)
	if err != nil {
		return "", false
	}
	matched, ok := fuzzyMatchTool(name, allTools)
	if !ok {
		return "", false
	}
	return matched.Name, true
}

// isReadOnlyTool checks the tool's annotations for readOnlyHint.
// Returns true if the tool is explicitly marked as read-only.
func (h *handler) isReadOnlyTool(ctx context.Context, serverID, toolName string) bool {
	srv, err := h.store.GetDownstreamServer(ctx, serverID)
	if srv == nil || err != nil || len(srv.CapabilitiesCache) == 0 {
		return false
	}

	var result struct {
		Tools []struct {
			Name        string                     `json:"name"`
			Annotations map[string]json.RawMessage `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(srv.CapabilitiesCache, &result); err != nil {
		return false
	}

	for _, t := range result.Tools {
		if t.Name != toolName {
			continue
		}
		if t.Annotations == nil {
			return false
		}
		raw, ok := t.Annotations["readOnlyHint"]
		if !ok {
			return false
		}
		var readOnly bool
		if err := json.Unmarshal(raw, &readOnly); err != nil {
			return false
		}
		return readOnly
	}
	return false
}
