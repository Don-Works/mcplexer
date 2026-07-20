package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/store"
)

func (h *handler) handleMeshSend(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}

	var req struct {
		Kind          string `json:"kind"`
		Content       string `json:"content"`
		Priority      string `json:"priority"`
		Audience      string `json:"audience"`
		Tags          string `json:"tags"`
		ReplyTo       string `json:"reply_to"`
		NotifyUser    bool   `json:"notify_user"`
		ToPeer        string `json:"to_peer"`
		ToAgent       string `json:"to_agent"`
		Repo          string `json:"repo"`
		Branch        string `json:"branch"`
		WorkspacePath string `json:"workspace_path"`
		ToWorkspace   string `json:"to_workspace"`
		Scope         string `json:"scope"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	v := newValidator()
	v.requireStringWithHint("content", req.Content,
		"the message body (markdown allowed)")
	// The hint derives from the validator's enforced set — it must never
	// advertise kinds that mesh.Send would reject (it used to claim
	// plan/ack/status/error and "custom tags" were valid; none were).
	v.requireStringWithHint("kind", req.Kind,
		"one of: "+mesh.ValidKindsHint())
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	sendLimit := h.meshSendMaxContentBytes(ctx)
	if len(req.Content) > sendLimit {
		return nil, &RPCError{
			Code: CodeInvalidParams,
			Message: fmt.Sprintf(
				"content too large (%d bytes; max %d)",
				len(req.Content), sendLimit,
			),
		}
	}
	// `scope:"global"` is an ergonomic alias for `to_workspace:"*"`.
	// Caller may pass either; an explicit to_workspace wins on conflict.
	if req.ToWorkspace == "" && (req.Scope == "global" || req.Scope == "*") {
		req.ToWorkspace = "*"
	}

	meta := h.sessionMeshMeta(ctx)
	targetWorkspace := req.ToWorkspace
	if targetWorkspace == "" && len(meta.WorkspaceIDs) > 0 {
		targetWorkspace = meta.WorkspaceIDs[0]
	}
	_, isWorker := workerWorkspaceAccessFromContext(ctx)
	if isWorker && (targetWorkspace == "*" || targetWorkspace == "global") {
		return nil, &RPCError{Code: CodeInvalidRequest, Message: "worker mesh sends must target a granted workspace; global broadcasts are denied"}
	}
	// Stamp the actor so worker traffic is distinguishable from agent
	// traffic. mesh__send is in the default worker allowlist, so without
	// this every delegated worker's send defaulted to "agent" (mesh.Send)
	// and three mechanisms silently no-opped on it: exclude_actor_kinds:
	// "worker" matched nothing, the 24h ArchiveOldWorkerFindings reaper
	// (which filters actor_kind='worker') never swept it — a direct cause
	// of inbox backlog — and the UI could not tell the two apart. The
	// runner's output dispatcher (cmd/mcplexer/workers_wiring.go) already
	// stamped "worker"; this is the tool agents actually call.
	actorKind := "agent"
	if isWorker {
		actorKind = "worker"
	}
	if targetWorkspace != "" && targetWorkspace != "*" && targetWorkspace != "global" {
		if rpc := h.requireWorkspaceWrite(ctx, targetWorkspace); rpc != nil {
			return nil, rpc
		}
	}
	msg, err := h.mesh.Send(ctx, meta, mesh.SendRequest{
		Kind:          req.Kind,
		Content:       req.Content,
		Priority:      req.Priority,
		Audience:      req.Audience,
		Tags:          req.Tags,
		ReplyTo:       req.ReplyTo,
		NotifyUser:    req.NotifyUser,
		ToPeer:        req.ToPeer,
		ToAgent:       req.ToAgent,
		Repo:          req.Repo,
		Branch:        req.Branch,
		WorkspacePath: req.WorkspacePath,
		ToWorkspace:   req.ToWorkspace,
		ActorKind:     actorKind,
	})
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}

	return marshalToolResult(mesh.FormatSendResult(msg)), nil
}

func (h *handler) handleMeshReceive(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}

	var req struct {
		Filter          string `json:"filter"`
		Tags            string `json:"tags"`
		SinceMinutes    int    `json:"since_minutes"`
		MaxResults      int    `json:"max_results"`
		ThreadID        string `json:"thread_id"`
		Name            string `json:"name"`
		Role            string `json:"role"`
		TmuxSession     string `json:"tmux_session"`
		TmuxWindow      string `json:"tmux_window"`
		TmuxPane        string `json:"tmux_pane"`
		Repo            string `json:"repo"`
		Branch          string `json:"branch"`
		WorkspacePath   string `json:"workspace_path"`
		MaxContentBytes int    `json:"max_content_bytes"`
		// Kind-level + actor-kind filters (comma-separated). task_event is
		// excluded by default unless kinds explicitly includes it.
		Kinds             string `json:"kinds"`
		ExcludeKinds      string `json:"exclude_kinds"`
		ActorKinds        string `json:"actor_kinds"`
		ExcludeActorKinds string `json:"exclude_actor_kinds"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}

	var notices []string
	meta := h.sessionMeshMeta(ctx)
	maxResults := h.meshReceiveMaxResults(ctx)
	if req.MaxResults > 0 {
		if req.MaxResults > maxResults {
			notices = append(notices, fmt.Sprintf(
				"max_results capped at %d (requested %d)",
				maxResults, req.MaxResults,
			))
		} else {
			maxResults = req.MaxResults
		}
	}
	previewBytes := h.meshReceivePreviewBytes(ctx)
	if req.MaxContentBytes > 0 {
		if req.MaxContentBytes > previewBytes {
			notices = append(notices, fmt.Sprintf(
				"content previews capped at %d bytes/message (requested %d)",
				previewBytes, req.MaxContentBytes,
			))
		} else {
			previewBytes = req.MaxContentBytes
		}
	}
	result, err := h.mesh.Receive(ctx, meta, mesh.ReceiveRequest{
		Filter:            req.Filter,
		Tags:              req.Tags,
		SinceMinutes:      req.SinceMinutes,
		MaxResults:        maxResults,
		ThreadID:          req.ThreadID,
		Name:              req.Name,
		Role:              req.Role,
		TmuxSession:       req.TmuxSession,
		TmuxWindow:        req.TmuxWindow,
		TmuxPane:          req.TmuxPane,
		Repo:              req.Repo,
		Branch:            req.Branch,
		WorkspacePath:     req.WorkspacePath,
		Kinds:             req.Kinds,
		ExcludeKinds:      req.ExcludeKinds,
		ActorKinds:        req.ActorKinds,
		ExcludeActorKinds: req.ExcludeActorKinds,
	})
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}

	env := mesh.BuildReceiveEnvelope(result, meta.SessionID, mesh.ReceiveEnvelopeOptions{
		ContentPreviewBytes: previewBytes,
		Notices:             notices,
		// Message previews are by-definition cross-peer free text: always
		// wrapped in the <untrusted-content> trust marker. Identity fields
		// (names, roles, sender labels) are scanned and wrapped only on a
		// denylist hit so clean identifiers stay clean. Sanitizing per-field
		// instead of enveloping the whole result keeps the envelope valid
		// JSON — code-mode consumers get a parseable object, not a string.
		WrapUntrusted: h.meshFieldSanitizer(ctx, "mesh__receive", true),
		ScanText:      h.meshFieldSanitizer(ctx, "mesh__receive", false),
	})
	return marshalJSONResult(env)
}

