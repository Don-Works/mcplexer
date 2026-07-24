package runner

import "testing"

// TestPreambleForProvider proves the CLI variant is selected only for
// CLI-backed adapters, and that omitting it is a no-op — the fallback is what
// keeps existing callers (and every test that wires only Deps.Preamble) on
// exactly their previous behaviour.
func TestPreambleForProvider(t *testing.T) {
	const api, cli = "API-PREAMBLE", "CLI-PREAMBLE"

	cases := []struct {
		provider string
		want     string
	}{
		// CLI-backed: the adapter shells out to a coding agent that runs its
		// own loop and never sees the runner's tool list, so the API
		// preamble's exact three-tool claim would be false.
		{"pi_cli", cli},
		{"claude_cli", cli},
		{"opencode_cli", cli},
		{"grok_cli", cli},
		{"mimo_cli", cli},
		{"gemini_cli", cli},
		{"codex_cli", cli},
		// API providers must be untouched by this change.
		{"anthropic", api},
		{"openai", api},
		{"openai_compat", api},
		{"", api},
	}
	for _, tc := range cases {
		if got := preambleForProvider(tc.provider, api, cli); got != tc.want {
			t.Errorf("preambleForProvider(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}

	// An unwired CLI variant falls back rather than blanking the preamble.
	if got := preambleForProvider("pi_cli", api, ""); got != api {
		t.Errorf("empty cli variant must fall back to the default, got %q", got)
	}
}
