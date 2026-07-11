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
	})
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
	})
	if !ok || window.Used == nil || *window.Used != 0 || window.UsedPercent != nil {
		t.Fatalf("zero window = %+v, ok=%v", window, ok)
	}
}

func TestEstimatedMiMoCreditsWindowRejectsUnknownOnly(t *testing.T) {
	_, ok := estimatedMiMoCreditsWindow(store.SourceConfig{}, map[string]localStatsResult{
		"mimo": {Stats: []clistats.ModelStats{{Model: "xiaomi/unknown", InputTokens: 100}}},
	})
	if ok {
		t.Fatal("unknown-only stats must not create a credits window")
	}
}
