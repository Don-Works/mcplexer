package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// codexCLIEvent is the JSON envelope emitted by `codex --format json`.
// Only fields we consume are declared; codex may add more without
// breaking decode.
type codexCLIEvent struct {
	Type string `json:"type"`
	// Flat envelope fields (codex >= 0.2 style)
	Text         string  `json:"text"`
	Result       string  `json:"result"`
	StopReason   string  `json:"stop_reason"`
	IsError      bool    `json:"is_error"`
	Error        string  `json:"error"`
	CostUSD      float64 `json:"cost_usd"`
	OutputTokens int     `json:"output_tokens"`
	// Nested usage object (codex >= 0.3 / structured output)
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		TotalTokens              int `json:"total_tokens"`
	} `json:"usage"`
	// Nested message object (alternative envelope shape)
	Message struct {
		Content    string `json:"content"`
		StopReason string `json:"stop_reason"`
	} `json:"message"`
	// Nested tokens object (another common shape)
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
}

// ({"type":"turn.failed","error":{"message":"…"}}), while the legacy flat
// envelope sends `message` as an object and `error` as a string. One struct
// cannot decode both, which is why the legacy shape stays where it is.
type codexStreamEvent struct {
	Type string `json:"type"`
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
	} `json:"item"`
	Usage struct {
		InputTokens           int `json:"input_tokens"`
		CachedInputTokens     int `json:"cached_input_tokens"`
		OutputTokens          int `json:"output_tokens"`
		ReasoningOutputTokens int `json:"reasoning_output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// parseCodexEventStream decodes the `codex exec --json` JSONL stream.
// Reports handled=false when the output carries no recognizable stream event,
// so the caller can fall back to the legacy flat envelope.
//
// Failure semantics matter here: a run emits transient
// {"type":"error","message":"Reconnecting… 2/5"} events and can still finish
// successfully, so a bare error event must NOT abort the parse. Only
// `turn.failed` is authoritative; loose error events are reported solely when
// the turn produced neither text nor a completion.
func parseCodexEventStream(raw []byte) (resp *SendResponse, handled bool, err error) {
	var acc codexStreamAccumulator
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev codexStreamEvent
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
			continue
		}
		acc.fold(ev)
	}
	return acc.result()
}

// codexStreamAccumulator folds the JSONL events of one run into a reply.
type codexStreamAccumulator struct {
	text          string
	lastError     string
	turnFailure   string
	inputTokens   int
	outputTokens  int
	sawEvent      bool
	sawCompletion bool
}

func (a *codexStreamAccumulator) fold(ev codexStreamEvent) {
	switch ev.Type {
	case "item.completed":
		a.sawEvent = true
		switch ev.Item.Type {
		case "agent_message":
			// Multiple assistant messages in one turn concatenate.
			if ev.Item.Text != "" {
				if a.text != "" {
					a.text += "\n"
				}
				a.text += ev.Item.Text
			}
		case "error":
			a.lastError = ev.Item.Message
		}
	case "turn.completed":
		a.sawEvent, a.sawCompletion = true, true
		// cached_input_tokens is the cache-hit PORTION of input_tokens, and
		// reasoning_output_tokens the reasoning portion of output_tokens.
		// Both are SUBSETS, never addends — this matches the documented
		// OpenAI usage semantics codex reports against
		// (input_tokens_details.cached_tokens ⊆ input_tokens;
		// completion_tokens_details.reasoning_tokens ⊆ output_tokens), and is
		// independently consistent with a captured live run reporting 13635
		// input / 9984 cached for a one-line prompt.
		//
		// The pre-0.144 code summed input + cache_read + cache_creation. Under
		// this schema that reports 23619 for the run above — input inflated
		// ~73%, and every cost figure derived from it likewise.
		a.inputTokens, a.outputTokens = ev.Usage.InputTokens, ev.Usage.OutputTokens
	case "turn.failed":
		a.sawEvent = true
		a.turnFailure = ev.Error.Message
	case "error":
		a.sawEvent = true
		a.lastError = ev.Message
	case "thread.started", "turn.started":
		a.sawEvent = true
	}
}

func (a *codexStreamAccumulator) result() (*SendResponse, bool, error) {
	if !a.sawEvent {
		return nil, false, nil
	}
	if a.turnFailure != "" {
		return nil, true, fmt.Errorf("codex_cli: turn failed: %s", a.turnFailure)
	}
	if a.text == "" && !a.sawCompletion {
		reason := a.lastError
		if reason == "" {
			reason = "stream ended with no assistant message"
		}
		return nil, true, fmt.Errorf("codex_cli: %s", reason)
	}
	return &SendResponse{
		Text:         strings.TrimSpace(a.text),
		InputTokens:  a.inputTokens,
		OutputTokens: a.outputTokens,
		StopReason:   StopEndTurn,
	}, true, nil
}

// decodeCodexLegacyEnvelope finds the flat result envelope emitted by codex
// builds older than 0.144. Codex may emit NDJSON, so it walks lines looking
// for the result envelope and skips banners and non-JSON prefix lines.
func decodeCodexLegacyEnvelope(raw []byte) (codexCLIEvent, error) {
	var env codexCLIEvent
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		// Accept the first parseable JSON object that looks like a
		// result (has text/result/message or is an error).
		if env.Text != "" || env.Result != "" || env.Message.Content != "" ||
			env.IsError || env.Error != "" || env.Usage.OutputTokens > 0 || env.OutputTokens > 0 {
			return env, nil
		}
	}
	// No recognisable envelope found — try the whole blob as one JSON
	// object (single-envelope mode).
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, fmt.Errorf("codex_cli: no JSON envelope in output")
	}
	return env, nil
}

func parseCodexJSON(raw []byte) (*SendResponse, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("codex_cli: empty output")
	}
	// codex >= 0.144 emits a JSONL event stream. Older builds emitted a flat
	// envelope, which the code below still handles.
	if resp, handled, err := parseCodexEventStream(raw); handled {
		return resp, err
	}
	env, err := decodeCodexLegacyEnvelope(raw)
	if err != nil {
		return nil, err
	}

	if env.IsError || env.Error != "" {
		errMsg := env.Error
		if errMsg == "" {
			errMsg = "unknown error"
		}
		return nil, fmt.Errorf("codex_cli: error: %s", errMsg)
	}

	text := env.Text
	if text == "" {
		text = env.Result
	}
	if text == "" {
		text = env.Message.Content
	}

	inputTokens := env.Usage.InputTokens + env.Usage.CacheReadInputTokens + env.Usage.CacheCreationInputTokens
	outputTokens := env.Usage.OutputTokens
	if outputTokens == 0 {
		outputTokens = env.OutputTokens
	}
	if outputTokens == 0 {
		outputTokens = env.Tokens.Output
	}
	if inputTokens == 0 {
		inputTokens = env.Tokens.Input
	}

	stopReason := env.StopReason
	if stopReason == "" {
		stopReason = env.Message.StopReason
	}

	return &SendResponse{
		Text:         strings.TrimSpace(text),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostUSD:      env.CostUSD,
		StopReason:   normalizeCodexStop(stopReason),
	}, nil
}

func normalizeCodexStop(s string) string {
	switch s {
	case "stop", "end_turn":
		return StopEndTurn
	case "tool_use", "tool_use_end", "tool_calls":
		return StopToolUse
	case "max_tokens", "length":
		return StopMaxTokens
	case "stop_sequence":
		return StopStopSequence
	default:
		return StopOther
	}
}
