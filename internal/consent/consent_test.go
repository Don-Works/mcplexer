package consent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAcceptedByMarshalAutoPair(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(AutoPair())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if got["kind"] != "auto_pair" {
		t.Fatalf("kind = %v, want auto_pair", got["kind"])
	}
	// auto_pair must NOT carry user_id / agent_id / timestamp — those
	// fields are reserved for the human envelope. The brief mandates
	// that the dashboard renders auto_pair distinctly.
	for _, k := range []string{"user_id", "agent_id", "timestamp"} {
		if _, present := got[k]; present {
			t.Errorf("auto_pair envelope unexpectedly carries %q: %v", k, got)
		}
	}
}

func TestAcceptedByMarshalHumanIsoTimestamp(t *testing.T) {
	t.Parallel()
	env := Human("u-alice", "agent-cli-7")
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Kind      string `json:"kind"`
		UserID    string `json:"user_id"`
		AgentID   string `json:"agent_id"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != "human" {
		t.Errorf("kind = %q, want human", got.Kind)
	}
	if got.UserID != "u-alice" || got.AgentID != "agent-cli-7" {
		t.Errorf("user_id / agent_id wrong: %+v", got)
	}
	// Must be ISO-8601 / RFC-3339 — that's the consent_audit contract.
	if _, err := time.Parse(time.RFC3339, got.Timestamp); err != nil {
		t.Errorf("timestamp %q is not RFC-3339: %v", got.Timestamp, err)
	}
}

func TestGrantOriginIsZero(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		g    GrantOrigin
		want bool
	}{
		{"empty", GrantOrigin{}, true},
		{"peer-only", GrantOrigin{PeerID: "peer-1"}, false},
		{"agent-only", GrantOrigin{AgentID: "agent-x"}, false},
		{"grant-only", GrantOrigin{GrantID: "g-1"}, false},
		{"full", GrantOrigin{PeerID: "p", AgentID: "a", GrantID: "g"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.g.IsZero(); got != tc.want {
				t.Errorf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnvelopeMarshalOmitsEmpty(t *testing.T) {
	t.Parallel()
	// Empty envelope → both AcceptedBy + GrantOrigin marshal to nil so
	// the audit row column lands NULL (not "{}" string).
	e := Envelope{}
	if got := e.MarshalAcceptedBy(); got != nil {
		t.Errorf("MarshalAcceptedBy on zero envelope = %s, want nil", string(got))
	}
	if got := e.MarshalGrantOrigin(); got != nil {
		t.Errorf("MarshalGrantOrigin on zero envelope = %s, want nil", string(got))
	}
}

func TestEnvelopeMarshalTier1(t *testing.T) {
	t.Parallel()
	e := Envelope{
		Tier:       TierSameUser,
		AcceptedBy: AutoPair(),
	}
	raw := e.MarshalAcceptedBy()
	if raw == nil {
		t.Fatal("MarshalAcceptedBy returned nil for Tier 1 envelope")
	}
	if !strings.Contains(string(raw), `"auto_pair"`) {
		t.Errorf("Tier 1 envelope missing auto_pair: %s", string(raw))
	}
	// No grant origin on Tier 1 — that's the silent-grant contract.
	if got := e.MarshalGrantOrigin(); got != nil {
		t.Errorf("Tier 1 unexpectedly carries grant_origin: %s", string(got))
	}
}

func TestEnvelopeMarshalTier2Human(t *testing.T) {
	t.Parallel()
	e := Envelope{
		Tier:        TierSameOrg,
		AcceptedBy:  Human("u-bob", "agent-9"),
		GrantOrigin: GrantOrigin{PeerID: "peer-a", AgentID: "agent-3", GrantID: "g-42"},
	}
	ab := e.MarshalAcceptedBy()
	if ab == nil {
		t.Fatal("MarshalAcceptedBy returned nil for Tier 2 envelope")
	}
	if !strings.Contains(string(ab), `"human"`) ||
		!strings.Contains(string(ab), `"u-bob"`) ||
		!strings.Contains(string(ab), `"agent-9"`) {
		t.Errorf("Tier 2 accepted_by missing required fields: %s", string(ab))
	}
	go_ := e.MarshalGrantOrigin()
	if go_ == nil {
		t.Fatal("Tier 2 envelope must carry grant_origin")
	}
	if !strings.Contains(string(go_), "peer-a") ||
		!strings.Contains(string(go_), "g-42") {
		t.Errorf("grant_origin missing fields: %s", string(go_))
	}
}

func TestNopResolverConservativeDefaults(t *testing.T) {
	t.Parallel()
	r := NopResolver{}
	ctx := context.Background()
	if got := r.TierFor(ctx, "any-peer"); got != TierCrossOrg {
		t.Errorf("NopResolver.TierFor = %q, want cross_org (most-restrictive default)", got)
	}
	if r.AutoPairAccepted(ctx, "any-peer") {
		t.Error("NopResolver.AutoPairAccepted must be false")
	}
	if got := r.GrantOriginFor(ctx, "any-peer", "any-scope"); !got.IsZero() {
		t.Errorf("NopResolver.GrantOriginFor must be zero: %+v", got)
	}
}
