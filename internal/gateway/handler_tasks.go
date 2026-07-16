// handler_tasks.go — universal task__* MCP tools (migration 061).
// All handlers resolve workspace from the session's CWD. Surface shape
// follows the eight pre-decisions in .planning/tasks/REVIEW_NOTES.md:
// state-enum (not terminal-bool), single-string assignee, explicit-null
// updates via raw-JSON key-presence, task__claim primitive, bulk update
// via ids[], composed_by reverse-link, and a discovery envelope on
// list/get responses (known_statuses + known_assignees + known_tags).
//
// The discovery envelope is trimmed per `envelopeMode` to keep per-call
// token cost minimal (task 01KSGCVATZDZD0YK9KBFDBKVBS):
//   - list responses carry all three known_* fields, but each is
//     filtered to "relevant + canonical" rather than the full
//     workspace history.
//   - single-task get carries known_assignees only (the row already
//     has its own status + tags).
//   - writes (create, update, assign, claim, heartbeat, set_work_context,
//     accept_offer) carry NO known_* fields — the caller just supplied
//     the vocabulary they care about.
//
// workspace_id is never echoed in the response envelope; it's a
// caller-side identifier already known at the request.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/compact"
	"github.com/don-works/mcplexer/internal/embedding"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// isTaskToolName reports whether a tool name belongs to the tasks
// subsystem (the surface gated by the degraded-mode schema probe).
func isTaskToolName(name string) bool {
	return strings.HasPrefix(name, "task__") || name == "task_status_vocabulary__upsert"
}

// dispatchTaskTool routes task__* tool names to their handlers.
func (h *handler) dispatchTaskTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if h.tasksSvc == nil {
		switch name {
		case "task__create", "task__list", "task__get",
			"task__update", "task__set_visibility", "task__assign", "task__delete",
			"task__append_note", "task__claim",
			"task__history", "task__rollback",
			"task__compose", "task__decompose",
			"task__set_work_context",
			"task__recent_activity",
			"task__attach", "task__list_attachments", "task__get_attachment",
			"task__offer", "task__assign_remote", "task__publish_home",
			"task__accept_offer", "task__decline_offer", "task__list_offers",
			"task_status_vocabulary__upsert":
			return marshalErrorResult("Tasks subsystem is not enabled."), nil, true
		}
		return nil, nil, false
	}
	// Degraded-mode gate: a failed boot schema probe means every task
	// call would die with an opaque SQL error — surface the actionable
	// remedy instead. Fires only for task tool names so this dispatcher
	// still falls through cleanly for everything else.
	if err := h.tasksSvc.SchemaErr(); err != nil && isTaskToolName(name) {
		return marshalErrorResult(fmt.Sprintf(
			"Tasks subsystem degraded: %v. The tasks tables are missing expected columns — restart the mcplexer daemon to re-run migrations (or run `mcplexer upgrade`). Other tool families are unaffected.",
			err)), nil, true
	}
	switch name {
	case "task__create":
		resp, err := h.handleTaskCreate(ctx, raw)
		return resp, err, true
	case "task__list":
		resp, err := h.handleTaskList(ctx, raw)
		return resp, err, true
	case "task__get":
		resp, err := h.handleTaskGet(ctx, raw)
		return resp, err, true
	case "task__update":
		resp, err := h.handleTaskUpdate(ctx, raw)
		return resp, err, true
	case "task__set_visibility":
		resp, err := h.handleTaskSetVisibility(ctx, raw)
		return resp, err, true
	case "task__assign":
		resp, err := h.handleTaskAssign(ctx, raw)
		return resp, err, true
	case "task__claim":
		resp, err := h.handleTaskClaim(ctx, raw)
		return resp, err, true
	case "task__heartbeat":
		resp, err := h.handleTaskHeartbeat(ctx, raw)
		return resp, err, true
	case "task__delete":
		resp, err := h.handleTaskDelete(ctx, raw)
		return resp, err, true
	case "task__append_note":
		resp, err := h.handleTaskAppendNote(ctx, raw)
		return resp, err, true
	case "task__history":
		resp, err := h.handleTaskHistory(ctx, raw)
		return resp, err, true
	case "task__rollback":
		resp, err := h.handleTaskRollback(ctx, raw)
		return resp, err, true
	case "task__set_work_context":
		resp, err := h.handleTaskSetWorkContext(ctx, raw)
		return resp, err, true
	case "task__compose":
		resp, err := h.handleTaskCompose(ctx, raw)
		return resp, err, true
	case "task__decompose":
		resp, err := h.handleTaskDecompose(ctx, raw)
		return resp, err, true
	case "task__recent_activity":
		resp, err := h.handleTaskRecentActivity(ctx, raw)
		return resp, err, true
	case "task__list_milestones":
		resp, err := h.handleTaskListMilestones(ctx, raw)
		return resp, err, true
	case "task__attach", "task__list_attachments", "task__get_attachment":
		return h.dispatchTaskAttachmentTool(ctx, name, raw)
	case "task__offer", "task__assign_remote", "task__publish_home",
		"task__accept_offer", "task__decline_offer", "task__list_offers":
		return h.dispatchTaskOfferTool(ctx, name, raw)
	case "task_status_vocabulary__upsert":
		resp, err := h.handleTaskVocabUpsert(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

// handleTaskListMilestones surfaces milestone burndowns on the MCP
// surface. The capability existed store + REST-side only
// (ListMilestonesWithBurndown, GET /api/v1/tasks/milestones) — agents
// had no path to it.
func (h *handler) handleTaskListMilestones(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	rows, err := h.store.ListMilestonesWithBurndown(ctx, wsID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("List milestones failed: %v", err)), nil
	}
	if rows == nil {
		rows = []store.MilestoneBurndown{}
	}
	return marshalJSONResult(map[string]any{
		"workspace_id": wsID,
		"milestones":   rows,
		"count":        len(rows),
	})
}

// handleTaskRecentActivity is the per-workspace "what just happened
// here" feed — quieter than mesh__receive (which is cross-workspace
// firehose). Closes task 01KSJ053RRTDBW1AQBVVVSJX26.
func (h *handler) handleTaskRecentActivity(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	sinceStr, _ := stringField(args, "since")
	sinceT, err := parseOptionalRFC3339(sinceStr)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "since: " + err.Error()}
	}
	var since time.Time
	if sinceT != nil {
		since = *sinceT
	}
	limit, _ := intField(args, "limit")
	entries, err := h.tasksSvc.RecentActivity(ctx, wsID, since, limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("RecentActivity failed: %v", err)), nil
	}
	if entries == nil {
		entries = []tasks.ActivityEntry{}
	}
	if boolField(args, "dedupe") {
		return marshalJSONResult(map[string]any{
			"workspace_id":   wsID,
			"activity_view":  "deduped",
			"count":          len(entries),
			"clusters":       taskActivityClusters(entries),
			"hydrate":        "Call task__recent_activity({dedupe:false,...}) for full activity rows; call task__get({id}) for a task body.",
			"hydrate_fields": []string{"entries"},
		})
	}
	return marshalJSONResult(map[string]any{
		"workspace_id": wsID,
		"entries":      entries,
		"count":        len(entries),
	})
}

func taskActivityClusters(entries []tasks.ActivityEntry) compact.LexicalClusterResult {
	items := make([]compact.LexicalItem, 0, len(entries))
	for _, e := range entries {
		text := strings.Join([]string{
			e.Evt,
			e.From,
			e.To,
			e.Status,
			e.Note,
		}, " ")
		items = append(items, compact.LexicalItem{
			ID:    e.TaskID,
			Label: e.TaskTitle,
			Text:  strings.TrimSpace(text),
		})
	}
	opts := compact.DefaultLexicalClusterOptions()
	opts.MaxItems = 500
	opts.MaxGroups = 50
	opts.MaxExamples = 3
	opts.MaxExampleText = 180
	return compact.ClusterLexical(items, opts)
}

