// Package grokstats reads the Grok CLI's local structured inference telemetry.
// It decodes only timestamps, model changes, and numeric token counters;
// prompt text, session identifiers, identities, and auth are ignored.
package grokstats

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/usage/clistats"
)

const (
	defaultMaxBytes = int64(32 << 20)
	maxLineBytes    = 1 << 20
)

// Collector implements usage.LocalStatsCollector for Grok CLI JSONL logs.
type Collector struct {
	Path     string
	MaxBytes int64
	Now      func() time.Time
}

type logEvent struct {
	Timestamp string          `json:"ts"`
	Message   string          `json:"msg"`
	PID       json.RawMessage `json:"pid"`
	Context   struct {
		Model              string `json:"model"`
		NewModel           string `json:"new_model"`
		CurrentModelID     string `json:"current_model_id"`
		PromptTokens       int    `json:"prompt_tokens"`
		CachedPromptTokens int    `json:"cached_prompt_tokens"`
		CompletionTokens   int    `json:"completion_tokens"`
		ReasoningTokens    int    `json:"reasoning_tokens"`
	} `json:"ctx"`
}

func (c Collector) Stats(ctx context.Context, days int) ([]clistats.ModelStats, error) {
	path, err := c.logPath()
	if err != nil {
		return nil, err
	}
	file, start, err := openBoundedRegularFile(path, c.byteLimit())
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek grok usage log: %w", err)
	}
	reader := bufio.NewReader(file)
	if start > 0 {
		_, _ = reader.ReadString('\n')
	}

	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	if days <= 0 {
		days = 30
	}
	cutoff := now.AddDate(0, 0, -days)
	modelsByPID := make(map[string]string)
	byModel := make(map[string]*clistats.ModelStats)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		var event logEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		pid := rawID(event.PID)
		if model := modelFromEvent(event); model != "" && pid != "" {
			modelsByPID[pid] = model
			continue
		}
		if event.Message != "shell.turn.inference_done" {
			continue
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil || occurredAt.Before(cutoff) || occurredAt.After(now.Add(time.Minute)) {
			continue
		}
		model := modelsByPID[pid]
		if model == "" {
			model = "unknown"
		}
		key := "grok/" + strings.TrimPrefix(strings.TrimSpace(model), "grok/")
		stats := byModel[key]
		if stats == nil {
			stats = &clistats.ModelStats{Model: key}
			byModel[key] = stats
		}
		cached := max(0, event.Context.CachedPromptTokens)
		stats.Requests++
		stats.InputTokens += max(0, event.Context.PromptTokens-cached)
		stats.CacheReadTokens += cached
		stats.OutputTokens += max(0, event.Context.CompletionTokens) +
			max(0, event.Context.ReasoningTokens)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan grok usage log: %w", err)
	}
	result := make([]clistats.ModelStats, 0, len(byModel))
	for _, stats := range byModel {
		result = append(result, *stats)
	}
	return result, nil
}

func (c Collector) logPath() (string, error) {
	if strings.TrimSpace(c.Path) != "" {
		return c.Path, nil
	}
	root := strings.TrimSpace(os.Getenv("GROK_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve grok home: %w", err)
		}
		root = filepath.Join(home, ".grok")
	}
	return filepath.Join(root, "logs", "unified.jsonl"), nil
}

func (c Collector) byteLimit() int64 {
	if c.MaxBytes <= 0 || c.MaxBytes > defaultMaxBytes {
		return defaultMaxBytes
	}
	return c.MaxBytes
}

func openBoundedRegularFile(path string, maxBytes int64) (*os.File, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open grok usage log: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, errors.New("grok usage log is not a regular file")
	}
	file, err := os.Open(path) //nolint:gosec -- fixed local path or injected test path
	if err != nil {
		return nil, 0, fmt.Errorf("open grok usage log: %w", err)
	}
	return file, max(int64(0), info.Size()-maxBytes), nil
}

func rawID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return strings.TrimSpace(string(raw))
}

func modelFromEvent(event logEvent) string {
	switch event.Message {
	case "model changed":
		return strings.TrimSpace(event.Context.Model)
	case "backend_search: model switch":
		return strings.TrimSpace(event.Context.NewModel)
	case "model catalog: notifying clients":
		return strings.TrimSpace(event.Context.CurrentModelID)
	default:
		return ""
	}
}
