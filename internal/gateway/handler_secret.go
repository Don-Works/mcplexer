package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
)

// secretPromptToolDefinition returns the MCP tool that lets an agent request
// a secret from the human without ever seeing the value. The tool blocks
// synchronously from the agent's POV. On success the result contains a
// {file_path, handle, expires_at} JSON object — never the secret content.
func secretPromptToolDefinition() Tool {
	return Tool{
		Name: "secret__prompt",
		Description: "Request a secret from the human without ever putting " +
			"the value in your context. Returns a file path to a 0600 " +
			"owner-only file; the secret content is written by the daemon " +
			"and never appears in tool output.\n\n" +
			"WHEN TO USE — prefer this over asking the user to paste a " +
			"credential in chat. Any time you need a password, API token, " +
			"SSH key, or other secret to run a subprocess (psql, gh, ssh, " +
			"curl with auth, terraform, kubectl with a token, etc.), call " +
			"this FIRST instead of prompting in chat. Pasting puts the " +
			"value in your context window, breaks rotation, and persists " +
			"in transcripts; this tool avoids all of that.\n\n" +
			"HOW TO USE SAFELY — pass the file PATH (never the contents) " +
			"to a subprocess that reads the file directly. Preferred " +
			"patterns: `PGPASSFILE=$PATH psql ...`, `gh auth login " +
			"--with-token < $PATH`, `ssh -i $PATH user@host`, `curl " +
			"--config $PATH https://...` (config file with `header = " +
			"\"Authorization: Bearer ...\"`), or env-file loaders. " +
			"Acceptable but weaker: `$(<$PATH)` — the value lands on the " +
			"subprocess command line, visible to `ps` and shell history; " +
			"prefer stdin/--*-file flags when available.\n\n" +
			"NEVER — read the file in your own tools (`cat`, `Read`, " +
			"`head`, `print(open(path).read())`, `execute_code` snippets " +
			"that print the contents). Doing so loads the secret into your " +
			"context and triggers `delete_on_read`, breaking the consumer. " +
			"Never log or echo the path's contents. If a subprocess errors " +
			"and might print the secret in its output, redirect stderr/" +
			"stdout to a file you do not read, or filter before surfacing.\n\n" +
			"LIFECYCLE — file is hard-deleted on first read by default " +
			"(`delete_on_read: true`) and on `expires_at` regardless. " +
			"Default timeout 300s. Set `delete_on_read: false` only when " +
			"the consumer must read the file multiple times within the " +
			"timeout window.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"reason": {
					"type": "string",
					"description": "Short human-readable justification shown to the user. e.g. 'Connect to prod customers DB'."
				},
				"label": {
					"type": "string",
					"description": "Short label naming the secret. e.g. 'PROD_DATABASE_URL'."
				},
				"timeout_sec": {
					"type": "integer",
					"description": "Optional seconds to wait before timing out. Default: 300 (5 min)."
				},
				"delete_on_read": {
					"type": "boolean",
					"description": "If true (default) the file is hard-deleted on its first read. Set false for secrets the consumer reads multiple times before expiry."
				}
			},
			"required": ["reason", "label"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Request Secret from Human",
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}

// secretPromptArgs is the parsed args payload for secret__prompt.
type secretPromptArgs struct {
	Reason       string `json:"reason"`
	Label        string `json:"label"`
	TimeoutSec   int    `json:"timeout_sec"`
	DeleteOnRead *bool  `json:"delete_on_read,omitempty"`
}

// handleSecretPrompt executes the secret__prompt tool: it creates a pending
// row, fires the user-facing notification, blocks until the user resolves
// it, and returns the file path on success.
//
// The secret value NEVER appears in the audit log, broadcast, or response —
// only the file path (which is included only in the agent-private result).
func (h *handler) handleSecretPrompt(
	ctx context.Context, rawArgs json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.secretPrompts == nil {
		return marshalErrorResult(
			"secret__prompt is not enabled on this mcplexer instance.",
		), nil
	}

	var args secretPromptArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	v := newValidator()
	v.requireStringWithHint("reason", args.Reason,
		"short human-readable justification shown in the secret prompt UI")
	v.requireStringWithHint("label", args.Label,
		"short label naming the secret (e.g. \"PROD_DATABASE_URL\")")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	timeout := 300 * time.Second
	if args.TimeoutSec > 0 {
		timeout = time.Duration(args.TimeoutSec) * time.Second
	}
	deleteOnRead := true
	if args.DeleteOnRead != nil {
		deleteOnRead = *args.DeleteOnRead
	}

	req := ephemeral.PromptRequest{
		Reason:       args.Reason,
		Label:        args.Label,
		Requester:    h.sessions.sessionID(),
		Timeout:      timeout,
		DeleteOnRead: deleteOnRead,
	}

	created, err := h.secretPrompts.RequestPrompt(ctx, req)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("create prompt: %v", err),
		}
	}

	res, err := h.secretPrompts.Wait(ctx, created.ID)
	if err != nil {
		return secretPromptErrorResult(err), nil
	}

	payload := map[string]any{
		"file_path":  res.Path,
		"handle":     res.Handle,
		"expires_at": res.ExpiresAt.Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("marshal result: %v", err),
		}
	}
	return marshalToolResult(string(body)), nil
}

