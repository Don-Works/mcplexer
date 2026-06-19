package brain

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}
func tp(s string) *time.Time { t := ts(s); return &t }

func TestSerializeTask_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		task *store.Task
		body string
	}{
		{
			name: "minimal",
			task: &store.Task{
				ID: "01J7XYZ", WorkspaceID: "mcplexer", Title: "Fix it",
				Status: "open", CreatedAt: ts("2026-06-03T10:00:00Z"),
				UpdatedAt: ts("2026-06-03T10:00:00Z"),
			},
			body: "",
		},
		{
			name: "full with history+composes+tags+assignee+source",
			task: &store.Task{
				ID: "01J7XYZ", WorkspaceID: "mcplexer", Title: "Re-arm cron",
				Status: "review", Priority: "high",
				TagsJSON:           json.RawMessage(`["scheduler","bug"]`),
				DueAt:              tp("2026-06-10T00:00:00Z"),
				Pinned:             true,
				AssigneeOriginKind: "local", AssigneeSessionID: "sess_1",
				SourceKind: "agent", SourceSessionID: "sess_1", SourceToolCallID: "tc_9",
				Meta:              `{"composes":["01CHILD1","01CHILD2"]}`,
				StatusHistoryJSON: json.RawMessage(`[{"at":"2026-06-03T10:00:00Z","evt":"created","to":"open"},{"at":"2026-06-03T11:00:00Z","evt":"status_changed","from":"open","to":"doing","by_session":"sess_1"}]`),
				CreatedAt:         ts("2026-06-03T10:00:00Z"),
				UpdatedAt:         ts("2026-06-03T11:30:00Z"),
			},
			body: "Cron jobs of kind=worker fired exactly once.\n\n## Notes\n- root cause in scheduler.go",
		},
		{
			name: "unicode body + single composes",
			task: &store.Task{
				ID: "01J7ABC", WorkspaceID: "halo", Title: "Café ☕ task",
				Status: "doing", Meta: `{"composes":"01ONLYCHILD"}`,
				CreatedAt: ts("2026-06-03T10:00:00Z"), UpdatedAt: ts("2026-06-03T10:00:00Z"),
			},
			body: "Multi-byte: 日本語 — emoji 🚀 in the body.",
		},
		{
			// Regression: meta keys other than composes (composed_by,
			// rollup_to, work_context, worktree, arbitrary user keys) MUST
			// survive the round-trip or the indexer would silently drop
			// them when it writes ToTask() output back via UpdateTask.
			// Keys + composes are emitted canonically sorted to match the
			// DB's tasks.encodeMetaJSON shape (byte-stable).
			name: "preserves non-composes meta",
			task: &store.Task{
				ID: "01J7DEF", WorkspaceID: "mcplexer", Title: "Child task",
				Status:    "doing",
				Meta:      `{"composed_by":"01EPIC","composes":["01A","01B"],"worktree":"/tmp/wt","z_custom":["one","two"]}`,
				CreatedAt: ts("2026-06-03T10:00:00Z"), UpdatedAt: ts("2026-06-03T10:00:00Z"),
			},
			body: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := SerializeTask(tc.task, tc.body)
			if err != nil {
				t.Fatalf("SerializeTask: %v", err)
			}
			fm, body, err := ParseTask(data)
			if err != nil {
				t.Fatalf("ParseTask: %v", err)
			}
			got, err := fm.ToTask(body)
			if err != nil {
				t.Fatalf("ToTask: %v", err)
			}
			assertTaskEquivalent(t, tc.task, got, tc.body, body)

			// Idempotency: re-serializing the round-tripped task must
			// reproduce the original bytes exactly (the canonical-form
			// invariant the watcher's self-write suppression relies on).
			redata, err := SerializeTask(got, body)
			if err != nil {
				t.Fatalf("re-SerializeTask: %v", err)
			}
			if !bytes.Equal(data, redata) {
				t.Errorf("round-trip not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", data, redata)
			}
		})
	}
}

