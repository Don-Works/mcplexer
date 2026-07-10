package collectors

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type codexParsed struct {
	windows  []store.UsageWindow
	observed store.ObservedUsage
	plan     string
	errors   []string
}

type codexEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Params json.RawMessage `json:"params"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseCodexOutput(output []byte) codexParsed {
	var parsed codexParsed
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var envelope codexEnvelope
		if json.Unmarshal(scanner.Bytes(), &envelope) != nil {
			continue
		}
		parseCodexEnvelope(envelope, &parsed)
	}
	if err := scanner.Err(); err != nil {
		parsed.errors = append(parsed.errors, "read codex output: "+err.Error())
	}
	parsed.windows = dedupeCodexWindows(parsed.windows)
	return parsed
}

func parseCodexEnvelope(envelope codexEnvelope, parsed *codexParsed) {
	id := rpcID(envelope.ID)
	if envelope.Error != nil && (id == "2" || id == "3") {
		message := cleanCodexError(fmt.Errorf("codex request %s: %s", id, envelope.Error.Message))
		parsed.errors = append(parsed.errors, message)
		return
	}
	switch id {
	case "2":
		windows, plan := parseCodexRateResult(envelope.Result)
		parsed.windows = append(parsed.windows, windows...)
		if plan != "" {
			parsed.plan = plan
		}
	case "3":
		if observed, ok := parseCodexUsageResult(envelope.Result); ok {
			parsed.observed = observed
		}
	default:
		parseCodexNotification(envelope, parsed)
	}
}

func rpcID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

func parseCodexNotification(envelope codexEnvelope, parsed *codexParsed) {
	if !strings.Contains(strings.ToLower(envelope.Method), "ratelimit") || len(envelope.Params) == 0 {
		return
	}
	windows, plan := parseCodexRateResult(envelope.Params)
	parsed.windows = append(parsed.windows, windows...)
	if parsed.plan == "" {
		parsed.plan = plan
	}
}

type codexRateResult struct {
	RateLimits            codexRateSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID   map[string]codexRateSnapshot `json:"rateLimitsByLimitId"`
	RateLimitResetCredits json.RawMessage              `json:"rateLimitResetCredits"`
}

type codexRateSnapshot struct {
	LimitID   *string          `json:"limitId"`
	LimitName *string          `json:"limitName"`
	PlanType  *string          `json:"planType"`
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
}

type codexRateWindow struct {
	UsedPercent        int64  `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

func parseCodexRateResult(raw json.RawMessage) ([]store.UsageWindow, string) {
	var result codexRateResult
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return nil, ""
	}
	topWindows := rateSnapshotWindows(result.RateLimits, "", "Codex")
	var windows []store.UsageWindow
	plan := stringValue(result.RateLimits.PlanType)
	keys := make([]string, 0, len(result.RateLimitsByLimitID))
	for key := range result.RateLimitsByLimitID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		snapshot := result.RateLimitsByLimitID[key]
		windows = append(windows, rateSnapshotWindows(snapshot, key, rateLabel(snapshot, key))...)
		if plan == "" {
			plan = stringValue(snapshot.PlanType)
		}
	}
	for _, top := range topWindows {
		if !containsEquivalentRateWindow(windows, top) {
			windows = append(windows, top)
		}
	}
	return windows, displayCodexPlan(plan)
}

func displayCodexPlan(plan string) string {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "pro":
		return "Pro"
	case "plus":
		return "Plus"
	case "team":
		return "Team"
	case "enterprise":
		return "Enterprise"
	default:
		return strings.TrimSpace(plan)
	}
}

func containsEquivalentRateWindow(windows []store.UsageWindow, candidate store.UsageWindow) bool {
	for _, window := range windows {
		if rateTier(window.ID) == rateTier(candidate.ID) &&
			pointerText(window.UsedPercent) == pointerText(candidate.UsedPercent) &&
			window.DurationMinutes == candidate.DurationMinutes && sameReset(window, candidate) {
			return true
		}
	}
	return false
}

