//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestHandleStreamLegacyInitiatorNoDeadlock is the regression test for the
// responder-side handshake deadlock: a genuine legacy initiator sends ONLY
// the code line and then blocks reading our reply (it never writes the M7.1
// identity line nor closes its write side). Before the fix, the responder's
// readIdentityFrame did a blind ReadString('\n') under the full 30s handshake
// budget, so it stalled for the whole window and pinned a goroutine. The fix
// reads the identity frame under a short independent deadline and treats a
// timeout/EOF as "legacy peer, no identity", so the "ok" reply must arrive
// promptly (well under the 2s identity-frame timeout).
func TestHandleStreamLegacyInitiatorNoDeadlock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a-legacy-raw")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-legacy-raw")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}

	// B dials A and opens a raw pairing stream — no PairingService on B, so
	// we control the exact wire bytes (mimicking an old binary).
	connectHosts(t, ctx, b, a)
	stream, err := b.Inner().NewStream(ctx, a.ID(), PairingProtocol)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Write ONLY the code line. Crucially: we do NOT write the identity
	// line and we do NOT close our write side — this is exactly what a
	// legacy initiator does while it blocks waiting for the reply.
	if _, err := fmt.Fprintln(stream, res.Code); err != nil {
		t.Fatalf("write code line: %v", err)
	}

	// The responder must reply "ok" promptly. Read with a deadline shorter
	// than the legacy handshake budget but comfortably above the
	// identity-frame timeout, so a regression (blocking on the full 30s)
	// fails loudly here.
	const replyBudget = 5 * time.Second
	type result struct {
		reply string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		line, rerr := bufio.NewReader(stream).ReadString('\n')
		done <- result{reply: strings.TrimSpace(line), err: rerr}
	}()

	start := time.Now()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("read reply: %v", r.err)
		}
		if r.reply != "ok" {
			t.Fatalf("reply = %q, want %q", r.reply, "ok")
		}
		if elapsed := time.Since(start); elapsed >= identityFrameTimeout+2*time.Second {
			t.Fatalf("reply took %v — responder appears to block on the full handshake budget (deadlock regression)", elapsed)
		}
	case <-time.After(replyBudget):
		t.Fatalf("no reply within %v — responder deadlocked waiting for the (never-sent) identity line", replyBudget)
	}
}

// TestHandleStreamRateLimitDoesNotBurnCode is the end-to-end regression for
// the rate-limit rejection path through handleStream. When the limiter
// rejects a dial, the responder writes "no" and returns WITHOUT consuming a
// code. We assert: (a) a rate-limited CompletePair fails with
// ErrPairingInvalid, and (b) after the limiter is reset, a subsequent
// CompletePair with the SAME code still succeeds — proving the pending code
// was never burned by the rejected attempt.
func TestHandleStreamRateLimitDoesNotBurnCode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a-ratelimit")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-ratelimit")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	bSvc := NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}

	// Force A's responder to reject every dial: perPeerMax=0 means allow()
	// returns false before any code is consulted.
	rejecting := newPairingRateLimiter()
	rejecting.perPeerMax = 0
	aSvc.rateLim = rejecting

	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); !errors.Is(err, ErrPairingInvalid) {
		t.Fatalf("rate-limited CompletePair err = %v, want ErrPairingInvalid", err)
	}

	// Reset to a permissive limiter and retry with the SAME code. If the
	// rejected attempt had consumed/invalidated the pending code, this would
	// now fail with ErrPairingInvalid.
	aSvc.rateLim = newPairingRateLimiter()
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("retry after reset (same code) should succeed, got: %v", err)
	}
}