// assertTaskEquivalent compares the load-bearing fields that survive a
// Serialize -> Parse -> ToTask round-trip.
func assertTaskEquivalent(t *testing.T, want, got *store.Task, wantBody, gotBody string) {
	t.Helper()
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID ||
		got.Title != want.Title || got.Status != want.Status ||
		got.Priority != want.Priority || got.Pinned != want.Pinned {
		t.Errorf("scalar mismatch:\n want %+v\n got  %+v", want, got)
	}
	// A canonical text file carries a trailing newline; the parser
	// preserves it. Compare with trailing newlines normalised away — the
	// load-bearing invariant is that re-serializing is byte-stable, which
	// TestSerializeTask_DeterministicOrder + the idempotency check below
	// cover.
	if bodyTrimmed(gotBody) != bodyTrimmed(wantBody) {
		t.Errorf("body mismatch: want %q got %q", bodyTrimmed(wantBody), gotBody)
	}
	if !sameStatusHistory(t, want.StatusHistoryJSON, got.StatusHistoryJSON) {
		t.Errorf("status history mismatch: want %s got %s", want.StatusHistoryJSON, got.StatusHistoryJSON)
	}
	if !reflect.DeepEqual(metaComposesList(want.Meta), metaComposesList(got.Meta)) {
		t.Errorf("composes mismatch: want %v got %v", metaComposesList(want.Meta), metaComposesList(got.Meta))
	}
	// Full meta object must round-trip losslessly (not just composes).
	if !sameMetaObject(want.Meta, got.Meta) {
		t.Errorf("meta object mismatch: want %s got %s", want.Meta, got.Meta)
	}
	wantTags, _ := decodeStringSlice(want.TagsJSON)
	gotTags, _ := decodeStringSlice(got.TagsJSON)
	if !reflect.DeepEqual(wantTags, gotTags) {
		t.Errorf("tags mismatch: want %v got %v", wantTags, gotTags)
	}
	if got.AssigneeOriginKind != want.AssigneeOriginKind || got.AssigneeSessionID != want.AssigneeSessionID {
		t.Errorf("assignee mismatch")
	}
	if got.SourceKind != want.SourceKind || got.SourceToolCallID != want.SourceToolCallID {
		t.Errorf("source mismatch")
	}
}

// sameMetaObject compares two meta JSON strings by unmarshalling both to
// maps (key-order-insensitive). Empty/"" both normalise to an empty map.
func sameMetaObject(a, b string) bool {
	parse := func(s string) map[string]any {
		s = strings.TrimSpace(s)
		if s == "" {
			return map[string]any{}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil
		}
		if m == nil {
			return map[string]any{}
		}
		return m
	}
	return reflect.DeepEqual(parse(a), parse(b))
}

func bodyTrimmed(b string) string {
	// SerializeTask trims trailing newlines; ParseTask trims leading
	// blank lines. Compare against the same normalisation.
	for len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b
}

func sameStatusHistory(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv []store.TaskStatusHistoryEntry
	if len(bytes.TrimSpace(a)) > 0 {
		_ = json.Unmarshal(a, &av)
	}
	if len(bytes.TrimSpace(b)) > 0 {
		_ = json.Unmarshal(b, &bv)
	}
	return reflect.DeepEqual(av, bv)
}

