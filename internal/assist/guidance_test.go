package assist

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
)

// errTest is a sentinel for index-error grounding cases.
var errTest = errors.New("index unavailable")

// TestDeterministicNudges covers the no-model rule set (DESIGN §4.4): the
// missing-criteria / auto-tag / entity-extraction nudges must fire (or not)
// purely from the record's status + tags + body, with no model consulted.
func TestDeterministicNudges(t *testing.T) {
	tests := []struct {
		name      string
		req       GuidanceRequest
		wantKinds []string
	}{
		{
			name: "doing task without criteria gets the checklist nudge",
			req:  GuidanceRequest{Status: "doing", Body: "Fix the bug in the handler."},
			// "handler" implies no tag; no [[ref]] => only missing-criteria.
			wantKinds: []string{GuidanceMissingCriteria},
		},
		{
			name: "open task does not get the criteria nudge",
			req:  GuidanceRequest{Status: "open", Body: "Fix the bug in the handler."},
			// Not doing/review, no keyword tag, no ref => nothing.
			wantKinds: nil,
		},
		{
			name: "existing checklist suppresses the criteria nudge",
			req:  GuidanceRequest{Status: "review", Body: "Done.\n- [ ] verify\n- [x] build"},
			// Has a checklist already; "build"/"done" imply no tag.
			wantKinds: nil,
		},
		{
			name:      "auto-tag fires on a body keyword not already tagged",
			req:       GuidanceRequest{Status: "open", Body: "re-arm the cron jobs in the scheduler"},
			wantKinds: []string{GuidanceAutoTag},
		},
		{
			name:      "auto-tag suppressed when the tag already present",
			req:       GuidanceRequest{Status: "open", Body: "scheduler change", Tags: []string{"scheduler"}},
			wantKinds: nil,
		},
		{
			name:      "prose ref triggers entity-extraction",
			req:       GuidanceRequest{Status: "open", Body: "depends on [[01J7ABC Spec the Brain]]"},
			wantKinds: []string{GuidanceEntityExtract},
		},
		{
			name: "all three fire together",
			req: GuidanceRequest{
				Status: "doing",
				Body:   "cron scheduler fix; see [[deploy-hygiene]] for the rollout",
			},
			wantKinds: []string{GuidanceMissingCriteria, GuidanceAutoTag, GuidanceEntityExtract},
		},
		{
			name:      "empty body yields nothing",
			req:       GuidanceRequest{Status: "doing", Body: "  "},
			wantKinds: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deterministicNudges(tt.req, strings.TrimSpace(tt.req.Body))
			if len(got) != len(tt.wantKinds) {
				t.Fatalf("got %d nudges %v, want kinds %v", len(got), kindsOf(got), tt.wantKinds)
			}
			for i, want := range tt.wantKinds {
				if got[i].Kind != want {
					t.Errorf("nudge[%d] kind = %q want %q", i, got[i].Kind, want)
				}
			}
		})
	}
}

// TestGuidanceNoProfileStillReturnsDeterministic verifies the NEVER-204
// contract: with no store (no model profile), Guidance still returns the
// deterministic nudges and an empty profile.
func TestGuidanceNoProfileStillReturnsDeterministic(t *testing.T) {
	a := New(nil, nil, nil) // nil store => resolveConfig => ErrNoProfile
	nudges, profile, err := a.Guidance(context.Background(), GuidanceRequest{
		Status: "doing",
		Body:   "re-arm the cron scheduler",
	})
	if err != nil {
		t.Fatalf("Guidance err = %v, want nil (never errors on no profile)", err)
	}
	if profile != "" {
		t.Errorf("profile = %q want empty (no model resolved)", profile)
	}
	// doing + "scheduler" keyword => missing-criteria + auto-tag.
	if len(nudges) != 2 {
		t.Fatalf("got %d nudges %v, want 2 deterministic", len(nudges), kindsOf(nudges))
	}
}

// TestSuggestTag covers the keyword-to-tag mapping + the already-present skip.
func TestSuggestTag(t *testing.T) {
	if got := suggestTag("telegram footer cleanup", nil); got != "telegram" {
		t.Errorf("suggestTag(telegram) = %q", got)
	}
	if got := suggestTag("telegram footer", []string{"telegram"}); got != "" {
		t.Errorf("suggestTag with existing tag = %q want empty", got)
	}
	if got := suggestTag("nothing relevant here", nil); got != "" {
		t.Errorf("suggestTag(none) = %q want empty", got)
	}
}

