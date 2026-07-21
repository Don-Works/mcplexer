package collectors

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func claudeTestNow() time.Time {
	return time.Date(2026, 7, 10, 17, 0, 0, 0, time.UTC)
}

func readClaudeFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseClaudeUsageMapsAllWindows(t *testing.T) {
	parsed := parseClaudeUsageOutput(readClaudeFixture(t, "claude_usage_basic.txt"), claudeTestNow())
	if len(parsed.windows) != 3 {
		t.Fatalf("windows = %+v", parsed.windows)
	}
	requireNumber(t, parsed.windows[0].UsedPercent, 11)
	requireNumber(t, parsed.windows[1].UsedPercent, 8)
	requireNumber(t, parsed.windows[2].UsedPercent, 2)
	wantSessionReset := time.Date(2026, 7, 10, 18, 40, 0, 0, time.UTC)
	if parsed.windows[0].ResetsAt == nil || !parsed.windows[0].ResetsAt.Equal(wantSessionReset) {
		t.Fatalf("session reset = %v", parsed.windows[0].ResetsAt)
	}
	if parsed.windows[0].DurationMinutes != 300 || parsed.windows[1].DurationMinutes != 10080 {
		t.Fatalf("durations = %d, %d", parsed.windows[0].DurationMinutes, parsed.windows[1].DurationMinutes)
	}
	wantWeekReset := time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC)
	if parsed.windows[1].ResetsAt == nil || !parsed.windows[1].ResetsAt.Equal(wantWeekReset) {
		t.Fatalf("week reset = %v", parsed.windows[1].ResetsAt)
	}
}

func TestParseClaudeUsageKeepsLatestRedraw(t *testing.T) {
	output := []byte(`Current session
10% used
Resets 7:40pm (Europe/London)
Current week (all models)
8% used
Resets Jul 12 at 12pm (Europe/London)
Current session
12% used
Resets 7:40pm (Europe/London)
Current week (all models)
9% used
Resets Jul 12 at 12pm (Europe/London)`)
	parsed := parseClaudeUsageOutput(output, claudeTestNow())
	if len(parsed.windows) != 2 {
		t.Fatalf("windows = %+v", parsed.windows)
	}
	requireNumber(t, parsed.windows[0].UsedPercent, 12)
	requireNumber(t, parsed.windows[1].UsedPercent, 9)
}

func TestClaudeWaitRequiresSessionAndWeeklyWindows(t *testing.T) {
	output := newCappedBuffer(4096)
	_, _ = output.Write([]byte("Current session\n10% used\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	found, err := claudeWaitForWindows(ctx, output)
	if found || err == nil {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestClaudeWaitSettlesAfterCoreWindows(t *testing.T) {
	output := newCappedBuffer(4096)
	_, _ = output.Write([]byte(
		"Current session\n10% used\nCurrent week (all models)\n8% used\n",
	))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	found, err := claudeWaitForWindows(ctx, output)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if time.Since(started) < claudeSettleDelay {
		t.Fatalf("returned before settle delay: %s", time.Since(started))
	}
}

func TestParseClaudeUsageStripsANSIDedupesRedrawsAndZeroPercent(t *testing.T) {
	parsed := parseClaudeUsageOutput(readClaudeFixture(t, "claude_usage_ansi_redraw.txt"), claudeTestNow())
	if len(parsed.windows) != 3 {
		t.Fatalf("windows = %+v", parsed.windows)
	}
	requireNumber(t, parsed.windows[0].UsedPercent, 12)
	requireNumber(t, parsed.windows[2].UsedPercent, 0)
}

func TestClaudeCollectorFetchUsesInjectedRunner(t *testing.T) {
	runner := func(_ context.Context, _ string) ([]byte, error) {
		return readClaudeFixture(t, "claude_usage_basic.txt"), nil
	}
	collector := &ClaudeCollector{Run: runner, Now: claudeTestNow}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider: store.ProviderClaude, Label: "Claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusOK || len(result.Snapshot.Windows) != 3 {
		t.Fatalf("snapshot = %+v", result.Snapshot)
	}
	if result.Snapshot.SourceLabel != claudeUsageSourceLbl {
		t.Fatalf("source label = %q", result.Snapshot.SourceLabel)
	}
}

func TestClaudeCollectorReadsPlanWithoutIdentityFields(t *testing.T) {
	collector := &ClaudeCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return readClaudeFixture(t, "claude_usage_basic.txt"), nil
		},
		Auth: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"loggedIn":true,"subscriptionType":"max","email":"private@example.com","orgName":"Private"}`), nil
		},
		Now: claudeTestNow,
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderClaude})
	if err != nil || result.Snapshot.Plan != "Max" {
		t.Fatalf("plan=%q err=%v", result.Snapshot.Plan, err)
	}
	encoded := result.Snapshot.Plan + result.Snapshot.Detail + result.Snapshot.Error
	if strings.Contains(encoded, "private") || strings.Contains(encoded, "@") {
		t.Fatalf("snapshot leaked identity: %q", encoded)
	}
}

func TestClaudeCollectorFetchPartialOnMissingWindows(t *testing.T) {
	runner := func(_ context.Context, _ string) ([]byte, error) {
		return []byte("Claude Code v2.1.206\nScanning local sessions…"), nil
	}
	collector := &ClaudeCollector{Run: runner, Now: claudeTestNow}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusPartial {
		t.Fatalf("status = %q", result.Snapshot.Status)
	}
	if !strings.Contains(result.Snapshot.Error, "no allowance windows") {
		t.Fatalf("error = %q", result.Snapshot.Error)
	}
}

func TestClaudeCollectorFetchPartialOnTimeout(t *testing.T) {
	runner := func(_ context.Context, _ string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}
	collector := &ClaudeCollector{Run: runner, Now: claudeTestNow}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusPartial {
		t.Fatalf("status = %q", result.Snapshot.Status)
	}
	if !strings.Contains(result.Snapshot.Error, "timed out") && !strings.Contains(result.Snapshot.Error, "deadline") {
		t.Fatalf("error = %q", result.Snapshot.Error)
	}
}

func TestClaudeCollectorFetchPartialOnExitError(t *testing.T) {
	runner := func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}
	collector := &ClaudeCollector{Run: runner, Now: claudeTestNow}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusPartial || !strings.Contains(result.Snapshot.Error, "exit status") {
		t.Fatalf("snapshot = %+v", result.Snapshot)
	}
}

func TestClaudeCollectorFetchRedactsSensitiveRunnerErrors(t *testing.T) {
	runner := func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("failed reading ~/.claude/credentials.json for user@example.com")
	}
	collector := &ClaudeCollector{Run: runner, Now: claudeTestNow}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Snapshot.Error, "credentials") || strings.Contains(result.Snapshot.Error, "@") {
		t.Fatalf("error leaked account data: %q", result.Snapshot.Error)
	}
}
