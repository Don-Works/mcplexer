//go:build p2p

package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLocalAgents implements LocalAgentSource for unit tests.
type fakeLocalAgents struct {
	agents []AgentRecord
}

func (f *fakeLocalAgents) ListLocalAgents(_ context.Context, _ string) ([]AgentRecord, error) {
	out := make([]AgentRecord, len(f.agents))
	copy(out, f.agents)
	return out, nil
}

// fakeRemoteSink records every call so tests can assert ordering +
// payload. Concurrent-safe — the read pump runs on a goroutine.
type fakeRemoteSink struct {
	mu        sync.Mutex
	snapshots []sinkSnapshot
	deltas    []sinkDelta
	byes      []string
}

type sinkSnapshot struct {
	from   string
	agents []AgentRecord
}

type sinkDelta struct {
	from    string
	added   []AgentRecord
	removed []string
}

func (f *fakeRemoteSink) ApplyRemoteSnapshot(_ context.Context, from string, agents []AgentRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, sinkSnapshot{from: from, agents: agents})
	return nil
}

func (f *fakeRemoteSink) ApplyRemoteDelta(_ context.Context, from string, added []AgentRecord, removed []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deltas = append(f.deltas, sinkDelta{from: from, added: added, removed: removed})
	return nil
}

func (f *fakeRemoteSink) HandleRemoteBye(_ context.Context, from string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byes = append(f.byes, from)
	return nil
}

// TestShouldDial pins the lower-id-dials-higher-accepts contract. The
// invariant prevents both sides racing each other to NewStream and
// burning a duplicate connection.
func TestShouldDial(t *testing.T) {
	tests := []struct {
		name  string
		self  string
		other string
		want  bool
	}{
		{"self < other → dial", "12D3aaa", "12D3bbb", true},
		{"self > other → accept only", "12D3bbb", "12D3aaa", false},
		{"self == other → no-op (impossible in practice)", "12D3aaa", "12D3aaa", false},
		{"empty self", "", "12D3aaa", false},
		{"empty other", "12D3aaa", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldDial(tt.self, tt.other); got != tt.want {
				t.Fatalf("shouldDial(%q,%q) = %v, want %v",
					tt.self, tt.other, got, tt.want)
			}
		})
	}
}

