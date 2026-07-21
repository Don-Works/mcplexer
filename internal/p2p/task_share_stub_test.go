//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"testing"
)

// TestTaskShareStub_Returns_NotBuiltIn pins the slim-build contract:
// the TaskShareService constructor returns a non-nil stub and every
// method returns ErrP2PNotBuiltIn so the gateway tooling can surface
// "p2p not enabled" to the agent.
func TestTaskShareStub_Returns_NotBuiltIn(t *testing.T) {
	svc := NewTaskShareService(nil, nil, nil, nil, nil, nil)
	if svc == nil {
		t.Fatal("NewTaskShareService returned nil — should be non-nil even in stub")
	}
	if _, err := svc.OfferTask(context.Background(), "peer", TaskOfferEnvelope{}); !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("OfferTask err = %v, want ErrP2PNotBuiltIn", err)
	}
	if _, err := svc.AssignTaskRemote(context.Background(), "peer", TaskOfferEnvelope{}); !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("AssignTaskRemote err = %v, want ErrP2PNotBuiltIn", err)
	}
	if _, err := svc.RequestTaskPayload(context.Background(), "peer", "nonce", "task-id"); !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("RequestTaskPayload err = %v, want ErrP2PNotBuiltIn", err)
	}
}
