package idtrunc

import "testing"

func TestShort(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty/n8", "", 8, ""},
		{"short/n8", "abc", 8, "abc"},
		{"exact/n8", "abcdefgh", 8, "abcdefgh"},
		{"longer/n8", "abcdefghij", 8, "abcdefgh"},
		{"n0", "abcdef", 0, ""},
		{"n_negative", "abcdef", -1, ""},
		{"n_larger_than_len", "ab", 100, "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Short(tc.in, tc.n); got != tc.want {
				t.Errorf("Short(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestEllipsis(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		head, tail int
		want       string
	}{
		{"empty", "", 8, 8, ""},
		{"short_lt_headtail", "abcdef", 4, 4, "abcdef"},
		{"exact_eq_headtail", "abcdefgh", 4, 4, "abcd…efgh"},
		{"longer", "abcdefghij", 4, 4, "abcd…ghij"},
		{"head_only", "abcdefghij", 6, 0, "abcdef…"},
		{"tail_only", "abcdefghij", 0, 4, "…ghij"},
		{"negative_head", "abcdef", -1, 2, "…ef"},
		{"head_plus_tail_zero_empty", "", 0, 0, ""},
		{"long_session", "0123456789abcdef0123456789abcdef", 8, 8, "01234567…89abcdef"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Ellipsis(tc.in, tc.head, tc.tail); got != tc.want {
				t.Errorf("Ellipsis(%q, %d, %d) = %q, want %q", tc.in, tc.head, tc.tail, got, tc.want)
			}
		})
	}
}

// TestNoPanicOnEmpty is a belt-and-braces guard. The whole point of the
// package is to never panic on empty input, so we exercise the cases that
// historically did panic at the call sites.
func TestNoPanicOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	_ = Short("", 8)
	_ = Ellipsis("", 8, 8)
	_ = Ellipsis("", 6, 8)
	_ = Ellipsis("", 8, 4)
}
