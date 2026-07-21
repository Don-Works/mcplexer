package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestHTTPInstance() *HTTPInstance {
	return &HTTPInstance{
		key: InstanceKey{ServerID: "test-http"},
	}
}

func expectedID(id int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`%d`, id))
}

func readTestSSEResponse(t *testing.T, h *HTTPInstance, stream string, id int) (json.RawMessage, error) {
	t.Helper()
	return h.readSSEResponse(strings.NewReader(stream), expectedID(id))
}

func TestReadSSEResponse_SingleResult(t *testing.T) {
	stream := "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n"
	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if _, ok := m["tools"]; !ok {
		t.Errorf("expected tools key in result, got %v", m)
	}
}

func TestReadSSEResponse_NotificationThenResult(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"reason":"refresh"}}`,
		"",
		`data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"foo"}]}}`,
		"",
	}, "\n")

	var notifyMethod string
	var notifyParams json.RawMessage
	h := newTestHTTPInstance()
	h.onNotify = func(method string, params json.RawMessage) {
		notifyMethod = method
		notifyParams = params
	}

	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if notifyMethod != "notifications/tools/list_changed" {
		t.Errorf("onNotify called with %q, want notifications/tools/list_changed", notifyMethod)
	}
	if string(notifyParams) != `{"reason":"refresh"}` {
		t.Errorf("notification params = %s, want reason payload", string(notifyParams))
	}
}

func TestReadSSEResponse_MultipleNotificationsBeforeResult(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		"",
		`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"token":"abc","progress":50,"total":100}}`,
		"",
		`data: {"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"done"}]}}`,
		"",
	}, "\n")

	var methods []string
	h := newTestHTTPInstance()
	h.onNotify = func(method string, params json.RawMessage) {
		methods = append(methods, method)
	}

	result, err := readTestSSEResponse(t, h, stream, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(methods) != 2 {
		t.Fatalf("got %d notifications, want 2", len(methods))
	}
	if methods[0] != "notifications/tools/list_changed" {
		t.Errorf("methods[0] = %q", methods[0])
	}
	if methods[1] != "notifications/progress" {
		t.Errorf("methods[1] = %q", methods[1])
	}
}

func TestReadSSEResponse_MultiLineData(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","id":1,`,
		`data: "result":{"content":[{"type":"text","text":"line1\nline2"}]}}`,
		"",
	}, "\n")

	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestReadSSEResponse_CommentLinesIgnored(t *testing.T) {
	stream := strings.Join([]string{
		": this is a comment",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		": another comment",
		"",
	}, "\n")

	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestReadSSEResponse_ErrorResponse(t *testing.T) {
	stream := `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad request"}}` + "\n\n"
	h := newTestHTTPInstance()
	_, err := readTestSSEResponse(t, h, stream, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error = %q, want to contain bad request", err.Error())
	}
}

func TestReadSSEResponse_MismatchedResponseID(t *testing.T) {
	stream := `data: {"jsonrpc":"2.0","id":2,"result":{"ok":true}}` + "\n\n"
	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if !errors.Is(err, ErrResponseDesync) {
		t.Fatalf("err = %v, want ErrResponseDesync", err)
	}
	if result != nil {
		t.Fatalf("result = %s, want nil on desync", string(result))
	}
}

func TestReadSSEResponse_NoResult(t *testing.T) {
	stream := ": just a comment\n\n"
	h := newTestHTTPInstance()
	_, err := readTestSSEResponse(t, h, stream, 1)
	if err == nil {
		t.Fatal("expected error for empty stream")
	}
	if !strings.Contains(err.Error(), "no result in sse stream") {
		t.Errorf("error = %q, want no result in sse stream", err.Error())
	}
}

func TestReadSSEResponse_NonJSONDataLinesSkipped(t *testing.T) {
	stream := strings.Join([]string{
		"data: not-json-at-all",
		"",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		"",
	}, "\n")

	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestReadSSEResponse_MethodBearingMessageIgnoredAsResponse(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","id":99,"method":"sampling/createMessage","result":{"not":"the response"}}`,
		"",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		"",
	}, "\n")

	var notifyMethod string
	h := newTestHTTPInstance()
	h.onNotify = func(method string, _ json.RawMessage) {
		notifyMethod = method
	}

	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s, want final matching response", string(result))
	}
	if notifyMethod != "sampling/createMessage" {
		t.Errorf("notifyMethod = %q, want sampling/createMessage", notifyMethod)
	}
}

