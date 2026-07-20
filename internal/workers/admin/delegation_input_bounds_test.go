package admin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestNormalizeDelegationInputBoundsRepoBriefAndHandoff proves the two
// caller-supplied prompt sections are capped. Before this bound existed the
// only backstop was runner.maxUserPromptBytes (128 KB ≈ 32k tokens), which a
// 100k-window local model cannot absorb.
func TestNormalizeDelegationInputBoundsRepoBriefAndHandoff(t *testing.T) {
	svc := &Service{clock: realClock{}}

	in := &DelegationInput{
		WorkspaceID: "ws", Objective: "Do the thing",
		RepoBrief:       strings.Repeat("brief line\n", 8*1024), // ~88 KB
		Handoff:         strings.Repeat("handoff line\n", 8*1024),
		ModelProvider:   "grok_cli",
		ModelID:         "grok-build",
		SecretScopeID:   "scope-test",
		WorkerIsolation: "none",
	}
	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	// Truncated, not rejected — a delegation that runs on a trimmed brief
	// beats one that never starts.
	if len(in.RepoBrief) > maxRepoBriefBytes+len(truncateMarker)+64 {
		t.Errorf("repo_brief not bounded: %d bytes", len(in.RepoBrief))
	}
	if len(in.Handoff) > maxHandoffBytes+len(truncateMarker)+64 {
		t.Errorf("handoff not bounded: %d bytes", len(in.Handoff))
	}
	for _, s := range []string{in.RepoBrief, in.Handoff} {
		if !strings.Contains(s, "truncated by mcplexer") {
			t.Errorf("truncated section is missing its marker: %q", tail(s))
		}
	}
	if len(in.truncationWarnings) != 2 {
		t.Fatalf("want 2 truncation warnings, got %d: %v", len(in.truncationWarnings), in.truncationWarnings)
	}
	// The warning has to name the field and the drop size, or the parent
	// cannot tell an under-performing worker from an under-briefed one.
	joined := strings.Join(in.truncationWarnings, "\n")
	for _, want := range []string{"repo_brief truncated", "handoff truncated", "dropped"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
}

// TestNormalizeDelegationInputLeavesInBudgetInputAlone is the no-regression
// half: ordinary-sized briefs must pass through byte-identical and warn-free.
func TestNormalizeDelegationInputLeavesInBudgetInputAlone(t *testing.T) {
	svc := &Service{clock: realClock{}}
	brief := strings.Repeat("small brief\n", 32)
	handoff := "Fix the parser. Acceptance: go test ./internal/parse/ passes."

	in := &DelegationInput{
		WorkspaceID: "ws", Objective: "Fix the parser",
		RepoBrief: brief, Handoff: handoff,
		ModelProvider:   "grok_cli",
		ModelID:         "grok-build",
		SecretScopeID:   "scope-test",
		WorkerIsolation: "none",
	}
	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if in.RepoBrief != strings.TrimSpace(brief) {
		t.Error("in-budget repo_brief was modified")
	}
	if in.Handoff != handoff {
		t.Error("in-budget handoff was modified")
	}
	if len(in.truncationWarnings) != 0 {
		t.Errorf("unexpected warnings: %v", in.truncationWarnings)
	}
}

// TestTruncateSectionIsRuneSafe guards the seam: a cut that splits a UTF-8
// rune would make the worker read a replacement char, and a marker sliced in
// half is unparseable garbage.
func TestTruncateSectionIsRuneSafe(t *testing.T) {
	// Multi-byte runes with no newlines, so the cut lands mid-rune unless
	// the helper walks back to a boundary.
	s := strings.Repeat("日本語テキスト", 4096)
	out, warn := truncateSection("repo_brief", s, 1000)
	if warn == "" {
		t.Fatal("expected a truncation warning")
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncated output is not valid UTF-8")
	}
	if !strings.Contains(out, "truncated by mcplexer") {
		t.Error("missing truncation marker")
	}
}

// TestTruncateSectionPrefersLineBoundary keeps the kept window ending on a
// whole line when one is close, so the worker doesn't read a half-sentence.
func TestTruncateSectionPrefersLineBoundary(t *testing.T) {
	s := strings.Repeat("aaaaaaaaa\n", 500) // 5000 bytes, 10-byte lines
	out, warn := truncateSection("handoff", s, 1000)
	if warn == "" {
		t.Fatal("expected a truncation warning")
	}
	kept := out[:strings.Index(out, "\n\n[…truncated")]
	if !strings.HasSuffix(kept, "\n") {
		t.Errorf("cut mid-line, want a whole-line boundary: %q", tail(kept))
	}
}

func tail(s string) string {
	if len(s) <= 120 {
		return s
	}
	return "…" + s[len(s)-120:]
}
