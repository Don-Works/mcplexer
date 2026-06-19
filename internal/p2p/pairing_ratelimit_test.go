package p2p

import (
	"testing"
	"time"
)

func TestPairingRateLimiter_PerPeer(t *testing.T) {
	l := newPairingRateLimiter()
	l.perPeerMax = 3
	l.window = time.Minute
	l.lockoutAfter = 100 // disable for this test

	now := time.Now()
	for i := 0; i < l.perPeerMax; i++ {
		if !l.allow("peerA", now) {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if l.allow("peerA", now) {
		t.Fatal("4th attempt within window should be rejected")
	}

	// Different peer is unaffected.
	if !l.allow("peerB", now) {
		t.Fatal("peerB should be allowed (independent counter)")
	}

	// After window passes, peerA's bucket is cleared.
	later := now.Add(2 * time.Minute)
	if !l.allow("peerA", later) {
		t.Fatal("peerA should be allowed after window resets")
	}
}

func TestPairingRateLimiter_Global(t *testing.T) {
	l := newPairingRateLimiter()
	l.perPeerMax = 1000
	l.globalMax = 5
	l.window = time.Minute
	l.lockoutAfter = 100 // disable for this test

	now := time.Now()
	for i := 0; i < l.globalMax; i++ {
		peer := "peer-" + string(rune('A'+i))
		if !l.allow(peer, now) {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if l.allow("peer-Z", now) {
		t.Fatal("global cap should reject")
	}
}

func TestPairingRateLimiter_Lockout(t *testing.T) {
	l := newPairingRateLimiter()
	l.perPeerMax = 1000
	l.globalMax = 1000
	l.window = time.Hour
	l.lockoutAfter = 5
	l.lockoutWindow = time.Hour

	now := time.Now()
	for i := 0; i < l.lockoutAfter; i++ {
		if !l.allow("attacker", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	// Now locked out.
	if l.allow("attacker", now.Add(time.Hour)) {
		t.Fatal("attacker should be locked out within lockoutWindow")
	}
	// After lockoutWindow expires, allowed again.
	if !l.allow("attacker", now.Add(2*time.Hour+time.Minute)) {
		t.Fatal("attacker should be allowed after lockoutWindow")
	}
}
