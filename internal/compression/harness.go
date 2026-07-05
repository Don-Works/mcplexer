package compression

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// The gimmick gate. This is the machinery that turns the three hard product
// constraints into executable checks, so a transform can only be trusted On
// after it PROVES itself on a representative corpus:
//
//   - 100% accuracy  → a Lossless transform must never change the JSON VALUE a
//     consumer parses, and must never drop a must-survive token (secrets,
//     UUIDs, hashes, credentials).
//   - performance    → each transform stays under a per-payload latency budget.
//   - no gimmicks     → a transform that changes payloads must show a real byte
//     win across the corpus (not a vanity no-op or a loss).
//
// RunGate + GateCorpus live in package (not _test) code so a future CI command
// or the answer-equivalence eval can reuse them.

// Fixture is one corpus payload replayed through every transform.
type Fixture struct {
	Name string
	// Payload is a full MCP tool-result envelope.
	Payload json.RawMessage
	// ExpectJSON is true when the text content blocks carry JSON, so the gate
	// can verify a lossless transform preserved the JSON value.
	ExpectJSON bool
	// MustSurvive lists substrings (secrets, UUIDs, hashes, credentials) that
	// MUST appear verbatim in any transform's output — no compressor may ever
	// mangle a load-bearing token.
	MustSurvive []string
}

// GateMetrics is a transform's measured behaviour over the whole corpus.
type GateMetrics struct {
	Transform       string
	Lossless        bool
	Samples         int
	Changed         int
	TotalOrigBytes  int
	TotalSavedBytes int
	MaxLatency      time.Duration
	// LosslessOK is false if a Lossless transform altered a JSON value.
	LosslessOK bool
	// SecretSafe is false if any must-survive token was dropped from the
	// inline output (see note in RunGate — stashing transforms are exempt
	// because the token remains recoverable via CCR).
	SecretSafe bool
	// RecoverableOK is false if a non-lossless transform changed a payload
	// without stashing the original — i.e. it dropped information irreversibly.
	RecoverableOK bool
	// FirstViolation is a human-readable description of the first failure.
	FirstViolation string
}

// RunGate replays every fixture through every transform and returns per-transform
// metrics. It never fails — callers (the gate test) assert on the metrics.
func RunGate(transforms []Transform, fixtures []Fixture) []GateMetrics {
	out := make([]GateMetrics, 0, len(transforms))
	for _, t := range transforms {
		m := GateMetrics{Transform: t.Name(), Lossless: t.Lossless(), LosslessOK: true, SecretSafe: true, RecoverableOK: true}
		for _, f := range fixtures {
			start := time.Now()
			res, changed, stash := safeApply(t, f.Payload)
			if lat := time.Since(start); lat > m.MaxLatency {
				m.MaxLatency = lat
			}
			m.Samples++
			if !changed {
				continue
			}
			m.Changed++
			m.TotalOrigBytes += len(f.Payload)
			m.TotalSavedBytes += len(f.Payload) - len(res)

			// A non-lossless transform must hand back the original it dropped,
			// or the loss is irreversible.
			if !t.Lossless() && len(stash) == 0 {
				m.RecoverableOK = false
				m.setViolation("lossy transform changed payload without stashing the original in " + f.Name)
			}
			// Must-survive tokens are checked inline only for lossless
			// transforms; a stashing transform may legitimately move a token
			// into CCR (still recoverable), so we don't require it inline.
			if len(stash) == 0 {
				for _, s := range f.MustSurvive {
					if !bytes.Contains(res, []byte(s)) {
						m.SecretSafe = false
						m.setViolation("dropped must-survive token " + s + " in " + f.Name)
					}
				}
			}
			if t.Lossless() && f.ExpectJSON && !jsonTextValuesEqual(f.Payload, res) {
				m.LosslessOK = false
				m.setViolation("JSON value changed by lossless transform in " + f.Name)
			}
		}
		out = append(out, m)
	}
	return out
}

func (m *GateMetrics) setViolation(s string) {
	if m.FirstViolation == "" {
		m.FirstViolation = s
	}
}

