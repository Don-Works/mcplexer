package collectors

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const miniMaxDefaultURL = "https://www.minimax.io/v1/token_plan/remains"

// MiniMaxCollector fetches Token Plan quota windows from MiniMax.
type MiniMaxCollector struct {
	Client httpClient
	Secret SecretReader
}

func (c *MiniMaxCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	token, err := requireSecret(ctx, c.Secret, cfg.AuthScopeID, cfg.SecretKey)
	if err != nil {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusUnconfigured, err.Error(), start), nil
	}
	body, status, err := c.doFetch(ctx, miniMaxURL(cfg.BaseURL), token)
	if err != nil {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusError,
			fmt.Sprintf("request failed: %v", err), start), nil
	}
	if status != http.StatusOK {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusError,
			fmt.Sprintf("HTTP %d", status), start), nil
	}
	windows, err := parseMiniMaxResponse(body)
	if err != nil {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusError,
			fmt.Sprintf("parse: %v", err), start), nil
	}
	snapshot := baseSnapshot(store.ProviderMiniMax, cfg, "api")
	if snapshot.Plan == "" {
		snapshot.Plan = "Token Plan"
	}
	snapshot.Status, snapshot.UpdatedAt, snapshot.Windows = store.StatusOK, timePtr(start), windows
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}, nil
}

func miniMaxURL(base string) string {
	if base == "" {
		return miniMaxDefaultURL
	}
	return strings.TrimRight(base, "/") + "/v1/token_plan/remains"
}

func (c *MiniMaxCollector) doFetch(ctx context.Context, url, token string) ([]byte, int, error) {
	req, err := newBearerRequest(ctx, url, token)
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

func parseMiniMaxResponse(body []byte) ([]store.UsageWindow, error) {
	root, err := decodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("unmarshal minimax: %w", err)
	}
	var windows []store.UsageWindow
	if object, ok := root.(map[string]any); ok {
		if rawRemains, found := lookupValue(object, "model_remains"); found {
			remains, _ := rawRemains.([]any)
			for _, raw := range remains {
				entry, objectErr := mustObject(raw)
				if objectErr != nil {
					continue
				}
				// MiniMax projects one unified Token Plan across modality rows. Showing
				// video beside general makes it look like a separate coding allowance,
				// so use general as the canonical dashboard view and never sum them.
				if normalizedKey(miniMaxLabel(entry)) == "video" {
					continue
				}
				mapped := mapMiniMaxCodingPlanWindows(entry)
				if len(mapped) == 0 {
					if window, valid := mapMiniMaxWindow(entry, len(windows)); valid {
						mapped = append(mapped, window)
					}
				}
				windows = append(windows, mapped...)
			}
			for key, child := range object {
				if normalizedKey(key) != "modelremains" {
					collectMiniMaxWindows(child, &windows, 0)
				}
			}
		} else {
			collectMiniMaxWindows(root, &windows, 0)
		}
	} else {
		collectMiniMaxWindows(root, &windows, 0)
	}
	windows = dedupeMiniMaxWindows(windows)
	if len(windows) == 0 {
		return nil, fmt.Errorf("response contains no measurable quota window")
	}
	return windows, nil
}

func mapMiniMaxCodingPlanWindows(values map[string]any) []store.UsageWindow {
	label := miniMaxLabel(values)
	return compactMiniMaxWindows(
		miniMaxCodingPlanWindow(values, label+" (5-hour)", "currentInterval", 300),
		miniMaxCodingPlanWindow(values, label+" (weekly)", "currentWeekly", 7*24*60),
	)
}

func miniMaxCodingPlanWindow(
	values map[string]any,
	label string,
	prefix string,
	duration int,
) store.UsageWindow {
	total := miniMaxNumber(values, prefix+"TotalCount")
	remaining := miniMaxNumber(values, prefix+"UsageCount")
	remainingPercent := miniMaxNumber(values, prefix+"RemainingPercent")
	window := store.UsageWindow{
		ID: identifier("minimax", label), Label: label, Limit: total,
		Remaining: remaining, Unit: store.UnitRequests, DurationMinutes: duration,
	}
	if strings.Contains(strings.ToLower(prefix), "weekly") {
		window.ResetsAt = lookupTimestamp(values, "weeklyEndTime")
	} else {
		window.ResetsAt = lookupTimestamp(values, "endTime")
	}
	if total != nil && *total > 0 && remaining != nil {
		clamped := math.Min(*total, math.Max(0, *remaining))
		window.Remaining = numberPtr(clamped)
		window.Used = numberPtr(*total - clamped)
		window.UsedPercent = numberPtr((*window.Used / *total) * 100)
	}
	// Explicit remaining percentage is authoritative in the official CLI.
	// Keep request-equivalent counts for display, but do not let them overwrite
	// a provider-supplied percentage (including an explicit zero usage).
	if remainingPercent != nil {
		window.UsedPercent = numberPtr(nonNegative(100 - *remainingPercent))
		if total == nil || *total == 0 {
			window.Unit = store.UnitPercent
			window.Remaining = nil
			window.Used = nil
			window.Limit = nil
		}
	}
	return window
}

func compactMiniMaxWindows(values ...store.UsageWindow) []store.UsageWindow {
	result := make([]store.UsageWindow, 0, len(values))
	for _, value := range values {
		if hasWindowMeasurement(value) {
			result = append(result, value)
		}
	}
	return result
}

