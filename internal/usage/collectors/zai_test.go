package collectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const zaiCurrentResponse = `{
  "code": 200,
  "data": {"level":"max","limits": [
    {"type":"TOKENS_LIMIT","unit":3,"number":5,"usage":800000000,
     "currentValue":127694464,"remaining":672305536,"percentage":15,
     "nextResetTime":1770648402389},
    {"type":"TIME_LIMIT","unit":5,"number":1,"usage":4000,
     "currentValue":0,"remaining":4000,"percentage":0,
     "usageDetails":[{"modelCode":"search-prime","usage":0}]}
  ]},
  "success": true
}`

func TestParseZAICurrentLimits(t *testing.T) {
	windows, err := parseZAIResponse([]byte(zaiCurrentResponse))
	if err != nil || len(windows) != 2 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	tokens, mcp := windows[0], windows[1]
	if tokens.ID != "zai_tokenslimit" || tokens.Unit != store.UnitTokens || tokens.DurationMinutes != 300 {
		t.Fatalf("token window = %+v", tokens)
	}
	requireNumber(t, tokens.UsedPercent, 15)
	requireNumber(t, tokens.Limit, 800000000)
	if tokens.ResetsAt == nil || tokens.ResetsAt.Unix() != 1770648402 {
		t.Fatalf("reset = %v", tokens.ResetsAt)
	}
	if mcp.ID != "zai_timelimit" || mcp.Unit != store.UnitRequests {
		t.Fatalf("MCP window = %+v", mcp)
	}
	requireNumber(t, mcp.Used, 0)
	requireNumber(t, mcp.UsedPercent, 0)
}

func TestParseZAIWrapperVariants(t *testing.T) {
	cases := []string{
		`{"limits":[{"type":"TOKENS_LIMIT","percentage":9}]}`,
		`{"payload":{"data":{"limits":[{"type":"TIME_LIMIT","usage":10,
          "usageDetails":[{"modelCode":"search-prime","usage":2}]}]}}}`,
		`[{"type":"TOKENS_LIMIT","percentage":3}]`,
		`{"data":[{"type":"TOKENS_LIMIT","percentage":4}]}`,
	}
	for _, body := range cases {
		windows, err := parseZAIResponse([]byte(body))
		if err != nil || len(windows) != 1 {
			t.Errorf("body=%s windows=%+v err=%v", body, windows, err)
		}
		if len(windows) == 1 && windows[0].ID == "zai_timelimit" {
			requireNumber(t, windows[0].Used, 2)
		}
	}
}

func TestParseZAIPresentZeroTokenPercentageIsMeasured(t *testing.T) {
	windows, err := parseZAIResponse([]byte(`{"data":{"limits":[
      {"type":"TIME_LIMIT","usage":4000,"currentValue":0,"percentage":0},
      {"type":"TOKENS_LIMIT","percentage":0}
    ]}}`))
	if err != nil || len(windows) != 2 || windows[1].ID != "zai_tokenslimit" {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	requireNumber(t, windows[1].UsedPercent, 0)
}

func TestParseZAIPlanLevel(t *testing.T) {
	if got := parseZAIPlan([]byte(`{"data":{"level":"max"}}`)); got != "GLM Coding Max" {
		t.Fatalf("plan = %q", got)
	}
}

func TestZAIFetchUsesRawScopedAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor/usage/quota/limit" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "raw-token" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(zaiCurrentResponse))
	}))
	defer server.Close()
	secret := &recordingSecret{value: []byte("raw-token")}
	collector := ZAICollector{Client: server.Client(), Secret: secret}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		AuthScopeID: "scope-z", SecretKey: "zai-key", BaseURL: server.URL,
	})
	if err != nil || result.Snapshot.Status != store.StatusOK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Snapshot.Plan != "GLM Coding Max" {
		t.Fatalf("plan = %q", result.Snapshot.Plan)
	}
	if secret.scope != "scope-z" || secret.key != "zai-key" {
		t.Fatalf("secret lookup scope=%q key=%q", secret.scope, secret.key)
	}
}

func TestZAIRejectsMeaninglessResponse(t *testing.T) {
	if _, err := parseZAIResponse([]byte(`{"data":{"limits":[]}}`)); err == nil {
		t.Fatal("expected empty limits error")
	}
	collector := ZAICollector{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := collector.Fetch(ctx, store.SourceConfig{AuthScopeID: "scope", SecretKey: "key"})
	if err != nil || result.Snapshot.Status != store.StatusUnconfigured {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
