package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// readNamedEvents reads up to want named SSE frames (skipping `:` heartbeat
// comment lines) from r, returning a channel->data map. Blocks until want
// frames arrive or the stream closes.
func readNamedEvents(t *testing.T, r *bufio.Reader, want int) map[string]string {
	t.Helper()
	got := make(map[string]string, want)
	var curEvent string
	for len(got) < want {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream (got %d/%d frames): %v", len(got), want, err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			curEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if curEvent != "" {
				got[curEvent] = strings.TrimPrefix(line, "data: ")
				curEvent = ""
			}
		}
	}
	return got
}

// TestEventsSSE_Multiplex wires all real buses, opens the consolidated
// stream, publishes one event per bus, and asserts each arrives as a named
// `event: <channel>` frame carrying the per-stream JSON. This guards the
// connection-pool-exhaustion fix: a regression in the per-channel naming or
// the multiplex select would ship silently.
func TestEventsSSE_Multiplex(t *testing.T) {
	notifyBus := notify.NewBus()
	approvalBus := approval.NewBus()
	sessionBus := session.NewBus()
	secretBus := ephemeral.NewBus()
	tasksBus := tasks.NewBus()
	workerBus := runner.NewRunBus()

	h := &eventsSSEHandler{
		notifyBus:   notifyBus,
		approvalBus: approvalBus,
		sessionBus:  sessionBus,
		secretBus:   secretBus,
		tasksBus:    tasksBus,
		workerBus:   workerBus,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/events/stream", h.stream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Closing the body cancels the server-side request context, which makes
	// stream() return via its ctx.Done() case — exercising teardown.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The handler flushes its SSE headers BEFORE registering bus
	// subscriptions, so Do() can return ahead of the Subscribe calls. A
	// publish landing before Subscribe is lost (no channel yet), so wait
	// briefly for the handler to settle; the buffered channels then
	// guarantee delivery of everything we publish after.
	time.Sleep(150 * time.Millisecond)

	notifyBus.Publish(notify.Event{MessageID: "m1", Source: "system", Title: "hi"})
	approvalBus.Publish(approval.ApprovalEvent{Type: "pending", Approval: &store.ToolApproval{ID: "a1"}})
	sessionBus.Publish(session.Event{Type: session.EventConnected, Session: store.Session{ID: "s1"}})
	secretBus.Publish(ephemeral.Event{Type: "pending", ID: "sec1"})
	tasksBus.Publish(tasks.Event{Kind: tasks.EventTaskCreated, WorkspaceID: "ws1"})
	workerBus.Publish(&runner.RunEvent{
		Kind:     runner.RunEventKindStatus,
		WorkerID: "w1",
		RunID:    "r1",
		Run:      &store.WorkerRun{ID: "r1", WorkerID: "w1", Status: "running"},
	})

	r := bufio.NewReader(resp.Body)
	got := readNamedEvents(t, r, 6)

	for _, ch := range []string{"notifications", "approvals", "sessions", "secrets", "tasks", "workers"} {
		if _, ok := got[ch]; !ok {
			t.Errorf("missing %q frame; got channels = %v", ch, keys(got))
		}
	}
	// Spot-check one payload routed losslessly.
	if data := got["notifications"]; !strings.Contains(data, `"message_id":"m1"`) {
		t.Errorf("notifications payload = %q, want message_id m1", data)
	}
	if data := got["tasks"]; !strings.Contains(data, `"kind":"task_created"`) {
		t.Errorf("tasks payload = %q, want kind task_created", data)
	}
	if data := got["workers"]; !strings.Contains(data, `"worker_id":"w1"`) {
		t.Errorf("workers payload = %q, want worker_id w1", data)
	}
}

// TestEventsSSE_NilBusesNoPanic confirms the all-nil-bus stream serves a 200
// and tears down cleanly when the client disconnects — missing buses are
// absent channels, never a panic.
func TestEventsSSE_NilBusesNoPanic(t *testing.T) {
	h := &eventsSSEHandler{} // every bus nil
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/events/stream", h.stream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Read the initial flush (headers + empty), then disconnect. The handler
	// must not panic on nil channels and must return via ctx.Done().
	_ = resp.Body.Close()
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
