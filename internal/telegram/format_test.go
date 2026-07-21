package telegram

import (
	"strings"
	"testing"
)

// TestSplitBody_BoundaryTable covers the size boundaries around
// BodyMaxChars: short input, exactly-at-cap, one-byte-over, and
// large inputs that force multiple chunks. This is the regression
// guard for fix/telegram-no-truncate — historically a 240-char cap in
// the notify-event body silently truncated worker output before
// SplitBody ever saw it, so we need a load-bearing check that long
// inputs do produce >1 chunk and that no chunk exceeds the cap.
func TestSplitBody_BoundaryTable(t *testing.T) {
	cases := []struct {
		name      string
		inputLen  int
		wantChunk int
	}{
		{"empty input collapses to one empty chunk", 0, 1},
		{"one char fits in one chunk", 1, 1},
		{"one under the cap fits in one chunk", BodyMaxChars - 1, 1},
		{"exactly at cap fits in one chunk", BodyMaxChars, 1},
		{"one over the cap splits to two chunks", BodyMaxChars + 1, 2},
		{"double-cap splits to two chunks", BodyMaxChars * 2, 2},
		{"just over double splits to three chunks", BodyMaxChars*2 + 1, 3},
		{"large input ~8200 chars splits to three chunks", 8200, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Build an all-'a' input — no whitespace forces preferredCut
			// to fall through to the hard cap, which is the worst case
			// for boundary correctness.
			input := strings.Repeat("a", tc.inputLen)
			chunks := SplitBody(input)
			if got := len(chunks); got != tc.wantChunk {
				t.Fatalf("len=%d want %d (input %d chars)", got, tc.wantChunk, tc.inputLen)
			}
			for i, c := range chunks {
				if len(c) > BodyMaxChars {
					t.Errorf("chunk[%d] is %d chars, exceeds BodyMaxChars=%d",
						i, len(c), BodyMaxChars)
				}
			}
		})
	}
}

// TestSplitBody_NoCharLoss verifies the union of every chunk equals
// the original input modulo whitespace trimming. We never want to drop
// content silently — that was the original bug.
func TestSplitBody_NoCharLoss(t *testing.T) {
	input := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200)
	// Total ~9000 chars, mixed whitespace so preferredCut has boundaries
	// to find. Reassembled chunks should contain every non-whitespace
	// rune from the original.
	chunks := SplitBody(input)
	if len(chunks) < 2 {
		t.Fatalf("expected multi-chunk split, got %d", len(chunks))
	}
	reassembled := strings.Join(chunks, " ")
	// Compare with whitespace collapsed — boundary splits insert/strip
	// padding around cut sites, so an exact byte compare would be brittle.
	want := collapseWhitespace(input)
	got := collapseWhitespace(reassembled)
	if want != got {
		t.Errorf("reassembled content does not match input\n  want len=%d\n  got  len=%d",
			len(want), len(got))
	}
}

// TestSplitBody_PrefersBoundary checks that when paragraph or sentence
// boundaries are available inside the search window, SplitBody cuts
// there rather than at the hard cap.
func TestSplitBody_PrefersBoundary(t *testing.T) {
	// 3500 'a' chars + "\n\n" + 3500 'b' chars — the cut should land
	// at the paragraph boundary, so chunk[0] ends with 'a' and chunk[1]
	// starts with 'b'.
	first := strings.Repeat("a", 3500)
	second := strings.Repeat("b", 3500)
	input := first + "\n\n" + second
	chunks := SplitBody(input)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	if !strings.HasSuffix(chunks[0], "a") {
		t.Errorf("chunk[0] should end at the paragraph boundary; ends with %q",
			chunks[0][len(chunks[0])-8:])
	}
	if !strings.HasPrefix(chunks[1], "b") {
		t.Errorf("chunk[1] should start after the paragraph boundary; starts with %q",
			chunks[1][:8])
	}
}

// TestTruncateBody_DoesNotAffectShortInputs is a sanity check that the
// preserved (and discouraged) helper still works for callers that
// genuinely want a single capped chunk — e.g. SSE preview surfaces.
func TestTruncateBody_DoesNotAffectShortInputs(t *testing.T) {
	in := "hello world"
	if got := TruncateBody(in); got != in {
		t.Errorf("TruncateBody(%q) = %q, want unchanged", in, got)
	}
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