// handleTaskVocabUpsert handles the task_status_vocabulary__upsert MCP
// tool. Lets an agent declare "the word `triaging` means kind=working
// in this workspace" so the UI + auto-claim logic generalise without a
// code change. Workspace is resolved from the session CWD — matches
// the rest of the task__* surface.
func (h *handler) handleTaskVocabUpsert(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID := h.currentWorkspaceID(ctx)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory before declaring task vocabulary."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	statusText, _ := stringField(args, "status_text")
	statusText = strings.TrimSpace(statusText)
	if statusText == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "status_text is required"}
	}
	kind, _ := stringField(args, "kind")
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "open"
	}
	switch kind {
	case "open", "working", "blocked", "review", "done", "cancelled":
		// valid — review (migration 099) is NOT working (no lease) and
		// NOT terminal; it sits between working and done.
	default:
		return nil, &RPCError{Code: CodeInvalidParams, Message: "kind must be one of: open, working, blocked, review, done, cancelled"}
	}
	// is_terminal: explicit value wins; otherwise default to true for
	// terminal kinds (done|cancelled), false for the others.
	var isTerminal bool
	if v, ok := args["is_terminal"]; ok && len(v) > 0 && string(v) != "null" {
		if err := json.Unmarshal(v, &isTerminal); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "is_terminal: " + err.Error()}
		}
	} else {
		isTerminal = kind == "done" || kind == "cancelled"
	}
	displayColor, _ := stringField(args, "display_color")
	displayOrder, _ := intField(args, "display_order")
	v := &store.TaskStatusVocab{
		WorkspaceID:  wsID,
		StatusText:   statusText,
		IsTerminal:   isTerminal,
		Kind:         kind,
		DisplayColor: displayColor,
		DisplayOrder: displayOrder,
		ManagedBy:    "user",
		UpdatedAt:    time.Now().UTC(),
	}
	if err := h.store.UpsertTaskStatusVocab(ctx, v); err != nil {
		return marshalErrorResult(fmt.Sprintf("Upsert vocab failed: %v", err)), nil
	}
	return marshalJSONResult(v)
}

// parseAssignee turns a single-string assignee arg into an Assignee.
// Accepts "me" (resolves to current session), "<agent_name>" (resolved
// against mesh), or "<peer_short>:<agent_name>". Returns (nil, nil)
// for empty input — interpreted as "no assignee specified". The
// distinction between "field omitted" and "field present with null"
// is the caller's job (key-presence detection on raw JSON).
func (h *handler) parseAssignee(ctx context.Context, raw json.RawMessage) (*tasks.Assignee, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("assignee: expected string (\"me\" | \"<agent>\" | \"<peer>:<agent>\" | \"user:<id>\" | \"user:self\")")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if s == "me" {
		if h.sessions.sessionID() == "" {
			return nil, errors.New("assignee=\"me\" requires an active session")
		}
		return &tasks.Assignee{SessionID: h.sessions.sessionID()}, nil
	}
	// "user:<id>" or "user:self" form — human assignee.
	// Parsed BEFORE peer:agent to avoid ambiguity.
	if strings.HasPrefix(s, "user:") {
		userID := strings.TrimPrefix(s, "user:")
		if userID == "self" {
			selfUser, err := h.store.GetSelfUser(ctx)
			if err != nil {
				return nil, fmt.Errorf("assignee=user:self: %w", err)
			}
			userID = selfUser.UserID
		}
		if userID == "" {
			return nil, errors.New("assignee=user: requires a user id")
		}
		return &tasks.Assignee{UserID: userID}, nil
	}
	// "<peer_short>:<agent_name>" form — peer prefix is everything
	// before the first ":". The agent-name resolution then runs
	// against that peer's slice of the mesh directory.
	var peerID, agentName string
	if i := strings.Index(s, ":"); i > 0 {
		peerID = s[:i]
		agentName = s[i+1:]
	} else {
		agentName = s
	}
	// Resolve agent_name → session_id via active mesh directory.
	sessionID, resolvePeer, err := h.resolveAgentName(ctx, agentName, peerID)
	if err != nil {
		return nil, err
	}
	return &tasks.Assignee{SessionID: sessionID, PeerID: resolvePeer}, nil
}

// resolveAgentName scans the active mesh agent directory for a row
// whose Name matches `agentName`. If `peerHint` is non-empty, only
// agents whose Origin starts with "peer:<peerHint>" are considered;
// otherwise all matches across local + peers count. Returns
// (session_id, peer_id, error). peer_id is empty for local agents.
// Ambiguous names fail loudly (mirrors mesh__send.to_agent).
func (h *handler) resolveAgentName(ctx context.Context, agentName, peerHint string) (string, string, error) {
	if h.store == nil {
		return "", "", errors.New("agent directory unavailable")
	}
	wsID := h.currentWorkspaceID(ctx)
	since := time.Now().UTC().Add(-24 * time.Hour)
	agents, err := h.store.ListActiveMeshAgents(ctx, wsID, since)
	if err != nil {
		return "", "", fmt.Errorf("list agents: %w", err)
	}
	var matches []store.MeshAgent
	for _, a := range agents {
		if a.Name != agentName {
			continue
		}
		if peerHint != "" {
			if !strings.HasPrefix(a.Origin, "peer:"+peerHint) {
				continue
			}
		}
		matches = append(matches, a)
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf(
			"no agent named %q is registered on this daemon. "+
				"If you're a new session, call mesh__receive({name:%q, role:%q}) first to register; "+
				"otherwise list active agents with mesh__list_agents",
			agentName, agentName, "agent")
	}
	if len(matches) > 1 {
		var hints []string
		for _, m := range matches {
			locator := "local"
			if m.Origin != "" {
				locator = m.Origin
			}
			hints = append(hints, fmt.Sprintf("%s(%s)", m.Name, locator))
		}
		return "", "", fmt.Errorf("ambiguous agent name %q — matches: %s", agentName, strings.Join(hints, ", "))
	}
	m := matches[0]
	peer := ""
	if strings.HasPrefix(m.Origin, "peer:") {
		peer = strings.TrimPrefix(m.Origin, "peer:")
	}
	return m.SessionID, peer, nil
}

// parseStateArg maps the "state" arg ("open"|"closed"|"any", default
// "open") to the store filter's OnlyTerminal *bool. "any" → nil.
func parseStateArg(state string) *bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "open":
		f := false
		return &f
	case "closed":
		f := true
		return &f
	case "any":
		return nil
	default:
		// Unknown values fall through to "open" silently rather than
		// erroring — keeps the surface friendly to typos.
		f := false
		return &f
	}
}

// ----------------------------------------------------------------------
// create
// ----------------------------------------------------------------------

func (h *handler) handleTaskCreate(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	title, _ := stringField(args, "title")
	if strings.TrimSpace(title) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "title is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory before creating tasks, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	desc, _ := stringField(args, "description")
	status, _ := stringField(args, "status")
	priority, _ := stringField(args, "priority")
	meta, _ := stringField(args, "meta")
	composeIntoRaw, _ := stringField(args, "compose_into")
	composeInto, composeErr := h.resolveComposeInto(ctx, composeIntoRaw, wsID)
	if composeErr != nil {
		return marshalErrorResult(composeErr.Error()), nil
	}
	dueAtStr, _ := stringField(args, "due_at")
	due, err := parseOptionalRFC3339(dueAtStr)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "due_at: " + err.Error()}
	}
	var tags []string
	if v, ok := args["tags"]; ok {
		_ = json.Unmarshal(v, &tags)
	}
	var assignee *tasks.Assignee
	if v, ok := args["assignee"]; ok {
		assignee, err = h.parseAssignee(ctx, v)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	t, err := h.tasksSvc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              title,
		Description:        desc,
		Status:             status,
		Priority:           priority,
		DueAt:              due,
		Tags:               tags,
		Meta:               meta,
		Assignee:           assignee,
		ComposeInto:        composeInto,
		SourceKind:         store.TaskSourceAgent,
		SourceSessionID:    h.sessions.sessionID(),
		CreatedBySessionID: h.sessions.sessionID(),
		// Phase 2 plumbing: MCP callers are agents by default. Worker
		// subprocesses calling task__* via mcpx__execute_code surface here
		// too — for now they share the "agent" tag; PLAN.md "Phase 2"
		// notes the worker→actor_kind split is a follow-up once the
		// handler can distinguish them via session metadata.
		ActorKind:     "agent",
		WorkspacePath: h.routingClientRoot(ctx),
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Create failed: %v", err)), nil
	}
	// Record the new task id as the "last created" pointer for this
	// (session, workspace) pair so a follow-up
	// `compose_into: "last"` resolves to this row.
	h.rememberLastCreatedTask(h.sessions.sessionID(), wsID, t.ID)
	// Aging signal: surface stale open work (review > 24h, blocked >
	// 72h, assigned-open > 7d) on every create — the moment an agent
	// starts something new is exactly when it should notice what it
	// never finished. Non-blocking, same posture as coordination_warnings.
	return h.marshalTaskWriteResponse(ctx, t, wsID, envelopeModeNone, pickResponseShape(args), nil, h.staleTasksExtra(ctx, wsID))
}

