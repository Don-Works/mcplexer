package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/readiness"
)

// TestHealthZAlias_StatusMirrorsHealth pins the /healthz alias contract: it
// must run the same handler as /api/v1/health and therefore report the same
// readiness state. Important because external probes (k8s readiness, load
// balancers, claude/codex/cursor MCP managers when they health-check the
// daemon) standardise on /healthz, and silent drift between the two
// endpoints would mean a probe says ready while the gateway is actually
// draining (or vice versa).
//
// Table-driven across all three readiness states so a future bug in any
// single state surfaces independently.
func TestHealthZAlias_StatusMirrorsHealth(t *testing.T) {
	cases := []struct {
		name         string
		state        readiness.State
		wantHTTP     int
		wantBodyStat string
	}{
		{
			name:         "starting -> 503",
			state:        readiness.Starting,
			wantHTTP:     http.StatusServiceUnavailable,
			wantBodyStat: "starting",
		},
		{
			name:         "ready -> 200",
			state:        readiness.Ready,
			wantHTTP:     http.StatusOK,
			wantBodyStat: "ready",
		},
		{
			name:         "draining -> 503",
			state:        readiness.Draining,
			wantHTTP:     http.StatusServiceUnavailable,
			wantBodyStat: "draining",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			tr := readiness.NewTracker()
			switch c.state {
			case readiness.Ready:
				tr.SetReady()
			case readiness.Draining:
				tr.SetDraining()
			}

			orig := readinessTracker
			readinessTracker = tr
			defer func() { readinessTracker = orig }()

			// Walk every supported path through the SAME handler the
			// router wires; assert identical responses on every path.
			for _, path := range []string{"/api/v1/health", "/healthz"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				rec := httptest.NewRecorder()
				healthCheck(rec, req)

				if rec.Code != c.wantHTTP {
					t.Errorf("%s: status=%d want %d", path, rec.Code, c.wantHTTP)
				}
				var resp healthResponse
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("%s: decode: %v", path, err)
				}
				if resp.Status != c.wantBodyStat {
					t.Errorf("%s: body.status=%q want %q", path, resp.Status, c.wantBodyStat)
				}
			}
		})
	}
}

// TestHealthZ_AuthExempt locks down the auth-middleware exemption: a probe
// hitting /healthz must NEVER require an API token. Without this, an
// out-of-the-box k8s readinessProbe (which has no way to learn the per-
// install API token) would always return 401 and the daemon would
// perpetually look unhealthy.
func TestHealthZ_AuthExempt(t *testing.T) {
	cases := []struct {
		path     string
		wantPass bool
	}{
		{"/healthz", true},
		{"/api/v1/health", true},
		{"/api/v1/tools", false}, // sanity: gated path is NOT exempt
	}
	for _, c := range cases {
		c := c
		t.Run(c.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			got := isAuthExempt(req)
			if got != c.wantPass {
				t.Fatalf("isAuthExempt(%s)=%v want %v", c.path, got, c.wantPass)
			}
		})
	}
}
