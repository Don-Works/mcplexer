package runner

import "testing"

// TestHasMeshOutputChannel covers the predicate that drives the
// finished-signal-summary suppression. The original bug: when mesh was
// an output channel, the worker.finished signal also carried a 200-char
// truncated summary alongside the full output emission — two messages
// with overlapping content per run-end.
func TestHasMeshOutputChannel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   string
		wantMsh bool
	}{
		{"empty", "", false},
		{"single mesh", `[{"type":"mesh","priority":"normal"}]`, true},
		{"single mesh case-insensitive", `[{"type":"Mesh"}]`, true},
		{"slack only", `[{"type":"slack","channel":"#ops"}]`, false},
		{"mixed includes mesh", `[{"type":"webhook","url":"https://x"},{"type":"mesh"}]`, true},
		{"mixed excludes mesh", `[{"type":"webhook","url":"https://x"},{"type":"file","path":"/tmp/x"}]`, false},
		{"parse error fails closed (no suppression)", `not json`, false},
		{"empty array", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasMeshOutputChannel(tc.input); got != tc.wantMsh {
				t.Errorf("hasMeshOutputChannel(%q) = %v, want %v", tc.input, got, tc.wantMsh)
			}
		})
	}
}
