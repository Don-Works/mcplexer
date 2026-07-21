//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"testing"
)

// TestSkillShareStub_Returns_NotBuiltIn verifies that calling any method on
// a stub-mode SkillShareService returns ErrP2PNotBuiltIn. The gateway
// tooling pattern-matches on this sentinel to surface a helpful message
// to the agent.
func TestSkillShareStub_Returns_NotBuiltIn(t *testing.T) {
	svc := NewSkillShareService(nil, nil, nil, nil, nil, nil)
	if svc == nil {
		t.Fatal("NewSkillShareService returned nil — should be non-nil even in stub")
	}

	if err := svc.OfferSkill(context.Background(), "peer", "skill"); !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("OfferSkill err = %v, want ErrP2PNotBuiltIn", err)
	}
	if _, err := svc.RequestSkill(
		context.Background(), "peer", "skill", "1.0.0",
	); !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("RequestSkill err = %v, want ErrP2PNotBuiltIn", err)
	}
	if _, ok := svc.LastOfferFor("peer", "skill"); ok {
		t.Fatalf("LastOfferFor: expected ok=false in stub mode")
	}
}
