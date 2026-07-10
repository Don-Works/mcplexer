package collectors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// CodexCollector gathers usage data from the Codex CLI via stdio JSON
// lines. It initializes with clientInfo, sends account/usage/read, and
// parses rate-limit + token-summary responses. Stdin is closed after
// requests; the process is bounded by context timeout.
type CodexCollector struct {
	// CodexBinary is the path to the codex CLI binary. Empty = "codex".
	CodexBinary string
}

// Fetch spawns a codex process, sends the init + usage request, and
// parses the output. Returns partial status on parse/timeout errors.
func (c *CodexCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()

	bin := c.CodexBinary
	if bin == "" {
		bin = "codex"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return codexError(cfg, fmt.Sprintf("stdin pipe: %v", err), start), nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return codexError(cfg, fmt.Sprintf("stdout pipe: %v", err), start), nil
	}

	if err := cmd.Start(); err != nil {
		return codexError(cfg, fmt.Sprintf("start: %v", err), start), nil
	}

	// Send initialize request.
	initMsg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name":    "mcplexer-usage",
				"version": "1.0.0",
			},
		},
	}
	if err := writeJSONLine(stdin, initMsg); err != nil {
		return codexError(cfg, fmt.Sprintf("write init: %v", err), start), nil
	}

	// Send account/usage/read request.
	usageMsg := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "account/usage/read",
	}
	if err := writeJSONLine(stdin, usageMsg); err != nil {
		return codexError(cfg, fmt.Sprintf("write usage: %v", err), start), nil
	}

	// Close stdin to signal we're done.
	_ = stdin.Close()

	windows, err := readCodexWindows(bufio.NewScanner(stdout), 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return codexError(cfg, fmt.Sprintf("read: %v", err), start), nil
	}
	_ = cmd.Wait()

	snap := baseSnapshot(store.ProviderCodex, cfg, "api")
	snap.Status = store.StatusOK
	snap.UpdatedAt = timePtr(start)
	snap.Windows = windows
	return store.CollectorResult{Snapshot: snap, Duration: time.Since(start)}, nil
}

func writeJSONLine(w interface{ Write([]byte) (int, error) }, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// readCodexWindows scans stdout for the usage response. Codex returns
// rateLimits and account/usage/read results as JSON-RPC responses.
func readCodexWindows(scanner *bufio.Scanner, timeout time.Duration) ([]store.UsageWindow, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var windows []store.UsageWindow
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			w := tryParseCodexUsageLine(line)
			if w != nil {
				windows = append(windows, w...)
			}
		}
	}()

	select {
	case <-done:
	case <-timer.C:
		return windows, fmt.Errorf("timeout reading codex output")
	}
	return windows, nil
}

// tryParseCodexUsageLine attempts to parse one JSON line for usage data.
// Returns nil if the line doesn't contain relevant data.
func tryParseCodexUsageLine(line string) []store.UsageWindow {
	var msg json.RawMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil
	}

	// Try rateLimits response.
	if windows := parseCodexRateLimits(msg); len(windows) > 0 {
		return windows
	}

	// Try account/usage/read result.
	return parseCodexUsageRead(msg)
}

type codexMsg struct {
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Params json.RawMessage `json:"params"`
}

type codexRateLimit struct {
	Name               string `json:"name"`
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins int    `json:"windowDurationMins"`
	ResetsAt           int64  `json:"resetsAt"`
}

func parseCodexRateLimits(msg json.RawMessage) []store.UsageWindow {
	var m struct {
		Params struct {
			RateLimits []codexRateLimit `json:"rateLimits"`
		} `json:"params"`
	}
	if err := json.Unmarshal(msg, &m); err != nil || len(m.Params.RateLimits) == 0 {
		return nil
	}
	windows := make([]store.UsageWindow, 0, len(m.Params.RateLimits))
	for _, rl := range m.Params.RateLimits {
		var resetsAt *time.Time
		if rl.ResetsAt > 0 {
			t := time.Unix(rl.ResetsAt, 0).UTC()
			resetsAt = &t
		}
		windows = append(windows, store.UsageWindow{
			ID:              "codex_" + strings.ToLower(rl.Name),
			Label:           rl.Name,
			UsedPercent:     float64(rl.UsedPercent),
			Unit:            store.UnitPercent,
			ResetsAt:        resetsAt,
			DurationMinutes: rl.WindowDurationMins,
		})
	}
	return windows
}

func parseCodexUsageRead(msg json.RawMessage) []store.UsageWindow {
	var m struct {
		Result struct {
			TotalTokens int     `json:"totalTokens"`
			TotalCost   float64 `json:"totalCost"`
		} `json:"result"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil
	}
	if m.Result.TotalTokens == 0 && m.Result.TotalCost == 0 {
		return nil
	}
	return []store.UsageWindow{{
		ID:    "codex_usage",
		Label: "Token Usage",
		Used:  float64(m.Result.TotalTokens),
		Unit:  store.UnitTokens,
	}}
}

func codexError(cfg store.SourceConfig, msg string, start time.Time) store.CollectorResult {
	snap := baseSnapshot(store.ProviderCodex, cfg, "api")
	snap.Status = store.StatusPartial
	snap.Error = msg
	return store.CollectorResult{Snapshot: snap, Duration: time.Since(start)}
}