func TestReadSSEResponse_EventFieldIgnored(t *testing.T) {
	stream := strings.Join([]string{
		"event: message",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		"",
	}, "\n")

	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestReadSSEResponse_NoBlankLineAfterLastEvent(t *testing.T) {
	stream := `data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"
	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestReadSSEResponse_ErrorThenResult(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"first-error"}}`,
		"",
		`data: {"jsonrpc":"2.0","id":2,"result":{"ok":true}}`,
		"",
	}, "\n")

	h := newTestHTTPInstance()
	_, err := readTestSSEResponse(t, h, stream, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "first-error") {
		t.Errorf("error = %q, want to contain first-error", err.Error())
	}
}

func TestReadSSEResponse_StdioNotificationForwarding(t *testing.T) {
	var count atomic.Int32
	h := newTestHTTPInstance()
	h.onNotify = func(method string, params json.RawMessage) {
		if method == "notifications/tools/list_changed" {
			count.Add(1)
		}
	}

	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		"",
		`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		"",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		"",
	}, "\n")

	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if count.Load() != 2 {
		t.Errorf("onNotify called %d times, want 2", count.Load())
	}
}

func TestReadSSEResponse_NonJSONRPCJSONIgnored(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"hello":"world"}`,
		"",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		"",
	}, "\n")

	h := newTestHTTPInstance()
	result, err := readTestSSEResponse(t, h, stream, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestProcessSSEData(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantDone   bool
		wantResult string
		wantErr    bool
		wantNotify string
	}{
		{
			name:       "notification returns not done",
			raw:        `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
			wantNotify: "notifications/tools/list_changed",
		},
		{
			name:       "response with result returns done",
			raw:        `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
			wantDone:   true,
			wantResult: `{"tools":[]}`,
		},
		{
			name:     "response with error returns done and error",
			raw:      `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"fail"}}`,
			wantDone: true,
			wantErr:  true,
		},
		{
			name: "invalid JSON returns not done",
			raw:  `not-json`,
		},
		{
			name:       "method with result shape is notification",
			raw:        `{"jsonrpc":"2.0","id":1,"method":"sampling/createMessage","result":{"bad":true}}`,
			wantNotify: "sampling/createMessage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var notified string
			h := newTestHTTPInstance()
			h.onNotify = func(method string, _ json.RawMessage) {
				notified = method
			}
			gotResult, gotDone, gotErr := h.processSSEData(tt.raw, expectedID(1))
			if gotDone != tt.wantDone {
				t.Errorf("done = %v, want %v", gotDone, tt.wantDone)
			}
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if string(gotResult) != tt.wantResult {
				t.Errorf("result = %s, want %s", string(gotResult), tt.wantResult)
			}
			if notified != tt.wantNotify {
				t.Errorf("notified = %q, want %q", notified, tt.wantNotify)
			}
		})
	}
}

func TestDoRPC_JSONResponseIDMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer srv.Close()

	h := newTestHTTPInstance()
	h.url = srv.URL
	h.client = srv.Client()

	_, err := h.doRPC(context.Background(), jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      expectedID(1),
		Method:  "tools/list",
	})
	if !errors.Is(err, ErrResponseDesync) {
		t.Fatalf("err = %v, want ErrResponseDesync", err)
	}
}

func TestSSEResponseErrorIs(t *testing.T) {
	stream := `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}` + "\n\n"
	h := newTestHTTPInstance()
	_, err := readTestSSEResponse(t, h, stream, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, err) {
		t.Error("expected errors.Is to match")
	}
}
