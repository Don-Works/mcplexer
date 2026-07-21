package codemode

import (
	"fmt"
	"strings"
)

// TextProjectionKind tags plain-text tool results projected for code mode.
// Agents inspect .text for the payload and .bytes for size — never
// Object.keys on a raw string (which yields numeric character indexes).
const TextProjectionKind = "text"

// textPreviewMax is how many bytes of a projected text value print() shows
// inline before relying on truncation notices for the rest.
const textPreviewMax = 240

// textProjectionEnvelopeMin is the byte threshold below which a text
// projection is returned as plain text instead of the {kind:text, bytes:N, preview:"..."} envelope.
// Outputs under 1 KB are common tool-result fragments (IDs, names, short messages)
// and the envelope just adds noise. Reserve the envelope for genuinely large or
// truncated outputs where the preview/hydration path adds value.
const textProjectionEnvelopeMin = 1024

// projectTextValue wraps a plain-text MCP tool result in a predictable
// object for code-mode consumption.
func projectTextValue(text string) map[string]any {
	return map[string]any{
		"kind":  TextProjectionKind,
		"text":  text,
		"bytes": len(text),
	}
}

// isTextProjection reports whether v is a projected plain-text value.
func isTextProjection(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	kind, _ := m["kind"].(string)
	return kind == TextProjectionKind
}

// formatTextProjection renders a projected text value compactly for print().
// Small outputs (under textProjectionEnvelopeMin) are returned as plain text
// so the agent sees exact values without extra parsing. The envelope is
// reserved for genuinely large or truncated outputs.
func formatTextProjection(m map[string]any) string {
	text, _ := m["text"].(string)
	bytes := len(text)
	if b, ok := m["bytes"].(int); ok {
		bytes = b
	} else if b, ok := m["bytes"].(float64); ok {
		bytes = int(b)
	}
	if bytes < textProjectionEnvelopeMin {
		return text
	}
	preview := compactTextPreview(text, textPreviewMax)
	return fmt.Sprintf(`{kind:text, bytes:%d, preview:%q}`, bytes, preview)
}

// compactTextPreview returns a single-line, length-capped preview of text.
func compactTextPreview(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	oneLine := strings.ReplaceAll(text, "\n", `\n`)
	oneLine = strings.ReplaceAll(oneLine, "\r", "")
	oneLine = strings.ReplaceAll(oneLine, "\t", ` `)
	if len(oneLine) <= maxBytes {
		return oneLine
	}
	return safeUTF8Prefix(oneLine, maxBytes) + "..."
}
