package compression

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// This file holds content-aware, CCR-backed transforms for text-shaped tool
// results. All are StashingTransforms: they only ever drop content they have
// stashed (recoverable via mcpx__retrieve), so they are reversible-lossy and
// safe by construction — the model can always expand the marker.

// --- LogCompressor: drop low-severity log noise, keep errors + stack traces ---

const (
	logMinLines   = 10
	logMinDropped = 5
)

var (
	logKeepRE = regexp.MustCompile(`(?i)\b(ERROR|WARN(ING)?|FATAL|PANIC|CRITICAL|SEVERE|EXCEPTION|FAIL(ED|URE)?)\b`)
	logDropRE = regexp.MustCompile(`(?i)\b(INFO|DEBUG|TRACE|VERBOSE|NOTICE)\b`)
	// Continuation / stack-trace lines that must be kept with their error.
	logStackRE = regexp.MustCompile(`^\s+(at |File "|\.\.\.|Traceback|Caused by|\tat )`)
	// Load-bearing tokens that veto a drop even on an INFO/DEBUG line.
	logMustKeepRE = regexp.MustCompile(`secret://|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}|[0-9a-f]{32,}`)
)

type logCompressor struct{}

func (logCompressor) Name() string   { return "log_compact" }
func (logCompressor) Lossless() bool { return false }

func (logCompressor) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (logCompressor) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	var stash [][]byte
	out, changed := walkTextBlocks(result, func(text string) (string, bool) {
		lines := strings.Split(text, "\n")
		if len(lines) < logMinLines || !looksLikeLog(lines) {
			return text, false
		}
		kept := filterLogLines(lines)
		dropped := len(lines) - len(kept)
		if dropped < logMinDropped {
			return text, false
		}
		original := []byte(text)
		stash = append(stash, original)
		body := strings.Join(kept, "\n")
		return body + "\n" + CCRMarker(CCRKey(original), len(original)) +
			" (" + strconv.Itoa(dropped) + " lower-severity lines omitted)", true
	})
	if !changed {
		return result, false, nil
	}
	return out, true, stash
}

func looksLikeLog(lines []string) bool {
	hits := 0
	for _, ln := range lines {
		if logKeepRE.MatchString(ln) || logDropRE.MatchString(ln) {
			hits++
		}
	}
	return hits >= 3 && hits*4 >= len(lines)
}

// filterLogLines drops a line ONLY when it is clearly low-severity
// (INFO/DEBUG/TRACE), not also an error/warn, not a stack/continuation line,
// and carries no load-bearing token. Conservative by design.
func filterLogLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if logDropRE.MatchString(ln) &&
			!logKeepRE.MatchString(ln) &&
			!logStackRE.MatchString(ln) &&
			!logMustKeepRE.MatchString(ln) {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// --- structuredDedup (T7): drop content[0].text when structuredContent carries
// the identical JSON value (the payload is wire-encoded twice today). ---

// structuredDedupMinBytes is the smallest duplicated text worth replacing —
// below it the CCR marker costs more than the dropped copy saves.
const structuredDedupMinBytes = 256

type structuredDedup struct{}

func (structuredDedup) Name() string   { return "structured_dedup" }
func (structuredDedup) Lossless() bool { return false }

func (structuredDedup) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (structuredDedup) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(result, &env); err != nil {
		return result, false, nil
	}
	sc, hasSC := env["structuredContent"]
	if !hasSC || len(sc) == 0 {
		return result, false, nil
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(env["content"], &content); err != nil || len(content) != 1 {
		return result, false, nil
	}
	var typ, text string
	if json.Unmarshal(content[0]["type"], &typ) != nil || typ != "text" {
		return result, false, nil
	}
	if json.Unmarshal(content[0]["text"], &text) != nil {
		return result, false, nil
	}
	// Skip small payloads: the marker overhead would exceed the saving.
	if len(text) < structuredDedupMinBytes {
		return result, false, nil
	}
	// Only dedup when the text's JSON value is exactly the structuredContent.
	var tv, scv any
	if json.Unmarshal([]byte(text), &tv) != nil || json.Unmarshal(sc, &scv) != nil {
		return result, false, nil
	}
	if !reflect.DeepEqual(tv, scv) {
		return result, false, nil
	}
	original := []byte(text)
	marker := "[value moved to structuredContent] " + CCRMarker(CCRKey(original), len(original))
	encoded, err := json.Marshal(marker)
	if err != nil {
		return result, false, nil
	}
	content[0]["text"] = encoded
	newContent, err := json.Marshal(content)
	if err != nil {
		return result, false, nil
	}
	env["content"] = newContent
	out, err := json.Marshal(env)
	if err != nil {
		return result, false, nil
	}
	return out, true, [][]byte{original}
}
