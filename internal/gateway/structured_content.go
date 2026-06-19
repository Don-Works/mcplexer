package gateway

import (
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/codemode"
)

// surfaceStructuredContent walks the CallToolResult and, when exactly
// one text content block is present whose text JSON-parses cleanly,
// populates the top-level `structuredContent` field with the parsed
// object. The text content stays intact for backward-compat with
// clients that haven't implemented structuredContent — the calling LLM
// can prefer the parsed object when present and fall back to parsing
// text otherwise.
//
// This is the MCP-spec-blessed way to ship JSON payloads: the calling
// LLM gets the parsed object directly instead of having to JSON.parse
// an escaped-string-inside-JSON-inside-JSON. Drops a meaningful chunk
// of overhead on tool calls whose primary output is a JSON document
// (most task__*/memory__*/mcpx__* results, plus any downstream tool
// that returns JSON).
//
// Returns the result unchanged when:
//   - the input is not a CallToolResult envelope (any unmarshal error)
//   - the result is an isError envelope (preserve error shape verbatim)
//   - structuredContent is already populated (don't overwrite)
//   - content[] has zero or >1 text blocks (ambiguous which to lift)
//   - the single text block is wrapped in <untrusted-content> (text
//     comes wrapped, no structured payload available)
//   - the text doesn't JSON-parse (it's prose, not structured)
//   - the parsed value is a plain string or scalar (lifting adds no
//     value and risks double-decoding)
//   - the text or lifted structured payload exceeds the Code Mode
//     output cap (avoid doubling very large tool results)
func surfaceStructuredContent(result json.RawMessage) json.RawMessage {
	if len(result) == 0 {
		return result
	}

	// Decode into a generic envelope first so we can check whether
	// structuredContent is already set without round-tripping through
	// the typed CallToolResult (which would lose unknown fields and
	// drop them on re-marshal).
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result
	}

	if _, alreadySet := envelope["structuredContent"]; alreadySet {
		return result
	}

	if isErr := envelopeIsError(envelope); isErr {
		return result
	}

	rawContent, ok := envelope["content"]
	if !ok {
		return result
	}

	var content []map[string]json.RawMessage
	if err := json.Unmarshal(rawContent, &content); err != nil {
		return result
	}
	if len(content) != 1 {
		return result
	}

	rawType, ok := content[0]["type"]
	if !ok {
		return result
	}
	var typ string
	if err := json.Unmarshal(rawType, &typ); err != nil || typ != "text" {
		return result
	}

	rawText, ok := content[0]["text"]
	if !ok {
		return result
	}
	var text string
	if err := json.Unmarshal(rawText, &text); err != nil {
		return result
	}
	if text == "" {
		return result
	}

	// Don't unwrap envelopes — the marker is load-bearing.
	if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "<untrusted-content") {
		return result
	}
	if len(text) > codemode.DefaultMaxOutputBytes {
		return result
	}

	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return result // prose, not structured — nothing to lift
	}

	// Only lift containers (objects / arrays). Scalars (strings, numbers,
	// bools, null) parsed from text would just round-trip without value
	// and could confuse clients that treat a structuredContent string as
	// a parsed-by-mistake URL.
	switch parsed.(type) {
	case map[string]any, []any:
	default:
		return result
	}

	structured, err := json.Marshal(parsed)
	if err != nil {
		return result
	}
	if len(structured) > codemode.DefaultMaxOutputBytes {
		return result
	}
	envelope["structuredContent"] = structured

	out, err := json.Marshal(envelope)
	if err != nil {
		return result
	}
	return out
}

// envelopeIsError reports whether the envelope carries isError=true. A
// missing or false isError counts as "not an error" and we proceed with
// lifting structured content. We deliberately preserve error envelopes
// verbatim — clients sometimes pattern-match on the exact error shape.
func envelopeIsError(envelope map[string]json.RawMessage) bool {
	raw, ok := envelope["isError"]
	if !ok {
		return false
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return v
}
