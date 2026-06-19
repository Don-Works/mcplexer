package downstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestReadResponse_IDMatching is the regression test for the stdio
// response/request desync bug. readResponse MUST only return a response
// whose id matches the request it was issued for; a stale late response
// (e.g. a prior request whose caller timed out) must surface as
// ErrResponseDesync rather than being mistaken for the current call's
// result — which would leak one request's data into another's reply.
func TestReadResponse_IDMatching(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		expectID  int
		wantErr   error
		wantOK    bool
		wantNotes int // expected onNotify call count
	}{
		{
			name: "matching numeric id returns result",
			lines: []string{
				`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`,
			},
			expectID: 7,
			wantOK:   true,
		},
		{
			name: "matching id after a notification",
			lines: []string{
				`{"jsonrpc":"2.0","method":"notifications/progress"}`,
				`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`,
			},
			expectID:  7,
			wantOK:    true,
			wantNotes: 1,
		},
		{
			name: "string-encoded id still matches",
			lines: []string{
				`{"jsonrpc":"2.0","id":"7","result":{"ok":true}}`,
			},
			expectID: 7,
			wantOK:   true,
		},
		{
			name: "stale late response with wrong id is a hard desync error",
			lines: []string{
				// id=3 is the late response of an abandoned prior call;
				// the current call is waiting on id=4. Must NOT be returned.
				`{"jsonrpc":"2.0","id":3,"result":{"secret":"prior-callers-data"}}`,
			},
			expectID: 4,
			wantErr:  ErrResponseDesync,
		},
		{
			name: "stale response then correct response: still desyncs (fail fast)",
			lines: []string{
				`{"jsonrpc":"2.0","id":3,"result":{"secret":"leak"}}`,
				`{"jsonrpc":"2.0","id":4,"result":{"ok":true}}`,
			},
			expectID: 4,
			wantErr:  ErrResponseDesync,
		},
		{
			name: "notification skipped, then wrong id desyncs",
			lines: []string{
				`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
				`{"jsonrpc":"2.0","id":99,"result":{"x":1}}`,
			},
			expectID:  4,
			wantErr:   ErrResponseDesync,
			wantNotes: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := strings.Join(tt.lines, "\n") + "\n"
			scanner := bufio.NewScanner(strings.NewReader(in))
			scanner.Buffer(make([]byte, 64*1024), 64*1024)

			var notes atomic.Int32
			inst := &Instance{
				key:      InstanceKey{ServerID: "test-server"},
				onNotify: func(string, json.RawMessage) { notes.Add(1) },
			}

			result, err := inst.readResponse(scanner, tt.expectID)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want errors.Is(err, %v)", err, tt.wantErr)
				}
				if result != nil {
					t.Errorf("result = %s, want nil on desync (no data leak)", string(result))
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.wantOK && result == nil {
					t.Fatal("result is nil, want a payload")
				}
			}
			if int(notes.Load()) != tt.wantNotes {
				t.Errorf("onNotify called %d times, want %d", notes.Load(), tt.wantNotes)
			}
		})
	}
}

// TestResponseIDMatches unit-tests the id comparison helper directly.
func TestResponseIDMatches(t *testing.T) {
	tests := []struct {
		name     string
		rawID    string
		expectID int
		want     bool
	}{
		{"numeric match", `7`, 7, true},
		{"numeric mismatch", `3`, 4, false},
		{"string-encoded match", `"7"`, 7, true},
		{"string-encoded mismatch", `"3"`, 4, false},
		{"whitespace tolerated", ` 7 `, 7, true},
		{"zero matches zero", `0`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := responseIDMatches([]byte(tt.rawID), tt.expectID)
			if got != tt.want {
				t.Errorf("responseIDMatches(%q, %d) = %v, want %v",
					tt.rawID, tt.expectID, got, tt.want)
			}
		})
	}
}

func TestReadResponse_SkipsNotifications(t *testing.T) {
	// Simulate a downstream that sends a notification before the response.
	lines := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`,
	}, "\n") + "\n"

	scanner := bufio.NewScanner(strings.NewReader(lines))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var notified atomic.Int32
	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
		onNotify: func(method string, _ json.RawMessage) {
			if method == "notifications/tools/list_changed" {
				notified.Add(1)
			}
		},
	}

	result, err := inst.readResponse(scanner, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	if notified.Load() != 1 {
		t.Errorf("notification callback called %d times, want 1", notified.Load())
	}
}