// staleTasksExtra returns the {"stale_tasks": summary} advisory map for
// the workspace, or nil when nothing is stale. Best-effort — an error
// here must never fail the carrying response.
func (h *handler) staleTasksExtra(ctx context.Context, wsID string) map[string]any {
	sum, err := h.tasksSvc.StaleTasks(ctx, wsID)
	if err != nil || sum == nil {
		return nil
	}
	return map[string]any{"stale_tasks": sum}
}

// rememberLastCreatedTask records the most-recent task id created by
// (sessionID, workspaceID). Cleared on process restart — intentionally
// not durable. Empty sessionID = drop (test fixtures may call create
// before a session is bound, and "last" is meaningless without one).
func (h *handler) rememberLastCreatedTask(sessionID, workspaceID, taskID string) {
	if sessionID == "" || workspaceID == "" || taskID == "" {
		return
	}
	h.lastCreatedTaskMu.Lock()
	if h.lastCreatedTask == nil {
		h.lastCreatedTask = map[lastCreatedKey]string{}
	}
	h.lastCreatedTask[lastCreatedKey{SessionID: sessionID, WorkspaceID: workspaceID}] = taskID
	h.lastCreatedTaskMu.Unlock()
}

// lookupLastCreatedTask returns the most-recent task id created in
// this (sessionID, workspaceID) pair, or "" if none recorded.
func (h *handler) lookupLastCreatedTask(sessionID, workspaceID string) string {
	if sessionID == "" || workspaceID == "" {
		return ""
	}
	h.lastCreatedTaskMu.RLock()
	defer h.lastCreatedTaskMu.RUnlock()
	return h.lastCreatedTask[lastCreatedKey{SessionID: sessionID, WorkspaceID: workspaceID}]
}

// resolveComposeInto translates the freeform compose_into argument
// into a canonical task ULID before it reaches the service layer.
// Accepts:
//   - "" (empty)            → "" (no compose; caller didn't pass the arg).
//   - 26-char ULID          → pass through unchanged.
//   - 8-25 char prefix      → look up in the workspace; error on ambiguous.
//   - "last"                → most-recent task this session created in
//     this workspace; error if none recorded.
//
// Returns ("", error) on any failure (ambiguous prefix, no prior create
// for "last", prefix too short). The caller surfaces this to the agent
// verbatim — the error is the disambiguation hint, candidates and all.
func (h *handler) resolveComposeInto(ctx context.Context, raw, workspaceID string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.EqualFold(s, "last") {
		if h == nil {
			return "", fmt.Errorf("compose_into: \"last\" requires a session-bound handler")
		}
		id := h.lookupLastCreatedTask(h.sessions.sessionID(), workspaceID)
		if id == "" {
			return "", fmt.Errorf("compose_into: \"last\" — no task created in this session+workspace yet. Create a parent first, then compose children with \"last\" or paste the new id")
		}
		return id, nil
	}
	// 26 characters is the canonical ULID length; assume it's a full
	// id and let the service layer surface "not found" if it isn't.
	// Reject obvious mis-sizes (1-7 chars) — too short to be uniquely
	// resolved without burning a workspace scan on a guaranteed-miss.
	if len(s) >= 26 {
		// Some callers paste extra whitespace or wrap quotes; trim was
		// already applied. Anything longer is most likely a typo — pass
		// through and let service layer report ErrNotFound.
		return s, nil
	}
	if len(s) < 8 {
		return "", fmt.Errorf("compose_into: %q is too short to disambiguate — pass at least 8 chars of the ULID, the full id, or %q", s, "last")
	}
	if h.store == nil {
		return "", fmt.Errorf("compose_into: short-prefix lookup requires a store-backed handler")
	}
	// Cap candidates at 6: enough to give the agent a useful "did you
	// mean" list without exploding the error envelope.
	ids, err := h.store.ListTaskIDsByPrefix(ctx, workspaceID, s, 6)
	if err != nil {
		return "", fmt.Errorf("compose_into: prefix lookup failed: %w", err)
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("compose_into: no task in this workspace starts with %q. Use the full ULID or %q", s, "last")
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("compose_into: prefix %q is ambiguous — matches %d tasks (%s). Pass more characters or the full ULID", s, len(ids), strings.Join(ids, ", "))
	}
}

// ----------------------------------------------------------------------
// list
// ----------------------------------------------------------------------

func (h *handler) handleTaskList(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	state, _ := stringField(args, "state")
	status, _ := stringField(args, "status")
	tag, _ := stringField(args, "tag")
	q, _ := stringField(args, "q")
	semantic := boolField(args, "semantic")
	originPeerID, _ := stringField(args, "origin_peer_id")
	updatedAfterStr, _ := stringField(args, "updated_after")
	updatedAfter, err := parseOptionalRFC3339(updatedAfterStr)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "updated_after: " + err.Error()}
	}
	limit, _ := intField(args, "limit")
	offset, _ := intField(args, "offset")
	fullList := boolField(args, "full")

	f := store.TaskFilter{
		WorkspaceID:  wsID,
		Status:       status,
		OnlyTerminal: parseStateArg(state),
		OriginPeerID: originPeerID,
		UpdatedAfter: updatedAfter,
		Limit:        limit,
		Offset:       offset,
	}
	if tag != "" {
		f.Tags = []string{tag}
	}
	if v, ok := args["assignee"]; ok {
		a, err := h.parseAssignee(ctx, v)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "assignee: " + err.Error()}
		}
		if a != nil {
			f.AssigneeSessionID = a.SessionID
			f.AssigneePeerID = a.PeerID
			f.AssigneeUserID = a.UserID
			if a.UserID != "" {
				f.AssigneeOriginKind = store.TaskAssigneeHuman
			} else if a.PeerID != "" {
				f.AssigneeOriginKind = store.TaskAssigneePeer
			}
		}
	}
	if v, ok := args["assigned_by"]; ok {
		a, err := h.parseAssignee(ctx, v)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "assigned_by: " + err.Error()}
		}
		if a != nil {
			f.AssignedBySessionID = a.SessionID
			f.AssignedByPeerID = a.PeerID
		}
	}
	if v, ok := args["meta_match"]; ok {
		m, err := parseMetaMatchArg(v)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "meta_match: " + err.Error()}
		}
		f.MetaMatch = m
	}
	if v, ok := args["meta_has_key"]; ok {
		keys, err := parseMetaHasKeyArg(v)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "meta_has_key: " + err.Error()}
		}
		f.MetaHasKey = keys
	}
	if v, ok := args["meta_in"]; ok {
		in, err := parseMetaInArg(v)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "meta_in: " + err.Error()}
		}
		f.MetaIn = in
	}

	var rows []store.Task
	if strings.TrimSpace(q) != "" {
		if semantic {
			rows, err = h.semanticTaskSearch(ctx, f, q, limit, offset)
		} else {
			rows, err = h.tasksSvc.Search(ctx, f, q)
		}
	} else {
		rows, err = h.tasksSvc.List(ctx, f)
	}
	if err != nil {
		if envelope, ok := marshalFieldErrorResult(err); ok {
			return envelope, nil
		}
		return marshalErrorResult(fmt.Sprintf("List failed: %v", err)), nil
	}
	envelope := h.discoveryEnvelope(ctx, wsID, rows, envelopeModeList)
	// LLM ergonomics: marshal as [] not null when the query has zero matches
	// — an LLM otherwise can't distinguish "no matches" from "field absent
	// / query failed". See task 01KSGHS25GM0BG8K6T7EEFHSDN.
	if rows == nil {
		rows = []store.Task{}
	}
	// Always present: count makes the empty state unambiguous (a bare
	// discovery envelope with no count read as "maybe the call failed"),
	// and task_view self-describes the row shape in BOTH modes so
	// full:true doesn't silently change the contract.
	envelope["count"] = len(rows)
	// When a search query returned zero matches, attach diagnostics so
	// the caller can adjust the query instead of retrying blind. The
	// envelope already carries known_statuses/known_tags/known_meta_keys
	// from the discovery envelope — but those are scoped to the *result
	// set* which is empty here. The diagnostics pull from the broader
	// workspace so the caller sees what vocabulary exists.
	if len(rows) == 0 && strings.TrimSpace(q) != "" {
		envelope["search_diagnostics"] = h.taskSearchDiagnostics(ctx, wsID, q)
	}
	if fullList {
		envelope["tasks"] = rows
		envelope["task_view"] = "full"
	} else {
		envelope["tasks"] = taskListPreviews(ctx, rows, h.store)
		envelope["task_view"] = "preview"
		envelope["hydrate"] = "task__get({id}) or task__list({full:true})"
	}
	// Aging signal — non-blocking, only when something is actually
	// stale. Same advisory posture as coordination_warnings.
	if sum, err := h.tasksSvc.StaleTasks(ctx, wsID); err == nil && sum != nil {
		envelope["stale_tasks"] = sum
	}
	return marshalJSONResult(envelope)
}

