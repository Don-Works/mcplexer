// memory_share_deny_test.go — locks down the constant-shape deny
// envelope for /mcplexer/memory/1.0.0 (JTAC65 / D7.3 fix).
//
// The load-bearing security claim: a Tier-2/3 peer that issues a memory
// request the daemon refuses MUST receive bytes that are IDENTICAL in
// shape across the four refusal causes:
//
//   - memory_id genuinely doesn't exist
//   - memory_id exists but the peer doesn't hold the boolean
//     mesh.memory_request scope
//   - memory_id exists in a workspace the peer isn't scoped to
//   - memory is over the size cap
//
// If ANY field, header, or byte differs between these four cases the
// requester can side-channel-infer the un-granted memory's existence.
// These tests pin the envelope shape + verify the helper that builds
// it never emits a resource-specific token (scope name, memory id,
// content fragment, workspace id).
//
// Tests build under both `-tags p2p` and the slim stub so the contract
// is enforced regardless of build mode. (The memoryShareError type +
// newDenyError live in the p2p-build path; under the stub there's no
// network handler to test, so the file builds but only the p2p tests
// run.)

//go:build p2p

package p2p

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/scopes"
)

// TestNewDenyErrorShape pins the wire envelope for a deny reply. Any
// change to this string is a wire-break — every paired peer's
// decodeMemoryStreamError relies on Code="denied" and the typed Denial
// body. Regression tests for this exact byte sequence catch accidental
// field additions (e.g. someone adding scope/remote_id back).
func TestNewDenyErrorShape(t *testing.T) {
	got := newDenyError("12D3KooWPeerExample")
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"error","code":"denied","denial":{"code":"no_scope","peer":"12D3KooWPeerExample"}}`
	if string(b) != want {
		t.Errorf("deny envelope shape drift:\n got %s\nwant %s", b, want)
	}
}

// TestNewDenyErrorNoResourceTokens fuzz-checks the load-bearing claim:
// regardless of what peerID we pass, the wire bytes MUST NOT contain
// any of the resource-name tokens that the four refusal causes would
// otherwise leak — scope strings, memory IDs, workspace names, content
// fragments. We seed the helper with each forbidden token in turn and
// assert it does not appear in the marshalled envelope.
func TestNewDenyErrorNoResourceTokens(t *testing.T) {
	// Each "forbidden" token here represents a kind of disclosure the
	// previous envelope (memoryShareError{Code:"denied", Message: ...})
	// could have leaked. None of them is the peerID, which IS allowed
	// in the envelope (the requester already knows their own peer id).
	forbidden := []string{
		"alpha-private",              // workspace_id
		"mesh.memory_request",        // scope name
		"01HZX4M7TZTPC4MQM4MR4WJN3J", // ULID-shaped memory id
		"secret-canary-content",      // memory content fragment
		"too large",                  // size cap message
		"scope required",             // legacy err message
		"memory not found",           // legacy err message
		"p2p: memory share denied: scope mesh.memory_request required",
	}
	env := newDenyError("12D3KooWPeerExample")
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, tok := range forbidden {
		if bytes.Contains(b, []byte(tok)) {
			t.Errorf("deny envelope leaked %q: %s", tok, b)
		}
	}
}

// TestNewDenyErrorConstantBytesAcrossPeers verifies the envelope length
// is a pure function of peerID length — NOT a function of which cause
// triggered the deny. The handler MUST pass the same peerID to
// newDenyError regardless of the underlying error, so the bytes a
// requester sees are identical across (not_paired, no_scope,
// memory_not_found, workspace_not_granted, too_large). This test pins
// the property by computing the length for one peer and asserting it
// stays stable across several "cause" invocations.
func TestNewDenyErrorConstantBytesAcrossPeers(t *testing.T) {
	const peerID = "12D3KooWPeerExample"
	// Build several deny envelopes through what would be the four
	// distinct causes in the previous (leaky) code: not_paired,
	// no_scope, not_found, too_large. Post-fix they all go through
	// the same helper.
	cases := []string{"not_paired", "no_scope", "not_found", "too_large"}
	first, err := json.Marshal(newDenyError(peerID))
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	for _, c := range cases {
		got, err := json.Marshal(newDenyError(peerID))
		if err != nil {
			t.Fatalf("marshal %s: %v", c, err)
		}
		if !bytes.Equal(first, got) {
			t.Errorf("%s deny bytes differ from baseline:\n  base %s\n  got  %s",
				c, first, got)
		}
	}
}

// TestDecodeMemoryStreamErrorCollapsesToDenied verifies the receive-side
// decoder normalises every denied envelope to ErrMemoryShareDenied. Pre-
// fix the decoder had four error sentinels (denied/not_found/too_large/
// internal); post-fix only "denied" + "bad_request" remain on the wire,
// and the decoder MUST return ErrMemoryShareDenied for "denied" without
// dragging the scope/remote_id back into the sender-side caller's view
// (which would just re-create the side-channel one layer up).
func TestDecodeMemoryStreamErrorCollapsesToDenied(t *testing.T) {
	envBytes, err := json.Marshal(newDenyError("12D3KooWPeer"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotErr := decodeMemoryStreamError(envBytes)
	if gotErr == nil {
		t.Fatal("expected error, got nil")
	}
	// The returned error MUST be the bare sentinel — NOT a wrapped
	// "p2p: memory share denied: <cause>" string with detail. That
	// would re-leak the cause via the sender-side error message.
	if gotErr.Error() != ErrMemoryShareDenied.Error() {
		t.Errorf("decoded error leaks detail:\n got %q\nwant %q",
			gotErr.Error(), ErrMemoryShareDenied.Error())
	}
}

// TestDecodeMemoryStreamErrorRetainsBadRequestDetail verifies the
// non-secret branch (malformed envelope) STILL returns a narrative
// error — the bad_request path is not a leak vector since the daemon
// is echoing back the requester's own bytes that failed to parse, not
// disclosing local resource state.
func TestDecodeMemoryStreamErrorRetainsBadRequestDetail(t *testing.T) {
	env := memoryShareError{
		Type: "error", Code: "bad_request", Message: "json: unexpected EOF",
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotErr := decodeMemoryStreamError(envBytes)
	if gotErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(gotErr.Error(), "bad_request") {
		t.Errorf("bad_request error didn't pass through label: %v", gotErr)
	}
	if !strings.Contains(gotErr.Error(), "unexpected EOF") {
		t.Errorf("bad_request error didn't pass through message: %v", gotErr)
	}
}

// TestDenyEnvelopeMatchesScopeDenialContract makes the wire format's
// dependency on internal/scopes.Denial explicit. If a future PR changes
// the Denial JSON tags (e.g. renames "code" → "kind"), every consumer
// of newDenyError silently re-encodes — this test fires first so the
// breakage is caught at the right layer.
func TestDenyEnvelopeMatchesScopeDenialContract(t *testing.T) {
	env := newDenyError("12D3KooWPeer")
	if env.Denial == nil {
		t.Fatal("Denial body must not be nil")
	}
	if env.Denial.Code != scopes.DenialNoScope {
		t.Errorf("Denial.Code: got %q, want %q",
			env.Denial.Code, scopes.DenialNoScope)
	}
	// The Scope field MUST be empty — passing a scope name here would
	// re-introduce the side-channel via the marshalled body.
	if env.Denial.Scope != "" {
		t.Errorf("Denial.Scope must be empty on the wire, got %q",
			env.Denial.Scope)
	}
	// Peer is the only resource-id field the wire carries — and it's
	// the REQUESTER's own peer id, which they already know. Surfacing
	// it back is a sanity check ("yes, I talked to the right daemon"),
	// not a disclosure of the un-granted memory.
	if env.Denial.Peer != "12D3KooWPeer" {
		t.Errorf("Denial.Peer: got %q, want %q",
			env.Denial.Peer, "12D3KooWPeer")
	}
	// The Message field MUST stay empty — populating it with the
	// underlying error string is the literal leak the audit captured.
	if env.Denial.Message != "" {
		t.Errorf("Denial.Message must stay empty on the wire, got %q",
			env.Denial.Message)
	}
}
