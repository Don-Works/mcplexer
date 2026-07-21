package collectors

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const grokWeeklyMinutes = 7 * 24 * 60

type grokParsed struct {
	windows []store.UsageWindow
	plan    string
	errors  []string
}

type grokBillingLine struct {
	Msg string `json:"msg"`
	Ctx struct {
		SubscriptionTier string          `json:"subscriptionTier"`
		Config           grokBillingConf `json:"config"`
	} `json:"ctx"`
}

type grokBillingConf struct {
	CurrentPeriod      grokPeriod      `json:"currentPeriod"`
	CreditUsagePercent json.RawMessage `json:"creditUsagePercent"`
	OnDemandCap        json.RawMessage `json:"onDemandCap"`
	OnDemandUsed       json.RawMessage `json:"onDemandUsed"`
	PrepaidBalance     json.RawMessage `json:"prepaidBalance"`
}

type grokPeriod struct {
	Type  string          `json:"type"`
	Start json.RawMessage `json:"start"`
	End   json.RawMessage `json:"end"`
}

func parseGrokDebugOutput(output []byte) grokParsed {
	var parsed grokParsed
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if event, ok := parseGrokBillingLine(scanner.Bytes()); ok {
			parsed.plan = event.plan
			parsed.windows = append(parsed.windows, event.windows...)
			return parsed
		}
	}
	if err := scanner.Err(); err != nil {
		parsed.errors = append(parsed.errors, "read grok debug output: "+err.Error())
	}
	return parsed
}

func parseGrokBillingLine(line []byte) (grokParsed, bool) {
	var event grokBillingLine
	if json.Unmarshal(line, &event) == nil && normalizedKey(event.Msg) == normalizedKey(grokBillingMsg) {
		plan := strings.TrimSpace(event.Ctx.SubscriptionTier)
		windows := grokWindowsFromConfig(event.Ctx.Config)
		if len(windows) > 0 || plan != "" {
			return grokParsed{windows: windows, plan: plan}, true
		}
	}
	response, ok := grokEmbeddedBillingResponse(line)
	if !ok {
		return grokParsed{}, false
	}
	plan := strings.TrimSpace(response.SubscriptionTier)
	windows := grokWindowsFromConfig(response.Config)
	if len(windows) == 0 && plan == "" {
		return grokParsed{}, false
	}
	return grokParsed{windows: windows, plan: plan}, true
}

type grokBillingResponse struct {
	Config           grokBillingConf `json:"config"`
	SubscriptionTier string          `json:"subscription_tier"`
}

func grokEmbeddedBillingResponse(line []byte) (grokBillingResponse, bool) {
	clean := claudeANSI.ReplaceAll(line, nil)
	start := bytes.IndexByte(clean, '{')
	if start < 0 {
		return grokBillingResponse{}, false
	}
	var response grokBillingResponse
	if json.Unmarshal(clean[start:], &response) != nil {
		return grokBillingResponse{}, false
	}
	if response.SubscriptionTier == "" && len(response.Config.CurrentPeriod.Start) == 0 {
		return grokBillingResponse{}, false
	}
	return response, true
}

func grokWindowsFromConfig(config grokBillingConf) []store.UsageWindow {
	windows := make([]store.UsageWindow, 0, 3)
	window := grokWeeklyWindow(config)
	if window != nil {
		windows = append(windows, *window)
	}
	if onDemand := grokOnDemandWindow(config); onDemand != nil {
		windows = append(windows, *onDemand)
	}
	if prepaid := grokPrepaidWindow(config); prepaid != nil {
		windows = append(windows, *prepaid)
	}
	return windows
}

func grokWeeklyWindow(config grokBillingConf) *store.UsageWindow {
	period := config.CurrentPeriod
	if len(period.Start) == 0 && len(period.End) == 0 && len(config.CreditUsagePercent) == 0 {
		return nil
	}
	duration := grokPeriodMinutes(period)
	reset := grokPeriodReset(period.End)
	usedPercent, hasPercent := grokOptionalPercent(config.CreditUsagePercent)
	window := store.UsageWindow{
		ID: "grok_shared_pool", Label: grokPeriodLabel(period.Type),
		Unit: store.UnitPercent, DurationMinutes: duration, ResetsAt: reset,
	}
	if hasPercent {
		window.UsedPercent = usedPercent
	} else if len(period.Start) > 0 && len(period.End) > 0 {
		// The Grok proto-to-JSON response omits scalar zero values. A complete
		// billing period therefore makes an omitted percentage a measured zero.
		window.UsedPercent = numberPtr(0)
	}
	return &window
}

func grokOnDemandWindow(config grokBillingConf) *store.UsageWindow {
	capValue, hasCap := grokOptionalNumber(config.OnDemandCap)
	used, hasUsed := grokOptionalNumber(config.OnDemandUsed)
	if (!hasCap || *capValue <= 0) && (!hasUsed || *used <= 0) {
		return nil
	}
	window := store.UsageWindow{
		ID: "grok_on_demand", Label: "On-demand credits", Unit: store.UnitCredits,
	}
	if hasCap {
		window.Limit = capValue
	}
	if hasUsed {
		window.Used = used
	}
	completeWindowValues(&window)
	if window.Used != nil && window.Limit != nil && *window.Limit > 0 {
		window.UsedPercent = numberPtr((*window.Used / *window.Limit) * 100)
	}
	return &window
}

func grokPrepaidWindow(config grokBillingConf) *store.UsageWindow {
	balance, ok := grokOptionalNumber(config.PrepaidBalance)
	if !ok || *balance <= 0 {
		return nil
	}
	return &store.UsageWindow{
		ID: "grok_prepaid", Label: "Prepaid balance", Unit: store.UnitCredits,
		Remaining: balance,
	}
}

func grokPeriodLabel(periodType string) string {
	normalized := strings.ToLower(strings.TrimSpace(periodType))
	switch {
	case strings.Contains(normalized, "week"):
		return "Weekly shared pool"
	case strings.Contains(normalized, "month"):
		return "Monthly shared pool"
	case strings.Contains(normalized, "day"):
		return "Daily shared pool"
	default:
		return "Shared subscription pool"
	}
}

func grokOptionalPercent(raw json.RawMessage) (*float64, bool) {
	return grokOptionalNumber(raw)
}

func grokOptionalNumber(raw json.RawMessage) (*float64, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	if number, ok := numericValue(value); ok {
		return number, true
	}
	if object, ok := value.(map[string]any); ok {
		if nested, found := lookupValue(object, "val", "value"); found {
			return numericValue(nested)
		}
	}
	return nil, false
}

func grokPeriodMinutes(period grokPeriod) int {
	start := grokParseTime(period.Start)
	end := grokParseTime(period.End)
	if start != nil && end != nil && end.After(*start) {
		return int(end.Sub(*start).Minutes())
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(period.Type)), "week") {
		return grokWeeklyMinutes
	}
	return grokWeeklyMinutes
}

func grokPeriodReset(end json.RawMessage) *time.Time {
	return grokParseTime(end)
}

func grokParseTime(raw json.RawMessage) *time.Time {
	if len(raw) == 0 {
		return nil
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil
	}
	return timestampValue(value)
}
