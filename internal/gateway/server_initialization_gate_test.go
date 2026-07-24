package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

type initializeFailingStore struct {
	*mockStore
	err error
}

func (s *initializeFailingStore) CreateSession(context.Context, *store.Session) error {
	return s.err
}

func TestServerInitializationGateRejectsExternalToolMethodsBeforeInitialize(t *testing.T) {
	srv := newInitializationGateServer(t, TransportSocket, &mockStore{})

	tests := []struct {
		name   string
		method string
		params json.RawMessage
	}{
		{name: "list", method: "tools/list"},
		{
			name:   "call",
			method: "tools/call",
			params: json.RawMessage(`{"name":"mcpx__search_tools","arguments":{"queries":["test"]}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := dispatchInitializationGateRequest(t, srv, tt.method, tt.params)
			if resp.Error == nil {
				t.Fatal("expected pre-initialize request to be rejected")
			}
			if resp.Error.Code != CodeInvalidRequest {
				t.Fatalf("error code = %d, want %d", resp.Error.Code, CodeInvalidRequest)
			}
			if !strings.Contains(resp.Error.Message, "must initialize successfully") {
				t.Fatalf("unexpected error message: %q", resp.Error.Message)
			}
		})
	}
}

func TestServerInitializationGateMalformedInitializeIsSticky(t *testing.T) {
	srv := newInitializationGateServer(t, TransportSocket, &mockStore{})

	malformed := dispatchInitializationGateRequest(t, srv, "initialize", json.RawMessage(`[]`))
	if malformed.Error == nil || malformed.Error.Code != CodeInvalidParams {
		t.Fatalf("malformed initialize error = %#v, want code %d", malformed.Error, CodeInvalidParams)
	}

	retry := dispatchInitializationGateRequest(t, srv, "initialize", initializeParams(t, t.TempDir()))
	assertAlreadyInitialized(t, retry)
	assertToolsStillBlocked(t, srv)
}

func TestServerInitializationGateFailedInitializeIsSticky(t *testing.T) {
	wantErr := errors.New("session store unavailable")
	st := &initializeFailingStore{mockStore: &mockStore{}, err: wantErr}
	srv := newInitializationGateServer(t, TransportSocket, st)

	failed := dispatchInitializationGateRequest(t, srv, "initialize", initializeParams(t, t.TempDir()))
	if failed.Error == nil || failed.Error.Code != CodeInvalidRequest {
		t.Fatalf("failed initialize error = %#v, want code %d", failed.Error, CodeInvalidRequest)
	}
	if !strings.Contains(failed.Error.Message, wantErr.Error()) {
		t.Fatalf("failed initialize message = %q, want %q", failed.Error.Message, wantErr)
	}

	retry := dispatchInitializationGateRequest(t, srv, "initialize", initializeParams(t, t.TempDir()))
	assertAlreadyInitialized(t, retry)
	assertToolsStillBlocked(t, srv)
}

func TestServerInitializationGateSecondInitializeCannotSwitchRoots(t *testing.T) {
	srv := newInitializationGateServer(t, TransportSocket, &mockStore{})
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()

	first := dispatchInitializationGateRequest(t, srv, "initialize", initializeParams(t, firstRoot))
	if first.Error != nil {
		t.Fatalf("first initialize failed: %#v", first.Error)
	}
	if got := srv.handler.sessions.clientRoot(); got != firstRoot {
		t.Fatalf("client root after first initialize = %q, want %q", got, firstRoot)
	}

	second := dispatchInitializationGateRequest(t, srv, "initialize", initializeParams(t, secondRoot))
	assertAlreadyInitialized(t, second)
	if got := srv.handler.sessions.clientRoot(); got != firstRoot {
		t.Fatalf("client root changed after second initialize: got %q, want %q", got, firstRoot)
	}
}

func TestServerInitializationGateAllowsPingAndInternalTransport(t *testing.T) {
	t.Run("external ping before initialize", func(t *testing.T) {
		srv := newInitializationGateServer(t, TransportSocket, &mockStore{})
		resp := dispatchInitializationGateRequest(t, srv, "ping", nil)
		if resp.Error != nil {
			t.Fatalf("ping failed before initialize: %#v", resp.Error)
		}
		var result map[string]any
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			t.Fatalf("decode ping result: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("ping result = %#v, want empty object", result)
		}
	})

	t.Run("internal tools bypass initialize", func(t *testing.T) {
		srv := newInitializationGateServer(t, TransportInternal, &mockStore{})
		resp := dispatchInitializationGateRequest(t, srv, "tools/list", nil)
		if resp.Error != nil {
			t.Fatalf("internal tools/list failed before initialize: %#v", resp.Error)
		}
		if len(resp.Result) == 0 {
			t.Fatal("internal tools/list returned no result")
		}
	})
}

func newInitializationGateServer(t *testing.T, transport TransportMode, st store.Store) *Server {
	t.Helper()
	return NewServer(st, routing.NewEngine(st), &mockToolLister{}, nil, transport)
}

func dispatchInitializationGateRequest(
	t *testing.T,
	srv *Server,
	method string,
	params json.RawMessage,
) *Response {
	t.Helper()
	req := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  params,
	}
	line, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	resp := srv.dispatch(context.Background(), line)
	if resp == nil {
		t.Fatalf("%s returned no response", method)
	}
	return resp
}

func initializeParams(t *testing.T, root string) json.RawMessage {
	t.Helper()
	params := InitializeParams{
		ProtocolVersion: "2025-03-26",
		Capabilities:    ClientCapabilities{},
		ClientInfo:      ClientInfo{Name: "initialization-gate-test", Version: "1.0"},
		Roots:           []Root{{URI: (&url.URL{Scheme: "file", Path: root}).String()}},
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	return data
}

func assertAlreadyInitialized(t *testing.T, resp *Response) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("second initialize error = %#v, want code %d", resp.Error, CodeInvalidRequest)
	}
	if !strings.Contains(resp.Error.Message, "already initialized") {
		t.Fatalf("second initialize message = %q, want already initialized", resp.Error.Message)
	}
}

func assertToolsStillBlocked(t *testing.T, srv *Server) {
	t.Helper()
	resp := dispatchInitializationGateRequest(t, srv, "tools/list", nil)
	if resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("tools/list after failed initialize error = %#v, want code %d", resp.Error, CodeInvalidRequest)
	}
	if !strings.Contains(resp.Error.Message, "must initialize successfully") {
		t.Fatalf("tools/list after failed initialize message = %q", resp.Error.Message)
	}
}
