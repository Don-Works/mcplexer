// memory_recall_envelope.go — the memory__recall / memory__recall_about
// response envelope.
//
// THE BUG THIS FIXES (observed live twice on 2026-07-18): recall returned
// `{count, hits:[...]}`, and agents read the result through the sandbox's
// compact() helper. compact() columnarises any array of >=3 homogeneous
// objects (internal/compact.CompactArray), so `hits` silently changes TYPE
// with the result size:
//
//	2 hits → {"count":2,"hits":[{...},{...}]}
//	3 hits → {"count":3,"hits":{"_cols":[...],"_rows":[[...],[...],[...]]}}
//
// Agent code written against the small case — `if (r.hits.length > 0)` — then
// evaluates `undefined > 0` → false and confidently reports ZERO hits when
// there were hits. A false zero from a memory store is worse than an error:
// the agent proceeds as though nothing was ever recorded.
//
// The server cannot stop compact() from reshaping an array, so the envelope
// instead carries the count REDUNDANTLY in forms compaction cannot destroy:
//
//   - count   — a top-level int. PruneObject explicitly preserves 0 and
//     never columnarises a scalar, so it survives verbatim either way.
//   - summary — a top-level non-empty string stating the count in words.
//     Non-empty strings are never pruned, and a scalar is never turned into
//     _rows. It also names where the rows went under compaction, so an agent
//     holding `hits._rows` has a plain-language pointer back.
//   - hits    — unchanged, for every existing consumer.
//
// The invariant the tests pin: for any non-empty result, no field of the
// compacted envelope reads as zero or absent.
package gateway

import (
	"encoding/json"
	"fmt"
)

// memoryRecallEnvelope is the shared shape for both recall surfaces. Field
// order is deliberate: count and summary precede hits so a truncated read of
// the serialised JSON still carries the count.
type memoryRecallEnvelope struct {
	Count   int               `json:"count"`
	Summary string            `json:"summary"`
	Hits    []memoryRecallHit `json:"hits"`
}

// newMemoryRecallEnvelope builds the envelope, guaranteeing a non-nil Hits
// slice (so the JSON is `[]` rather than `null`) and a non-empty Summary.
//
// subject describes what was searched, e.g. `query "postgres"` or
// `task:01ABC`, and is folded into the summary so a caller reading only the
// summary can tell which recall it belongs to.
func newMemoryRecallEnvelope(hits []memoryRecallHit, subject string) memoryRecallEnvelope {
	if hits == nil {
		hits = []memoryRecallHit{}
	}
	return memoryRecallEnvelope{
		Count:   len(hits),
		Summary: recallSummary(len(hits), subject),
		Hits:    hits,
	}
}

// recallSummary renders the compaction-proof scalar. The non-empty form
// states the count twice (prose + `count=N`) and warns that compact() moves
// the rows to hits._rows — the exact mis-parse that produced the false zero.
func recallSummary(n int, subject string) string {
	if subject != "" {
		subject = " for " + subject
	}
	if n == 0 {
		return fmt.Sprintf("0 memories recalled%s — nothing matched (count=0)", subject)
	}
	return fmt.Sprintf(
		"%d memor%s recalled%s (count=%d) — read hits[]; note that compact() "+
			"renders hits as {_cols,_rows} for 3+ results, so use count (or "+
			"hits._rows) rather than hits.length",
		n, plural(n, "y", "ies"), subject, n)
}

// marshal renders the envelope as the JSON text the tool result carries.
// json.Marshal cannot fail for this shape (no channels, funcs, or NaN), so
// the error is discarded to keep call sites readable — matching the
// surrounding handlers.
func (e memoryRecallEnvelope) marshal() string {
	b, _ := json.Marshal(e)
	return string(b)
}
