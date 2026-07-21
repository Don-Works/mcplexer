package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestDiscoverRefreshesLiveDownstreamInstance(t *testing.T) {
	var initializeCalls atomic.Int32
	var listCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			initializeCalls.Add(1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			n := listCalls.Add(1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[{"name":"tool_` + strconv.Itoa(int(n)) + `"}]}}`))
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer ts.Close()

	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "discover.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := &store.DownstreamServer{
		ID:            "fresh",
		Name:          "fresh",
		Transport:     "http",
		URL:           &ts.URL,
		ToolNamespace: "fresh",
		Discovery:     "dynamic",
		Source:        "test",
	}
	if err := db.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer: %v", err)
	}
	h := &discoverHandler{manager: downstream.NewManager(db, nil), store: db}

	first := runDiscover(t, h, "fresh")
	if first.Code != http.StatusOK {
		t.Fatalf("first discover status = %d, body %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-Mcplexer-Discovery-Fresh-Instance"); got != "true" {
		t.Fatalf("fresh header = %q, want true", got)
	}
	if got := first.Header().Get("X-Mcplexer-Discovery-Evicted-Instances"); got != "0" {
		t.Fatalf("first evicted header = %q, want 0", got)
	}
	if got := initializeCalls.Load(); got != 1 {
		t.Fatalf("initialize calls after first discover = %d, want 1", got)
	}

	second := runDiscover(t, h, "fresh")
	if second.Code != http.StatusOK {
		t.Fatalf("second discover status = %d, body %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-Mcplexer-Discovery-Evicted-Instances"); got != "1" {
		t.Fatalf("second evicted header = %q, want 1", got)
	}
	if got := initializeCalls.Load(); got != 2 {
		t.Fatalf("initialize calls after second discover = %d, want 2", got)
	}

	updated, err := db.GetDownstreamServer(context.Background(), "fresh")
	if err != nil {
		t.Fatalf("GetDownstreamServer: %v", err)
	}
	if !json.Valid(updated.CapabilitiesCache) || !containsString(updated.CapabilitiesCache, "tool_2") {
		t.Fatalf("capabilities cache = %s, want second tools/list result", updated.CapabilitiesCache)
	}
}

func runDiscover(t *testing.T, h *discoverHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/downstreams/"+id+"/discover", nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.discover(rr, req)
	return rr
}

func containsString(raw json.RawMessage, needle string) bool {
	return len(raw) > 0 && json.Valid(raw) && strings.Contains(string(raw), needle)
}
