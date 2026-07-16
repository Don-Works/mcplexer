//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// fakeTaskSyncSource is the in-memory equivalent of the production
// wiring adapter — backs a per-workspace list of events that the
// server-side handler walks via ListTasksSinceHLC.
type fakeTaskSyncSource struct {
	mu        sync.Mutex
	workspace map[string][]TaskSyncEvent
}

func newFakeTaskSyncSource() *fakeTaskSyncSource {
	return &fakeTaskSyncSource{workspace: make(map[string][]TaskSyncEvent)}
}

// add appends/replaces an event for taskID in workspace. Replays the
// "this task changed; the new authoritative event is X" idea — gossip
// receivers must observe the latest event per task.
func (f *fakeTaskSyncSource) add(workspaceID string, evt TaskSyncEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// One event per task — replace by taskID to model "latest state".
	existing := f.workspace[workspaceID]
	for i := range existing {
		if existing[i].TaskID == evt.TaskID {
			existing[i] = evt
			f.workspace[workspaceID] = existing
			return
		}
	}
	f.workspace[workspaceID] = append(existing, evt)
}

// ListTasksSinceHLC returns events ordered by HLC ASC where HLC >
// sinceHLC, capped at limit.
func (f *fakeTaskSyncSource) ListTasksSinceHLC(
	_ context.Context, _ string, workspaceID, sinceHLC string, limit int,
) ([]TaskSyncEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.workspace[workspaceID]
	out := make([]TaskSyncEvent, 0, len(src))
	for _, e := range src {
		if e.HLC > sinceHLC {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HLC < out[j].HLC })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MaxHLCForWorkspace returns the highest stored HLC.
func (f *fakeTaskSyncSource) MaxHLCForWorkspace(
	_ context.Context, workspaceID string,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	max := ""
	for _, e := range f.workspace[workspaceID] {
		if e.HLC > max {
			max = e.HLC
		}
	}
	return max, nil
}

// fakeTaskSyncSink stores received events keyed by (taskID, hlc,
// peer) so the dedup contract can be asserted.
type fakeTaskSyncSink struct {
	mu       sync.Mutex
	received []TaskSyncEvent
	keys     map[string]bool
}

func newFakeTaskSyncSink() *fakeTaskSyncSink {
	return &fakeTaskSyncSink{keys: make(map[string]bool)}
}

func (f *fakeTaskSyncSink) ApplyRemoteEvent(_ context.Context, fromPeerID string, evt TaskSyncEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := evt.TaskID + "|" + evt.HLC + "|" + evt.ByPeer
	if f.keys[k] {
		return nil
	}
	f.keys[k] = true
	f.received = append(f.received, evt)
	return nil
}

func (f *fakeTaskSyncSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakeTaskSyncSink) seenTitles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.received))
	for _, e := range f.received {
		var patch struct {
			Title string `json:"title,omitempty"`
		}
		_ = json.Unmarshal(e.FieldPatchesJSON, &patch)
		out = append(out, patch.Title)
	}
	sort.Strings(out)
	return out
}

// fakeScopeChecker is an in-memory TaskSyncScopeChecker that lets a
// test grant + revoke task_sync scope on the fly.
type fakeScopeChecker struct {
	mu     sync.Mutex
	grants map[string]map[string]bool // peerID -> workspaceID -> allowed
}

func newFakeScopeChecker() *fakeScopeChecker {
	return &fakeScopeChecker{grants: make(map[string]map[string]bool)}
}

func (f *fakeScopeChecker) grant(peerID, workspaceID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.grants[peerID]
	if m == nil {
		m = make(map[string]bool)
		f.grants[peerID] = m
	}
	m[workspaceID] = true
}

func (f *fakeScopeChecker) HasTaskSyncScope(_ context.Context, peerID, workspaceID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.grants[peerID]; m != nil {
		return m[workspaceID], nil
	}
	return false, nil
}

// makeEvent builds a TaskSyncEvent with a synthesized field patch.
func makeEvent(taskID, workspaceID, hlc, title, byPeer string) TaskSyncEvent {
	patch, _ := json.Marshal(map[string]any{
		"title":  title,
		"status": "open",
	})
	return TaskSyncEvent{
		Type: taskSyncFrameEvent, TaskID: taskID, WorkspaceID: workspaceID,
		HLC: hlc, ByPeer: byPeer, FieldPatchesJSON: patch,
	}
}

