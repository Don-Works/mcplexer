package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/usage"
)

func TestUsageSnapshotTools(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	b := NewInternalBackend(db, nil)
	b.SetUsageService(&usage.Service{Store: db})

	for _, name := range []string{"get_usage_dashboard", "refresh_usage_dashboard"} {
		result, err := b.Call(context.Background(), name, json.RawMessage(`{"days":14}`))
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(result, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.IsError || len(envelope.Content) != 1 || !strings.Contains(envelope.Content[0].Text, `"window_days": 14`) {
			t.Fatalf("%s result = %s", name, result)
		}
	}
}

func TestUsageSnapshotToolWithoutService(t *testing.T) {
	t.Parallel()
	b := NewInternalBackend(newTestDB(t), nil)
	result, err := b.Call(context.Background(), "get_usage_dashboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "usage service not initialised") {
		t.Fatalf("result = %s", result)
	}
}
