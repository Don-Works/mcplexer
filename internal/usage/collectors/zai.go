package collectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const zaiDefaultBase = "https://api.z.ai"

// ZAICollector fetches Coding Plan quota data from Z.AI or BigModel.
type ZAICollector struct {
	Client httpClient
	Secret SecretReader
}

func (c *ZAICollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	token, err := requireSecret(ctx, c.Secret, cfg.AuthScopeID, cfg.SecretKey)
	if err != nil {
		return collectorError(store.ProviderZAI, cfg, store.StatusUnconfigured, err.Error(), start), nil
	}
	body, status, err := c.doFetch(ctx, zaiURL(cfg.BaseURL), token)
	if err != nil {
		return collectorError(store.ProviderZAI, cfg, store.StatusError,
			fmt.Sprintf("request failed: %v", err), start), nil
	}
	if status != http.StatusOK {
		return collectorError(store.ProviderZAI, cfg, store.StatusError,
			fmt.Sprintf("HTTP %d", status), start), nil
	}
	windows, err := parseZAIResponse(body)
	if err != nil {
		return collectorError(store.ProviderZAI, cfg, store.StatusError,
			fmt.Sprintf("parse: %v", err), start), nil
	}
	snapshot := baseSnapshot(store.ProviderZAI, cfg, "api")
	if plan := parseZAIPlan(body); plan != "" {
		snapshot.Plan = plan
	} else if snapshot.Plan == "" {
		snapshot.Plan = "Coding Plan"
	}
	snapshot.Status, snapshot.UpdatedAt, snapshot.Windows = store.StatusOK, timePtr(start), windows
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}, nil
}

func zaiURL(base string) string {
	if base == "" {
		base = zaiDefaultBase
	}
	return strings.TrimRight(base, "/") + "/api/monitor/usage/quota/limit"
}

func (c *ZAICollector) doFetch(ctx context.Context, url, token string) ([]byte, int, error) {
	req, err := newRawAuthRequest(ctx, url, token)
	if err != nil {
		return nil, 0, err
	}
	resp, err := requestClient(c.Client).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

func parseZAIResponse(body []byte) ([]store.UsageWindow, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("unmarshal zai: %w", err)
	}
	entries, ok := findArrayByKey(root, "limits", 0)
	if !ok {
		entries, ok = findZAIQuotaArray(root, 0)
	}
	if !ok || len(entries) == 0 {
		return nil, fmt.Errorf("no quota limits")
	}
	windows := make([]store.UsageWindow, 0, len(entries))
	for _, raw := range entries {
		entry, objectErr := mustObject(raw)
		if objectErr != nil {
			continue
		}
		if window, mapped := mapZAIWindow(entry); mapped {
			windows = append(windows, window)
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no supported quota limits")
	}
	return windows, nil
}

func findZAIQuotaArray(value any, depth int) ([]any, bool) {
	if depth > 8 {
		return nil, false
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok && isZAILimitEntry(entry) {
				return typed, true
			}
		}
		for _, child := range typed {
			if result, ok := findZAIQuotaArray(child, depth+1); ok {
				return result, true
			}
		}
	case map[string]any:
		for _, child := range typed {
			if result, ok := findZAIQuotaArray(child, depth+1); ok {
				return result, true
			}
		}
	}
	return nil, false
}

func isZAILimitEntry(entry map[string]any) bool {
	limitType, ok := lookupString(entry, "type")
	if !ok {
		return false
	}
	return strings.EqualFold(limitType, "TOKENS_LIMIT") || strings.EqualFold(limitType, "TIME_LIMIT")
}

func mapZAIWindow(entry map[string]any) (store.UsageWindow, bool) {
	limitType, ok := lookupString(entry, "type")
	if !ok {
		return store.UsageWindow{}, false
	}
	window := store.UsageWindow{
		ID: identifier("zai", limitType), UsedPercent: optionalNumber(entry, "percentage"),
		Used: optionalNumber(entry, "currentValue"), Limit: optionalNumber(entry, "usage"),
		Remaining: optionalNumber(entry, "remaining"),
		ResetsAt:  lookupTimestamp(entry, "nextResetTime", "resetsAt"),
	}
	switch strings.ToUpper(limitType) {
	case "TOKENS_LIMIT":
		window.Label, window.Unit, window.DurationMinutes = "Token usage (5 hour)", store.UnitTokens, 300
	case "TIME_LIMIT":
		window.Label, window.Unit = "MCP usage (monthly)", store.UnitRequests
		if window.Used == nil {
			window.Used = sumZAIUsageDetails(entry)
		}
	default:
		return store.UsageWindow{}, false
	}
	completeWindowValues(&window)
	return window, hasZAIWindowMeasurement(window, limitType)
}

func hasZAIWindowMeasurement(window store.UsageWindow, limitType string) bool {
	if strings.EqualFold(limitType, "TOKENS_LIMIT") && window.Used == nil &&
		window.Limit == nil && window.Remaining == nil {
		return window.UsedPercent != nil
	}
	return hasWindowMeasurement(window)
}

func parseZAIPlan(body []byte) string {
	root, err := decodeJSON(body)
	if err != nil {
		return ""
	}
	level := findZAIPlanLevel(root, 0)
	switch strings.ToLower(level) {
	case "max":
		return "GLM Coding Max"
	case "pro":
		return "GLM Coding Pro"
	case "lite":
		return "GLM Coding Lite"
	case "free":
		return "GLM Coding Free"
	case "":
		return ""
	default:
		return "GLM Coding " + level
	}
}

func findZAIPlanLevel(value any, depth int) string {
	if depth > 8 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		if level, ok := lookupString(typed, "level"); ok {
			return level
		}
		for _, child := range typed {
			if level := findZAIPlanLevel(child, depth+1); level != "" {
				return level
			}
		}
	case []any:
		for _, child := range typed {
			if level := findZAIPlanLevel(child, depth+1); level != "" {
				return level
			}
		}
	}
	return ""
}

func sumZAIUsageDetails(entry map[string]any) *float64 {
	value, ok := lookupValue(entry, "usageDetails")
	if !ok {
		return nil
	}
	details, ok := value.([]any)
	if !ok || len(details) == 0 {
		return nil
	}
	var total float64
	measured := false
	for _, raw := range details {
		detail, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if usage, ok := lookupNumber(detail, "usage", "currentValue"); ok {
			total, measured = total+*usage, true
		}
	}
	if !measured {
		return nil
	}
	return numberPtr(total)
}

func optionalNumber(values map[string]any, names ...string) *float64 {
	value, _ := lookupNumber(values, names...)
	return value
}

func completeWindowValues(window *store.UsageWindow) {
	if window.Limit == nil {
		return
	}
	if window.Used == nil && window.Remaining != nil {
		window.Used = numberPtr(nonNegative(*window.Limit - *window.Remaining))
	}
	if window.Remaining == nil && window.Used != nil {
		window.Remaining = numberPtr(nonNegative(*window.Limit - *window.Used))
	}
}

func hasWindowMeasurement(window store.UsageWindow) bool {
	return window.UsedPercent != nil || window.Used != nil || window.Remaining != nil
}

func nonNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}
