package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/store"
)

// memoryRecaller is the narrow read surface the session hook depends on to
// surface relevant memories at session start. Defined as an interface so
// tests inject a fake without dragging the full store.Store dependency
// into the api test surface (mirrors workspaceLookup / approvalRequester).
//
// The session hook calls ListMemories with a workspace-scoped filter and a
// small Limit — the goal is a lightweight "here is what past sessions
// learned" nudge, not a full recall. Heavy semantic recall stays an
// explicit `memory.recall(...)` the agent runs inside execute_code.
type memoryRecaller interface {
	ListMemories(ctx context.Context, f store.MemoryFilter) ([]store.MemoryEntry, error)
}

// SessionHookRequest is Claude Code's SessionStart / SessionEnd / Stop
// webhook payload. We accept extra fields without erroring so upstream
// additions don't break us. hook_event_name distinguishes start from end;
// `source` carries Claude Code's start reason ("startup", "resume", ...).
//
// NOTE: "Stop" is a per-TURN event (it fires after every assistant turn,
// not at session end), so it is handled distinctly from SessionEnd — see
// session(): Stop is acknowledged + audited but does NOT emit the capture
// nudge, which belongs only to the true session boundary (SessionEnd).
type SessionHookRequest struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`
	CWD           string `json:"cwd"`
}

// SessionHookResponse is what Claude Code expects back from a session hook.
// additionalContext is injected into the session as a system reminder; we
// use it to deliver the recall nudge + a digest of relevant memories at
// start, and the capture nudge at end (SessionEnd only). systemMessage
// surfaces a short line to the user/agent transcript.
type SessionHookResponse struct {
	HookSpecificOutput *sessionHookOutput `json:"hookSpecificOutput,omitempty"`
	SystemMessage      string             `json:"systemMessage,omitempty"`
}

// sessionHookOutput is Claude Code's nested hook output envelope. The
// hookEventName echoes the event; additionalContext is the injected text.
type sessionHookOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// sessionMemoryDigestLimit caps how many memory rows the start nudge
// inlines. Kept small so the injected context is a teaser ("recall found
// these — run memory.recall for more"), not a context-flooding dump.
const sessionMemoryDigestLimit = 5

// sessionHookEventStart / End are the canonical Claude Code event names the
// session endpoint understands. Any other event_name is acknowledged with
// an empty response (forward-compat, like the pretool non-Bash passthrough).
const (
	sessionHookEventStart = "SessionStart"
	sessionHookEventEnd   = "SessionEnd"
	sessionHookEventStop  = "Stop"
)

// session serves POST /v1/hooks/session. It bridges Claude Code's
// SessionStart, SessionEnd, and Stop events into mcplexer's memory contract:
//
//   - SessionStart: surface a recall nudge + a digest of recent
//     workspace-scoped memories so the agent recalls BEFORE acting, instead
//     of the advisory-only "consider memory.recall" that agents skip.
//   - SessionEnd: surface a capture nudge so decisions, preferences, and
//     anti-patterns from the session get saved BEFORE context is lost.
//   - Stop: a per-TURN event (fires after every assistant turn, NOT at
//     session end). Acknowledged + audited but emits NO capture nudge —
//     injecting the heavyweight "CAPTURE BEFORE ENDING" reminder on every
//     turn would flood the session. (We also stop registering Stop in the
//     installer; this no-op is defence in depth for clients that still send
//     it.)
//
// This is the memory analogue of the task-lease enforcement: recall/capture
// stop being purely advisory and become a session-lifecycle event the
// gateway actively injects. The nudge is lightweight — it never blocks the
// session — but it always fires, which is the behaviour change.
func (h *hooksHandler) session(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req SessionHookRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid hook payload")
		return
	}
	_ = r.Body.Close()

	// Resolve the workspace ONCE per request and reuse it for both the audit
	// row and the recall digest (avoids a second resolveWorkspaceFromCWD).
	wsID, wsName := h.resolveWorkspaceFromCWD(r.Context(), req.CWD)

	switch req.HookEventName {
	case sessionHookEventStart:
		h.recordSessionAudit(r.Context(), req, start, wsID, wsName)
		writeJSON(w, http.StatusOK, h.sessionStartResponse(r.Context(), req, wsID))
	case sessionHookEventEnd:
		h.recordSessionAudit(r.Context(), req, start, wsID, wsName)
		writeJSON(w, http.StatusOK, sessionEndResponse())
	case sessionHookEventStop:
		// Stop is a per-TURN event (fires after every assistant turn), NOT a
		// session boundary. Audit it (so the timeline still shows it) but do
		// NOT emit the capture nudge — that belongs to SessionEnd only.
		// Acknowledge with an empty body, like an unknown event.
		h.recordSessionAudit(r.Context(), req, start, wsID, wsName)
		writeJSON(w, http.StatusOK, SessionHookResponse{})
	default:
		// Unknown event — acknowledge without injecting anything
		// (forward-compat with future Claude Code session events).
		writeJSON(w, http.StatusOK, SessionHookResponse{})
	}
}

// sessionStartResponse builds the SessionStart reply. The reply is gated on
// req.Source (Claude Code's start reason):
//
//   - "startup" / "clear": a brand-new (or context-cleared) session that has
//     never seen the memory digest. Inject the FULL imperative "RECALL BEFORE
//     ACTING" nudge plus a digest of relevant workspace memories.
//   - "resume" / "compact" (or any other non-startup source): the session
//     already ran and its context was just resumed or compacted. Re-injecting
//     the full nudge+digest would re-flood the context that compaction just
//     discarded, so inject at most a single terse one-line reminder and NO
//     digest.
//
// The memory lookup is best-effort — a nil recaller or a lookup error degrades
// to the nudge alone, never an error to the agent (a broken recall must not
// break the session).
func (h *hooksHandler) sessionStartResponse(ctx context.Context, req SessionHookRequest, wsID string) SessionHookResponse {
	if !sourceWantsFullDigest(req.Source) {
		// Resumed/compacted session: a terse one-liner, no digest re-injection.
		return SessionHookResponse{
			HookSpecificOutput: &sessionHookOutput{
				HookEventName:     sessionHookEventStart,
				AdditionalContext: sessionResumeNudge,
			},
			SystemMessage: "mcplexer: recall relevant memory before acting (memory.recall).",
		}
	}
	digest := h.recallDigest(ctx, wsID)
	ctxText := sessionStartNudge + digest
	return SessionHookResponse{
		HookSpecificOutput: &sessionHookOutput{
			HookEventName:     sessionHookEventStart,
			AdditionalContext: ctxText,
		},
		SystemMessage: "mcplexer: recall relevant memory before acting (memory.recall).",
	}
}

// sourceWantsFullDigest reports whether the SessionStart source warrants the
// full nudge + memory digest. Only a fresh "startup" or an explicit context
// "clear" qualifies; "resume", "compact", and any unknown source get only a
// terse reminder so the digest compaction just discarded isn't re-flooded.
func sourceWantsFullDigest(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "startup", "clear":
		return true
	default:
		return false
	}
}

// sessionEndResponse builds the SessionEnd reply: a capture nudge so the
// agent saves durable knowledge before the session's context is lost. Only
// SessionEnd reaches this — Stop (per-turn) is a no-op (see session()).
func sessionEndResponse() SessionHookResponse {
	return SessionHookResponse{
		HookSpecificOutput: &sessionHookOutput{
			HookEventName:     sessionHookEventEnd,
			AdditionalContext: sessionEndNudge,
		},
		SystemMessage: "mcplexer: capture decisions/preferences/anti-patterns before ending (memory.save).",
	}
}

// recallDigest returns a short markdown digest of the most relevant
// memories for the given workspace, or "" when there is nothing to show (no
// recaller wired, lookup error, or no memories). wsID is resolved once by the
// caller (session) so the workspace lookup runs a single time per request. The
// digest is appended to the recall nudge so SessionStart context carries both
// "recall before acting" AND a concrete head-start.
func (h *hooksHandler) recallDigest(ctx context.Context, wsID string) string {
	if h.memories == nil {
		return ""
	}
	scope := store.SkillScope{}
	if wsID != "" {
		// Workspace ∪ global (SkillScope semantics): the agent sees both
		// project-specific facts and machine-wide preferences.
		scope.WorkspaceIDs = []string{wsID}
	}
	rows, err := h.memories.ListMemories(ctx, store.MemoryFilter{
		Scope: scope,
		Limit: sessionMemoryDigestLimit,
	})
	if err != nil || len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nRecent memories for this workspace (run `memory.recall({query})` for the full set):\n")
	for i := range rows {
		fmt.Fprintf(&b, "- %s\n", memoryDigestLine(&rows[i]))
	}
	return b.String()
}

// memoryDigestLine renders one memory row as a single bullet: name plus a
// truncated content preview. Kept terse so the injected digest stays a
// teaser, not a dump.
func memoryDigestLine(e *store.MemoryEntry) string {
	preview := strings.TrimSpace(e.Content)
	preview = strings.ReplaceAll(preview, "\n", " ")
	const maxPreview = 120
	// Truncate by RUNE, not byte, so a multibyte UTF-8 character is never cut
	// mid-sequence (a byte slice can split a rune and corrupt the preview).
	if r := []rune(preview); len(r) > maxPreview {
		preview = string(r[:maxPreview]) + "…"
	}
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return preview
	}
	if preview == "" {
		return name
	}
	return name + ": " + preview
}

// sessionStartNudge is the load-bearing recall reminder injected at the top
// of every session. It mirrors the imperative tone of the task-discipline
// rules: recall is a FIRST step, not an optional one.
const sessionStartNudge = "mcplexer memory contract — RECALL BEFORE ACTING. " +
	"Before answering a project-specific question or starting non-trivial work, " +
	"run `memory.recall({query})` inside `mcpx__execute_code` to pull decisions, " +
	"user preferences, project facts, and anti-patterns past sessions saved. " +
	"Memory is mesh-shared, embedding-indexed, and survives session end — chat does not."

// sessionResumeNudge is the terse one-liner injected when a session is
// resumed or compacted (source != "startup"/"clear"). It deliberately omits
// the memory digest: compaction just discarded that context, so re-injecting
// the full digest would re-flood it. A single reminder keeps the recall
// contract visible without the cost.
const sessionResumeNudge = "mcplexer memory contract — recall relevant memory " +
	"(`memory.recall({query})` inside `mcpx__execute_code`) before resuming non-trivial work."

// sessionEndNudge is the capture reminder injected when the session ends.
// Capture is the mirror of recall: durable knowledge that only lives in
// this session's context is lost the moment the session closes.
const sessionEndNudge = "mcplexer memory contract — CAPTURE BEFORE ENDING. " +
	"Save anything future sessions need that is NOT derivable from the repo: " +
	"decisions with rationale, user preferences, project facts, and anti-patterns. " +
	"Run `memory.save({...})` inside `mcpx__execute_code`. Do NOT save code (repo is " +
	"canonical), git history, or one-off task progress (use task notes)."

// recordSessionAudit emits one audit row for a session-hook event so the
// dashboard timeline shows recall/capture nudges alongside shell-guard and
// MCP activity. Best-effort: a nil auditor or a record error never changes
// the response the agent already received.
func (h *hooksHandler) recordSessionAudit(ctx context.Context, req SessionHookRequest, start time.Time, wsID, wsName string) {
	if h.auditor == nil {
		return
	}
	params, _ := json.Marshal(map[string]string{
		"event":  req.HookEventName,
		"source": req.Source,
		"cwd":    req.CWD,
	})
	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      start.UTC(),
		SessionID:      req.SessionID,
		ClientType:     "claude_code",
		WorkspaceID:    wsID,
		WorkspaceName:  wsName,
		Subpath:        req.CWD,
		ToolName:       "memory:" + strings.ToLower(req.HookEventName),
		ParamsRedacted: json.RawMessage(params),
		Status:         "nudge",
		LatencyMs:      int(time.Since(start) / time.Millisecond),
		CreatedAt:      time.Now().UTC(),
		ActorKind:      "user",
		ActorID:        req.SessionID,
	}
	_ = h.auditor.Record(ctx, rec)
}
