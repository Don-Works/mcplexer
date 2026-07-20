package gateway

import (
	"encoding/json"
	"strings"
)

// coerceStringifiedArgs walks the top-level keys of a JSON object and
// converts string values that look like JSON objects or arrays into their
// parsed form. LLMs frequently stringify nested objects (e.g. passing
// `"filters": "{\"key\": \"value\"}"` instead of `"filters": {"key": "value"}`),
// which causes downstream schema validation to fail.
//
// stringFields is an optional set of top-level field names that the
// downstream tool's input schema declares as type: "string". Those fields
// are left alone even if their value looks like JSON — auto-parsing them
// would break tools that genuinely want the string form (e.g. Excalidraw's
// create_view, whose `elements` field is a JSON-array-string by contract).
// Pass nil when no schema is available — falls back to the legacy
// coerce-everything behaviour.
func coerceStringifiedArgs(raw json.RawMessage, stringFields map[string]bool) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return raw
	}

	changed := false
	for key, val := range args {
		if stringFields[key] {
			continue
		}
		if len(val) < 2 {
			continue
		}
		// Only process JSON string values (starts with `"`).
		if val[0] != '"' {
			continue
		}

		var s string
		if err := json.Unmarshal(val, &s); err != nil {
			continue
		}

		// Check if the string content looks like a JSON object or array.
		if len(s) < 2 {
			continue
		}
		first := s[0]
		if first != '{' && first != '[' {
			continue
		}

		// Validate it's actually valid JSON before replacing.
		if !json.Valid([]byte(s)) {
			continue
		}

		args[key] = json.RawMessage(s)
		changed = true
	}

	if !changed {
		return raw
	}

	out, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return out
}

// stringFieldsFromInputSchema parses a JSON-Schema-shaped inputSchema and
// returns the set of top-level property names whose `type` is exactly
// "string". Used by coerceStringifiedArgs to skip auto-parsing fields the
// downstream tool genuinely wants as strings. Returns nil if schema is
// empty/invalid — the coercer then treats every field as coercible (legacy
// behaviour, safe default).
func stringFieldsFromInputSchema(schema json.RawMessage) map[string]bool {
	if len(schema) == 0 {
		return nil
	}
	var parsed struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil
	}
	if len(parsed.Properties) == 0 {
		return nil
	}
	out := make(map[string]bool, len(parsed.Properties))
	for name, prop := range parsed.Properties {
		switch t := prop.Type.(type) {
		case string:
			if t == "string" {
				out[name] = true
			}
		case []any:
			// JSON-Schema allows `type: ["string", "null"]`. Treat as string
			// only when "string" is the sole non-null option.
			seenString := false
			otherNonNull := false
			for _, v := range t {
				s, ok := v.(string)
				if !ok {
					continue
				}
				if s == "string" {
					seenString = true
				} else if s != "null" {
					otherNonNull = true
				}
			}
			if seenString && !otherNonNull {
				out[name] = true
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// builtinToolFullName reconstructs the fully namespaced builtin tool name
// from the synthetic downstream server ID and the post-namespace remainder
// that extractOriginalToolName produces at the dispatch site. Builtin server
// IDs are "<namespace>-builtin" and their tool definitions are named
// "<namespace>__<tool>", so "mcpx-builtin" + "delegate_worker" resolves to
// "mcpx__delegate_worker". Returns "" when either part is missing, or when
// serverID is not a builtin ID.
func builtinToolFullName(serverID, originalToolName string) string {
	if originalToolName == "" {
		return ""
	}
	ns, ok := strings.CutSuffix(serverID, "-builtin")
	if !ok || ns == "" {
		return ""
	}
	// Already namespaced (the caller passed a full name) — use it as-is.
	if strings.Contains(originalToolName, "__") {
		return originalToolName
	}
	return ns + "__" + originalToolName
}

// stringFieldsForBuiltinTool resolves the type: "string" top-level fields for
// a builtin tool. Builtin tools never appear in the downstream tools/list
// catalog — their schemas live in-process (delegationToolDefinitions and
// friends) — so schema-aware coercion has to resolve against those
// definitions instead. Without this, every builtin dispatch fell back to the
// nil-schema legacy path and re-parsed any string argument beginning with
// '[' or '{', corrupting string-typed fields such as delegate_worker's
// tool_allowlist_json (a JSON array carried as text, by contract).
//
// fullToolName must be the fully namespaced name; matching is exact so that
// same-suffix tools in different namespaces (mesh__send vs email__send) can
// never resolve to each other's schema. Returns nil when the tool is unknown
// or declares no string fields — the caller then keeps legacy behaviour.
func stringFieldsForBuiltinTool(tools []Tool, fullToolName string) map[string]bool {
	if fullToolName == "" {
		return nil
	}
	for _, t := range tools {
		if t.Name == fullToolName {
			return stringFieldsFromInputSchema(t.InputSchema)
		}
	}
	return nil
}