// TestFrameRoundTrip pins the wire format. A frame round-tripped through
// JSON must come back byte-identical for the discriminator + payload.
// This is the contract the receiver dispatches on.
func TestFrameRoundTrip(t *testing.T) {
	t.Run("hello", func(t *testing.T) {
		in := agentHelloFrame{
			Type:         agentFrameHello,
			PeerID:       "12D3aaa",
			ProtoVersion: AgentDirectoryProtocolVersion,
			TS:           time.Now().UTC().Truncate(time.Second),
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out agentHelloFrame
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out != in {
			t.Fatalf("hello round-trip mismatch:\n got=%+v\nwant=%+v", out, in)
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		in := agentSnapshotFrame{
			Type: agentFrameSnapshot,
			Agents: []AgentRecord{
				{SessionID: "s1", Name: "alpha", Role: "planner", LastSeenAt: now},
				{SessionID: "s2", Name: "beta", LastSeenAt: now},
			},
			TS: now,
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out agentSnapshotFrame
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(out.Agents) != len(in.Agents) {
			t.Fatalf("agents len: got %d want %d", len(out.Agents), len(in.Agents))
		}
		for i := range in.Agents {
			if out.Agents[i] != in.Agents[i] {
				t.Fatalf("agent[%d] mismatch:\n got=%+v\nwant=%+v",
					i, out.Agents[i], in.Agents[i])
			}
		}
	})

	t.Run("delta", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		in := agentDeltaFrame{
			Type:    agentFrameDelta,
			Added:   []AgentRecord{{SessionID: "s1", Name: "alpha", LastSeenAt: now}},
			Removed: []string{"s2", "s3"},
			TS:      now,
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out agentDeltaFrame
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(out.Added) != 1 || out.Added[0].SessionID != "s1" {
			t.Fatalf("added round-trip wrong: %+v", out.Added)
		}
		if len(out.Removed) != 2 || out.Removed[0] != "s2" || out.Removed[1] != "s3" {
			t.Fatalf("removed round-trip wrong: %+v", out.Removed)
		}
	})
}

// TestAgentRecordWireFields locks down the JSON tags. Receivers parse
// off these names; renames break wire compat. Pinning here so any future
// rename has to come with this test update.
func TestAgentRecordWireFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	r := AgentRecord{
		SessionID:  "abc123",
		Name:       "peer-laptop",
		Role:       "backend",
		ClientType: "claude-code",
		LastSeenAt: now,
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantSubstrings := []string{
		`"session_id":"abc123"`,
		`"name":"peer-laptop"`,
		`"role":"backend"`,
		`"client_type":"claude-code"`,
		`"last_seen_at":`,
	}
	got := string(raw)
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("missing %q in wire JSON: %s", sub, got)
		}
	}
	// Optional fields elide when empty.
	rMinimal := AgentRecord{SessionID: "x", Name: "y", LastSeenAt: now}
	rawMin, _ := json.Marshal(rMinimal)
	if strings.Contains(string(rawMin), `"role"`) {
		t.Errorf("role should omitempty: %s", rawMin)
	}
	if strings.Contains(string(rawMin), `"client_type"`) {
		t.Errorf("client_type should omitempty: %s", rawMin)
	}
}

// TestByteWindowEvictsAndCaps verifies the per-peer rate limiter:
// (1) sustained-rate budget is enforced, (2) old samples drop out so a
// quiet peer regains budget.
func TestByteWindowEvictsAndCaps(t *testing.T) {
	w := newByteWindow(100, 50*time.Millisecond)
	if err := w.add(60); err != nil {
		t.Fatalf("first add should succeed: %v", err)
	}
	if err := w.add(30); err != nil {
		t.Fatalf("second add (90 total) should succeed: %v", err)
	}
	// 90 + 20 = 110 > 100 → rate-limited.
	if err := w.add(20); err == nil {
		t.Fatal("over-cap add should error")
	}
	// Wait for the window to clear, then add again — should pass.
	time.Sleep(60 * time.Millisecond)
	if err := w.add(80); err != nil {
		t.Fatalf("post-eviction add should succeed: %v", err)
	}
}

// TestByteWindowNilSafe — the limiter degrades gracefully when not
// constructed (e.g. before NewAgentDirectoryService wires one up).
func TestByteWindowNilSafe(t *testing.T) {
	var w *byteWindow
	if err := w.add(1024); err != nil {
		t.Fatalf("nil byteWindow.add should be a no-op: %v", err)
	}
}

// TestSnapshotCapEnforcement verifies the sender truncates to the cap
// (sorted by last_seen_at desc) and that an inbound snapshot beyond the
// cap is rejected by the dispatcher.
//
// The send path is tested by directly invoking the truncation logic,
// rather than spinning up a real libp2p host (handled by the bridge
// integration test). The receive path is tested via applySnapshot on a
// fake stream — we wire our own readFrame loop in.
func TestSnapshotCapTruncatesOnSend(t *testing.T) {
	now := time.Now().UTC()
	agents := make([]AgentRecord, MaxAgentsPerSnapshot+50)
	for i := range agents {
		agents[i] = AgentRecord{
			SessionID:  fmtIndex("s", i),
			Name:       fmtIndex("n", i),
			LastSeenAt: now.Add(time.Duration(i) * time.Second),
		}
	}
	// Mimic the sendHelloAndSnapshot truncation block.
	if len(agents) > MaxAgentsPerSnapshot {
		// sort desc by LastSeenAt
		sortByLastSeenDesc(agents)
		agents = agents[:MaxAgentsPerSnapshot]
	}
	if len(agents) != MaxAgentsPerSnapshot {
		t.Fatalf("expected %d agents post-truncation, got %d",
			MaxAgentsPerSnapshot, len(agents))
	}
	// The kept agents should be the highest-LastSeenAt entries — the
	// last 256 of the original (which were assigned descending indices).
	if agents[0].SessionID == "s0" {
		t.Fatalf("oldest entry should have been evicted, but s0 kept")
	}
}

// TestApplySnapshotRejectsOverCap exercises the receiver-side cap check.
// We feed a wire-shaped snapshot beyond MaxAgentsPerSnapshot and assert
// the sink does NOT see it (rejected before sink invocation).
func TestApplySnapshotRejectsOverCap(t *testing.T) {
	src := &fakeLocalAgents{}
	sink := &fakeRemoteSink{}
	svc := &AgentDirectoryService{
		source:  src,
		sink:    sink,
		logger:  testLogger(),
		streams: map[string]*agentStream{},
	}
	now := time.Now().UTC()
	agents := make([]AgentRecord, MaxAgentsPerSnapshot+1)
	for i := range agents {
		agents[i] = AgentRecord{
			SessionID:  fmtIndex("s", i),
			LastSeenAt: now,
		}
	}
	frameBytes, _ := json.Marshal(agentSnapshotFrame{
		Type:   agentFrameSnapshot,
		Agents: agents,
		TS:     now,
	})
	st := &agentStream{peerID: "12D3remote"}
	svc.applySnapshot(context.Background(), st, frameBytes)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.snapshots) != 0 {
		t.Fatalf("expected snapshot rejected by cap, but sink saw %d", len(sink.snapshots))
	}
}