// secretListRefsToolDefinition returns the discovery tool that lets an
// agent enumerate available `secret://<key>` references it can splice
// into tool arguments. Returns scope+key names but NEVER plaintext.
//
// Refs are scope-bound: the same key under two scopes is two separate
// secrets, and a `secret://<key>` is resolved against the auth_scope of
// whichever server the call dispatches to. The output groups by scope
// so the agent can pick refs that match where their call is heading.
func secretListRefsToolDefinition() Tool {
	return Tool{
		Name: "secret__list_refs",
		Description: "List secret references available for substitution " +
			"into tool arguments. Returns ref names and the auth scope " +
			"each belongs to — NEVER the plaintext value.\n\n" +
			"USAGE — when a downstream tool needs a credential (api key, " +
			"bearer token, etc.), pass the value as a `secret://<key>` " +
			"string in the tool's arguments instead of the raw secret. " +
			"The gateway substitutes the plaintext into the downstream " +
			"call at dispatch time; the audit log only ever sees the " +
			"placeholder, and the plaintext never enters your context. " +
			"Refs resolve against the auth_scope of the server you're " +
			"calling — if a server has no auth_scope, refs cannot be " +
			"used.\n\n" +
			"Filter the output by scope name or ID with the optional " +
			"`scope` argument.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scope": {
					"type": "string",
					"description": "Optional. Filter results to a single auth_scope (matches by name or ID)."
				}
			}
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "List Secret References",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}

// secretListRefsArgs is the parsed args payload for secret__list_refs.
type secretListRefsArgs struct {
	Scope string `json:"scope,omitempty"`
}

// secretRefEntry is one row in the secret__list_refs result. The `ref`
// field is the literal value the agent should splice into tool args.
type secretRefEntry struct {
	ScopeID   string `json:"scope_id"`
	ScopeName string `json:"scope_name"`
	Key       string `json:"key"`
	Ref       string `json:"ref"`
}

// handleSecretListRefs enumerates scope+key tuples and returns them as
// secret://<key> references. The secrets manager's List() emits one
// `secret.list` audit row per enumerated scope (scope_id only, never
// the keys or count) — this is the existing forensic signal for "who
// queried what scope's inventory."
func (h *handler) handleSecretListRefs(
	ctx context.Context, rawArgs json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.secretsManager == nil {
		return marshalErrorResult(
			"secret__list_refs is not enabled (no secrets manager configured).",
		), nil
	}

	var args secretListRefsArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}

	scopes, err := h.store.ListAuthScopes(ctx)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("list auth scopes: %v", err),
		}
	}

	refs := make([]secretRefEntry, 0)
	for _, scope := range scopes {
		if args.Scope != "" && scope.ID != args.Scope && scope.Name != args.Scope {
			continue
		}
		keys, err := h.secretsManager.List(ctx, scope.ID)
		if err != nil {
			// A scope might have unreadable data (corruption, key mismatch);
			// skip it rather than fail the whole listing — the rest of the
			// inventory is still useful, and the secret.list audit row
			// already recorded the failure for forensics.
			continue
		}
		sort.Strings(keys)
		for _, key := range keys {
			refs = append(refs, secretRefEntry{
				ScopeID:   scope.ID,
				ScopeName: scope.Name,
				Key:       key,
				Ref:       "secret://" + key,
			})
		}
	}

	payload := map[string]any{"refs": refs}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("marshal result: %v", err),
		}
	}
	return marshalToolResult(string(body)), nil
}

// secretPromptErrorResult maps internal sentinels to structured isError
// tool results without leaking any secret content.
func secretPromptErrorResult(err error) json.RawMessage {
	switch {
	case errors.Is(err, ephemeral.ErrUserCancelled):
		return marshalErrorResult("Secret prompt cancelled by user.")
	case errors.Is(err, ephemeral.ErrPromptTimeout):
		return marshalErrorResult("Secret prompt timed out.")
	case errors.Is(err, ephemeral.ErrPromptNotFound):
		return marshalErrorResult("Secret prompt not found or already resolved.")
	default:
		return marshalErrorResult(fmt.Sprintf(
			"Secret prompt failed: %v", err,
		))
	}
}
