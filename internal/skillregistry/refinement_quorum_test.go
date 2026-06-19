package skillregistry

import (
	"strings"
	"testing"
)

func TestFuzzyFrictionKey(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"lowercased", "Foo BAR", "foo bar"},
		{"trimmed", "   foo bar   ", "foo bar"},
		{"whitespace collapsed", "foo    bar\tbaz", "foo bar baz"},
		{"truncated to 50", strings.Repeat("a", 70), strings.Repeat("a", 50)},
		{"empty stays empty", "", ""},
		{"unicode preserved", "café", "café"},
		{"unicode truncation rune-safe",
			strings.Repeat("é", 60),
			strings.Repeat("é", 50)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FuzzyFrictionKey(tt.in)
			if got != tt.want {
				t.Fatalf("FuzzyFrictionKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQuorumThreshold(t *testing.T) {
	tests := []struct {
		override, want int
	}{
		{0, DefaultRefinementQuorum},
		{-5, DefaultRefinementQuorum},
		{1, 1},
		{5, 5},
		{100, 100},
	}
	for _, tt := range tests {
		got := QuorumThreshold(tt.override)
		if got != tt.want {
			t.Errorf("QuorumThreshold(%d) = %d, want %d", tt.override, got, tt.want)
		}
	}
}

func TestQuorumReached(t *testing.T) {
	tests := []struct {
		name             string
		count, threshold int
		want             bool
	}{
		{"below default", 2, 0, false},
		{"at default", 3, 0, true},
		{"above default", 5, 0, true},
		{"override 1 always reaches", 1, 1, true},
		{"override 10, count 5", 5, 10, false},
		{"override 10, count 10", 10, 10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QuorumReached(tt.count, tt.threshold)
			if got != tt.want {
				t.Fatalf("QuorumReached(%d,%d) = %v, want %v",
					tt.count, tt.threshold, got, tt.want)
			}
		})
	}
}

func TestSimilarFrictionPatternIsKeyToday(t *testing.T) {
	// Locks in the current "pattern == key" behaviour so the milestone-
	// 1 contract is explicit: callers can substitute the key for the
	// pattern. When the aggregator gets smarter, this test changes
	// AND CountSimilarProposals's call site updates together.
	in := "FFmpeg Fails  on H.265"
	if got := SimilarFrictionPattern(in); got != FuzzyFrictionKey(in) {
		t.Fatalf("SimilarFrictionPattern(%q) = %q, want %q",
			in, got, FuzzyFrictionKey(in))
	}
}
