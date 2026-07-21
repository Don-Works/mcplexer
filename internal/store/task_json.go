// task_json.go — display-precision JSON marshalling for the task surface.
//
// Every timestamp in a tool / REST response on the task surface defaults
// to seconds precision (`2026-05-25T19:50:45Z`) instead of the
// RFC3339Nano default (`2026-05-25T19:50:45.137142Z`). Nanoseconds are
// rarely actionable for an LLM caller and bloat tool responses with
// noise; seconds are the right default for human + agent display.
//
// Internal precision is PRESERVED in two ways:
//
//  1. The Go structs still carry full nanosecond `time.Time` values —
//     any in-memory `sort.Slice(history, by .At)` continues to work
//     correctly because the truncation only happens at the JSON wire
//     boundary, not on the values themselves.
//
//  2. The at-rest `status_history_json` column is left untouched.
//     `TaskStatusHistoryEntry` deliberately does NOT carry a custom
//     `MarshalJSON` — service-layer code that writes
//     `t.StatusHistoryJSON, _ = json.Marshal(history)` continues to
//     persist nanosecond-precision strings so ordering remains stable
//     across daemon restarts.
//
// The truncation happens inside `Task.MarshalJSON`: we parse the
// `StatusHistoryJSON` blob, walk every entry's `at` field, and re-emit
// with seconds precision. `TaskNote` and `TaskOffer` get the same
// MarshalJSON treatment on their own time.Time fields.
//
// Opt-in nanosecond output: callers that need the full precision (audit
// scrapers, debug tools, peer-import pipelines) call
// `MarshalJSONWithPrecision(v, true)` — the helper uses a type alias to
// bypass the custom MarshalJSON and emit the raw `time.Time` form.

package store

import (
	"bytes"
	"encoding/json"
	"time"
)

// truncSeconds returns t truncated to second precision. Zero values are
// left as zero so JSON output stays consistent with the default Go
// "0001-01-01T00:00:00Z" form.
func truncSeconds(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.Truncate(time.Second)
}

// truncSecondsPtr returns a pointer to a truncated copy of *t. Nil
// pointers pass through unchanged so omitempty fields stay omitted.
func truncSecondsPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	if t.IsZero() {
		return t
	}
	cp := t.Truncate(time.Second)
	return &cp
}

// taskAlias / taskNoteAlias / taskOfferAlias are unexported aliases
// used to avoid infinite recursion when implementing MarshalJSON on the
// public types. Marshalling the alias hits the reflection-based encoder
// directly instead of looping back into our custom method.
type (
	taskAlias      Task
	taskNoteAlias  TaskNote
	taskOfferAlias TaskOffer
)

// MarshalJSON emits a seconds-precision form of every time field on
// Task, including the embedded `status_history` entries. See package
// docs for the rationale.
func (t Task) MarshalJSON() ([]byte, error) {
	cp := taskAlias(t)
	cp.CreatedAt = truncSeconds(cp.CreatedAt)
	cp.UpdatedAt = truncSeconds(cp.UpdatedAt)
	cp.ClosedAt = truncSecondsPtr(cp.ClosedAt)
	cp.DueAt = truncSecondsPtr(cp.DueAt)
	cp.AssignedAt = truncSecondsPtr(cp.AssignedAt)
	cp.LeaseExpiresAt = truncSecondsPtr(cp.LeaseExpiresAt)
	cp.DeletedAt = truncSecondsPtr(cp.DeletedAt)
	cp.StatusHistoryJSON = truncateStatusHistoryJSON(cp.StatusHistoryJSON)
	return json.Marshal(cp)
}

// MarshalJSON emits a seconds-precision form for TaskNote.
func (n TaskNote) MarshalJSON() ([]byte, error) {
	cp := taskNoteAlias(n)
	cp.CreatedAt = truncSeconds(cp.CreatedAt)
	return json.Marshal(cp)
}

