package store

import (
	"context"
	"time"
)

// CompressionObservation is one transform's measured effect on one tool
// result, recorded by the gateway compression pipeline. Would* is the measured
// potential saving (accrues in any non-off mode, including shadow/dry-run);
// Applied* is what was actually used (on mode only). Would* values are
// clamped to >= 0 by the caller.
type CompressionObservation struct {
	Transform         string
	Lossless          bool
	Changed           bool
	Applied           bool
	OrigBytes         int
	WouldSaveBytes    int
	WouldSaveTokens   int
	AppliedSaveBytes  int
	AppliedSaveTokens int
}

// CompressionAggregate is the savings rollup over a day window. NOTE: with
// more than one transform the top-level Would*/Orig* sums can overlap (each
// transform measures the same incoming payload in shadow), so treat them as
// upper bounds and read ByTransform for per-transform truth. AppliedSave* are
// exact (applied transforms chain non-overlapping). Today only one transform
// ships, so the sums are exact.
type CompressionAggregate struct {
	Days              int                             `json:"days"`
	Samples           int64                           `json:"samples"`
	OrigBytes         int64                           `json:"orig_bytes"`
	WouldSaveBytes    int64                           `json:"would_save_bytes"`
	WouldSaveTokens   int64                           `json:"would_save_tokens"`
	AppliedSaveBytes  int64                           `json:"applied_save_bytes"`
	AppliedSaveTokens int64                           `json:"applied_save_tokens"`
	ByTransform       []CompressionTransformAggregate `json:"by_transform"`
	Daily             []CompressionDailyPoint         `json:"daily"`
}

// CompressionTransformAggregate is the per-transform observed effect — the
// numbers shown next to each transform's toggle in the settings UI.
type CompressionTransformAggregate struct {
	Transform         string `json:"transform"`
	Lossless          bool   `json:"lossless"`
	Samples           int64  `json:"samples"`
	Changed           int64  `json:"changed"`
	OrigBytes         int64  `json:"orig_bytes"`
	WouldSaveBytes    int64  `json:"would_save_bytes"`
	WouldSaveTokens   int64  `json:"would_save_tokens"`
	Applied           int64  `json:"applied"`
	AppliedSaveBytes  int64  `json:"applied_save_bytes"`
	AppliedSaveTokens int64  `json:"applied_save_tokens"`
}

// CompressionDailyPoint is one UTC day's token savings for the sparkline.
type CompressionDailyPoint struct {
	Date              string `json:"date"`
	WouldSaveTokens   int64  `json:"would_save_tokens"`
	AppliedSaveTokens int64  `json:"applied_save_tokens"`
}

// CompressionStatsStore persists the token-compression savings ledger so the
// dashboard shows observed per-transform savings that survive daemon restarts.
type CompressionStatsStore interface {
	// RecordCompression upserts the daily rollup for each observation into its
	// (workspace_id, transform, day) bucket. day is derived from now (UTC).
	// Best-effort from the gateway hot path — implementations must be cheap.
	RecordCompression(ctx context.Context, workspaceID string, now time.Time, obs []CompressionObservation) error

	// CompressionAggregate returns the savings rollup over the last `days` UTC
	// days: overall totals, per-transform breakdown, and a contiguous daily
	// token-savings series. Empty workspaceID aggregates every workspace.
	CompressionAggregate(ctx context.Context, workspaceID string, days int, now time.Time) (CompressionAggregate, error)
}
