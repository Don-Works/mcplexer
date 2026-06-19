package codemode

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestSandbox_ConcurrentExecuteNoFalseMemoryTrip runs two memory-heavy
// Execute calls concurrently and asserts neither falsely trips the
// process-global heap watchdog. With a generous limit and the
// consecutive-breach guard, transient cross-execution allocation noise must
// not be attributed to an innocent sandbox. Regression for the spurious
// "execution memory limit exceeded" abort under concurrency.
func TestSandbox_ConcurrentExecuteNoFalseMemoryTrip(t *testing.T) {
	// Modest, well-behaved allocation per execution: build and discard a few
	// arrays. This stays far under the limit on its own; the risk being
	// guarded against is the OTHER goroutine's heap inflating this one's
	// measured growth.
	code := `
let total = 0;
for (let i = 0; i < 200; i++) {
  const a = new Array(500).fill(i);
  total += a.length;
}
print(total);`

	const n = 4
	results := make([]*ExecutionResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			caller := newMockCaller()
			sb := NewSandbox(caller, 10*time.Second)
			// Tight-ish limit + short period so a naive single-tick watchdog
			// would be very likely to false-trip under the concurrent noise;
			// the consecutive-breach guard must prevent that.
			sb.maxHeapGrowthMB = 32
			sb.watchdogPeriod = 5 * time.Millisecond
			results[idx], errs[idx] = sb.Execute(context.Background(), code, nil)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("execution %d returned error: %v", i, errs[i])
		}
		if results[i].Error != "" {
			t.Fatalf("execution %d falsely tripped a limit: %q", i, results[i].Error)
		}
		if results[i].Output != "100000\n" {
			t.Fatalf("execution %d wrong output: %q", i, results[i].Output)
		}
	}
}

// TestSandbox_MemoryLimitStillTripsOnRunaway guards the other direction: the
// consecutive-breach guard must NOT defang the watchdog against a genuine
// monotonic runaway allocation.
func TestSandbox_MemoryLimitStillTripsOnRunaway(t *testing.T) {
	caller := newMockCaller()
	sb := NewSandbox(caller, 30*time.Second)
	sb.maxHeapGrowthMB = 4
	sb.watchdogPeriod = 5 * time.Millisecond

	code := `var a = []; while (true) { a.push(new Array(10000).fill('x')); }`
	start := time.Now()
	result, err := sb.Execute(context.Background(), code, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "execution exceeded memory limit" {
		t.Fatalf("expected memory-limit error, got %q", result.Error)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("runaway took too long to trip: %s", time.Since(start))
	}
}

// TestCompactValue covers the compactValue helper behind the compact()
// global: nested-map recursion, columnar compaction of a uniform array, and
// element-wise pruning of a mixed array.
func TestCompactValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		// validate inspects the compacted result.
		validate func(t *testing.T, got any)
	}{
		{
			name: "nested map prunes nulls and recurses",
			in: map[string]any{
				"keep":  "v",
				"empty": "",
				"null":  nil,
				"inner": map[string]any{"a": 1.0, "blank": ""},
			},
			validate: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("want map, got %T", got)
				}
				if _, present := m["null"]; present {
					t.Errorf("null should be pruned: %#v", m)
				}
				if _, present := m["empty"]; present {
					t.Errorf("empty string should be pruned: %#v", m)
				}
				inner, ok := m["inner"].(map[string]any)
				if !ok {
					t.Fatalf("inner not recursed: %#v", m)
				}
				if _, present := inner["blank"]; present {
					t.Errorf("nested empty should be pruned: %#v", inner)
				}
			},
		},
		{
			name: "uniform array of >=3 objects becomes columnar",
			in: []any{
				map[string]any{"id": 1.0, "name": "a"},
				map[string]any{"id": 2.0, "name": "b"},
				map[string]any{"id": 3.0, "name": "c"},
			},
			validate: func(t *testing.T, got any) {
				// Columnar form is a map of column-name → slice, not a slice.
				if _, isSlice := got.([]any); isSlice {
					t.Fatalf("expected columnar map, got element-wise slice: %#v", got)
				}
				if _, isMap := got.(map[string]any); !isMap {
					t.Fatalf("expected columnar map, got %T", got)
				}
			},
		},
		{
			name: "mixed array falls back to element-wise prune",
			in: []any{
				map[string]any{"id": 1.0},
				"scalar",
				map[string]any{"id": 3.0},
			},
			validate: func(t *testing.T, got any) {
				s, ok := got.([]any)
				if !ok {
					t.Fatalf("mixed array should stay a slice, got %T", got)
				}
				if len(s) != 3 {
					t.Fatalf("want 3 elements, got %d: %#v", len(s), s)
				}
				el, ok := s[1].(map[string]any)
				if !ok || !isTextProjection(el) || el["text"] != "scalar" {
					t.Errorf("scalar element should project to text object, got %#v", s[1])
				}
			},
		},
		{
			name: "string wraps into text projection",
			in:   "hello",
			validate: func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok || !isTextProjection(m) {
					t.Fatalf("want text projection, got %#v", got)
				}
				if m["text"] != "hello" {
					t.Errorf("text = %#v", m["text"])
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tc.validate(t, compactValue(tc.in))
		})
	}
}

