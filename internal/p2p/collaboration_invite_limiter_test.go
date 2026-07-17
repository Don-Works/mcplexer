package p2p

import (
	"testing"
	"time"
)

func TestCollaborationInviteLimiterBoundsPeerAndTokenAttempts(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	var limiter collaborationInviteLimiter
	for i := 0; i < collaborationInviteTokenBurst; i++ {
		if !limiter.allow("peer-a", "invite-a", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("token attempt %d denied too early", i)
		}
	}
	if limiter.allow("peer-a", "invite-a", now.Add(time.Minute)) {
		t.Fatal("per-invitation burst was not enforced")
	}
	for i := collaborationInviteTokenBurst; i < collaborationInviteRemoteBurst; i++ {
		if !limiter.allow("peer-a", "invite-other-"+time.Duration(i).String(), now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("remote attempt %d denied too early", i)
		}
	}
	if limiter.allow("peer-a", "one-more", now.Add(2*time.Minute)) {
		t.Fatal("per-peer burst was not enforced")
	}
	if !limiter.allow("peer-a", "invite-a", now.Add(collaborationInviteRateWindow+time.Minute)) {
		t.Fatal("limiter did not recover after the window")
	}
}

// TestCollaborationInviteLimiterRejectedInvitesDoNotGrowMaps is the memory-DoS
// regression: once a peer's burst is exhausted, further requests with fresh
// invitation IDs must be rejected WITHOUT materializing a permanent map entry
// per attacker-chosen ID (the original unbounded-growth vector).
func TestCollaborationInviteLimiterRejectedInvitesDoNotGrowMaps(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	var limiter collaborationInviteLimiter
	// Exhaust the per-peer burst.
	for i := 0; i < collaborationInviteRemoteBurst; i++ {
		limiter.allow("peer-flood", "invite-"+time.Duration(i).String(), now.Add(time.Duration(i)*time.Second))
	}
	invitesAfterBurst := len(limiter.invites)
	// Now hammer with unique, always-rejected invitation IDs.
	for i := 0; i < 5000; i++ {
		if limiter.allow("peer-flood", "flood-"+time.Duration(i).String(), now.Add(time.Minute)) {
			t.Fatalf("request %d unexpectedly allowed after burst", i)
		}
	}
	if grew := len(limiter.invites) - invitesAfterBurst; grew != 0 {
		t.Fatalf("rejected fresh invitation IDs grew l.invites by %d entries, want 0", grew)
	}
	if len(limiter.remote) != 1 {
		t.Fatalf("l.remote = %d keys, want 1 (single flooding peer)", len(limiter.remote))
	}
}
