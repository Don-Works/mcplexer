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
// Defaults true: only the five keep-list tools appear in the static tools/list response.
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

// minifyToolSchemas strips true-noise metadata from each tool's InputSchema.
// Quality-first (2026-07): the old allowlist also dropped property
// descriptions, defaults, examples, and additionalProperties — all of which
// materially help a model call the tool correctly. Now only genuinely inert
// keys are removed; semantic content always survives.
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

// schemaNoiseKeys are the only keys minification removes: they carry no
// information a model uses to construct a call. "title" almost always
// restates the property name; "$schema"/"$id"/"$comment" are validator
// plumbing. Everything else — description, default, examples, enum, format,
// additionalProperties, numeric bounds — is load-bearing and preserved.
var schemaNoiseKeys = []string{"$schema", "$id", "$comment", "title"}

// minifySchema removes noise keys from a JSON schema, recursing into
// properties, items, and combinators. Structure and semantics are preserved
// exactly; on any parse surprise the input is returned untouched.
func minifySchema(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	minifySchemaObj(obj)
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

func minifySchemaObj(obj map[string]json.RawMessage) {
	for _, k := range schemaNoiseKeys {
		delete(obj, k)
	}
	if props, ok := obj["properties"]; ok {
		obj["properties"] = minifyProperties(props)
	}
	for _, k := range []string{"items", "additionalProperties", "not"} {
		if sub, ok := obj[k]; ok {
			obj[k] = minifySchema(sub)
		}
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		if list, ok := obj[k]; ok {
			obj[k] = minifySchemaList(list)
		}
	}
}

func minifyProperties(raw json.RawMessage) json.RawMessage {
	var props map[string]json.RawMessage
	if err := json.Unmarshal(raw, &props); err != nil {
		return raw
	}
	for name, propRaw := range props {
		props[name] = minifySchema(propRaw)
	}
	result, err := json.Marshal(props)
	if err != nil {
		return raw
	}
	return result
}

func minifySchemaList(raw json.RawMessage) json.RawMessage {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return raw
	}
	for i, sub := range list {
		list[i] = minifySchema(sub)
	}
	out, err := json.Marshal(list)
	if err != nil {
		return raw
	}
	return out
}
