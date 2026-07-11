package collectors

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	mimoWhoamiTimeout   = 8 * time.Second
	mimoWhoamiOutputCap = 1 << 20
)

// MiMo credit-per-token coefficients. Aggregate CLI data cannot reconstruct
// the official 0.8x off-peak discount, so all windows are labelled estimates.
var (
	mimoV25ProCoefficients = mimoCreditCoefficients{
		Input:     300,
		Output:    600,
		CacheRead: 2.5,
	}
	mimoV25Coefficients = mimoCreditCoefficients{
		Input:     100,
		Output:    200,
		CacheRead: 2,
	}
)

type mimoCreditCoefficients struct {
	Input     float64
	Output    float64
	CacheRead float64
}

func (c mimoCreditCoefficients) estimate(observed store.ObservedUsage) float64 {
	return float64(observed.InputTokens)*c.Input +
		float64(observed.OutputTokens)*c.Output +
		float64(observed.CacheReadTokens)*c.CacheRead
}

var mimoProviderLine = regexp.MustCompile(`(?im)\bProvider:\s*([A-Za-z0-9._-]{1,64})\b`)

// MiMoRunFunc is injectable so auth-probe tests never launch a live CLI.
type MiMoRunFunc func(ctx context.Context, binary string) ([]byte, error)

// MiMoCollector verifies local MiMo CLI authentication without inventing quota.
type MiMoCollector struct {
	MiMoBinary string
	Run        MiMoRunFunc
	Secret     SecretReader
}

func (c *MiMoCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	bounded, cancel := context.WithTimeout(ctx, mimoWhoamiTimeout)
	defer cancel()
	output, runErr := c.runner()(bounded, c.binary())
	parsed := parseMiMoWhoami(output)
	if runErr != nil {
		parsed.errors = append(parsed.errors, redactMiMoError(runErr))
	}
	tokenPlan := c.detectTokenPlan(ctx, cfg)
	return mimoAuthResult(cfg, parsed, tokenPlan, start), nil
}

func (c *MiMoCollector) detectTokenPlan(ctx context.Context, cfg store.SourceConfig) bool {
	if cfg.Plan != "" {
		return false
	}
	secret, err := readSecret(ctx, c.Secret, cfg.AuthScopeID, cfg.SecretKey)
	if err != nil || len(secret) == 0 {
		return false
	}
	return MiMoIsTokenPlanCredential(string(secret))
}

func (c *MiMoCollector) binary() string {
	if c.MiMoBinary == "" {
		return ResolveBinary("mimo")
	}
	return c.MiMoBinary
}

func (c *MiMoCollector) runner() MiMoRunFunc {
	if c.Run != nil {
		return c.Run
	}
	return runMiMoWhoami
}

func runMiMoWhoami(ctx context.Context, binary string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, "providers", "whoami") //nolint:gosec
	output, err := cmd.CombinedOutput()
	if len(output) > mimoWhoamiOutputCap {
		output = output[:mimoWhoamiOutputCap]
	}
	if err != nil {
		return output, err
	}
	return output, nil
}

type mimoWhoamiParsed struct {
	provider string
	errors   []string
}

