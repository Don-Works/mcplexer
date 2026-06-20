package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/shellmeta"
	"github.com/don-works/mcplexer/internal/store"
)

// hooksHandler serves /v1/hooks/* endpoints. M1-A adds /v1/hooks/pretool;
// later milestones add posttool, schedule, etc. The handler bridges
// Claude Code's PreToolUse webhook into mcplexer's approval pipeline so
// shell commands proposed by the agent are validated and (optionally)
// gated behind a human-in-the-loop approval. Every decision (approve,
// cheap-block, denied, approval-error) emits an audit_records row via
// `auditor` so shell-guard activity shows on the dashboard Audit page
// alongside MCP tool calls.
type hooksHandler struct {
	approvalMgr approvalRequester
	auditor     auditRecorder // optional; nil disables audit emission
	// workspaces resolves the agent's cwd (from the PreToolUse
	// payload) to the matching workspace so the approval row + audit
	// row both carry workspace_id / workspace_name. Without this,
	// every shell-guard row surfaces as workspace="-" on the Audit
	// page and there is no way for a per-workspace allowlist rule to
	// surface "which project". Nil = no lookup (the pre-fix path);
	// rows just carry empty workspace ids.
	workspaces workspaceLookup
	// dangerousMode is the runtime accessor for the global "dangerous
	// mode" toggle. When set and the accessor returns true, the pretool
	// path:
	//   - skips every cheap-block (metachars, banned interpreters,
	//     eval flags),
	//   - skips the approval-manager round trip,
	//   - still records an audit row with status="dangerous-mode bypass"
	//     so the dashboard timeline accurately reflects what was waved
	//     through (so a follow-up review can answer "what was I
	//     blocked on?").
	// Nil = always off (the historical behaviour). The router wires this
	// from SettingsSvc.
	dangerousMode func() bool
	// shellGuardAllowChaining is the runtime accessor for the
	// ShellGuardAllowChaining setting. When set and the accessor returns
	// true (the default), the pretool path stops cheap-blocking command-
	// chaining metacharacters + substitutions: those commands fall through
	// to the normal approval + audit path instead of being hard-blocked.
	// The protected-path / secret guard still runs FIRST and unconditionally
	// (see pretool), so allowing chaining never opens a hole to ~/.mcplexer.
	// Nil = treat as the default (allow chaining). The router wires this
	// from SettingsSvc.
	shellGuardAllowChaining func() bool
	// memories is the narrow read surface the session hook uses to
	// surface a digest of relevant memories at SessionStart (see
	// hooks_session.go). Nil = no digest; the recall/capture nudges
	// still fire, just without the inline head-start. The router wires
	// this from deps.Store.
	memories memoryRecaller
}

// workspaceLookup is the narrow surface the hook handler depends on
// to resolve a Bash invocation's cwd to a workspace. Defined as an
// interface so tests can inject a fake without dragging the full
// store.Store dependency into the api package's test surface.
type workspaceLookup interface {
	ListWorkspaces(ctx context.Context) ([]store.Workspace, error)
}

// resolveWorkspaceFromCWD walks the configured workspaces and returns
// (id, name) of the one whose RootPath is the most specific match for
// cwd — i.e. RootPath == cwd, or RootPath/ is a prefix of cwd. The
// longest matching RootPath wins so nested workspaces (parent at
// /Users/me, child at /Users/me/project) resolve to the child for
// cwd values under /Users/me/project. Returns ("", "") when no
// workspace matches OR when the lookup isn't wired (h.workspaces nil).
//
// Both sides are passed through filepath.Clean so superficial
// differences ("/srv/wsA/", "/srv/wsA/.", "//srv/wsA") all match the
// same canonical form. The clean is critical for the dashboard form
// where operators routinely paste paths with trailing slashes.
func (h *hooksHandler) resolveWorkspaceFromCWD(ctx context.Context, cwd string) (id, name string) {
	if h.workspaces == nil || cwd == "" {
		return "", ""
	}
	cleanCwd := filepath.Clean(cwd)
	list, err := h.workspaces.ListWorkspaces(ctx)
	if err != nil || len(list) == 0 {
		return "", ""
	}
	bestLen := -1
	for i := range list {
		w := &list[i]
		if w.RootPath == "" {
			continue
		}
		root := filepath.Clean(w.RootPath)
		match := cleanCwd == root
		if !match {
			prefix := root + string(filepath.Separator)
			if strings.HasPrefix(cleanCwd, prefix) {
				match = true
			}
		}
		if match && len(root) > bestLen {
			bestLen = len(root)
			id = w.ID
			name = w.Name
		}
	}
	return id, name
}

