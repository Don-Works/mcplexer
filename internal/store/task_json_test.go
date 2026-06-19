package store

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixed reference instant used across the table tests: a wall-clock
// time with sub-second precision so truncation is observable.
var refNano = time.Date(2026, 5, 25, 19, 50, 45, 137_142_000, time.UTC)

func TestTaskMarshalJSON_DefaultTruncatesToSeconds(t *testing.T) {
	due := refNano.Add(48 * time.Hour)
	assigned := refNano.Add(-30 * time.Minute)
	lease := refNano.Add(5 * time.Minute)
	closed := refNano.Add(1 * time.Hour)
	deleted := refNano.Add(2 * time.Hour)

	tt := Task{
		ID:                "01TEST",
		WorkspaceID:       "ws-1",
		Title:             "Hello",
		Status:            "doing",
		Priority:          "normal",
		DueAt:             &due,
		AssignedAt:        &assigned,
		LeaseExpiresAt:    &lease,
		ClosedAt:          &closed,
		DeletedAt:         &deleted,
		CreatedAt:         refNano,
		UpdatedAt:         refNano.Add(time.Second + 250*time.Millisecond),
		StatusHistoryJSON: json.RawMessage(`[{"at":"2026-05-25T19:50:45.137142Z","evt":"created","to":"open"}]`),
	}

	raw, err := json.Marshal(tt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)

	// Every emitted timestamp must end in plain `Z` (no fractional
	// component) — substring scan is sufficient because no other
	// field shape includes a `.` followed by digits + `Z` here.
	if strings.Contains(s, ".137142Z") {
		t.Errorf("expected nanoseconds to be stripped, got %s", s)
	}
	for _, want := range []string{
		`"created_at":"2026-05-25T19:50:45Z"`,
		`"updated_at":"2026-05-25T19:50:46Z"`,
		`"due_at":"2026-05-27T19:50:45Z"`,
		`"assigned_at":"2026-05-25T19:20:45Z"`,
		`"lease_expires_at":"2026-05-25T19:55:45Z"`,
		`"closed_at":"2026-05-25T20:50:45Z"`,
		`"deleted_at":"2026-05-25T21:50:45Z"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}

	// status_history's embedded `at` was also rewritten.
	if !strings.Contains(s, `"at":"2026-05-25T19:50:45Z"`) {
		t.Errorf("status_history at not truncated: %s", s)
	}
}

func TestTaskNote_TaskOffer_DefaultTruncates(t *testing.T) {
	note := TaskNote{
		ID:        "n1",
		TaskID:    "t1",
		Body:      "hi",
		CreatedAt: refNano,
	}
	if b, err := json.Marshal(note); err != nil {
		t.Fatalf("note marshal: %v", err)
	} else if !strings.Contains(string(b), `"created_at":"2026-05-25T19:50:45Z"`) {
		t.Errorf("note: want truncated created_at, got %s", b)
	}

	accepted := refNano.Add(time.Second)
	declined := refNano.Add(2 * time.Second)
	offer := TaskOffer{
		ID:                "o1",
		EnvelopeCreatedAt: refNano,
		CreatedAt:         refNano,
		AcceptedAt:        &accepted,
		DeclinedAt:        &declined,
	}
	b, err := json.Marshal(offer)
	if err != nil {
		t.Fatalf("offer marshal: %v", err)
	}
	for _, want := range []string{
		`"envelope_created_at":"2026-05-25T19:50:45Z"`,
		`"created_at":"2026-05-25T19:50:45Z"`,
		`"accepted_at":"2026-05-25T19:50:46Z"`,
		`"declined_at":"2026-05-25T19:50:47Z"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("offer missing %s: %s", want, b)
		}
	}
}

