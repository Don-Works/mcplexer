package index

import (
	"reflect"
	"testing"
)

func TestSplitIdent(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"HandleKVSet", []string{"handle", "kv", "set"}},
		{"HTTPServer", []string{"http", "server"}},
		{"internal/api/foo_bar.go", []string{"internal", "api", "foo", "bar", "go"}},
		{"parseURLPath", []string{"parse", "url", "path"}},
		{"snake_case_name", []string{"snake", "case", "name"}},
		{"kebab-case-thing", []string{"kebab", "case", "thing"}},
		{"already", []string{"already"}},
	}
	for _, c := range cases {
		got := splitIdent(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitIdent(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if got := splitIdent(""); len(got) != 0 {
		t.Errorf("splitIdent(\"\") = %v, want empty", got)
	}
}

func TestTokenString(t *testing.T) {
	if got := tokenString("HandleKVSet"); got != "handle kv set" {
		t.Errorf("tokenString = %q, want %q", got, "handle kv set")
	}
}

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 1},      // ceil(3/3.5) = 1
		{"abcd", 2},     // ceil(4/3.5) = 2
		{"1234567", 2},  // ceil(7/3.5) = 2
		{"12345678", 3}, // ceil(8/3.5) = 3
	}
	for _, c := range cases {
		if got := estimateTokens(c.in); got != c.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