// approvalRequester is the narrow surface of *approval.Manager that the
// hooks handler depends on. Keeping this as an interface lets us inject
// fakes from tests without reaching into the approval package.
//
// HasAllowMetacharsMatch is the read-only "would the resolver auto-approve
// this via a rule that has opted in to metachar passthrough?" probe used
// by the shell hook to decide whether to short-circuit its
// cheap-block-on-metacharacters rejection. Implementations must return
// false when no such rule is configured.
type approvalRequester interface {
	RequestApproval(ctx context.Context, a *store.ToolApproval) (bool, error)
	HasAllowMetacharsMatch(a *store.ToolApproval) bool
}

// resolverAllowsMetachars is the hook's call into the manager probe.
// Tolerates a nil approvalMgr (in tests / setup edge cases) by
// returning false so the cheap-block stays active.
func (h *hooksHandler) resolverAllowsMetachars(a *store.ToolApproval) bool {
	if h.approvalMgr == nil || a == nil {
		return false
	}
	return h.approvalMgr.HasAllowMetacharsMatch(a)
}

// chainingAllowed reports whether command-chaining metacharacters +
// substitutions should be allowed through to the approval path rather than
// cheap-blocked. Nil accessor = the default (allow chaining), so older
// wirings / tests that don't set it get the new behaviour. This ONLY
// governs the metachar/substitution cheap-block; the protected-path guard
// runs regardless.
func (h *hooksHandler) chainingAllowed() bool {
	if h.shellGuardAllowChaining == nil {
		return true
	}
	return h.shellGuardAllowChaining()
}

// dangerousModeProtectedPath runs the protected-mcplexer-path guard over
// both the whole command line and the exe/args split, returning the first
// violation. Factored out so the dangerous-mode branch and the normal path
// apply identical containment — both relax chaining, so both must catch a
// chained protected path on the raw line, not only on a clean argv token.
func dangerousModeProtectedPath(fullCmd, exe string, rest []string) error {
	if err := downstream.ValidateLocalBashLine(fullCmd); err != nil {
		return err
	}
	return downstream.ValidateLocalBashExec(exe, rest)
}

// auditRecorder is the narrow surface of *audit.Logger we depend on.
// Mirrors the approvalRequester pattern so tests can wire a fake without
// pulling the audit package into the api test deps.
type auditRecorder interface {
	Record(ctx context.Context, rec *store.AuditRecord) error
}

// PreToolHookRequest is Claude Code's PreToolUse webhook payload. We
// accept extra fields (omitted from this struct) without erroring so
// upstream additions don't break us.
type PreToolHookRequest struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	CWD           string          `json:"cwd"`
}

// PreToolHookResponse is what Claude Code expects back. decision="block"
// vetoes the call; "approve" or missing means proceed.
type PreToolHookResponse struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// hookPretoolTimeoutSec is the approval timeout for /v1/hooks/pretool.
// Hardcoded for M1-A; later milestones can pull it from settings.
const hookPretoolTimeoutSec = 60