func (h *handler) semanticTaskSearch(
	ctx context.Context,
	f store.TaskFilter,
	query string,
	limit, offset int,
) ([]store.Task, error) {
	candidateFilter := f
	candidateFilter.Offset = 0
	candidateFilter.Limit = semanticTaskCandidateLimit(limit, offset)
	candidates, err := h.tasksSvc.List(ctx, candidateFilter)
	if err != nil {
		return nil, err
	}
	return semanticRankTasks(candidates, query, semanticTaskResultLimit(limit), offset), nil
}

func semanticTaskCandidateLimit(limit, offset int) int {
	final := semanticTaskResultLimit(limit)
	capN := final*5 + max(offset, 0)
	if capN < 100 {
		capN = 100
	}
	if capN > 500 {
		capN = 500
	}
	return capN
}

func semanticTaskResultLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func semanticRankTasks(rows []store.Task, query string, limit, offset int) []store.Task {
	if len(rows) == 0 {
		return []store.Task{}
	}
	docs := make([]embedding.Document, 0, len(rows))
	byID := make(map[string]store.Task, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
		docs = append(docs, embedding.Document{
			ID:   row.ID,
			Text: taskSemanticText(row),
		})
	}
	idx := embedding.NewIndex(docs)
	hits := idx.Search(query, len(docs))
	if len(hits) == 0 {
		return []store.Task{}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(hits) {
		return []store.Task{}
	}
	hits = hits[offset:]
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]store.Task, 0, len(hits))
	for _, hit := range hits {
		if row, ok := byID[hit.ID]; ok {
			out = append(out, row)
		}
	}
	return out
}

func taskSemanticText(row store.Task) string {
	parts := []string{
		row.ID,
		row.Title,
		row.Description,
		row.Status,
		row.Priority,
		strings.Join(taskTags(row.TagsJSON), " "),
		row.Meta,
	}
	return strings.Join(parts, "\n")
}

// taskSearchDiagnostics returns a diagnostic map when a task search
// returns zero results. The map includes the workspace ID, the query,
// any task IDs that match the query as a prefix, and the known tags
// in the workspace so the caller can adjust their query.
func (h *handler) taskSearchDiagnostics(ctx context.Context, wsID, query string) map[string]any {
	diag := map[string]any{
		"workspace_id": wsID,
		"query":        query,
	}
	// Check if the query looks like a partial task ID (valid ULID chars).
	// ULIDs use Crockford alphabet: 0-9 A-Z minus I/L/O/U.
	if isLikelyIDPrefix(query) {
		if ids, err := h.store.ListTaskIDsByPrefix(ctx, wsID, strings.ToUpper(query), 10); err == nil && len(ids) > 0 {
			diag["nearby_ids"] = ids
		}
	}
	// Fetch a broad set of tasks to surface the workspace vocabulary.
	broadFilter := store.TaskFilter{
		WorkspaceID: wsID,
		Limit:       50,
	}
	if allTasks, err := h.tasksSvc.List(ctx, broadFilter); err == nil && len(allTasks) > 0 {
		allTags := map[string]bool{}
		for _, t := range allTasks {
			for _, tag := range taskTags(t.TagsJSON) {
				allTags[tag] = true
			}
		}
		if len(allTags) > 0 {
			tags := make([]string, 0, len(allTags))
			for tag := range allTags {
				tags = append(tags, tag)
			}
			diag["known_tags"] = tags
		}
	}
	return diag
}

