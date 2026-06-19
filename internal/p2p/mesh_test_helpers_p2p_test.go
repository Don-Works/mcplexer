//go:build p2p

package p2p

// Test helpers shared across mesh_p2p tests. Keeps the main test file under
// the 300-line cap.

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// meshMemLookup is an in-memory MeshPeerLookup for tests.
type meshMemLookup struct {
	mu      sync.Mutex
	peers   map[string]struct{}
	failure error
}

func newMeshLookup(ids ...string) *meshMemLookup {
	m := &meshMemLookup{peers: make(map[string]struct{})}
	for _, id := range ids {
		m.peers[id] = struct{}{}
	}
	return m
}

func (m *meshMemLookup) IsPaired(_ context.Context, peerID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure != nil {
		return false, m.failure
	}
	_, ok := m.peers[peerID]
	return ok, nil
}

func (m *meshMemLookup) ListPeerIDs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure != nil {
		return nil, m.failure
	}
	out := make([]string, 0, len(m.peers))
	for id := range m.peers {
		out = append(out, id)
	}
	return out, nil
}

// memAuditor collects audit calls for assertions.
type memAuditor struct {
	mu      sync.Mutex
	records []auditRecord
}

type auditRecord struct {
	Kind, PeerID, Reason, EnvID string
}

func (a *memAuditor) Record(_ context.Context, kind, peerID, reason, envID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, auditRecord{kind, peerID, reason, envID})
}

func (a *memAuditor) reasons() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.records))
	for i, r := range a.records {
		out[i] = r.Reason
	}
	return out
}

func newULID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

// connectHosts dials b from a so subsequent NewStream calls succeed.
func connectHosts(t *testing.T, ctx context.Context, a, b *Host) {
	t.Helper()
	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
