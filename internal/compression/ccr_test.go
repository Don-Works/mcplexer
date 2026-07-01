package compression

import (
	"strings"
	"testing"
)

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
