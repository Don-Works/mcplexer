package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCCRKeysIgnoresNonMarkerHex(t *testing.T) {
	// Bare `key=<hex>` substrings (URL params, config dumps, git refs) must NOT
	// be treated as CCR markers, or they'd falsely trip the kill-switch.
	noise := `GET /x?key=deadbeefdeadbeefdeadbeef  config: key=aaaaaaaaaaaaaaaaaaaaaaaa`
	if keys := ParseCCRKeys(noise); len(keys) != 0 {
		t.Errorf("non-marker key= substrings must be ignored, got %v", keys)
	}
	real := CCRMarker(CCRKey([]byte("payload")), 100)
	if keys := ParseCCRKeys("pre " + real + " post"); len(keys) != 1 {
		t.Errorf("a real marker must still parse, got %v", keys)
	}
}

func TestOversizeTruncatePreservesValidUTF8AtSeams(t *testing.T) {
	// Multibyte content so the head/tail byte cuts land mid-rune without the fix.
	big := strings.Repeat("héllo wörld 日本語 ", 2000)
	out, changed, _ := oversizeTruncate{}.ApplyWithStash(fixtureText(big))
	if !changed {
		t.Fatal("expected truncation of an oversize multibyte payload")
	}
	var e struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	kept := e.Content[0].Text
	head := kept[:strings.Index(kept, "\n[[ccr")]
	if !strings.HasPrefix(big, head) {
		t.Error("head preview is not a byte-exact prefix of the original — a rune was split at the seam")
	}
	if strings.ContainsRune(kept, '�') {
		t.Error("preview contains U+FFFD — a rune was split at a seam")
	}
}

func TestCCRKeyStableAndDistinct(t *testing.T) {
	a := CCRKey([]byte("hello world"))
	if a != CCRKey([]byte("hello world")) {
		t.Fatal("CCRKey not stable for identical input")
	}
	if len(a) != 24 {
		t.Fatalf("key length %d, want 24", len(a))
	}
	if a == CCRKey([]byte("different")) {
		t.Error("distinct payloads produced the same key")
	}
}

func TestCCRMarkerParse(t *testing.T) {
	key := CCRKey([]byte("payload"))
	marker := CCRMarker(key, 4321)
	keys := ParseCCRKeys("head " + marker + " tail")
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("ParseCCRKeys = %v, want [%s]", keys, key)
	}
	if len(ParseCCRKeys("no markers here")) != 0 {
		t.Error("expected no keys in marker-free text")
	}
}

func TestOversizeTruncateStashesAndAddressesOriginal(t *testing.T) {
	big := strings.Repeat("abcdefghij", 2000) // 20000 bytes
	env := fixtureText(big)

	out, changed, stash := oversizeTruncate{}.ApplyWithStash(env)
	if !changed {
		t.Fatal("expected truncation of an oversize payload")
	}
	if len(stash) != 1 {
		t.Fatalf("expected 1 stashed original, got %d", len(stash))
	}
	if string(stash[0]) != big {
		t.Error("stashed original does not equal the input text")
	}
	if len(out) >= len(env) {
		t.Errorf("output not smaller than input: %d >= %d", len(out), len(env))
	}
	keys := ParseCCRKeys(string(out))
	if len(keys) != 1 || keys[0] != CCRKey(stash[0]) {
		t.Fatalf("marker key %v does not address the stashed original %s", keys, CCRKey(stash[0]))
	}
}

func TestOversizeTruncateNoopBelowThreshold(t *testing.T) {
	env := fixtureText("small payload, nothing to truncate")
	out, changed, stash := oversizeTruncate{}.ApplyWithStash(env)
	if changed || stash != nil || string(out) != string(env) {
		t.Fatal("a below-threshold payload must be returned untouched with no stash")
	}
}
