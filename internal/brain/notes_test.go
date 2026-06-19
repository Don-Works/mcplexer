package brain

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSplitBodyNotes(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantDesc  string
		wantNotes string
	}{
		{"no notes", "Just prose.", "Just prose.", ""},
		{
			name:      "prose + notes",
			body:      "Prose here.\n\n## Notes\n- 2026-06-03 (agent): a note",
			wantDesc:  "Prose here.",
			wantNotes: "## Notes\n- 2026-06-03 (agent): a note",
		},
		{"notes only", "## Notes\n- one", "", "## Notes\n- one"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desc, notes := SplitBodyNotes(tc.body)
			if desc != tc.wantDesc {
				t.Errorf("desc = %q, want %q", desc, tc.wantDesc)
			}
			if notes != tc.wantNotes {
				t.Errorf("notes = %q, want %q", notes, tc.wantNotes)
			}
		})
	}
}

func TestComposeTaskBody(t *testing.T) {
	at := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	notes := []store.TaskNote{
		{Body: "first note", AuthorKind: "agent", CreatedAt: at},
		{Body: "second note", AuthorKind: "user", CreatedAt: at},
	}
	got := composeTaskBody("Prose.", notes)
	want := "Prose.\n\n## Notes\n- 2026-06-03 (agent): first note\n- 2026-06-03 (user): second note"
	if got != want {
		t.Errorf("composeTaskBody:\n got %q\nwant %q", got, want)
	}

	// No notes -> just the prose.
	if got := composeTaskBody("Only prose.", nil); got != "Only prose." {
		t.Errorf("note-less body = %q", got)
	}
}

func TestSelfWriteSet(t *testing.T) {
	s := newSelfWriteSet()
	s.Mark("/a.md", "sha1")

	if !s.IsSelf("/a.md", "sha1") {
		t.Fatal("expected self-write to match")
	}
	// Consumed — a second identical check must NOT match.
	if s.IsSelf("/a.md", "sha1") {
		t.Error("self-write should be consumed after first match")
	}
	// Different sha never matches.
	s.Mark("/b.md", "shaB")
	if s.IsSelf("/b.md", "other") {
		t.Error("mismatched sha should not match")
	}
}

func TestSelfWriteSet_TTLEviction(t *testing.T) {
	now := time.Now()
	s := &selfWriteSet{m: map[string]selfWriteEntry{}, now: func() time.Time { return now }}
	s.Mark("/a.md", "sha1")
	// Advance past the TTL.
	now = now.Add(selfWriteTTL + time.Second)
	if s.IsSelf("/a.md", "sha1") {
		t.Error("entry past TTL should be evicted, not match")
	}
}
