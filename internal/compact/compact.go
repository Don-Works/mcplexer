package compact

import "encoding/json"

// Compactor compresses verbose MCP tool results into a token-efficient format.
type Compactor struct{}

// New creates a Compactor.
func New() *Compactor {
	return &Compactor{}
}

// CompactToolResult processes an MCP CallToolResult, compacting JSON in
// text content items. Returns the input unchanged if compaction isn't
// applicable (non-JSON text, errors, already compact).
func (c *Compactor) CompactToolResult(result json.RawMessage) json.RawMessage {
	var envelope struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result
	}
	if envelope.IsError {
		return result
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(result, &raw); err != nil {
		return result
	}

	contentRaw, ok := raw["content"]
	if !ok {
		return result
	}

	var items []map[string]any
	if err := json.Unmarshal(contentRaw, &items); err != nil {
		return result
	}

	changed := false
	for i, item := range items {
		typ, _ := item["type"].(string)
		if typ != "text" {
			continue
		}
		text, _ := item["text"].(string)
		if text == "" {
			continue
		}
		compacted := c.CompactJSON([]byte(text))
		if string(compacted) != text {
			items[i]["text"] = string(compacted)
			changed = true
		}
	}

	if !changed {
		return result
	}

	newContent, err := json.Marshal(items)
	if err != nil {
		return result
	}
	raw["content"] = newContent

	out, err := json.Marshal(raw)
	if err != nil {
		return result
	}
	return out
}

// CompactJSON compacts a raw JSON value. Arrays of objects get columnar
// treatment. Objects get null/empty pruning. Primitives pass through.
func (c *Compactor) CompactJSON(data []byte) []byte {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return data
	}

	result := compactValue(parsed)

	out, err := json.Marshal(result)
	if err != nil {
		return data
	}
	return out
}

func compactValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return PruneObject(val)
	case []any:
		return compactSlice(val)
	default:
		return v
	}
}

func compactSlice(items []any) any {
	maps := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			// Mixed types — prune nested objects, pass rest through.
			result := make([]any, len(items))
			for i, it := range items {
				if m, ok := it.(map[string]any); ok {
					result[i] = PruneObject(m)
				} else {
					result[i] = it
				}
			}
			return result
		}
		maps = append(maps, m)
	}
	return CompactArray(maps)
}
