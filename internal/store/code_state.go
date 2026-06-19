package store

import (
	"context"
	"encoding/json"
	"time"
)

// CodeStateStore is a workspace-scoped key/value blob store for the code-mode
// sandbox. Each mcpx__execute_code call runs in a fresh goja VM, so in-memory
// JavaScript state never survives a call boundary. CodeStateStore lets an agent
// build an expensive dataset once (kv.set) and rehydrate it as a plain JS value
// in a later call (kv.get) — "build once, reuse many times" — without re-running
// the work or paying the API cost again.
//
// It complements DataWorkbenchStore: the data workbench is for tabular/document
// scratch queried with SQL/FTS, while CodeStateStore holds opaque JSON values
// keyed by name. Both are scratch, not durable conclusions (use MemoryStore for
// those).
type CodeStateStore interface {
	// SetCodeState upserts one entry by (workspace_id, key). The created_at of
	// an existing key is preserved across updates.
	SetCodeState(ctx context.Context, e *CodeStateEntry) error
	// GetCodeState returns one entry, or ErrNotFound when absent or expired.
	GetCodeState(ctx context.Context, workspaceID, key string) (*CodeStateEntry, error)
	// ListCodeState returns entry metadata (no values) for a workspace, newest
	// first, optionally filtered by key prefix.
	ListCodeState(ctx context.Context, f CodeStateFilter) ([]CodeStateEntry, error)
	// DeleteCodeState removes one entry. Returns ErrNotFound when absent.
	DeleteCodeState(ctx context.Context, workspaceID, key string) error
	// PruneExpiredCodeState hard-deletes expired, unpinned entries and returns
	// the count removed.
	PruneExpiredCodeState(ctx context.Context, now time.Time) (int, error)
}

// CodeStateEntry is a single key/value blob. ValueJSON is the agent-supplied
// value serialized as JSON; it is omitted from list views to keep them light.
type CodeStateEntry struct {
	WorkspaceID     string          `json:"workspace_id"`
	Key             string          `json:"key"`
	ValueJSON       json.RawMessage `json:"value_json,omitempty"`
	Bytes           int             `json:"bytes"`
	Pinned          bool            `json:"pinned"`
	TTLExpiresAt    *time.Time      `json:"ttl_expires_at,omitempty"`
	SourceSessionID string          `json:"source_session_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// CodeStateFilter scopes a ListCodeState call.
type CodeStateFilter struct {
	WorkspaceID    string
	Prefix         string
	IncludeExpired bool
	Limit          int
	Offset         int
}
