// usage.go — domain models and UsageStore interface for the AI
// subscription usage dashboard (task 01KX685FTG7CJ7X591KNSYGPSD).
//
// SourceConfig defines one provider's data source. UsageSnapshot is the
// serialised JSON contract returned by /api/v1/usage. The store persists
// per-provider snapshots so the service can cache slow-probe results.
package store

import (
	"context"
	"time"
)

// SourceKind values control how the service gathers data for a provider.
const (
	SourceKindAuto   = "auto"   // aggregate from worker_runs
	SourceKindAPI    = "api"    // HTTP API call
	SourceKindManual = "manual" // CLI stats or manual config
)

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

// SourceConfig configures one provider's usage data source.
type SourceConfig struct {
	Provider      string  `json:"provider"`
	Kind          string  `json:"kind"` // auto|api|manual
	Label         string  `json:"label"`
	Plan          string  `json:"plan,omitempty"`
	AuthScopeID   string  `json:"auth_scope_id,omitempty"`
	SecretKey     string  `json:"secret_key,omitempty"` // secret:// ref name
	BaseURL       string  `json:"base_url,omitempty"`
	Limit         float64 `json:"limit,omitempty"`
	Unit          string  `json:"unit,omitempty"`
	WindowLabel   string  `json:"window_label,omitempty"`
	WindowMinutes int     `json:"window_minutes,omitempty"`
	Enabled       bool    `json:"enabled"`
}

// ProviderSnapshot is one provider's usage data in the JSON contract.
type ProviderSnapshot struct {
	Provider    string        `json:"provider"`
	Label       string        `json:"label"`
	Plan        string        `json:"plan,omitempty"`
	Status      string        `json:"status"` // ok|partial|unconfigured|unavailable|error
	Source      string        `json:"source"`
	SourceLabel string        `json:"source_label"`
	Observed    ObservedUsage `json:"observed"`
	Windows     []UsageWindow `json:"windows"`
	UpdatedAt   *time.Time    `json:"updated_at,omitempty"`
	Stale       bool          `json:"stale"`
	Error       string        `json:"error,omitempty"`
	Detail      string        `json:"detail,omitempty"`
}

// ObservedUsage holds aggregated usage metrics.
type ObservedUsage struct {
	Requests              int     `json:"requests"`
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
	UsedPercent     float64    `json:"used_percent,omitempty"`
	Used            float64    `json:"used,omitempty"`
	Limit           float64    `json:"limit,omitempty"`
	Remaining       float64    `json:"remaining,omitempty"`
	Unit            string     `json:"unit"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
	DurationMinutes int        `json:"duration_minutes,omitempty"`
}

// ORCreditInfo holds OpenRouter credit balance data.
type ORCreditInfo struct {
	Usage        float64 `json:"usage,omitempty"`
	Limit        float64 `json:"limit,omitempty"`
	Remaining    float64 `json:"remaining,omitempty"`
	UsageDaily   float64 `json:"usage_daily,omitempty"`
	UsageWeekly  float64 `json:"usage_weekly,omitempty"`
	UsageMonthly float64 `json:"usage_monthly,omitempty"`
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

// CachedProviderSnapshot is the SQLite-persisted row for one provider.
type CachedProviderSnapshot struct {
	Provider  string    `json:"provider"`
	Snapshot  string    `json:"snapshot"` // JSON-encoded ProviderSnapshot
	UpdatedAt time.Time `json:"updated_at"`
}

// CachedOpenRouter is the SQLite-persisted OpenRouter snapshot.
type CachedOpenRouter struct {
	ID        int       `json:"id"`
	Snapshot  string    `json:"snapshot"` // JSON-encoded OpenRouterSnapshot
	UpdatedAt time.Time `json:"updated_at"`
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

// UsageStore persists cached usage snapshots.
type UsageStore interface {
	// UpsertCachedProviderSnapshot inserts or replaces a provider
	// snapshot. UpdatedAt is stamped by the caller.
	UpsertCachedProviderSnapshot(ctx context.Context, s *CachedProviderSnapshot) error

	// GetCachedProviderSnapshot returns one provider's cached snapshot.
	// ErrNotFound when no row exists.
	GetCachedProviderSnapshot(ctx context.Context, provider string) (*CachedProviderSnapshot, error)

	// ListCachedProviderSnapshots returns every cached provider snapshot.
	ListCachedProviderSnapshots(ctx context.Context) ([]CachedProviderSnapshot, error)

	// UpsertCachedOpenRouter inserts or replaces the OpenRouter snapshot.
	UpsertCachedOpenRouter(ctx context.Context, s *CachedOpenRouter) error

	// GetCachedOpenRouter returns the cached OpenRouter snapshot.
	// ErrNotFound when no row exists.
	GetCachedOpenRouter(ctx context.Context) (*CachedOpenRouter, error)
}
