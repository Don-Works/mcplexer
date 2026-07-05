package compression

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// DefaultTransforms returns the transforms registered by default at the
// gateway seam, in evaluation order. New transforms are added here as they
// pass the gimmick-gate harness; each starts life measured in shadow before
// being flipped on. Ordering is load-bearing: terminal cleanups run first so
// later transforms see clean text; base64 externalization runs before the
// blunt oversize truncation so blobs get precise markers.
func DefaultTransforms() []Transform {
	return []Transform{
		jsonMinify{},
		ansiStrip{},
		crCollapse{},
		repeatCollapse{},
		structuredDedup{},
		logCompressor{},
		base64Externalize{},
		oversizeTruncate{},
	}
}

// oversize truncation window. A single text content block larger than the
// threshold is replaced with its head + a CCR marker + its tail; the full
// original is stashed so mcpx__retrieve returns the exact bytes. Big MCP tool
// outputs (DOM dumps, large logs/JSON) are the target of the biggest wins.
const (
	oversizeThresholdBytes = 8 * 1024
	oversizeHeadBytes      = 2 * 1024
	oversizeTailBytes      = 1 * 1024
)

// oversizeTruncate is a lossy-but-recoverable transform: it only ever drops
// content it has stashed in CCR, so from the model's perspective nothing is
// lost (it can expand the marker on demand). It runs only in On mode with CCR
// backing; the plain Apply path is a deliberate no-op so a caller that can't
// persist never drops anything.
type oversizeTruncate struct{}

func (oversizeTruncate) Name() string   { return "oversize_truncate" }
func (oversizeTruncate) Lossless() bool { return false }

func (oversizeTruncate) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (oversizeTruncate) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	var stash [][]byte
	out, changed := walkTextBlocks(result, func(text string) (string, bool) {
		if len(text) <= oversizeThresholdBytes {
			return text, false
		}
		original := []byte(text)
		stash = append(stash, original)
		headEnd := runeSafeEnd(text, oversizeHeadBytes)
		tailStart := runeSafeStart(text, len(text)-oversizeTailBytes)
		headEnd, tailStart = adjustCutForMarkers(text, headEnd, tailStart)
		return text[:headEnd] + "\n" + CCRMarker(CCRKey(original), len(original)) + "\n" + text[tailStart:], true
	})
	if !changed {
		return result, false, nil
	}
	return out, true, stash
}

// ccrMarkerSpanRE matches a complete inline CCR marker (as produced by
// CCRMarker; no "]]" can occur inside one).
var ccrMarkerSpanRE = regexp.MustCompile(`\[\[ccr key=[0-9a-f]{24} bytes=\d+[^\]]*\]\]`)

// adjustCutForMarkers moves the head/tail cut points off any inline CCR
// marker left by an earlier transform (e.g. log_compact), so truncation never
// slices a marker in half — a half marker is unparseable garbage the model
// can neither read nor expand. Head cuts shrink to before the marker; tail
// cuts grow to after it. Content is unaffected otherwise: everything between
// the cuts is stashed and recoverable via this transform's own marker.
func adjustCutForMarkers(text string, headEnd, tailStart int) (int, int) {
	for _, span := range ccrMarkerSpanRE.FindAllStringIndex(text, -1) {
		if span[0] < headEnd && headEnd < span[1] {
			headEnd = span[0]
		}
		if span[0] < tailStart && tailStart < span[1] {
			tailStart = span[1]
		}
	}
	return headEnd, tailStart
}

// TransformInfo is the descriptor the dashboard uses to render a toggle for
// every transform (even ones with no measured traffic yet). Verified reflects
// whether the transform currently passes the gimmick gate, so the UI can tell
// the operator which transforms are proven safe to turn On.
type TransformInfo struct {
	Name     string `json:"name"`
	Lossless bool   `json:"lossless"`
	Verified bool   `json:"verified"`
}

// DefaultTransformInfo lists the default transforms with their lossless flag and
// live gimmick-gate verdict so the settings UI can show a toggle + a "verified"
// badge per transform before any traffic exists. The gate runs against the
// small synthetic GateCorpus (microseconds), so recomputing per call is cheap
// and keeps the flag honest rather than hard-coded.
func DefaultTransformInfo() []TransformInfo {
	ts := DefaultTransforms()
	verified := make(map[string]bool, len(ts))
	for _, m := range RunGate(ts, GateCorpus()) {
		verified[m.Transform] = (!m.Lossless || m.LosslessOK) && m.SecretSafe && m.RecoverableOK &&
			(m.Changed == 0 || m.TotalSavedBytes > 0)
	}
	out := make([]TransformInfo, 0, len(ts))
	for _, t := range ts {
		out = append(out, TransformInfo{Name: t.Name(), Lossless: t.Lossless(), Verified: verified[t.Name()]})
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

// Verify confirms the minified output parses to the same JSON value — the
// runtime backstop the pipeline calls before applying a lossless transform.
func (jsonMinify) Verify(before, after json.RawMessage) bool {
	return jsonTextValuesEqual(before, after)
}

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

// runeSafeEnd returns an offset <= n where s[:offset] does not split a UTF-8
// rune (so json.Marshal won't substitute U+FFFD at the seam).
func runeSafeEnd(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// runeSafeStart returns an offset >= n where s[offset:] begins on a UTF-8 rune
// boundary.
func runeSafeStart(s string, n int) int {
	if n <= 0 {
		return 0
	}
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return n
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
