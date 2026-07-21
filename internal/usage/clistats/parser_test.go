package clistats

import (
	"os"
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello", "hello"},
		{"bold", "\x1b[1mhello\x1b[0m", "hello"},
		{"color", "\x1b[32m1.5K\x1b[0m", "1.5K"},
		{"nested", "\x1b[1;31m\x1b[4mtest\x1b[0m\x1b[0m", "test"},
		{"cursor movement", "\x1b[1Ahello", "hello"},
		{"OSC title", "\x1b]0;private title\x07hello", "hello"},
		{"no escape", "no codes here", "no codes here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.input); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseNumericSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"plain", "1234", 1234},
		{"comma", "1,234", 1234},
		{"large comma", "1,234,567", 1234567},
		{"K suffix", "1.5K", 1500},
		{"k lowercase", "2k", 2000},
		{"M suffix", "2M", 2000000},
		{"m lowercase", "1.5m", 1500000},
		{"B suffix", "1B", 1000000000},
		{"comma+K", "1,000K", 1000000},
		{"ansi+K", "\x1b[32m1.5K\x1b[0m", 1500},
		{"ansi+comma", "\x1b[1m1,234\x1b[0m", 1234},
		{"empty", "", 0},
		{"dash", "-", 0},
		{"zero", "0", 0},
		{"decimal", "1234.56", 1235},
		{"M suffix decimal", "1.23M", 1230000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseNumericSuffix(tt.input); got != tt.want {
				t.Errorf("ParseNumericSuffix(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseUSD(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  float64
	}{
		{"plain", "1.23", 1.23},
		{"dollar sign", "$1.23", 1.23},
		{"ansi+dollar", "\x1b[32m$5.00\x1b[0m", 5.0},
		{"empty", "", 0},
		{"dash", "-", 0},
		{"zero", "0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseUSD(tt.input); got != tt.want {
				t.Errorf("ParseUSD(%q) = %f, want %f", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseModelStatsTable(t *testing.T) {
	lines := []string{
		"Model | Requests | Input Tokens | Output Tokens | Cost",
		"--- | --- | --- | --- | ---",
		"\x1b[1mclaude-sonnet-4\x1b[0m | \x1b[32m1,234\x1b[0m | \x1b[33m5.2M\x1b[0m | \x1b[33m1.1M\x1b[0m | \x1b[31m$12.50\x1b[0m",
		"gpt-4o | 500 | 2,000,000 | 500,000 | $3.00",
		"TOTAL | 1,734 | 7,200,000 | 1,600,000 | $15.50",
	}
	results := ParseModelStatsTable(lines)
	if len(results) != 2 {
		t.Fatalf("expected 2 models, got %d", len(results))
	}
	if results[0].Model != "claude-sonnet-4" {
		t.Errorf("model[0] = %q", results[0].Model)
	}
	if results[0].Requests != 1234 {
		t.Errorf("requests[0] = %d, want 1234", results[0].Requests)
	}
	if results[0].InputTokens != 5200000 {
		t.Errorf("input[0] = %d, want 5200000", results[0].InputTokens)
	}
	if results[0].OutputTokens != 1100000 {
		t.Errorf("output[0] = %d, want 1100000", results[0].OutputTokens)
	}
	if results[0].CostUSD != 12.50 {
		t.Errorf("cost[0] = %f, want 12.50", results[0].CostUSD)
	}
	if results[1].Model != "gpt-4o" {
		t.Errorf("model[1] = %q", results[1].Model)
	}
	if results[1].Requests != 500 {
		t.Errorf("requests[1] = %d, want 500", results[1].Requests)
	}
}

func TestParseModelStatsBlocksRealFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/mimo_stats.txt")
	if err != nil {
		t.Fatal(err)
	}
	results := ParseModelStatsTable(strings.Split(string(raw), "\n"))
	if len(results) != 2 {
		t.Fatalf("models = %d, want 2: %+v", len(results), results)
	}
	first := results[0]
	if first.Model != "xiaomi/mimo-v2.5-pro" || first.Requests != 339 {
		t.Fatalf("first model = %+v", first)
	}
	if first.InputTokens != 2_800_000 || first.OutputTokens != 132_600 {
		t.Errorf("first token counts = %+v", first)
	}
	if first.CacheReadTokens != 33_400_000 || first.CacheWriteTokens != 0 {
		t.Errorf("first cache counts = %+v", first)
	}
	if first.CostUSD != 1.4539 {
		t.Errorf("first cost = %f, want 1.4539", first.CostUSD)
	}
	second := results[1]
	if second.Model != "anthropic/claude-opus-4-8" || second.CacheWriteTokens != 1_500_000 {
		t.Errorf("second model = %+v", second)
	}
}

func TestParseModelStatsBlocksANSIAndMalformed(t *testing.T) {
	lines := []string{
		"\x1b[1;35m│                      MODEL USAGE                       │\x1b[0m",
		"│  Messages                                          999 │", // orphan metric
		"├────────────────────────────────────────────────────────┤",
		"│ \x1b[36mopenrouter/example-model\x1b[0m                            │",
		"│  Messages                                           12 │",
		"│  Input Tokens                                      nope │",
		"│  Output Tokens                                     1.2K │",
		"│  Cache Read                                           - │",
		"│  Cache Write                                         25 │",
		"│  Cost                                            $0.125 │",
		"\x1b[1A└────────────────────────────────────────────────────────┘",
		"│                      TOOL USAGE                        │",
	}
	results := ParseModelStatsTable(lines)
	if len(results) != 1 {
		t.Fatalf("models = %d, want 1: %+v", len(results), results)
	}
	got := results[0]
	if got.Model != "openrouter/example-model" || got.Requests != 12 {
		t.Errorf("model = %+v", got)
	}
	if got.InputTokens != 0 || got.OutputTokens != 1200 || got.CacheWriteTokens != 25 {
		t.Errorf("malformed numeric handling = %+v", got)
	}
}
