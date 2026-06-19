package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("decodes valid json", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"ok"}`))
		var p payload
		if err := decodeJSON(req, &p); err != nil {
			t.Fatalf("decodeJSON returned error: %v", err)
		}
		if p.Name != "ok" {
			t.Fatalf("expected name=ok, got %q", p.Name)
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"ok","extra":"nope"}`))
		var p payload
		if err := decodeJSON(req, &p); err == nil {
			t.Fatal("expected unknown field error, got nil")
		}
	})

	t.Run("rejects multiple json values", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"ok"}{"name":"again"}`))
		var p payload
		if err := decodeJSON(req, &p); err == nil {
			t.Fatal("expected trailing JSON error, got nil")
		}
	})
}
