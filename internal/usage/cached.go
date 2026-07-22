package usage

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// CachedSnapshot returns the last persisted usage dashboard for the given
// configs and window WITHOUT probing any provider. It is the strictly
// side-effect-free read path: unlike Snapshot(force=false), it never runs a
// provider collector, never triggers the background refresh, and never writes
// the cache — a cold or stale cache is reported as "not found" rather than
// silently kicking a probe.
//
// This exists so a non-admin agent surface (e.g. a delegating model asking
// "how much frontier quota is left?") can read allowance/observed numbers a
// deterministic, zero-cost way. The dashboard, admin tools, and API keep using
// Snapshot to assemble and refresh the cache.
//
// found is false when no snapshot has been assembled for this key yet. Callers
// MUST treat that as "data unavailable", never as zero usage/remaining.
func (s *Service) CachedSnapshot(
	ctx context.Context,
	configs []store.SourceConfig,
	days int,
) (store.UsageSnapshot, bool, error) {
	days = normalizeDays(days)
	key := snapshotCacheKey(configs, days)
	return s.loadPersistedSnapshot(ctx, key)
}
