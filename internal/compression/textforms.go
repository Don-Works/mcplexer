package compression

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// decodeNumberExact parses JSON with UseNumber so numeric values compare as
// exact digit strings (json.Number) instead of lossy float64.
func decodeNumberExact(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing content after JSON value")
	}
	return v, nil
}

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
	// logKeepRE vetoes a drop when a line mentions anything error-shaped,
	// anywhere, any case — keeping too much is safe, dropping too much is not.
	logKeepRE = regexp.MustCompile(`(?i)\b(ERROR|WARN(ING)?|FATAL|PANIC|CRITICAL|SEVERE|EXCEPTION|FAIL(ED|URE)?)\b`)
	// logLevelTokenRE matches a whitespace-separated field that IS an
	// UPPERCASE severity token (optionally bracketed / suffixed with ):,]).
	// Anchored per-field and case-sensitive so prose ("this info panel"),
	// JSON keys ("debug": true), and YAML values (level: debug) never match —
	// the old case-insensitive match-anywhere regex false-positived on all
	// three and could mangle non-log text inline.
	logLevelTokenRE = regexp.MustCompile(`^[\[\(]?(INFO|DEBUG|TRACE|VERBOSE|NOTICE|WARN|WARNING|ERROR|FATAL|PANIC|CRITICAL|SEVERE)[\]\):,]?$`)
	// logfmtLevelRE matches the unambiguous logfmt form (level=info); prose
	// never contains it, so lowercase is safe here.
	logfmtLevelRE = regexp.MustCompile(`^level=(info|debug|trace|warn|warning|error|fatal|panic|critical)$`)
	// Continuation / stack-trace lines that must be kept with their error.
	logStackRE = regexp.MustCompile(`^\s+(at |File "|\.\.\.|Traceback|Caused by|\tat )`)
	// Load-bearing tokens that veto a drop even on an INFO/DEBUG line.
	logMustKeepRE = regexp.MustCompile(`secret://|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}|[0-9a-f]{32,}`)
)

// logDroppableLevels are the severities safe to omit (recoverable via CCR).
var logDroppableLevels = map[string]bool{
	"INFO": true, "DEBUG": true, "TRACE": true, "VERBOSE": true, "NOTICE": true,
	"info": true, "debug": true, "trace": true,
}

// lineSeverity returns the severity token of a log line, looking only at the
// first few whitespace-separated fields (level, or timestamp/thread + level).
// Empty string means "no recognizable log level" — the line is never dropped.
func lineSeverity(ln string) string {
	fields := strings.Fields(ln)
	for i, f := range fields {
		if i >= 4 {
			break
		}
		if m := logLevelTokenRE.FindStringSubmatch(f); m != nil {
			return m[1]
		}
		if m := logfmtLevelRE.FindStringSubmatch(f); m != nil {
			return m[1]
		}
	}
	return ""
}

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
		if lineSeverity(ln) != "" {
			hits++
		}
	}
	return hits >= 3 && hits*4 >= len(lines)
}

// filterLogLines drops a line ONLY when its anchored severity token is
// clearly low (INFO/DEBUG/TRACE/...), nothing error-shaped appears anywhere
// in it, it is not a stack/continuation line, and it carries no load-bearing
// token. Conservative by design.
func filterLogLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if logDroppableLevels[lineSeverity(ln)] &&
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
	// Number-exact comparison via UseNumber: a float64-based compare would
	// call two int64s that differ beyond 2^53 precision "equal" and drop the
	// accurate text copy while the corrupted structuredContent survives.
	tv, terr := decodeNumberExact([]byte(text))
	scv, serr := decodeNumberExact(sc)
	if terr != nil || serr != nil {
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
