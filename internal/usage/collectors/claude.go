package collectors

import (
	"context"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	claudeTimeout        = 25 * time.Second
	claudeOutputCap      = 2 << 20
	claudeStartupDelay   = 3 * time.Second
	claudeHandshakeLimit = 10 * time.Second
	claudePollInterval   = 100 * time.Millisecond
	claudeSettleDelay    = 750 * time.Millisecond
	claudeUsageCommand   = "/usage\r"
	claudeUsageSourceLbl = "Claude CLI /usage"
)

var claudeArgv = []string{
	"--ax-screen-reader", "--safe-mode", "--no-chrome", "--permission-mode", "dontAsk",
}

// ClaudeRunFunc is injectable so parser and process tests never launch a live CLI.
type ClaudeRunFunc func(ctx context.Context, binary string) ([]byte, error)

// ClaudeAuthFunc reads only the CLI's non-secret subscription type.
type ClaudeAuthFunc func(ctx context.Context, binary string) ([]byte, error)

// ClaudeCollector probes the logged-in Claude CLI /usage screen via a PTY launch.
type ClaudeCollector struct {
	ClaudeBinary string
	Run          ClaudeRunFunc
	Auth         ClaudeAuthFunc
	Now          func() time.Time
}

func (c *ClaudeCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	bounded, cancel := context.WithTimeout(ctx, claudeTimeout)
	defer cancel()
	output, runErr := c.runner()(bounded, c.binary())
	parsed := parseClaudeUsageOutput(output, c.clock()())
	parsed.plan = c.subscriptionPlan(bounded)
	if runErr != nil {
		parsed.errors = append(parsed.errors, redactClaudeError(runErr))
	}
	return claudeResult(cfg, parsed, start), nil
}

func (c *ClaudeCollector) subscriptionPlan(ctx context.Context) string {
	if c.Auth == nil && c.Run != nil {
		return ""
	}
	runner := c.Auth
	if runner == nil {
		runner = runClaudeAuthStatus
	}
	output, err := runner(ctx, c.binary())
	if err != nil {
		return ""
	}
	return parseClaudeSubscriptionPlan(output)
}

func (c *ClaudeCollector) binary() string {
	if c.ClaudeBinary == "" {
		return "claude"
	}
	return c.ClaudeBinary
}

func (c *ClaudeCollector) runner() ClaudeRunFunc {
	if c.Run != nil {
		return c.Run
	}
	return runClaudeUsageProbe
}

func (c *ClaudeCollector) clock() func() time.Time {
	if c.Now != nil {
		return c.Now
	}
	return time.Now
}

func claudeResult(cfg store.SourceConfig, parsed claudeParsed, start time.Time) store.CollectorResult {
	snapshot := baseSnapshot(store.ProviderClaude, cfg, "cli")
	snapshot.SourceLabel = claudeUsageSourceLbl
	snapshot.Windows = parsed.windows
	if parsed.plan != "" {
		snapshot.Plan = parsed.plan
	}
	if len(parsed.windows) == 0 {
		parsed.errors = append(parsed.errors, "claude returned no allowance windows")
	}
	if len(parsed.errors) > 0 {
		snapshot.Status, snapshot.Error = store.StatusPartial, strings.Join(parsed.errors, "; ")
	} else {
		snapshot.Status = store.StatusOK
	}
	if len(parsed.windows) > 0 {
		snapshot.UpdatedAt = timePtr(start)
	}
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}
}

func redactClaudeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	message = redactClaudeSensitive(message)
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func redactClaudeSensitive(message string) string {
	lower := strings.ToLower(message)
	for _, token := range []string{"@", ".claude", "credential", "token", "email", "oauth"} {
		if strings.Contains(lower, token) {
			return "claude usage probe failed"
		}
	}
	return message
}