func TestMarshalJSONWithPrecision_NanosOptIn(t *testing.T) {
	tt := Task{
		ID:        "01TEST",
		CreatedAt: refNano,
		UpdatedAt: refNano,
		StatusHistoryJSON: json.RawMessage(
			`[{"at":"2026-05-25T19:50:45.137142Z","evt":"created","to":"open"}]`),
	}

	defaultOut, err := MarshalJSONWithPrecision(tt, false)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if !strings.Contains(string(defaultOut), `"created_at":"2026-05-25T19:50:45Z"`) {
		t.Errorf("default should truncate: %s", defaultOut)
	}
	if strings.Contains(string(defaultOut), ".137142") {
		t.Errorf("default leaked nanos: %s", defaultOut)
	}

	nanoOut, err := MarshalJSONWithPrecision(tt, true)
	if err != nil {
		t.Fatalf("nanos: %v", err)
	}
	if !strings.Contains(string(nanoOut), ".137142") {
		t.Errorf("nanos opt-in dropped precision: %s", nanoOut)
	}
}

func TestMarshalJSONWithPrecision_HandlesPointers(t *testing.T) {
	tt := &Task{ID: "p1", CreatedAt: refNano, UpdatedAt: refNano}

	def, _ := MarshalJSONWithPrecision(tt, false)
	if strings.Contains(string(def), ".") {
		t.Errorf("pointer default leaked nanos: %s", def)
	}
	nan, _ := MarshalJSONWithPrecision(tt, true)
	if !strings.Contains(string(nan), ".137142") {
		t.Errorf("pointer nanos: missing fraction in %s", nan)
	}
}

func TestMarshalJSONWithPrecision_HandlesSlices(t *testing.T) {
	rows := []Task{
		{ID: "a", CreatedAt: refNano, UpdatedAt: refNano},
		{ID: "b", CreatedAt: refNano.Add(time.Microsecond), UpdatedAt: refNano},
	}
	def, _ := MarshalJSONWithPrecision(rows, false)
	if strings.Contains(string(def), ".") {
		t.Errorf("slice default leaked nanos: %s", def)
	}
	nan, _ := MarshalJSONWithPrecision(rows, true)
	if !strings.Contains(string(nan), ".137142") {
		t.Errorf("slice nanos: missing fraction in %s", nan)
	}

	notes := []TaskNote{{ID: "n", CreatedAt: refNano}}
	def, _ = MarshalJSONWithPrecision(notes, false)
	if strings.Contains(string(def), ".") {
		t.Errorf("note slice leaked nanos: %s", def)
	}
	nan, _ = MarshalJSONWithPrecision(notes, true)
	if !strings.Contains(string(nan), ".137142") {
		t.Errorf("note slice nanos lost: %s", nan)
	}
}

// TestMarshalJSONWithPrecision_PointerSlices exercises the []*T branches
// of withNanos (pointer slices, distinct from the value-slice []T paths
// covered above) AND the nil-element-inside-slice case. A regression that
// mishandled a nil element would panic or emit wrong precision; this pins
// that a nil entry marshals to JSON null while non-nil entries still get
// nanosecond precision under the opt-in.
func TestMarshalJSONWithPrecision_PointerSlices(t *testing.T) {
	a := &Task{ID: "a", CreatedAt: refNano, UpdatedAt: refNano}
	noteA := &TaskNote{ID: "n", CreatedAt: refNano}
	offerA := &TaskOffer{ID: "o", CreatedAt: refNano, EnvelopeCreatedAt: refNano}

	cases := []struct {
		name string
		val  any
	}{
		{"[]*Task with nil element", []*Task{a, nil}},
		{"[]*TaskNote with nil element", []*TaskNote{noteA, nil}},
		{"[]*TaskOffer with nil element", []*TaskOffer{offerA, nil}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Default (no nanos): truncated, nil entry is null, no panic.
			def, err := MarshalJSONWithPrecision(tc.val, false)
			if err != nil {
				t.Fatalf("default marshal: %v", err)
			}
			if strings.Contains(string(def), ".137142") {
				t.Errorf("default leaked nanos: %s", def)
			}
			if !strings.Contains(string(def), "null") {
				t.Errorf("nil element should marshal to null: %s", def)
			}

			// Nanos opt-in: non-nil entry keeps full precision, nil entry
			// is still null, still no panic.
			nan, err := MarshalJSONWithPrecision(tc.val, true)
			if err != nil {
				t.Fatalf("nanos marshal: %v", err)
			}
			if !strings.Contains(string(nan), ".137142") {
				t.Errorf("nanos opt-in dropped precision for non-nil entry: %s", nan)
			}
			if !strings.Contains(string(nan), "null") {
				t.Errorf("nil element should marshal to null under nanos: %s", nan)
			}
		})
	}
}