// TestParseLinkMemory covers the tolerant JSON-object parse for the
// model-backed link-memory nudge.
func TestParseLinkMemory(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{`{"name":"deploy-hygiene"}`, "deploy-hygiene"},
		{"```json\n{\"name\": \"worktree-hygiene\"}\n```", "worktree-hygiene"},
		{`{}`, ""},
		{`nothing related`, ""},
		{`{"name": ""}`, ""},
	}
	for _, tt := range tests {
		if got := parseLinkMemory(tt.raw); got != tt.want {
			t.Errorf("parseLinkMemory(%q) = %q want %q", tt.raw, got, tt.want)
		}
	}
}

// TestFirstProseRef covers ref extraction from prose.
func TestFirstProseRef(t *testing.T) {
	if got := firstProseRef("see [[abc Spec]] and [[def]]"); got != "abc Spec" {
		t.Errorf("firstProseRef = %q", got)
	}
	if got := firstProseRef("no refs here"); got != "" {
		t.Errorf("firstProseRef(none) = %q want empty", got)
	}
}

// fakeMemoryIndex is a static assist.MemoryIndex for grounding tests.
type fakeMemoryIndex struct {
	cands []MemoryCandidate
	err   error
}

func (f fakeMemoryIndex) SearchMemories(context.Context, string, string, int) ([]MemoryCandidate, error) {
	return f.cands, f.err
}

// TestLinkMemoryNudgeGrounding covers the §4.4 grounding contract: the
// link-related-memory nudge is suppressed unless an index is wired AND the
// model's pick is a real, indexed memory name. A fabricated slug never reaches
// the GUI (it would mint a dangling [[ref]]).
func TestLinkMemoryNudgeGrounding(t *testing.T) {
	ctx := context.Background()
	cands := []MemoryCandidate{
		{Name: "01JDEPLOY", Title: "deploy hygiene"},
		{Name: "01JWORKTREE", Title: "worktree hygiene"},
	}
	tests := []struct {
		name      string
		index     MemoryIndex
		reply     string
		wantNudge bool
		wantRef   string
	}{
		{
			name:      "no index suppresses the nudge",
			index:     nil,
			reply:     `{"name":"01JDEPLOY"}`,
			wantNudge: false,
		},
		{
			name:      "empty index suppresses the nudge",
			index:     fakeMemoryIndex{cands: nil},
			reply:     `{"name":"01JDEPLOY"}`,
			wantNudge: false,
		},
		{
			name:      "index error suppresses the nudge",
			index:     fakeMemoryIndex{err: errTest},
			reply:     `{"name":"01JDEPLOY"}`,
			wantNudge: false,
		},
		{
			name:      "model picks a real indexed memory",
			index:     fakeMemoryIndex{cands: cands},
			reply:     `{"name":"01JDEPLOY"}`,
			wantNudge: true,
			wantRef:   "01JDEPLOY",
		},
		{
			name:      "fabricated slug is dropped",
			index:     fakeMemoryIndex{cands: cands},
			reply:     `{"name":"deploy-hygiene"}`,
			wantNudge: false,
		},
		{
			name:      "model declines",
			index:     fakeMemoryIndex{cands: cands},
			reply:     `{}`,
			wantNudge: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAdapter{reply: tt.reply}
			a := newTestAssistant(oneProfileStore(), fa)
			if tt.index != nil {
				a = a.WithMemoryIndex(tt.index)
				// WithMemoryIndex copies the Assistant, so re-pin the test factory.
				a.factory = func(models.Config) (models.ModelAdapter, error) { return fa, nil }
			}
			cfg, _, err := a.resolveConfig(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			n, ok := a.linkMemoryNudge(ctx, cfg, GuidanceRequest{Title: "ship it", Body: "deploy steps"}, "deploy steps")
			if ok != tt.wantNudge {
				t.Fatalf("ok = %v want %v (nudge %+v)", ok, tt.wantNudge, n)
			}
			if tt.wantNudge {
				if n.Kind != GuidanceLinkMemory {
					t.Errorf("kind = %q", n.Kind)
				}
				if n.Apply.InsertRef != tt.wantRef {
					t.Errorf("InsertRef = %q want %q", n.Apply.InsertRef, tt.wantRef)
				}
			}
		})
	}
}

func kindsOf(ns []Nudge) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Kind
	}
	return out
}
