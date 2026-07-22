// handler_usage_test.go — coverage for mcpx__usage_summary: the compact
// per-provider projection (missing data reported as missing, never zero), the
// cache-only read path (no provider refresh), the absence of secret material,
// and the fact that it is NOT admin-CWD-gated (contrast: the dashboard tools).
package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage"
)

func usageF64Ptr(v float64) *float64 { return &v }

// --- fakes for the cache-only dispatch test ------------------------------

type fakeUsageCacheDB struct {
	mu    sync.Mutex
	snaps map[string]store.UsageSnapshot
}

func (f *fakeUsageCacheDB) ListUsageLedgerRuns(
	_ context.Context, _ time.Time,
) ([]store.UsageLedgerRun, error) {
	return nil, nil
}

func (f *fakeUsageCacheDB) GetUsageSnapshot(
	_ context.Context, key string,
) (store.UsageSnapshot, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.snaps[key]
	return s, ok, nil
}

func (f *fakeUsageCacheDB) PutUsageSnapshot(
	_ context.Context, key string, s store.UsageSnapshot,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snaps == nil {
		f.snaps = make(map[string]store.UsageSnapshot)
	}
	f.snaps[key] = s
	return nil
}

type countingUsageCollector struct{ calls atomic.Int32 }

func (c *countingUsageCollector) Fetch(
	_ context.Context, _ store.SourceConfig,
) (store.CollectorResult, error) {
	c.calls.Add(1)
	return store.CollectorResult{Snapshot: store.ProviderSnapshot{
		Status: store.StatusOK,
		Windows: []store.UsageWindow{{
			ID: "w", Label: "5 hour", Unit: store.UnitPercent,
			UsedPercent: usageF64Ptr(42),
		}},
	}}, nil
}

func decodeUsageSummary(t *testing.T, raw json.RawMessage) usageSummaryResult {
	t.Helper()
	var env CallToolResult
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.IsError {
		t.Fatalf("tool returned error: %s", envText(env))
	}
	var res usageSummaryResult
	if err := json.Unmarshal([]byte(envText(env)), &res); err != nil {
		t.Fatalf("unmarshal summary: %v (text=%s)", err, envText(env))
	}
	return res
}

func envText(env CallToolResult) string {
	if len(env.Content) == 0 {
		return ""
	}
	return env.Content[0].Text
}

func findUsageProvider(res usageSummaryResult, provider string) *usageProviderSummary {
	for i := range res.Providers {
		if res.Providers[i].Provider == provider {
			return &res.Providers[i]
		}
	}
	return nil
}

// --- projection: missing data is reported as missing, not zero -----------

func TestBuildUsageSummaryReportsMissingNotZero(t *testing.T) {
	updated := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	snap := store.UsageSnapshot{
		GeneratedAt: time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC),
		WindowDays:  30,
		Providers: []store.ProviderSnapshot{
			{
				Provider: store.ProviderClaude, Label: "Claude", Status: store.StatusOK,
				Windows: []store.UsageWindow{{
					Label: "weekly", UsedPercent: usageF64Ptr(80),
					Limit: usageF64Ptr(100), Remaining: usageF64Ptr(20),
					Unit: store.UnitPercent,
				}},
				Observed: store.ObservedUsage{
					Requests: 12, InputTokens: 1000, OutputTokens: 500, CostUSD: 3.5,
				},
				ObservedUpdatedAt: &updated, ObservedCostKind: store.ObservedCostMetered,
			},
			{
				Provider: store.ProviderCodex, Label: "Codex",
				Status: store.StatusUnconfigured,
			},
		},
		OpenRouter: store.OpenRouterSnapshot{Status: store.StatusUnconfigured},
	}

	res := buildUsageSummary(snap, true)
	if !res.Available || res.WindowDays != 30 {
		t.Fatalf("summary meta = %+v", res)
	}

	codex := findUsageProvider(res, store.ProviderCodex)
	if codex == nil || !codex.AllowanceMissing || !codex.ObservedMissing || codex.Observed != nil {
		t.Fatalf("codex should report missing (never zero): %+v", codex)
	}

	claude := findUsageProvider(res, store.ProviderClaude)
	if claude == nil || claude.AllowanceMissing || claude.ObservedMissing {
		t.Fatalf("claude should carry real data: %+v", claude)
	}
	if claude.Observed == nil || claude.Observed.Requests != 12 ||
		claude.Observed.TotalTokens != 1500 || claude.Observed.CostKind != store.ObservedCostMetered {
		t.Fatalf("claude observed = %+v", claude.Observed)
	}
	if len(claude.Windows) != 1 || claude.Windows[0].UsedPercent == nil ||
		*claude.Windows[0].UsedPercent != 80 || claude.Windows[0].Remaining == nil {
		t.Fatalf("claude window = %+v", claude.Windows)
	}

	or := findUsageProvider(res, store.ProviderOpenRouter)
	if or == nil || !or.AllowanceMissing || !or.ObservedMissing {
		t.Fatalf("openrouter should report missing: %+v", or)
	}
}

func TestBuildUsageSummaryNotFoundIsUnknownNotZero(t *testing.T) {
	res := buildUsageSummary(store.UsageSnapshot{}, false)
	if res.Available {
		t.Fatal("expected available=false on a cold cache")
	}
	if len(res.Providers) != 0 {
		t.Fatalf("expected no providers, got %d", len(res.Providers))
	}
	if !strings.Contains(res.Reason, "zero") {
		t.Fatalf("reason must warn that absent != zero: %q", res.Reason)
	}
}

// --- no secret material in the output ------------------------------------

