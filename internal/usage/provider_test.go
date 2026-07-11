package usage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

func TestProviderSnapshotMixedAllowanceAndCLIObserved(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	collector := &fakeProviderCollector{results: []store.ProviderSnapshot{{
		Status: store.StatusOK,
		Source: "api", SourceLabel: "MiniMax Token Plan API",
		Windows: []store.UsageWindow{{
			ID: "mm", Label: "Token grant", UsedPercent: numberPtr(42), Unit: store.UnitPercent,
		}},
	}}}
	service := &Service{
		Store: &fakeUsageStore{runs: []store.UsageLedgerRun{{
			ModelProvider: "minimax_cli", SubscriptionBucket: "minimax",
			InputTokens: 5, OutputTokens: 1, Status: "success",
		}}},
		Collectors: map[string]ProviderCollector{store.ProviderMiniMax: collector},
		LocalStats: map[string]LocalStatsCollector{"opencode": &fakeLocalStats{stats: []clistats.ModelStats{
			{Model: "minimax/MiniMax-M3", Requests: 4, InputTokens: 40, OutputTokens: 8, CostUSD: 0.12},
		}}},
		now: func() time.Time { return now },
	}
	snapshot, err := service.Snapshot(context.Background(), []store.SourceConfig{apiConfig(store.ProviderMiniMax, "scope")}, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	mini := providerByName(t, snapshot, store.ProviderMiniMax)
	if mini.AllowanceStatus != store.StatusOK || mini.AllowanceSource != "api" {
		t.Fatalf("allowance = status:%s source:%s", mini.AllowanceStatus, mini.AllowanceSource)
	}
	if mini.ObservedSource != "cli" || mini.ObservedSourceLabel != "OpenCode CLI stats" {
		t.Fatalf("observed lineage = %s / %s", mini.ObservedSource, mini.ObservedSourceLabel)
	}
	if mini.Observed.Requests != 4 || mini.Observed.InputTokens != 40 {
		t.Fatalf("observed totals = %+v", mini.Observed)
	}
	if mini.ObservedCostKind != store.ObservedCostEstimate {
		t.Fatalf("observed_cost_kind = %q", mini.ObservedCostKind)
	}
	if mini.Source != "cli" || mini.SourceLabel != "OpenCode CLI stats" {
		t.Fatalf("backward compat source = %s / %s", mini.Source, mini.SourceLabel)
	}
}

func TestGrokLocalLogsSupersedeUnmeasuredLedgerRuns(t *testing.T) {
	service := &Service{
		Store: &fakeUsageStore{runs: []store.UsageLedgerRun{{
			ModelProvider: "grok_cli", SubscriptionBucket: "grok", Status: "success",
		}}},
		LocalStats: map[string]LocalStatsCollector{"grok": &fakeLocalStats{stats: []clistats.ModelStats{{
			Model: "grok/grok-code-fast-1", Requests: 3, InputTokens: 40,
			OutputTokens: 12, CacheReadTokens: 80,
		}}}},
	}
	snapshot, err := service.Snapshot(context.Background(), nil, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	grok := providerByName(t, snapshot, store.ProviderGrok)
	if grok.ObservedSource != "cli" || grok.ObservedSourceLabel != "Grok CLI logs" ||
		grok.Observed.Requests != 3 || grok.Observed.AccountingMissingRuns != 0 {
		t.Fatalf("grok observed = %+v source=%s/%s", grok.Observed, grok.ObservedSource, grok.ObservedSourceLabel)
	}
}

func TestProviderSnapshotObservedWithoutAllowance(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service := &Service{
		Store: &fakeUsageStore{runs: []store.UsageLedgerRun{{
			ModelProvider: "claude_cli", SubscriptionBucket: "claude",
			InputTokens: 12, OutputTokens: 3, CostUSD: 0.05, Status: "success",
		}}},
		now: func() time.Time { return now },
	}
	snapshot, err := service.Snapshot(context.Background(), nil, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	claude := providerByName(t, snapshot, store.ProviderClaude)
	if claude.AllowanceStatus != store.StatusUnavailable {
		t.Fatalf("allowance_status = %s", claude.AllowanceStatus)
	}
	if claude.ObservedSource != "ledger" || claude.Observed.InputTokens != 12 {
		t.Fatalf("observed = source:%s totals:%+v", claude.ObservedSource, claude.Observed)
	}
	if claude.ObservedCostKind != store.ObservedCostMetered {
		t.Fatalf("observed_cost_kind = %q", claude.ObservedCostKind)
	}
	if claude.Status != store.StatusPartial {
		t.Fatalf("composite status = %s", claude.Status)
	}
}

func TestProviderSnapshotMarksMissingAccounting(t *testing.T) {
	service := &Service{Store: &fakeUsageStore{runs: []store.UsageLedgerRun{{
		ModelProvider: "codex_cli", SubscriptionBucket: "codex",
		InputTokens: 0, OutputTokens: 0, Status: "success",
	}}}}
	snapshot, err := service.Snapshot(context.Background(), nil, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	codex := providerByName(t, snapshot, store.ProviderCodex)
	if codex.Observed.AccountingMissingRuns != 1 {
		t.Fatalf("missing runs = %d", codex.Observed.AccountingMissingRuns)
	}
	if codex.Status != store.StatusPartial {
		t.Fatalf("status = %s, want partial", codex.Status)
	}
	if !strings.Contains(codex.Detail, "omitted accounting") {
		t.Fatalf("detail = %q", codex.Detail)
	}
}

func TestManualAllowanceDoesNotFabricateUsage(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service := &Service{Store: &fakeUsageStore{}, now: func() time.Time { return now }}
	config := store.SourceConfig{
		Provider: store.ProviderGrok, Kind: store.SourceKindManual,
		Label: "Grok", Limit: 100, Unit: store.UnitPercent, Enabled: true,
	}
	snapshot, err := service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	grok := providerByName(t, snapshot, store.ProviderGrok)
	if len(grok.Windows) != 1 {
		t.Fatalf("windows = %+v", grok.Windows)
	}
	window := grok.Windows[0]
	if window.UsedPercent != nil || window.Used != nil || window.Remaining != nil {
		t.Fatalf("fabricated usage = %+v", window)
	}
	if window.Limit == nil || *window.Limit != 100 {
		t.Fatalf("limit = %+v", window.Limit)
	}
}

func TestManualCreditAllowanceDoesNotTreatTokensAsCredits(t *testing.T) {
	service := &Service{Store: &fakeUsageStore{runs: []store.UsageLedgerRun{{
		SubscriptionBucket: store.ProviderGrok,
		InputTokens:        400, OutputTokens: 100, Status: "success",
	}}}}
	config := store.SourceConfig{
		Provider: store.ProviderGrok, Kind: store.SourceKindManual,
		Limit: 1_000, Unit: store.UnitCredits, Enabled: true,
	}
	snapshot, err := service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	window := providerByName(t, snapshot, store.ProviderGrok).Windows[0]
	if window.Used != nil || window.Remaining != nil || window.UsedPercent != nil {
		t.Fatalf("tokens were fabricated as credits: %+v", window)
	}
}
