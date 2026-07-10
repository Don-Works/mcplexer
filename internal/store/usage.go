// usage.go — domain models and UsageStore interface for the AI
// subscription usage dashboard (task 01KX685FTG7CJ7X591KNSYGPSD).
//
// SourceConfig defines one provider's data source. UsageSnapshot is the
// serialised JSON contract returned by /api/v1/usage. Slow external probes
// are cached in memory by the usage service.
package store

import (
	"context"
	"time"
)

// SourceKind values control how the service gathers data for a provider.
const (
	SourceKindAuto   = "auto"   // aggregate from worker_runs
	SourceKindAPI    = "api"    // HTTP API call
	SourceKindCLI    = "cli"    // local first-party CLI statistics
	SourceKindManual = "manual" // operator-supplied allowance
)

// Local auth scope sentinels resolve at runtime from installed CLI auth files.
// They are never persisted and are not valid encrypted auth-scope IDs.
const (
	LocalAuthScopeOpenCode = "local:opencode"
	LocalAuthScopeMiMo     = "local:mimo"
)

// LocalAuthKeys are the only secret_key values accepted with local scopes.
const (
	LocalAuthKeyMiniMax    = "minimax"
	LocalAuthKeyZAI        = "zai-coding-plan"
	LocalAuthKeyOpenRouter = "openrouter"
	LocalAuthKeyMiMoXiaomi = "xiaomi"
)

// IsLocalAuthRef reports whether scopeID and secretKey identify a built-in
// CLI credential lookup. User-supplied paths or keys are never accepted.
func IsLocalAuthRef(scopeID, secretKey string) bool {
	switch scopeID {
	case LocalAuthScopeOpenCode:
		switch secretKey {
		case LocalAuthKeyMiniMax, LocalAuthKeyZAI, LocalAuthKeyOpenRouter:
			return true
		}
	case LocalAuthScopeMiMo:
		return secretKey == LocalAuthKeyMiMoXiaomi
	}
	return false
}

// Provider keys in the JSON contract.
const (
	ProviderClaude     = "claude"
	ProviderCodex      = "codex"
	ProviderMiniMax    = "minimax"
	ProviderZAI        = "zai"
	ProviderGrok       = "grok"
	ProviderMiMo       = "mimo"
	ProviderOpenRouter = "openrouter"
)

// Status values for provider and openrouter status fields.
const (
	StatusOK           = "ok"
	StatusPartial      = "partial"
	StatusUnconfigured = "unconfigured"
	StatusUnavailable  = "unavailable"
	StatusError        = "error"
)

// WindowUnit values.
const (
	UnitPercent  = "percent"
	UnitRequests = "requests"
	UnitCredits  = "credits"
	UnitUSD      = "usd"
	UnitTokens   = "tokens"
)

// AllProviders returns the canonical six-provider list (openrouter is
// separate). The service uses this to ensure every snapshot always
// contains all six entries.
var AllProviders = []string{
	ProviderClaude, ProviderCodex, ProviderMiniMax,
	ProviderZAI, ProviderGrok, ProviderMiMo,
}

// ProviderLabels are the stable display labels used when the operator has
// not supplied a custom label.
var ProviderLabels = map[string]string{
	ProviderClaude: "Claude", ProviderCodex: "Codex",
	ProviderMiniMax: "MiniMax", ProviderZAI: "Z.AI",
	ProviderGrok: "Grok", ProviderMiMo: "MiMo",
}

// SourceConfig configures one provider's usage data source.
type SourceConfig struct {
	Provider      string  `json:"provider"`
	Kind          string  `json:"kind"` // auto|api|cli|manual
	Label         string  `json:"label"`
	Plan          string  `json:"plan,omitempty"`
	Harness       string  `json:"harness,omitempty"`
	AuthScopeID   string  `json:"auth_scope_id,omitempty"`
	SecretKey     string  `json:"secret_key,omitempty"` // secret:// ref name
	BaseURL       string  `json:"base_url,omitempty"`
	Limit         float64 `json:"limit,omitempty"`
	Unit          string  `json:"unit,omitempty"`
	WindowLabel   string  `json:"window_label,omitempty"`
	WindowMinutes int     `json:"window_minutes,omitempty"`
	Enabled       bool    `json:"enabled"`
}

// ObservedCostKind values describe how observed cost_usd should be read.
const (
	ObservedCostEstimate = "estimate" // local CLI list-price estimate
	ObservedCostMetered  = "metered"  // mcplexer worker-run accounting
)