// isLikelyIDPrefix returns true if s looks like a partial ULID
// (2+ characters of the Crockford alphabet: 0-9 A-Z minus I/L/O/U).
func isLikelyIDPrefix(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	for _, r := range strings.ToUpper(s) {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z' && r != 'I' && r != 'L' && r != 'O' && r != 'U':
		default:
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------
// get
// ----------------------------------------------------------------------

func (h *handler) handleTaskGet(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if args.ID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := strings.TrimSpace(args.WorkspaceID)
	if wsID == "" {
		wsID = h.currentWorkspaceID(ctx)
	}
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	t, err := h.tasksSvc.Get(ctx, wsID, args.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The error from Service.Get conflates "no such ID anywhere" with
			// "ID exists in another workspace" — deliberate, so cross-workspace
			// existence doesn't leak (internal/tasks/service.go:272). Help the
			// LLM stop searching without confirming/denying cross-workspace
			// state. See task 01KSGHRTBX7WQ2EPN1XWGKXS98.
			return marshalErrorResult("Task not found in this workspace. If the ID came from a mesh TASK_EVENT broadcast, it exists in another workspace and cross-workspace task reads aren't currently supported (see task 01KSGHSDPWZEXZ10CHAZX1EGGV)."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	return h.marshalTaskWithEnvelope(ctx, t, t.WorkspaceID, envelopeModeSingle)
}

// handleTaskSetVisibility is the model-facing audience control. The task
// service applies the workspace's agent ceiling and approval rule; this
// handler only resolves workspace scope and keeps cross-workspace existence
// fail-closed like task__get.
func (h *handler) handleTaskSetVisibility(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID          string   `json:"id"`
		WorkspaceID string   `json:"workspace_id"`
		Visibility  string   `json:"visibility"`
		Audience    []string `json:"audience"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.ID) == "" || strings.TrimSpace(args.Visibility) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id and visibility are required"}
	}
	wsID := strings.TrimSpace(args.WorkspaceID)
	if wsID == "" {
		wsID = h.currentWorkspaceID(ctx)
	}
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	access, err := h.tasksSvc.SetVisibility(ctx, wsID, args.ID, args.Visibility, args.Audience)
	if err != nil {
		if errors.Is(err, tasks.ErrVisibilityApprovalRequired) {
			return marshalErrorResult("Visibility widening requires human approval. Ask the operator to approve it on the task page, or choose a narrower audience."), nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found in this workspace."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Set visibility failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{
		"ok": true, "id": access.TaskID,
		"visibility":             access.Visibility,
		"visibility_epoch":       access.VisibilityEpoch,
		"audience_principal_ids": access.AudiencePrincipalIDs,
	})
}

// envelopeMode controls which `known_*` fields the discovery envelope
// carries. The trimming rules come from the LLM-ergonomics task
// (01KSGCVATZDZD0YK9KBFDBKVBS) — every byte of unrequested context
// burns the caller's budget, so writes carry nothing and the two read
// surfaces carry only what the caller might plausibly act on next.
type envelopeMode int

const (
	// envelopeModeNone is used for write responses (create / update /
	// assign / claim / heartbeat / set_work_context / accept_offer).
	// The caller just sent the mutation — they already know the
	// vocabulary their args used. Returning the envelope on every
	// write was the dominant token cost flagged in the source task.
	envelopeModeNone envelopeMode = iota
	// envelopeModeSingle is used for task__get. Carries
	// known_assignees only — the row's own status + tags are already
	// in the returned task object, so re-shipping the workspace's
	// full status/tag vocabulary is pure noise.
	envelopeModeSingle
	// envelopeModeList is used for task__list. Carries all three
	// trimmed sets so an agent that just listed open work can pick
	// its next status/tag/assignee without a follow-up round-trip.
	envelopeModeList
)

// compactTaskView is the slim post-write task echo, opt-out via
// `full: true`. The fields are the bare minimum the caller needs to
// confirm "the right row got mutated":
//   - id / title / status — what the agent thought it was acting on
//   - workspace_id — disambiguates when the call carried an explicit override
//   - updated_at / closed_at — let the caller decide whether to re-fetch
//   - terminal — whether the task is in a closed-vocabulary status
//
// Existing callers that read `result.task.title` continue to work
// because the compact view is still nested under `task` in the
// response envelope (the wire shape under `task` is a strict subset of
// the full Task, NOT a replacement).
type compactTaskView struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	Terminal    bool       `json:"terminal"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

const (
	taskListDescriptionPreviewBytes = 320
	taskListMetaPreviewBytes        = 240
)

type taskListPreview struct {
	ID                   string     `json:"id"`
	WorkspaceID          string     `json:"workspace_id"`
	Title                string     `json:"title"`
	Status               string     `json:"status"`
	Terminal             bool       `json:"terminal"`
	Priority             string     `json:"priority,omitempty"`
	DueAt                *time.Time `json:"due_at,omitempty"`
	Tags                 []string   `json:"tags,omitempty"`
	AssigneeSessionID    string     `json:"assignee_session_id,omitempty"`
	AssigneeOriginKind   string     `json:"assignee_origin_kind,omitempty"`
	AssigneePeerID       string     `json:"assignee_peer_id,omitempty"`
	AssigneeUserID       string     `json:"assignee_user_id,omitempty"`
	OriginPeerID         string     `json:"origin_peer_id,omitempty"`
	DescriptionPreview   string     `json:"description_preview,omitempty"`
	DescriptionBytes     int        `json:"description_bytes,omitempty"`
	DescriptionTruncated bool       `json:"description_truncated,omitempty"`
	MetaPreview          string     `json:"meta_preview,omitempty"`
	MetaBytes            int        `json:"meta_bytes,omitempty"`
	MetaTruncated        bool       `json:"meta_truncated,omitempty"`
	StatusHistoryCount   int        `json:"status_history_count,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
	CreatedAt            time.Time  `json:"created_at"`
}

func taskListPreviews(ctx context.Context, rows []store.Task, s store.TaskStore) []taskListPreview {
	out := make([]taskListPreview, 0, len(rows))
	for i := range rows {
		t := &rows[i]
		desc, descTruncated := previewTaskText(t.Description, taskListDescriptionPreviewBytes)
		meta, metaTruncated := previewTaskText(t.Meta, taskListMetaPreviewBytes)
		out = append(out, taskListPreview{
			ID:                   t.ID,
			WorkspaceID:          t.WorkspaceID,
			Title:                t.Title,
			Status:               t.Status,
			Terminal:             compactTaskFrom(ctx, t, s).Terminal,
			Priority:             t.Priority,
			DueAt:                t.DueAt,
			Tags:                 taskTags(t.TagsJSON),
			AssigneeSessionID:    t.AssigneeSessionID,
			AssigneeOriginKind:   t.AssigneeOriginKind,
			AssigneePeerID:       t.AssigneePeerID,
			AssigneeUserID:       t.AssigneeUserID,
			OriginPeerID:         t.OriginPeerID,
			DescriptionPreview:   desc,
			DescriptionBytes:     len(t.Description),
			DescriptionTruncated: descTruncated,
			MetaPreview:          meta,
			MetaBytes:            len(t.Meta),
			MetaTruncated:        metaTruncated,
			StatusHistoryCount:   taskStatusHistoryCount(t.StatusHistoryJSON),
			UpdatedAt:            t.UpdatedAt,
			CreatedAt:            t.CreatedAt,
		})
	}
	return out
}

func previewTaskText(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	if cut <= 0 {
		return "", true
	}
	return s[:cut], true
}

func taskStatusHistoryCount(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var entries []store.TaskStatusHistoryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return 0
	}
	return len(entries)
}

func taskTags(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil
	}
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func compactTaskFrom(ctx context.Context, t *store.Task, s store.TaskStore) compactTaskView {
	view := compactTaskView{
		ID:          t.ID,
		WorkspaceID: t.WorkspaceID,
		Title:       t.Title,
		Status:      t.Status,
		UpdatedAt:   t.UpdatedAt,
		ClosedAt:    t.ClosedAt,
		Terminal:    t.ClosedAt != nil,
	}
	// Prefer the vocabulary's terminal flag when available — closed_at
	// is set only on the transition; an existing closed row that was
	// never re-closed still has ClosedAt populated, but a row stamped
	// terminal via vocab-edit (no transition) might not. Best-effort:
	// fall back to ClosedAt!=nil when the lookup errors.
	if s != nil && !view.Terminal && t.Status != "" {
		if term, err := s.IsTerminalStatus(ctx, t.WorkspaceID, t.Status); err == nil && term {
			view.Terminal = true
		}
	}
	return view
}

// responseShape picks between compact and full response envelopes for
// write operations (create / update / assign / claim / heartbeat).
// Default is compact: the caller just sent the mutation, they already
// know everything about the row except what changed on the server side
// (updated_at / closed_at / auto-claim). Opt-in `full: true` restores
// the historical full Task + notes + composed_by + composes payload.
type responseShape int

const (
	responseShapeCompact responseShape = iota
	responseShapeFull
)

// pickResponseShape reads the optional `full` arg. Default = compact.
// Accepts `full: true` or the legacy `compact: false` (same effect).
func pickResponseShape(args map[string]json.RawMessage) responseShape {
	if v, ok := args["full"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil && b {
			return responseShapeFull
		}
	}
	if v, ok := args["compact"]; ok {
		var b bool
		// `compact: false` => full envelope (the inverse spelling).
		if err := json.Unmarshal(v, &b); err == nil && !b {
			return responseShapeFull
		}
	}
	return responseShapeCompact
}

// marshalTaskWithEnvelope shapes a single-task response: full task +
// notes + composed_by (derived from meta) + optional discovery
// envelope per `mode`. workspace_id is intentionally omitted from the
// envelope — the caller already supplied it via session CWD binding.
func (h *handler) marshalTaskWithEnvelope(ctx context.Context, t *store.Task, wsID string, mode envelopeMode) (json.RawMessage, *RPCError) {
	notes, _ := h.tasksSvc.ListNotes(ctx, wsID, t.ID, 100)
	composedBy := tasks.ReadMetaList(t.Meta, "composed_by")
	composes := tasks.ReadMetaList(t.Meta, "composes")
	// LLM ergonomics: marshal collections as [] not null when empty —
	// an LLM can't otherwise distinguish "this task has no notes" from
	// "field absent / fetch failed". See task 01KSGHS25GM0BG8K6T7EEFHSDN.
	if notes == nil {
		notes = []store.TaskNote{}
	}
	if composedBy == nil {
		composedBy = []string{}
	}
	if composes == nil {
		composes = []string{}
	}
	envelope := h.discoveryEnvelope(ctx, wsID, []store.Task{*t}, mode)
	envelope["task"] = t
	envelope["notes"] = nonNilTaskNotes(notes)
	envelope["composed_by"] = nonNilStrings(composedBy)
	envelope["composes"] = nonNilStrings(composes)
	return marshalJSONResult(envelope)
}

// marshalTaskWriteResponse dispatches to either the compact post-write
// shape (default) or the historical full envelope (`full: true`).
//
// Compact shape:
//
//	{
//	  "id": "<task_id>",                # convenience top-level
//	  "ok": true,
//	  "task": {<compactTaskView>},      # still nested under `task` for
//	                                    # back-compat: result.task.title works
//	  "coordination_warnings": [...]    # only when non-empty
//	}
//
// Full shape: identical to marshalTaskWithEnvelope (plus warnings).
//
// extra carries additional non-blocking advisory fields (stale_tasks,
// review_skipped, …) merged into the envelope top level in BOTH
// shapes. Nil/empty = no extra fields.
func (h *handler) marshalTaskWriteResponse(
	ctx context.Context,
	t *store.Task,
	wsID string,
	mode envelopeMode,
	shape responseShape,
	warnings []tasks.CoordinationWarning,
	extra map[string]any,
) (json.RawMessage, *RPCError) {
	if shape == responseShapeFull {
		if len(warnings) == 0 && len(extra) == 0 {
			return h.marshalTaskWithEnvelope(ctx, t, wsID, mode)
		}
		return h.marshalTaskWithCoordination(ctx, t, wsID, mode, warnings, extra)
	}
	out := map[string]any{
		"ok":   true,
		"id":   t.ID,
		"task": compactTaskFrom(ctx, t, h.store),
	}
	if len(warnings) > 0 {
		out["coordination_warnings"] = warnings
	}
	for k, v := range extra {
		out[k] = v
	}
	return marshalJSONResult(out)
}

// discoveryEnvelope assembles the `known_*` discovery fields for the
// workspace, trimmed per `mode`. See the envelopeMode docs for what
// each mode carries and why. workspace_id is NOT included — that's a
// request-side identifier the caller already has, not a response-side
// hint.
func (h *handler) discoveryEnvelope(ctx context.Context, wsID string, rows []store.Task, mode envelopeMode) map[string]any {
	out := map[string]any{}
	if mode == envelopeModeNone {
		return out
	}
	// known_assignees is the one envelope field useful on every read —
	// even for a single task__get the caller might want to re-assign
	// the row to a different active session next, and they need the
	// directory to do it without a separate mesh__list_agents call.
	out["known_assignees"] = nonNilKnownAssignees(h.knownAssignees(ctx, wsID))
	if mode == envelopeModeList {
		if statuses, err := h.tasksSvc.KnownStatuses(ctx, wsID); err == nil {
			out["known_statuses"] = nonNilStrings(tasks.FilterKnownStatuses(rows, statuses))
		} else {
			out["known_statuses"] = nonNilStrings(tasks.FilterKnownStatuses(rows, nil))
		}
		// Tags: union of "tags actually present in the result set"
		// and "top-5 most frequent across the same rows". For a
		// workspace with 50+ historical tasks this dramatically
		// shrinks vs. the old "topN=50 over everything visible" path
		// while still surfacing the established vocabulary.
		topN := tasks.TopWorkspaceTags(rows, 5)
		out["known_tags"] = nonNilStrings(tasks.FilterKnownTags(rows, topN))
		// Meta keys: distinct keys present across the returned rows,
		// ranked by frequency (descending), capped at 20. Lets agents
		// see which keys are queryable via meta_match / meta_has_key /
		// meta_in without a separate round-trip. Cheap — runs over the
		// rows we already loaded.
		out["known_meta_keys"] = nonNilStrings(tasks.TopMetaKeys(rows, 20))
	}
	return out
}

// knownAssignees returns the active mesh-agent directory scoped to
// the workspace, trimmed for the discovery envelope. Trim rules
// (FilterKnownAssignees) live in the tasks package so they're unit-
// testable without a full RPC fixture.
func (h *handler) knownAssignees(ctx context.Context, wsID string) []tasks.KnownAssignee {
	if h.store == nil {
		return nil
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	agents, err := h.store.ListActiveMeshAgents(ctx, wsID, since)
	if err != nil {
		return nil
	}
	return tasks.FilterKnownAssignees(agents)
}

// ----------------------------------------------------------------------
// update (single OR bulk)
// ----------------------------------------------------------------------

func (h *handler) handleTaskUpdate(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	ids, isBulk := collectIDs(args)
	if !isBulk && len(ids) == 0 {
		// Distinguish "id was passed but rejected" from "id missing".
		// collectIDs returns no ids in both cases — re-check the raw arg
		// so the structured envelope can surface the offending value.
		if v, ok := args["id"]; ok {
			var s string
			_ = json.Unmarshal(v, &s)
			if envelope, ok := marshalFieldErrorResult(validateTaskID("id", s)); ok {
				return envelope, nil
			}
		}
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id or ids[] is required"}
	}
	// Length-validate every id (single + bulk).
	for _, id := range ids {
		if err := validateTaskID("id", id); err != nil {
			if envelope, ok := marshalFieldErrorResult(err); ok {
				return envelope, nil
			}
		}
	}
	patch, rpc := h.buildUpdatePatch(ctx, args)
	if rpc != nil {
		return nil, rpc
	}

	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	if isBulk {
		ok, failed := h.tasksSvc.BulkUpdate(ctx, wsID, ids, patch)
		if ok == nil {
			ok = []*store.Task{}
		}
		if failed == nil {
			failed = []tasks.BulkFailure{}
		}
		return marshalJSONResult(map[string]any{
			"ok":     ok,
			"failed": failed,
		})
	}
	t, signals, err := h.tasksSvc.UpdateWithSignals(ctx, wsID, ids[0], patch)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Update failed: %v", err)), nil
	}
	// Coordination check: if this update put the task into a working
	// status AND it declared `touches_files`, surface any overlapping
	// in-progress tasks in the same workspace. Best-effort — a check
	// failure must not turn a successful update into an error response.
	warnings, _ := h.tasksSvc.CheckCoordinationOverlap(ctx, t)
	if len(warnings) > 0 {
		h.broadcastCoordinationAlert(ctx, t, warnings)
	}
	// Skipped-review nudge: working-kind → terminal-kind with no
	// review-kind status ever visited. Non-blocking advisory.
	var extra map[string]any
	if signals != nil && signals.ReviewSkipped {
		extra = map[string]any{
			"review_skipped":      true,
			"review_skipped_hint": signals.ReviewSkippedHint,
		}
	}
	return h.marshalTaskWriteResponse(ctx, t, t.WorkspaceID, envelopeModeNone, pickResponseShape(args), warnings, extra)
}

// broadcastCoordinationAlert fires a high-priority mesh alert when a
// task__update produces non-empty coordination_warnings. The
// originating agent already saw the warnings in its own response
// envelope; this surfaces them to (a) the conflicting agent(s) so
// they can pause or coordinate, (b) any other observer (human via
// dashboard, peer rigs). Fire-and-forget — a mesh failure must not
// turn a successful update into an error.
func (h *handler) broadcastCoordinationAlert(ctx context.Context, t *store.Task, warnings []tasks.CoordinationWarning) {
	if h.mesh == nil || len(warnings) == 0 {
		return
	}
	// Build a compact one-line summary per colliding task — the LLM
	// reading the mesh inbox shouldn't need to parse a large JSON blob
	// to know what to do.
	var b strings.Builder
	fmt.Fprintf(&b, "Coordination collision: task %s (%q) just transitioned to %q and overlaps with:\n",
		t.ID, truncateForAlert(t.Title, 60), t.Status)
	for _, w := range warnings {
		fmt.Fprintf(&b, "  - %s (%q) — %s — overlapping: %s\n",
			w.TaskID, truncateForAlert(w.Title, 60), w.Status, strings.Join(w.OverlappingPaths, ", "))
	}
	b.WriteString("Coordinate via mesh__send or pick a non-overlapping file region.")
	meta := h.sessionMeshMeta(ctx)
	if _, err := h.mesh.Send(ctx, meta, mesh.SendRequest{
		Kind:     "alert",
		Content:  b.String(),
		Priority: "high",
		Tags:     "coordination,touches_files",
		Audience: "*",
	}); err != nil {
		slog.Warn("coordination: mesh broadcast failed",
			"task_id", t.ID, "warnings", len(warnings), "err", err)
	}
}

// truncateForAlert clips a string to n bytes with an ellipsis when
// over. Used for the alert summary so a 400-char task title doesn't
// bloat the mesh feed. Byte-counted (not rune-counted) — task titles
// are ASCII in practice, and the alert is a human-readable hint not a
// data payload.
func truncateForAlert(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// marshalTaskWithCoordination wraps marshalTaskWithEnvelope with the
// optional `coordination_warnings` field + any extra advisory fields.
// Falls through to the plain envelope when both are empty so the
// response stays compact for the common no-collision case.
func (h *handler) marshalTaskWithCoordination(ctx context.Context, t *store.Task, wsID string, mode envelopeMode, warnings []tasks.CoordinationWarning, extra map[string]any) (json.RawMessage, *RPCError) {
	if len(warnings) == 0 && len(extra) == 0 {
		return h.marshalTaskWithEnvelope(ctx, t, wsID, mode)
	}
	// Re-implement the envelope build inline so we can append the
	// warnings field. Kept narrow: same fields as marshalTaskWithEnvelope.
	notes, _ := h.tasksSvc.ListNotes(ctx, wsID, t.ID, 100)
	composedBy := tasks.ReadMetaList(t.Meta, "composed_by")
	composes := tasks.ReadMetaList(t.Meta, "composes")
	if notes == nil {
		notes = []store.TaskNote{}
	}
	if composedBy == nil {
		composedBy = []string{}
	}
	if composes == nil {
		composes = []string{}
	}
	envelope := h.discoveryEnvelope(ctx, wsID, []store.Task{*t}, mode)
	envelope["task"] = t
	envelope["notes"] = notes
	envelope["composed_by"] = composedBy
	envelope["composes"] = composes
	if len(warnings) > 0 {
		envelope["coordination_warnings"] = warnings
	}
	for k, v := range extra {
		envelope[k] = v
	}
	return marshalJSONResult(envelope)
}

// buildUpdatePatch translates a raw JSON object into an UpdatePatch,
// honouring explicit null = clear vs key-absent = no-change.
func (h *handler) buildUpdatePatch(ctx context.Context, args map[string]json.RawMessage) (tasks.UpdatePatch, *RPCError) {
	patch := tasks.UpdatePatch{
		UpdatedBySessionID: h.sessions.sessionID(),
		ActorKind:          "agent",
		WorkspacePath:      h.routingClientRoot(ctx),
	}
	if v, ok := args["title"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return patch, &RPCError{Code: CodeInvalidParams, Message: "title must be string"}
		}
		patch.Title = &s
	}
	if v, ok := args["description"]; ok {
		var s string
		if string(v) != "null" {
			_ = json.Unmarshal(v, &s)
		}
		patch.Description = &s
	}
	if v, ok := args["status"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			patch.Status = &s
		}
	}
	if v, ok := args["priority"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			patch.Priority = &s
		}
	}
	if v, ok := args["meta"]; ok {
		var s string
		if string(v) != "null" {
			_ = json.Unmarshal(v, &s)
		}
		patch.Meta = &s
	}
	if v, ok := args["due_at"]; ok {
		if string(v) == "null" {
			patch.Clear = append(patch.Clear, "due_at")
		} else {
			var s string
			if err := json.Unmarshal(v, &s); err != nil || s == "" {
				patch.Clear = append(patch.Clear, "due_at")
			} else {
				t, err := parseOptionalRFC3339(s)
				if err != nil {
					return patch, &RPCError{Code: CodeInvalidParams, Message: "due_at: " + err.Error()}
				}
				patch.DueAt = t
			}
		}
	}
	if v, ok := args["tags"]; ok && string(v) != "null" {
		var ts []string
		if err := json.Unmarshal(v, &ts); err != nil {
			return patch, &RPCError{Code: CodeInvalidParams, Message: "tags must be array of strings"}
		}
		patch.Tags = &ts
	}
	if v, ok := args["assignee"]; ok {
		if string(v) == "null" {
			patch.Clear = append(patch.Clear, "assignee")
		} else {
			a, err := h.parseAssignee(ctx, v)
			if err != nil {
				return patch, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			}
			if a == nil {
				patch.Clear = append(patch.Clear, "assignee")
			} else {
				patch.Assignee = a
			}
		}
	}
	if v, ok := args["terminal"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			patch.Terminal = &b
		}
	}
	if v, ok := args["pinned"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			patch.Pinned = &b
		}
	}
	return patch, nil
}

// collectIDs reads id (string) OR ids ([]string) from the args.
// Returns (ids, isBulk). Bulk implies the caller wants the {ok, failed}
// shape even with a single id.
func collectIDs(args map[string]json.RawMessage) ([]string, bool) {
	if v, ok := args["ids"]; ok {
		var ids []string
		if err := json.Unmarshal(v, &ids); err == nil && len(ids) > 0 {
			return ids, true
		}
	}
	if v, ok := args["id"]; ok {
		var id string
		if err := json.Unmarshal(v, &id); err == nil && id != "" {
			return []string{id}, false
		}
	}
	return nil, false
}

// ----------------------------------------------------------------------
// assign
// ----------------------------------------------------------------------

func (h *handler) handleTaskAssign(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	patch := tasks.UpdatePatch{
		UpdatedBySessionID: h.sessions.sessionID(),
		ActorKind:          "agent",
		WorkspacePath:      h.routingClientRoot(ctx),
	}
	if v, ok := args["assignee"]; ok {
		if string(v) == "null" {
			patch.Clear = []string{"assignee"}
		} else {
			a, err := h.parseAssignee(ctx, v)
			if err != nil {
				return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			}
			if a == nil {
				patch.Clear = []string{"assignee"}
			} else {
				patch.Assignee = a
			}
		}
	} else {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "assignee is required (use null to unassign)"}
	}
	t, err := h.tasksSvc.Update(ctx, wsID, id, patch)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Assign failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, t, t.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// ----------------------------------------------------------------------
// claim — atomic assign + status flip
// ----------------------------------------------------------------------

func (h *handler) handleTaskClaim(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	status, _ := stringField(args, "status")
	note, _ := stringField(args, "note")
	t, err := h.tasksSvc.Claim(ctx, wsID, id, status, h.sessions.sessionID(), note, tasks.MutationContext{
		ActorKind:     "agent",
		SessionID:     h.sessions.sessionID(),
		WorkspacePath: h.routingClientRoot(ctx),
	})
	if err != nil {
		if errors.Is(err, tasks.ErrTaskAlreadyClaimed) {
			return marshalErrorResult("Task already claimed by another session."), nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Claim failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, t, t.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// ----------------------------------------------------------------------
// heartbeat — bumps lease for the calling session if it owns the lease
// ----------------------------------------------------------------------

func (h *handler) handleTaskHeartbeat(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	sessionID := h.sessions.sessionID()
	if sessionID == "" {
		return marshalErrorResult("Heartbeat requires an active session."), nil
	}
	if err := h.tasksSvc.Heartbeat(ctx, wsID, id, sessionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Heartbeat failed: %v", err)), nil
	}
	// Return the canonical row regardless of whether the bump took —
	// the caller can inspect lease_expires_at + assignee_session_id
	// to learn whether they still own the lease.
	t, err := h.tasksSvc.Get(ctx, wsID, id)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Fetch after heartbeat failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, t, t.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// ----------------------------------------------------------------------
// delete + note
// ----------------------------------------------------------------------

func (h *handler) handleTaskDelete(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	if err := h.tasksSvc.Delete(ctx, wsID, id, tasks.MutationContext{
		ActorKind:     "agent",
		SessionID:     h.sessions.sessionID(),
		WorkspacePath: h.routingClientRoot(ctx),
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found or already deleted."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Delete failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf("Deleted task %s.", id)), nil
}

func (h *handler) handleTaskAppendNote(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID          string `json:"id"`
		Body        string `json:"body"`
		Note        string `json:"note"` // ergonomic alias for body — agents guess it constantly
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Body) == "" {
		args.Body = args.Note
	}
	if args.ID == "" || strings.TrimSpace(args.Body) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id and body are required"}
	}
	wsID := strings.TrimSpace(args.WorkspaceID)
	if wsID == "" {
		wsID = h.currentWorkspaceID(ctx)
	}
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	n, err := h.tasksSvc.AppendNote(ctx, wsID, args.ID, args.Body, h.sessions.sessionID(), store.TaskSourceAgent, tasks.MutationContext{
		ActorKind:     "agent",
		SessionID:     h.sessions.sessionID(),
		WorkspacePath: h.routingClientRoot(ctx),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Append note failed: %v", err)), nil
	}
	return marshalJSONResult(n)
}

func (h *handler) handleTaskHistory(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	limit := 100
	if v, ok := args["limit"]; ok {
		_ = json.Unmarshal(v, &limit)
	}
	full := false
	if v, ok := args["full"]; ok {
		_ = json.Unmarshal(v, &full)
	}
	rows, err := h.tasksSvc.ListHistory(ctx, wsID, id, limit)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("History failed: %v", err)), nil
	}
	if full {
		return marshalJSONResult(map[string]any{"count": len(rows), "history": rows})
	}
	compactRows := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		changedFields := []string{}
		if len(row.ChangedFieldsJSON) > 0 {
			_ = json.Unmarshal(row.ChangedFieldsJSON, &changedFields)
		}
		compactRows = append(compactRows, map[string]any{
			"id":               row.ID,
			"task_id":          row.TaskID,
			"revision":         row.Revision,
			"action":           row.Action,
			"actor_kind":       row.ActorKind,
			"actor_session_id": row.ActorSessionID,
			"actor_peer_id":    row.ActorPeerID,
			"actor_user_id":    row.ActorUserID,
			"workspace_path":   row.WorkspacePath,
			"changed_fields":   changedFields,
			"related_revision": row.RelatedRevision,
			"note":             row.Note,
			"created_at":       row.CreatedAt,
		})
	}
	return marshalJSONResult(map[string]any{"count": len(compactRows), "history": compactRows})
}

func (h *handler) handleTaskRollback(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	var revision int
	if v, ok := args["revision"]; ok {
		_ = json.Unmarshal(v, &revision)
	}
	if revision <= 0 {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "revision is required"}
	}
	note, _ := stringField(args, "note")
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	t, err := h.tasksSvc.Rollback(ctx, wsID, id, tasks.RollbackOptions{
		Revision:      revision,
		ActorKind:     "agent",
		SessionID:     h.sessions.sessionID(),
		WorkspacePath: h.routingClientRoot(ctx),
		Note:          note,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Rollback failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, t, t.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// ----------------------------------------------------------------------
// set_work_context — chunk 6 (work-context annotations)
// ----------------------------------------------------------------------

// handleTaskSetWorkContext applies a WorkContext patch. The MCP
// surface contract: empty-string values in the request body MEAN
// "clear that key"; absent keys mean "leave unchanged". This matches
// the agent UX of "send me the diff" rather than "send me the full
// state vector".
func (h *handler) handleTaskSetWorkContext(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	id, _ := stringField(args, "id")
	if id == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	patch, clears := buildWorkContextPatch(args)
	t, err := h.tasksSvc.SetWorkContext(ctx, wsID, id, patch, clears, h.sessions.sessionID(), tasks.MutationContext{
		ActorKind:     "agent",
		SessionID:     h.sessions.sessionID(),
		WorkspacePath: h.routingClientRoot(ctx),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("set_work_context failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, t, t.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// buildWorkContextPatch reads the MCP args into a WorkContext + clears
// slice. Key-presence + empty-string detection is the substrate: any
// key present with an empty string lands on the clears slice; non-empty
// strings land on the typed patch struct.
func buildWorkContextPatch(args map[string]json.RawMessage) (tasks.WorkContext, []string) {
	patch := tasks.WorkContext{}
	clears := []string{}
	read := func(key string, dst *string) {
		v, ok := args[key]
		if !ok {
			return
		}
		var s string
		if string(v) == "null" {
			clears = append(clears, key)
			return
		}
		if err := json.Unmarshal(v, &s); err != nil {
			return
		}
		if s == "" {
			clears = append(clears, key)
			return
		}
		*dst = s
	}
	read("worktree", &patch.Worktree)
	read("branch", &patch.Branch)
	read("pr", &patch.PR)
	read("commits", &patch.Commits)
	read("peer", &patch.Peer)
	read("session", &patch.Session)
	read("linear", &patch.Linear)
	read("mesh_thread", &patch.MeshThread)
	return patch, clears
}

// ----------------------------------------------------------------------
// compose / decompose — post-hoc bidirectional parent/child linking
// ----------------------------------------------------------------------

// handleTaskCompose wraps Service.Compose / BulkCompose. Single form
// (child_id) returns the re-fetched parent + discovery envelope; bulk
// form (child_ids) returns the {ok:[id...], failed:[{id,error}...]}
// shape to mirror task__update's bulk pattern.
func (h *handler) handleTaskCompose(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	parentID, _ := stringField(args, "parent_id")
	if strings.TrimSpace(parentID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "parent_id is required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	bySession := h.sessions.sessionID()

	// Bulk form wins when child_ids is present + non-empty.
	if v, ok := args["child_ids"]; ok && string(v) != "null" {
		var childIDs []string
		if err := json.Unmarshal(v, &childIDs); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "child_ids must be array of strings"}
		}
		if len(childIDs) > 0 {
			okIDs, failed := h.tasksSvc.BulkCompose(ctx, wsID, parentID, childIDs, bySession)
			if failed == nil {
				failed = []tasks.BulkFailure{}
			}
			return marshalJSONResult(map[string]any{
				"ok":     nonNilStrings(okIDs),
				"failed": failed,
			})
		}
	}

	childID, _ := stringField(args, "child_id")
	if strings.TrimSpace(childID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "child_id or child_ids[] is required"}
	}
	if err := h.tasksSvc.Compose(ctx, wsID, parentID, childID, bySession); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found (cross-workspace links refused)."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Compose failed: %v", err)), nil
	}
	parent, err := h.tasksSvc.Get(ctx, wsID, parentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Parent task not found after compose."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Fetch parent after compose failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, parent, parent.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// handleTaskDecompose wraps Service.Decompose — mirror of compose.
// Returns the re-fetched parent + discovery envelope.
func (h *handler) handleTaskDecompose(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	parentID, _ := stringField(args, "parent_id")
	childID, _ := stringField(args, "child_id")
	if strings.TrimSpace(parentID) == "" || strings.TrimSpace(childID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "parent_id and child_id are required"}
	}
	wsID := h.resolveWorkspace(ctx, args)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory, or pass workspace_id."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	if err := h.tasksSvc.Decompose(ctx, wsID, parentID, childID, h.sessions.sessionID()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found (cross-workspace links refused)."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Decompose failed: %v", err)), nil
	}
	parent, err := h.tasksSvc.Get(ctx, wsID, parentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Parent task not found after decompose."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Fetch parent after decompose failed: %v", err)), nil
	}
	return h.marshalTaskWriteResponse(ctx, parent, parent.WorkspaceID, envelopeModeNone, pickResponseShape(args), nil, nil)
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func parseOptionalRFC3339(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("expected RFC3339 timestamp, got %q", s)
}

func marshalJSONResult(v any) (json.RawMessage, *RPCError) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(b)), nil
}

// unmarshalRawObject reads the request arg blob as map[string]RawMessage
// so callers can detect key presence vs absence vs explicit-null. This
// is the substrate for the "null = clear, omit = unchanged" pattern
// (item 3 in REVIEW_NOTES.md).
func unmarshalRawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out, nil
}

// resolveWorkspace returns the workspace_id override from args if
// present + non-empty, else the session-bound workspace. Lets
// task__* tools accept an explicit `workspace_id` arg so callers
// (e.g. the Telegram concierge) can route a task into a workspace
// they're NOT currently bound to. Validation: tasks-service Get/
// Update/etc. already enforce per-workspace isolation; this helper
// just provides the choice. Returns "" if neither source has a value
// — caller MUST surface "no workspace bound" with the existing
// envelope.
//
// Closes task 01KSGJZ515JD4XT6RXJA3EMD39.
func (h *handler) resolveWorkspace(ctx context.Context, args map[string]json.RawMessage) string {
	if override, ok := stringField(args, "workspace_id"); ok && strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	return h.currentWorkspaceID(ctx)
}

func stringField(args map[string]json.RawMessage, key string) (string, bool) {
	v, ok := args[key]
	if !ok || string(v) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s, true
	}
	// Tolerance for the args-coercer (internal/gateway/args_coerce.go):
	// builtin tools don't get their string-fields schema looked up, so a
	// caller's `meta: '{"k":"v"}'` string gets pre-parsed into a JSON
	// object before reaching here. Re-marshal the raw value back to its
	// JSON-string representation so the rest of the handler (which
	// expects the frontmatter/JSON string form) keeps working. Fixes
	// the touches_files coordination feature when invoked via mcpx
	// code-mode (task 01KSJ03CXACC0PHXQW60C250QN).
	trimmed := strings.TrimSpace(string(v))
	if len(trimmed) >= 2 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return trimmed, true
	}
	return "", false
}

func intField(args map[string]json.RawMessage, key string) (int, bool) {
	v, ok := args[key]
	if !ok || string(v) == "null" {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	return n, true
}

func boolField(args map[string]json.RawMessage, key string) bool {
	v, ok := args[key]
	if !ok || string(v) == "null" {
		return false
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false
	}
	return b
}

// parseMetaMatchArg decodes the `meta_match` argument on task__list.
// Accepts either a JSON object literal {"key":"value", ...} or the
// URL-friendly comma-shorthand "key:value,key2:value2". Empty input
// returns (nil, nil); non-string values are rejected.
func parseMetaMatchArg(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Object form
	var asObj map[string]any
	if err := json.Unmarshal(raw, &asObj); err == nil && asObj != nil {
		out := make(map[string]string, len(asObj))
		for k, v := range asObj {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("value at key %q must be string", k)
			}
			if err := validateMetaKey(k); err != nil {
				return nil, err
			}
			out[k] = s
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	// String shorthand
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("expected object or shorthand string: %w", err)
	}
	return parseMetaMatchShorthand(s)
}

// parseMetaMatchShorthand parses "k:v,k2:v2" into a map. Whitespace
// is trimmed; empty pairs are skipped. Used by both the MCP and REST
// paths so the URL-friendly form behaves the same in both.
func parseMetaMatchShorthand(s string) (map[string]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.IndexByte(pair, ':')
		if idx <= 0 {
			return nil, fmt.Errorf("expected key:value, got %q", pair)
		}
		k := strings.TrimSpace(pair[:idx])
		v := strings.TrimSpace(pair[idx+1:])
		if err := validateMetaKey(k); err != nil {
			return nil, err
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseMetaHasKeyArg decodes the `meta_has_key` argument. Accepts a
// single string ("key") or an array (["key1", "key2"]).
func parseMetaHasKeyArg(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Array form
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := make([]string, 0, len(arr))
		for _, k := range arr {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if err := validateMetaKey(k); err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, nil
	}
	// Single string
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("expected string or array of strings")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if err := validateMetaKey(s); err != nil {
		return nil, err
	}
	return []string{s}, nil
}

// parseMetaInArg decodes the `meta_in` argument. Accepts an object
// {key: [v1, v2], ...}.
func parseMetaInArg(raw json.RawMessage) (map[string][]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asObj map[string][]string
	if err := json.Unmarshal(raw, &asObj); err != nil {
		return nil, fmt.Errorf("expected object of {key: [values]}: %w", err)
	}
	out := make(map[string][]string, len(asObj))
	for k, vs := range asObj {
		if err := validateMetaKey(k); err != nil {
			return nil, err
		}
		if len(vs) == 0 {
			continue
		}
		out[k] = vs
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validateMetaKey rejects keys that would be unsafe to interpolate
// into a JSON1 path expression. Allows letters, digits, underscore,
// and hyphen (the existing meta-key convention) — explicitly
// excludes quote / dollar / brackets so a maliciously-crafted key
// can't slip out of the json_extract path syntax.
func validateMetaKey(k string) error {
	if k == "" {
		return fmt.Errorf("meta key is required")
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("meta key %q contains illegal character %q (allowed: a-z, A-Z, 0-9, _, -)", k, r)
		}
	}
	return nil
}
