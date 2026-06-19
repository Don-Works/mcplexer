package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// fakePeerApprover records calls and returns scripted results.
type fakePeerApprover struct {
	calls    int
	approved bool
	reason   string
	err      error
}

func (f *fakePeerApprover) Ask(
	_ context.Context, _ *store.ToolApproval, targets []string,
) (bool, string, error) {
	f.calls++
	if len(targets) == 0 {
		return false, "", ErrNoPeerTargets
	}
	return f.approved, f.reason, f.err
}

func TestPolicyResolver_Deny(t *testing.T) {
	r := &PolicyResolver{Policy: PolicyDeny}
	approved, reason, _, err := r.Resolve(context.Background(), &store.ToolApproval{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if approved {
		t.Error("expected denied")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestPolicyResolver_Queue(t *testing.T) {
	r := &PolicyResolver{Policy: PolicyQueue}
	_, _, _, err := r.Resolve(context.Background(), &store.ToolApproval{})
	if !errors.Is(err, ErrQueueRequested) {
		t.Fatalf("err = %v, want ErrQueueRequested", err)
	}
}

func TestPolicyResolver_TrustedAllow_Match(t *testing.T) {
	rule := store.ApprovalRule{
		ID:       "rule-1",
		Surface:  "mcp",
		Pattern:  "github__create_issue",
		Decision: "allow",
		Priority: 10,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}
	a := &store.ToolApproval{
		ID:       uuid.NewString(),
		ToolName: "github__create_issue",
		Surface:  "mcp",
	}
	approved, reason, ruleID, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !approved {
		t.Error("expected approved=true on matching rule")
	}
	if reason == "" {
		t.Error("expected reason set")
	}
	if ruleID != "rule-1" {
		t.Errorf("ruleID = %q, want rule-1", ruleID)
	}
}

// TestPolicyResolver_TrustedAllow_WildcardPattern verifies that pattern="*"
// matches any tool name on the correct surface — the canonical "allow + audit
// everything" rule the Shell Guard UI installs with one click.
// "*" is a single-segment glob (no slashes), which covers every shell tool
// name (bash, node, git, etc.) that the shell-guard surfaces. If tool names
// ever contain slashes, use "**" instead.
func TestPolicyResolver_TrustedAllow_WildcardPattern(t *testing.T) {
	rule := store.ApprovalRule{
		ID:       "wildcard-rule",
		Surface:  "shell",
		Pattern:  "*",
		Decision: "allow",
		Priority: 999,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}

	toolNames := []string{"bash", "node", "git", "npm", "python3", "make"}
	for _, name := range toolNames {
		t.Run(name, func(t *testing.T) {
			a := &store.ToolApproval{
				ID:       uuid.NewString(),
				ToolName: name,
				Surface:  "shell",
			}
			approved, _, ruleID, err := r.Resolve(context.Background(), a)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !approved {
				t.Errorf("expected wildcard * to approve %q", name)
			}
			if ruleID != "wildcard-rule" {
				t.Errorf("ruleID = %q, want wildcard-rule", ruleID)
			}
		})
	}
}

// TestPolicyResolver_TrustedAllow_RuleIDAttributed verifies the ruleID
// return is set to the winning rule's ID so the caller can write
// "rule:<id>" into approver_session_id for dashboard attribution.
func TestPolicyResolver_TrustedAllow_RuleIDAttributed(t *testing.T) {
	rule := store.ApprovalRule{
		ID:       "attr-rule-xyz",
		Surface:  "shell",
		Pattern:  "*",
		Decision: "allow",
		Priority: 10,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}
	a := &store.ToolApproval{
		ID:       uuid.NewString(),
		ToolName: "bash",
		Surface:  "shell",
	}
	_, _, ruleID, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ruleID != "attr-rule-xyz" {
		t.Errorf("ruleID = %q, want attr-rule-xyz", ruleID)
	}
}

// TestPolicyResolver_TrustedAllow_DenyRuleIDAttributed verifies the ruleID
// is returned even when the matching rule decision is "deny".
func TestPolicyResolver_TrustedAllow_DenyRuleIDAttributed(t *testing.T) {
	rule := store.ApprovalRule{
		ID:       "deny-rule-abc",
		Surface:  "shell",
		Pattern:  "*",
		Decision: "deny",
		Priority: 10,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}
	a := &store.ToolApproval{
		ID:       uuid.NewString(),
		ToolName: "bash",
		Surface:  "shell",
	}
	approved, _, ruleID, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if approved {
		t.Error("expected deny rule to produce approved=false")
	}
	if ruleID != "deny-rule-abc" {
		t.Errorf("ruleID = %q, want deny-rule-abc", ruleID)
	}
}

func TestPolicyResolver_TrustedAllow_NoMatchKeepsPending(t *testing.T) {
	rule := store.ApprovalRule{
		ID:       "rule-1",
		Surface:  "shell",
		Pattern:  "git*",
		Decision: "allow",
		Priority: 10,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}
	a := &store.ToolApproval{
		ID:       uuid.NewString(),
		ToolName: "github__create_issue", // wrong surface
		Surface:  "mcp",
	}
	approved, reason, ruleID, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if approved {
		t.Error("expected no decision (approved=false, reason=\"\")")
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
	if ruleID != "" {
		t.Errorf("ruleID = %q, want empty on no-match", ruleID)
	}
}

func TestPolicyResolver_TrustedAllow_PriorityWinsLowest(t *testing.T) {
	rules := []store.ApprovalRule{
		{ID: "low", Surface: "mcp", Pattern: "**", Decision: "deny", Priority: 100},
		{ID: "high", Surface: "mcp", Pattern: "github__x", Decision: "allow", Priority: 1},
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: rules}
	a := &store.ToolApproval{ID: uuid.NewString(), ToolName: "github__x", Surface: "mcp"}
	approved, _, _, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !approved {
		t.Error("expected lowest-priority allow rule to win")
	}
}

func TestPolicyResolver_TrustedAllow_DirectoryMatch(t *testing.T) {
	rule := store.ApprovalRule{
		ID:        "rule-1",
		Surface:   "mcp",
		Pattern:   "github__create_issue",
		Directory: "/repo/work",
		Decision:  "allow",
		Priority:  10,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}

	cases := []struct {
		name    string
		args    string
		wantApp bool
	}{
		{"exact match", `{"cwd":"/repo/work"}`, true},
		{"subpath", `{"cwd":"/repo/work/sub"}`, true},
		{"prefix-only-no-slash", `{"cwd":"/repo/working"}`, false},
		{"absent cwd skips match", `{}`, false},
		{"not json", `not json`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &store.ToolApproval{
				ID:        uuid.NewString(),
				ToolName:  "github__create_issue",
				Surface:   "mcp",
				Arguments: c.args,
			}
			approved, _, _, err := r.Resolve(context.Background(), a)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if approved != c.wantApp {
				t.Errorf("approved = %v, want %v", approved, c.wantApp)
			}
		})
	}
}

func TestPolicyResolver_TrustedAllow_SessionMatch(t *testing.T) {
	rule := store.ApprovalRule{
		ID:          "rule-1",
		Surface:     "mcp",
		Pattern:     "**",
		AISessionID: "sess-x",
		Decision:    "allow",
		Priority:    10,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}

	a := &store.ToolApproval{ID: uuid.NewString(), ToolName: "x", Surface: "mcp", RequestSessionID: "sess-x"}
	approved, _, _, _ := r.Resolve(context.Background(), a)
	if !approved {
		t.Error("expected match on session")
	}

	a.RequestSessionID = "sess-y"
	approved, _, _, _ = r.Resolve(context.Background(), a)
	if approved {
		t.Error("expected no match on different session")
	}
}

func TestPolicyResolver_TrustedAllow_ExpiredRule(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	rule := store.ApprovalRule{
		ID:        "rule-1",
		Surface:   "mcp",
		Pattern:   "**",
		Decision:  "allow",
		Priority:  10,
		ExpiresAt: &past,
	}
	r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{rule}}
	a := &store.ToolApproval{ID: uuid.NewString(), ToolName: "x", Surface: "mcp"}
	approved, _, _, _ := r.Resolve(context.Background(), a)
	if approved {
		t.Error("expired rule should not match")
	}
}

func TestPolicyResolver_MeshPeer_BubblesApproved(t *testing.T) {
	fake := &fakePeerApprover{approved: true, reason: "peer says yes"}
	r := &PolicyResolver{
		Policy:       PolicyMeshPeer,
		PeerApprover: fake,
		TrustedPeers: []string{"peer-A"},
	}
	approved, reason, ruleID, err := r.Resolve(context.Background(), &store.ToolApproval{ID: uuid.NewString()})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !approved || reason != "peer says yes" {
		t.Errorf("got approved=%v reason=%q", approved, reason)
	}
	if ruleID != "" {
		t.Errorf("ruleID = %q, want empty for mesh-peer policy", ruleID)
	}
	if fake.calls != 1 {
		t.Errorf("expected 1 peer call, got %d", fake.calls)
	}
}

func TestPolicyResolver_MeshPeer_TimeoutKeepsPending(t *testing.T) {
	fake := &fakePeerApprover{err: ErrPeerTimeout}
	r := &PolicyResolver{
		Policy:       PolicyMeshPeer,
		PeerApprover: fake,
		TrustedPeers: []string{"peer-A"},
	}
	approved, reason, _, err := r.Resolve(context.Background(), &store.ToolApproval{ID: uuid.NewString()})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if approved || reason != "" {
		t.Errorf("expected no-decision on timeout, got approved=%v reason=%q", approved, reason)
	}
}

func TestPolicyResolver_MeshPeer_NoPeers(t *testing.T) {
	fake := &fakePeerApprover{}
	r := &PolicyResolver{
		Policy:       PolicyMeshPeer,
		PeerApprover: fake,
		TrustedPeers: nil,
	}
	approved, reason, _, err := r.Resolve(context.Background(), &store.ToolApproval{ID: uuid.NewString()})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if approved {
		t.Error("expected denied with no peers")
	}
	if reason == "" {
		t.Error("expected reason on no-peers")
	}
	if fake.calls != 0 {
		t.Errorf("peer should not have been called")
	}
}

func TestPolicyResolver_UnknownPolicyTreatsAsQueue(t *testing.T) {
	r := &PolicyResolver{Policy: AFKPolicy("???")}
	_, _, _, err := r.Resolve(context.Background(), &store.ToolApproval{})
	if !errors.Is(err, ErrQueueRequested) {
		t.Fatalf("err = %v, want ErrQueueRequested", err)
	}
}

func TestPolicyResolver_HasAllowMetacharsMatch(t *testing.T) {
	wildcardWithFlag := store.ApprovalRule{
		ID:             "wild",
		Surface:        "shell",
		Pattern:        "*",
		Decision:       "allow",
		Priority:       999,
		AllowMetachars: true,
	}
	narrowWithoutFlag := store.ApprovalRule{
		ID:       "narrow",
		Surface:  "shell",
		Pattern:  "shell:git",
		Decision: "allow",
		Priority: 100,
	}
	denyWithFlag := store.ApprovalRule{
		ID:             "deny",
		Surface:        "shell",
		Pattern:        "shell:rm",
		Decision:       "deny",
		Priority:       1,
		AllowMetachars: true,
	}

	t.Run("matching wildcard with flag returns true", func(t *testing.T) {
		r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{wildcardWithFlag}}
		got := r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "shell", ToolName: "shell:ssh"})
		if !got {
			t.Fatal("want true on wildcard match")
		}
	})

	t.Run("matching narrow rule WITHOUT flag returns false", func(t *testing.T) {
		r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{narrowWithoutFlag}}
		got := r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "shell", ToolName: "shell:git"})
		if got {
			t.Fatal("want false: rule does not opt into metachar bypass")
		}
	})

	t.Run("deny rule with flag does not bypass", func(t *testing.T) {
		r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{denyWithFlag}}
		got := r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "shell", ToolName: "shell:rm"})
		if got {
			t.Fatal("want false: flag is only honoured on allow rules")
		}
	})

	t.Run("non-matching surface returns false", func(t *testing.T) {
		r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{wildcardWithFlag}}
		got := r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "mcp", ToolName: "anything"})
		if got {
			t.Fatal("want false: wildcard is for shell surface only")
		}
	})

	t.Run("non-TrustedAllow policy returns false", func(t *testing.T) {
		r := &PolicyResolver{Policy: PolicyDeny, Rules: []store.ApprovalRule{wildcardWithFlag}}
		got := r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "shell", ToolName: "shell:ssh"})
		if got {
			t.Fatal("want false: only TrustedAllow consults rule snapshot")
		}
	})

	t.Run("nil resolver / nil approval safe", func(t *testing.T) {
		var r *PolicyResolver
		if r.HasAllowMetacharsMatch(&store.ToolApproval{}) {
			t.Fatal("nil resolver should return false")
		}
		r = &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{wildcardWithFlag}}
		if r.HasAllowMetacharsMatch(nil) {
			t.Fatal("nil approval should return false")
		}
	})

	t.Run("expired rule with flag is ignored", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		expired := wildcardWithFlag
		expired.ID = "expired"
		expired.ExpiresAt = &past
		r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{expired}}
		got := r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "shell", ToolName: "shell:ssh"})
		if got {
			t.Fatal("want false: expired rules do not bypass")
		}
	})

	t.Run("read-only — hit_count is not bumped", func(t *testing.T) {
		hits := &countingHitRecorder{}
		r := &PolicyResolver{Policy: PolicyTrustedAllow, Rules: []store.ApprovalRule{wildcardWithFlag}}
		r.SetHitRecorder(hits)
		_ = r.HasAllowMetacharsMatch(&store.ToolApproval{Surface: "shell", ToolName: "shell:ssh"})
		if hits.calls != 0 {
			t.Fatalf("hit recorder fired %d times; probe must be read-only", hits.calls)
		}
	})
}

type countingHitRecorder struct {
	calls int
}

func (c *countingHitRecorder) IncrementHitCount(_ context.Context, _ string, _ time.Time) error {
	c.calls++
	return nil
}
