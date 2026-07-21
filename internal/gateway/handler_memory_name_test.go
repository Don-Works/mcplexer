package gateway

import (
	"strings"
	"testing"
)

// TestDeriveMemoryName covers the auto-derived memory__save name: stable,
// kebab-case, bounded, and distinct for distinct content sharing a prefix.
func TestDeriveMemoryName(t *testing.T) {
	a := deriveMemoryName("The dashboard listens on port 13333 with API auth.")
	if a != deriveMemoryName("The dashboard listens on port 13333 with API auth.") {
		t.Fatal("derived name must be deterministic for identical content")
	}
	if !strings.HasPrefix(a, "the-dashboard-listens-on-port-13333-") {
		t.Fatalf("derived name should lead with content words, got %q", a)
	}

	b := deriveMemoryName("The dashboard listens on port 13333 but requires no auth at all.")
	if a == b {
		t.Fatalf("distinct content sharing a word prefix must derive distinct names, both %q", a)
	}

	// Symbol-only content falls back to the hashed form.
	c := deriveMemoryName("!!! ??? ***")
	if !strings.HasPrefix(c, "memory-") {
		t.Fatalf("symbol-only content should fall back to memory-<hash>, got %q", c)
	}

	// Long content stays bounded: <=48 chars of words + "-" + 8 hash chars.
	d := deriveMemoryName(strings.Repeat("supercalifragilistic ", 20))
	if len(d) > 48+1+8 {
		t.Fatalf("derived name too long (%d): %q", len(d), d)
	}
}
