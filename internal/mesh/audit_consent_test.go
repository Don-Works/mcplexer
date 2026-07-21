package mesh

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeConsentResolver is the test double for ConsentResolver. Drives
// the recordSend tier+accepted_by population.
type fakeConsentResolver struct {
	tier     string
	autoPair bool
}

func (f fakeConsentResolver) TierForString(context.Context, string) string {
	return f.tier
}

func (f fakeConsentResolver) AutoPairAccepted(context.Context, string) bool {
	return f.autoPair
}

// TestRecordSendBroadcastSkipsConsentEnvelope confirms that audience
// broadcasts (req.ToPeer == "") do NOT carry tier/accepted_by — only
// peer-addressed sends count as cross-boundary per the brief.
func TestRecordSendBroadcastSkipsConsentEnvelope(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	m.SetConsentResolver(fakeConsentResolver{tier: "same_org"}, "u-self", "agent-self")

	req := SendRequest{
		Audience: "*",
		Kind:     "finding",
		Content:  "broadcast",
	}
	m.recordSend(context.Background(), SessionMeta{SessionID: "s"}, req, nil,
		"success", "", "")

	recs := a.waitFor(t, 1)
	if recs[0].Tier != "" {
		t.Errorf("broadcast row leaked tier=%q, want empty", recs[0].Tier)
	}
	if recs[0].AcceptedBy != nil {
		t.Errorf("broadcast row leaked accepted_by=%s, want nil",
			string(recs[0].AcceptedBy))
	}
}

// TestRecordSendPeerAddressedTier1AutoPair pins the Tier 1 contract:
// peer-addressed mesh__send to a same-user peer stamps tier=same_user
// + accepted_by={kind:auto_pair}, NOT a human envelope.
func TestRecordSendPeerAddressedTier1AutoPair(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	m.SetConsentResolver(
		fakeConsentResolver{tier: "same_user", autoPair: true},
		"u-self", "agent-self",
	)

	req := SendRequest{
		ToPeer:  "peer-machine-2",
		Kind:    "task",
		Content: "hand-off",
	}
	m.recordSend(context.Background(), SessionMeta{SessionID: "s"}, req, nil,
		"success", "", "")

	recs := a.waitFor(t, 1)
	r := recs[0]
	if r.Tier != "same_user" {
		t.Errorf("Tier = %q, want same_user", r.Tier)
	}
	var env map[string]any
	if err := json.Unmarshal(r.AcceptedBy, &env); err != nil {
		t.Fatalf("decode accepted_by: %v / %s", err, r.AcceptedBy)
	}
	if env["kind"] != "auto_pair" {
		t.Errorf("kind = %v, want auto_pair", env["kind"])
	}
	// Tier 1 must NOT carry user_id / agent_id / timestamp.
	for _, k := range []string{"user_id", "agent_id", "timestamp"} {
		if _, present := env[k]; present {
			t.Errorf("auto_pair envelope unexpectedly carries %q: %v", k, env)
		}
	}
}

// TestRecordSendPeerAddressedTier2Human pins the Tier 2/3 contract:
// peer-addressed mesh__send to a different-user peer stamps tier=
// same_org (or cross_org) + accepted_by={kind:human, user_id, agent_id,
// timestamp}.
func TestRecordSendPeerAddressedTier2Human(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	m.SetConsentResolver(
		fakeConsentResolver{tier: "same_org", autoPair: false},
		"u-alice", "agent-cli-7",
	)

	req := SendRequest{
		ToPeer:  "peer-bob",
		Kind:    "finding",
		Content: "shared insight",
	}
	m.recordSend(context.Background(), SessionMeta{SessionID: "s"}, req, nil,
		"success", "", "")

	recs := a.waitFor(t, 1)
	r := recs[0]
	if r.Tier != "same_org" {
		t.Errorf("Tier = %q, want same_org", r.Tier)
	}
	var env map[string]any
	if err := json.Unmarshal(r.AcceptedBy, &env); err != nil {
		t.Fatalf("decode accepted_by: %v / %s", err, r.AcceptedBy)
	}
	if env["kind"] != "human" {
		t.Errorf("kind = %v, want human", env["kind"])
	}
	if env["user_id"] != "u-alice" {
		t.Errorf("user_id = %v, want u-alice", env["user_id"])
	}
	if env["agent_id"] != "agent-cli-7" {
		t.Errorf("agent_id = %v, want agent-cli-7", env["agent_id"])
	}
	if env["timestamp"] == nil || env["timestamp"] == "" {
		t.Errorf("timestamp missing: %v", env)
	}
}

// TestRecordSendCrossOrgDeniedHasDenialReason confirms the deny path
// populates denial_reason from errMsg via the inlined matcher.
func TestRecordSendCrossOrgDeniedHasDenialReason(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	m.SetConsentResolver(
		fakeConsentResolver{tier: "cross_org"},
		"u-self", "agent-self",
	)

	req := SendRequest{ToPeer: "peer-stranger"}
	m.recordSend(context.Background(), SessionMeta{SessionID: "s"}, req, nil,
		"denied", "denied", "scope mesh.peer_send required")

	recs := a.waitFor(t, 1)
	r := recs[0]
	if r.Tier != "cross_org" {
		t.Errorf("Tier = %q, want cross_org", r.Tier)
	}
	if r.DenialReason != "cross_org_boundary" {
		t.Errorf("DenialReason = %q, want cross_org_boundary", r.DenialReason)
	}
}

// TestRecordSendNoResolverStillRecordsBaseRow confirms back-compat:
// callers that don't wire SetConsentResolver still get a clean audit
// row (no tier, no envelope) — we never break the existing mesh__send
// path.
func TestRecordSendNoResolverStillRecordsBaseRow(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	// No SetConsentResolver call.

	req := SendRequest{ToPeer: "peer-x"}
	m.recordSend(context.Background(), SessionMeta{SessionID: "s"}, req, nil,
		"success", "", "")

	recs := a.waitFor(t, 1)
	if recs[0].Tier != "" {
		t.Errorf("Tier = %q, want empty (no resolver wired)", recs[0].Tier)
	}
	if recs[0].AcceptedBy != nil {
		t.Errorf("AcceptedBy = %s, want nil (no resolver wired)",
			string(recs[0].AcceptedBy))
	}
}
