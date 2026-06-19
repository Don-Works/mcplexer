package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func parseGrokJSON(raw []byte) (*SendResponse, error) {
	payload, ok := lastJSONPayload(raw)
	if !ok {
		return nil, fmt.Errorf("no JSON object in output")
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, err
	}
	if isTruthy(obj["is_error"]) || isTruthy(obj["error"]) {
		return nil, fmt.Errorf("%s", firstString(obj, "error", "message", "result", "text"))
	}
	resp := &SendResponse{
		Text:       strings.TrimSpace(grokText(obj)),
		CostUSD:    firstFloat(obj, "cost_usd", "total_cost_usd", "cost"),
		StopReason: normalizeGrokStop(firstString(obj, "stop_reason", "finish_reason", "reason")),
	}
	resp.InputTokens = grokInputTokens(obj)
	resp.OutputTokens = grokOutputTokens(obj)
	return resp, nil
}

func lastJSONPayload(raw []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false
	}
	if json.Valid(trimmed) {
		return trimmed, true
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' || !json.Valid(line) {
			continue
		}
		return line, true
	}
	return nil, false
}

func grokText(obj map[string]any) string {
	if s := firstString(obj, "result", "text", "output_text", "response", "content"); s != "" {
		return s
	}
	for _, key := range []string{"message", "output"} {
		if s := textFromAny(obj[key]); s != "" {
			return s
		}
	}
	return ""
}

func textFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if s := firstString(x, "text", "content", "result", "output_text"); s != "" {
			return s
		}
	case []any:
		var parts []string
		for _, item := range x {
			if s := textFromAny(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

func grokInputTokens(obj map[string]any) int {
	usage, _ := obj["usage"].(map[string]any)
	tokens, _ := obj["tokens"].(map[string]any)
	cache, _ := tokens["cache"].(map[string]any)
	return intFromAny(usage["input_tokens"]) +
		intFromAny(usage["cache_read_input_tokens"]) +
		intFromAny(usage["cache_creation_input_tokens"]) +
		intFromAny(tokens["input"]) +
		intFromAny(cache["read"]) +
		intFromAny(cache["write"])
}

func grokOutputTokens(obj map[string]any) int {
	usage, _ := obj["usage"].(map[string]any)
	tokens, _ := obj["tokens"].(map[string]any)
	return intFromAny(usage["output_tokens"]) + intFromAny(tokens["output"])
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := obj[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func firstFloat(obj map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch v := obj[key].(type) {
		case float64:
			return v
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		}
	}
	return 0
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func isTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	default:
		return false
	}
}

func normalizeGrokStop(s string) string {
	switch s {
	case "stop", "end_turn", "":
		return StopEndTurn
	case "tool_use", "tool-calls", "tool_calls":
		return StopToolUse
	case "max_tokens", "length":
		return StopMaxTokens
	case "stop_sequence":
		return StopStopSequence
	default:
		return StopOther
	}
}
