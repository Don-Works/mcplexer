package usage

import (
	"context"
	"math"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func (s *Service) providerSnapshot(
	ctx context.Context,
	provider string,
	cfg store.SourceConfig,
	runs []store.UsageLedgerRun,
	local map[string]localStatsResult,
	ledgerErr error,
	force bool,
	now time.Time,
) store.ProviderSnapshot {
	snapshot := baseProviderSnapshot(provider, cfg)
	applyObservedLineage(&snapshot, provider, runs, local, ledgerErr, now)
	if cfg.Kind == store.SourceKindManual && cfg.Limit > 0 {
		applyManualAllowance(&snapshot, cfg, now)
	} else if cfg.Kind == store.SourceKindAPI || cfg.Kind == store.SourceKindCLI {
		allowance := s.providerAllowance(ctx, cfg, force, now)
		mergeProviderAllowance(&snapshot, allowance)
	}
	finishProviderStatus(&snapshot, provider, cfg, ledgerErr)
	setBackwardCompatFields(&snapshot)
	return snapshot
}

func baseProviderSnapshot(
	provider string,
	cfg store.SourceConfig,
) store.ProviderSnapshot {
	label := cfg.Label
	if label == "" {
		label = store.ProviderLabels[provider]
	}
	return store.ProviderSnapshot{
		Provider: provider,
		Label:    label,
		Plan:     cfg.Plan,
		Windows:  []store.UsageWindow{},
	}
}

func applyObservedLineage(
	snapshot *store.ProviderSnapshot,
	provider string,
	runs []store.UsageLedgerRun,
	local map[string]localStatsResult,
	ledgerErr error,
	now time.Time,
) {
	snapshot.Observed = observedFromLedger(runs, provider)
	snapshot.ObservedSource = "ledger"
	snapshot.ObservedSourceLabel = "mcplexer worker ledger"
	if hasObserved(snapshot.Observed) {
		snapshot.ObservedUpdatedAt = &now
		snapshot.ObservedCostKind = store.ObservedCostMetered
	}
	if ledgerErr != nil {
		snapshot.Detail = appendDetail(snapshot.Detail, "mcplexer ledger unavailable")
	}
	if observed, source, ok := observedFromLocal(provider, local); ok {
		snapshot.Observed = observed
		snapshot.ObservedSource = "cli"
		snapshot.ObservedSourceLabel = source
		snapshot.ObservedUpdatedAt = localUpdatedAt(local, provider, now)
		snapshot.ObservedCostKind = store.ObservedCostEstimate
	}
}

func observedFromLedger(
	runs []store.UsageLedgerRun,
	provider string,
) store.ObservedUsage {
	agg := AggregateLedger(runs, provider)
	return store.ObservedUsage{
		Requests:              agg.Requests,
		InputTokens:           agg.InputTokens,
		OutputTokens:          agg.OutputTokens,
		CacheReadTokens:       agg.CacheReadTokens,
		CacheWriteTokens:      agg.CacheWriteTokens,
		CostUSD:               agg.CostUSD,
		AccountingMissingRuns: agg.AccountingMissingRuns,
	}
}

func (s *Service) providerAllowance(
	ctx context.Context,
	cfg store.SourceConfig,
	force bool,
	now time.Time,
) store.ProviderSnapshot {
	key := sourceCacheKey(cfg)
	if !force {
		if cached, ok := s.cachedProvider(key, now); ok {
			return cached
		}
	}
	flight, leader := s.beginProviderFlight(key)
	if !leader {
		return waitProviderFlight(ctx, flight, cfg)
	}
	snapshot := s.collectProviderAllowance(ctx, cfg, now)
	snapshot = s.putProviderCache(key, snapshot, now)
	s.finishProviderFlight(key, flight, snapshot)
	return snapshot
}

func (s *Service) collectProviderAllowance(
	ctx context.Context,
	cfg store.SourceConfig,
	now time.Time,
) store.ProviderSnapshot {
	collector := s.Collectors[cfg.Provider]
	if collector == nil {
		return providerError(cfg, store.StatusUnavailable, "collector unavailable")
	}
	result, err := collector.Fetch(ctx, cfg)
	snapshot := result.Snapshot
	if err != nil {
		snapshot = providerError(cfg, store.StatusError, err.Error())
	}
	normalizeProviderAllowance(&snapshot, cfg, now)
	return snapshot
}

func normalizeProviderAllowance(
	snapshot *store.ProviderSnapshot,
	cfg store.SourceConfig,
	now time.Time,
) {
	if snapshot.Provider == "" {
		snapshot.Provider = cfg.Provider
	}
	if snapshot.Label == "" {
		snapshot.Label = cfg.Label
	}
	if snapshot.AllowanceSource == "" {
		snapshot.AllowanceSource = firstNonEmpty(snapshot.Source, "api")
	}
	if snapshot.AllowanceSourceLabel == "" {
		snapshot.AllowanceSourceLabel = firstNonEmpty(
			snapshot.SourceLabel, providerAPILabel(cfg.Provider),
		)
	}
	if snapshot.AllowanceStatus == "" && snapshot.Status != "" {
		snapshot.AllowanceStatus = snapshot.Status
		snapshot.AllowanceError = snapshot.Error
	}
	if snapshot.Windows == nil {
		snapshot.Windows = []store.UsageWindow{}
	}
	if snapshot.AllowanceUpdatedAt == nil {
		snapshot.AllowanceUpdatedAt = &now
	}
	if snapshot.AllowanceStatus == "" {
		if len(snapshot.Windows) > 0 {
			snapshot.AllowanceStatus = store.StatusOK
		} else {
			snapshot.AllowanceStatus = store.StatusError
			snapshot.AllowanceError = "collector returned no allowance data"
		}
	}
	// Backward-compat fields mirror allowance during cache round-trips.
	snapshot.Source = snapshot.AllowanceSource
	snapshot.SourceLabel = snapshot.AllowanceSourceLabel
	snapshot.UpdatedAt = snapshot.AllowanceUpdatedAt
	snapshot.Status = snapshot.AllowanceStatus
	snapshot.Error = snapshot.AllowanceError
	snapshot.Stale = snapshot.AllowanceStale
}

func mergeProviderAllowance(
	dst *store.ProviderSnapshot,
	allowance store.ProviderSnapshot,
) {
	dst.AllowanceStatus = allowance.AllowanceStatus
	if dst.AllowanceStatus == "" {
		dst.AllowanceStatus = allowance.Status
	}
	dst.AllowanceSource = firstNonEmpty(allowance.AllowanceSource, allowance.Source, "api")
	dst.AllowanceSourceLabel = firstNonEmpty(
		allowance.AllowanceSourceLabel, allowance.SourceLabel, providerAPILabel(dst.Provider),
	)
	dst.AllowanceUpdatedAt = coalesceTime(allowance.AllowanceUpdatedAt, allowance.UpdatedAt)
	dst.AllowanceStale = allowance.AllowanceStale || allowance.Stale
	dst.AllowanceError = firstNonEmpty(allowance.AllowanceError, allowance.Error)
	dst.Windows = allowance.Windows
	dst.Detail = appendDetail(dst.Detail, allowance.Detail)
	if allowance.Plan != "" {
		dst.Plan = allowance.Plan
	}
}

func applyManualAllowance(
	snapshot *store.ProviderSnapshot,
	cfg store.SourceConfig,
	now time.Time,
) {
	label := cfg.WindowLabel
	if label == "" {
		label = "Configured allowance"
	}
	window := store.UsageWindow{
		ID:              cfg.Provider + "_manual",
		Label:           label,
		Limit:           numberPtr(cfg.Limit),
		Unit:            cfg.Unit,
		DurationMinutes: cfg.WindowMinutes,
	}
	if used, compatible := observedForUnit(snapshot.Observed, cfg.Unit); hasObserved(snapshot.Observed) && compatible {
		remaining := math.Max(0, cfg.Limit-used)
		percent := math.Min(100, used/cfg.Limit*100)
		window.Used = numberPtr(used)
		window.Remaining = numberPtr(remaining)
		window.UsedPercent = numberPtr(percent)
	}
	snapshot.Windows = []store.UsageWindow{window}
	snapshot.AllowanceStatus = store.StatusPartial
	snapshot.AllowanceSource = "manual"
	snapshot.AllowanceSourceLabel = "operator configuration"
	snapshot.AllowanceUpdatedAt = &now
}
