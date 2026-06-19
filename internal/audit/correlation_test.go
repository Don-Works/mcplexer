package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestWithCorrelationAndFromCtx(t *testing.T) {
	tests := []struct {
		name     string
		seed     context.Context
		set      string
		wantFrom string
	}{
		{
			name:     "empty ctx returns empty id",
			seed:     context.Background(),
			set:      "",
			wantFrom: "",
		},
		{
			name:     "roundtrip stores and reads id",
			seed:     context.Background(),
			set:      "01HFOOBAR",
			wantFrom: "01HFOOBAR",
		},
		{
			name:     "nested call replaces id",
			seed:     WithCorrelation(context.Background(), "outer"),
			set:      "inner",
			wantFrom: "inner",
		},
		{
			name:     "empty set leaves existing id intact",
			seed:     WithCorrelation(context.Background(), "outer"),
			set:      "",
			wantFrom: "outer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FromCtx(WithCorrelation(tc.seed, tc.set))
			if got != tc.wantFrom {
				t.Fatalf("FromCtx = %q, want %q", got, tc.wantFrom)
			}
		})
	}
}

func TestFromCtxNilSafe(t *testing.T) {
	//nolint:staticcheck // SA1012 — deliberately verifying nil safety.
	if got := FromCtx(nil); got != "" {
		t.Fatalf("FromCtx(nil) = %q, want \"\"", got)
	}
}

func TestWithCorrelationNilSafe(t *testing.T) {
	//nolint:staticcheck // SA1012 — deliberately verifying nil safety.
	ctx := WithCorrelation(nil, "id-1")
	if got := FromCtx(ctx); got != "id-1" {
		t.Fatalf("FromCtx(WithCorrelation(nil)) = %q, want %q", got, "id-1")
	}
}

func TestSlogAttrs(t *testing.T) {
	if got := SlogAttrs(context.Background()); len(got) != 0 {
		t.Fatalf("SlogAttrs(empty) = %v, want empty", got)
	}
	ctx := WithCorrelation(context.Background(), "id-9")
	attrs := SlogAttrs(ctx)
	if len(attrs) != 1 {
		t.Fatalf("SlogAttrs len = %d, want 1", len(attrs))
	}
	if attrs[0].Key != correlationAttrKey || attrs[0].Value.String() != "id-9" {
		t.Fatalf("SlogAttrs[0] = %+v, want %s=id-9", attrs[0], correlationAttrKey)
	}
}

func TestSlogLoggerRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	ctx := WithCorrelation(context.Background(), "id-log")
	SlogLogger(ctx).Info("hello")

	line := strings.TrimSpace(buf.String())
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("unmarshal log line %q: %v", line, err)
	}
	if rec[correlationAttrKey] != "id-log" {
		t.Fatalf("log record %s = %v, want id-log", correlationAttrKey, rec[correlationAttrKey])
	}
}

func TestSlogLoggerFallsBackWhenNoID(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	SlogLogger(context.Background()).Info("plain")

	if strings.Contains(buf.String(), correlationAttrKey) {
		t.Fatalf("expected no %s attr, got: %s", correlationAttrKey, buf.String())
	}
}

func TestContextHandlerAutoStamps(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	h := NewContextHandler(base)
	logger := slog.New(h)

	ctx := WithCorrelation(context.Background(), "id-handler")
	logger.InfoContext(ctx, "auto")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec[correlationAttrKey] != "id-handler" {
		t.Fatalf("auto-stamp missing: got %v", rec[correlationAttrKey])
	}
}

func TestContextHandlerNoIDLeavesRecordClean(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(NewContextHandler(base))

	logger.InfoContext(context.Background(), "no-id")

	if strings.Contains(buf.String(), correlationAttrKey) {
		t.Fatalf("expected no %s attr without ctx id; got %s", correlationAttrKey, buf.String())
	}
}

func TestContextHandlerSurvivesWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(NewContextHandler(base)).With("svc", "worker")

	ctx := WithCorrelation(context.Background(), "id-attrs")
	logger.InfoContext(ctx, "tagged")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec[correlationAttrKey] != "id-attrs" {
		t.Fatalf("With() lost auto-stamp: %v", rec[correlationAttrKey])
	}
	if rec["svc"] != "worker" {
		t.Fatalf("With() lost original attr: %v", rec["svc"])
	}
}

func TestNewContextHandlerNilBase(t *testing.T) {
	if h := NewContextHandler(nil); h != nil {
		t.Fatalf("NewContextHandler(nil) = %#v, want nil", h)
	}
}
