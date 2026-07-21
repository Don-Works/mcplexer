package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorDetailWithholdsInternalDetailAndLogsIt(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const (
		requestID = "request-123"
		internal  = "database open failed at /srv/example-system/state.db"
	)
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", requestID)

	writeErrorDetail(rec, http.StatusInternalServerError, "operation failed", internal)

	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "operation failed" {
		t.Fatalf("error = %q, want stable public message", body.Error)
	}
	if strings.Contains(rec.Body.String(), internal) || strings.Contains(rec.Body.String(), "state.db") {
		t.Fatalf("response leaked internal detail: %q", rec.Body.String())
	}
	if body.Details != "see server logs; request_id="+requestID {
		t.Fatalf("details = %q, want opaque request reference", body.Details)
	}
	if got := logs.String(); !strings.Contains(got, internal) || !strings.Contains(got, requestID) {
		t.Fatalf("private log should retain detail and request id, got %q", got)
	}
}

func TestWriteErrorDetailCreatesCorrelationIDWithoutMiddleware(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorDetail(rec, http.StatusBadGateway, "upstream failed", "private upstream error")

	requestID := rec.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("X-Request-ID should be generated")
	}
	if got := rec.Header().Get("X-Correlation-ID"); got != requestID {
		t.Fatalf("X-Correlation-ID = %q, want %q", got, requestID)
	}
	if strings.Contains(rec.Body.String(), "private upstream error") {
		t.Fatalf("response leaked internal detail: %q", rec.Body.String())
	}
}

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
