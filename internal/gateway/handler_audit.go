package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// recordAudit creates and persists an audit record for a tool call.
func (h *handler) recordAudit(
	ctx context.Context,
	toolName string,
	params json.RawMessage,
	route *routing.RouteResult,
	result json.RawMessage,
	rpcErr *RPCError,
	start time.Time,
) {
	h.recordAuditWithCache(ctx, toolName, params, route, result, rpcErr, start, false)
}

// recordAuditWithCache creates and persists an audit record with cache hit info.
func (h *handler) recordAuditWithCache(
	ctx context.Context,
	toolName string,
	params json.RawMessage,
	route *routing.RouteResult,
	result json.RawMessage,
	rpcErr *RPCError,
	start time.Time,
	cacheHit bool,
) {
	status := "success"
	toolErr := false
	toolErrText := ""
	if rpcErr != nil {
		status = "error"
	} else if isToolError(result) {
		status = "error"
		toolErr = true
		toolErrText = extractToolErrorText(result)
	}
	h.recordContextCostResult(toolName, result, status)
	if h.auditor == nil {
		return
	}

	// Audit workspace is always the session's workspace (where the user is),
	// not the workspace that owns the matched rule. The matched rule's
	// workspace is recoverable via route_rules.workspace_id ← RouteRuleID.
	wsID := h.currentWorkspaceID(ctx)
	wsName := h.currentWorkspaceName(ctx)
	subpath := h.currentSubpath(ctx)

	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      start,
		SessionID:      h.sessions.sessionID(),
		ClientType:     h.sessions.clientType(),
		Model:          h.sessions.modelHint(),
		WorkspaceID:    wsID,
		WorkspaceName:  wsName,
		Subpath:        subpath,
		ToolName:       toolName,
		ParamsRedacted: scrubAuditParams(toolName, params),
		Status:         status,
		LatencyMs:      int(time.Since(start).Milliseconds()),
		ResponseSize:   len(result),
		CacheHit:       cacheHit,
		ExecutionID:    executionIDFromContext(ctx),
		SkillID:        skillIDPtr(ctx),
		ActorKind:      "user",
		ActorID:        h.sessions.sessionID(),
		CorrelationID:  executionIDFromContext(ctx),
	}

	if route != nil {
		rec.RouteRuleID = route.MatchedRuleID
		rec.DownstreamServerID = route.DownstreamServerID
		rec.AuthScopeID = route.AuthScopeID
	}

	if rpcErr != nil {
		rec.ErrorCode = fmt.Sprintf("%d", rpcErr.Code)
		rec.ErrorMessage = rpcErr.Message
	} else if toolErr {
		rec.ErrorCode = "tool_error"
		rec.ErrorMessage = toolErrText
	}

	if err := h.auditor.Record(ctx, rec); err != nil {
		// Warn (not Error): the audit write failed but the gateway already
		// continued — the tool result is still on its way to the client.
		// Error-level alarms on-call dashboards for every transient SQLite
		// hiccup that we explicitly swallowed; warn is the honest level.
		slog.Warn("audit record failed", "error", err)
	}
}

// callerAttribution snapshots the current session's identity so deep
// emitters (the secrets resolver) can attribute their own audit rows to the
// agent that triggered them rather than to a synthesized placeholder. It is
// stamped onto ctx at the gateway dispatch entry; downstream secret reads —
// whether from secret__list_refs or `secret://` substitution at dispatch —
// inherit it. Returns a zero Attribution when no session is bound, in which
// case audit.WithAttribution leaves ctx unchanged and emitters fall back to
// their own defaults.
func (h *handler) callerAttribution(ctx context.Context) audit.Attribution {
	if h == nil || h.sessions == nil {
		return audit.Attribution{}
	}
	sid := h.sessions.sessionID()
	return audit.Attribution{
		SessionID:     sid,
		ClientType:    h.sessions.clientType(),
		Model:         h.sessions.modelHint(),
		WorkspaceID:   h.currentWorkspaceID(ctx),
		WorkspaceName: h.currentWorkspaceName(ctx),
		Subpath:       h.currentSubpath(ctx),
		ActorKind:     "user",
		ActorID:       sid,
	}
}

