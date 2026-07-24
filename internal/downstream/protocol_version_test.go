package downstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/mcpversion"
)

func TestStdioInitializeNegotiatesSupportedVersions(t *testing.T) {
	for _, selected := range mcpversion.Supported() {
		t.Run(selected, func(t *testing.T) {
			inst := &Instance{key: InstanceKey{ServerID: "stdio-version-test"}}
			stream := fmt.Sprintf(
				"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":%q,\"capabilities\":{}}}\n",
				selected,
			)
			var stdin bytes.Buffer
			if err := inst.initialize(
				context.Background(),
				&stdin,
				bufio.NewScanner(strings.NewReader(stream)),
			); err != nil {
				t.Fatalf("initialize: %v", err)
			}

			if inst.protocolVersion != selected {
				t.Fatalf("selected version = %q, want %q", inst.protocolVersion, selected)
			}
			requests := decodeStdioRequests(t, stdin.Bytes())
			if len(requests) != 2 {
				t.Fatalf("stdio writes = %d, want initialize + initialized", len(requests))
			}
			var params initializeResult
			if err := json.Unmarshal(requests[0].Params, &params); err != nil {
				t.Fatalf("decode initialize params: %v", err)
			}
			if params.ProtocolVersion != mcpversion.Latest {
				t.Errorf(
					"proposed version = %q, want latest %q",
					params.ProtocolVersion,
					mcpversion.Latest,
				)
			}
			if requests[1].Method != "notifications/initialized" {
				t.Errorf("second write = %q, want notifications/initialized", requests[1].Method)
			}
		})
	}
}

func TestStdioInitializeRejectsUnsupportedSelection(t *testing.T) {
	for _, selected := range []string{"", "2026-01-01", "DRAFT-2026-v1"} {
		t.Run(selected, func(t *testing.T) {
			inst := &Instance{key: InstanceKey{ServerID: "stdio-version-test"}}
			stream := fmt.Sprintf(
				"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":%q,\"capabilities\":{}}}\n",
				selected,
			)
			var stdin bytes.Buffer
			err := inst.initialize(
				context.Background(),
				&stdin,
				bufio.NewScanner(strings.NewReader(stream)),
			)
			if !errors.Is(err, mcpversion.ErrUnsupported) {
				t.Fatalf("initialize error = %v, want ErrUnsupported", err)
			}
			requests := decodeStdioRequests(t, stdin.Bytes())
			if len(requests) != 1 || requests[0].Method != "initialize" {
				t.Fatalf("writes after rejection = %#v, want only initialize", requests)
			}
		})
	}
}

func TestHTTPInitializeNegotiatesAndSendsProtocolHeader(t *testing.T) {
	for _, selected := range mcpversion.Supported() {
		t.Run(selected, func(t *testing.T) {
			var mu sync.Mutex
			var proposed string
			var initializeHeader string
			var initializedHeader string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req jsonRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch req.Method {
				case "initialize":
					mu.Lock()
					initializeHeader = r.Header.Get("MCP-Protocol-Version")
					var params initializeResult
					if err := json.Unmarshal(req.Params, &params); err != nil {
						t.Errorf("decode initialize params: %v", err)
					}
					proposed = params.ProtocolVersion
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(
						w,
						`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":%q,"capabilities":{}}}`,
						selected,
					)
				case "notifications/initialized":
					mu.Lock()
					initializedHeader = r.Header.Get("MCP-Protocol-Version")
					mu.Unlock()
					w.WriteHeader(http.StatusAccepted)
				default:
					t.Errorf("unexpected method %q", req.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer ts.Close()

			h := newHTTPInstance(
				InstanceKey{ServerID: "http-version-test"},
				ts.URL,
				0,
				nil,
				nil,
			)
			if err := h.start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}
			mu.Lock()
			gotProposed := proposed
			gotInitializeHeader := initializeHeader
			gotInitializedHeader := initializedHeader
			mu.Unlock()
			if gotProposed != mcpversion.Latest {
				t.Errorf("proposed version = %q, want %q", gotProposed, mcpversion.Latest)
			}
			if gotInitializeHeader != "" {
				t.Errorf("initialize header = %q, want empty before negotiation", gotInitializeHeader)
			}
			if gotInitializedHeader != selected {
				t.Errorf(
					"initialized header = %q, want selected %q",
					gotInitializedHeader,
					selected,
				)
			}
			h.mu.Lock()
			gotSelected := h.protocolVersion
			h.mu.Unlock()
			if gotSelected != selected {
				t.Errorf("retained version = %q, want %q", gotSelected, selected)
			}
		})
	}
}

func TestHTTPInitializeRejectsUnsupportedSelection(t *testing.T) {
	for _, selected := range []string{"", "2026-01-01", "DRAFT-2026-v1"} {
		t.Run(selected, func(t *testing.T) {
			var initializedCount atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req jsonRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				if req.Method == "notifications/initialized" {
					initializedCount.Add(1)
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(
					w,
					`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":%q,"capabilities":{}}}`,
					selected,
				)
			}))
			defer ts.Close()

			h := newHTTPInstance(
				InstanceKey{ServerID: "http-version-test"},
				ts.URL,
				0,
				nil,
				nil,
			)
			err := h.start(context.Background())
			if !errors.Is(err, mcpversion.ErrUnsupported) {
				t.Fatalf("start error = %v, want ErrUnsupported", err)
			}
			if got := initializedCount.Load(); got != 0 {
				t.Fatalf("initialized notifications = %d, want 0", got)
			}
			if got := h.getState(); got != StateStopped {
				t.Fatalf("state after rejection = %s, want stopped", got)
			}
		})
	}
}

func decodeStdioRequests(t *testing.T, data []byte) []jsonRPCRequest {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var requests []jsonRPCRequest
	for scanner.Scan() {
		var req jsonRPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Fatalf("decode stdio request: %v", err)
		}
		requests = append(requests, req)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stdio requests: %v", err)
	}
	return requests
}
