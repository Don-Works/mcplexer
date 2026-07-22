// handler_usage.go — dispatch + projection for mcpx__usage_summary. A
// read-only, cache-only view of AI-subscription allowance vs observed usage
// that a delegating model can call WITHOUT the admin CWD-gate. It reads the
// last snapshot the usage service persisted and never triggers a provider
// refresh. The projection is deliberately narrow: only allowance/spend numbers
// and provider labels are exposed — no secret material — and missing provider
// data is flagged explicitly so a model never reads absent data as zero.
package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

// usageSummaryResult is the compact, secret-free payload of mcpx__usage_summary.
type usageSummaryResult struct {
	Available   bool                   `json:"available"`
	Reason      string                 `json:"reason,omitempty"`
	GeneratedAt *time.Time             `json:"generated_at,omitempty"`
	WindowDays  int                    `json:"window_days,omitempty"`
	CacheError  string                 `json:"cache_error,omitempty"`
	Note        string                 `json:"note,omitempty"`
	Providers   []usageProviderSummary `json:"providers"`
}

// usageProviderSummary is one provider's allowance/observed digest. The two
// *_missing flags carry the "reported explicitly, not zeroed" contract: true
// means the number is UNKNOWN, never that the remaining allowance is zero.
type usageProviderSummary struct {
	Provider         string                `json:"provider"`
	Label            string                `json:"label"`
	Plan             string                `json:"plan,omitempty"`
	Status           string                `json:"status"`
	AllowanceMissing bool                  `json:"allowance_missing"`
	ObservedMissing  bool                  `json:"observed_missing"`
	Windows          []usageWindowSummary  `json:"windows,omitempty"`
	Observed         *usageObservedSummary `json:"observed,omitempty"`
	Stale            bool                  `json:"stale,omitempty"`
	Detail           string                `json:"detail,omitempty"`
	Error            string                `json:"error,omitempty"`
	UpdatedAt        *time.Time            `json:"updated_at,omitempty"`
}

type usageWindowSummary struct {
	Label       string     `json:"label,omitempty"`
	UsedPercent *float64   `json:"used_percent,omitempty"`
	Used        *float64   `json:"used,omitempty"`
	Limit       *float64   `json:"limit,omitempty"`
	Remaining   *float64   `json:"remaining,omitempty"`
	Unit        string     `json:"unit,omitempty"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

type usageObservedSummary struct {
	Requests    int     `json:"requests"`
	TotalTokens int     `json:"total_tokens,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
	CostKind    string  `json:"cost_kind,omitempty"`
}