func rateTier(id string) string {
	if strings.HasSuffix(id, "_secondary") {
		return "secondary"
	}
	return "primary"
}

func sameReset(left, right store.UsageWindow) bool {
	if left.ResetsAt == nil || right.ResetsAt == nil {
		return left.ResetsAt == nil && right.ResetsAt == nil
	}
	return left.ResetsAt.Equal(*right.ResetsAt)
}

func rateSnapshotWindows(snapshot codexRateSnapshot, limitID, label string) []store.UsageWindow {
	var windows []store.UsageWindow
	if snapshot.Primary != nil {
		windows = append(windows, mapCodexRateWindow(*snapshot.Primary, limitID, label, "primary"))
	}
	if snapshot.Secondary != nil {
		windows = append(windows, mapCodexRateWindow(*snapshot.Secondary, limitID, label, "secondary"))
	}
	return windows
}

func mapCodexRateWindow(value codexRateWindow, limitID, label, tier string) store.UsageWindow {
	duration := 0
	if value.WindowDurationMins != nil && *value.WindowDurationMins > 0 {
		duration = int(*value.WindowDurationMins)
	}
	var reset *time.Time
	if value.ResetsAt != nil {
		reset = timestampValue(json.Number(strconv.FormatInt(*value.ResetsAt, 10)))
	}
	return store.UsageWindow{
		ID: identifier("codex", limitID, tier), Label: label + " " + tier,
		UsedPercent: numberPtr(float64(value.UsedPercent)), Unit: store.UnitPercent,
		ResetsAt: reset, DurationMinutes: duration,
	}
}

func rateLabel(snapshot codexRateSnapshot, fallback string) string {
	if value := stringValue(snapshot.LimitName); value != "" {
		return value
	}
	if value := stringValue(snapshot.LimitID); value != "" {
		return value
	}
	if fallback != "" {
		return fallback
	}
	return "Codex"
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

type codexUsageResult struct {
	Summary struct {
		LifetimeTokens  *int64 `json:"lifetimeTokens"`
		PeakDailyTokens *int64 `json:"peakDailyTokens"`
	} `json:"summary"`
	DailyUsageBuckets []struct {
		StartDate string `json:"startDate"`
		Tokens    int64  `json:"tokens"`
	} `json:"dailyUsageBuckets"`
}

func parseCodexUsageResult(raw json.RawMessage) (store.ObservedUsage, bool) {
	var result codexUsageResult
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return store.ObservedUsage{}, false
	}
	buckets := result.DailyUsageBuckets
	if len(buckets) > 31 {
		buckets = buckets[len(buckets)-31:]
	}
	var total int64
	for _, bucket := range buckets {
		total += bucket.Tokens
	}
	if len(buckets) == 0 && result.Summary.LifetimeTokens != nil {
		total = *result.Summary.LifetimeTokens
	}
	if total <= 0 {
		return store.ObservedUsage{}, false
	}
	return store.ObservedUsage{TotalTokens: int(total)}, true
}

func dedupeCodexWindows(windows []store.UsageWindow) []store.UsageWindow {
	result := make([]store.UsageWindow, 0, len(windows))
	seen := make(map[string]bool, len(windows))
	for _, window := range windows {
		signature := windowSignature(window)
		if seen[signature] {
			continue
		}
		seen[signature] = true
		result = append(result, window)
	}
	return result
}

func windowSignature(window store.UsageWindow) string {
	reset := int64(0)
	if window.ResetsAt != nil {
		reset = window.ResetsAt.Unix()
	}
	return fmt.Sprintf("%s|%s|%s|%d|%d|%s", normalizedKey(window.Label),
		pointerText(window.UsedPercent), pointerText(window.Used), window.DurationMinutes, reset, window.Unit)
}

func pointerText(value *float64) string {
	if value == nil {
		return "nil"
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}
