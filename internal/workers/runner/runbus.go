package runner

import (
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// RunEvent kinds. Subscribers (the SSE handler) translate Kind to an
// "event:" line on the wire; payload fields used per kind are
// documented on RunEvent.
const (
	RunEventKindStatus    = "status"
	RunEventKindTextDelta = "text_delta"
	RunEventKindToolCall  = "tool_call"
	RunEventKindUsage     = "usage"
)

// RunEvent is one mid-flight signal from an executing run to live SSE
// subscribers. The bus broadcasts to every subscriber and the handler
// filters by RunID — same pattern as audit.Bus + the audit SSE handler.
//
// Kind disambiguates which payload fields are meaningful:
//
//   - status     — Run holds the full WorkerRun snapshot. Emitted on
//     prepareRun (initial running row) and persistRunFinalize
//     (terminal row). The SSE handler also synthesises a
//     status frame from the persisted row on every new
//     subscription so reconnects don't miss the starting
//     point.
//   - text_delta — Text holds one adapter turn's assistant prose. Empty-
//     string deltas are dropped at publish time so the
//     channel doesn't fill with no-ops on tool-only turns.
//   - tool_call  — ToolName + ToolInputJSON (truncated to keep frame
//     size sane) + ToolAllowed (false when propose-mode
//     gating short-circuited before dispatch).
//   - usage      — InputTokens / OutputTokens / CostUSD / ToolCalls are
//     CUMULATIVE counters after the most recent turn, not
//     per-turn deltas. Frontend renders these directly.
type RunEvent struct {
	Kind      string `json:"kind"`
	WorkerID  string `json:"worker_id"`
	RunID     string `json:"run_id"`
	Iteration int    `json:"iteration,omitempty"`

	Run *store.WorkerRun `json:"run,omitempty"`

	Text string `json:"text,omitempty"`

	ToolName      string `json:"tool_name,omitempty"`
	ToolInputJSON string `json:"tool_input_json,omitempty"`
	ToolAllowed   bool   `json:"tool_allowed,omitempty"`

	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	ToolCalls    int     `json:"tool_calls,omitempty"`

	// DelegationID + Note allow admin surfaces (create/review on a
	// delegation) to publish a lightweight signal on the same bus that
	// feeds the multiplexed 'workers' SSE channel. Frontend listeners
	// treat these as "invalidate delegations list" hints. Backwards
	// compatible (omitted for ordinary run events).
	DelegationID string `json:"delegation_id,omitempty"`
	Note         string `json:"note,omitempty"`
}

// RunBus fans out RunEvents to live SSE subscribers. Mirrors audit.Bus:
// non-blocking publish, buffered per-subscriber channel, slow consumers
// silently drop events. Buffer is larger than audit's (128 vs 64) because
// a single run can emit dozens of text_delta + tool_call events in quick
// succession during a chatty turn.
type RunBus struct {
	mu   sync.RWMutex
	subs map[<-chan *RunEvent]chan *RunEvent
}

// NewRunBus creates a new bus.
func NewRunBus() *RunBus {
	return &RunBus{
		subs: make(map[<-chan *RunEvent]chan *RunEvent),
	}
}

// Subscribe returns a receive-only channel of events. The caller MUST
// call Unsubscribe to release the channel + goroutine when done.
func (b *RunBus) Subscribe() <-chan *RunEvent {
	ch := make(chan *RunEvent, 128)
	b.mu.Lock()
	b.subs[ch] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes the listener and closes its channel.
func (b *RunBus) Unsubscribe(ch <-chan *RunEvent) {
	b.mu.Lock()
	if send, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(send)
	}
	b.mu.Unlock()
}

// Publish broadcasts to every subscriber non-blockingly. Nil bus or
// nil event is a no-op so unwired callers don't have to guard.
func (b *RunBus) Publish(ev *RunEvent) {
	if b == nil || ev == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