func (h *handler) handleMeshWaitForEvent(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}

	var req struct {
		Name             string `json:"name"`
		Role             string `json:"role"`
		WorkspaceID      string `json:"workspace_id"`
		Kinds            string `json:"kinds"`
		Tags             string `json:"tags"`
		AllTags          string `json:"all_tags"`
		FromPeer         string `json:"from_peer"`
		StatusFrom       string `json:"status_from"`
		StatusTo         string `json:"status_to"`
		IncludeRole      *bool  `json:"include_role"`
		IncludeBroadcast *bool  `json:"include_broadcast"`
		Consume          *bool  `json:"consume"`
		TimeoutSeconds   int    `json:"timeout_seconds"`
		MaxContentBytes  int    `json:"max_content_bytes"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}

	meta := h.sessionMeshMeta(ctx)
	if meta.SessionID == "" {
		return nil, &RPCError{Code: CodeInvalidRequest, Message: "mesh__wait_for_event requires an active MCP session"}
	}
	if err := h.mesh.EnsureAgent(ctx, meta, req.Name, req.Role); err != nil {
		return marshalErrorResult(err.Error()), nil
	}

	wsID := strings.TrimSpace(req.WorkspaceID)
	if wsID == "" && len(meta.WorkspaceIDs) > 0 {
		wsID = meta.WorkspaceIDs[0]
	}
	if strings.TrimSpace(req.WorkspaceID) != "" {
		if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
			return nil, rpc
		}
	}

	includeBroadcast := true
	if req.IncludeBroadcast != nil {
		includeBroadcast = *req.IncludeBroadcast
	}
	includeRole := false
	if req.IncludeRole != nil {
		includeRole = *req.IncludeRole
	}
	consume := false
	if req.Consume != nil {
		consume = *req.Consume
	}

	kinds := splitMeshWaitCSV(req.Kinds)
	if (strings.TrimSpace(req.StatusFrom) != "" || strings.TrimSpace(req.StatusTo) != "") && len(kinds) == 0 {
		kinds = []string{mesh.KindTaskEvent}
	}
	timeout := meshWaitTimeout(req.TimeoutSeconds)
	started := time.Now()
	msgs, err := h.mesh.WaitForMessage(ctx, mesh.WaitCriteria{
		SessionID:        meta.SessionID,
		Role:             req.Role,
		Tags:             splitMeshWaitCSV(req.Tags),
		AllTags:          splitMeshWaitCSV(req.AllTags),
		Kinds:            kinds,
		FromPeer:         req.FromPeer,
		StatusFrom:       req.StatusFrom,
		StatusTo:         req.StatusTo,
		IncludeRole:      includeRole,
		IncludeBroadcast: includeBroadcast,
		Consume:          consume,
		WorkspaceID:      wsID,
	}, timeout)
	if errors.Is(err, mesh.ErrUnknownAgent) {
		return nil, &RPCError{Code: CodeInvalidRequest, Message: "waiter session is not registered in the mesh agent directory"}
	}
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}

	var notices []string
	previewBytes := h.meshReceivePreviewBytes(ctx)
	if req.MaxContentBytes > 0 {
		if req.MaxContentBytes > previewBytes {
			notices = append(notices, fmt.Sprintf(
				"max_content_bytes capped at %d bytes/message (requested %d)",
				previewBytes, req.MaxContentBytes,
			))
		} else {
			previewBytes = req.MaxContentBytes
		}
	}
	rows := make([]store.MeshMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg != nil {
			rows = append(rows, *msg)
		}
	}
	env := mesh.BuildReceiveEnvelope(&mesh.ReceiveResult{Messages: rows}, meta.SessionID, mesh.ReceiveEnvelopeOptions{
		ContentPreviewBytes: previewBytes,
		Notices:             notices,
		WrapUntrusted:       h.meshFieldSanitizer(ctx, "mesh__wait_for_event", true),
		ScanText:            h.meshFieldSanitizer(ctx, "mesh__wait_for_event", false),
	})
	return marshalJSONResult(map[string]any{
		"timed_out": len(rows) == 0,
		"count":     len(rows),
		"waited_ms": time.Since(started).Milliseconds(),
		"consume":   consume,
		"workspace": wsID,
		"messages":  env.Messages,
		"notices":   notices,
		"hint":      "For task review hooks, wait with {kinds:\"task_event\", status_to:\"review\"}, then read task_id from message tags and call task__get({id}).",
	})
}

func meshWaitTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 300 * time.Second
	}
	if seconds > 3600 {
		return 3600 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func splitMeshWaitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// meshFieldSanitizer returns a closure that runs one untrusted mesh text
// field through the sanitize pipeline, recording an audit row per denylist
// hit. envelopeAlways=true forces the trust wrapper even on clean text
// (message bodies); false wraps only on a hit (short identity fields).
func (h *handler) meshFieldSanitizer(ctx context.Context, toolName string, envelopeAlways bool) func(string) string {
	trust := classifyTrust(toolName)
	return func(s string) string {
		if s == "" {
			return s
		}
		out := sanitize.Process(sanitize.ProcessOptions{
			Denylist:       h.sanitizer,
			Source:         trust.Source,
			Trust:          trust.TrustLevel,
			Body:           s,
			EnvelopeAlways: envelopeAlways,
		})
		for _, m := range out.Matches {
			h.recordSanitizeEvent(ctx, toolName, m)
		}
		return out.Body
	}
}

func (h *handler) handleMeshHydrate(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	var req struct {
		MessageID       string `json:"message_id"`
		MaxContentBytes int    `json:"max_content_bytes"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if req.MessageID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "message_id is required"}
	}
	if req.MaxContentBytes > mesh.MaxHydrateContentBytes {
		return nil, &RPCError{
			Code: CodeInvalidParams,
			Message: fmt.Sprintf(
				"max_content_bytes too large (%d; max %d)",
				req.MaxContentBytes, mesh.MaxHydrateContentBytes,
			),
		}
	}
	msg, err := h.mesh.Hydrate(ctx, h.sessionMeshMeta(ctx), req.MessageID)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return h.marshalMeshPeerResult(ctx, "mesh__hydrate", mesh.FormatHydrateResult(msg, req.MaxContentBytes)), nil
}