func TestUsageSummaryCarriesNoSecretMaterial(t *testing.T) {
	updated := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	snap := store.UsageSnapshot{
		GeneratedAt: updated, WindowDays: 30,
		Providers: []store.ProviderSnapshot{{
			Provider: store.ProviderMiniMax, Label: "MiniMax", Status: store.StatusOK,
			Windows:           []store.UsageWindow{{Label: "monthly", Unit: store.UnitUSD, Limit: usageF64Ptr(50)}},
			Observed:          store.ObservedUsage{Requests: 3, CostUSD: 1.2},
			ObservedUpdatedAt: &updated,
		}},
		OpenRouter: store.OpenRouterSnapshot{
			Status:  store.StatusOK,
			Credits: store.ORCreditInfo{Limit: usageF64Ptr(20), Usage: usageF64Ptr(5), Remaining: usageF64Ptr(15)},
		},
	}
	out, err := json.Marshal(buildUsageSummary(snap, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"auth_scope", "secret", "api_key", "secret://", "Authorization"} {
		if strings.Contains(string(out), marker) {
			t.Fatalf("usage summary leaked secret marker %q: %s", marker, out)
		}
	}
}

// --- dispatch: cache-only, no provider refresh triggered by the read -----

func TestHandleUsageSummaryCacheOnlyNoRefresh(t *testing.T) {
	db := &fakeUsageCacheDB{}
	collector := &countingUsageCollector{}
	svc := &usage.Service{
		Store:      db,
		Collectors: map[string]usage.ProviderCollector{store.ProviderMiniMax: collector},
	}
	ctx := context.Background()

	// Prime the cache the way the dashboard/admin path would (force refresh).
	if _, err := svc.Snapshot(ctx, nil, 30, true); err != nil {
		t.Fatalf("prime snapshot: %v", err)
	}
	primeCalls := collector.calls.Load()
	if primeCalls == 0 {
		t.Fatal("prime should have probed the collector at least once")
	}

	h := &handler{usageSvc: svc}
	raw, rpc := h.handleUsageSummary(ctx, json.RawMessage(`{}`))
	if rpc != nil {
		t.Fatalf("rpc error: %+v", rpc)
	}
	if got := collector.calls.Load(); got != primeCalls {
		t.Fatalf("read path triggered a provider refresh: collector calls %d -> %d", primeCalls, got)
	}

	res := decodeUsageSummary(t, raw)
	if !res.Available {
		t.Fatalf("expected available snapshot: %s", raw)
	}
	mini := findUsageProvider(res, store.ProviderMiniMax)
	if mini == nil || mini.AllowanceMissing || len(mini.Windows) == 0 {
		t.Fatalf("minimax allowance should be present: %+v", mini)
	}
	if mini.Windows[0].UsedPercent == nil || *mini.Windows[0].UsedPercent != 42 {
		t.Fatalf("minimax window = %+v", mini.Windows[0])
	}
}

func TestHandleUsageSummaryColdCacheReportsUnavailable(t *testing.T) {
	svc := &usage.Service{Store: &fakeUsageCacheDB{}}
	h := &handler{usageSvc: svc}
	raw, rpc := h.handleUsageSummary(context.Background(), json.RawMessage(`{"days":7}`))
	if rpc != nil {
		t.Fatalf("rpc error: %+v", rpc)
	}
	res := decodeUsageSummary(t, raw)
	if res.Available {
		t.Fatal("expected available=false for an unprimed cache")
	}
}

func TestHandleUsageSummaryRejectsOutOfRangeDays(t *testing.T) {
	h := &handler{usageSvc: &usage.Service{Store: &fakeUsageCacheDB{}}}
	raw, rpc := h.handleUsageSummary(context.Background(), json.RawMessage(`{"days":9000}`))
	if rpc != nil {
		t.Fatalf("rpc error: %+v", rpc)
	}
	var env CallToolResult
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.IsError || !strings.Contains(envText(env), "365") {
		t.Fatalf("expected a days-range error, got %s", raw)
	}
}

// --- surface: registered when enabled, and NOT admin-CWD-gated ------------

func TestUsageSummaryRegisteredOnlyWhenEnabled(t *testing.T) {
	with := (&handler{usageSvc: &usage.Service{}}).codeModeBuiltinTools()
	if !toolListHasName(with, "mcpx__usage_summary") {
		t.Fatal("mcpx__usage_summary missing when usageSvc is set")
	}
	without := (&handler{}).codeModeBuiltinTools()
	if toolListHasName(without, "mcpx__usage_summary") {
		t.Fatal("mcpx__usage_summary present when usageSvc is nil")
	}
}

// TestUsageSummaryNotAdminGated is the core access contract: from a normal
// project directory (non-admin CWD) the usage tool survives the admin filter
// while an admin tool (mcpx__reload_server) is dropped. Contrast with
// get_usage_dashboard, which requires the admin CWD context.
func TestUsageSummaryNotAdminGated(t *testing.T) {
	if IsAdminTool("mcpx__usage_summary") {
		t.Fatal("mcpx__usage_summary must not be classified as an admin tool")
	}
	gate := NewAdminCWDGate(filepath.Join(t.TempDir(), "data"))
	projectCWD := t.TempDir() // outside the data dir and the source tree
	tools := []Tool{usageSummaryToolDefinition(), reloadServerToolDefinition()}

	filtered := gate.FilterAdminTools(tools, projectCWD, nil)
	if !toolListHasName(filtered, "mcpx__usage_summary") {
		t.Fatal("usage summary was dropped from a non-admin CWD — it must be reachable there")
	}
	if toolListHasName(filtered, "mcpx__reload_server") {
		t.Fatal("admin tool survived the non-admin CWD filter — gate is not engaged")
	}
}