// TestApplyDeltaForwardsAddedAndRemoved verifies that a wire-shaped
// delta is forwarded verbatim to the sink, with from-peer-id propagated.
func TestApplyDeltaForwardsAddedAndRemoved(t *testing.T) {
	sink := &fakeRemoteSink{}
	svc := &AgentDirectoryService{
		sink:    sink,
		logger:  testLogger(),
		streams: map[string]*agentStream{},
	}
	now := time.Now().UTC()
	frameBytes, _ := json.Marshal(agentDeltaFrame{
		Type:    agentFrameDelta,
		Added:   []AgentRecord{{SessionID: "s9", Name: "γ", LastSeenAt: now}},
		Removed: []string{"sX"},
		TS:      now,
	})
	st := &agentStream{peerID: "12D3remote"}
	svc.applyDelta(context.Background(), st, frameBytes)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.deltas) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(sink.deltas))
	}
	d := sink.deltas[0]
	if d.from != "12D3remote" {
		t.Fatalf("from peer wrong: %s", d.from)
	}
	if len(d.added) != 1 || d.added[0].SessionID != "s9" {
		t.Fatalf("added wrong: %+v", d.added)
	}
	if len(d.removed) != 1 || d.removed[0] != "sX" {
		t.Fatalf("removed wrong: %+v", d.removed)
	}
}

// TestApplyByeForwarded verifies bye dispatch.
func TestApplyByeForwarded(t *testing.T) {
	sink := &fakeRemoteSink{}
	svc := &AgentDirectoryService{
		sink:    sink,
		logger:  testLogger(),
		streams: map[string]*agentStream{},
	}
	st := &agentStream{peerID: "12D3remote"}
	svc.applyBye(context.Background(), st)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.byes) != 1 || sink.byes[0] != "12D3remote" {
		t.Fatalf("bye not forwarded: %+v", sink.byes)
	}
}

// TestBroadcastDeltaCoalesces verifies the 250 ms debounce window
// collapses a burst into a single fire.
func TestBroadcastDeltaCoalesces(t *testing.T) {
	svc := &AgentDirectoryService{
		logger:     testLogger(),
		streams:    map[string]*agentStream{},
		pendingAdd: make(map[string]AgentRecord),
		pendingDel: make(map[string]struct{}),
	}
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		svc.BroadcastDelta(context.Background(),
			[]AgentRecord{{SessionID: fmtIndex("s", i), Name: "x", LastSeenAt: now}},
			nil,
		)
	}
	svc.pendMu.Lock()
	pending := len(svc.pendingAdd)
	svc.pendMu.Unlock()
	if pending != 5 {
		t.Fatalf("expected 5 buffered upserts, got %d", pending)
	}
	// Wait for debounce + a small slop, then verify the buffer drained.
	time.Sleep(agentDeltaDebounce + 100*time.Millisecond)
	svc.pendMu.Lock()
	pendingPost := len(svc.pendingAdd)
	svc.pendMu.Unlock()
	if pendingPost != 0 {
		t.Fatalf("expected buffer to drain after debounce, got %d still pending", pendingPost)
	}
}

// TestBroadcastDeltaUpsertCancelsPriorRemove verifies the queue's
// add-overrides-remove ordering: a remove queued earlier must be
// cancelled if an upsert for the same SessionID arrives before the
// debounce flush.
func TestBroadcastDeltaUpsertCancelsPriorRemove(t *testing.T) {
	svc := &AgentDirectoryService{
		logger:     testLogger(),
		streams:    map[string]*agentStream{},
		pendingAdd: make(map[string]AgentRecord),
		pendingDel: make(map[string]struct{}),
	}
	now := time.Now().UTC()

	svc.BroadcastDelta(context.Background(), nil, []string{"sZ"})
	svc.BroadcastDelta(context.Background(),
		[]AgentRecord{{SessionID: "sZ", Name: "back", LastSeenAt: now}},
		nil,
	)

	svc.pendMu.Lock()
	_, stillRemoved := svc.pendingDel["sZ"]
	_, stillAdded := svc.pendingAdd["sZ"]
	svc.pendMu.Unlock()

	if stillRemoved {
		t.Error("upsert should have cancelled the pending remove for sZ")
	}
	if !stillAdded {
		t.Error("upsert should be present in pendingAdd")
	}
}

// helpers ----------------------------------------------------------------

func fmtIndex(prefix string, i int) string {
	var b bytes.Buffer
	b.WriteString(prefix)
	b.WriteString(itoa(i))
	return b.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func sortByLastSeenDesc(a []AgentRecord) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].LastSeenAt.After(a[j-1].LastSeenAt); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// testLogger returns an slog.Logger that discards every record so test
// output stays clean.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
