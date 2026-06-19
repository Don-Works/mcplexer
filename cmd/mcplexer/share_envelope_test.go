package main

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeResolver returns canned tier/auto_pair/grant values so each
// envelope-shape test stays self-contained.
type fakeResolver struct {
	tier     consent.Tier
	autoPair bool
	grant    consent.GrantOrigin
}

func (f fakeResolver) TierFor(context.Context, string) consent.Tier {
	return f.tier
}

func (f fakeResolver) AutoPairAccepted(context.Context, string) bool {
	return f.autoPair
}

func (f fakeResolver) GrantOriginFor(context.Context, string, string) consent.GrantOrigin {
	return f.grant
}

func TestShareEnvelopeTier1IsAutoPair(t *testing.T) {
	t.Parallel()
	r := fakeResolver{tier: consent.TierSameUser, autoPair: true}
	self := &store.User{UserID: "u-self"}
	env := shareEnvelope(context.Background(), r, self,
		"peer-x", "mesh.skill_request", "ok", "")
	if env.Tier != consent.TierSameUser {
		t.Errorf("Tier = %q, want same_user", env.Tier)
	}
	if env.AcceptedBy.Kind != consent.AcceptedKindAutoPair {
		t.Errorf("Kind = %q, want auto_pair", env.AcceptedBy.Kind)
	}
	// Tier 1 must NOT carry a grant_origin — the silent-grant contract.
	if !env.GrantOrigin.IsZero() {
		t.Errorf("Tier 1 leaked grant_origin: %+v", env.GrantOrigin)
	}
	if env.DenialReason != "" {
		t.Errorf("DenialReason on ok row = %q, want empty", env.DenialReason)
	}
}

func TestShareEnvelopeTier2HumanWithGrantOrigin(t *testing.T) {
	t.Parallel()
	r := fakeResolver{
		tier:     consent.TierSameOrg,
		autoPair: false,
		grant:    consent.GrantOrigin{PeerID: "p", AgentID: "a", GrantID: "g-1"},
	}
	self := &store.User{UserID: "u-bob"}
	env := shareEnvelope(context.Background(), r, self,
		"peer-c", "mesh.memory_request", "ok", "")
	if env.Tier != consent.TierSameOrg {
		t.Errorf("Tier = %q, want same_org", env.Tier)
	}
	if env.AcceptedBy.Kind != consent.AcceptedKindHuman {
		t.Errorf("Kind = %q, want human", env.AcceptedBy.Kind)
	}
	if env.AcceptedBy.UserID != "u-bob" {
		t.Errorf("UserID = %q, want u-bob", env.AcceptedBy.UserID)
	}
	if env.AcceptedBy.Timestamp.IsZero() {
		t.Error("human envelope timestamp must be stamped")
	}
	if env.GrantOrigin.IsZero() {
		t.Error("Tier 2 success row must carry grant_origin")
	}
}

func TestShareEnvelopeCrossOrgDenied(t *testing.T) {
	t.Parallel()
	r := fakeResolver{tier: consent.TierCrossOrg}
	self := &store.User{UserID: "u-alice"}
	env := shareEnvelope(context.Background(), r, self,
		"peer-d", "mesh.skill_request",
		"denied", "scope mesh.skill_request required")
	if env.Tier != consent.TierCrossOrg {
		t.Errorf("Tier = %q, want cross_org", env.Tier)
	}
	if env.DenialReason != "cross_org_boundary" {
		t.Errorf("DenialReason = %q, want cross_org_boundary", env.DenialReason)
	}
	// On rejection rows we don't populate GrantOrigin — no grant
	// authorized the share (that's the whole point of the denial).
	if !env.GrantOrigin.IsZero() {
		t.Errorf("rejected row leaked grant_origin: %+v", env.GrantOrigin)
	}
}

