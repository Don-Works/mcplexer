//go:build p2p

package p2p

import (
	"context"
	"errors"
	"sync"
)

// fakePairedLookup implements PairedPeerLookup with an in-memory map. Lets
// each test wire its own paired-or-not + scopes scenario without needing a
// sqlite database.
type fakePairedLookup struct {
	mu    sync.Mutex
	peers map[string]PairedPeer
}

func newFakeLookup() *fakePairedLookup {
	return &fakePairedLookup{peers: make(map[string]PairedPeer)}
}

func (f *fakePairedLookup) addPaired(peerID string, scopes []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers[peerID] = PairedPeer{
		PeerID: peerID, Scopes: scopes, Revoked: false,
	}
}

func (f *fakePairedLookup) GetPairedPeer(_ context.Context, peerID string) (PairedPeer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.peers[peerID]
	if !ok {
		return PairedPeer{}, errors.New("not paired")
	}
	return p, nil
}

// fakeProvider is the offering-side hook with a single skill in memory.
type fakeProvider struct {
	skillName string
	version   string
	bundle    []byte
	sig       []byte
}

func (f *fakeProvider) GetInstalledOffer(_ context.Context, name string) (*SkillOffer, error) {
	if name != f.skillName {
		return nil, ErrSkillNotInstalled
	}
	return &SkillOffer{
		Name: f.skillName, Version: f.version,
		ManifestJSON: []byte(`{"name":"` + f.skillName + `"}`),
		SizeBytes:    int64(len(f.bundle)),
	}, nil
}

func (f *fakeProvider) GetSkillBundle(_ context.Context, name, _ string) ([]byte, []byte, error) {
	if name != f.skillName {
		return nil, nil, ErrSkillNotInstalled
	}
	return f.bundle, f.sig, nil
}

// fakeReceiver records the bytes it was handed and lets the test pretend
// "install succeeded" or "install declined".
type fakeReceiver struct {
	mu      sync.Mutex
	gotBund []byte
	gotSig  []byte
	err     error
}

func (f *fakeReceiver) HandleIncomingBundle(_ context.Context, _ string, bundle, sig []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotBund = append([]byte(nil), bundle...)
	f.gotSig = append([]byte(nil), sig...)
	return f.err
}

// fakeAuditor counts events keyed by action+status so tests can assert the
// right audit rows would be written.
type fakeAuditor struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeAuditor) RecordSkillShare(
	_ context.Context, action, _, _, status, _ string,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, action+":"+status)
}

func (f *fakeAuditor) seen(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e == want {
			return true
		}
	}
	return false
}

// pairHosts wires both directions of the pairing record so each host
// considers the other a trusted, scope-granted peer.
func pairHosts(a, b *Host, lookA, lookB *fakePairedLookup) {
	lookA.addPaired(b.PeerID(), []string{skillShareScopeName})
	lookB.addPaired(a.PeerID(), []string{skillShareScopeName})
}
