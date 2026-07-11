package collectors

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMiMoCollectorAuthenticatedWithoutFakeAllowance(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi","email":"user@example.com","user_id":"u-123"}`), nil
		},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider: store.ProviderMiMo, Kind: store.SourceKindCLI, Label: "MiMo",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != store.StatusOK || snapshot.AllowanceSource != "auth" || len(snapshot.Windows) != 0 {
		t.Fatalf("snapshot = status:%s windows:%d", snapshot.Status, len(snapshot.Windows))
	}
	if snapshot.Detail != "Local MiMoCode session usage collected; subscription balance is not exposed by the CLI/API" {
		t.Fatalf("detail = %q", snapshot.Detail)
	}
	if strings.Contains(snapshot.Detail, "user@example.com") || strings.Contains(snapshot.Detail, "u-123") {
		t.Fatalf("detail leaked user identity: %q", snapshot.Detail)
	}
}

func TestMiMoCollectorReportsProbeFailure(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return nil, context.DeadlineExceeded
		},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderMiMo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusPartial || result.Snapshot.Error == "" {
		t.Fatalf("snapshot = %+v", result.Snapshot)
	}
}

func TestParseMiMoWhoamiRejectsNonProviderOutput(t *testing.T) {
	parsed := parseMiMoWhoami([]byte(`{"email":"user@example.com","id":"abc"}`))
	if len(parsed.errors) == 0 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseMiMoWhoamiAcceptsHumanOutputWithoutIdentity(t *testing.T) {
	parsed := parseMiMoWhoami([]byte("\x1b[0m\nCurrent user\nProvider: MiMo\nUser ID: 1234567890\n"))
	if parsed.provider != "mimo" || len(parsed.errors) != 0 {
		t.Fatalf("parsed = %+v", parsed)
	}
	result := mimoAuthResult(store.SourceConfig{}, parsed, false, time.Now())
	if strings.Contains(result.Snapshot.Detail, "1234567890") {
		t.Fatalf("detail leaked identity: %q", result.Snapshot.Detail)
	}
}

func TestMiMoCollectorSetsUpdatedAtOnSuccess(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderMiMo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.UpdatedAt == nil {
		t.Fatal("expected updated_at")
	}
	if result.Duration < 0 || start.After(time.Now()) {
		t.Fatalf("duration = %s", result.Duration)
	}
}

func TestMiMoEstimatedCreditsV25Pro(t *testing.T) {
	observed := store.ObservedUsage{
		InputTokens:     1000,
		OutputTokens:    500,
		CacheReadTokens: 2000,
	}
	got := MiMoEstimatedCredits("xiaomi/mimo-v2.5-pro", observed)
	// 1000*300 + 500*600 + 2000*2.5 = 300000 + 300000 + 5000 = 605000
	want := 605000.0
	if got != want {
		t.Fatalf("v2.5-pro credits = %f, want %f", got, want)
	}
}

func TestMiMoEstimatedCreditsV25(t *testing.T) {
	observed := store.ObservedUsage{
		InputTokens:     1000,
		OutputTokens:    500,
		CacheReadTokens: 2000,
	}
	got := MiMoEstimatedCredits("mimo/mimo-v2.5", observed)
	// 1000*100 + 500*200 + 2000*2 = 100000 + 100000 + 4000 = 204000
	want := 204000.0
	if got != want {
		t.Fatalf("v2.5 credits = %f, want %f", got, want)
	}
}

func TestMiMoEstimatedCreditsUnknownModelIgnored(t *testing.T) {
	observed := store.ObservedUsage{InputTokens: 1000}
	got := MiMoEstimatedCredits("xiaomi/unknown-model", observed)
	if got != 0 {
		t.Fatalf("unknown model credits = %f, want 0", got)
	}
}

func TestMiMoTokenPlanWindowWithConfiguredLimit(t *testing.T) {
	cfg := store.SourceConfig{
		Provider: store.ProviderMiMo,
		Limit:    1000000,
		Unit:     store.UnitCredits,
	}
	observed := store.ObservedUsage{InputTokens: 100, OutputTokens: 50}
	window, ok := MiMoTokenPlanWindow(cfg, observed, 50000)
	if !ok {
		t.Fatal("expected window")
	}
	if window.Label != "Token Plan credits (estimate)" {
		t.Fatalf("label = %q", window.Label)
	}
	if window.Unit != store.UnitCredits {
		t.Fatalf("unit = %q", window.Unit)
	}
	if window.Used == nil || *window.Used != 50000 {
		t.Fatalf("used = %v", window.Used)
	}
	if window.Limit == nil || *window.Limit != 1000000 {
		t.Fatalf("limit = %v", window.Limit)
	}
	if window.Remaining == nil || *window.Remaining != 950000 {
		t.Fatalf("remaining = %v", window.Remaining)
	}
	if window.UsedPercent == nil || *window.UsedPercent != 5.0 {
		t.Fatalf("used_percent = %v", window.UsedPercent)
	}
}

func TestMiMoTokenPlanWindowWithoutLimit(t *testing.T) {
	cfg := store.SourceConfig{Provider: store.ProviderMiMo}
	observed := store.ObservedUsage{InputTokens: 100}
	window, ok := MiMoTokenPlanWindow(cfg, observed, 30000)
	if !ok {
		t.Fatal("expected window")
	}
	if window.Used == nil || *window.Used != 30000 {
		t.Fatalf("used = %v", window.Used)
	}
	if window.Limit != nil {
		t.Fatalf("limit should be nil without configured limit, got %v", window.Limit)
	}
	if window.Remaining != nil {
		t.Fatalf("remaining should be nil without configured limit, got %v", window.Remaining)
	}
	if window.UsedPercent != nil {
		t.Fatalf("used_percent should be nil without configured limit, got %v", window.UsedPercent)
	}
}

func TestMiMoTokenPlanWindowZeroCreditsReturnsFalse(t *testing.T) {
	cfg := store.SourceConfig{Provider: store.ProviderMiMo}
	_, ok := MiMoTokenPlanWindow(cfg, store.ObservedUsage{}, 0)
	if ok {
		t.Fatal("expected false for zero credits")
	}
}

func TestMiMoCollectorTokenPlanSetsPlanFromSecret(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("tp-abc123secret")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Plan != "Token Plan" {
		t.Fatalf("plan = %q, want Token Plan", result.Snapshot.Plan)
	}
}

func TestMiMoCollectorNonTokenPlanDefaultsToMiMoCode(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("sk-regular-key")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Plan != "MiMoCode" {
		t.Fatalf("plan = %q, want MiMoCode", result.Snapshot.Plan)
	}
}

func TestMiMoCollectorConfiguredPlanOverridesDetection(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("tp-abc123")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		Plan:        "Custom Plan",
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Plan != "Custom Plan" {
		t.Fatalf("plan = %q, want Custom Plan", result.Snapshot.Plan)
	}
}

func TestMiMoCollectorSecretNotLeaked(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("tp-supersecretkey123")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if strings.Contains(snapshot.Detail, "tp-supersecretkey123") {
		t.Fatalf("detail leaked secret: %q", snapshot.Detail)
	}
	if strings.Contains(snapshot.Error, "tp-supersecretkey123") {
		t.Fatalf("error leaked secret: %q", snapshot.Error)
	}
}

func TestMiMoAggregateEstimatedCreditsSkipsUnknownModels(t *testing.T) {
	stats := []ObservedModelUsage{
		{Model: "xiaomi/mimo-v2.5-pro", Observed: store.ObservedUsage{InputTokens: 100}},
		{Model: "xiaomi/unknown-model", Observed: store.ObservedUsage{InputTokens: 200}},
		{Model: "mimo/mimo-v2.5", Observed: store.ObservedUsage{InputTokens: 300}},
	}
	got := MiMoAggregateEstimatedCredits(stats)
	// v2.5-pro: 100*300 = 30000; v2.5: 300*100 = 30000; unknown: 0
	want := 60000.0
	if got != want {
		t.Fatalf("aggregate credits = %f, want %f", got, want)
	}
}

func TestMiMoIsTokenPlanCredential(t *testing.T) {
	if !MiMoIsTokenPlanCredential("tp-abc123") {
		t.Fatal("expected tp- prefix to be detected")
	}
	if MiMoIsTokenPlanCredential("sk-regular") {
		t.Fatal("expected non-tp- prefix to be rejected")
	}
	if MiMoIsTokenPlanCredential("") {
		t.Fatal("expected empty string to be rejected")
	}
}

type fakeSecretReader struct {
	value []byte
	err   error
}

func (f *fakeSecretReader) Get(_ context.Context, _, _ string) ([]byte, error) {
	return f.value, f.err
}
