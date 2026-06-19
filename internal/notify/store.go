package notify

import (
	"context"
	"time"
)

// StoredEvent is a notification with persistence metadata. ID is the
// auto-increment row key (used for stable ordering + read/unread
// transitions); ReadAt is nil for unread events. See Event for the
// Source/Kind distinction.
type StoredEvent struct {
	ID        int64      `json:"id"`
	MessageID string     `json:"message_id"`
	Source    string     `json:"source"`
	AgentName string     `json:"agent_name"`
	Role      string     `json:"role"`
	Kind      string     `json:"kind"`
	Priority  string     `json:"priority"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Tags      string     `json:"tags"`
	Link      string     `json:"link"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

// ListFilter narrows the result set for a history query. Empty values
// mean "no filter on that dimension."
type ListFilter struct {
	Source     string // "mesh" / "approval" / "system" / "secret"; empty = all
	Kind       string // producer sub-classification; empty = all
	Priority   string // "critical" / "high" / "normal" / "low"; empty = all
	UnreadOnly bool
	BeforeID   int64 // pagination cursor — return rows with id < BeforeID; 0 = newest
	Limit      int   // hard-capped to 200 server-side
}

// Store persists notifications so the Signal tray survives page reloads
// and serves backfill on open. The SSE Bus stays the live channel; this
// is the durable record.
type Store interface {
	// Insert adds an event. Returns the assigned ID. If message_id
	// duplicates an existing row, the existing ID is returned and no
	// new row is created — producers can safely re-publish on retry.
	Insert(ctx context.Context, evt Event) (int64, error)
	// List returns events in newest-first order matching the filter.
	List(ctx context.Context, f ListFilter) ([]StoredEvent, error)
	// MarkRead sets read_at = now for the given IDs. Idempotent on
	// already-read rows.
	MarkRead(ctx context.Context, ids []int64) error
	// MarkAllRead sets read_at = now for every unread row.
	MarkAllRead(ctx context.Context) error
	// UnreadCount returns the count of unread events (read_at NULL).
	UnreadCount(ctx context.Context) (int, error)
	// Prune evicts oldest-read rows first, then oldest period, until
	// the table holds at most `cap` rows. Returns rows deleted.
	Prune(ctx context.Context, cap int) (int, error)
}
