package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogCompressorKeepsErrorsDropsNoise(t *testing.T) {
	orig := logFixtureText()
	out, changed, stash := logCompressor{}.ApplyWithStash(fixtureText(orig))
	if !changed {
		t.Fatal("expected log compaction")
	}
	if len(stash) != 1 || string(stash[0]) != orig {
		t.Fatal("the exact original must be stashed for recovery")
	}
	kept := textOf(t, out)
	for _, must := range []string{"ERROR connection refused", "at net.Dial", "WARN  slow query", "ERROR upstream still down"} {
		if !strings.Contains(kept, must) {
			t.Errorf("must-keep line dropped: %q", must)
		}
	}
	if strings.Contains(kept, "cache warm complete") || strings.Contains(kept, "metrics flushed") {
		t.Error("low-severity DEBUG/INFO noise was not dropped")
	}
	if len(out) >= len(fixtureText(orig)) {
		t.Error("expected a smaller output")
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Errorf("marker does not address the stashed original: %v", keys)
	}
}

func TestLogCompressorNoopOnNonLog(t *testing.T) {
	out, changed, _ := logCompressor{}.ApplyWithStash(fixtureText("just a short note\nnot a log at all"))
	if changed {
		t.Fatalf("non-log text must be untouched, got: %s", out)
	}
}

func TestStructuredDedupReplacesDuplicateText(t *testing.T) {
	env := structuredDupFixture()
	out, changed, stash := structuredDedup{}.ApplyWithStash(env)
	if !changed {
		t.Fatal("expected dedup when content text duplicates structuredContent")
	}
	if len(stash) != 1 {
		t.Fatal("the original text must be stashed")
	}
	if len(out) >= len(env) {
		t.Error("expected a smaller output after dropping the duplicate text")
	}
	var e struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(out, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(e.StructuredContent) == 0 {
		t.Error("structuredContent must be preserved (it carries the data)")
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Errorf("marker does not address the stashed original: %v", keys)
	}
}

func TestStructuredDedupNoopWithoutStructuredContent(t *testing.T) {
	out, changed, _ := structuredDedup{}.ApplyWithStash(fixtureText(`{"a":1,"b":2}`))
	if changed {
		t.Fatalf("no structuredContent → must be a no-op, got: %s", out)
	}
}
