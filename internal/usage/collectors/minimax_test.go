package collectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestParseMiniMaxLegacyBalance(t *testing.T) {
	windows, err := parseMiniMaxResponse([]byte(`{
      "total_tokens_remain":250,"total_tokens_grant":1000
    }`))
	if err != nil || len(windows) != 1 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	window := windows[0]
	if window.Unit != store.UnitTokens {
		t.Fatalf("unit = %q", window.Unit)
	}
	requireNumber(t, window.Used, 750)
	requireNumber(t, window.Limit, 1000)
	requireNumber(t, window.Remaining, 250)
	requireNumber(t, window.UsedPercent, 75)
}

func TestParseMiniMaxNestedWindows(t *testing.T) {
	body := `{"model_remains":[
		  {"model_name":"MiniMax-M2.5","percentage":0,
		   "current_interval_total_count":1500,"current_interval_usage_count":1500,
		   "remaining":1500,"start_time":1770634002389,"end_time":1770648402389},
		  {"name":"Weekly","used_percent":25}
		]}`
	windows, err := parseMiniMaxResponse([]byte(body))
	if err != nil || len(windows) != 2 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	byLabel := make(map[string]store.UsageWindow)
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	model := byLabel["MiniMax-M2.5 (5-hour)"]
	if model.Unit != store.UnitRequests || model.DurationMinutes != 300 {
		t.Fatalf("model window = %+v", model)
	}
	requireNumber(t, model.Used, 0)
	requireNumber(t, model.UsedPercent, 0)
	weekly := byLabel["Weekly"]
	if weekly.Unit != store.UnitPercent || weekly.DurationMinutes != 10080 {
		t.Fatalf("weekly window = %+v", weekly)
	}
	requireNumber(t, weekly.UsedPercent, 25)
}

func TestParseMiniMaxLiveCodingPlanWindows(t *testing.T) {
	body := `{"model_remains":[
      {"model_name":"general",
       "current_interval_total_count":0,"current_interval_usage_count":0,
       "current_interval_remaining_percent":91,"end_time":1770648402389,
       "current_weekly_total_count":0,"current_weekly_usage_count":0,
       "current_weekly_remaining_percent":74,"weekly_end_time":1771253202389},
      {"model_name":"video",
       "current_interval_total_count":100,"current_interval_usage_count":80,
       "end_time":1770648402389,
       "current_weekly_total_count":1000,"current_weekly_usage_count":700,
       "weekly_end_time":1771253202389}
    ]}`
	windows, err := parseMiniMaxResponse([]byte(body))
	if err != nil || len(windows) != 2 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	byLabel := make(map[string]store.UsageWindow)
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	requireNumber(t, byLabel["general (5-hour)"].UsedPercent, 9)
	requireNumber(t, byLabel["general (weekly)"].UsedPercent, 26)
	if _, ok := byLabel["video (5-hour)"]; ok {
		t.Fatal("unavailable video bucket should not be shown on the coding dashboard")
	}
}

func TestMiniMaxExplicitRemainingPercentWinsOverCounts(t *testing.T) {
	windows, err := parseMiniMaxResponse([]byte(`{"model_remains":[{
		"model_name":"general","current_interval_total_count":100,
		"current_interval_usage_count":80,"current_interval_remaining_percent":55
	}]}`))
	if err != nil || len(windows) != 1 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	requireNumber(t, windows[0].Remaining, 80)
	requireNumber(t, windows[0].UsedPercent, 45)
}

func TestParseMiniMaxRemainingPercentAndCounts(t *testing.T) {
	cases := []struct {
		body string
		want float64
	}{
		{`{"data":{"usage_percent":75}}`, 25},
		{`{"data":{"prompt_limit":200,"prompt_remain":150,"usage_percent":90}}`, 25},
		{`{"data":{"usage_ratio":"0.4"}}`, 40},
	}
	for _, tc := range cases {
		windows, err := parseMiniMaxResponse([]byte(tc.body))
		if err != nil || len(windows) != 1 {
			t.Fatalf("body=%s windows=%+v err=%v", tc.body, windows, err)
		}
		requireNumber(t, windows[0].UsedPercent, tc.want)
	}
}

func TestParseMiniMaxRemainingPercentage(t *testing.T) {
	windows, err := parseMiniMaxResponse([]byte(`{
      "data":{"windows":[{"name":"Rolling 5h","remaining_percentage":80}]}
    }`))
	if err != nil || len(windows) != 1 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	requireNumber(t, windows[0].UsedPercent, 20)
}

func TestParseMiniMaxRemainingOnlyIsMeasured(t *testing.T) {
	windows, err := parseMiniMaxResponse([]byte(`{
      "data":{"windows":[{"name":"Rolling quota","remaining":12}]}
    }`))
	if err != nil || len(windows) != 1 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	requireNumber(t, windows[0].Remaining, 12)
}

func TestMiniMaxFetchUsesScopedBearerSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token_plan/remains" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer plan-key" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":{"windows":[{"percentage":11}]}}`))
	}))
	defer server.Close()
	secret := &recordingSecret{value: []byte("plan-key")}
	collector := MiniMaxCollector{Client: server.Client(), Secret: secret}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		AuthScopeID: "scope-mm", SecretKey: "minimax", BaseURL: server.URL,
	})
	if err != nil || result.Snapshot.Status != store.StatusOK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if secret.scope != "scope-mm" || secret.key != "minimax" {
		t.Fatalf("secret lookup scope=%q key=%q", secret.scope, secret.key)
	}
}

func TestMiniMaxNeverReportsOKWithoutMeasurement(t *testing.T) {
	if _, err := parseMiniMaxResponse([]byte(`{"data":{"plan":"Max"}}`)); err == nil {
		t.Fatal("expected no measurable quota error")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"plan":"Max"}}`))
	}))
	defer server.Close()
	collector := MiniMaxCollector{
		Client: server.Client(), Secret: &recordingSecret{value: []byte("key")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		AuthScopeID: "scope", SecretKey: "key", BaseURL: server.URL,
	})
	if err != nil || result.Snapshot.Status == store.StatusOK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
