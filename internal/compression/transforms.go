package compression

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DefaultTransforms returns the transforms registered by default at the
// gateway seam. New transforms are added here as they pass the gimmick-gate
// harness; each starts life measured in shadow before being flipped on.
func DefaultTransforms() []Transform {
	return []Transform{jsonMinify{}}
}

// TransformInfo is the static descriptor the dashboard uses to render a toggle
// for every transform (even ones with no measured traffic yet).
type TransformInfo struct {
	Name     string `json:"name"`
	Lossless bool   `json:"lossless"`
}

// DefaultTransformInfo lists the default transforms' names + lossless flags so
// the settings UI can show a toggle per transform before any data exists.
func DefaultTransformInfo() []TransformInfo {
	ts := DefaultTransforms()
	out := make([]TransformInfo, 0, len(ts))
	for _, t := range ts {
		out = append(out, TransformInfo{Name: t.Name(), Lossless: t.Lossless()})
	}
	return out
}

// jsonMinify re-encodes JSON text content blocks in their most compact form
// (no insignificant whitespace). The JSON value is preserved exactly, so the
// model parses identical data from fewer bytes — value-lossless. A no-op on
// already-compact JSON and on non-JSON text.
type jsonMinify struct{}

func (jsonMinify) Name() string   { return "json_minify" }
func (jsonMinify) Lossless() bool { return true }

func (jsonMinify) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return walkTextBlocks(result, func(text string) (string, bool) {
		trimmed := strings.TrimSpace(text)
		if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
			return text, false
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(text)); err != nil {
			return text, false // not valid JSON — leave untouched
		}
		if buf.Len() >= len(text) {
			return text, false // already compact
		}
		return buf.String(), true
	})
}

// walkTextBlocks applies fn to every text content block in an MCP tool-result
// envelope and returns the possibly-rewritten envelope plus whether anything
// changed. On any structural surprise it returns the input unchanged — a
// transform must never corrupt a result. Non-content envelope fields are
// preserved verbatim (their json.RawMessage bytes are re-emitted as-is;
// object key order may change, which is JSON-insignificant).
func walkTextBlocks(result json.RawMessage, fn func(text string) (string, bool)) (json.RawMessage, bool) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(result, &env); err != nil {
		return result, false
	}
	rawContent, ok := env["content"]
	if !ok {
		return result, false
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(rawContent, &content); err != nil {
		return result, false
	}
	changed := false
	for i, block := range content {
		var typ string
		if err := json.Unmarshal(block["type"], &typ); err != nil || typ != "text" {
			continue
		}
		var text string
		if err := json.Unmarshal(block["text"], &text); err != nil {
			continue
		}
		newText, ok := fn(text)
		if !ok || newText == text {
			continue
		}
		encoded, err := json.Marshal(newText)
		if err != nil {
			continue
		}
		block["text"] = encoded
		content[i] = block
		changed = true
	}
	if !changed {
		return result, false
	}
	newContent, err := json.Marshal(content)
	if err != nil {
		return result, false
	}
	env["content"] = newContent
	out, err := json.Marshal(env)
	if err != nil {
		return result, false
	}
	return out, true
}