// skillIDPtr returns a *string pointer to the skill ID in ctx, or nil when
// no skill context is attached. AuditRecord.SkillID uses the pointer form
// so legacy NULL values round-trip cleanly through SQLite.
func skillIDPtr(ctx context.Context) *string {
	id := skillIDFromContext(ctx)
	if id == "" {
		return nil
	}
	return &id
}

// recordAuditBlocked creates an audit record with status "blocked" for route
// denials, approval gates, and other policy-level rejections.
func (h *handler) recordAuditBlocked(
	ctx context.Context,
	toolName string,
	params json.RawMessage,
	route *routing.RouteResult,
	result json.RawMessage,
	rpcErr *RPCError,
	start time.Time,
) {
	h.recordContextCostResult(toolName, result, "blocked")
	if h.auditor == nil {
		return
	}

	wsID := h.currentWorkspaceID(ctx)
	wsName := h.currentWorkspaceName(ctx)
	subpath := h.currentSubpath(ctx)

	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      start,
		SessionID:      h.sessions.sessionID(),
		ClientType:     h.sessions.clientType(),
		Model:          h.sessions.modelHint(),
		WorkspaceID:    wsID,
		WorkspaceName:  wsName,
		Subpath:        subpath,
		ToolName:       toolName,
		ParamsRedacted: scrubAuditParams(toolName, params),
		Status:         "blocked",
		LatencyMs:      int(time.Since(start).Milliseconds()),
		ResponseSize:   len(result),
		ExecutionID:    executionIDFromContext(ctx),
		SkillID:        skillIDPtr(ctx),
		ActorKind:      "user",
		ActorID:        h.sessions.sessionID(),
		CorrelationID:  executionIDFromContext(ctx),
	}

	if route != nil {
		rec.RouteRuleID = route.MatchedRuleID
		rec.DownstreamServerID = route.DownstreamServerID
		rec.AuthScopeID = route.AuthScopeID
	}

	if rpcErr != nil {
		rec.ErrorCode = fmt.Sprintf("%d", rpcErr.Code)
		rec.ErrorMessage = rpcErr.Message
	} else if isToolError(result) {
		rec.ErrorCode = "blocked"
		rec.ErrorMessage = extractToolErrorText(result)
	}

	if err := h.auditor.Record(ctx, rec); err != nil {
		// Warn (not Error): the audit write failed but the gateway already
		// continued — the tool result is still on its way to the client.
		// Error-level alarms on-call dashboards for every transient SQLite
		// hiccup that we explicitly swallowed; warn is the honest level.
		slog.Warn("audit record failed", "error", err)
	}
}

func scrubAuditParams(toolName string, params json.RawMessage) json.RawMessage {
	if toolName != "data__ingest" {
		return params
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(params, &in); err != nil {
		return json.RawMessage(`{"payload_redacted":true,"redaction":"invalid data__ingest params"}`)
	}
	out := map[string]any{"payload_redacted": true}
	for _, key := range []string{"name", "kind", "workspace_id", "ttl_minutes", "pinned", "tags"} {
		if raw, ok := in[key]; ok {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				out[key] = v
			}
		}
	}
	out["rows_count"] = rawArrayLen(in["rows"])
	out["documents_count"] = rawArrayLen(in["documents"])
	if raw, ok := in["text"]; ok {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			out["text_bytes"] = len(text)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

func rawArrayLen(raw json.RawMessage) int {
	var arr []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &arr) != nil {
		return 0
	}
	return len(arr)
}

// isToolError checks whether a tools/call result has isError set.
func isToolError(result json.RawMessage) bool {
	if len(result) == 0 {
		return false
	}
	var peek struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &peek); err != nil {
		return false
	}
	return peek.IsError
}

// extractToolErrorText pulls the first text content from an isError result.
func extractToolErrorText(result json.RawMessage) string {
	var r CallToolResult
	if err := json.Unmarshal(result, &r); err != nil {
		return "tool returned error"
	}
	for _, c := range r.Content {
		if c.Text != "" {
			if len(c.Text) > 200 {
				return c.Text[:200]
			}
			return c.Text
		}
	}
	return "tool returned error"
}
