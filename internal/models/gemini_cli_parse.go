package models

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseGeminiJSON parses the output from `gemini --output-format json`.
// The Gemini CLI outputs a JSON object with fields like "response",
// "text", "result", or nested "message.content" structures. Token
// usage may appear under "usage" or "tokens" keys. When absent,
// mcplexer records token/cost metrics as zero rather than inventing values.
func parseGeminiJSON(raw []byte) (*SendResponse, error) {
	payload, ok := lastJSONPayload(raw)
	if !ok {
		return nil, fmt.Errorf("no JSON object in output")
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, err
	}
	if isTruthy(obj["is_error"]) || isTruthy(obj["error"]) {
		errMsg := firstString(obj, "error", "message", "result", "text")
		if errMsg == "" {
			errMsg = "gemini_cli: unspecified error"
		}
		return nil, fmt.Errorf("%s", errMsg)
	}
	resp := &SendResponse{
		Text:       strings.TrimSpace(geminiText(obj)),
		CostUSD:    firstFloat(obj, "cost_usd", "total_cost_usd", "cost"),
		StopReason: normalizeGeminiStop(firstString(obj, "stop_reason", "finish_reason", "reason")),
	}
	resp.InputTokens = geminiInputTokens(obj)
	resp.OutputTokens = geminiOutputTokens(obj)
	return resp, nil
}

func geminiText(obj map[string]any) string {
	if s := firstString(obj, "response", "result", "text", "output_text", "content"); s != "" {
		return s
	}
	for _, key := range []string{"message", "output"} {
		if s := textFromAny(obj[key]); s != "" {
			return s
		}
	}
	return ""
}

func geminiInputTokens(obj map[string]any) int {
	usage, _ := obj["usage"].(map[string]any)
	tokens, _ := obj["tokens"].(map[string]any)
	cache, _ := tokens["cache"].(map[string]any)
	return intFromAny(usage["input_tokens"]) +
		intFromAny(usage["prompt_tokens"]) +
		intFromAny(usage["cache_read_input_tokens"]) +
		intFromAny(usage["cache_creation_input_tokens"]) +
		intFromAny(tokens["input"]) +
		intFromAny(cache["read"]) +
		intFromAny(cache["write"])
}

func geminiOutputTokens(obj map[string]any) int {
	usage, _ := obj["usage"].(map[string]any)
	tokens, _ := obj["tokens"].(map[string]any)
	return intFromAny(usage["output_tokens"]) +
		intFromAny(usage["completion_tokens"]) +
		intFromAny(tokens["output"])
}

func normalizeGeminiStop(s string) string {
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
