package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// UsageSourceSettings describes one usage collector without carrying secret
// material. AuthScopeID and SecretKey reference the encrypted secrets store.
type UsageSourceSettings struct {
	Provider      string  `json:"provider"`
	Label         string  `json:"label,omitempty"`
	Plan          string  `json:"plan,omitempty"`
	Harness       string  `json:"harness,omitempty"`
	AuthScopeID   string  `json:"auth_scope_id,omitempty"`
	SecretKey     string  `json:"secret_key,omitempty"`
	BaseURL       string  `json:"base_url,omitempty"`
	Limit         float64 `json:"limit,omitempty"`
	Unit          string  `json:"unit,omitempty"`
	WindowLabel   string  `json:"window_label,omitempty"`
	WindowMinutes int     `json:"window_minutes,omitempty"`
}

// UsageSourceConfigs converts persisted, non-secret settings into the domain
// DTO consumed by the usage service. Enabled is explicit because a saved row
// represents an active source; removal is how an operator disables it.
func UsageSourceConfigs(settings Settings) []store.SourceConfig {
	out := make([]store.SourceConfig, 0, len(settings.UsageSources))
	for _, source := range settings.UsageSources {
		provider := strings.ToLower(strings.TrimSpace(source.Provider))
		out = append(out, store.SourceConfig{
			Provider:      provider,
			Kind:          usageSourceKind(source),
			Label:         source.Label,
			Plan:          source.Plan,
			Harness:       source.Harness,
			AuthScopeID:   source.AuthScopeID,
			SecretKey:     source.SecretKey,
			BaseURL:       source.BaseURL,
			Limit:         source.Limit,
			Unit:          strings.ToLower(strings.TrimSpace(source.Unit)),
			WindowLabel:   source.WindowLabel,
			WindowMinutes: source.WindowMinutes,
			Enabled:       true,
		})
	}
	return out
}

func usageSourceKind(source UsageSourceSettings) string {
	if source.AuthScopeID != "" {
		return store.SourceKindAPI
	}
	if source.Limit > 0 {
		return store.SourceKindManual
	}
	switch strings.ToLower(strings.TrimSpace(source.Provider)) {
	case store.ProviderClaude, store.ProviderCodex, store.ProviderGrok, store.ProviderMiMo:
		return store.SourceKindCLI
	}
	return store.SourceKindAuto
}

var usageSecretKeyRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

var usageProviderHosts = map[string]map[string]bool{
	"minimax":    {"www.minimax.io": true, "api.minimax.io": true, "api.minimaxi.com": true},
	"zai":        {"api.z.ai": true, "open.bigmodel.cn": true, "dev.bigmodel.cn": true},
	"openrouter": {"openrouter.ai": true},
}

var usageUnits = map[string]bool{
	"percent": true, "requests": true, "credits": true,
	"usd": true, "tokens": true,
}

// ValidateUsageSources rejects malformed or unsafe collector configuration.
// In particular, custom API roots are restricted to known first-party hosts
// so a saved setting cannot turn refresh into an SSRF primitive.
func ValidateUsageSources(sources []UsageSourceSettings) error {
	seen := make(map[string]bool, len(sources))
	for i := range sources {
		source := sources[i]
		provider := strings.ToLower(strings.TrimSpace(source.Provider))
		if err := validateUsageSource(provider, source); err != nil {
			return fmt.Errorf("usage_sources[%d]: %w", i, err)
		}
		key := provider
		if seen[key] {
			return fmt.Errorf("usage_sources[%d]: duplicate provider/harness %q", i, key)
		}
		seen[key] = true
	}
	return nil
}

func validateUsageSource(provider string, source UsageSourceSettings) error {
	if !isUsageProvider(provider) {
		return fmt.Errorf("provider must be one of: claude, codex, minimax, zai, grok, mimo, openrouter")
	}
	if len(source.Label) > 80 || len(source.Plan) > 80 || len(source.Harness) > 80 || len(source.WindowLabel) > 80 {
		return fmt.Errorf("label, plan, harness, and window_label must be at most 80 characters")
	}
	if (source.AuthScopeID == "") != (source.SecretKey == "") {
		return fmt.Errorf("auth_scope_id and secret_key must be supplied together")
	}
	if source.SecretKey != "" && !usageSecretKeyRE.MatchString(source.SecretKey) {
		return fmt.Errorf("secret_key must contain only letters, numbers, '.', '_', or '-'")
	}
	if source.Harness != "" && provider != "openrouter" {
		return fmt.Errorf("harness is only valid for openrouter sources")
	}
	if err := validateUsageBaseURL(provider, source.BaseURL); err != nil {
		return err
	}
	if math.IsNaN(source.Limit) || math.IsInf(source.Limit, 0) || source.Limit < 0 {
		return fmt.Errorf("limit must be a finite non-negative number")
	}
	unit := strings.ToLower(strings.TrimSpace(source.Unit))
	if (source.Limit > 0) != (unit != "") {
		return fmt.Errorf("limit and unit must be supplied together")
	}
	if unit != "" && !usageUnits[unit] {
		return fmt.Errorf("unit must be one of: percent, requests, credits, usd, tokens")
	}
	if source.WindowMinutes < 0 || source.WindowMinutes > 366*24*60 {
		return fmt.Errorf("window_minutes must be between 0 and 527040")
	}
	return nil
}

func isUsageProvider(provider string) bool {
	switch provider {
	case "claude", "codex", "minimax", "zai", "grok", "mimo", "openrouter":
		return true
	default:
		return false
	}
}

func validateUsageBaseURL(provider, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	allowed := usageProviderHosts[provider]
	if len(allowed) == 0 {
		return fmt.Errorf("base_url is not supported for %s", provider)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("base_url must be an https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url must not include credentials, query, or fragment")
	}
	if !allowed[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("base_url host is not an approved first-party host for %s", provider)
	}
	return nil
}