// MarshalJSON emits a seconds-precision form for TaskOffer.
func (o TaskOffer) MarshalJSON() ([]byte, error) {
	cp := taskOfferAlias(o)
	cp.EnvelopeCreatedAt = truncSeconds(cp.EnvelopeCreatedAt)
	cp.CreatedAt = truncSeconds(cp.CreatedAt)
	cp.AcceptedAt = truncSecondsPtr(cp.AcceptedAt)
	cp.DeclinedAt = truncSecondsPtr(cp.DeclinedAt)
	return json.Marshal(cp)
}

// truncateStatusHistoryJSON walks a json.RawMessage representing an
// array of TaskStatusHistoryEntry objects and rewrites each `at` field
// in-place with seconds precision. Empty / whitespace-only input is
// normalised to nil so the surrounding `omitempty` tag kicks in
// (json.Marshal otherwise errors on a non-nil zero-length RawMessage).
// Non-array / malformed input passes through unchanged so we never
// corrupt unexpected payloads.
func truncateStatusHistoryJSON(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var entries []TaskStatusHistoryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return raw
	}
	for i := range entries {
		entries[i].At = truncSeconds(entries[i].At)
	}
	out, err := json.Marshal(entries)
	if err != nil {
		return raw
	}
	return out
}

// MarshalJSONWithPrecision is the single chokepoint a handler can use
// to honour a caller-provided precision hint. `nanos=true` returns the
// full RFC3339Nano form; `nanos=false` (or unset) returns the
// seconds-truncated default. Works on Task, *Task, []Task, TaskNote,
// TaskOffer, and any composite that embeds them (e.g. the
// task__get response envelope) — anything else round-trips through
// the standard library encoder unchanged.
//
// REST handlers wire this through `?precision=ns`; MCP tool handlers
// will wire this through a `precision` argument once the task-handler
// agent in flight has landed its surface changes.
func MarshalJSONWithPrecision(v any, nanos bool) ([]byte, error) {
	if !nanos {
		return json.Marshal(v)
	}
	return json.Marshal(withNanos(v))
}

// MarshalJSONIndentWithPrecision mirrors MarshalJSONWithPrecision for
// the indented form used by MCP tool result bodies (the gateway calls
// `json.MarshalIndent(v, "", "  ")` for human-readable tool output).
func MarshalJSONIndentWithPrecision(v any, prefix, indent string, nanos bool) ([]byte, error) {
	if !nanos {
		return json.MarshalIndent(v, prefix, indent)
	}
	return json.MarshalIndent(withNanos(v), prefix, indent)
}

// withNanos converts the task-surface types into alias-typed values
// that bypass the custom MarshalJSON, restoring full nanosecond
// precision. Non-task values pass through unchanged.
func withNanos(v any) any {
	switch x := v.(type) {
	case Task:
		return taskAlias(x)
	case *Task:
		if x == nil {
			return v
		}
		a := taskAlias(*x)
		return &a
	case []Task:
		out := make([]taskAlias, len(x))
		for i := range x {
			out[i] = taskAlias(x[i])
		}
		return out
	case []*Task:
		out := make([]*taskAlias, len(x))
		for i, p := range x {
			if p == nil {
				continue
			}
			a := taskAlias(*p)
			out[i] = &a
		}
		return out
	case TaskNote:
		return taskNoteAlias(x)
	case *TaskNote:
		if x == nil {
			return v
		}
		a := taskNoteAlias(*x)
		return &a
	case []TaskNote:
		out := make([]taskNoteAlias, len(x))
		for i := range x {
			out[i] = taskNoteAlias(x[i])
		}
		return out
	case []*TaskNote:
		out := make([]*taskNoteAlias, len(x))
		for i, p := range x {
			if p == nil {
				continue
			}
			a := taskNoteAlias(*p)
			out[i] = &a
		}
		return out
	case TaskOffer:
		return taskOfferAlias(x)
	case *TaskOffer:
		if x == nil {
			return v
		}
		a := taskOfferAlias(*x)
		return &a
	case []TaskOffer:
		out := make([]taskOfferAlias, len(x))
		for i := range x {
			out[i] = taskOfferAlias(x[i])
		}
		return out
	case []*TaskOffer:
		out := make([]*taskOfferAlias, len(x))
		for i, p := range x {
			if p == nil {
				continue
			}
			a := taskOfferAlias(*p)
			out[i] = &a
		}
		return out
	}
	return v
}