// handleUsageSummary dispatches mcpx__usage_summary. Cache-only: no provider
// probe, no refresh, no cache write — deterministic and free to poll.
func (h *handler) handleUsageSummary(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.usageSvc == nil {
		return marshalErrorResult("usage summary not enabled on this daemon"), nil
	}
	var in struct {
		Days int `json:"days"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return marshalErrorResult("invalid params: " + err.Error()), nil
		}
	}
	if in.Days < 0 || in.Days > 365 {
		return marshalErrorResult("days must be between 1 and 365"), nil
	}
	snapshot, found, err := h.usageSvc.CachedSnapshot(ctx, h.usageSourceConfigs(ctx), in.Days)
	if err != nil {
		return marshalErrorResult("usage snapshot: " + err.Error()), nil
	}
	data, mErr := json.Marshal(buildUsageSummary(snapshot, found))
	if mErr != nil {
		return marshalErrorResult(mErr.Error()), nil
	}
	return marshalToolResult(string(data)), nil
}

// usageSourceConfigs mirrors the admin/API path so the cache key computed here
// matches the one the dashboard warmed. Nil settings (e.g. an unconfigured
// worker gateway) yields nil, and the service falls back to provider defaults.
func (h *handler) usageSourceConfigs(ctx context.Context) []store.SourceConfig {
	if h.settingsSvc == nil {
		return nil
	}
	return config.UsageSourceConfigs(h.settingsSvc.Load(ctx))
}

func buildUsageSummary(snap store.UsageSnapshot, found bool) usageSummaryResult {
	if !found {
		return usageSummaryResult{
			Available: false,
			Reason: "no cached usage snapshot yet — open the mcplexer dashboard to populate it. " +
				"Absent data is UNKNOWN, not zero remaining.",
			Providers: []usageProviderSummary{},
		}
	}
	res := usageSummaryResult{
		Available:   true,
		GeneratedAt: &snap.GeneratedAt,
		WindowDays:  snap.WindowDays,
		CacheError:  snap.CacheError,
		Note: "cache-only read; no provider refresh triggered. " +
			"allowance_missing/observed_missing = UNKNOWN, never zero.",
		Providers: make([]usageProviderSummary, 0, len(snap.Providers)+1),
	}
	for _, p := range snap.Providers {
		res.Providers = append(res.Providers, summarizeProvider(p))
	}
	res.Providers = append(res.Providers, summarizeOpenRouter(snap.OpenRouter))
	return res
}

func summarizeProvider(p store.ProviderSnapshot) usageProviderSummary {
	out := usageProviderSummary{
		Provider:  p.Provider,
		Label:     p.Label,
		Plan:      p.Plan,
		Status:    p.Status,
		Stale:     p.Stale,
		Detail:    p.Detail,
		Error:     p.Error,
		UpdatedAt: p.UpdatedAt,
	}
	for _, w := range p.Windows {
		out.Windows = append(out.Windows, summarizeWindow(w))
	}
	out.AllowanceMissing = !windowsHaveAllowance(p.Windows)
	out.ObservedMissing = p.ObservedUpdatedAt == nil && !observedPresent(p.Observed)
	if !out.ObservedMissing {
		out.Observed = &usageObservedSummary{
			Requests:    p.Observed.Requests,
			TotalTokens: observedTotalTokens(p.Observed),
			CostUSD:     p.Observed.CostUSD,
			CostKind:    p.ObservedCostKind,
		}
	}
	return out
}

func summarizeWindow(w store.UsageWindow) usageWindowSummary {
	return usageWindowSummary{
		Label:       w.Label,
		UsedPercent: w.UsedPercent,
		Used:        w.Used,
		Limit:       w.Limit,
		Remaining:   w.Remaining,
		Unit:        w.Unit,
		ResetsAt:    w.ResetsAt,
	}
}

// summarizeOpenRouter folds the separate OpenRouter section into one uniform
// provider entry (a single "credits" window) so a model can iterate providers
// without special-casing it.
func summarizeOpenRouter(or store.OpenRouterSnapshot) usageProviderSummary {
	out := usageProviderSummary{
		Provider:  store.ProviderOpenRouter,
		Label:     "OpenRouter",
		Status:    or.Status,
		Stale:     or.Stale,
		Error:     or.Error,
		UpdatedAt: or.UpdatedAt,
	}
	c := or.Credits
	hasAllowance := c.Limit != nil || c.Remaining != nil
	out.AllowanceMissing = !hasAllowance
	if hasAllowance || c.Usage != nil {
		w := usageWindowSummary{
			Label: "credits", Used: c.Usage, Limit: c.Limit,
			Remaining: c.Remaining, Unit: store.UnitUSD,
		}
		if c.Limit != nil && *c.Limit > 0 && c.Usage != nil {
			pct := (*c.Usage / *c.Limit) * 100
			w.UsedPercent = &pct
		}
		out.Windows = append(out.Windows, w)
	}
	out.Observed = aggregateORObserved(or.ByHarness)
	out.ObservedMissing = out.Observed == nil
	return out
}

func aggregateORObserved(harnesses []store.ORHarnessUsage) *usageObservedSummary {
	var agg usageObservedSummary
	for _, hu := range harnesses {
		agg.Requests += hu.Requests
		agg.TotalTokens += hu.InputTokens + hu.OutputTokens
		agg.CostUSD += hu.CostUSD
	}
	if agg.Requests == 0 && agg.TotalTokens == 0 && agg.CostUSD == 0 {
		return nil
	}
	return &agg
}

// windowsHaveAllowance reports whether any window carries a known ceiling: a
// limit, a used-percent, or a remaining. None of these present means the
// allowance is UNKNOWN, not zero.
func windowsHaveAllowance(ws []store.UsageWindow) bool {
	for _, w := range ws {
		if w.Limit != nil || w.UsedPercent != nil || w.Remaining != nil {
			return true
		}
	}
	return false
}

func observedPresent(o store.ObservedUsage) bool {
	return o.Requests > 0 || o.TotalTokens > 0 || o.InputTokens > 0 ||
		o.OutputTokens > 0 || o.CostUSD > 0
}

func observedTotalTokens(o store.ObservedUsage) int {
	if o.TotalTokens > 0 {
		return o.TotalTokens
	}
	return o.InputTokens + o.OutputTokens
}
