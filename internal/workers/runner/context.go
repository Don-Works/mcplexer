// Package runner: ctx-bound worker run metadata.
//
// When a worker run dispatches an in-process tool call (e.g. the
// Telegram concierge calling mcplexer__spawn_subagent), the receiving
// handler needs to know which worker/run made the call and what mesh
// message triggered it — so the spawned sub-agent can inherit the
// `reply_to_trigger` thread back to the human asking the question.
//
// We carry this via context.Context (parallel to audit.WithCorrelation)
// rather than a new method on every adapter / tool boundary; the
// runner attaches the metadata once at the top of RunWithOpts and
// every downstream call sees it for free. Read by handlers like
// handleSpawnSubagent via WorkerRunCtxFromContext.
package runner

import "context"

// WorkerRunCtx is the parent-run metadata that flows with every
// in-process tool dispatch. Every field is optional; readers should
// treat the zero value as "not in a worker run".
type WorkerRunCtx struct {
	WorkerID          string
	RunID             string
	WorkspaceID       string
	TriggerKind       string
	TriggerMessageID  string
	TriggerSourcePeer string
	TriggerChainDepth int
	// FilesystemRoot/WorkspacePath carry the canonical isolated checkout
	// boundary for delegated worktree runs. Empty for ordinary workers.
	FilesystemRoot string
	WorkspacePath  string
	ClaimedPaths   []string
	Branch         string
}

// workerRunCtxKey is the unexported context key used to stash
// WorkerRunCtx. Following the audit.WithCorrelation pattern: a typed
// empty struct avoids collisions with other packages' string keys.
type workerRunCtxKey struct{}

// WithWorkerRunCtx attaches c to ctx so downstream handlers can read
// the parent-run metadata. Nil-safe (passes through unchanged when
// ctx is nil — defers to context.Background equivalence).
func WithWorkerRunCtx(ctx context.Context, c WorkerRunCtx) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	c.ClaimedPaths = append([]string(nil), c.ClaimedPaths...)
	return context.WithValue(ctx, workerRunCtxKey{}, c)
}

// WorkerRunCtxFromContext returns the attached WorkerRunCtx, or the
// zero value + ok=false when none is attached. Handlers should treat
// ok=false as "this call did not originate from a worker run" and
// fall back to whatever the caller passed explicitly.
func WorkerRunCtxFromContext(ctx context.Context) (WorkerRunCtx, bool) {
	if ctx == nil {
		return WorkerRunCtx{}, false
	}
	v, ok := ctx.Value(workerRunCtxKey{}).(WorkerRunCtx)
	if !ok {
		return WorkerRunCtx{}, false
	}
	v.ClaimedPaths = append([]string(nil), v.ClaimedPaths...)
	return v, true
}