// nextHLC returns a stamp that lexically follows the previous one.
// The integration tests don't need real wall-time accuracy — they need
// monotonicity and a way to skip ahead deterministically.
func nextHLC(prev string, i int) string {
	return fmt.Sprintf("%032x", uint64(time.Now().UnixMilli())+uint64(i))
}

// TestTaskSync_PartitionAndReconcile is the headline integration test:
// host A mutates a task while B is "disconnected" (just doesn't dial);
// once B does dial + send a Hello, it sees the mutation within the
// stream cycle.
func TestTaskSync_PartitionAndReconcile(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "task-sync-a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "task-sync-b")
	defer func() { _ = b.Close() }()

	lookA := newMeshLookup(b.PeerID())
	lookB := newMeshLookup(a.PeerID())
	connectHosts(t, ctx, b, a)

	const wsID = "ws-partition"
	srcA := newFakeTaskSyncSource()
	scopeA := newFakeScopeChecker()
	scopeA.grant(b.PeerID(), wsID) // B may pull from A's workspace
	NewTaskSyncService(a, lookA, srcA, nil, scopeA, nil, nil)

	sinkB := newFakeTaskSyncSink()
	syncB := NewTaskSyncService(b, lookB, nil, sinkB, nil, nil, nil)

	// A mutates task while B is partitioned (just hasn't dialled yet).
	srcA.add(wsID, makeEvent("task-001", wsID, "0000000000000001", "v1", a.PeerID()))
	srcA.add(wsID, makeEvent("task-002", wsID, "0000000000000002", "v1", a.PeerID()))

	// B catches up — Hello with empty since_hlc means "send everything".
	if err := syncB.ConnectToPeer(ctx, a.PeerID(), []TaskSyncHelloWorkspace{
		{WorkspaceID: wsID, SinceHLC: ""},
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := sinkB.count(); got != 2 {
		t.Fatalf("after first sync, B saw %d events, want 2", got)
	}

	// Now A mutates again — B re-dials with its watermark.
	srcA.add(wsID, makeEvent("task-001", wsID, "0000000000000003", "v2", a.PeerID()))
	if err := syncB.ConnectToPeer(ctx, a.PeerID(), []TaskSyncHelloWorkspace{
		{WorkspaceID: wsID, SinceHLC: "0000000000000002"},
	}); err != nil {
		t.Fatalf("connect 2: %v", err)
	}
	titles := sinkB.seenTitles()
	wantHas := []string{"v2"}
	for _, w := range wantHas {
		if !containsStr(titles, w) {
			t.Fatalf("after reconnect, missing %q in seen titles %v", w, titles)
		}
	}
}

// TestTaskSync_BurstOf100 — 100 tasks streamed from A to B in one
// gossip cycle. Validates the batching + paging logic + wire framing.
func TestTaskSync_BurstOf100(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := startTestHost(t, "burst-a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "burst-b")
	defer func() { _ = b.Close() }()
	lookA := newMeshLookup(b.PeerID())
	lookB := newMeshLookup(a.PeerID())
	connectHosts(t, ctx, b, a)

	const wsID = "ws-burst"
	srcA := newFakeTaskSyncSource()
	scopeA := newFakeScopeChecker()
	scopeA.grant(b.PeerID(), wsID)
	NewTaskSyncService(a, lookA, srcA, nil, scopeA, nil, nil)

	sinkB := newFakeTaskSyncSink()
	syncB := NewTaskSyncService(b, lookB, nil, sinkB, nil, nil, nil)

	for i := 0; i < 100; i++ {
		hlc := fmt.Sprintf("%032d", i+1)
		srcA.add(wsID, makeEvent(fmt.Sprintf("task-%03d", i), wsID, hlc,
			fmt.Sprintf("title-%03d", i), a.PeerID()))
	}

	deadline := time.Now().Add(5 * time.Second)
	if err := syncB.ConnectToPeer(ctx, a.PeerID(), []TaskSyncHelloWorkspace{
		{WorkspaceID: wsID, SinceHLC: ""},
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if time.Now().After(deadline) {
		t.Fatalf("burst sync exceeded 5s SLA")
	}
	if got := sinkB.count(); got != 100 {
		t.Fatalf("burst received %d events, want 100", got)
	}
}

// TestTaskSync_ScopeRevokedAbortsStream verifies the deny path: the
// peer doesn't hold task_sync scope for the requested workspace, so
// the server emits a workspace_denied error frame instead of any
// events. The receiver MUST observe the denial as a non-fatal
// per-workspace error (catalog-level) and not crash or corrupt state.
func TestTaskSync_ScopeRevokedAbortsStream(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "scope-a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "scope-b")
	defer func() { _ = b.Close() }()
	lookA := newMeshLookup(b.PeerID())
	lookB := newMeshLookup(a.PeerID())
	connectHosts(t, ctx, b, a)

	const wsAllowed = "ws-yes"
	const wsDenied = "ws-no"

	srcA := newFakeTaskSyncSource()
	srcA.add(wsAllowed, makeEvent("allowed-1", wsAllowed, "0000000000000001", "allowed", a.PeerID()))
	srcA.add(wsDenied, makeEvent("denied-1", wsDenied, "0000000000000001", "denied", a.PeerID()))

	scopeA := newFakeScopeChecker()
	scopeA.grant(b.PeerID(), wsAllowed) // only one workspace granted
	NewTaskSyncService(a, lookA, srcA, nil, scopeA, nil, nil)

	sinkB := newFakeTaskSyncSink()
	syncB := NewTaskSyncService(b, lookB, nil, sinkB, nil, nil, nil)

	// Hello asks for both — server replies one workspace_denied frame +
	// the allowed workspace's events + Bye. Per protocol contract this
	// is not a fatal error; ConnectToPeer returns nil.
	if err := syncB.ConnectToPeer(ctx, a.PeerID(), []TaskSyncHelloWorkspace{
		{WorkspaceID: wsAllowed, SinceHLC: ""},
		{WorkspaceID: wsDenied, SinceHLC: ""},
	}); err != nil {
		t.Fatalf("connect (expected per-workspace denial to be non-fatal): %v", err)
	}
	titles := sinkB.seenTitles()
	if !containsStr(titles, "allowed") {
		t.Fatalf("missing allowed event; got %v", titles)
	}
	if containsStr(titles, "denied") {
		t.Fatalf("denied workspace leaked through; titles %v", titles)
	}
}

// TestTaskSync_UnpairedPeerRejected pins the outer ACL: a peer that
// isn't in the pairing lookup must be refused at stream-open with the
// "denied" sentinel — no envelope details, no probe vector.
func TestTaskSync_UnpairedPeerRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "unpaired-a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "unpaired-b")
	defer func() { _ = b.Close() }()

	lookA := newMeshLookup() // A doesn't know B
	lookB := newMeshLookup(a.PeerID())
	connectHosts(t, ctx, b, a)

	srcA := newFakeTaskSyncSource()
	NewTaskSyncService(a, lookA, srcA, nil, nil, nil, nil)
	sinkB := newFakeTaskSyncSink()
	syncB := NewTaskSyncService(b, lookB, nil, sinkB, nil, nil, nil)

	err := syncB.ConnectToPeer(ctx, a.PeerID(), []TaskSyncHelloWorkspace{
		{WorkspaceID: "ws-x", SinceHLC: ""},
	})
	if err == nil {
		t.Fatalf("expected denial; got nil error")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected 'denied' in error, got: %v", err)
	}
	if sinkB.count() != 0 {
		t.Fatalf("unpaired stream leaked events: %d", sinkB.count())
	}
}

// TestTaskSync_HelloVersionMismatch verifies we reject a peer that
// claims a different proto version. The client reports the wire error;
// no events flow.
func TestTaskSync_HelloVersionMismatch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "ver-a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "ver-b")
	defer func() { _ = b.Close() }()
	lookA := newMeshLookup(b.PeerID())
	connectHosts(t, ctx, b, a)
	NewTaskSyncService(a, lookA, newFakeTaskSyncSource(), nil, newFakeScopeChecker(), nil, nil)

	// Manually open a stream + write a hello with a bogus proto version.
	pid, err := peer.Decode(a.PeerID())
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}
	stream, err := b.Inner().NewStream(ctx, pid, TaskSyncProtocol)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	bogus := TaskSyncHello{
		Type: taskSyncFrameHello, PeerID: b.PeerID(),
		ProtoVersion: 999, // unknown
		Workspaces:   []TaskSyncHelloWorkspace{{WorkspaceID: "ws", SinceHLC: ""}},
		TS:           time.Now().UTC(),
	}
	raw, _ := json.Marshal(&bogus)
	raw = append(raw, '\n')
	if _, err := stream.Write(raw); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	// Read response — should be a bad_hello error frame. Server writes
	// then closes, so we may get the frame followed by EOF on Read.
	buf := make([]byte, 1024)
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := stream.Read(buf)
	if n == 0 {
		t.Fatalf("expected error frame, got empty")
	}
	if !strings.Contains(string(buf[:n]), "bad_hello") {
		t.Fatalf("expected bad_hello frame; got %q", string(buf[:n]))
	}
}

// TestTaskSync_RejectsUnboundWorkspaceEvent is the P0 regression: a
// MALICIOUS serving peer (one that doesn't run our server code, so the
// per-workspace scope gate never fires) streams a TaskSyncEvent for a
// workspace the client never named in its Hello. The client MUST drop
// that event without applying it — otherwise a hostile peer can inject
// task rows into arbitrary local workspaces it was never granted. A
// legitimate event for the requested workspace, sent on the same
// stream, must still apply.
func TestTaskSync_RejectsUnboundWorkspaceEvent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "unbound-a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "unbound-b")
	defer func() { _ = b.Close() }()

	lookB := newMeshLookup(a.PeerID())
	connectHosts(t, ctx, b, a)

	const wsLegit = "ws-legit"
	const wsEvil = "ws-evil"

	// Hand-rolled hostile server: reads the hello, then streams an event
	// for wsEvil (NOT requested) followed by a legit event for wsLegit,
	// then Bye. It deliberately ignores scope + the client's workspace
	// set — exactly what a malicious peer would do.
	a.Inner().SetStreamHandler(TaskSyncProtocol, func(stream network.Stream) {
		defer func() { _ = stream.Close() }()
		br := bufio.NewReaderSize(stream, MaxTaskSyncFrameBytes)
		_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := br.ReadBytes('\n'); err != nil { // consume hello
			return
		}
		writeRaw := func(v any) bool {
			raw, _ := json.Marshal(v)
			raw = append(raw, '\n')
			_ = stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := stream.Write(raw)
			return err == nil
		}
		if !writeRaw(makeEvent("evil-1", wsEvil, "0000000000000001", "evil", a.PeerID())) {
			return
		}
		if !writeRaw(makeEvent("legit-1", wsLegit, "0000000000000002", "legit", a.PeerID())) {
			return
		}
		writeRaw(TaskSyncBye{Type: taskSyncFrameBye, ServerHLC: "0000000000000002", TS: time.Now().UTC()})
	})

	sinkB := newFakeTaskSyncSink()
	syncB := NewTaskSyncService(b, lookB, nil, sinkB, nil, nil, nil)

	if err := syncB.ConnectToPeer(ctx, a.PeerID(), []TaskSyncHelloWorkspace{
		{WorkspaceID: wsLegit, SinceHLC: ""},
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	titles := sinkB.seenTitles()
	if containsStr(titles, "evil") {
		t.Fatalf("unbound-workspace event leaked into sink; titles=%v", titles)
	}
	if !containsStr(titles, "legit") {
		t.Fatalf("legit (bound) event was dropped; titles=%v", titles)
	}
	if got := sinkB.count(); got != 1 {
		t.Fatalf("sink applied %d events, want exactly 1 (legit only)", got)
	}
}

// containsStr returns true iff needle appears in haystack.
func containsStr(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// _ silences any unused-var lint if the burst HLC helper ends up
// inlined.
var _ = nextHLC