func collectMiniMaxWindows(value any, windows *[]store.UsageWindow, depth int) {
	if depth > 8 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		if window, ok := mapMiniMaxWindow(typed, len(*windows)); ok {
			*windows = append(*windows, window)
		}
		for _, child := range typed {
			collectMiniMaxWindows(child, windows, depth+1)
		}
	case []any:
		for _, child := range typed {
			collectMiniMaxWindows(child, windows, depth+1)
		}
	}
}

func mapMiniMaxWindow(values map[string]any, index int) (store.UsageWindow, bool) {
	label := miniMaxLabel(values)
	window := store.UsageWindow{
		ID: identifier("minimax", label, fmt.Sprint(index)), Label: label,
		Used: miniMaxNumber(values, "used", "usage", "currentValue", "usedTokens",
			"currentIntervalUsageCount", "consumed"),
		Limit: miniMaxNumber(values, "limit", "total", "grant", "totalTokensGrant",
			"currentIntervalTotalCount", "promptLimit", "quota"),
		Remaining: miniMaxNumber(values, "remaining", "remain", "totalTokensRemain",
			"currentIntervalRemainingCount", "promptRemain", "available"),
		UsedPercent: miniMaxPercentage(values), ResetsAt: lookupTimestamp(values,
			"nextResetTime", "resetsAt", "resetAt", "endTime", "expireTime"),
	}
	completeWindowValues(&window)
	if window.Used != nil && window.Limit != nil && *window.Limit > 0 {
		window.UsedPercent = numberPtr((*window.Used / *window.Limit) * 100)
	}
	window.Unit = inferMiniMaxUnit(values, label, window)
	window.DurationMinutes = inferMiniMaxDuration(values, label)
	return window, hasWindowMeasurement(window)
}

func miniMaxNumber(values map[string]any, names ...string) *float64 {
	result, _ := lookupNumber(values, names...)
	return result
}

func miniMaxPercentage(values map[string]any) *float64 {
	if value, ok := lookupNumber(values, "usedPercent", "usedPercentage", "percentage"); ok {
		return value
	}
	if value, ok := lookupNumber(values, "usageRatio"); ok {
		if *value >= 0 && *value <= 1 {
			return numberPtr(*value * 100)
		}
		return value
	}
	if value, ok := lookupNumber(values, "usagePercent", "remainingPercentage", "remainPercent"); ok {
		return numberPtr(nonNegative(100 - *value))
	}
	return nil
}

func miniMaxLabel(values map[string]any) string {
	if label, ok := lookupString(values, "label", "name", "windowName", "modelName", "model", "type"); ok {
		return label
	}
	return "Token Plan"
}

func inferMiniMaxUnit(values map[string]any, label string, window store.UsageWindow) string {
	if unit, ok := lookupString(values, "unit"); ok {
		normalized := normalizedKey(unit)
		if strings.Contains(normalized, "token") {
			return store.UnitTokens
		}
		if strings.Contains(normalized, "request") || strings.Contains(normalized, "call") {
			return store.UnitRequests
		}
	}
	keys := normalizedKey(label)
	for key := range values {
		keys += normalizedKey(key)
	}
	if strings.Contains(keys, "token") {
		return store.UnitTokens
	}
	if strings.Contains(keys, "count") || strings.Contains(keys, "request") || strings.Contains(keys, "call") {
		return store.UnitRequests
	}
	if window.Used == nil && window.Limit == nil && window.Remaining == nil {
		return store.UnitPercent
	}
	return store.UnitRequests
}

func inferMiniMaxDuration(values map[string]any, label string) int {
	if duration, ok := lookupNumber(values, "windowMinutes", "windowDurationMins"); ok {
		return int(*duration)
	}
	if duration, ok := lookupNumber(values, "windowHours"); ok {
		return int(*duration * 60)
	}
	start := lookupTimestamp(values, "startTime")
	end := lookupTimestamp(values, "endTime")
	if start != nil && end != nil && end.After(*start) {
		return int(end.Sub(*start).Minutes())
	}
	normalized := normalizedKey(label)
	switch {
	case strings.Contains(normalized, "weekly") || strings.Contains(normalized, "week"):
		return 7 * 24 * 60
	case strings.Contains(normalized, "daily") || strings.Contains(normalized, "day"):
		return 24 * 60
	case strings.Contains(normalized, "5hour") || strings.Contains(normalized, "5h"):
		return 5 * 60
	default:
		return 0
	}
}

func dedupeMiniMaxWindows(windows []store.UsageWindow) []store.UsageWindow {
	bySignature := make(map[string]store.UsageWindow, len(windows))
	for _, window := range windows {
		signature := fmt.Sprintf("%s|%s|%s|%s|%s", normalizedKey(window.Label),
			pointerText(window.UsedPercent), pointerText(window.Used),
			pointerText(window.Limit), pointerText(window.Remaining))
		bySignature[signature] = window
	}
	keys := make([]string, 0, len(bySignature))
	for key := range bySignature {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]store.UsageWindow, 0, len(keys))
	for index, key := range keys {
		window := bySignature[key]
		window.ID = identifier("minimax", window.Label, fmt.Sprint(index))
		result = append(result, window)
	}
	return result
}

func baseSnapshot(provider string, cfg store.SourceConfig, source string) store.ProviderSnapshot {
	return store.ProviderSnapshot{
		Provider: provider, Label: cfg.Label, Plan: cfg.Plan, Source: source,
		Windows: []store.UsageWindow{},
	}
}

func collectorError(provider string, cfg store.SourceConfig, status, msg string, start time.Time) store.CollectorResult {
	snapshot := baseSnapshot(provider, cfg, "api")
	snapshot.Status, snapshot.Error = status, msg
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}
}

func timePtr(value time.Time) *time.Time { return &value }