// ProviderSnapshot is one provider's usage data in the JSON contract.
type ProviderSnapshot struct {
	Provider    string        `json:"provider"`
	Label       string        `json:"label"`
	Plan        string        `json:"plan,omitempty"`
	Status      string        `json:"status"` // composite; see allowance/observed status
	Source      string        `json:"source"`
	SourceLabel string        `json:"source_label"`
	Observed    ObservedUsage `json:"observed"`
	Windows     []UsageWindow `json:"windows"`
	UpdatedAt   *time.Time    `json:"updated_at,omitempty"`
	Stale       bool          `json:"stale"`
	Error       string        `json:"error,omitempty"`
	Detail      string        `json:"detail,omitempty"`

	// Allowance lineage — live or configured quota windows.
	AllowanceStatus      string     `json:"allowance_status,omitempty"`
	AllowanceSource      string     `json:"allowance_source,omitempty"`
	AllowanceSourceLabel string     `json:"allowance_source_label,omitempty"`
	AllowanceUpdatedAt   *time.Time `json:"allowance_updated_at,omitempty"`
	AllowanceStale       bool       `json:"allowance_stale,omitempty"`
	AllowanceError       string     `json:"allowance_error,omitempty"`

	// Observed lineage — local accounting over window_days.
	ObservedSource      string     `json:"observed_source,omitempty"`
	ObservedSourceLabel string     `json:"observed_source_label,omitempty"`
	ObservedUpdatedAt   *time.Time `json:"observed_updated_at,omitempty"`
	ObservedCostKind    string     `json:"observed_cost_kind,omitempty"`
}

// ObservedUsage holds aggregated usage metrics.
type ObservedUsage struct {
	Requests              int     `json:"requests"`
	TotalTokens           int     `json:"total_tokens,omitempty"`
	InputTokens           int     `json:"input_tokens"`
	OutputTokens          int     `json:"output_tokens"`
	CacheReadTokens       int     `json:"cache_read_tokens"`
	CacheWriteTokens      int     `json:"cache_write_tokens"`
	CostUSD               float64 `json:"cost_usd"`
	AccountingMissingRuns int     `json:"accounting_missing_runs"`
}

// UsageWindow is one limit/usage window.
type UsageWindow struct {
	ID              string     `json:"id"`
	Label           string     `json:"label"`
	UsedPercent     *float64   `json:"used_percent,omitempty"`
	Used            *float64   `json:"used,omitempty"`
	Limit           *float64   `json:"limit,omitempty"`
	Remaining       *float64   `json:"remaining,omitempty"`
	Unit            string     `json:"unit"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
	DurationMinutes int        `json:"duration_minutes,omitempty"`
}

// ORCreditInfo holds OpenRouter credit balance data.
type ORCreditInfo struct {
	Usage        *float64 `json:"usage,omitempty"`
	Limit        *float64 `json:"limit,omitempty"`
	Remaining    *float64 `json:"remaining,omitempty"`
	UsageDaily   *float64 `json:"usage_daily,omitempty"`
	UsageWeekly  *float64 `json:"usage_weekly,omitempty"`
	UsageMonthly *float64 `json:"usage_monthly,omitempty"`
}

// ORHarnessUsage is one harness's usage in the OpenRouter breakdown.
type ORHarnessUsage struct {
	Harness               string         `json:"harness"`
	Requests              int            `json:"requests"`
	InputTokens           int            `json:"input_tokens"`
	OutputTokens          int            `json:"output_tokens"`
	CacheReadTokens       int            `json:"cache_read_tokens"`
	CacheWriteTokens      int            `json:"cache_write_tokens"`
	CostUSD               float64        `json:"cost_usd"`
	CostKind              string         `json:"cost_kind,omitempty"`
	AccountingMissingRuns int            `json:"accounting_missing_runs"`
	Models                []ORModelUsage `json:"models"`
}

// ORModelUsage is one model's usage within a harness.
type ORModelUsage struct {
	Model        string  `json:"model"`
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// OpenRouterSnapshot holds OpenRouter-specific data.
type OpenRouterSnapshot struct {
	Status    string           `json:"status"`
	Credits   ORCreditInfo     `json:"credits"`
	ByHarness []ORHarnessUsage `json:"by_harness"`
	UpdatedAt *time.Time       `json:"updated_at,omitempty"`
	Stale     bool             `json:"stale"`
	Error     string           `json:"error,omitempty"`
}

// UsageSnapshot is the top-level JSON contract.
type UsageSnapshot struct {
	GeneratedAt time.Time          `json:"generated_at"`
	WindowDays  int                `json:"window_days"`
	Providers   []ProviderSnapshot `json:"providers"`
	OpenRouter  OpenRouterSnapshot `json:"openrouter"`
}

// UsageLedgerRun is the minimal worker_runs projection needed by the usage
// dashboard. Keeping the time-window query in the store avoids loading every
// worker and then silently truncating each worker's run history.
type UsageLedgerRun struct {
	StartedAt          time.Time
	ModelProvider      string
	ModelID            string
	BillingModel       string
	SubscriptionBucket string
	RealCostUSD        float64
	CostUSD            float64
	InputTokens        int
	OutputTokens       int
	Status             string
}

// CollectorResult is what a provider collector returns.
type CollectorResult struct {
	Snapshot ProviderSnapshot
	Duration time.Duration
}

// ORCollectorResult is what the OpenRouter collector returns.
type ORCollectorResult struct {
	Snapshot OpenRouterSnapshot
	Duration time.Duration
}

// UsageStore exposes the local accounting ledger used by the dashboard.
type UsageStore interface {
	// ListUsageLedgerRuns returns every worker run at or after since, ordered
	// newest first. It is the authoritative observed-usage window.
	ListUsageLedgerRuns(ctx context.Context, since time.Time) ([]UsageLedgerRun, error)
}
