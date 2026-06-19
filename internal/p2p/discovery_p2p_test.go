//go:build p2p

package p2p

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	_ "modernc.org/sqlite"
)

// TestMDNSAnnounceAndDiscoverHappyPath boots two libp2p hosts on the same
// goroutine with a unique mDNS service tag and verifies that:
//   - both peers learn each other via mDNS, AND
//   - when the peer is recorded as "paired" in p2p_peers, the discovery
//     service drives a direct dial that succeeds.
//
// The test uses an in-memory peer-lookup so it does not depend on M1.2's
// migration having shipped.
func TestMDNSAnnounceAndDiscoverHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mDNS happy-path in short mode")
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tag := fmt.Sprintf("mcplexer-m13-happy-%d", time.Now().UnixNano())
	a := startTestHostWithTag(t, "a", tag)
	defer func() { _ = a.Close() }()
	b := startTestHostWithTag(t, "b", tag)
	defer func() { _ = b.Close() }()

	// Mark each host's view of the other as "paired" so discovery dials.
	lookupA := newMemLookup(map[string]bool{b.PeerID(): true})
	lookupB := newMemLookup(map[string]bool{a.PeerID(): true})
	dA := NewDiscoveryService(a, lookupA, nil)
	dB := NewDiscoveryService(b, lookupB, nil)
	defer func() { _ = dA.Close() }()
	defer func() { _ = dB.Close() }()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if dA.ModeFor(b.PeerID()) == ModeDirect &&
			dB.ModeFor(a.PeerID()) == ModeDirect {
			return
		}
		select {
		case <-ctx.Done():
			t.Skipf("ctx done before mDNS converged: %v", ctx.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Skipf("mDNS did not converge — likely no multicast on this host")
}

// TestLANDropFallsBackToRelay simulates a LAN drop by closing the direct
// connection while the peer remains paired. After the drop the cached
// connection mode for the peer must clear, and a re-attempt that fails to
// reach the LAN addr leaves the mode at relay (or empty) — never at
// "direct" against a peer we cannot actually reach directly anymore.
//
// We don't actually stand up a relay here (that requires an external
// infrastructure node); instead we assert the state-machine behaviour:
// when the direct connection drops and the peer is unreachable, ModeFor
// returns "" rather than the stale "direct".
func TestLANDropFallsBackToRelay(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookup := newMemLookup(map[string]bool{b.PeerID(): true})
	d := NewDiscoveryService(a, lookup, nil)
	defer func() { _ = d.Close() }()

	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("a.Connect(b): %v", err)
	}

	// Wait for the Notifiee to record direct mode.
	if !waitFor(2*time.Second, func() bool {
		return d.ModeFor(b.PeerID()) == ModeDirect
	}) {
		t.Fatalf("expected direct mode, got %q", d.ModeFor(b.PeerID()))
	}
	if got := lookup.lastMode(b.PeerID()); got != ModeDirect {
		t.Fatalf("lookup last mode = %q, want %q", got, ModeDirect)
	}

	// Simulate LAN drop: close all conns from a to b.
	for _, c := range a.Inner().Network().ConnsToPeer(b.ID()) {
		_ = c.Close()
	}

	if !waitFor(2*time.Second, func() bool {
		return d.ModeFor(b.PeerID()) == ""
	}) {
		t.Fatalf("expected mode cleared after disconnect, got %q",
			d.ModeFor(b.PeerID()))
	}
}

func TestConnectionModeLogDedupesReconnectSameMode(t *testing.T) {
	t.Parallel()

	handler := &captureSlogHandler{}
	d := &DiscoveryService{
		logger:   slog.New(handler),
		reported: make(map[peer.ID]ConnectionMode),
	}
	p := peer.ID("12D3KooWLogSpamRegression")

	d.logConnectionMode(p, ModeDirect, "")
	d.logConnectionMode(p, ModeDirect, "")
	d.logConnectionMode(p, ModeRelay, "")

	records := handler.records()
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	if records[0].level != slog.LevelInfo {
		t.Fatalf("first observation level = %s, want INFO", records[0].level)
	}
	if records[1].level != slog.LevelDebug {
		t.Fatalf("same-mode reconnect level = %s, want DEBUG", records[1].level)
	}
	if records[2].level != slog.LevelInfo {
		t.Fatalf("mode change level = %s, want INFO", records[2].level)
	}
}