// pretool serves POST /v1/hooks/pretool. It decodes Claude Code's
// PreToolUse payload, fast-blocks unsafe Bash commands, and otherwise
// gates the call through the approval manager with Surface="shell".
// Every terminal decision (pass-through, block, approve, error) is
// audited via h.recordPretoolAudit so the dashboard Audit page shows the
// full shell-guard timeline alongside MCP tool calls.
func (h *hooksHandler) pretool(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.approvalMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "approval service not configured")
		return
	}

	var req PreToolHookRequest
	dec := json.NewDecoder(r.Body)
	// Don't DisallowUnknownFields — Claude Code may add new fields
	// (permission_mode, etc.) and we must not 400 on them.
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid hook payload")
		return
	}
	_ = r.Body.Close()

	// M1-A only guards Bash. Other tools pass without approval and
	// without audit — we'd flood audit_records with every Read/Edit
	// the agent makes. M2+ adds per-tool surfaces if/when needed.
	if req.ToolName != "Bash" {
		writeJSON(w, http.StatusOK, PreToolHookResponse{})
		return
	}

	fullCmd, exe, rest, description, ok := extractBashCommand(req.ToolInput)
	if !ok {
		// No parsable command — treat as a hard block; the agent
		// shouldn't be running an empty Bash invocation. Even in
		// dangerous mode this stays a hard block because there's
		// literally nothing to execute.
		h.recordPretoolAudit(r.Context(), req, "", "", "blocked",
			"missing or invalid Bash command", start)
		writeJSON(w, http.StatusOK, PreToolHookResponse{
			Decision: "block",
			Reason:   "missing or invalid Bash command",
		})
		return
	}

	// Dangerous-mode bypass: every shell hit gets a free pass while the
	// toggle is on. Cheap-block patterns (metachars, banned interpreters,
	// eval flags) are NOT evaluated — by design, the user has opted out
	// of every approval gate. The audit row keeps the "what would have
	// been gated" signal alive so the post-hoc review pipeline can
	// reconstruct the full timeline.
	//
	// EXCEPT the protected-path lockdown: dangerous mode opts out of
	// approval gates, not the mcplexer data-dir contract. The DB/secrets/
	// key files are off-limits to AI tool calls at every layer (harness
	// denylist, this hook, gateway cmdguard, file modes) — leaving local
	// Bash exempt here while downstream spawns stayed guarded was an
	// asymmetry a prompt-injection could aim at the moment the user
	// flipped the toggle.
	if h.dangerousMode != nil && h.dangerousMode() {
		// Scan the WHOLE command line as well as the exe/args split: like the
		// chaining-allowed path below, dangerous mode does not cheap-block
		// chaining, so a chained protected path must be caught on the raw
		// line, not just on a cleanly-tokenised argv.
		if err := dangerousModeProtectedPath(fullCmd, exe, rest); err != nil {
			reason := err.Error() + " (protected paths stay blocked in dangerous mode)"
			h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "blocked", reason, start)
			writeJSON(w, http.StatusOK, PreToolHookResponse{
				Decision: "block",
				Reason:   reason,
			})
			return
		}
		h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "dangerous-mode bypass", "", start)
		writeJSON(w, http.StatusOK, PreToolHookResponse{})
		return
	}

	wsID, wsName := h.resolveWorkspaceFromCWD(r.Context(), req.CWD)
	a := buildShellApproval(req, fullCmd, exe, description, wsID, wsName)

	// Protected-path guard FIRST — and unconditionally. This is the
	// load-bearing safety property: the mcplexer data-dir lockdown
	// (DB / secrets / api-key / p2p / backups) must hard-block BEFORE any
	// chaining decision, so that allowing chaining can never become a hole
	// to ~/.mcplexer. We scan the WHOLE command line (ValidateLocalBashLine)
	// so a chained `echo ok; cat ~/.mcplexer/api-key` is caught regardless
	// of how the agent spaces the chain, AND the exe/args split
	// (ValidateLocalBashExec) which adds nothing the line scan misses but
	// preserves the historical per-token error wording. Neither is governed
	// by ShellGuardAllowChaining or the AllowMetachars opt-in.
	if err := downstream.ValidateLocalBashLine(fullCmd); err != nil {
		h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "blocked", err.Error(), start)
		writeJSON(w, http.StatusOK, PreToolHookResponse{
			Decision: "block",
			Reason:   err.Error(),
		})
		return
	}
	if err := downstream.ValidateLocalBashExec(exe, rest); err != nil {
		h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "blocked", err.Error(), start)
		writeJSON(w, http.StatusOK, PreToolHookResponse{
			Decision: "block",
			Reason:   err.Error(),
		})
		return
	}

	// Cheap-block: shell metachars + substitutions. Only a guard concern —
	// no human approval needed; reject without prompting.
	//
	// The metachar block on fullCmd protects against an agent chaining
	// destructive commands together (`git status; rm -rf $HOME`). It is
	// LIFTED when either:
	//   - ShellGuardAllowChaining is on (the default): the operator has
	//     accepted chaining, so the command flows through to the normal
	//     approval + audit path instead of dying here, OR
	//   - a matching AllowMetachars rule (typically the amber "Allow +
	//     audit everything" wildcard) opts this request in explicitly.
	// In both cases the protected-path guard above has already run, so
	// nothing reaching ~/.mcplexer can slip through. When chaining is NOT
	// allowed and no AllowMetachars rule matches, the historical hard-block
	// stays in force.
	allowMetachars := h.chainingAllowed() || h.resolverAllowsMetachars(a)
	if !allowMetachars {
		if c, ok := shellmeta.ContainsUnquotedMetachar(fullCmd); ok {
			reason := "shell command contains metacharacter " + string(c)
			h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "blocked", reason, start)
			writeJSON(w, http.StatusOK, PreToolHookResponse{
				Decision: "block",
				Reason:   reason,
			})
			return
		}
		if sub := shellmeta.FindUnquotedSubstitution(fullCmd); sub != "" {
			reason := "shell command contains substitution " + sub
			h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "blocked", reason, start)
			writeJSON(w, http.StatusOK, PreToolHookResponse{
				Decision: "block",
				Reason:   reason,
			})
			return
		}
	}

	approved, err := h.approvalMgr.RequestApproval(r.Context(), a)
	if err != nil {
		reason := "approval failed: " + err.Error()
		h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "error", reason, start)
		writeJSON(w, http.StatusOK, PreToolHookResponse{
			Decision: "block",
			Reason:   reason,
		})
		return
	}

	if approved {
		h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "success", "", start)
		// Empty body = "proceed". Claude Code treats absent decision
		// as approve.
		writeJSON(w, http.StatusOK, PreToolHookResponse{})
		return
	}

	reason := a.Resolution
	if reason == "" {
		reason = a.Status
	}
	h.recordPretoolAudit(r.Context(), req, exe, fullCmd, "blocked", reason, start)
	writeJSON(w, http.StatusOK, PreToolHookResponse{
		Decision: "block",
		Reason:   reason,
	})
}

