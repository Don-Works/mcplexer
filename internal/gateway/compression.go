package gateway

import (
	"context"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/models"
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
