package collectors

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	grokTimeout    = 30 * time.Second
	grokOutputCap  = 2 << 20
	grokBillingMsg = "billing: fetched credits config"
)

// GrokRunFunc is injectable so parser and process tests never launch a live CLI.
type GrokRunFunc func(ctx context.Context, binary string, debugPath string) ([]byte, error)

// GrokCollector probes the logged-in Grok CLI billing extension via a PTY launch.
type GrokCollector struct {
	GrokBinary string
	Run        GrokRunFunc
}

func (c *GrokCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	debugPath, cleanup, err := grokDebugTempFile()
	if err != nil {
		return grokError(cfg, "temp debug file: "+redactGrokError(err), start), nil
	}
	defer cleanup()
	bounded, cancel := context.WithTimeout(ctx, grokTimeout)
	defer cancel()
	output, runErr := c.runner()(bounded, c.binary(), debugPath)
	parsed := parseGrokDebugOutput(output)
	if runErr != nil {
		parsed.errors = append(parsed.errors, redactGrokError(runErr))
	}
	return grokResult(cfg, parsed, start), nil
}

func (c *GrokCollector) binary() string {
	if c.GrokBinary == "" {
		return "grok"
	}
	return c.GrokBinary
}

func (c *GrokCollector) runner() GrokRunFunc {
	if c.Run != nil {
		return c.Run
	}
	return runGrokBillingProbe
}

func grokDebugTempFile() (string, func(), error) {
	file, err := os.CreateTemp("", "mcplexer-grok-debug-*.log")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	_ = file.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", func() {}, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func grokResult(cfg store.SourceConfig, parsed grokParsed, start time.Time) store.CollectorResult {
	snapshot := baseSnapshot(store.ProviderGrok, cfg, "cli")
	snapshot.SourceLabel = "Grok CLI"
	snapshot.Windows = parsed.windows
	if parsed.plan != "" {
		snapshot.Plan = parsed.plan
	}
	if len(parsed.windows) == 0 {
		parsed.errors = append(parsed.errors, "grok returned no billing data")
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

func grokError(cfg store.SourceConfig, message string, start time.Time) store.CollectorResult {
	snapshot := baseSnapshot(store.ProviderGrok, cfg, "cli")
	snapshot.SourceLabel = "Grok CLI"
	snapshot.Status, snapshot.Error = store.StatusPartial, message
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}
}

func redactGrokError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	message = redactGrokSensitive(message)
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func redactGrokSensitive(message string) string {
	lower := strings.ToLower(message)
	for _, token := range []string{"@", "auth.json", "cookie", "token", "email"} {
		if strings.Contains(lower, token) {
			return "grok billing probe failed"
		}
	}
	return message
}

func readGrokDebugFile(path string, limit int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read debug file: %w", err)
	}
	if len(data) > limit {
		data = data[:limit]
	}
	return data, nil
}