func TestSerializeTask_DeterministicOrder(t *testing.T) {
	task := &store.Task{
		ID: "01J7XYZ", WorkspaceID: "mcplexer", Title: "x", Status: "open",
		TagsJSON:          json.RawMessage(`["b","a"]`),
		Meta:              `{"composes":["c2","c1"]}`,
		StatusHistoryJSON: json.RawMessage(`[{"at":"2026-06-03T10:00:00Z","evt":"created","to":"open"}]`),
		CreatedAt:         ts("2026-06-03T10:00:00Z"), UpdatedAt: ts("2026-06-03T10:00:00Z"),
	}
	a, err := SerializeTask(task, "body")
	if err != nil {
		t.Fatalf("serialize a: %v", err)
	}
	b, err := SerializeTask(task, "body")
	if err != nil {
		t.Fatalf("serialize b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("serialization not byte-stable:\n%s\n---\n%s", a, b)
	}
}

func TestSerializeMemory_FactBitemporal(t *testing.T) {
	wsID := "mcplexer"
	cases := []struct {
		name       string
		mem        *store.MemoryEntry
		ents       []store.MemoryEntityRow
		wantTValid bool
		wantGlobal bool
		wantEntity bool
	}{
		{
			name: "note has no t_valid fields",
			mem: &store.MemoryEntry{
				ID: "01MEM", Name: "deploy-hygiene", Kind: MemoryKindNote,
				Content: "Never deploy dirty.", WorkspaceID: &wsID, Pinned: true,
				CreatedAt: ts("2026-06-03T09:00:00Z"), UpdatedAt: ts("2026-06-03T09:00:00Z"),
			},
			wantTValid: false,
		},
		{
			name: "fact carries bitemporal + entities",
			mem: &store.MemoryEntry{
				ID: "01FACT", Name: "primary-stack", Kind: MemoryKindFact,
				Content: "Go + TS + Postgres.", WorkspaceID: &wsID,
				TValidStart: ts("2026-06-03T00:00:00Z"),
				CreatedAt:   ts("2026-06-03T09:00:00Z"), UpdatedAt: ts("2026-06-03T09:00:00Z"),
			},
			ents: []store.MemoryEntityRow{
				{EntityKind: "person", EntityID: "person-a", Role: "subject"},
			},
			wantTValid: true,
			wantEntity: true,
		},
		{
			name: "global memory has nil workspace",
			mem: &store.MemoryEntry{
				ID: "01G", Name: "global-note", Kind: MemoryKindNote,
				Content: "Global.", WorkspaceID: nil,
				CreatedAt: ts("2026-06-03T09:00:00Z"), UpdatedAt: ts("2026-06-03T09:00:00Z"),
			},
			wantGlobal: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := SerializeMemory(tc.mem, tc.ents)
			if err != nil {
				t.Fatalf("SerializeMemory: %v", err)
			}
			hasTValid := bytes.Contains(data, []byte("t_valid_start"))
			if hasTValid != tc.wantTValid {
				t.Errorf("t_valid_start presence = %v, want %v\n%s", hasTValid, tc.wantTValid, data)
			}

			fm, body, err := ParseMemory(data)
			if err != nil {
				t.Fatalf("ParseMemory: %v", err)
			}
			gotMem, refs, err := fm.ToMemory(body)
			if err != nil {
				t.Fatalf("ToMemory: %v", err)
			}
			if gotMem.ID != tc.mem.ID || gotMem.Name != tc.mem.Name || gotMem.Kind != tc.mem.Kind {
				t.Errorf("scalar mismatch: %+v", gotMem)
			}
			if bodyTrimmed(body) != bodyTrimmed(tc.mem.Content) {
				t.Errorf("body mismatch: want %q got %q", tc.mem.Content, body)
			}
			// Idempotency: re-serialize must be byte-stable.
			redata, err := SerializeMemory(gotMem, tc.ents)
			if err != nil {
				t.Fatalf("re-SerializeMemory: %v", err)
			}
			if !bytes.Equal(data, redata) {
				t.Errorf("memory round-trip not byte-stable:\n%s\n---\n%s", data, redata)
			}
			if tc.wantGlobal && gotMem.WorkspaceID != nil {
				t.Errorf("expected nil workspace, got %v", *gotMem.WorkspaceID)
			}
			if !tc.wantGlobal && (gotMem.WorkspaceID == nil || *gotMem.WorkspaceID != wsID) {
				t.Errorf("expected workspace %q, got %v", wsID, gotMem.WorkspaceID)
			}
			if tc.wantTValid && gotMem.TValidStart.IsZero() {
				t.Errorf("fact lost t_valid_start")
			}
			if tc.wantEntity && len(refs) != 1 {
				t.Errorf("expected 1 entity ref, got %d", len(refs))
			}
		})
	}
}

func TestSerializeWorkspace_RoundTrip(t *testing.T) {
	w := &store.Workspace{
		ID: "ws1", Name: "mcplexer", RootPath: "/repo", ParentID: "acme",
		Tags: json.RawMessage(`["primary"]`), DefaultPolicy: "allow", Source: "brain",
		CreatedAt: ts("2026-06-03T10:00:00Z"), UpdatedAt: ts("2026-06-03T10:00:00Z"),
	}
	data, err := SerializeWorkspace(w)
	if err != nil {
		t.Fatalf("SerializeWorkspace: %v", err)
	}
	fm, _, err := ParseWorkspace(data)
	if err != nil {
		t.Fatalf("ParseWorkspace: %v", err)
	}
	got, err := fm.ToWorkspace()
	if err != nil {
		t.Fatalf("ToWorkspace: %v", err)
	}
	if got.ID != w.ID || got.Name != w.Name || got.RootPath != w.RootPath ||
		got.ParentID != w.ParentID ||
		got.DefaultPolicy != w.DefaultPolicy || got.Source != w.Source {
		t.Fatalf("workspace round-trip mismatch:\n want %+v\n got  %+v", w, got)
	}
	wantTags, _ := decodeStringSlice(w.Tags)
	gotTags, _ := decodeStringSlice(got.Tags)
	if !reflect.DeepEqual(wantTags, gotTags) {
		t.Fatalf("tags mismatch: want %v got %v", wantTags, gotTags)
	}
}
