package codemode

import (
	"encoding/json"

	"github.com/don-works/mcplexer/internal/compact"
)

// compactForSandbox strips nulls, empty values, and pagination metadata
// from a CallToolResult's text content before parsing into JS values.
func compactForSandbox(raw json.RawMessage) json.RawMessage {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw
	}

	var isError bool
	if rawIsError, ok := envelope["isError"]; ok {
		_ = json.Unmarshal(rawIsError, &isError)
	}
	if isError {
		return raw
	}

	rawContent, ok := envelope["content"]
	if !ok {
		return raw
	}
	var content []map[string]any
	if err := json.Unmarshal(rawContent, &content); err != nil {
		return raw
	}

	changed := false
	for i, item := range content {
		text, ok := item["text"].(string)
		if !ok || text == "" {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			continue
		}
		pruned := pruneSandboxValue(parsed)
		if pruned == nil {
			continue
		}
		data, err := json.Marshal(pruned)
		if err != nil {
			continue
		}
		if string(data) != text {
			content[i]["text"] = string(data)
			changed = true
		}
	}

	if !changed {
		return raw
	}

	contentRaw, err := json.Marshal(content)
	if err != nil {
		return raw
	}
	envelope["content"] = contentRaw
	out, err := json.Marshal(envelope)
	if err != nil {
		return raw
	}
	return out
}

// pruneSandboxValue applies PruneForSandbox to maps, recurses into slices.
func pruneSandboxValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return compact.PruneForSandbox(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			if m, ok := item.(map[string]any); ok {
				out[i] = compact.PruneForSandbox(m)
			} else {
				out[i] = item
			}
		}
		return out
	default:
		return v
	}
}
