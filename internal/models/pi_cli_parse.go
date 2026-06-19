package models

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// piCLIContentBlock is one element of an assistant message's content
// array. Only the text-type blocks contribute to the final reply; tool
// calls are observability-only (mcplexer's own dispatcher owns tools).
type piCLIContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// piCLIUsage is Pi's per-message token accounting.
type piCLIUsage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cacheRead"`
	CacheWrite  int `json:"cacheWrite"`
	TotalTokens int `json:"totalTokens"`
}

// piCLIMessage is an assistant/user message as Pi serialises it — whether
// it appears as a flat top-level object or nested under a stream
// envelope's "message"/"messages" field.
type piCLIMessage struct {
	Role       string              `json:"role"`
	Content    []piCLIContentBlock `json:"content"`
	Usage      piCLIUsage          `json:"usage"`
	StopReason string              `json:"stopReason"`
	ResponseID string              `json:"responseId"`
	Model      string              `json:"model"`
	Provider   string              `json:"provider"`
}

// piCLILine is one JSONL line from `pi --mode json`. Real Pi wraps messages
// in stream envelopes — {"type":"message_start|message_update|message_end",
// "message":{...}} per turn, plus a terminal {"type":"agent_end",
// "messages":[...]} — and may also emit a top-level {"type":"error",...}.
// We also accept a flat top-level assistant object (defensive, and what the
// hermetic fixtures use). The embedded piCLIMessage captures the flat shape;
// Message/Messages capture the enveloped shape.
type piCLILine struct {
	Type  string `json:"type"`
	Error string `json:"error"`
	piCLIMessage
	Message  *piCLIMessage  `json:"message"`
	Messages []piCLIMessage `json:"messages"`
}

// parsePiJSON walks the JSONL stream emitted by `pi --mode json` and
// collapses it into a SendResponse.
//
//   - Assistant messages are harvested from whichever shape Pi uses: a flat
//     top-level object, a stream envelope's "message", or the terminal
//     "agent_end" "messages" array.
//   - Only assistant messages carrying a NON-EMPTY stopReason are counted.
//   - They are DEDUPED by responseId (insertion-ordered, last write wins), so
//     the message_start/message_update/message_end/agent_end re-emits of one
//     turn collapse to a single entry with that turn's final usage — never
//     double-counted.
//   - InputTokens/OutputTokens sum usage across the deduped turns.
//   - Text is the concatenation of the text-type content blocks of the LAST
//     deduped assistant message.
//   - CostUSD stays 0 (local/unknown-priced); the caller falls back to its
//     own estimate.
func parsePiJSON(raw []byte) (*SendResponse, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("pi_cli: empty output")
	}

	order := make([]string, 0, 8)
	byID := make(map[string]piCLIMessage)
	synthetic := 0
	sawAny := false

	add := func(m piCLIMessage) {
		if m.Role != "assistant" || strings.TrimSpace(m.StopReason) == "" {
			return
		}
		key := m.ResponseID
		if key == "" {
			synthetic++
			key = fmt.Sprintf("\x00synthetic-%d", synthetic)
		}
		if _, seen := byID[key]; !seen {
			order = append(order, key)
		}
		byID[key] = m
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var l piCLILine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			continue
		}
		sawAny = true
		// Surface an explicit error line, mirroring the other CLI parsers.
		if l.Type == "error" || l.Error != "" {
			errMsg := l.Error
			if errMsg == "" {
				errMsg = "pi_cli: unspecified error"
			}
			return nil, fmt.Errorf("pi_cli: error: %s", errMsg)
		}
		add(l.piCLIMessage) // flat top-level assistant object
		if l.Message != nil {
			add(*l.Message) // stream-envelope payload
		}
		for _, m := range l.Messages {
			add(m) // agent_end messages array
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawAny {
		return nil, fmt.Errorf("pi_cli: no JSON object in output")
	}

	resp := &SendResponse{StopReason: StopEndTurn}
	var lastFinal *piCLIMessage
	for _, key := range order {
		m := byID[key]
		resp.InputTokens += m.Usage.Input
		resp.OutputTokens += m.Usage.Output
		mm := m
		lastFinal = &mm
	}
	if lastFinal != nil {
		resp.Text = strings.TrimSpace(piMessageText(*lastFinal))
		resp.StopReason = normalizePiStop(lastFinal.StopReason)
	}
	return resp, nil
}

// piMessageText concatenates the text-type content blocks of an assistant
// message, dropping toolCall and other non-text blocks.
func piMessageText(m piCLIMessage) string {
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Type == "text" && blk.Text != "" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func normalizePiStop(s string) string {
	switch s {
	case "stop", "end_turn", "":
		return StopEndTurn
	case "toolUse", "tool_use", "tool-calls", "tool_calls":
		return StopToolUse
	case "maxTokens", "max_tokens", "length":
		return StopMaxTokens
	case "stopSequence", "stop_sequence":
		return StopStopSequence
	default:
		return StopOther
	}
}
