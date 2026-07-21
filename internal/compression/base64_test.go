package compression

import (
	"strings"
	"testing"
)

func TestBase64ExternalizeDataURI(t *testing.T) {
	blob := "data:image/png;base64," + strings.Repeat("iVBORw0KGgoAAAANSUhEUg", 100) + "=="
	orig := "screenshot attached:\n" + blob + "\nend of result"
	out, changed, stash := base64Externalize{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected data URI to be externalized")
	}
	if len(stash) != 1 || string(stash[0]) != blob {
		t.Fatalf("the exact blob must be stashed, got %d entries", len(stash))
	}
	kept := textOf(t, out)
	if strings.Contains(kept, "iVBORw0KGgo") {
		t.Error("blob bytes survived inline")
	}
	for _, must := range []string{"screenshot attached:", "end of result"} {
		if !strings.Contains(kept, must) {
			t.Errorf("surrounding text %q lost", must)
		}
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Errorf("marker does not address the stashed blob: %v", keys)
	}
}

func TestBase64ExternalizeMultipleBlobs(t *testing.T) {
	blobA := strings.Repeat("QUJDREVGR0hJSktMTU5PUA", 80) // mixed case → passes guard
	blobB := "data:application/pdf;base64," + strings.Repeat("JVBERi0xLjQKJcOkw7zDtsO", 60)
	orig := "a: " + blobA + "\nb: " + blobB
	out, changed, stash := base64Externalize{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected both blobs to be externalized")
	}
	if len(stash) != 2 {
		t.Fatalf("want 2 stashed blobs, got %d", len(stash))
	}
	keys := ParseCCRKeys(string(textOf(t, out)))
	if len(keys) != 2 {
		t.Fatalf("want 2 distinct markers, got %v", keys)
	}
	if keys[0] != CCRKey(stash[0]) || keys[1] != CCRKey(stash[1]) {
		t.Errorf("markers must address their blobs in order: %v", keys)
	}
}

// TestBase64ExternalizeGuards: long runs that merely share base64's alphabet
// — lowercase hex dumps, all-caps identifier walls — must be left alone.
func TestBase64ExternalizeGuards(t *testing.T) {
	for name, run := range map[string]string{
		"lowercase-hex": strings.Repeat("deadbeef0123456789abcdef", 60),
		"all-caps":      strings.Repeat("ABCDEFGHIJKLMNOPQRSTUVWX", 60),
		"digits":        strings.Repeat("0123456789", 120),
	} {
		_, changed, _ := base64Externalize{}.ApplyWithStash(fixtureText("dump:\n" + run))
		if changed {
			t.Errorf("%s: non-base64 run was externalized", name)
		}
	}
}

func TestBase64ExternalizeNoopBelowThreshold(t *testing.T) {
	short := "data:image/png;base64," + strings.Repeat("iVBORw0KGgo", 20) // ~220 chars
	_, changed, _ := base64Externalize{}.ApplyWithStash(fixtureText("x: " + short))
	if changed {
		t.Fatal("short blobs must not be externalized")
	}
}