// TestInternalSortPreservesNanos pins the acceptance constraint that
// truncation is JSON-only — the Go-side time.Time still carries nano
// precision so same-second events keep stable ordering when sorted.
func TestInternalSortPreservesNanos(t *testing.T) {
	base := time.Date(2026, 5, 25, 19, 50, 45, 0, time.UTC)
	history := []TaskStatusHistoryEntry{
		{At: base.Add(900 * time.Nanosecond), Evt: "third"},
		{At: base.Add(100 * time.Nanosecond), Evt: "first"},
		{At: base.Add(500 * time.Nanosecond), Evt: "second"},
	}
	sort.Slice(history, func(i, j int) bool {
		return history[i].At.Before(history[j].At)
	})
	got := []string{history[0].Evt, history[1].Evt, history[2].Evt}
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sort lost nano precision: got %v want %v", got, want)
		}
	}

	// Round-trip the history through Task.MarshalJSON (truncating) and
	// back through Unmarshal; the at-rest column would never see this
	// path, but if a caller did try to re-import a truncated response
	// the entries should still parse cleanly.
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("history marshal: %v", err)
	}
	tt := Task{StatusHistoryJSON: raw}
	out, err := json.Marshal(tt)
	if err != nil {
		t.Fatalf("task marshal: %v", err)
	}
	// Extract status_history back out of the task envelope.
	var envelope struct {
		StatusHistoryJSON json.RawMessage `json:"status_history"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("envelope unmarshal: %v", err)
	}
	var rt []TaskStatusHistoryEntry
	if err := json.Unmarshal(envelope.StatusHistoryJSON, &rt); err != nil {
		t.Fatalf("history rt: %v", err)
	}
	for _, e := range rt {
		if e.At.Nanosecond() != 0 {
			t.Errorf("expected emitted at to be seconds-precision, got %v", e.At)
		}
	}
}

// TestStatusHistoryJSON_MalformedPassesThrough confirms we never
// corrupt unexpected payloads — malformed status_history is left as-is
// rather than dropped.
func TestStatusHistoryJSON_MalformedPassesThrough(t *testing.T) {
	tt := Task{StatusHistoryJSON: json.RawMessage(`{"not":"an array"}`)}
	out, err := json.Marshal(tt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"status_history":{"not":"an array"}`) {
		t.Errorf("malformed payload not preserved: %s", out)
	}
}

// TestStatusHistoryJSON_NilOrEmptyOmitted confirms an empty or absent
// status_history blob stays absent in output (omitempty respected).
func TestStatusHistoryJSON_NilOrEmptyOmitted(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage(" ")} {
		tt := Task{ID: "x", CreatedAt: refNano, UpdatedAt: refNano, StatusHistoryJSON: raw}
		out, err := json.Marshal(tt)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), `"status_history"`) {
			t.Errorf("empty raw should have been omitted: %s", out)
		}
	}
}

// TestZeroTimePreserved confirms zero time.Time values stay as the
// canonical Go zero string ("0001-01-01T00:00:00Z") rather than being
// shifted by truncation oddities.
func TestZeroTimePreserved(t *testing.T) {
	tt := Task{ID: "z"} // all time fields zero
	out, err := json.Marshal(tt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"created_at":"0001-01-01T00:00:00Z"`) {
		t.Errorf("zero CreatedAt lost: %s", out)
	}
}
