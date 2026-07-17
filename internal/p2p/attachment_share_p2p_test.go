//go:build p2p

package p2p

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeAttachmentProvider is the offering-side hook with a single
// attachment in memory. Returns ErrAttachmentNotFound on id mismatch.
type fakeAttachmentProvider struct {
	id          string
	taskID      string
	workspaceID string
	filename    string
	mimeType    string
	body        []byte
	sha256      string
}

func (f *fakeAttachmentProvider) GetAttachmentPayload(
	_ context.Context, id, _ string,
) (*AttachmentPayload, error) {
	if id != f.id {
		return nil, ErrAttachmentNotFound
	}
	return &AttachmentPayload{
		Type:          "attachment",
		ID:            f.id,
		TaskID:        f.taskID,
		WorkspaceID:   f.workspaceID,
		Filename:      f.filename,
		MimeType:      f.mimeType,
		SizeBytes:     int64(len(f.body)),
		Sha256:        f.sha256,
		ContentBase64: base64.StdEncoding.EncodeToString(f.body),
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// fakeAttachmentAuditor records action+status pairs so tests can assert
// the right audit rows would have been emitted.
type fakeAttachmentAuditor struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeAttachmentAuditor) RecordAttachmentShare(
	_ context.Context, action, _, _, status, _ string,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, action+":"+status)
}

func (f *fakeAttachmentAuditor) seen(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e == want {
			return true
		}
	}
	return false
}

// pairHostsForAttachment wires both directions of the pairing record so
// each host considers the other a trusted, attachment-scope-granted
// peer. Mirror of pairHosts but for the mesh.attachment_request scope.
func pairHostsForAttachment(a, b *Host, lookA, lookB *fakePairedLookup) {
	lookA.addPaired(b.PeerID(), []string{attachmentShareScopeName})
	lookB.addPaired(a.PeerID(), []string{attachmentShareScopeName})
}

// TestAttachmentShare_HappyPath is the acceptance test: host A serves an
// attachment, host B requests it by id, the bytes round-trip across the
// /mcplexer/attachment/1.0.0 protocol intact. Mirrors the e2e shape of
// TestSkillShare_OfferRequestInstall and TestMemoryShare_RequestRoundTrip.
func TestAttachmentShare_HappyPath(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairHostsForAttachment(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	body := []byte("payload bytes for cross-peer attachment fetch test\n")
	provA := &fakeAttachmentProvider{
		id:          "01HZZZZZZZZZZZZZZZZZZZZZZA",
		taskID:      "01HZZZZZZZZZZZZZZZZZZZZZZT",
		workspaceID: "ws-1",
		filename:    "report.txt",
		mimeType:    "text/plain",
		body:        body,
		sha256:      "deadbeef",
	}
	auditA, auditB := &fakeAttachmentAuditor{}, &fakeAttachmentAuditor{}

	NewAttachmentShareService(a, lookA, provA, auditA, nil)
	bSvc := NewAttachmentShareService(b, lookB, nil, auditB, nil)

	got, err := bSvc.RequestAttachment(ctx, a.PeerID(), provA.id)
	if err != nil {
		t.Fatalf("RequestAttachment: %v", err)
	}
	if got == nil {
		t.Fatal("nil payload")
	}
	if got.ID != provA.id || got.TaskID != provA.taskID || got.WorkspaceID != provA.workspaceID {
		t.Errorf("metadata mismatch: %+v", got)
	}
	if got.Filename != provA.filename || got.MimeType != provA.mimeType {
		t.Errorf("filename/mime mismatch: %+v", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(got.ContentBase64)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if string(decoded) != string(body) {
		t.Fatalf("body mismatch:\nwant=%q\n got=%q", body, decoded)
	}
	if !auditB.seen("request:ok") {
		t.Errorf("expected request:ok on requesting side, got %v", auditB.events)
	}
	if !auditA.seen("request_served:ok") {
		t.Errorf("expected request_served:ok on offering side, got %v", auditA.events)
	}
}

// TestAttachmentShare_RejectsMissingScope drives the scope-gate: the
// peer is paired but DOES NOT carry mesh.attachment_request. The server
// must respond with ErrAttachmentShareDenied (mapped through the typed
// sentinel) so the agent can branch on the cause.
func TestAttachmentShare_RejectsMissingScope(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	// Pair without the attachment scope — both sides see the other as
	// known but neither holds the gating scope.
	lookA.addPaired(b.PeerID(), []string{})
	lookB.addPaired(a.PeerID(), []string{})
	connectHosts(t, context.Background(), a, b)

	provA := &fakeAttachmentProvider{
		id:   "01HZZZZZZZZZZZZZZZZZZZZZZA",
		body: []byte("x"),
	}
	NewAttachmentShareService(a, lookA, provA, nil, nil)
	bSvc := NewAttachmentShareService(b, lookB, nil, nil, nil)

	_, err := bSvc.RequestAttachment(ctx, a.PeerID(), provA.id)
	if err == nil {
		t.Fatal("expected denied error, got nil")
	}
	if !errors.Is(err, ErrAttachmentShareDenied) {
		t.Errorf("expected ErrAttachmentShareDenied, got %v", err)
	}
}

// TestAttachmentShare_RejectsUnknownID asserts the not-found path is
// distinguishable from the denied path on the wire — agents need both
// shapes so they can fall back to a re-request or surface a clear UX
// error.
func TestAttachmentShare_RejectsUnknownID(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairHostsForAttachment(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	provA := &fakeAttachmentProvider{
		id:   "01HZZZZZZZZZZZZZZZZZZZZZZA",
		body: []byte("x"),
	}
	NewAttachmentShareService(a, lookA, provA, nil, nil)
	bSvc := NewAttachmentShareService(b, lookB, nil, nil, nil)

	_, err := bSvc.RequestAttachment(ctx, a.PeerID(), "does-not-exist")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	if !errors.Is(err, ErrAttachmentNotFound) {
		t.Errorf("expected ErrAttachmentNotFound, got %v", err)
	}
}

// TestAttachmentShare_RejectsRevokedPeer asserts a peer that was once
// paired but has since been revoked is denied — the revoke flag wins
// over scope membership. Same guarantee skill/memory share offers.
func TestAttachmentShare_RejectsRevokedPeer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	// Wire B's view of A as revoked (so B's outbound dial fails on the
	// assertPeerPaired check). For the server-side revoked check we'd
	// flip lookA but the outbound assertion fires first; covering both
	// paths is overkill for one regression test.
	lookA.peers[b.PeerID()] = PairedPeer{PeerID: b.PeerID(), Revoked: true}
	lookB.peers[a.PeerID()] = PairedPeer{
		PeerID: a.PeerID(), Scopes: []string{attachmentShareScopeName}, Revoked: true,
	}
	connectHosts(t, context.Background(), a, b)

	provA := &fakeAttachmentProvider{id: "id-x", body: []byte("x")}
	NewAttachmentShareService(a, lookA, provA, nil, nil)
	bSvc := NewAttachmentShareService(b, lookB, nil, nil, nil)

	_, err := bSvc.RequestAttachment(ctx, a.PeerID(), provA.id)
	if err == nil {
		t.Fatal("expected error for revoked peer, got nil")
	}
	if !errors.Is(err, ErrPeerNotPaired) {
		t.Errorf("expected ErrPeerNotPaired, got %v", err)
	}
}
