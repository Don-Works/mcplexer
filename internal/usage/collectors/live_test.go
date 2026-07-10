package collectors

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestLiveClaudeUsageProbe(t *testing.T) {
	if os.Getenv("MCPLEXER_LIVE_USAGE_TEST") == "" {
		t.Skip("set MCPLEXER_LIVE_USAGE_TEST=1 to exercise logged-in CLIs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	output, err := runClaudeUsageProbe(ctx, "claude")
	parsed := parseClaudeUsageOutput(output, time.Now())
	t.Logf("error=%v bytes=%d windows=%v", err, len(output), usageWindowLabels(parsed.windows))
	if err != nil || !claudeHasCoreWindows(parsed.windows) {
		t.Fatal("Claude live usage probe did not return session and weekly windows")
	}
}

func TestLiveGrokBillingProbe(t *testing.T) {
	if os.Getenv("MCPLEXER_LIVE_USAGE_TEST") == "" {
		t.Skip("set MCPLEXER_LIVE_USAGE_TEST=1 to exercise logged-in CLIs")
	}
	path, cleanup, err := grokDebugTempFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	output, runErr := runGrokBillingProbe(ctx, "grok", path)
	parsed := parseGrokDebugOutput(output)
	t.Logf("error=%v bytes=%d plan=%q windows=%v", runErr, len(output), parsed.plan, usageWindowLabels(parsed.windows))
	if runErr != nil || parsed.plan == "" || len(parsed.windows) == 0 {
		t.Fatal("Grok live billing probe did not return plan and period")
	}
}

func usageWindowLabels(windows []store.UsageWindow) []string {
	labels := make([]string, 0, len(windows))
	for _, window := range windows {
		labels = append(labels, window.Label)
	}
	return labels
}
