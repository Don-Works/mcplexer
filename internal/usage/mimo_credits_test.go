package usage

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

func TestEstimateMiMoCreditsOfficialRates(t *testing.T) {
	tests := []struct {
		model string
		want  float64
	}{
		{"xiaomi/mimo-v2.5-pro", 605000},
		{"mimo/mimo-v2.5", 204000},
	}
	for _, tt := range tests {
		got, ok := estimateMiMoCredits(clistats.ModelStats{
			Model: tt.model, InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000,
		})
		if !ok || got != tt.want {
			t.Fatalf("estimateMiMoCredits(%q) = %v, %v; want %v, true", tt.model, got, ok, tt.want)
		}
	}
}

func TestEstimateMiMoCreditsRejectsUnknownAndNearMatch(t *testing.T) {
	for _, model := range []string{"xiaomi/unknown", "xiaomi/not-mimo-v2.5-pro-preview"} {
		if got, ok := estimateMiMoCredits(clistats.ModelStats{Model: model, InputTokens: 100}); ok || got != 0 {
			t.Fatalf("estimateMiMoCredits(%q) = %v, %v; want 0, false", model, got, ok)
		}
	}
}

func TestEstimatedMiMoCreditsWindowWithLimit(t *testing.T) {
	cfg := store.SourceConfig{
		Limit: 1_000_000, Unit: store.UnitCredits,
		WindowLabel: "MAX monthly credits", WindowMinutes: 30 * 24 * 60,
	}
	window, ok := estimatedMiMoCreditsWindow(cfg, map[string]localStatsResult{
		"mimo": {Stats: []clistats.ModelStats{{Model: "mimo-v2.5-pro", InputTokens: 100}}},
	}, 30*24*60)
	if !ok || window.Used == nil || *window.Used != 30_000 {
		t.Fatalf("window = %+v, ok=%v", window, ok)
	}
	if window.Limit == nil || *window.Limit != 1_000_000 ||
		window.Remaining == nil || *window.Remaining != 970_000 ||
		window.UsedPercent == nil || *window.UsedPercent != 3 {
		t.Fatalf("bounded window = %+v", window)
	}
	if window.Label != "MAX monthly credits (estimate)" || window.DurationMinutes != 43_200 {
		t.Fatalf("window metadata = %+v", window)
	}
}

func TestEstimatedMiMoCreditsWindowShowsMeasuredZero(t *testing.T) {
	window, ok := estimatedMiMoCreditsWindow(store.SourceConfig{}, map[string]localStatsResult{
		"mimo": {Stats: []clistats.ModelStats{{Model: "mimo-v2.5-pro"}}},
	}, 7*24*60)
	if !ok || window.Used == nil || *window.Used != 0 || window.UsedPercent != nil {
		t.Fatalf("zero window = %+v, ok=%v", window, ok)
	}
}

func TestEstimatedMiMoCreditsWindowRejectsUnknownOnly(t *testing.T) {
	_, ok := estimatedMiMoCreditsWindow(store.SourceConfig{}, map[string]localStatsResult{
		"mimo": {Stats: []clistats.ModelStats{{Model: "xiaomi/unknown", InputTokens: 100}}},
	}, 30*24*60)
	if ok {
		t.Fatal("unknown-only stats must not create a credits window")
	}
}

func TestEstimatedMiMoCreditsWindowRejectsMixedUnknownUsage(t *testing.T) {
	_, ok := estimatedMiMoCreditsWindow(store.SourceConfig{}, map[string]localStatsResult{
		"mimo": {Stats: []clistats.ModelStats{
			{Model: "xiaomi/mimo-v2.5-pro", InputTokens: 100},
			{Model: "xiaomi/mimo-v-next", OutputTokens: 1},
		}},
	}, 30*24*60)
	if ok {
		t.Fatal("mixed unknown usage must fail closed instead of undercounting")
	}
}

func TestEstimateMiMoCreditsRejectsForeignNamespace(t *testing.T) {
	if _, ok := estimateMiMoCredits(clistats.ModelStats{
		Model: "other-provider/mimo-v2.5-pro", InputTokens: 100,
	}); ok {
		t.Fatal("foreign provider namespace must not use MiMo rates")
	}
}

func TestEstimatedMiMoCreditsWindowOmitsLimitWhenPeriodsDiffer(t *testing.T) {
	cfg := store.SourceConfig{
		Limit: 82_000_000_000, Unit: store.UnitCredits, WindowMinutes: 30 * 24 * 60,
	}
	window, ok := estimatedMiMoCreditsWindow(cfg, map[string]localStatsResult{
		"mimo": {Stats: []clistats.ModelStats{{Model: "mimo-v2.5-pro", InputTokens: 100}}},
	}, 7*24*60)
	if !ok || window.Used == nil {
		t.Fatalf("window = %+v, ok=%v", window, ok)
	}
	if window.Limit != nil || window.Remaining != nil || window.UsedPercent != nil {
		t.Fatalf("unaligned allowance metadata must be omitted: %+v", window)
	}
	if window.DurationMinutes != 7*24*60 {
		t.Fatalf("duration = %d", window.DurationMinutes)
	}
}

func TestIsMiMoTokenPlanRequiresExplicitSignal(t *testing.T) {
	if isMiMoTokenPlan(&store.ProviderSnapshot{Plan: "Not a Token Plan"}, store.SourceConfig{}) {
		t.Fatal("human-readable substring must not enable Token Plan estimation")
	}
	if !isMiMoTokenPlan(&store.ProviderSnapshot{Plan: "Token Plan"}, store.SourceConfig{}) {
		t.Fatal("collector Token Plan signal should enable estimation")
	}
	if !isMiMoTokenPlan(&store.ProviderSnapshot{Plan: "MAX Token Plan"}, store.SourceConfig{}) {
		t.Fatal("known configured Token Plan tier should enable estimation")
	}
}