// TestExtractCacheMeta covers the _meta.cache parsing behind setCacheMeta.
func TestExtractCacheMeta(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantHit bool
		wantAge int
	}{
		{
			name:    "cache hit with age",
			raw:     `{"content":[{"type":"text","text":"x"}],"_meta":{"cache":{"cached":true,"age_seconds":42}}}`,
			wantHit: true,
			wantAge: 42,
		},
		{
			name:    "cache miss explicit",
			raw:     `{"content":[],"_meta":{"cache":{"cached":false}}}`,
			wantHit: false,
			wantAge: 0,
		},
		{
			name:    "no _meta at all",
			raw:     `{"content":[{"type":"text","text":"x"}]}`,
			wantHit: false,
			wantAge: 0,
		},
		{
			name:    "_meta without cache key",
			raw:     `{"content":[],"_meta":{"other":1}}`,
			wantHit: false,
			wantAge: 0,
		},
		{
			name:    "malformed json",
			raw:     `not json`,
			wantHit: false,
			wantAge: 0,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			hit, age := extractCacheMeta(json.RawMessage(tc.raw))
			if hit != tc.wantHit || age != tc.wantAge {
				t.Fatalf("want (hit=%v, age=%d), got (hit=%v, age=%d)", tc.wantHit, tc.wantAge, hit, age)
			}
		})
	}
}

// TestCompactForSandbox asserts nulls/empties are pruned from text content
// while isError envelopes pass through unchanged.
func TestCompactForSandbox(t *testing.T) {
	t.Run("prunes nulls and empties from text content", func(t *testing.T) {
		raw := json.RawMessage(`{"content":[{"type":"text","text":"{\"keep\":\"v\",\"drop\":null,\"blank\":\"\"}"}],"isError":false}`)
		out := compactForSandbox(raw)

		var env struct {
			Content []map[string]any `json:"content"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatal(err)
		}
		text, _ := env.Content[0]["text"].(string)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			t.Fatalf("text not valid json: %q", text)
		}
		if _, present := parsed["drop"]; present {
			t.Errorf("null should be pruned: %#v", parsed)
		}
		if _, present := parsed["blank"]; present {
			t.Errorf("empty string should be pruned: %#v", parsed)
		}
		if parsed["keep"] != "v" {
			t.Errorf("kept value lost: %#v", parsed)
		}
	})

	t.Run("isError envelope passes through unchanged", func(t *testing.T) {
		raw := json.RawMessage(`{"content":[{"type":"text","text":"{\"drop\":null}"}],"isError":true}`)
		out := compactForSandbox(raw)
		if !reflect.DeepEqual([]byte(out), []byte(raw)) {
			t.Errorf("isError envelope should be untouched.\n want: %s\n got:  %s", raw, out)
		}
	})

	t.Run("unchanged content returns input verbatim", func(t *testing.T) {
		raw := json.RawMessage(`{"content":[{"type":"text","text":"plain"}],"isError":false}`)
		out := compactForSandbox(raw)
		if !reflect.DeepEqual([]byte(out), []byte(raw)) {
			t.Errorf("nothing-to-prune should return input verbatim.\n want: %s\n got:  %s", raw, out)
		}
	})

	t.Run("preserves structuredContent and metadata when compacting text", func(t *testing.T) {
		raw := json.RawMessage(`{"content":[{"type":"text","text":"{\"keep\":\"fallback\",\"drop\":null}"}],"structuredContent":{"keep":"structured","drop":null},"_meta":{"cache":{"cached":true}}}`)
		out := compactForSandbox(raw)

		var env struct {
			Content           []map[string]any `json:"content"`
			StructuredContent map[string]any   `json:"structuredContent"`
			Meta              map[string]any   `json:"_meta"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatal(err)
		}
		if env.StructuredContent["keep"] != "structured" {
			t.Fatalf("structuredContent was not preserved: %s", out)
		}
		if env.Meta["cache"] == nil {
			t.Fatalf("_meta was not preserved: %s", out)
		}
		text, _ := env.Content[0]["text"].(string)
		var fallback map[string]any
		if err := json.Unmarshal([]byte(text), &fallback); err != nil {
			t.Fatalf("compacted fallback text is not JSON: %q", text)
		}
		if _, present := fallback["drop"]; present {
			t.Fatalf("fallback text was not compacted: %s", text)
		}
	})
}

// ensure the cache-meta path exercised end-to-end via Execute keeps working.
func TestSandbox_LastCallMetaSurface(t *testing.T) {
	caller := newMockCaller()
	caller.responses["svc__cached"] = json.RawMessage(
		`{"content":[{"type":"text","text":"{\"ok\":1}"}],"_meta":{"cache":{"cached":true,"age_seconds":7}}}`,
	)
	tools := []ToolDef{{Name: "svc__cached", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}

	sb := NewSandbox(caller, 5*time.Second)
	result, err := sb.Execute(context.Background(),
		`svc.cached(); print(_lastCallMeta.cached + "," + _lastCallMeta.age_seconds);`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Output != "true,7\n" {
		t.Fatalf("want _lastCallMeta surfaced, got %q", result.Output)
	}
}