func TestDenialReasonFromError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  string
		tier consent.Tier
		want string
	}{
		{"peer not paired: 12D3...", consent.TierCrossOrg, "not_paired"},
		{"peer not paired: 12D3...", consent.TierSameOrg, "not_paired"},
		{"scope mesh.foo revoked", consent.TierSameOrg, "scope_revoked"},
		{"scope mesh.bar required", consent.TierCrossOrg, "cross_org_boundary"},
		{"scope mesh.bar required", consent.TierSameOrg, "no_scope"},
		{"skill not installed: foo", consent.TierSameUser, "not_installed"},
		{"memory not found", consent.TierCrossOrg, "not_found"},
		{"bundle too large: 200 > 100", consent.TierSameUser, "too_large"},
		{"generic denied message", consent.TierCrossOrg, "cross_org_boundary"},
		{"generic denied message", consent.TierSameOrg, "no_scope"},
		{"unknown failure mode", consent.TierCrossOrg, ""},
		{"", consent.TierCrossOrg, ""},
	}
	for _, tc := range cases {
		t.Run(tc.err, func(t *testing.T) {
			got := denialReasonFromError(tc.tier, tc.err)
			if got != tc.want {
				t.Errorf("denialReasonFromError(%q, %q) = %q, want %q",
					tc.tier, tc.err, got, tc.want)
			}
		})
	}
}

func TestShareEnvelopeSelfUserNil(t *testing.T) {
	t.Parallel()
	// Daemon hasn't bootstrapped the self user yet — envelope still
	// builds but UserID/AgentID stay empty. The consent_audit scenario
	// will then SKIP+PENDING on these rows (not FAIL).
	r := fakeResolver{tier: consent.TierSameOrg}
	env := shareEnvelope(context.Background(), r, nil,
		"peer-c", "mesh.memory_request", "ok", "")
	if env.AcceptedBy.Kind != consent.AcceptedKindHuman {
		t.Errorf("Kind = %q, want human", env.AcceptedBy.Kind)
	}
	if env.AcceptedBy.UserID != "" || env.AcceptedBy.AgentID != "" {
		t.Errorf("expected empty user/agent ids with nil selfUser, got %+v",
			env.AcceptedBy)
	}
}

func TestShareEnvelopeNilResolverFallsBackToCrossOrg(t *testing.T) {
	t.Parallel()
	env := shareEnvelope(context.Background(), nil, nil,
		"peer-x", "mesh.skill_request", "ok", "")
	// Nop resolver returns cross_org — most-restrictive default.
	if env.Tier != consent.TierCrossOrg {
		t.Errorf("Tier = %q, want cross_org default", env.Tier)
	}
	if env.AcceptedBy.Kind != consent.AcceptedKindHuman {
		t.Errorf("Kind = %q, want human (no auto_pair without resolver)",
			env.AcceptedBy.Kind)
	}
}

func TestIsShareSuccessAndRejection(t *testing.T) {
	t.Parallel()
	successes := []string{"ok", "OK", "success", "Pending"}
	for _, s := range successes {
		if !isShareSuccess(s) {
			t.Errorf("isShareSuccess(%q) = false, want true", s)
		}
	}
	rejections := []struct{ status, err string }{
		{"denied", ""},
		{"error", ""},
		{"rejected", ""},
		{"success", "operation denied"},
	}
	for _, r := range rejections {
		if !isShareRejection(r.status, r.err) {
			t.Errorf("isShareRejection(%q, %q) = false, want true",
				r.status, r.err)
		}
	}
	if isShareRejection("success", "") {
		t.Error("isShareRejection(success, '') = true, want false")
	}
}

// TestConsentResolverImplFallsBackToCrossOrg pins the "safest default"
// contract: a resolver with no self user OR no peer mapping returns
// cross_org rather than silently downgrading to same_user. Misclassify
// in the strict direction.
func TestConsentResolverImplFallsBackToCrossOrg(t *testing.T) {
	t.Parallel()
	r := newConsentResolver(nopLookupStore{}, nil)
	if got := r.TierFor(context.Background(), "any-peer"); got != consent.TierCrossOrg {
		t.Errorf("Tier with nil selfUser = %q, want cross_org", got)
	}
	if r.AutoPairAccepted(context.Background(), "any-peer") {
		t.Error("AutoPairAccepted with nil selfUser must be false")
	}
}