// jsonTextValuesEqual reports whether the JSON encoded WITHIN each text content
// block is value-identical before and after (whitespace differences are fine;
// a changed value is not). Non-JSON text blocks must match byte-for-byte.
func jsonTextValuesEqual(before, after json.RawMessage) bool {
	tb, ta := textBlocks(before), textBlocks(after)
	if len(tb) != len(ta) {
		return false
	}
	for i := range tb {
		var vb, va any
		eb := json.Unmarshal([]byte(tb[i]), &vb)
		ea := json.Unmarshal([]byte(ta[i]), &va)
		if eb != nil || ea != nil {
			if tb[i] != ta[i] {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(vb, va) {
			return false
		}
	}
	return true
}

// textBlocks extracts the text of each text content block, in order.
func textBlocks(result json.RawMessage) []string {
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &env); err != nil {
		return nil
	}
	out := make([]string, 0, len(env.Content))
	for _, c := range env.Content {
		if c.Type == "text" {
			out = append(out, c.Text)
		}
	}
	return out
}

// GateCorpus is the representative fixture set the gate replays. It spans the
// payload-type taxonomy so a transform can't win only on cherry-picked inputs.
func GateCorpus() []Fixture {
	return []Fixture{
		{Name: "pretty-json-object", ExpectJSON: true, Payload: fixtureText(prettyJSON(map[string]any{
			"id": 1, "name": "acme", "status": "active", "nested": map[string]any{"a": 1, "b": 2},
		}))},
		{Name: "pretty-json-array", ExpectJSON: true, Payload: fixtureText(prettyJSON([]any{
			map[string]any{"id": 1, "name": "one"},
			map[string]any{"id": 2, "name": "two"},
			map[string]any{"id": 3, "name": "three"},
		}))},
		{Name: "compact-json", ExpectJSON: true, Payload: fixtureText(`{"a":1,"b":2,"c":[1,2,3]}`)},
		{Name: "plain-text-log", Payload: fixtureText("INFO starting\nWARN slow query 1.2s\nERROR connection refused")},
		{Name: "verbose-log", Payload: fixtureText(logFixtureText())},
		{Name: "structured-dup", Payload: structuredDupFixture()},
		{Name: "ansi-colored-log", Payload: fixtureText(ansiFixtureText())},
		{Name: "progress-frames", Payload: fixtureText(progressFixtureText())},
		{Name: "repeated-lines", Payload: fixtureText(strings.Repeat("Retrying connection to upstream:9000 in 5s\n", 60) + "ERROR gave up after 60 attempts")},
		{
			Name:        "base64-data-uri",
			MustSurvive: []string{"550e8400-e29b-41d4-a716-446655440000"},
			Payload: fixtureText("screenshot for trace 550e8400-e29b-41d4-a716-446655440000:\n" +
				"data:image/png;base64," + strings.Repeat("iVBORw0KGgoAAAANSUhEUg", 100) + "=="),
		},
		{Name: "oversize-text", Payload: fixtureText(strings.Repeat("some log line with structured data here 0123456789\n", 400))},
		{Name: "error-envelope", Payload: json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"boom"}]}`)},
		{
			Name:        "secrets-and-ids",
			ExpectJSON:  true,
			MustSurvive: []string{"secret://STRIPE_KEY", "550e8400-e29b-41d4-a716-446655440000", "ghp_ABCDEF0123456789abcdef0123456789ABCD", "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},
			Payload: fixtureText(prettyJSON(map[string]any{
				"api_key":  "secret://STRIPE_KEY",
				"trace_id": "550e8400-e29b-41d4-a716-446655440000",
				"token":    "ghp_ABCDEF0123456789abcdef0123456789ABCD",
				"sha":      "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
			})),
		},
	}
}

func fixtureText(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
	return b
}

func prettyJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// logFixtureText is a representative verbose log: droppable INFO/DEBUG noise
// interleaved with WARN/ERROR + a stack trace that must be kept.
func logFixtureText() string {
	return strings.Join([]string{
		"INFO  server starting on :8080",
		"DEBUG loaded config from /etc/app.yaml",
		"INFO  connected to database",
		"DEBUG cache warm complete",
		"INFO  handling request GET /api/x",
		"DEBUG query took 12ms",
		"INFO  handling request GET /api/y",
		"DEBUG query took 8ms",
		"WARN  slow query detected 1.4s",
		"ERROR connection refused to upstream:9000",
		"  at net.Dial (net.go:120)",
		"  at client.Do (client.go:44)",
		"INFO  retrying in 2s",
		"DEBUG retry attempt 1",
		"ERROR upstream still down",
		"INFO  request completed with 502",
		"DEBUG cleanup done",
		"INFO  metrics flushed",
	}, "\n")
}

// ansiFixtureText is CI-style output saturated with color/cursor escapes.
func ansiFixtureText() string {
	line := "\x1b[32m✓\x1b[0m \x1b[1mtest passed\x1b[0m \x1b[90m(12ms)\x1b[0m\n"
	return strings.Repeat(line, 40) + "\x1b[31m✗ one failure\x1b[0m"
}

// progressFixtureText is npm/pip-style output where \r overwrites frames.
func progressFixtureText() string {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("Downloading package [")
		b.WriteString(strings.Repeat("#", i/10))
		b.WriteString("] ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("%\r")
	}
	b.WriteString("Downloading package [##########] 100%\ndone")
	return b.String()
}

// structuredDupFixture is a tool result whose content[0].text JSON duplicates
// its structuredContent — the double-encoding structuredDedup collapses.
func structuredDupFixture() json.RawMessage {
	obj := map[string]any{
		"id": 7, "name": "widget", "active": true, "score": 42,
		"description": strings.Repeat("a detailed description of the item. ", 8),
		"tags":        []any{"alpha", "beta", "gamma", "delta", "epsilon"},
	}
	objJSON, _ := json.Marshal(obj)
	env, _ := json.Marshal(map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(objJSON)}},
		"structuredContent": obj,
	})
	return env
}