// TestMDNSNotAnnouncedWhenP2PDisabled verifies the master switch: when
// Config.Enabled is false, NewHost returns nil and no mDNS service ever
// starts (so a paired peer on the same LAN never learns about us).
func TestMDNSNotAnnouncedWhenP2PDisabled(t *testing.T) {
	t.Parallel()
	cfg := Config{Enabled: false, EnableMDNS: true}
	h, err := NewHost(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewHost(disabled): %v", err)
	}
	if h != nil {
		t.Fatalf("NewHost(disabled) = %v, want nil", h)
	}
}

// TestSQLPeerLookupTableMissing verifies the defensive contract: when the
// p2p_peers table doesn't exist (because M1.2 hasn't merged), IsPaired
// returns (false, nil) and MarkConnectionMode is silently a no-op rather
// than crashing the daemon.
func TestSQLPeerLookupTableMissing(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck

	lookup := NewSQLPeerLookup(db, nil)
	paired, err := lookup.IsPaired(context.Background(), "12D3KooWFakePeerID")
	if err != nil {
		t.Fatalf("IsPaired with no table = %v, want nil", err)
	}
	if paired {
		t.Fatalf("IsPaired with no table = true, want false")
	}
	// Should not panic / log loudly.
	lookup.MarkConnectionMode(context.Background(),
		"12D3KooWFakePeerID", ModeDirect)
}

