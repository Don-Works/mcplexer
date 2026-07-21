package scopes

import (
	"encoding/json"
	"testing"
)

// TestDenialCodeValid asserts each canonical code reports itself as
// valid, and rejects an unknown string. Guards against a future PR
// adding a fifth const without also extending Valid().
func TestDenialCodeValid(t *testing.T) {
	cases := []struct {
		code DenialCode
		want bool
	}{
		{DenialNoScope, true},
		{DenialScopeRevoked, true},
		{DenialScopeOutOfBand, true},
		{DenialCrossOrgBoundary, true},
		{DenialCode("bogus"), false},
		{DenialCode(""), false},
	}
	for _, c := range cases {
		if got := c.code.Valid(); got != c.want {
			t.Errorf("DenialCode(%q).Valid() = %v, want %v", c.code, got, c.want)
		}
	}
}

// TestDenialCodeStringMatchesConst makes the wire tokens load-bearing.
// Clients pattern-match on the literal strings (no_scope, scope_revoked,
// scope_out_of_band, cross_org_boundary) — changing them silently
// breaks every consumer.
func TestDenialCodeStringMatchesConst(t *testing.T) {
	cases := map[DenialCode]string{
		DenialNoScope:          "no_scope",
		DenialScopeRevoked:     "scope_revoked",
		DenialScopeOutOfBand:   "scope_out_of_band",
		DenialCrossOrgBoundary: "cross_org_boundary",
	}
	for code, want := range cases {
		if got := code.String(); got != want {
			t.Errorf("DenialCode.String(): got %q, want %q", got, want)
		}
	}
}

// TestDenialJSONShape locks down the on-the-wire JSON. Both the full
// shape (all four fields populated) and the minimal shape (code only,
// optional fields omitted) are user-visible — pin both.
func TestDenialJSONShape(t *testing.T) {
	full := Denial{
		Code:    DenialScopeRevoked,
		Scope:   "memory.read",
		Peer:    "12D3KooW...",
		Message: "revoked at 2026-05-27",
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	want := `{"code":"scope_revoked","scope":"memory.read","peer":"12D3KooW...","message":"revoked at 2026-05-27"}`
	if string(b) != want {
		t.Errorf("full denial JSON mismatch:\n got %s\nwant %s", b, want)
	}

	min := Denial{Code: DenialNoScope}
	b, err = json.Marshal(min)
	if err != nil {
		t.Fatalf("marshal min: %v", err)
	}
	if string(b) != `{"code":"no_scope"}` {
		t.Errorf("minimal denial JSON mismatch: got %s, want %s",
			b, `{"code":"no_scope"}`)
	}
}

// TestNewBuildsFields covers the two-line happy path. The builder must
// not silently drop the scope/peer arguments.
func TestNewBuildsFields(t *testing.T) {
	d := New(DenialCrossOrgBoundary, "task_offer:bravo", "peerX")
	if d.Code != DenialCrossOrgBoundary {
		t.Errorf("code: got %q, want %q", d.Code, DenialCrossOrgBoundary)
	}
	if d.Scope != "task_offer:bravo" {
		t.Errorf("scope: got %q, want %q", d.Scope, "task_offer:bravo")
	}
	if d.Peer != "peerX" {
		t.Errorf("peer: got %q, want %q", d.Peer, "peerX")
	}
	if d.Message != "" {
		t.Errorf("message should default empty, got %q", d.Message)
	}
}

// TestWithMessageReturnsCopy verifies the chained builder doesn't
// mutate the receiver — Denial is a value type, callers may share
// it across requests; a copy-on-chain is the safer default.
func TestWithMessageReturnsCopy(t *testing.T) {
	base := New(DenialNoScope, "skill.install", "")
	derived := base.WithMessage("ask user to grant")
	if base.Message != "" {
		t.Errorf("WithMessage mutated receiver: base.Message = %q", base.Message)
	}
	if derived.Message != "ask user to grant" {
		t.Errorf("WithMessage didn't set Message: %q", derived.Message)
	}
	if derived.Code != base.Code || derived.Scope != base.Scope {
		t.Errorf("WithMessage altered non-message fields: %+v vs %+v",
			base, derived)
	}
}