// recordPretoolAudit emits a single audit row for one pretool decision.
// Tool name is normalised to `shell:<exe-basename>` (matching the
// approval surface) so the leaderboard/error-breakdown widgets group
// shell hits together. params_redacted carries command+cwd so the audit
// row reads like the agent's actual intent; secrets-style redaction is a
// no-op here because the auditor's per-scope hints don't apply (no
// auth_scope_id on shell hits) — sensitive args live in env vars that
// the agent doesn't pass through Claude Code's Bash tool. Errors from
// the auditor are swallowed: a failed audit must NOT change the user-
// visible block/approve decision the agent already received.
func (h *hooksHandler) recordPretoolAudit(
	ctx context.Context, req PreToolHookRequest, exe, fullCmd, status, errMsg string, start time.Time,
) {
	if h.auditor == nil {
		return
	}
	toolName := "shell:unknown"
	if exe != "" {
		toolName = "shell:" + strings.ToLower(filepath.Base(exe))
	}
	// Redact secret-shaped substrings (Bearer tokens, ghp_*, sk-*, etc.)
	// from the command before persisting. Run the JSON body through the
	// same Redact pipeline the rest of the audit log uses so a Bash
	// invocation like `curl -H "Authorization: Bearer hunter2" ...`
	// lands as `[REDACTED]` instead of the raw token.
	params, _ := json.Marshal(map[string]string{
		"command": fullCmd,
		"cwd":     req.CWD,
	})
	params = audit.Redact(params, nil)
	wsID, wsName := h.resolveWorkspaceFromCWD(ctx, req.CWD)
	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      start.UTC(),
		SessionID:      req.SessionID,
		ClientType:     "claude_code",
		WorkspaceID:    wsID,
		WorkspaceName:  wsName,
		Subpath:        req.CWD,
		ToolName:       toolName,
		ParamsRedacted: json.RawMessage(params),
		Status:         status,
		ErrorMessage:   errMsg,
		LatencyMs:      int(time.Since(start) / time.Millisecond),
		CreatedAt:      time.Now().UTC(),
		ActorKind:      "user",
		ActorID:        req.SessionID,
	}
	_ = h.auditor.Record(ctx, rec)
}

