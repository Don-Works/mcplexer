package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/downstream"
)

type eventToolLister struct {
	tools map[string]json.RawMessage

	sinceKey     downstream.InstanceKey
	sinceSeq     int64
	sinceLimit   int
	sinceMethods []string
	sinceState   downstream.EventStreamState

	waitKey      downstream.InstanceKey
	waitSeq      int64
	waitTimeout  time.Duration
	waitLimit    int
	waitMethods  []string
	waitState    downstream.EventStreamState
	waitTimedOut bool

	batchRequests []downstream.EventBatchRequest
	batchLimit    int
	batchMethods  []string
	batchStates   []downstream.EventStreamState
}

func (m *eventToolLister) ListAllTools(_ context.Context) (map[string]json.RawMessage, error) {
	return m.tools, nil
}

func (m *eventToolLister) ListToolsForServers(_ context.Context, serverIDs []string) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage)
	for _, id := range serverIDs {
		if tools, ok := m.tools[id]; ok {
			result[id] = tools
		}
	}
	return result, nil
}

func (m *eventToolLister) Call(
	_ context.Context, serverID, authScopeID, toolName string, args json.RawMessage,
) (json.RawMessage, error) {
	return nil, nil
}

func (m *eventToolLister) EventsSince(
	key downstream.InstanceKey, sinceSeq int64, limit int, methods []string,
) downstream.EventStreamState {
	m.sinceKey = key
	m.sinceSeq = sinceSeq
	m.sinceLimit = limit
	m.sinceMethods = methods
	return m.sinceState
}

func (m *eventToolLister) WaitForEvents(
	_ context.Context, key downstream.InstanceKey, sinceSeq int64, timeout time.Duration,
	limit int, methods []string,
) (downstream.EventStreamState, bool) {
	m.waitKey = key
	m.waitSeq = sinceSeq
	m.waitTimeout = timeout
	m.waitLimit = limit
	m.waitMethods = methods
	return m.waitState, m.waitTimedOut
}

func (m *eventToolLister) EventsBatch(
	requests []downstream.EventBatchRequest, limit int, methods []string,
) []downstream.EventStreamState {
	m.batchRequests = requests
	m.batchLimit = limit
	m.batchMethods = methods
	return m.batchStates
}

func toolResultTextForEvents(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var result CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	return result.Content[0].Text
}

