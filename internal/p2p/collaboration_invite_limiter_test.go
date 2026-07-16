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
