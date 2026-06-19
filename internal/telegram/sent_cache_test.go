package telegram

import "testing"

func TestSentCache_PutGet(t *testing.T) {
	c := newSentCache()
	c.Put("telegram", "1", "100", "mesh-a")
	if got := c.Get("telegram", "1", "100"); got != "mesh-a" {
		t.Fatalf("want mesh-a, got %q", got)
	}
	if got := c.Get("telegram", "1", "999"); got != "" {
		t.Fatalf("unexpected hit: %q", got)
	}
}

func TestSentCache_Overwrites(t *testing.T) {
	c := newSentCache()
	c.Put("telegram", "1", "100", "mesh-a")
	c.Put("telegram", "1", "100", "mesh-b")
	if got := c.Get("telegram", "1", "100"); got != "mesh-b" {
		t.Fatalf("want mesh-b, got %q", got)
	}
}

func TestSentCache_Evicts(t *testing.T) {
	c := newSentCache()
	c.capacity = 3
	c.Put("telegram", "1", "100", "A")
	c.Put("telegram", "1", "101", "B")
	c.Put("telegram", "1", "102", "C")
	c.Put("telegram", "1", "103", "D") // evicts A
	if got := c.Get("telegram", "1", "100"); got != "" {
		t.Errorf("expected A evicted, got %q", got)
	}
	if got := c.Get("telegram", "1", "103"); got != "D" {
		t.Errorf("want D, got %q", got)
	}
}
