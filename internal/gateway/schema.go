package gateway

import (
	"encoding/json"
	"os"
	"strings"
)

// slimToolsEnabled returns true unless MCPLEXER_SLIM_TOOLS is explicitly "false".
func slimToolsEnabled() bool {
	return strings.ToLower(os.Getenv("MCPLEXER_SLIM_TOOLS")) != "false"
}

// slimSurfaceEnvEnabled returns true unless MCPLEXER_SLIM_SURFACE is explicitly "false".
// Defaults true: only the 4 keep-list tools appear in the static tools/list response.
func slimSurfaceEnvEnabled() bool {
	return strings.ToLower(os.Getenv("MCPLEXER_SLIM_SURFACE")) != "false"
}

// pureModeEnvEnabled returns true when MCPLEXER_PURE_MODE is explicitly
// truthy. Unset/false/0 leave the gateway's baseline behavior intact.
func pureModeEnvEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCPLEXER_PURE_MODE"))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// slimSurfaceKeepers is the hand-picked set of built-in tool names that
// remain in the static tools/list response when SlimSurface is on.
// Everything else mcplexer-namespaced moves to searchableBuiltins —
// discoverable via mcpx__search_tools, callable via mcpx__execute_code
// or direct dispatch, but absent from the agent's top-level inventory.
var slimSurfaceKeepers = map[string]struct{}{
	"mcpx__execute_code": {},
	"mcpx__search_tools": {},
	"secret__prompt":     {},
	"secret__list_refs":  {},
	// mcpx__retrieve must stay top-level visible even under the slim surface:
	// when a tool result carries a CCR marker, the model has to be able to
	// call retrieve to expand it. Hiding it behind discovery would strand
	// markers it can't resolve.
	"mcpx__retrieve": {},
}

// isSlimSurfaceKeeper reports whether the named tool should remain in
// the static tools/list response under SlimSurface mode.
func isSlimSurfaceKeeper(name string) bool {
	_, ok := slimSurfaceKeepers[name]
	return ok
}

// minifyToolSchemas strips non-essential metadata from each tool's InputSchema
// to reduce context window consumption. Preserves type structure and constraints
// but removes property descriptions, defaults, examples, and other noise.
func minifyToolSchemas(tools []Tool) []Tool {
	out := make([]Tool, len(tools))
	for i, t := range tools {
		out[i] = t
		if len(t.InputSchema) > 0 {
			out[i].InputSchema = minifySchema(t.InputSchema)
		}
	}
	return out
}

// minifySchema strips non-essential fields from a JSON schema.
func minifySchema(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}

	stripTopLevel(obj)

	if props, ok := obj["properties"]; ok {
		obj["properties"] = minifyProperties(props)
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// stripTopLevel removes non-essential top-level schema fields.
func stripTopLevel(obj map[string]json.RawMessage) {
	delete(obj, "description")
	delete(obj, "additionalProperties")
	delete(obj, "examples")
	delete(obj, "default")
	delete(obj, "title")
	delete(obj, "$schema")
}

// keysToKeep is the set of property-level keys to preserve in minification.
var keysToKeep = map[string]bool{
	"type": true, "properties": true, "required": true,
	"enum": true, "items": true, "const": true,
	"oneOf": true, "anyOf": true, "allOf": true,
	"minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "pattern": true,
}

// minifyProperties strips descriptions and other noise from each property.
func minifyProperties(raw json.RawMessage) json.RawMessage {
	var props map[string]json.RawMessage
	if err := json.Unmarshal(raw, &props); err != nil {
		return raw
	}

	for name, propRaw := range props {
		var prop map[string]json.RawMessage
		if err := json.Unmarshal(propRaw, &prop); err != nil {
			continue
		}

		cleaned := make(map[string]json.RawMessage, len(prop))
		for k, v := range prop {
			if keysToKeep[k] {
				cleaned[k] = v
			}
		}

		// Recurse into nested object properties.
		if nested, ok := cleaned["properties"]; ok {
			cleaned["properties"] = minifyProperties(nested)
		}

		// Recurse into items for arrays.
		if items, ok := cleaned["items"]; ok {
			cleaned["items"] = minifySchema(items)
		}

		out, err := json.Marshal(cleaned)
		if err != nil {
			continue
		}
		props[name] = out
	}

	result, err := json.Marshal(props)
	if err != nil {
		return raw
	}
	return result
}
