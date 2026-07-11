package grokstats

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectorAggregatesOnlyTokenTelemetry(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "unified.jsonl")
	body := `{"ts":"2026-07-11T09:00:00Z","msg":"model changed","pid":12,"ctx":{"model":"grok-code-fast-1"}}
{"ts":"2026-07-11T09:01:00Z","msg":"shell.turn.inference_done","pid":12,"sid":"ignored","ctx":{"prompt_tokens":100,"cached_prompt_tokens":40,"completion_tokens":20,"reasoning_tokens":5,"prompt":"not decoded"}}
{"ts":"2026-06-01T09:01:00Z","msg":"shell.turn.inference_done","pid":12,"ctx":{"prompt_tokens":999,"completion_tokens":999}}
malformed
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stats, err := (Collector{Path: path, Now: func() time.Time { return now }}).
		Stats(context.Background(), 30)
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	got := stats[0]
	if got.Model != "grok/grok-code-fast-1" || got.Requests != 1 ||
		got.InputTokens != 60 || got.CacheReadTokens != 40 || got.OutputTokens != 25 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestCollectorAttributesLatestModelPerProcess(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "unified.jsonl")
	body := `{"ts":"2026-07-11T08:00:00Z","msg":"backend_search: model switch","pid":"p1","ctx":{"new_model":"grok-4"}}
{"ts":"2026-07-11T08:01:00Z","msg":"shell.turn.inference_done","pid":"p1","ctx":{"prompt_tokens":10,"completion_tokens":2}}
{"ts":"2026-07-11T08:02:00Z","msg":"shell.turn.inference_done","pid":"p2","ctx":{"prompt_tokens":5,"cached_prompt_tokens":9,"reasoning_tokens":3}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stats, err := (Collector{Path: path, Now: func() time.Time { return now }}).
		Stats(context.Background(), 1)
	if err != nil || len(stats) != 2 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	var requests, input, output int
	for _, row := range stats {
		requests += row.Requests
		input += row.InputTokens
		output += row.OutputTokens
	}
	if requests != 2 || input != 10 || output != 5 {
		t.Fatalf("totals requests=%d input=%d output=%d", requests, input, output)
	}
}

func TestCollectorRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "unified.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Collector{Path: link}).Stats(context.Background(), 30); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
