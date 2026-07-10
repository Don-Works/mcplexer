package collectors

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const grokBillingFixture = `{"level":"info","msg":"billing: fetched credits config","ctx":{"subscriptionTier":"SuperGrok Heavy","config":{"currentPeriod":{"type":"weekly","start":"2026-07-07T00:00:00Z","end":"2026-07-14T00:00:00Z"},"creditUsagePercent":42,"onDemandCap":100,"onDemandUsed":0,"prepaidBalance":0,"isUnifiedBillingUser":true}}}`

const grokBillingOmittedPercent = `{"msg":"billing: fetched credits config","ctx":{"subscriptionTier":"SuperGrok","config":{"currentPeriod":{"type":"weekly","start":"2026-07-07T00:00:00Z","end":"2026-07-14T00:00:00Z"}}}}`

const grokBillingExplicitZero = `{"msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":0,"currentPeriod":{"type":"weekly","start":"2026-07-07T00:00:00Z","end":"2026-07-14T00:00:00Z"}}}}`

const grokTextDebugBilling = `2026-07-10 DEBUG gateway received ext_method response {"config":{"currentPeriod":{"type":"SUBSCRIPTION_PERIOD_TYPE_WEEKLY","start":1783382400000,"end":1783987200000},"onDemandCap":{"val":0}},"subscription_tier":"SuperGrok Heavy"}`

func TestParseGrokBillingMapsTierWeeklyWindowAndPercent(t *testing.T) {
	parsed := parseGrokDebugOutput([]byte(grokBillingFixture))
	if parsed.plan != "SuperGrok Heavy" || len(parsed.windows) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
	window := parsed.windows[0]
	requireNumber(t, window.UsedPercent, 42)
	if window.DurationMinutes != grokWeeklyMinutes {
		t.Fatalf("duration = %d", window.DurationMinutes)
	}
	wantReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	if window.ResetsAt == nil || !window.ResetsAt.Equal(wantReset) {
		t.Fatalf("reset = %v", window.ResetsAt)
	}
}

func TestParseGrokBillingOmitsZeroPercent(t *testing.T) {
	parsed := parseGrokDebugOutput([]byte(grokBillingOmittedPercent))
	if len(parsed.windows) != 1 || parsed.windows[0].UsedPercent != nil {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseGrokBillingPreservesExplicitZeroPercent(t *testing.T) {
	parsed := parseGrokDebugOutput([]byte(grokBillingExplicitZero))
	requireNumber(t, parsed.windows[0].UsedPercent, 0)
}

func TestParseGrokTextDebugBillingResponse(t *testing.T) {
	parsed := parseGrokDebugOutput([]byte(grokTextDebugBilling))
	if parsed.plan != "SuperGrok Heavy" || len(parsed.windows) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.windows[0].DurationMinutes != grokWeeklyMinutes || parsed.windows[0].ResetsAt == nil {
		t.Fatalf("window = %+v", parsed.windows[0])
	}
}

func TestGrokCollectorFetchUsesInjectedRunner(t *testing.T) {
	runner := func(_ context.Context, _ string, _ string) ([]byte, error) {
		return []byte(grokBillingFixture), nil
	}
	collector := &GrokCollector{Run: runner}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider: store.ProviderGrok, Label: "Grok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusOK || result.Snapshot.Plan != "SuperGrok Heavy" {
		t.Fatalf("snapshot = %+v", result.Snapshot)
	}
	if result.Snapshot.SourceLabel != "Grok CLI" {
		t.Fatalf("source label = %q", result.Snapshot.SourceLabel)
	}
}

func TestGrokCollectorFetchPartialOnMissingBillingEvent(t *testing.T) {
	runner := func(_ context.Context, _ string, _ string) ([]byte, error) {
		return []byte(`{"msg":"startup"}`), nil
	}
	collector := &GrokCollector{Run: runner}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderGrok})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusPartial {
		t.Fatalf("status = %q", result.Snapshot.Status)
	}
	if !strings.Contains(result.Snapshot.Error, "no billing data") {
		t.Fatalf("error = %q", result.Snapshot.Error)
	}
}

func TestGrokCollectorFetchRedactsSensitiveRunnerErrors(t *testing.T) {
	runner := func(_ context.Context, _ string, _ string) ([]byte, error) {
		return nil, errors.New("failed reading auth.json for user@example.com")
	}
	collector := &GrokCollector{Run: runner}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderGrok})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Snapshot.Error, "auth.json") || strings.Contains(result.Snapshot.Error, "@") {
		t.Fatalf("error leaked account data: %q", result.Snapshot.Error)
	}
}

func TestGrokDebugTempFileMode0600AndRemoved(t *testing.T) {
	path, cleanup, err := grokDebugTempFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file still exists: %v", err)
	}
}