func TestReadResponse_MultipleNotificationsBeforeResponse(t *testing.T) {
	lines := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"token":"abc"}}`,
		`{"jsonrpc":"2.0","id":5,"result":{"tools":[]}}`,
	}, "\n") + "\n"

	scanner := bufio.NewScanner(strings.NewReader(lines))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var methods []string
	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
		onNotify: func(method string, _ json.RawMessage) {
			methods = append(methods, method)
		},
	}

	result, err := inst.readResponse(scanner, 5)
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

func TestReadResponse_NoNotifications(t *testing.T) {
	lines := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n"

	scanner := bufio.NewScanner(strings.NewReader(lines))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
	}

	result, err := inst.readResponse(scanner, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestReadResponse_DownstreamError(t *testing.T) {
	lines := `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad request"}}` + "\n"

	scanner := bufio.NewScanner(strings.NewReader(lines))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
	}

	_, err := inst.readResponse(scanner, 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error = %q, want to contain 'bad request'", err.Error())
	}
}

// TestForwardNotification_PreservesParams verifies that
// forwardNotification extracts and passes the full params payload
// to onNotify, not just the method name.
func TestForwardNotification_PreservesParams(t *testing.T) {
	var (
		gotMethod string
		gotParams []byte
	)
	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
		onNotify: func(method string, params json.RawMessage) {
			gotMethod = method
			gotParams = params
		},
	}

	data := []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"token":"abc","progress":50,"total":100}}`)
	inst.forwardNotification(data)

	if gotMethod != "notifications/progress" {
		t.Errorf("method = %q, want notifications/progress", gotMethod)
	}
	if string(gotParams) != `{"token":"abc","progress":50,"total":100}` {
		t.Errorf("params = %s, want full params payload", string(gotParams))
	}
}

// TestForwardNotification_StripsNothing verifies that even notifications
// without params are handled correctly.
func TestForwardNotification_NoParams(t *testing.T) {
	var gotMethod string
	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
		onNotify: func(method string, params json.RawMessage) {
			gotMethod = method
		},
	}

	data := []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	inst.forwardNotification(data)

	if gotMethod != "notifications/tools/list_changed" {
		t.Errorf("method = %q, want notifications/tools/list_changed", gotMethod)
	}
}

// TestReadResponse_PreservesNotificationParams verifies that
// notifications interleaved before a response have their params
// forwarded to onNotify.
func TestReadResponse_PreservesNotificationParams(t *testing.T) {
	lines := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"token":"abc","progress":25}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"token":"abc","progress":50}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
	}, "\n") + "\n"

	scanner := bufio.NewScanner(strings.NewReader(lines))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var notifications []string
	inst := &Instance{
		key: InstanceKey{ServerID: "test-server"},
		onNotify: func(method string, params json.RawMessage) {
			notifications = append(notifications, string(params))
		},
	}

	result, err := inst.readResponse(scanner, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(notifications) != 2 {
		t.Fatalf("got %d notifications, want 2", len(notifications))
	}
	// Verify params were preserved, not stripped.
	if !strings.Contains(notifications[0], `"progress":25`) {
		t.Errorf("notification 0 params = %s, want progress:25", notifications[0])
	}
	if !strings.Contains(notifications[1], `"progress":50`) {
		t.Errorf("notification 1 params = %s, want progress:50", notifications[1])
	}
}

type captureWriteCloser struct {
	bytes.Buffer
}

func (c *captureWriteCloser) Close() error { return nil }

func TestSendCancelledWritesJSONRPCNotification(t *testing.T) {
	w := &captureWriteCloser{}
	inst := &Instance{stdin: w}

	inst.sendCancelled(7, "context deadline exceeded")

	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &msg); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if msg.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", msg.JSONRPC)
	}
	if msg.Method != "notifications/cancelled" {
		t.Errorf("method = %q, want notifications/cancelled", msg.Method)
	}

	var params struct {
		RequestID int    `json:"requestId"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.RequestID != 7 {
		t.Errorf("requestId = %d, want 7", params.RequestID)
	}
	if params.Reason != "context deadline exceeded" {
		t.Errorf("reason = %q, want context deadline exceeded", params.Reason)
	}
}
