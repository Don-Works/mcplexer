package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogCompressorKeepsErrorsDropsNoise(t *testing.T) {
	orig := logFixtureText()
	out, changed, stash := logCompressor{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected log compaction")
	}
	if len(stash) != 1 || string(stash[0]) != orig {
		t.Fatal("the exact original must be stashed for recovery")
	}
	kept := textOf(t, out)
	for _, must := range []string{"ERROR connection refused", "at net.Dial", "WARN  slow query", "ERROR upstream still down"} {
		if !strings.Contains(kept, must) {
			t.Errorf("must-keep line dropped: %q", must)
		}
	}
	if strings.Contains(kept, "cache warm complete") || strings.Contains(kept, "metrics flushed") {
		t.Error("low-severity DEBUG/INFO noise was not dropped")
	}
	if len(out) >= len(fixtureText(orig)) {
		t.Error("expected a smaller output")
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Errorf("marker does not address the stashed original: %v", keys)
	}
}

func TestLogCompressorNoopOnNonLog(t *testing.T) {
	out, changed, _ := logCompressor{}.ApplyWithStash(fixtureText("just a short note\nnot a log at all"))
	if changed {
		t.Fatalf("non-log text must be untouched, got: %s", out)
	}
}

// TestLogCompressorNoFalsePositiveOnProse is the F4 regression: prose, JSON
// keys, and YAML values containing lowercase severity words must never be
// treated as droppable log lines — the old case-insensitive match-anywhere
// regex mangled exactly this kind of text.
func TestLogCompressorNoFalsePositiveOnProse(t *testing.T) {
	fixtures := map[string]string{
		"prose": strings.Join([]string{
			"This info panel shows debug output for the current trace.",
			"The info tab includes verbose diagnostics you can toggle.",
			"Use the debug flag to trace the request lifecycle.",
			"Set verbose mode in the info section of settings.",
			"A notice appears when the trace buffer fills up.",
			"More info about debug symbols lives in the trace docs.",
			"The verbose notice can be dismissed from the info menu.",
			"Debugging info: trace level output is verbose by design.",
			"Check the info page for notice history and trace ids.",
			"Verbose tracing is described in the debug info appendix.",
		}, "\n"),
		"json-ish": strings.Join([]string{
			`{`,
			`  "debug": true,`,
			`  "info": {"level": 2},`,
			`  "trace": "on",`,
			`  "verbose": false,`,
			`  "notice": null,`,
			`  "debugSymbols": true,`,
			`  "infoBanner": "welcome",`,
			`  "traceSampling": 0.5,`,
			`  "verboseLogging": "sometimes"`,
			`}`,
		}, "\n"),
		"yaml-ish": strings.Join([]string{
			"log_level: debug",
			"info_banner: enabled",
			"trace_sampling: 0.5",
			"verbose: true",
			"notice_ttl: 30s",
			"debug_symbols: full",
			"info_endpoint: /info",
			"trace_backend: otel",
			"verbose_shutdown: false",
			"notice_channel: alerts",
		}, "\n"),
	}
	for name, text := range fixtures {
		out, changed, _ := logCompressor{}.ApplyWithStash(fixtureText(text))
		if changed {
			t.Errorf("%s: non-log text was mangled by log_compact:\n%s", name, textOf(t, out))
		}
	}
}