func TestHandleDownstreamEventsSince(t *testing.T) {
	mgr := &eventToolLister{
		sinceState: downstream.EventStreamState{
			ServerID:    "browser",
			AuthScopeID: "scope-1",
			HeadSeq:     8,
			SinceSeq:    4,
			Events: []downstream.DownstreamEvent{{
				Seq:    5,
				Method: "notifications/progress",
				Params: json.RawMessage(`{"progress":50}`),
			}},
		},
	}
	h := &handler{manager: mgr}

	raw, rpcErr := h.handleDownstreamEventsSince(context.Background(), json.RawMessage(
		`{"server_id":"browser","auth_scope_id":"scope-1","since_seq":4,"limit":2,"methods":["notifications/progress"]}`,
	))
	if rpcErr != nil {
		t.Fatalf("rpcErr = %v", rpcErr)
	}
	if mgr.sinceKey.ServerID != "browser" || mgr.sinceKey.AuthScopeID != "scope-1" {
		t.Fatalf("key = %+v, want browser/scope-1", mgr.sinceKey)
	}
	if mgr.sinceSeq != 4 || mgr.sinceLimit != 2 {
		t.Fatalf("cursor args = seq %d limit %d, want 4/2", mgr.sinceSeq, mgr.sinceLimit)
	}
	if len(mgr.sinceMethods) != 1 || mgr.sinceMethods[0] != "notifications/progress" {
		t.Fatalf("methods = %v, want progress filter", mgr.sinceMethods)
	}

	var payload struct {
		ServerID string                       `json:"server_id"`
		HeadSeq  int64                        `json:"head_seq"`
		Count    int                          `json:"count"`
		Events   []downstream.DownstreamEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(toolResultTextForEvents(t, raw)), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ServerID != "browser" || payload.HeadSeq != 8 || payload.Count != 1 {
		t.Fatalf("payload = %+v, want browser/head 8/count 1", payload)
	}
	if string(payload.Events[0].Params) != `{"progress":50}` {
		t.Fatalf("event params = %s, want preserved payload", string(payload.Events[0].Params))
	}
}

func TestHandleDownstreamEventsWait(t *testing.T) {
	mgr := &eventToolLister{
		waitState: downstream.EventStreamState{
			ServerID: "browser",
			HeadSeq:  9,
			SinceSeq: 9,
			Events:   []downstream.DownstreamEvent{},
		},
		waitTimedOut: true,
	}
	h := &handler{manager: mgr}

	raw, rpcErr := h.handleDownstreamEventsWait(context.Background(), json.RawMessage(
		`{"server_id":"browser","since_seq":9,"timeout_seconds":3,"limit":7}`,
	))
	if rpcErr != nil {
		t.Fatalf("rpcErr = %v", rpcErr)
	}
	if mgr.waitKey.ServerID != "browser" || mgr.waitSeq != 9 || mgr.waitLimit != 7 {
		t.Fatalf("wait args = key %+v seq %d limit %d", mgr.waitKey, mgr.waitSeq, mgr.waitLimit)
	}
	if mgr.waitTimeout != 3*time.Second {
		t.Fatalf("timeout = %v, want 3s", mgr.waitTimeout)
	}

	var payload struct {
		TimedOut bool `json:"timed_out"`
		Count    int  `json:"count"`
	}
	if err := json.Unmarshal([]byte(toolResultTextForEvents(t, raw)), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.TimedOut || payload.Count != 0 {
		t.Fatalf("payload = %+v, want timed_out true/count 0", payload)
	}
}

func TestHandleDownstreamEventsBatch(t *testing.T) {
	mgr := &eventToolLister{
		batchStates: []downstream.EventStreamState{
			{ServerID: "browser-a", HeadSeq: 3, Events: []downstream.DownstreamEvent{{Seq: 3, Method: "n"}}},
			{ServerID: "browser-b", HeadSeq: 4, Events: []downstream.DownstreamEvent{}},
		},
	}
	h := &handler{manager: mgr}

	raw, rpcErr := h.handleDownstreamEventsBatch(context.Background(), json.RawMessage(
		`{"streams":[{"server_id":"browser-a","since_seq":2},{"server_id":"browser-b","auth_scope_id":"s","since_seq":4}],"limit":5,"methods":["n"]}`,
	))
	if rpcErr != nil {
		t.Fatalf("rpcErr = %v", rpcErr)
	}
	if len(mgr.batchRequests) != 2 {
		t.Fatalf("batchRequests = %d, want 2", len(mgr.batchRequests))
	}
	if mgr.batchRequests[1].AuthScopeID != "s" || mgr.batchRequests[1].SinceSeq != 4 {
		t.Fatalf("second request = %+v, want auth scope s / since 4", mgr.batchRequests[1])
	}
	if mgr.batchLimit != 5 || len(mgr.batchMethods) != 1 || mgr.batchMethods[0] != "n" {
		t.Fatalf("batch filter args = limit %d methods %v", mgr.batchLimit, mgr.batchMethods)
	}

	var payload struct {
		Count   int                           `json:"count"`
		Streams []downstream.EventStreamState `json:"streams"`
	}
	if err := json.Unmarshal([]byte(toolResultTextForEvents(t, raw)), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Count != 2 || len(payload.Streams) != 2 {
		t.Fatalf("payload = %+v, want two streams", payload)
	}
}

func TestDownstreamEventToolsInBuiltinLists(t *testing.T) {
	rawMgr := &eventToolLister{tools: map[string]json.RawMessage{}}
	mgr := cache.NewCachingToolLister(rawMgr, cache.NewToolCache(nil))
	h, _ := newTestHandler(mgr, nil)

	codeModeTools := h.codeModeBuiltinTools()
	searchable := h.searchableBuiltins(context.Background())

	for _, name := range []string{
		"mcpx__downstream_events_since",
		"mcpx__downstream_events_wait",
		"mcpx__downstream_events_batch",
	} {
		if !toolListHasName(codeModeTools, name) {
			t.Fatalf("%s missing from code mode builtin tools", name)
		}
		if !toolListHasName(searchable, name) {
			t.Fatalf("%s missing from searchable builtins", name)
		}
	}
}

func TestDownstreamEventToolDefinitionsAreReadOnly(t *testing.T) {
	for _, tool := range downstreamEventToolDefinitions() {
		raw, ok := tool.Extras["annotations"]
		if !ok {
			t.Fatalf("%s missing annotations", tool.Name)
		}
		var annotations ToolAnnotations
		if err := json.Unmarshal(raw, &annotations); err != nil {
			t.Fatalf("unmarshal annotations for %s: %v", tool.Name, err)
		}
		if annotations.ReadOnlyHint == nil || !*annotations.ReadOnlyHint {
			t.Fatalf("%s ReadOnlyHint = %v, want true", tool.Name, annotations.ReadOnlyHint)
		}
	}
}

func toolListHasName(tools []Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
