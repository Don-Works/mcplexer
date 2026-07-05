package compression

import (
	"strings"
	"testing"
)

func TestAnsiStripRemovesEscapesAndStashes(t *testing.T) {
	orig := ansiFixtureText()
	out, changed, stash := ansiStrip{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected ANSI-saturated text to change")
	}
	if len(stash) != 1 || string(stash[0]) != orig {
		t.Fatal("the exact original must be stashed for recovery")
	}
	kept := textOf(t, out)
	if strings.Contains(kept, "\x1b") {
		t.Errorf("escape sequences survived: %q", kept)
	}
	for _, must := range []string{"test passed", "✓", "✗ one failure"} {
		if !strings.Contains(kept, must) {
			t.Errorf("visible text %q lost", must)
		}
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Errorf("marker does not address the stashed original: %v", keys)
	}
}

func TestAnsiStripNoopBelowThreshold(t *testing.T) {
	// A handful of escapes is not worth a marker.
	_, changed, _ := ansiStrip{}.ApplyWithStash(fixtureText("\x1b[32mok\x1b[0m fine"))
	if changed {
		t.Fatal("small ANSI savings must not fire the transform")
	}
}

func TestCRCollapseKeepsFinalFrame(t *testing.T) {
	orig := progressFixtureText()
	out, changed, stash := crCollapse{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected progress frames to collapse")
	}
	if len(stash) != 1 || string(stash[0]) != orig {
		t.Fatal("the exact original must be stashed")
	}
	kept := textOf(t, out)
	if !strings.Contains(kept, "Downloading package [##########] 100%") {
		t.Errorf("final frame lost:\n%q", kept)
	}
	if !strings.Contains(kept, "done") {
		t.Errorf("trailing line lost:\n%q", kept)
	}
	if strings.Contains(kept, "] 50%") {
		t.Errorf("intermediate frame survived:\n%q", kept)
	}
}

func TestCRCollapsePreservesCRLF(t *testing.T) {
	// CRLF line endings are line breaks, not progress frames — a CRLF file
	// must pass through unchanged (below threshold, nothing collapsed).
	text := strings.Repeat("a windows line\r\n", 40)
	_, changed, _ := crCollapse{}.ApplyWithStash(fixtureText(text))
	if changed {
		t.Fatal("pure CRLF text must not be rewritten")
	}
	// And mixed content: frames collapse, CRLF bodies survive byte-exact.
	got := collapseCRFrames("frame1\rframe2\rfinal\r\nnext line\r\n")
	want := "final\r\nnext line\r\n"
	if got != want {
		t.Errorf("collapseCRFrames = %q, want %q", got, want)
	}
}

func TestRepeatCollapseExactCounts(t *testing.T) {
	line := "Retrying connection to upstream:9000 in 5s"
	orig := strings.Repeat(line+"\n", 60) + "ERROR gave up after 60 attempts"
	out, changed, stash := repeatCollapse{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected repeated lines to collapse")
	}
	if len(stash) != 1 || string(stash[0]) != orig {
		t.Fatal("the exact original must be stashed")
	}
	kept := textOf(t, out)
	if !strings.Contains(kept, line) {
		t.Error("the repeated line itself must survive once")
	}
	if !strings.Contains(kept, "[previous line repeated 59 more times]") {
		t.Errorf("exact repeat count missing:\n%q", kept)
	}
	if !strings.Contains(kept, "ERROR gave up after 60 attempts") {
		t.Error("trailing error line lost")
	}
	if n := strings.Count(kept, line); n != 1 {
		t.Errorf("repeated line should appear exactly once, got %d:\n%q", n, kept)
	}
}

func TestRepeatCollapseShortRunsUntouched(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("only three times\n", 3))
	for i := range 20 {
		b.WriteString("unique filler line ")
		b.WriteString(strings.Repeat("x", i+1))
		b.WriteString("\n")
	}
	text := b.String()
	got := collapseRepeatedLines(text)
	if got != text {
		t.Errorf("runs below repeatMinRun must be untouched:\n%q", got)
	}
}

func TestRepeatCollapseBlankRunsUntouched(t *testing.T) {
	text := "a" + strings.Repeat("\n", 30) + "b"
	got := collapseRepeatedLines(text)
	if got != text {
		t.Errorf("blank-line runs must be untouched:\n%q", got)
	}
}
