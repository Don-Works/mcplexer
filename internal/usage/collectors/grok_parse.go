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
	window := grokWeeklyWindow(config)
	if window == nil {
		return nil
	}
	return []store.UsageWindow{*window}
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
		ID: "grok_weekly_pool", Label: "Weekly shared pool",
		Unit: store.UnitPercent, DurationMinutes: duration, ResetsAt: reset,
	}
	if hasPercent {
		window.UsedPercent = usedPercent
	}
	return &window
}

func grokOptionalPercent(raw json.RawMessage) (*float64, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return numericValue(value)
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
