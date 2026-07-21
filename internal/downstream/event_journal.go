package downstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultJournalCap   = 256
	maxJournalCap       = 1024
	maxEventWait        = 3600 * time.Second
	eventWaitChanCap    = 16
	maxEventParamBytes  = 16 * 1024
	defaultEventReadCap = 50
)

// DownstreamEvent is one JSON-RPC server message captured from a downstream
// instance while synchronous MCP calls are in flight. Agents poll these via
// mcpx__downstream_events_* instead of receiving arbitrary streamed tool
// results.
type DownstreamEvent struct {
	Seq             int64           `json:"seq"`
	At              time.Time       `json:"at"`
	Method          string          `json:"method"`
	Params          json.RawMessage `json:"params,omitempty"`
	ParamsBytes     int             `json:"params_bytes,omitempty"`
	ParamsTruncated bool            `json:"params_truncated,omitempty"`
}

// EventStreamState is the cursor/delta payload returned by since/wait/batch
// readers.
type EventStreamState struct {
	ServerID    string            `json:"server_id"`
	AuthScopeID string            `json:"auth_scope_id,omitempty"`
	HeadSeq     int64             `json:"head_seq"`
	SinceSeq    int64             `json:"since_seq"`
	Events      []DownstreamEvent `json:"events"`
	Truncated   bool              `json:"truncated,omitempty"`
}

// EventBatchRequest names one journal stream inside a batch read.
type EventBatchRequest struct {
	ServerID    string
	AuthScopeID string
	SinceSeq    int64
}

type eventJournal struct {
	mu      sync.RWMutex
	seq     int64
	ring    []DownstreamEvent
	cap     int
	waiters []chan struct{}
}

type journalRegistry struct {
	mu       sync.RWMutex
	journals map[InstanceKey]*eventJournal
}

func newJournalRegistry() *journalRegistry {
	return &journalRegistry{journals: make(map[InstanceKey]*eventJournal)}
}

// dropForSession discards journals belonging to a per-session isolation id.
// Called from Manager.ReleaseSession so a disconnected agent's browser event
// history is reclaimed alongside its instance instead of leaking a ring per
// session for the process lifetime.
func (r *journalRegistry) dropForSession(sessionID string) {
	if sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.journals {
		if key.SessionID == sessionID {
			delete(r.journals, key)
		}
	}
}

func (r *journalRegistry) append(key InstanceKey, method string, params json.RawMessage) DownstreamEvent {
	j := r.journalFor(key)
	j.mu.Lock()
	defer j.mu.Unlock()

	j.seq++
	params, originalBytes, truncated := boundedEventParams(params)
	ev := DownstreamEvent{
		Seq:             j.seq,
		At:              time.Now().UTC(),
		Method:          method,
		Params:          params,
		ParamsBytes:     originalBytes,
		ParamsTruncated: truncated,
	}
	if j.cap <= 0 {
		j.cap = defaultJournalCap
	}
	if len(j.ring) < j.cap {
		j.ring = append(j.ring, ev)
	} else {
		copy(j.ring, j.ring[1:])
		j.ring[len(j.ring)-1] = ev
	}
	for _, ch := range j.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return ev
}

func (r *journalRegistry) since(
	key InstanceKey, sinceSeq int64, limit int, methods map[string]bool,
) EventStreamState {
	j := r.journalFor(key)
	j.mu.RLock()
	defer j.mu.RUnlock()
	return buildStreamState(key, sinceSeq, limit, methods, j.seq, j.ring, oldestRetainedSeq(j.ring))
}

