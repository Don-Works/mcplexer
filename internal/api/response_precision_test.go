package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fixed instant with nanos to make truncation observable.
var refNano = time.Date(2026, 5, 25, 19, 50, 45, 137_142_000, time.UTC)

func TestWriteJSONForRequest_DefaultTruncates(t *testing.T) {
	tt := store.Task{ID: "x", CreatedAt: refNano, UpdatedAt: refNano}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/tasks/x", nil)
	writeJSONForRequest(rec, req, 200, tt)

	body := rec.Body.String()
	if !strings.Contains(body, `"created_at":"2026-05-25T19:50:45Z"`) {
		t.Errorf("default should truncate: %s", body)
	}
	if strings.Contains(body, ".137142") {
		t.Errorf("default leaked nanos: %s", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestWriteJSONForRequest_PrecisionNs(t *testing.T) {
	tt := store.Task{ID: "x", CreatedAt: refNano, UpdatedAt: refNano}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/tasks/x?precision=ns", nil)
	writeJSONForRequest(rec, req, 200, tt)

	body := rec.Body.String()
	if !strings.Contains(body, ".137142") {
		t.Errorf("precision=ns should keep nanos: %s", body)
	}

	// Sanity: the body still parses as the same Task shape.
	var got store.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "x" {
		t.Errorf("ID = %q, want x", got.ID)
	}
}

func TestPrecisionNanos_Aliases(t *testing.T) {
	for _, val := range []string{"ns", "nano", "nanos", "nanoseconds"} {
		req := httptest.NewRequest("GET", "/x?precision="+val, nil)
		if !precisionNanos(req) {
			t.Errorf("precision=%s should be nanos", val)
		}
	}
	for _, val := range []string{"", "seconds", "s", "garbage"} {
		req := httptest.NewRequest("GET", "/x?precision="+val, nil)
		if precisionNanos(req) {
			t.Errorf("precision=%q should not be nanos", val)
		}
	}
	if precisionNanos(nil) {
		t.Errorf("nil request should not be nanos")
	}
}
