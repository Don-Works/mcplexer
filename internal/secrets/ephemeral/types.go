// Package ephemeral implements human-in-the-loop secret injection. The agent
// calls the secret__prompt MCP tool to request a secret without ever seeing
// its value. mcplexer creates a pending prompt, fires a UI/native
// notification, and blocks until the user submits or cancels. Submitted
// secrets are written to a 0600 file owned by the daemon under
// {data_dir}/secrets/ephemeral/<random> and the path is returned to the
// agent — never the value.
//
// Each file is hard-deleted on first read (kqueue on macOS, inotify on
// Linux) when DeleteOnRead is true, or by a background sweeper that
// removes any file whose prompt row is past its expires_at.
package ephemeral

import (
	"errors"
	"time"
)

// Sentinel errors. Callers compare with errors.Is.
var (
	// ErrUserCancelled means the human pressed Cancel in the prompt UI.
	ErrUserCancelled = errors.New("secret prompt cancelled by user")
	// ErrPromptTimeout means the prompt expired before submission.
	ErrPromptTimeout = errors.New("secret prompt timed out")
	// ErrPromptNotFound means no pending prompt with the given id exists.
	ErrPromptNotFound = errors.New("secret prompt not found")
	// ErrPromptAlreadyResolved means the prompt is no longer pending.
	ErrPromptAlreadyResolved = errors.New("secret prompt already resolved")
)

// PromptRequest is the input to RequestPrompt. Reason and Label are shown to
// the human in the UI; Requester is recorded in the audit row (e.g. session
// id or skill id) but is never sent back to the agent. DeleteOnRead defaults
// to true — set to false to disable file-watch deletion (file is still
// hard-deleted on expiry).
type PromptRequest struct {
	Reason       string
	Label        string
	Requester    string
	Timeout      time.Duration
	DeleteOnRead bool
}

// PromptResult is what the agent receives on submission. The Path is a
// 256-bit random file under the daemon-owned secrets dir; the agent passes
// it to subprocesses (psql/curl/ssh) which read it directly. ExpiresAt is
// the absolute deadline after which the sweeper hard-deletes the file
// regardless of read status.
type PromptResult struct {
	ID        string
	Path      string
	Handle    string // == ID, exposed under a friendlier name for the agent
	ExpiresAt time.Time
}

// PromptCreated is what the manager hands back to the gateway after creating
// a pending prompt. It does NOT include the path — the path only exists once
// the user actually submits.
type PromptCreated struct {
	ID        string
	ExpiresAt time.Time
}

// AuditHook is invoked at lifecycle boundaries so callers can record audit
// rows. The hook NEVER receives the secret value or the file path — only
// metadata. event is one of "created", "submitted", "cancelled", "timeout".
type AuditHook func(event string, prompt PromptRequest, id string)
