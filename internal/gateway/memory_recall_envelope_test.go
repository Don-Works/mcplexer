// memory_recall_envelope_test.go — the RECALL FALSE-ZERO regression
// (observed live twice on 2026-07-18).
//
// These tests run the envelope through the REAL compaction path
// (compact.PruneObject + compact.CompactArray, exactly what
// codemode.compactValue applies to a tool result) rather than reasoning
// about it in theory. The invariant: for a non-empty recall, no field of
// the compacted envelope reads as zero or absent.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/compact"
)

// compactEnvelope mirrors codemode.compactValue for a {count, summary,
// hits[]} map: prune the top level, then columnarise the hits array the way
// compact() does for 3+ homogeneous objects. Kept in lockstep with
// internal/codemode/sandbox.go compactValue.
func compactEnvelope(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw=%s)", err, raw)
	}
	pruned := compact.PruneObject(m)
	arr, ok := pruned["hits"].([]any)
	if !ok {
		return pruned // hits was pruned away (empty) — nothing to columnarise
	}
	if len(arr) < 3 {
		return pruned
	}
	maps := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			return pruned
		}
		maps = append(maps, compact.PruneObject(m))
	}
	pruned["hits"] = compact.CompactArray(maps)
	return pruned
}

// fakeHits builds n distinct hits — distinct so CompactArray does not hoist
// every column into _fixed, i.e. the worst case for a caller reading _rows.
func fakeHits(n int) []memoryRecallHit {
	out := make([]memoryRecallHit, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, memoryRecallHit{
			ID:      fmt.Sprintf("01ABC%03d", i),
			Name:    fmt.Sprintf("memory-%d", i),
			Kind:    "note",
			Content: fmt.Sprintf("rollback approver policy revision %d", i),
			Tags:    []string{"ops"},
			Scope:   "global",
			Score:   1.0 - float64(i)/10,
			Source:  "fts",
		})
	}
	return out
}

// TestRecallEnvelope_NonEmptyCannotRenderAsZero is the core regression.
//
// Pre-fix the envelope was {count, hits}; at 3+ hits compact() turned `hits`
// into {_cols,_rows}, so `hits.length` was undefined and agent code of the
// form `if (r.hits.length > 0)` evaluated `undefined > 0` → false and
// reported ZERO hits. The count survived even then, but nothing in the
// envelope told the caller to read it. This asserts the count survives AND
// that a second, self-describing signal (summary) survives with it.
func TestRecallEnvelope_NonEmptyCannotRenderAsZero(t *testing.T) {
	// 1 and 2 stay as arrays; 3+ is the columnar case that broke callers.
	for _, n := range []int{1, 2, 3, 5, 10} {
		t.Run(fmt.Sprintf("%d_hits", n), func(t *testing.T) {
			raw := newMemoryRecallEnvelope(fakeHits(n), `query "rollback"`).marshal()
			got := compactEnvelope(t, raw)

			// count must survive compaction verbatim.
			countVal, ok := got["count"]
			if !ok {
				t.Fatalf("count was pruned out of the compacted envelope: %v", got)
			}
			count, ok := countVal.(float64)
			if !ok {
				t.Fatalf("count is %T, want a number: %v", countVal, countVal)
			}
			if int(count) != n {
				t.Fatalf("count = %v, want %d", count, n)
			}

			// summary must survive and must state the real count. A caller
			// that reads only the summary cannot conclude "nothing found".
			summary, ok := got["summary"].(string)
			if !ok || summary == "" {
				t.Fatalf("summary missing from compacted envelope: %v", got)
			}
			if !strings.Contains(summary, fmt.Sprintf("count=%d", n)) {
				t.Fatalf("summary %q does not carry count=%d", summary, n)
			}
			if strings.Contains(summary, "nothing matched") {
				t.Fatalf("non-empty recall summary claims nothing matched: %q", summary)
			}

			// hits must still be present in SOME form — never pruned away.
			if _, ok := got["hits"]; !ok {
				t.Fatalf("hits vanished from the compacted envelope: %v", got)
			}

			// The decisive assertion: serialise the compacted envelope and
			// confirm it cannot be read as an empty result.
			blob, _ := json.Marshal(got)
			if strings.Contains(string(blob), `"count":0`) {
				t.Fatalf("non-empty recall serialises with count 0: %s", blob)
			}
		})
	}
}