func (r *journalRegistry) wait(
	ctx context.Context, key InstanceKey, sinceSeq int64, timeout time.Duration,
	limit int, methods map[string]bool,
) (EventStreamState, bool) {
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	if timeout > maxEventWait {
		timeout = maxEventWait
	}

	j := r.journalFor(key)
	trigger := make(chan struct{}, eventWaitChanCap)

	j.mu.Lock()
	j.waiters = append(j.waiters, trigger)
	state := buildStreamState(key, sinceSeq, limit, methods, j.seq, j.ring, oldestRetainedSeq(j.ring))
	j.mu.Unlock()

	if len(state.Events) > 0 {
		r.removeWaiter(key, trigger)
		return state, false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer r.removeWaiter(key, trigger)

	for {
		select {
		case <-ctx.Done():
			j.mu.RLock()
			state = buildStreamState(key, sinceSeq, limit, methods, j.seq, j.ring, oldestRetainedSeq(j.ring))
			j.mu.RUnlock()
			return state, false
		case <-timer.C:
			j.mu.RLock()
			state = buildStreamState(key, sinceSeq, limit, methods, j.seq, j.ring, oldestRetainedSeq(j.ring))
			j.mu.RUnlock()
			return state, true
		case <-trigger:
			j.mu.RLock()
			state = buildStreamState(key, sinceSeq, limit, methods, j.seq, j.ring, oldestRetainedSeq(j.ring))
			j.mu.RUnlock()
			if len(state.Events) > 0 {
				return state, false
			}
		}
	}
}

func (r *journalRegistry) batch(
	requests []EventBatchRequest, limit int, methods map[string]bool,
) []EventStreamState {
	limit = normalizeEventLimit(limit)
	out := make([]EventStreamState, 0, len(requests))
	for _, req := range requests {
		key := InstanceKey{ServerID: req.ServerID, AuthScopeID: req.AuthScopeID}
		out = append(out, r.since(key, req.SinceSeq, limit, methods))
	}
	return out
}

func (r *journalRegistry) journalFor(key InstanceKey) *eventJournal {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.journals[key]; ok {
		return j
	}
	j := &eventJournal{cap: defaultJournalCap}
	r.journals[key] = j
	return j
}

func (r *journalRegistry) removeWaiter(key InstanceKey, trigger chan struct{}) {
	r.mu.RLock()
	j := r.journals[key]
	r.mu.RUnlock()
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for i, ch := range j.waiters {
		if ch == trigger {
			j.waiters = append(j.waiters[:i], j.waiters[i+1:]...)
			return
		}
	}
}

func buildStreamState(
	key InstanceKey, sinceSeq int64, limit int, methods map[string]bool,
	headSeq int64, ring []DownstreamEvent, oldestSeq int64,
) EventStreamState {
	limit = normalizeEventLimit(limit)
	st := EventStreamState{
		ServerID:    key.ServerID,
		AuthScopeID: key.AuthScopeID,
		HeadSeq:     headSeq,
		SinceSeq:    sinceSeq,
		Events:      []DownstreamEvent{},
	}
	if oldestSeq > 0 && sinceSeq < oldestSeq-1 {
		st.Truncated = true
	}
	for _, ev := range ring {
		if ev.Seq <= sinceSeq {
			continue
		}
		if !methodMatches(methods, ev.Method) {
			continue
		}
		st.Events = append(st.Events, ev)
		if len(st.Events) >= limit {
			break
		}
	}
	return st
}

func oldestRetainedSeq(ring []DownstreamEvent) int64 {
	if len(ring) == 0 {
		return 0
	}
	return ring[0].Seq
}

func normalizeEventLimit(limit int) int {
	if limit <= 0 {
		return defaultEventReadCap
	}
	if limit > maxJournalCap {
		return maxJournalCap
	}
	return limit
}

func methodMatches(methods map[string]bool, method string) bool {
	if len(methods) == 0 {
		return true
	}
	return methods[strings.ToLower(strings.TrimSpace(method))]
}

func normalizeMethodFilter(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, m := range in {
		m = strings.ToLower(strings.TrimSpace(m))
		if m != "" {
			out[m] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundedEventParams(params json.RawMessage) (json.RawMessage, int, bool) {
	if len(params) == 0 {
		return nil, 0, false
	}
	originalBytes := len(params)
	if originalBytes <= maxEventParamBytes {
		return append(json.RawMessage(nil), params...), originalBytes, false
	}
	preview := string(params[:maxEventParamBytes])
	payload, err := json.Marshal(map[string]any{
		"truncated": true,
		"bytes":     originalBytes,
		"preview":   preview,
	})
	if err != nil {
		payload = []byte(fmt.Sprintf(`{"truncated":true,"bytes":%d}`, originalBytes))
	}
	return json.RawMessage(payload), originalBytes, true
}