// TestConsentResolverImplSameUserMatch covers the Tier 1 happy path:
// peer maps to a user whose ID equals self.UserID.
func TestConsentResolverImplSameUserMatch(t *testing.T) {
	t.Parallel()
	st := fakeLookup{userForPeer: &store.User{UserID: "u-alice"}}
	self := &store.User{UserID: "u-alice"}
	r := newConsentResolver(st, self)
	if got := r.TierFor(context.Background(), "peer-b"); got != consent.TierSameUser {
		t.Errorf("Tier = %q, want same_user", got)
	}
	if !r.AutoPairAccepted(context.Background(), "peer-b") {
		t.Error("AutoPairAccepted on same-user pair must be true")
	}
}

// TestGrantOriginForReturnsZeroOnEmpty pins the GrantOrigin zero-shape:
// empty peer or scope → zero envelope → audit row omits the field.
func TestGrantOriginForReturnsZeroOnEmpty(t *testing.T) {
	t.Parallel()
	r := newConsentResolver(nopLookupStore{}, &store.User{UserID: "u"})
	g := r.GrantOriginFor(context.Background(), "", "scope")
	if !g.IsZero() {
		t.Errorf("empty peerID should produce zero GrantOrigin, got %+v", g)
	}
	g = r.GrantOriginFor(context.Background(), "peer", "")
	if !g.IsZero() {
		t.Errorf("empty scope should produce zero GrantOrigin, got %+v", g)
	}
	g = r.GrantOriginFor(context.Background(), "peer", "mesh.foo")
	if g.IsZero() {
		t.Error("populated peer+scope must produce non-zero GrantOrigin")
	}
}

// TestConsentResolverImplDifferentUserCrossOrg covers the Tier 3 path:
// peer maps to a different user, MCPLEXER_SELF_ORG unset → cross_org.
// Not parallel — uses t.Setenv.
func TestConsentResolverImplDifferentUserCrossOrg(t *testing.T) {
	t.Setenv("MCPLEXER_SELF_ORG", "")
	st := fakeLookup{userForPeer: &store.User{UserID: "u-carol"}}
	self := &store.User{UserID: "u-alice"}
	r := newConsentResolver(st, self)
	if got := r.TierFor(context.Background(), "peer-d"); got != consent.TierCrossOrg {
		t.Errorf("Tier = %q, want cross_org (no MCPLEXER_SELF_ORG set)", got)
	}
}

// Helpers ----------------------------------------------------------------

type nopLookupStore struct{}

func (nopLookupStore) GetUserForPeer(context.Context, string) (*store.User, error) {
	return nil, store.ErrNotFound
}

type fakeLookup struct {
	userForPeer *store.User
}

func (f fakeLookup) GetUserForPeer(context.Context, string) (*store.User, error) {
	if f.userForPeer == nil {
		return nil, store.ErrNotFound
	}
	return f.userForPeer, nil
}

// Quick smoke that the mesh consent bridge wires both methods through.
func TestMeshConsentBridge(t *testing.T) {
	t.Parallel()
	r := fakeResolver{tier: consent.TierSameOrg, autoPair: false}
	b := newMeshConsentBridge(r)
	if b == nil {
		t.Fatal("bridge nil for non-nil resolver")
	}
	if got := b.TierForString(context.Background(), "p"); got != "same_org" {
		t.Errorf("TierForString = %q, want same_org", got)
	}
	if b.AutoPairAccepted(context.Background(), "p") {
		t.Error("AutoPairAccepted leaked true from underlying resolver")
	}
	// Nil resolver → nil bridge → callers no-op.
	if newMeshConsentBridge(nil) != nil {
		t.Error("nil resolver should produce nil bridge")
	}
	// Defensive: nil-bridge method calls must not panic.
	var nb *meshConsentBridge
	if got := nb.TierForString(context.Background(), "p"); got != "" {
		t.Errorf("nil bridge TierForString = %q, want empty", got)
	}
	if nb.AutoPairAccepted(context.Background(), "p") {
		t.Error("nil bridge AutoPairAccepted = true")
	}
}