// TestRecallEnvelope_ColumnarHitsStillCarryEveryRow proves no rows are lost
// when compact() columnarises, so a caller reading hits._rows sees exactly
// as many rows as `count` promised.
func TestRecallEnvelope_ColumnarHitsStillCarryEveryRow(t *testing.T) {
	const n = 6
	raw := newMemoryRecallEnvelope(fakeHits(n), `query "rollback"`).marshal()
	got := compactEnvelope(t, raw)

	hits, ok := got["hits"].(map[string]any)
	if !ok {
		t.Fatalf("expected hits to be columnarised at %d hits, got %T",
			n, got["hits"])
	}
	rows, ok := hits["_rows"].([]any)
	if !ok {
		t.Fatalf("columnar hits missing _rows: %v", hits)
	}
	if len(rows) != n {
		t.Fatalf("_rows has %d rows, want %d", len(rows), n)
	}
	// This is the shape that fooled callers: hits.length is undefined here.
	// The summary must therefore name the escape hatch explicitly.
	summary, ok := got["summary"].(string)
	if !ok {
		t.Fatalf("summary missing from compacted envelope: %v", got)
	}
	for _, want := range []string{"_rows", "count"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q does not point callers at %q", summary, want)
		}
	}
}

// TestRecallEnvelope_EmptyIsDistinguishable confirms the genuine zero still
// reads as zero — the fix must not make "nothing found" ambiguous either.
func TestRecallEnvelope_EmptyIsDistinguishable(t *testing.T) {
	raw := newMemoryRecallEnvelope(nil, `query "rollback"`).marshal()

	// Untouched, the empty envelope must carry an explicit empty hits array
	// (not null) so `hits.length` is 0 rather than a TypeError.
	if !strings.Contains(raw, `"hits":[]`) {
		t.Fatalf("empty envelope should carry hits:[], got %s", raw)
	}

	got := compactEnvelope(t, raw)
	count, ok := got["count"].(float64)
	if !ok || count != 0 {
		t.Fatalf("empty envelope count = %v, want 0", got["count"])
	}
	summary, _ := got["summary"].(string)
	if !strings.Contains(summary, "nothing matched") {
		t.Fatalf("empty summary should say nothing matched, got %q", summary)
	}
}

// TestMemoryRecall_EnvelopeEndToEnd drives the real tool and asserts the
// envelope shape arrives over the wire for both the empty and non-empty
// cases — the handler could always have regressed to a bare array.
func TestMemoryRecall_EnvelopeEndToEnd(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newHandlerWithMemoryStore(t)

	recall := func() memoryRecallEnvelope {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": "rollback approver"})
		resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__recall", body)
		if !handled || rpcErr != nil {
			t.Fatalf("memory__recall: handled=%v rpcErr=%v", handled, rpcErr)
		}
		var env memoryRecallEnvelope
		txt := toolResultText(t, resp)
		if err := json.Unmarshal([]byte(txt), &env); err != nil {
			t.Fatalf("recall result is not the JSON envelope: %v (raw=%s)", err, txt)
		}
		return env
	}

	if env := recall(); env.Count != 0 || env.Summary == "" {
		t.Fatalf("empty recall = %+v, want count 0 with a summary", env)
	}

	// Seed enough distinct memories to cross the 3-hit columnar threshold.
	for i := 0; i < 4; i++ {
		body, _ := json.Marshal(map[string]any{
			"scope": "global", "name": fmt.Sprintf("rollback-policy-%d", i),
			"content": fmt.Sprintf(
				"rollback approver policy revision %d requires a second production approver", i),
		})
		if _, rpcErr, _ := h.dispatchMemoryTool(ctx, "memory__save", body); rpcErr != nil {
			t.Fatalf("seed save %d: %v", i, rpcErr)
		}
	}

	env := recall()
	if env.Count == 0 {
		t.Fatal("recall returned zero after seeding 4 matching memories")
	}
	if env.Count != len(env.Hits) {
		t.Fatalf("count %d disagrees with len(hits) %d", env.Count, len(env.Hits))
	}
	if !strings.Contains(env.Summary, fmt.Sprintf("count=%d", env.Count)) {
		t.Fatalf("summary %q does not carry count=%d", env.Summary, env.Count)
	}
}

// TestMemoryRecallAbout_EmptyReturnsJSONNotProse pins the second false-zero
// path: recall_about used to return the prose string "No memories about X
// yet." on empty and JSON otherwise, so a JSON-parsing caller hit a parse
// error exactly when the answer was "none".
func TestMemoryRecallAbout_EmptyReturnsJSONNotProse(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newHandlerWithMemoryStore(t)

	body, _ := json.Marshal(map[string]any{"kind": "task", "id": "01NOPE"})
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__recall_about", body)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__recall_about: handled=%v rpcErr=%v", handled, rpcErr)
	}
	txt := toolResultText(t, resp)

	var env memoryRecallEnvelope
	if err := json.Unmarshal([]byte(txt), &env); err != nil {
		t.Fatalf("empty recall_about is not JSON: %v (raw=%s)", err, txt)
	}
	if env.Count != 0 {
		t.Fatalf("count = %d, want 0", env.Count)
	}
	if env.Summary == "" {
		t.Fatal("empty recall_about should still carry a summary")
	}
	if env.Hits == nil {
		t.Fatal("empty recall_about should carry hits:[] rather than null")
	}
}
