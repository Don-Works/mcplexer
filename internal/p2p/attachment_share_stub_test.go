//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"testing"
)

// TestAttachmentShareServiceStubReturnsNotBuilt asserts the slim build
// constructor produces a non-nil service whose only method short-
// circuits to ErrP2PNotBuiltIn. Mirrors TestSkillShareServiceStub.
func TestAttachmentShareServiceStubReturnsNotBuilt(t *testing.T) {
	svc := NewAttachmentShareService(nil, nil, nil, nil, nil)
	if svc == nil {
		t.Fatal("NewAttachmentShareService returned nil — should be non-nil even in stub")
	}
	_, err := svc.RequestAttachment(context.Background(), "peer-x", "id-y")
	if !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("expected ErrP2PNotBuiltIn, got %v", err)
	}
}

// TestMarshalAttachmentRequestSlimBuild guards the wire-shape helper —
// ensures the JSON encoding is stable and matches the protocol header
// the p2p build would read. The slim build doesn't have the protocol
// itself, but the encoding helpers must agree so cross-mode test fakes
// (e.g. a slim-build proxy fronting a real p2p daemon) round-trip.
func TestMarshalAttachmentRequestSlimBuild(t *testing.T) {
	got, err := MarshalAttachmentRequest(AttachmentRequest{Type: "request", ID: "01HZZZ"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"request","id":"01HZZZ"}`
	if string(got) != want {
		t.Errorf("marshal got %q, want %q", string(got), want)
	}
}
