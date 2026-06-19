package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

// BodyMaxChars is the per-chunk cap when splitting a long worker
// response across multiple telegram sends. Telegram's hard limit is
// 4096; we use 3900 to leave headroom for the rendered frame (title,
// tags, code-fence markers) without risk of mid-character truncation.
const BodyMaxChars = 3900

var (
	ansiEscapeRE        = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiColorFragmentRE = regexp.MustCompile(`\[[0-9;]{1,4}m`)
)

// TruncateBody is preserved for callers that explicitly want a single
// capped chunk (e.g. SSE preview surfaces); for telegram dispatch use
// SplitBody so users see the full response across multiple messages.
func TruncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= BodyMaxChars {
		return s
	}
	return s[:BodyMaxChars-1] + "…"
}

// SplitBody returns the input as a slice of chunks each ≤BodyMaxChars.
// The split prefers paragraph boundaries (double-newline), then single
// newlines, then sentence boundaries (period+space), then word
// boundaries (space). Falls back to a hard char cut only when the next
// boundary is beyond the chunk limit (e.g. a single 8000-char
// no-whitespace blob — pathological but bounded). Empty input returns
// a single empty string so callers don't special-case zero chunks.
func SplitBody(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{""}
	}
	if len(s) <= BodyMaxChars {
		return []string{s}
	}
	var out []string
	for len(s) > BodyMaxChars {
		cut := preferredCut(s, BodyMaxChars)
		out = append(out, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// preferredCut picks the best chunk-end position inside [limit/2, limit].
// Returns the byte index AFTER the last character of the chunk.
func preferredCut(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	floor := limit / 2
	candidates := []string{"\n\n", "\n", ". ", " "}
	for _, sep := range candidates {
		if i := strings.LastIndex(s[:limit], sep); i >= floor {
			return i + len(sep)
		}
	}
	return limit
}

// CleanOutboundBody strips terminal control noise before chat rendering. It is
// intentionally presentation-only: the mesh/Signal/audit records keep the raw
// body for forensics.
func CleanOutboundBody(s string) string {
	s = stripControlNoise(s)
	s = compactWorkerFailure(s)
	return strings.TrimSpace(s)
}

func stripControlNoise(s string) string {
	s = ansiEscapeRE.ReplaceAllString(s, "")
	s = ansiColorFragmentRE.ReplaceAllString(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func compactWorkerFailure(s string) string {
	if !strings.HasPrefix(s, "worker ") || !strings.Contains(s, " status=failure") {
		return s
	}
	name := between(s, `worker "`, `" finished`)
	runID := between(s, "(run ", ")")
	_, reason, _ := strings.Cut(s, " status=failure")
	reason = strings.TrimSpace(strings.TrimLeft(reason, " -:—"))
	reason = strings.ReplaceAll(reason, "(stderr:", "stderr:")
	reason = strings.TrimSuffix(reason, ")")
	reason = collapseSpaces(reason)
	reason = truncateClean(reason, 700)

	var lines []string
	if name != "" {
		lines = append(lines, "Worker failed: "+name)
	} else {
		lines = append(lines, "Worker failed")
	}
	if runID != "" {
		lines = append(lines, "Run: "+runID)
	}
	if reason != "" {
		lines = append(lines, "Reason: "+reason)
	}
	return strings.Join(lines, "\n")
}

func between(s, left, right string) string {
	_, after, ok := strings.Cut(s, left)
	if !ok {
		return ""
	}
	before, _, ok := strings.Cut(after, right)
	if !ok {
		return ""
	}
	return strings.TrimSpace(before)
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateClean(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return strings.TrimSpace(s[:max-3]) + "..."
}

// BuildTitle renders a short "<kind> from <agent>[ (<role>)]" header line.
func BuildTitle(agentName, role, kind string) string {
	who := agentName
	if role != "" && role != agentName {
		who = fmt.Sprintf("%s (%s)", agentName, role)
	}
	return fmt.Sprintf("%s from %s", kind, who)
}

// ParseCallbackData parses the "<action>:<arg>" payload from an inline-button
// callback. Returns ("", "", false) if the payload is malformed.
func ParseCallbackData(raw string) (action, arg string, ok bool) {
	idx := strings.Index(raw, ":")
	if idx <= 0 {
		return "", "", false
	}
	return raw[:idx], raw[idx+1:], true
}