// buildShellApproval assembles the approval record for a Bash hook
// invocation. Arguments is JSON-encoded so the existing dashboard
// "arguments" column stays parseable; the full agent-supplied command
// lives under the "command" key, while ToolName surfaces just the
// executable basename for quick scanning.
//
// wsID / wsName tag the row with the workspace whose RootPath the
// agent's cwd lands inside. Empty strings are tolerated end-to-end —
// the UI renders them as "-" rather than crashing — but populating
// them is what lets the Audit page actually show "Project A" instead
// of a dash.
func buildShellApproval(
	req PreToolHookRequest,
	fullCmd, exe, description, wsID, wsName string,
) *store.ToolApproval {
	argsPayload := map[string]string{
		"command": fullCmd,
		"cwd":     req.CWD,
	}
	argsJSON, _ := json.Marshal(argsPayload)

	base := strings.ToLower(filepath.Base(exe))
	return &store.ToolApproval{
		Surface:           "shell",
		ToolName:          "shell:" + base,
		Arguments:         string(argsJSON),
		RequestSessionID:  req.SessionID,
		RequestClientType: "claude_code",
		WorkspaceID:       wsID,
		WorkspaceName:     wsName,
		Justification:     description,
		TimeoutSec:        hookPretoolTimeoutSec,
	}
}

// extractBashCommand parses Claude Code's Bash tool_input shape:
//
//	{"command": "git status", "description": "check tree"}
//
// Returns:
//   - fullCmd: the trimmed agent-supplied command string (preserves
//     args + spacing so the dashboard / audit log can show exactly
//     what the agent asked for).
//   - exe: the first whitespace-delimited token, used to drive the
//     interpreter / eval-flag allowlist in downstream.ValidateCommand.
//   - rest: the remaining tokens, passed alongside exe.
//   - description: the agent's "why" string when present.
//   - ok=false when the command is missing/empty.
//
// Tokenization is intentionally naive (strings.Fields). Bash commands
// are shell-evaluated by Claude Code anyway, so we don't try to mirror
// quoting; the metachar pre-check in the handler catches the cases
// that would actually break this tokenizer's safety story.
func extractBashCommand(raw json.RawMessage) (fullCmd, exe string, rest []string, description string, ok bool) {
	if len(raw) == 0 {
		return "", "", nil, "", false
	}
	var input struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", "", nil, "", false
	}
	trimmed := strings.TrimSpace(input.Command)
	if trimmed == "" {
		return "", "", nil, input.Description, false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", "", nil, input.Description, false
	}
	return trimmed, fields[0], fields[1:], input.Description, true
}