// TestLogCompressorHandlesTimestampAndLogfmt: severity after a timestamp
// prefix, bracketed severity, and logfmt level= are all still recognized.
func TestLogCompressorHandlesTimestampAndLogfmt(t *testing.T) {
	lines := []string{
		"2026-07-04T10:00:00Z INFO starting worker",
		"2026-07-04T10:00:01Z [INFO] cache warm",
		"level=info msg=connected",
		"2026-07-04T10:00:02Z DEBUG poll tick",
		"level=debug msg=heartbeat",
		"2026-07-04T10:00:03Z INFO all good",
		"2026-07-04T10:00:04Z [DEBUG] noise",
		"level=trace msg=fine-grained",
		"2026-07-04T10:00:05Z ERROR upstream refused",
		"2026-07-04T10:00:06Z WARN retrying",
		"2026-07-04T10:00:07Z INFO recovered",
		"level=info msg=done",
	}
	out, changed, stash := logCompressor{}.ApplyWithStash(fixtureText(strings.Join(lines, "\n")))
	if !changed {
		t.Fatal("timestamped/logfmt log should compress")
	}
	if len(stash) != 1 {
		t.Fatal("original must be stashed")
	}
	kept := textOf(t, out)
	if !strings.Contains(kept, "ERROR upstream refused") || !strings.Contains(kept, "WARN retrying") {
		t.Errorf("errors/warnings must be kept:\n%s", kept)
	}
	if strings.Contains(kept, "poll tick") || strings.Contains(kept, "msg=heartbeat") {
		t.Errorf("droppable INFO/DEBUG noise survived:\n%s", kept)
	}
}

func TestStructuredDedupReplacesDuplicateText(t *testing.T) {
	env := structuredDupFixture()
	out, changed, stash := structuredDedup{}.ApplyWithStash(env)
	if !changed {
		t.Fatal("expected dedup when content text duplicates structuredContent")
	}
	if len(stash) != 1 {
		t.Fatal("the original text must be stashed")
	}
	if len(out) >= len(env) {
		t.Error("expected a smaller output after dropping the duplicate text")
	}
	var e struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(out, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(e.StructuredContent) == 0 {
		t.Error("structuredContent must be preserved (it carries the data)")
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Errorf("marker does not address the stashed original: %v", keys)
	}
}

func TestStructuredDedupNoopWithoutStructuredContent(t *testing.T) {
	out, changed, _ := structuredDedup{}.ApplyWithStash(fixtureText(`{"a":1,"b":2}`))
	if changed {
		t.Fatalf("no structuredContent → must be a no-op, got: %s", out)
	}
}

// bigIntEnvelope builds a tool result whose text and structuredContent carry
// the given big-int payloads (padded past structuredDedupMinBytes).
func bigIntEnvelope(t *testing.T, textID, scID string) json.RawMessage {
	t.Helper()
	pad := strings.Repeat("x", structuredDedupMinBytes)
	text := `{"id":` + textID + `,"pad":"` + pad + `"}`
	sc := `{"id":` + scID + `,"pad":"` + pad + `"}`
	encText, _ := json.Marshal(text)
	return json.RawMessage(`{"content":[{"type":"text","text":` + string(encText) + `}],"structuredContent":` + sc + `}`)
}

// TestStructuredDedupNumberExactMismatch is the F1 float64-blindness
// regression: two int64s that collide as float64 (differ beyond 2^53) are NOT
// the same value, so dedup must NOT fire — a float64 compare would drop the
// accurate text copy and leave only the differing structuredContent inline.
func TestStructuredDedupNumberExactMismatch(t *testing.T) {
	env := bigIntEnvelope(t, "9223372036854775807", "9223372036854776000")
	out, changed, _ := structuredDedup{}.ApplyWithStash(env)
	if changed {
		t.Fatalf("values differing beyond float64 precision must not dedup, got: %s", out)
	}
}

// TestStructuredDedupNumberExactMatch: identical big-int payloads still dedup
// (UseNumber compares digit strings, so exact equality keeps working).
func TestStructuredDedupNumberExactMatch(t *testing.T) {
	env := bigIntEnvelope(t, "9223372036854775807", "9223372036854775807")
	out, changed, stash := structuredDedup{}.ApplyWithStash(env)
	if !changed {
		t.Fatal("byte-identical values must still dedup")
	}
	if len(stash) != 1 || !strings.Contains(string(stash[0]), "9223372036854775807") {
		t.Fatalf("stash must hold the exact original text: %q", stash)
	}
	if !strings.Contains(string(out), "9223372036854775807") {
		t.Errorf("structuredContent must keep the exact digits inline: %s", out)
	}
}