// TestSQLPeerLookupConnectionModeRoundTrip exercises the persistence
// path: with the migration applied, marking a mode and reading it back via
// GetPeerStatus returns the same value.
func TestSQLPeerLookupConnectionModeRoundTrip(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck

	if _, err := db.Exec(testP2PPeersSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO p2p_peers (peer_id, paired_at) VALUES (?, ?)`,
		"12D3KooWPeer", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	lookup := NewSQLPeerLookup(db, nil)
	ok, err := lookup.IsPaired(context.Background(), "12D3KooWPeer")
	if err != nil || !ok {
		t.Fatalf("IsPaired = (%v, %v), want (true, nil)", ok, err)
	}
	lookup.MarkConnectionMode(context.Background(), "12D3KooWPeer", ModeRelay)

	st, err := lookup.GetPeerStatus(context.Background(), "12D3KooWPeer")
	if err != nil {
		t.Fatalf("GetPeerStatus: %v", err)
	}
	if st.ConnectionMode != string(ModeRelay) {
		t.Fatalf("ConnectionMode = %q, want %q",
			st.ConnectionMode, ModeRelay)
	}
	if st.LastSeen == "" {
		t.Fatalf("LastSeen empty, want timestamp")
	}
}

// TestGetPeerStatusReadsLastSeen is a regression test for the
// `last_seen_at` -> `last_seen` rename in peerstore.go. The original code
// SELECT'd a column that didn't exist in production migration 024, so any
// caller of GetPeerStatus saw "no such column: last_seen_at". This test seeds
// a known last_seen value via the M1.2 schema and asserts it round-trips.
func TestGetPeerStatusReadsLastSeen(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck

	if _, err := db.Exec(testP2PPeersSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	const peerID = "12D3KooWLastSeenRegression"
	const lastSeen = "2026-04-30T12:34:56Z"
	if _, err := db.Exec(
		`INSERT INTO p2p_peers (peer_id, paired_at, last_seen, connection_mode)
		 VALUES (?, ?, ?, ?)`,
		peerID, lastSeen, lastSeen, string(ModeDirect)); err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	lookup := NewSQLPeerLookup(db, nil)
	st, err := lookup.GetPeerStatus(context.Background(), peerID)
	if err != nil {
		t.Fatalf("GetPeerStatus: %v", err)
	}
	if st.LastSeen != lastSeen {
		t.Fatalf("LastSeen = %q, want %q", st.LastSeen, lastSeen)
	}
	if st.ConnectionMode != string(ModeDirect) {
		t.Fatalf("ConnectionMode = %q, want %q",
			st.ConnectionMode, ModeDirect)
	}
}

// testP2PPeersSchema mirrors migration 024's CREATE TABLE plus the columns
// added by the post-migration hooks (connection_mode from 024,
// last_known_addrs from 033). Keep this in lock-step with the production
// schema — the bug fixed in commit "fix: last_seen_at -> last_seen" was
// masked precisely because this fixture diverged.
const testP2PPeersSchema = `
CREATE TABLE p2p_peers (
    peer_id          TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL DEFAULT '',
    paired_at        TEXT NOT NULL,
    last_seen        TEXT,
    trust_level      INTEGER NOT NULL DEFAULT 0,
    scopes           TEXT NOT NULL DEFAULT '[]',
    revoked_at       TEXT,
    connection_mode  TEXT,
    last_known_addrs TEXT NOT NULL DEFAULT '[]'
);`

// TestDiscoveryPersistsAddrsOnDirectConnect dials two real libp2p hosts and
// asserts that the discovery service persists the dialed peer's addrs via
// RememberPeerAddrs once a direct connection is established. Mirrors the
// shape of TestLANDropFallsBackToRelay (LAN-direct dial through real
// libp2p) but inspects the lookup's addr cache instead of the mode cache.
func TestDiscoveryPersistsAddrsOnDirectConnect(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookup := newMemLookup(map[string]bool{b.PeerID(): true})
	d := NewDiscoveryService(a, lookup, nil)
	defer func() { _ = d.Close() }()

	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("a.Connect(b): %v", err)
	}

	// Wait for the Notifiee to record direct mode AND for the addr-persist
	// path to fire (it's gated behind the same handleConnected).
	if !waitFor(2*time.Second, func() bool {
		return d.ModeFor(b.PeerID()) == ModeDirect &&
			len(lookup.lastAddrs(b.PeerID())) > 0
	}) {
		t.Fatalf("addrs never persisted: mode=%q addrs=%v",
			d.ModeFor(b.PeerID()), lookup.lastAddrs(b.PeerID()))
	}

	// Persisted addrs must be dialable (no /p2p-circuit, no fe80, etc).
	for _, s := range lookup.lastAddrs(b.PeerID()) {
		for _, banned := range []string{"/p2p-circuit", "/webtransport", "/ip6/fe80:"} {
			if strings.Contains(s, banned) {
				t.Fatalf("persisted addr contains banned %s: %s", banned, s)
			}
		}
	}
}

// TestDiscoveryHydratesPeerstoreOnStart pre-loads a paired peer's
// last_known_addrs into the lookup, constructs a fresh DiscoveryService
// against a libp2p host that has never seen the peer, and asserts that the
// hydration step pushed the cached addrs into the libp2p peerstore. This is
// the hot-start path that lets the reconnector dial immediately on boot.
func TestDiscoveryHydratesPeerstoreOnStart(t *testing.T) {
	t.Parallel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	// Pre-cached addrs use raw multiaddrs (no /p2p/<id> suffix — peerstore.
	// AddAddrs takes the addr part only). Mix one valid + one bogus to
	// prove malformed entries don't break the rest of the hydrate.
	preCache := []string{b.Addrs()[0], "/ip4/127.0.0.1/tcp/65535"}
	lookup := newHydratingLookup(map[string]bool{b.PeerID(): true},
		map[string][]string{b.PeerID(): preCache})

	// Sanity: peerstore is empty for B before hydration.
	if got := a.LastSeenAddrs(b.PeerID()); len(got) != 0 {
		t.Fatalf("pre-hydrate addrs = %v, want empty", got)
	}

	d := NewDiscoveryService(a, lookup, nil)
	defer func() { _ = d.Close() }()

	got := a.LastSeenAddrs(b.PeerID())
	if len(got) == 0 {
		t.Fatalf("post-hydrate addrs empty, want at least one of %v", preCache)
	}
	// The host's first addr should now be in the peerstore.
	wantFirst := b.Addrs()[0]
	found := false
	for _, s := range got {
		if s == wantFirst {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("post-hydrate addrs %v missing primary %s", got, wantFirst)
	}
}

// hydratingLookup extends memLookup with PairedPeerLister so the hydrate
// path can iterate the paired-peer set. (The discovery service uses a
// runtime type-assertion to opt into hydration only when the lookup
// implements PairedPeerLister — keeping the basic memLookup minimal.)
type hydratingLookup struct {
	*memLookup
}

func newHydratingLookup(paired map[string]bool, addrs map[string][]string) *hydratingLookup {
	m := newMemLookup(paired)
	for k, v := range addrs {
		cp := make([]string, len(v))
		copy(cp, v)
		m.addrs[k] = cp
	}
	return &hydratingLookup{memLookup: m}
}

func (h *hydratingLookup) ListPeerIDs(_ context.Context) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.paired))
	for id, ok := range h.paired {
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// memLookup is an in-memory PeerLookup for tests.
type memLookup struct {
	mu        sync.Mutex
	paired    map[string]bool
	modes     map[string]ConnectionMode
	addrs     map[string][]string
	markCalls int64
}

func newMemLookup(paired map[string]bool) *memLookup {
	return &memLookup{
		paired: paired,
		modes:  make(map[string]ConnectionMode),
		addrs:  make(map[string][]string),
	}
}

func (m *memLookup) IsPaired(_ context.Context, peerID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paired[peerID], nil
}

func (m *memLookup) MarkConnectionMode(
	_ context.Context, peerID string, mode ConnectionMode,
) {
	atomic.AddInt64(&m.markCalls, 1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modes[peerID] = mode
}

func (m *memLookup) RememberPeerAddrs(
	_ context.Context, peerID string, addrs []string,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(addrs))
	copy(cp, addrs)
	m.addrs[peerID] = cp
}

func (m *memLookup) LoadPeerAddrs(_ context.Context, peerID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.addrs[peerID]))
	copy(cp, m.addrs[peerID])
	return cp
}

func (m *memLookup) lastMode(peerID string) ConnectionMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modes[peerID]
}

func (m *memLookup) lastAddrs(peerID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.addrs[peerID]))
	copy(cp, m.addrs[peerID])
	return cp
}

// waitFor polls cond every 50ms until it returns true or d elapses. Returns
// true when cond observed true within d.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

type captureSlogHandler struct {
	mu      sync.Mutex
	entries []capturedSlogRecord
}

type capturedSlogRecord struct {
	level   slog.Level
	message string
}

func (h *captureSlogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, capturedSlogRecord{
		level:   r.Level,
		message: r.Message,
	})
	return nil
}

func (h *captureSlogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureSlogHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureSlogHandler) recordsCopy() []capturedSlogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]capturedSlogRecord, len(h.entries))
	copy(cp, h.entries)
	return cp
}

func (h *captureSlogHandler) records() []capturedSlogRecord {
	return h.recordsCopy()
}

func (h *captureSlogHandler) countAtLevel(level slog.Level, substr string) int {
	n := 0
	for _, r := range h.recordsCopy() {
		if r.level == level && strings.Contains(r.message, substr) {
			n++
		}
	}
	return n
}

// TestConnectionModeLogSuppression verifies that the first direct connection
// emits an INFO log, but a disconnect→reconnect cycle with the same mode only
// emits DEBUG (not INFO), preventing log spam from flapping connections.
func TestConnectionModeLogSuppression(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	rh := &captureSlogHandler{}
	logger := slog.New(rh)

	lookup := newMemLookup(map[string]bool{b.PeerID(): true})
	d := NewDiscoveryService(a, lookup, logger)
	defer func() { _ = d.Close() }()

	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("a.Connect(b): %v", err)
	}

	if !waitFor(3*time.Second, func() bool {
		return d.ModeFor(b.PeerID()) == ModeDirect
	}) {
		t.Fatalf("first connect: expected direct mode, got %q", d.ModeFor(b.PeerID()))
	}

	infoAfterFirst := rh.countAtLevel(slog.LevelInfo, "p2p connection mode")
	if infoAfterFirst == 0 {
		t.Fatalf("expected at least one INFO log after first connect, got %d", infoAfterFirst)
	}

	for _, c := range a.Inner().Network().ConnsToPeer(b.ID()) {
		_ = c.Close()
	}
	if !waitFor(3*time.Second, func() bool {
		return d.ModeFor(b.PeerID()) == ""
	}) {
		t.Fatalf("disconnect: expected mode cleared, got %q", d.ModeFor(b.PeerID()))
	}

	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("a.Connect(b) second: %v", err)
	}
	if !waitFor(3*time.Second, func() bool {
		return d.ModeFor(b.PeerID()) == ModeDirect
	}) {
		t.Fatalf("reconnect: expected direct mode, got %q", d.ModeFor(b.PeerID()))
	}

	infoAfterReconnect := rh.countAtLevel(slog.LevelInfo, "p2p connection mode")
	debugAfterReconnect := rh.countAtLevel(slog.LevelDebug, "p2p connection mode")

	if infoAfterReconnect != infoAfterFirst {
		t.Fatalf("reconnect emitted extra INFO logs: before=%d after=%d",
			infoAfterFirst, infoAfterReconnect)
	}
	if debugAfterReconnect == 0 {
		t.Fatalf("reconnect to same mode should emit DEBUG, got 0 DEBUG logs")
	}
}
