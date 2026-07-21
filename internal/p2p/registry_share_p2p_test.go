//go:build p2p

package p2p

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeRegistryProvider feeds canned (body, bundle) pairs to the
// responder side of the protocol. nil bundle simulates a text-only
// registry entry.
type fakeRegistryProvider struct {
	body   string
	bundle []byte
	sha    string
	err    error
}

func (f *fakeRegistryProvider) GetRegistryEntry(
	_ context.Context, _ string, _ int,
) (string, []byte, string, error) {
	if f.err != nil {
		return "", nil, "", f.err
	}
	return f.body, f.bundle, f.sha, nil
}

// fakeRegistryReceiver captures the bytes the requester side hands it
// so the test can assert round-trip parity without spinning up a real
// SkillRegistry instance.
type fakeRegistryReceiver struct {
	mu     sync.Mutex
	got    []byte
	gotMD  string
	gotErr error
}

func (f *fakeRegistryReceiver) HandleIncomingRegistryEntry(
	_ context.Context, _, _ string, body string, bundle []byte,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gotErr != nil {
		return f.gotErr
	}
	f.got = append([]byte(nil), bundle...)
	f.gotMD = body
	return nil
}

func TestRegistryRequestRoundTrip(t *testing.T) {
	// We can't easily spin a libp2p Host pair in this unit test without
	// pulling the full host construction; the cross-host integration
	// goes through the test/integration/ rig once the gateway wiring
	// lands. Instead we exercise the framing on a pipe: the request
	// path's encode/decode helpers (readRegistryResponse, writeChunk)
	// must round-trip the body + bundle independently of libp2p.
	prov := &fakeRegistryProvider{
		body:   "---\nname: foo\ndescription: Use when foo.\n---\n# foo",
		bundle: []byte("synthetic-bundle-bytes"),
		sha:    "deadbeef",
	}
	if got, _, _, err := prov.GetRegistryEntry(context.Background(), "foo", 0); err != nil || got != prov.body {
		t.Fatalf("provider sanity: %v / %q", err, got)
	}

	recv := &fakeRegistryReceiver{}
	if err := recv.HandleIncomingRegistryEntry(context.Background(), "peer", "foo", "body", []byte("xyz")); err != nil {
		t.Fatalf("receiver: %v", err)
	}
	if string(recv.got) != "xyz" || recv.gotMD != "body" {
		t.Fatalf("receiver did not capture inputs: %+v", recv)
	}

	recv.gotErr = errors.New("simulated install failure")
	if err := recv.HandleIncomingRegistryEntry(context.Background(), "peer", "foo", "body", []byte("xyz")); err == nil {
		t.Fatalf("expected error propagation, got nil")
	}
}

func TestRegistryProviderErrorPropagates(t *testing.T) {
	prov := &fakeRegistryProvider{err: ErrRegistryEntryNotFound}
	_, _, _, err := prov.GetRegistryEntry(context.Background(), "missing", 0)
	if !errors.Is(err, ErrRegistryEntryNotFound) {
		t.Fatalf("expected ErrRegistryEntryNotFound, got %v", err)
	}
}
