package gateway

import "testing"

// TestHarnessProfileForClient_Pi verifies the Pi coding agent (pi.dev /
// Earendil) is classified HarnessDirect — Pi reaches the gateway through its
// native extension's CLI shim (raw MCP tools/call, advertised names verbatim)
// or the pi-mcp-adapter proxy tool, neither of which server-prefixes names.
// "picoclaw" must stay HarnessServerPrefixed despite sharing the "pi" prefix.
func TestHarnessProfileForClient_Pi(t *testing.T) {
	tests := []struct {
		client string
		want   HarnessProfile
	}{
		{"pi", HarnessDirect},
		{"Pi", HarnessDirect},
		{"pi-coding-agent", HarnessDirect},
		{"@earendil-works/pi-coding-agent", HarnessDirect},
		{"@mariozechner/pi-coding-agent", HarnessDirect},
		{"pi cli", HarnessDirect},
		{"Earendil Pi", HarnessDirect},
		{"earendil", HarnessDirect},
		// Must NOT be swept into Pi: picoclaw stays server-prefixed.
		{"picoclaw", HarnessServerPrefixed},
		{"Picoclaw", HarnessServerPrefixed},
		// Sanity: a name that merely contains "pi" mid-word is not Pi.
		{"happiness-cli", HarnessDirect}, // not Pi, not in prefixed list → default Direct
	}
	for _, tc := range tests {
		if got := harnessProfileForClient(tc.client); got != tc.want {
			t.Errorf("harnessProfileForClient(%q) = %v, want %v", tc.client, got, tc.want)
		}
	}
}

// TestHarnessKeyForClientInfo_Pi verifies Pi clientInfo.name values map to the
// stable "pi" harness key, and that lookalikes do not.
func TestHarnessKeyForClientInfo_Pi(t *testing.T) {
	tests := []struct {
		name    string
		wantKey string
		wantOK  bool
	}{
		{"MiMoCode", "mimo", true},
		{"mimo_cli", "mimo", true},
		{"pi", "pi", true},
		{"pi-coding-agent", "pi", true},
		{"@earendil-works/pi-coding-agent", "pi", true},
		{"@mariozechner/pi-coding-agent", "pi", true},
		{"pi.dev", "pi", true},
		{"Earendil", "pi", true},
		{"pi cli", "pi", true},
		// Lookalikes must NOT be mapped to the pi harness key.
		{"picoclaw", "", false},
		{"raspberry-pi", "", false},
		{"copilot", "", false},
		{"unknown-agent", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		gotKey, gotOK := harnessKeyForClientInfo(tc.name)
		if gotKey != tc.wantKey || gotOK != tc.wantOK {
			t.Errorf("harnessKeyForClientInfo(%q) = (%q, %v), want (%q, %v)",
				tc.name, gotKey, gotOK, tc.wantKey, tc.wantOK)
		}
	}
}

// TestIsPiHarness exercises the token-boundary logic directly so the
// picoclaw / happiness false-positive guards are pinned.
func TestIsPiHarness(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"pi", true},
		{"pi-coding-agent", true},
		{"@mariozechner/pi-coding-agent", true},
		{"pi.dev", true},
		{"pi_agent", true},
		{"pi/coding-agent", true},
		{"pi cli", true},
		{"earendil-works", true},
		{"@earendil/pi", true},
		{"picoclaw", false},
		{"pixel", false},
		{"pip", false},
		{"copilot", false},
		{"openai", false},
		{"cursor", false},
		{"raspberry-pi", false},
		{"happiness", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isPiHarness(tc.in); got != tc.want {
			t.Errorf("isPiHarness(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
