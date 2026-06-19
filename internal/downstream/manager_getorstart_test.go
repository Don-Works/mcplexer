package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newHTTPManager(t *testing.T) *Manager {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewManager(db, nil)
}

func registerHTTPServer(t *testing.T, m *Manager, id, url string) {
	t.Helper()
	srv := &store.DownstreamServer{
		ID:            id,
		Name:          id,
		Transport:     "http",
		URL:           &url,
		ToolNamespace: id,
		Discovery:     "dynamic",
		Source:        "test",
	}
	if err := m.store.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer(%s): %v", id, err)
	}
}

func registerOAuthScopeWithToken(t *testing.T, m *Manager, id string) string {
	t.Helper()
	scope := &store.AuthScope{
		ID:             id,
		Name:           id,
		Type:           "oauth2",
		OAuthTokenData: []byte("encrypted-token"),
	}
	if err := m.store.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("CreateAuthScope(%s): %v", id, err)
	}
	return scope.ID
}

func writeRPCResult(t *testing.T, w http.ResponseWriter, id json.RawMessage, result string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if len(id) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + result + `}`))
}

func decodeRPCRequest(t *testing.T, r *http.Request) struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
} {
	t.Helper()
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

func TestManagerGetOrStartCoalescesSameKeyColdStart(t *testing.T) {
	m := newHTTPManager(t)
	var initializeCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			initializeCalls.Add(1)
			time.Sleep(100 * time.Millisecond)
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "same", ts.URL)

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.ListTools(context.Background(), "same", "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
	}
	if got := initializeCalls.Load(); got != 1 {
		t.Fatalf("initialize calls = %d, want 1", got)
	}
}

func TestManagerGetOrStartDifferentKeysDoNotBlockOnColdStart(t *testing.T) {
	m := newHTTPManager(t)
	slowEntered := make(chan struct{})
	releaseSlow := make(chan struct{})
	tsSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			close(slowEntered)
			<-releaseSlow
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			t.Fatalf("unexpected slow method %q", req.Method)
		}
	}))
	defer tsSlow.Close()
	registerHTTPServer(t, m, "slow", tsSlow.URL)

	tsFast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			t.Fatalf("unexpected fast method %q", req.Method)
		}
	}))
	defer tsFast.Close()
	registerHTTPServer(t, m, "fast", tsFast.URL)

	slowDone := make(chan error, 1)
	go func() {
		_, err := m.ListTools(context.Background(), "slow", "")
		slowDone <- err
	}()
	<-slowEntered

	fastDone := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := m.ListTools(context.Background(), "fast", "")
		fastDone <- err
	}()

	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast ListTools: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
			t.Fatalf("fast ListTools blocked behind slow cold-start: %s", elapsed)
		}
	case <-time.After(300 * time.Millisecond):
		close(releaseSlow)
		t.Fatal("fast ListTools did not complete while unrelated slow start was blocked")
	}

	close(releaseSlow)
	if err := <-slowDone; err != nil {
		t.Fatalf("slow ListTools: %v", err)
	}
}

func TestManagerListToolsInvalidatesOAuthTokenOnInitialize401(t *testing.T) {
	m := newHTTPManager(t)
	scopeID := registerOAuthScopeWithToken(t, m, "slack-oauth")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"missing_token"}}`))
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "slack", ts.URL)

	_, err := m.ListTools(context.Background(), "slack", scopeID)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("ListTools error = %v, want ErrAuthRequired", err)
	}

	scope, err := m.store.GetAuthScope(context.Background(), scopeID)
	if err != nil {
		t.Fatalf("GetAuthScope: %v", err)
	}
	if len(scope.OAuthTokenData) != 0 {
		t.Fatalf("OAuthTokenData len = %d, want 0", len(scope.OAuthTokenData))
	}
}

func TestManagerListToolsInvalidatesOAuthTokenAfterRetry401(t *testing.T) {
	m := newHTTPManager(t)
	scopeID := registerOAuthScopeWithToken(t, m, "retry-oauth")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "retry", ts.URL)

	_, err := m.ListTools(context.Background(), "retry", scopeID)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("ListTools error = %v, want ErrAuthRequired", err)
	}

	scope, err := m.store.GetAuthScope(context.Background(), scopeID)
	if err != nil {
		t.Fatalf("GetAuthScope: %v", err)
	}
	if len(scope.OAuthTokenData) != 0 {
		t.Fatalf("OAuthTokenData len = %d, want 0", len(scope.OAuthTokenData))
	}
}

func TestManagerListToolsInvalidatesOAuthTokenWhenRetryInitialize401(t *testing.T) {
	m := newHTTPManager(t)
	scopeID := registerOAuthScopeWithToken(t, m, "retry-init-oauth")
	var initializeCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			if initializeCalls.Add(1) == 1 {
				writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "retry-init", ts.URL)

	_, err := m.ListTools(context.Background(), "retry-init", scopeID)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("ListTools error = %v, want ErrAuthRequired", err)
	}

	scope, err := m.store.GetAuthScope(context.Background(), scopeID)
	if err != nil {
		t.Fatalf("GetAuthScope: %v", err)
	}
	if len(scope.OAuthTokenData) != 0 {
		t.Fatalf("OAuthTokenData len = %d, want 0", len(scope.OAuthTokenData))
	}
}

func TestManagerCallInvalidatesOAuthTokenWhenRetryInitialize401(t *testing.T) {
	m := newHTTPManager(t)
	scopeID := registerOAuthScopeWithToken(t, m, "call-retry-init-oauth")
	var initializeCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			if initializeCalls.Add(1) == 1 {
				writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/call":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "call-retry-init", ts.URL)

	_, err := m.Call(context.Background(), "call-retry-init", scopeID, "anything", json.RawMessage(`{}`))
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("Call error = %v, want ErrAuthRequired", err)
	}

	scope, err := m.store.GetAuthScope(context.Background(), scopeID)
	if err != nil {
		t.Fatalf("GetAuthScope: %v", err)
	}
	if len(scope.OAuthTokenData) != 0 {
		t.Fatalf("OAuthTokenData len = %d, want 0", len(scope.OAuthTokenData))
	}
}
