package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

// compressionMinBytes is the smallest tool-result payload worth running the
// compression pipeline over. Below this the measurement cost outweighs any
// realistic saving.
const compressionMinBytes = 256

// newCompressionPipeline builds the gateway's token-compression pipeline with
// the default transform set, wired to the token estimator. The pipeline is
// stateless; the effective mode is resolved per-call from settings.
func newCompressionPipeline() *compression.Pipeline {
	p := compression.New(func(n int) int { return models.EstimateContextTokens(n) }, compressionMinBytes)
	p.Register(compression.DefaultTransforms()...)
	return p
}

// compressionMode resolves the effective compression mode from settings,
// defaulting to shadow (measure-only, dry-run) when settings are unavailable.
func (h *handler) compressionMode(ctx context.Context) compression.Mode {
	if h != nil && h.settingsSvc != nil {
		return compression.ParseMode(h.settingsSvc.Load(ctx).CompressionMode)
	}
	return compression.ModeShadow
}

// compressionDisabled returns the set of transform names the operator has
// toggled off in settings. nil (all enabled) is the common case.
func (h *handler) compressionDisabled(ctx context.Context) map[string]bool {
	if h == nil || h.settingsSvc == nil {
		return nil
	}
	list := h.settingsSvc.Load(ctx).CompressionDisabledTransforms
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]bool, len(list))
	for _, n := range list {
		m[n] = true
	}
	return m
}

// persistCompression writes the pipeline's observations to the durable savings
// ledger so the dashboard sees cross-connection, restart-surviving numbers
// (the in-memory ContextCostStats only ever sees one socket connection).
// Best-effort: measurement must never fail or meaningfully slow a tool call.
func (h *handler) persistCompression(ctx context.Context, obs []compression.Observation) {
	if h == nil || h.store == nil || len(obs) == 0 {
		return
	}
	rows := make([]store.CompressionObservation, 0, len(obs))
	for _, o := range obs {
		wb, wt := o.SavedBytes, o.SavedTokens
		if wb < 0 {
			wb = 0
		}
		if wt < 0 {
			wt = 0
		}
		ab, at := 0, 0
		if o.Applied {
			ab, at = wb, wt
		}
		rows = append(rows, store.CompressionObservation{
			Transform:         o.Transform,
			Lossless:          o.Lossless,
			Changed:           o.Changed,
			Applied:           o.Applied,
			OrigBytes:         o.OrigBytes,
			WouldSaveBytes:    wb,
			WouldSaveTokens:   wt,
			AppliedSaveBytes:  ab,
			AppliedSaveTokens: at,
		})
	}
	// Attribute to the session's workspace so the /stats workspace_id filter is
	// meaningful; empty (no bound workspace) still aggregates globally.
	if err := h.store.RecordCompression(ctx, h.currentWorkspaceID(ctx), time.Now(), rows); err != nil {
		slog.Debug("persist compression stats", "err", err)
	}
}
