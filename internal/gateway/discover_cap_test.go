package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestFormatDiscoverResults_CapsFullOutput is the regression test for the
// bug where detail:"full" rendered every matched tool's TypeScript signature
// with no cap — up to ~200 tools / ~81k chars, blowing the MCP result token
// cap. The full-signature block must be bounded (top-K by score + a byte
// budget), the highest-scored tool must survive, the lowest must be dropped
// from the Code-API block, and an explicit "capped" note must tell the agent
// how to fetch the rest.
func TestFormatDiscoverResults_CapsFullOutput(t *testing.T) {
	const n = 120
	// A schema verbose enough that 120 of them blow the byte budget.
	schema := json.RawMessage(`{"type":"object","properties":{` +
		`"alpha":{"type":"string","description":"the first parameter with a longish description"},` +
		`"bravo":{"type":"string","description":"the second parameter with a longish description"},` +
		`"charlie":{"type":"number","description":"the third parameter with a longish description"},` +
		`"delta":{"type":"boolean","description":"the fourth parameter with a longish description"}` +
		`},"required":["alpha"]}`)

	matches := make([]Tool, n)
	ranked := make([]rankedTool, n)
	allMatched := make(map[string]Tool, n)
	for i := range n {
		tool := Tool{
			Name:        fmt.Sprintf("svc__tool%03d", i),
			Description: "tool number " + fmt.Sprint(i),
			InputSchema: schema,
		}
		matches[i] = tool
		ranked[i] = rankedTool{Tool: tool, Score: float64(n - i)} // tool000 highest
		allMatched[tool.Name] = tool
	}
	results := []discoverQueryResult{{query: "svc", matches: matches, ranked: ranked}}

	output := formatDiscoverResults(results, allMatched, 0)

	// The Code-API (full signature) block must be bounded.
	idx := strings.Index(output, "## Code API")
	if idx < 0 {
		t.Fatal("missing Code API section")
	}
	codeAPI := output[idx:]
	if len(codeAPI) > maxFullDetailBytes+2000 { // budget + header/note slack
		t.Fatalf("Code API block not capped: %d bytes", len(codeAPI))
	}

	// Must render strictly fewer than all matches, with an explicit note.
	sigCount := strings.Count(codeAPI, "): any;")
	if sigCount == 0 || sigCount >= n {
		t.Fatalf("rendered %d signatures, want >0 and <%d", sigCount, n)
	}
	if !strings.Contains(codeAPI, "output capped") || !strings.Contains(codeAPI, "more matched") {
		t.Fatalf("missing capped-output guidance note:\n%s", codeAPI)
	}

	// Highest-scored tool survives; lowest-scored is dropped from Code API
	// (it still appears by name in the per-query list above the block).
	if !strings.Contains(codeAPI, "tool000") {
		t.Error("highest-scored tool missing from Code API block")
	}
	if strings.Contains(codeAPI, fmt.Sprintf("tool%03d", n-1)) {
		t.Errorf("lowest-scored tool%03d should have been dropped from Code API block", n-1)
	}
}

func TestFormatDiscoverResults_RespectsSignatureLimit(t *testing.T) {
	const n = 6
	matches := make([]Tool, n)
	ranked := make([]rankedTool, n)
	allMatched := make(map[string]Tool, n)
	for i := range n {
		tool := Tool{
			Name:        fmt.Sprintf("svc__tool%d", i),
			Description: "test tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
		}
		matches[i] = tool
		ranked[i] = rankedTool{Tool: tool, Score: float64(n - i)}
		allMatched[tool.Name] = tool
	}

	output := formatDiscoverResults(
		[]discoverQueryResult{{query: "svc", matches: matches, ranked: ranked}},
		allMatched,
		2,
	)
	codeAPI := output[strings.Index(output, "## Code API"):]
	if got := strings.Count(codeAPI, "function tool"); got != 2 {
		t.Fatalf("rendered signatures = %d, want 2\n%s", got, codeAPI)
	}
	if !strings.Contains(codeAPI, "top 2 of 6 tools") {
		t.Fatalf("missing limit/cap note:\n%s", codeAPI)
	}
}
