package collectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	codexTimeout   = 15 * time.Second
	codexOutputCap = 2 << 20
)

// CodexRunFunc is injectable so parser and protocol tests never launch a live
// CLI. The default starts `codex app-server --stdio` under a timeout.
type CodexRunFunc func(ctx context.Context, binary string, input []byte) ([]byte, error)

type CodexCollector struct {
	CodexBinary string
	Run         CodexRunFunc
}

func (c *CodexCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	input, err := codexRequests()
	if err != nil {
		return codexError(cfg, fmt.Sprintf("encode requests: %v", err), start), nil
	}
	bounded, cancel := context.WithTimeout(ctx, codexTimeout)
	defer cancel()
	output, runErr := c.runner()(bounded, c.binary(), input)
	parsed := parseCodexOutput(output)
	if runErr != nil {
		parsed.errors = append(parsed.errors, cleanCodexError(runErr))
	}
	return codexResult(cfg, parsed, start), nil
}

func (c *CodexCollector) binary() string {
	if c.CodexBinary == "" {
		return ResolveBinary("codex")
	}
	return c.CodexBinary
}

func (c *CodexCollector) runner() CodexRunFunc {
	if c.Run != nil {
		return c.Run
	}
	return runCodexAppServer
}

func codexRequests() ([]byte, error) {
	messages := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"clientInfo": map[string]string{"name": "mcplexer-usage", "version": "1.0.0"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "account/rateLimits/read", "params": nil},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "account/usage/read", "params": nil},
	}
	var input bytes.Buffer
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		input.Write(encoded)
		input.WriteByte('\n')
	}
	return input.Bytes(), nil
}

func runCodexAppServer(ctx context.Context, binary string, input []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdout: %w", err)
	}
	output := newCappedBuffer(codexOutputCap)
	stderr := newCappedBuffer(64 << 10)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex app-server: %w", err)
	}
	protocolErr := exchangeCodexProtocol(input, stdin, stdout, output)
	_ = stdin.Close()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return output.Bytes(), fmt.Errorf("codex app-server timed out: %w", ctx.Err())
	}
	if protocolErr != nil {
		return output.Bytes(), fmt.Errorf("codex app-server protocol: %w", protocolErr)
	}
	if waitErr != nil {
		return output.Bytes(), fmt.Errorf("codex app-server: %w", waitErr)
	}
	return output.Bytes(), nil
}

type cappedBuffer struct {
	mu     sync.RWMutex
	buffer bytes.Buffer
	limit  int
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func (b *cappedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(value[:remaining])
	}
	return written, nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func codexResult(cfg store.SourceConfig, parsed codexParsed, start time.Time) store.CollectorResult {
	snapshot := baseSnapshot(store.ProviderCodex, cfg, "api")
	snapshot.Windows = parsed.windows
	if parsed.observed.TotalTokens > 0 {
		snapshot.Observed = parsed.observed
		snapshot.ObservedSource = "api"
		snapshot.ObservedSourceLabel = "Codex app-server usage"
		snapshot.ObservedUpdatedAt = timePtr(start)
	}
	if parsed.plan != "" {
		snapshot.Plan = parsed.plan
	}
	if len(parsed.windows) == 0 {
		parsed.errors = append(parsed.errors, "codex returned no allowance data")
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

func cleanCodexError(err error) string {
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func codexError(cfg store.SourceConfig, message string, start time.Time) store.CollectorResult {
	snapshot := baseSnapshot(store.ProviderCodex, cfg, "api")
	snapshot.Status, snapshot.Error = store.StatusPartial, message
	return store.CollectorResult{Snapshot: snapshot, Duration: time.Since(start)}
}