func parseMiMoWhoami(output []byte) mimoWhoamiParsed {
	clean := claudeANSI.ReplaceAll(output, nil)
	trimmed := strings.TrimSpace(string(clean))
	if trimmed == "" {
		return mimoWhoamiParsed{errors: []string{"mimo whoami returned no output"}}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(clean, &root) == nil {
		if provider, ok := mimoProviderIdentity(root); ok {
			return mimoWhoamiParsed{provider: provider}
		}
		return mimoWhoamiParsed{errors: []string{"mimo whoami returned no provider identity"}}
	}
	match := mimoProviderLine.FindStringSubmatch(trimmed)
	if len(match) == 2 {
		return mimoWhoamiParsed{provider: strings.ToLower(strings.TrimSpace(match[1]))}
	}
	return mimoWhoamiParsed{errors: []string{"mimo whoami returned no provider identity"}}
}

func mimoProviderIdentity(root map[string]json.RawMessage) (string, bool) {
	candidates := []string{"provider", "provider_id", "providerId", "name", "slug"}
	for _, field := range candidates {
		raw, ok := root[field]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || looksLikeUserIdentifier(field, value) {
			continue
		}
		return value, true
	}
	return "", false
}

func looksLikeUserIdentifier(field, value string) bool {
	switch field {
	case "name", "slug", "provider", "provider_id", "providerId":
		return strings.Contains(value, "@")
	default:
		return false
	}
}

func mimoAuthResult(
	cfg store.SourceConfig,
	parsed mimoWhoamiParsed,
	tokenPlan bool,
	start time.Time,
) store.CollectorResult {
	snapshot := baseSnapshot(store.ProviderMiMo, cfg, "auth")
	snapshot.SourceLabel = "MiMo CLI auth"
	snapshot.AllowanceSource = "auth"
	snapshot.AllowanceSourceLabel = "MiMo CLI auth"
	snapshot.Windows = []store.UsageWindow{}
	if snapshot.Plan == "" {
		if tokenPlan {
			snapshot.Plan = "Token Plan"
		} else {
			snapshot.Plan = "MiMoCode"
		}
	}
	if len(parsed.errors) > 0 {
		snapshot.Status = store.StatusPartial
		snapshot.AllowanceStatus = store.StatusPartial
		snapshot.Error = strings.Join(parsed.errors, "; ")
		snapshot.AllowanceError = snapshot.Error
		return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}
	}
	snapshot.Status = store.StatusOK
	snapshot.AllowanceStatus = store.StatusOK
	snapshot.UpdatedAt = timePtr(start)
	snapshot.AllowanceUpdatedAt = timePtr(start)
	snapshot.Detail = "Local MiMoCode session usage collected; subscription balance is not exposed by the CLI/API"
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}
}

func redactMiMoError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

// MiMoEstimatedCredits returns the estimated Token Plan credits for the
// observed token usage. Returns 0 if the model is unknown.
func MiMoEstimatedCredits(model string, observed store.ObservedUsage) float64 {
	coefficients, ok := mimoCoefficientsForModel(model)
	if !ok {
		return 0
	}
	return coefficients.estimate(observed)
}

func mimoCoefficientsForModel(model string) (mimoCreditCoefficients, bool) {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "v2.5-pro") || strings.Contains(lower, "v2_5-pro") {
		return mimoV25ProCoefficients, true
	}
	if strings.Contains(lower, "v2.5") || strings.Contains(lower, "v2_5") {
		return mimoV25Coefficients, true
	}
	return mimoCreditCoefficients{}, false
}

// MiMoIsTokenPlanCredential reports whether the credential value starts with
// the "tp-" prefix that identifies a MiMo Token Plan key. The raw value is
// never stored, logged, or returned.
func MiMoIsTokenPlanCredential(credential string) bool {
	return strings.HasPrefix(credential, "tp-")
}

// MiMoTokenPlanWindow builds an estimated Token Plan credits window from
// aggregated observed usage across supported MiMo models. The window is
// labelled as an estimate because the CLI cannot reconstruct the official
// 0.8x off-peak discount.
//
// If cfg has Limit > 0 and Unit == "credits", Used/Remaining/Percent are
// computed. Without a configured limit, only Used is populated.
func MiMoTokenPlanWindow(
	cfg store.SourceConfig,
	observed store.ObservedUsage,
	totalCredits float64,
) (store.UsageWindow, bool) {
	if totalCredits <= 0 {
		return store.UsageWindow{}, false
	}
	window := store.UsageWindow{
		ID:    "mimo_token_plan_credits",
		Label: "Token Plan credits (estimate)",
		Unit:  store.UnitCredits,
		Used:  numberPtr(totalCredits),
	}
	if cfg.Limit > 0 && cfg.Unit == store.UnitCredits {
		window.Limit = numberPtr(cfg.Limit)
		window.Remaining = numberPtr(math.Max(0, cfg.Limit-totalCredits))
		window.UsedPercent = numberPtr(math.Min(100, totalCredits/cfg.Limit*100))
	}
	return window, true
}

// MiMoAggregateEstimatedCredits sums estimated credits across per-model
// stats, skipping models with unknown coefficients.
func MiMoAggregateEstimatedCredits(stats []ObservedModelUsage) float64 {
	var total float64
	for _, entry := range stats {
		total += MiMoEstimatedCredits(entry.Model, entry.Observed)
	}
	return total
}

// ObservedModelUsage pairs a model name with its observed usage for
// per-model credit estimation.
type ObservedModelUsage struct {
	Model    string
	Observed store.ObservedUsage
}
