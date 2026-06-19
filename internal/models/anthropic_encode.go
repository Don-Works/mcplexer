package models

// anthropicMessages converts common Messages into Anthropic's message array.
// Adjacent tool-result Messages are folded into a single user message with
// multiple tool_result content blocks (Anthropic requires this shape).
func anthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, anthropicMessage{
				Role:    "user",
				Content: []anthropicContentBlock{{Type: "text", Text: m.Content}},
			})
		case RoleAssistant:
			out = append(out, anthropicMessage{
				Role:    "assistant",
				Content: anthropicAssistantContent(m),
			})
		case RoleTool:
			out = appendAnthropicToolResult(out, m)
		}
	}
	return out
}

// anthropicAssistantContent builds the content blocks for an assistant turn,
// emitting text first (if any) followed by tool_use blocks.
func anthropicAssistantContent(m Message) []anthropicContentBlock {
	blocks := make([]anthropicContentBlock, 0, 1+len(m.ToolCalls))
	if m.Content != "" {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, anthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: tc.Input,
		})
	}
	return blocks
}

// appendAnthropicToolResult appends a tool-result block to the last user
// message if the previous turn was already a user/tool-result pair;
// otherwise it starts a new user message containing the result block.
func appendAnthropicToolResult(out []anthropicMessage, m Message) []anthropicMessage {
	block := anthropicContentBlock{
		Type:      "tool_result",
		ToolUseID: m.ToolUseID,
		Content:   m.ToolResult,
	}
	if n := len(out); n > 0 && out[n-1].Role == "user" && isAllToolResults(out[n-1].Content) {
		out[n-1].Content = append(out[n-1].Content, block)
		return out
	}
	return append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{block}})
}

func isAllToolResults(blocks []anthropicContentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// anthropicTools maps common ToolSchemas, marking the LAST tool with an
// ephemeral cache_control breakpoint so the whole tool array is cached.
func anthropicTools(tools []ToolSchema) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, len(tools))
	for i, t := range tools {
		out[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	out[len(out)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	return out
}

// decodeAnthropicResponse extracts text + tool calls + token usage from a
// parsed Anthropic response.
func decodeAnthropicResponse(parsed *anthropicResponse) *SendResponse {
	resp := &SendResponse{
		StopReason:   normalizeAnthropicStop(parsed.StopReason),
		InputTokens:  parsed.Usage.InputTokens + parsed.Usage.CacheReadInputTokens + parsed.Usage.CacheCreationInputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
	}
	for _, blk := range parsed.Content {
		switch blk.Type {
		case "text":
			if resp.Text == "" {
				resp.Text = blk.Text
			} else {
				resp.Text += blk.Text
			}
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:    blk.ID,
				Name:  blk.Name,
				Input: blk.Input,
			})
		}
	}
	return resp
}

func normalizeAnthropicStop(s string) string {
	switch s {
	case "end_turn":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	case "stop_sequence":
		return StopStopSequence
	default:
		return StopOther
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
