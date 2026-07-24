package downstream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newScanner(s string) *bufio.Scanner {
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	return sc
}

// TestInitializeSkipsLauncherPreamble proves a cold `uvx`/`npx` spawn that
// prints non-JSON preamble to stdout before the server speaks JSON-RPC does
// NOT desync the handshake — the id==1 response is still found.
func TestInitializeSkipsLauncherPreamble(t *testing.T) {
	inst := &Instance{key: InstanceKey{ServerID: "test"}}
	stream := "Resolved 12 packages in 300ms\n" +
		"Installed mcp-server-fetch\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}` + "\n"
	var stdin bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := inst.initialize(ctx, &stdin, newScanner(stream)); err != nil {
		t.Fatalf("initialize should skip preamble and succeed, got: %v", err)
	}
	if !strings.Contains(stdin.String(), "notifications/initialized") {
		t.Errorf("initialized notification not sent; stdin=%q", stdin.String())
	}
}

// TestInitializeSkipsPreInitNotification proves a notification (no id) emitted
// before the response is skipped rather than mistaken for the response.
func TestInitializeSkipsPreInitNotification(t *testing.T) {
	inst := &Instance{key: InstanceKey{ServerID: "test"}}
	stream := `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{}}}` + "\n"
	var stdin bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := inst.initialize(ctx, &stdin, newScanner(stream)); err != nil {
		t.Fatalf("initialize should skip pre-init notification, got: %v", err)
	}
}

// TestInitializeNoResponse proves a stream that never yields the response fails
// cleanly rather than hanging or accepting garbage.
func TestInitializeNoResponse(t *testing.T) {
	inst := &Instance{key: InstanceKey{ServerID: "test"}}
	var stdin bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := inst.initialize(ctx, &stdin, newScanner("only noise\nmore noise\n")); err == nil {
		t.Fatal("initialize should fail when no id==1 response arrives")
	}
}

// TestReadResponseParseErrorEvicts proves a corrupt (non-JSON) response line is
// reported as a desync so the Manager evicts the poisoned instance.
func TestReadResponseParseErrorEvicts(t *testing.T) {
	inst := &Instance{key: InstanceKey{ServerID: "test"}}
	_, err := inst.readResponse(newScanner("this is not json\n"), 7)
	if err == nil {
		t.Fatal("expected error on non-JSON response line")
	}
	if !errors.Is(err, ErrResponseDesync) {
		t.Errorf("parse error must wrap ErrResponseDesync so the instance is evicted; got %v", err)
	}
}
