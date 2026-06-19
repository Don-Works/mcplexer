package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/readiness"
)

func TestHealthCheck_ReadinessStates(t *testing.T) {
	tests := []struct {
		name         string
		state        readiness.State
		wantStatus   int
		wantRespStat string
	}{
		{
			name:         "starting returns 503",
			state:        readiness.Starting,
			wantStatus:   http.StatusServiceUnavailable,
			wantRespStat: "starting",
		},
		{
			name:         "ready returns 200",
			state:        readiness.Ready,
			wantStatus:   http.StatusOK,
			wantRespStat: "ready",
		},
		{
			name:         "draining returns 503",
			state:        readiness.Draining,
			wantStatus:   http.StatusServiceUnavailable,
			wantRespStat: "draining",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := readiness.NewTracker()
			switch tc.state {
			case readiness.Ready:
				tr.SetReady()
			case readiness.Draining:
				tr.SetDraining()
			}

			origTracker := readinessTracker
			readinessTracker = tr
			defer func() { readinessTracker = origTracker }()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			rec := httptest.NewRecorder()
			healthCheck(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			var resp healthResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Status != tc.wantRespStat {
				t.Errorf("response status = %q, want %q", resp.Status, tc.wantRespStat)
			}
		})
	}
}

func TestHealthCheck_NoTracker(t *testing.T) {
	origTracker := readinessTracker
	readinessTracker = nil
	defer func() { readinessTracker = origTracker }()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	healthCheck(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var resp healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("response status = %q, want %q", resp.Status, "ok")
	}
}