func (h *handler) handleMeshThread(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	var req struct {
		ThreadID        string `json:"thread_id"`
		MaxResults      int    `json:"max_results"`
		MaxContentBytes int    `json:"max_content_bytes"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if req.ThreadID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "thread_id is required"}
	}
	if req.MaxContentBytes > mesh.MaxHydrateContentBytes {
		return nil, &RPCError{
			Code: CodeInvalidParams,
			Message: fmt.Sprintf(
				"max_content_bytes too large (%d; max %d)",
				req.MaxContentBytes, mesh.MaxHydrateContentBytes,
			),
		}
	}
	msgs, err := h.mesh.Thread(ctx, h.sessionMeshMeta(ctx), req.ThreadID, req.MaxResults)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	text := mesh.FormatThreadResult(msgs, req.MaxContentBytes)
	return h.marshalMeshPeerResult(ctx, "mesh__thread", text), nil
}

func (h *handler) meshReceiveMaxResults(ctx context.Context) int {
	limit := mesh.DefaultReceiveMaxResults
	if h.settingsSvc != nil {
		if n := h.settingsSvc.Load(ctx).MeshReceiveMaxResults; n > 0 {
			limit = n
		}
	}
	return mesh.NormalizeReceiveMaxResults(limit)
}

func (h *handler) meshReceivePreviewBytes(ctx context.Context) int {
	limit := mesh.DefaultReceivePreviewBytes
	if h.settingsSvc != nil {
		if n := h.settingsSvc.Load(ctx).MeshReceivePreviewBytes; n > 0 {
			limit = n
		}
	}
	return mesh.NormalizeReceivePreviewBytes(limit)
}

func (h *handler) meshSendMaxContentBytes(ctx context.Context) int {
	limit := mesh.MaxSendContentBytes
	if h.settingsSvc != nil {
		if n := h.settingsSvc.Load(ctx).MeshSendMaxContentBytes; n > 0 {
			limit = n
		}
	}
	if limit > mesh.MaxSendContentBytes {
		return mesh.MaxSendContentBytes
	}
	if limit <= 0 {
		return mesh.MaxSendContentBytes
	}
	return limit
}

func (h *handler) marshalMeshPeerResult(ctx context.Context, toolName, text string) json.RawMessage {
	return h.sanitizeToolResult(ctx, marshalToolResult(text), toolName)
}

// sessionMeshMeta extracts session metadata for mesh operations.
func (h *handler) sessionMeshMeta(ctx context.Context) mesh.SessionMeta {
	wsIDs := h.readableWorkspaceIDs(ctx)
	// Ensure at least one workspace ID for sessions without workspace binding.
	if len(wsIDs) == 0 {
		wsIDs = []string{defaultMeshWorkspace(h.sessions)}
	}
	workspacePath := h.routingClientRoot(ctx)
	if _, isolated := workerFilesystemScopeFromContext(ctx); isolated {
		// Mesh metadata probes git when WorkspacePath is present. Exact
		// worktree workers keep non-file tools path-free, so never auto-probe
		// their checkout.
		workspacePath = ""
	}
	return mesh.SessionMeta{
		SessionID:     h.sessions.sessionID(),
		WorkspaceIDs:  wsIDs,
		ClientType:    h.sessions.clientType(),
		ModelHint:     h.sessions.modelHint(),
		WorkspacePath: workspacePath,
	}
}

// defaultMeshWorkspace returns a fallback workspace identity for sessions
// that don't resolve to a registered workspace.
//
// CRITICAL: this MUST be a private, per-session-directory identity — never a
// shared constant. The previous implementation returned the literal "global",
// which collapsed every unbound session (each a different repo / working
// directory) into one shared mesh namespace. The result was exactly the
// cross-workspace cross-talk mcplexer exists to eliminate: an agent in repo A
// saw the messages and agent directory of an agent in repo B. Worse, the p2p
// bridge treats "global" as the broadcast sentinel, so those messages also
// fanned out to every paired peer — a cross-machine leak.
//
// We instead derive a stable identity from the session's own client root, so
// two different directories are fully isolated while two sessions rooted in
// the SAME directory still coordinate (the desired same-repo behaviour). The
// deliberate broadcast channel is unaffected: callers that genuinely want
// every workspace to see a message still pass to_workspace:"*" / scope:"global"
// (mapped to WorkspaceID="" in mesh.Send), and the Telegram concierge keeps its
// own per-chat workspace binding.
func defaultMeshWorkspace(sm *sessionManager) string {
	if root := sm.clientRoot(); root != "" {
		return "dir:" + root
	}
	// No client root advertised (e.g. a daemon-socket session that never
	// sent roots/list): fall back to the session id so the session is
	// isolated to itself rather than merged into a shared bucket.
	if sid := sm.sessionID(); sid != "" {
		return "session:" + sid
	}
	return "global"
}

// resolveMeshPeer normalises any of {full libp2p peer ID, 10-char short
// suffix, device display_name} into the full peer ID of a currently-
// paired peer. Used by every mesh__* tool that takes a peer_id arg so
// that what shows up in mesh__list_peers is also a valid input.
//
// Returns mesh.ErrPeerNotPaired / mesh.ErrAmbiguousPeer on failure;
// callers should render with mesh.FormatPeerNotPairedError so the
// guidance ("try mesh__list_peers") is consistent across handlers.
func (h *handler) resolveMeshPeer(ctx context.Context, input string) (string, error) {
	if h.mesh == nil {
		// Slim builds without a mesh manager — preserve the caller's input
		// so the downstream validator still produces a recognisable error
		// instead of swallowing the value behind "not paired".
		return input, nil
	}
	return h.mesh.ResolvePeer(ctx, input)
}

// piggybackMeshNotice appends a pending-messages footer as a separate text
// content block. Never concatenate into an existing block — downstream servers
// often return JSON in content[0].text and a trailing footer breaks JSON.parse.
func (h *handler) piggybackMeshNotice(ctx context.Context, result json.RawMessage) json.RawMessage {
	if h.mesh == nil {
		return result
	}

	// Never piggyback onto tool calls dispatched from inside mcpx__execute_code.
	// Those results are consumed by the JS sandbox, not shown to the MCP client,
	// and the appended 2nd content block defeats the sandbox's "exactly one text
	// block" auto-unwrap gate — silently turning task.create().id into undefined
	// whenever the session has pending mesh messages. The OUTER execute_code
	// result is dispatched without this marker, so the agent is still nudged.
	if isInternalCodeModeCall(ctx) {
		return result
	}

	count, err := h.mesh.PendingCount(ctx, h.sessionMeshMeta(ctx))
	if err != nil || count == 0 {
		return result
	}
	return appendMeshNoticeBlock(result, count)
}

// appendMeshNoticeBlock returns result with an extra text content block for
// `count` pending mesh messages. Returns result unchanged on parse failure,
// empty content, or error envelopes.
func appendMeshNoticeBlock(result json.RawMessage, count int) json.RawMessage {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result
	}

	if raw, ok := envelope["isError"]; ok {
		var isErr bool
		if json.Unmarshal(raw, &isErr) == nil && isErr {
			return result
		}
	}

	rawContent, ok := envelope["content"]
	if !ok {
		return result
	}
	var content []json.RawMessage
	if err := json.Unmarshal(rawContent, &content); err != nil || len(content) == 0 {
		return result
	}

	noticeBlock, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{
		Type: "text",
		Text: fmt.Sprintf("[mesh: %d pending message(s) — call mesh__receive to read]", count),
	})
	if err != nil {
		return result
	}
	content = append(content, noticeBlock)

	newContent, err := json.Marshal(content)
	if err != nil {
		return result
	}
	envelope["content"] = newContent

	data, err := json.Marshal(envelope)
	if err != nil {
		return result
	}
	return data
}
