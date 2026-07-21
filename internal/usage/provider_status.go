package usage

import (
	"github.com/don-works/mcplexer/internal/store"
)

func finishProviderStatus(
	snapshot *store.ProviderSnapshot,
	provider string,
	cfg store.SourceConfig,
	ledgerErr error,
) {
	if snapshot.AllowanceStatus == "" {
		snapshot.AllowanceStatus = allowanceStatusWithoutSource(provider, cfg)
		snapshot.Detail = appendDetail(snapshot.Detail, providerLimitation(provider))
	}
	if snapshot.ObservedSource == "" {
		snapshot.ObservedSource = "ledger"
		snapshot.ObservedSourceLabel = "mcplexer worker ledger"
	}
	snapshot.Status = compositeStatus(*snapshot)
	if snapshot.Observed.AccountingMissingRuns > 0 {
		if snapshot.Status == store.StatusOK {
			snapshot.Status = store.StatusPartial
		}
		snapshot.Detail = appendDetail(snapshot.Detail, "some successful runs omitted accounting")
	}
	if ledgerErr != nil && !hasObserved(snapshot.Observed) &&
		cfg.Kind != store.SourceKindAPI && cfg.Kind != store.SourceKindCLI {
		snapshot.Status = store.StatusError
		snapshot.Error = "usage ledger unavailable"
	}
}

func allowanceStatusWithoutSource(provider string, cfg store.SourceConfig) string {
	if cfg.Kind == store.SourceKindAPI || cfg.Kind == store.SourceKindCLI {
		return store.StatusUnconfigured
	}
	switch provider {
	case store.ProviderMiniMax, store.ProviderZAI:
		return store.StatusUnconfigured
	default:
		return store.StatusUnavailable
	}
}

func compositeStatus(snapshot store.ProviderSnapshot) string {
	allowance := snapshot.AllowanceStatus
	observed := observedStatus(snapshot.Observed)
	switch {
	case allowance == store.StatusOK && observed == store.StatusOK:
		return store.StatusOK
	case allowance == store.StatusError && observed == store.StatusOK:
		return store.StatusPartial
	case allowance == store.StatusOK:
		return allowance
	case observed == store.StatusOK || observed == store.StatusPartial:
		return store.StatusPartial
	case allowance != "" && allowance != store.StatusUnavailable && allowance != store.StatusUnconfigured:
		return allowance
	default:
		return observed
	}
}

func observedStatus(observed store.ObservedUsage) string {
	if hasObserved(observed) {
		if observed.AccountingMissingRuns > 0 {
			return store.StatusPartial
		}
		return store.StatusOK
	}
	return store.StatusUnavailable
}

func setBackwardCompatFields(snapshot *store.ProviderSnapshot) {
	if hasObserved(snapshot.Observed) {
		snapshot.Source = snapshot.ObservedSource
		snapshot.SourceLabel = snapshot.ObservedSourceLabel
		snapshot.UpdatedAt = snapshot.ObservedUpdatedAt
	} else if snapshot.AllowanceSource != "" {
		snapshot.Source = snapshot.AllowanceSource
		snapshot.SourceLabel = snapshot.AllowanceSourceLabel
		snapshot.UpdatedAt = snapshot.AllowanceUpdatedAt
	}
	snapshot.Stale = snapshot.AllowanceStale
	if snapshot.Error == "" {
		snapshot.Error = snapshot.AllowanceError
	}
}

func providerLimitation(provider string) string {
	switch provider {
	case store.ProviderClaude:
		return "Claude CLI /usage did not return live allowance data"
	case store.ProviderGrok:
		return "Grok CLI did not return its billing period"
	case store.ProviderMiMo:
		return "no supported live plan allowance API; showing local CLI observations when available"
	case store.ProviderMiniMax, store.ProviderZAI:
		return "configure an auth-scope reference to add live allowance data"
	default:
		return "live allowance unavailable"
	}
}

func providerAPILabel(provider string) string {
	switch provider {
	case store.ProviderClaude:
		return "Claude CLI /usage"
	case store.ProviderCodex:
		return "Codex app-server"
	case store.ProviderGrok:
		return "Grok CLI billing"
	case store.ProviderMiMo:
		return "MiMo CLI auth"
	case store.ProviderMiniMax:
		return "MiniMax Token Plan API"
	case store.ProviderZAI:
		return "Z.AI usage API"
	default:
		return "provider API"
	}
}

func providerError(cfg store.SourceConfig, status, message string) store.ProviderSnapshot {
	source := store.SourceKindAPI
	if cfg.Kind == store.SourceKindCLI {
		source = store.SourceKindCLI
	}
	return store.ProviderSnapshot{
		Provider:             cfg.Provider,
		Label:                cfg.Label,
		Plan:                 cfg.Plan,
		Status:               status,
		AllowanceStatus:      status,
		AllowanceSource:      source,
		AllowanceSourceLabel: providerAPILabel(cfg.Provider),
		Source:               source,
		SourceLabel:          providerAPILabel(cfg.Provider),
		Windows:              []store.UsageWindow{},
		AllowanceError:       message,
		Error:                message,
	}
}
