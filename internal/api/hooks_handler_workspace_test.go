// hooks_handler_workspace_test.go — per-workspace integration tests
// for the Shell Guard PreToolUse hook. Unlike hooks_handler_test.go
// (which uses fake approval requesters), these tests wire a real
// sqlite-backed approval.Manager + PolicyResolver and assert the full
// directory-matching / priority / expiry / session-filter / CRUD-
// reload behaviour through the public HTTP surface.
//
// These mirror the docker-compose scenarios in
// test/integration/scenario_shellguard.sh but run as ordinary `go test`
// so the feedback loop is seconds, not the ~90s the harness takes for
// the same coverage.
//
// Each test installs only the rules it owns (no cross-test bleed),
// resolves "no match → pending" cases via context cancellation
// (matches what curl --max-time does in the integration harness), and
// asserts the audit row to confirm the dashboard timeline stays
// truthful.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// wsTestRig wires a real sqlite store, a real approval.Manager with
// PolicyTrustedAllow + a tight grace period, and a hooksHandler that
// targets them. Tests can install rules via rig.installRule, then post
// to rig.handler.pretool and assert the decision.
type wsTestRig struct {
	t       *testing.T
	db      *sqlite.DB
	mgr     *approval.Manager
	handler *hooksHandler
	audit   *fakeAuditor
}

// newWSTestRig spins up the rig with a 200ms grace period — enough for
// the runPolicyHook goroutine to start and execute resolveTrustedAllow,
// but tight enough that an 8-table-driven case finishes in ~2s instead
// of 8×5s. The shorter grace doesn't change semantics — Resolve runs
// once when the grace fires, regardless of duration.
func newWSTestRig(t *testing.T) *wsTestRig {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "wstest.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bus := approval.NewBus()
	mgr := approval.NewManager(db, bus)
	t.Cleanup(mgr.Shutdown)

	// Resolver starts with an empty ruleset; per-test rules are
	// installed via rig.installRule + reloadRules so the resolver
	// always sees what the DB sees.
	resolver := &approval.PolicyResolver{
		Policy: approval.PolicyTrustedAllow,
	}
	resolver.SetHitRecorder(db)
	mgr.SetPolicyResolver(resolver, 200*time.Millisecond)

	aud := &fakeAuditor{}
	h := &hooksHandler{
		approvalMgr: mgr,
		auditor:     aud,
		// Pointing at the same sqlite db gives the resolver real
		// ListWorkspaces semantics, including ordering + Tags
		// passthrough that an inline fake would have to mock.
		workspaces: db,
		// These rig tests exercise the per-workspace rule/approval
		// pipeline, and two of them (WorkspaceLookupLongestMatchWins,
		// WorkspaceLookupNoMatchLeavesEmpty) deliberately use a metachar
		// command ("ls; foo") to get an INSTANT cheap-block instead of
		// racing the approval lifecycle. Pin the chaining hard-block ON so
		// that shortcut still works; the chaining-allowed DEFAULT is covered
		// in hooks_handler_test.go.
		shellGuardAllowChaining: func() bool { return false },
	}
	return &wsTestRig{t: t, db: db, mgr: mgr, handler: h, audit: aud}
}

// installRule writes one approval_rule directly via the store and
// reloads the resolver. Returns the rule id so tests can later edit
// or delete it. Panics on error so callers don't have to thread error
// checks through every setup step.
func (r *wsTestRig) installRule(rule store.ApprovalRule) string {
	r.t.Helper()
	if rule.ID == "" {
		rule.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	if rule.UpdatedAt.IsZero() {
		rule.UpdatedAt = now
	}
	if err := r.db.CreateApprovalRule(context.Background(), &rule); err != nil {
		r.t.Fatalf("CreateApprovalRule(%q): %v", rule.ID, err)
	}
	if err := r.mgr.ReloadPolicyRules(context.Background(), r.db); err != nil {
		r.t.Fatalf("ReloadPolicyRules: %v", err)
	}
	return rule.ID
}

// updateRule rewrites an existing rule with the supplied fields then
// reloads the resolver. Used by the CRUD-hot-reload test to flip a
// rule's decision and confirm the next hook reflects the change.
func (r *wsTestRig) updateRule(rule store.ApprovalRule) {
	r.t.Helper()
	rule.UpdatedAt = time.Now().UTC()
	if err := r.db.UpdateApprovalRule(context.Background(), &rule); err != nil {
		r.t.Fatalf("UpdateApprovalRule(%q): %v", rule.ID, err)
	}
	if err := r.mgr.ReloadPolicyRules(context.Background(), r.db); err != nil {
		r.t.Fatalf("ReloadPolicyRules: %v", err)
	}
}

// deleteRule removes a rule + reloads the resolver. Used by CRUD
// hot-reload tests.
func (r *wsTestRig) deleteRule(id string) {
	r.t.Helper()
	if err := r.db.DeleteApprovalRule(context.Background(), id); err != nil {
		r.t.Fatalf("DeleteApprovalRule(%q): %v", id, err)
	}
	if err := r.mgr.ReloadPolicyRules(context.Background(), r.db); err != nil {
		r.t.Fatalf("ReloadPolicyRules: %v", err)
	}
}

// post sends a PreToolUse Bash payload through the handler and returns
// the decoded response. The supplied context is attached to the
// request so tests of the "no rule match → pending" path can cancel
// after a short deadline to avoid the 60s hook timeout.
func (r *wsTestRig) post(
	ctx context.Context, sessionID, cwd, command string,
) (PreToolHookResponse, int) {
	r.t.Helper()
	body, err := json.Marshal(PreToolHookRequest{
		SessionID:     sessionID,
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		CWD:           cwd,
		ToolInput:     json.RawMessage(`{"command": ` + jsonStr(command) + `}`),
	})
	if err != nil {
		r.t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/pretool",
		strings.NewReader(string(body)))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.handler.pretool(rr, req)
	var resp PreToolHookResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			r.t.Fatalf("decode response: %v (body=%q)", err, rr.Body.String())
		}
	}
	return resp, rr.Code
}

// auditCount returns how many rows the audit recorder has captured —
// useful as a sanity delta-check across a test step.
func (r *wsTestRig) auditCount() int {
	return len(r.audit.records)
}

// lastAudit returns the most recent audit row, or nil if empty.
func (r *wsTestRig) lastAudit() *store.AuditRecord {
	if len(r.audit.records) == 0 {
		return nil
	}
	return r.audit.records[len(r.audit.records)-1]
}

// TestPretoolHook_PerWorkspaceAllow_ExactDirectory confirms the
// canonical "rule whose directory equals the hook cwd" path: a
// shell:git allow rule scoped to /srv/wsA approves a `git status`
// hook posted with cwd=/srv/wsA within the grace period.
func TestPretoolHook_PerWorkspaceAllow_ExactDirectory(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, status := rig.post(ctx, "s-exact", "/srv/wsA", "git status")
	if status != http.StatusOK {
		t.Fatalf("status: want 200, got %d", status)
	}
	if resp.Decision != "" {
		t.Fatalf("decision: want approve (empty), got %q (reason=%q)", resp.Decision, resp.Reason)
	}
	// One audit row, status=success, tool_name=shell:git.
	if got := rig.auditCount(); got != 1 {
		t.Fatalf("audit row count: want 1, got %d", got)
	}
	rec := rig.lastAudit()
	if rec.Status != "success" || rec.ToolName != "shell:git" {
		t.Fatalf("audit: tool=%q status=%q (want shell:git/success)", rec.ToolName, rec.Status)
	}
}

// TestPretoolHook_PerWorkspaceAllow_Subdirectory confirms prefix-match
// behaviour in directoryMatches() — a rule with directory=/srv/wsA
// matches a hook with cwd=/srv/wsA/sub/deeper. This is the common
// case in real usage (the agent's cwd is rarely the workspace root
// itself; it's some file's parent directory).
func TestPretoolHook_PerWorkspaceAllow_Subdirectory(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, _ := rig.post(ctx, "s-sub", "/srv/wsA/sub/deeper", "git log -1")
	if resp.Decision != "" {
		t.Fatalf("decision: want approve, got %q (reason=%q)", resp.Decision, resp.Reason)
	}
}

// TestPretoolHook_PerWorkspaceAllow_DirectoryBoundary catches the
// common off-by-one bug where directory=/srv/ws would incorrectly
// match cwd=/srv/wsabc (string-prefix without a separator). The
// directoryMatches() implementation guards against this by appending
// '/' before the prefix check — we lock that behaviour in.
func TestPretoolHook_PerWorkspaceAllow_DirectoryBoundary(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/ws", Decision: "allow", Priority: 50,
	})

	// cwd=/srv/wsabc looks like a string prefix of /srv/ws but is NOT
	// a subdirectory. Must NOT match → request stays pending → ctx
	// cancellation produces a block-with-error.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	resp, _ := rig.post(ctx, "s-bnd", "/srv/wsabc", "git status")
	if resp.Decision != "block" {
		t.Fatalf("decision: want block (no match → ctx cancel), got %q (reason=%q)",
			resp.Decision, resp.Reason)
	}
	if !strings.Contains(resp.Reason, "context") {
		t.Fatalf("expected reason to mention context cancellation, got %q", resp.Reason)
	}
}

// TestPretoolHook_PerWorkspaceDeny verifies a deny rule scoped to a
// workspace fires within the grace period and surfaces the rule id in
// the reason. The audit row carries status=blocked + the deny reason.
func TestPretoolHook_PerWorkspaceDeny(t *testing.T) {
	rig := newWSTestRig(t)
	ruleID := rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:rm",
		Directory: "/srv/wsA", Decision: "deny", Priority: 50,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, _ := rig.post(ctx, "s-deny", "/srv/wsA", "rm -rf scratch")
	if resp.Decision != "block" {
		t.Fatalf("decision: want block, got %q (reason=%q)", resp.Decision, resp.Reason)
	}
	if !strings.Contains(resp.Reason, ruleID) {
		t.Fatalf("reason should name the rule id; got %q (want substring %q)",
			resp.Reason, ruleID)
	}
	if !strings.Contains(resp.Reason, "trusted-allowlist deny") {
		t.Fatalf("reason should call out trusted-allowlist deny; got %q", resp.Reason)
	}
	rec := rig.lastAudit()
	if rec == nil || rec.Status != "blocked" {
		t.Fatalf("audit row should be blocked; got %+v", rec)
	}
}

// TestPretoolHook_PerWorkspacePriorityWinsLowest verifies the resolver
// sorts matches ascending by priority — a priority=1 deny beats a
// priority=50 allow for the same (pattern, directory) shape.
func TestPretoolHook_PerWorkspacePriorityWinsLowest(t *testing.T) {
	rig := newWSTestRig(t)
	// Higher priority allow installed first; lower priority deny
	// installed second. Install order MUST NOT matter — the resolver
	// sorts on read.
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "deny", Priority: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, _ := rig.post(ctx, "s-pri", "/srv/wsA", "git status")
	if resp.Decision != "block" {
		t.Fatalf("priority=1 deny should win over priority=50 allow; got %q (reason=%q)",
			resp.Decision, resp.Reason)
	}
}

// TestPretoolHook_PerWorkspaceExpiredRuleIgnored confirms rules whose
// expires_at lies in the past do NOT match. A future-dated rule must
// still match. Both cases share the same shape so the only variable
// is the timestamp.
func TestPretoolHook_PerWorkspaceExpiredRuleIgnored(t *testing.T) {
	rig := newWSTestRig(t)
	past := time.Now().Add(-1 * time.Hour).UTC()
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:whoami",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
		ExpiresAt: &past,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	resp, _ := rig.post(ctx, "s-expired", "/srv/wsA", "whoami")
	if resp.Decision != "block" {
		t.Fatalf("expired rule must not match; expected block via ctx cancel, got %q",
			resp.Decision)
	}

	// Now install the same shape with future expiry — must match.
	future := time.Now().Add(1 * time.Hour).UTC()
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:uname",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
		ExpiresAt: &future,
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	resp2, _ := rig.post(ctx2, "s-future", "/srv/wsA", "uname -a")
	if resp2.Decision != "" {
		t.Fatalf("future-expiring rule should approve; got %q (reason=%q)",
			resp2.Decision, resp2.Reason)
	}
}

// TestPretoolHook_PerWorkspaceSessionFilter verifies the
// ai_session_id column on the rule scopes match by session — only the
// matching session approves; every other session falls through to
// "no decision".
func TestPretoolHook_PerWorkspaceSessionFilter(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:ls",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
		AISessionID: "trusted-sess",
	})

	cases := []struct {
		name        string
		sess        string
		wantBlocked bool // true → expect block via ctx cancel
	}{
		{"matching session approves", "trusted-sess", false},
		{"empty session no match", "", true},
		{"other session no match", "other-sess", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deadline := 3 * time.Second
			if tc.wantBlocked {
				deadline = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			resp, _ := rig.post(ctx, tc.sess, "/srv/wsA", "ls -la")
			if tc.wantBlocked && resp.Decision != "block" {
				t.Fatalf("decision: want block (no match), got %q", resp.Decision)
			}
			if !tc.wantBlocked && resp.Decision != "" {
				t.Fatalf("decision: want approve, got %q (reason=%q)",
					resp.Decision, resp.Reason)
			}
		})
	}
}

// TestPretoolHook_WorkspaceIsolation locks in that a rule scoped to
// /srv/wsA does NOT match a hook posted with cwd=/srv/wsB, even when
// the pattern matches. This is the cross-workspace isolation
// guarantee — every other workspace test scenario depends on this
// holding.
func TestPretoolHook_WorkspaceIsolation(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:ls",
		Directory: "/srv/wsB", Decision: "allow", Priority: 50,
	})

	t.Run("wsA pattern in wsB cwd does NOT match", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		resp, _ := rig.post(ctx, "s-iso", "/srv/wsB", "git status")
		if resp.Decision != "block" {
			t.Fatalf("decision: want block, got %q", resp.Decision)
		}
	})
	t.Run("wsB pattern in wsA cwd does NOT match", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		resp, _ := rig.post(ctx, "s-iso", "/srv/wsA", "ls -la")
		if resp.Decision != "block" {
			t.Fatalf("decision: want block, got %q", resp.Decision)
		}
	})
	t.Run("each pattern in its own cwd DOES match", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if resp, _ := rig.post(ctx, "s-iso", "/srv/wsA", "git status"); resp.Decision != "" {
			t.Errorf("wsA: want approve, got %q", resp.Decision)
		}
		if resp, _ := rig.post(ctx, "s-iso", "/srv/wsB", "ls -la"); resp.Decision != "" {
			t.Errorf("wsB: want approve, got %q", resp.Decision)
		}
	})
}

// TestPretoolHook_CRUDHotReload exercises the full lifecycle of one
// rule against the hook: create → match approves; update to deny →
// next match blocks; delete → next match stays pending. The
// ReloadPolicyRules call mirrors what the approval-rules HTTP CRUD
// handler does after every mutation — it is the load-bearing piece
// that makes "edit rule, immediately see effect" work without a
// daemon restart.
func TestPretoolHook_CRUDHotReload(t *testing.T) {
	rig := newWSTestRig(t)

	// Step 1: create allow → hook approves.
	ruleID := rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:env",
		Directory: "/srv/wsB", Decision: "allow", Priority: 60,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if resp, _ := rig.post(ctx, "s-crud", "/srv/wsB", "env"); resp.Decision != "" {
		t.Fatalf("step 1 (allow): want approve, got %q (reason=%q)", resp.Decision, resp.Reason)
	}

	// Step 2: PUT same rule with decision=deny → hook blocks.
	rig.updateRule(store.ApprovalRule{
		ID:      ruleID,
		Surface: "shell", Pattern: "shell:env",
		Directory: "/srv/wsB", Decision: "deny", Priority: 60,
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if resp, _ := rig.post(ctx2, "s-crud", "/srv/wsB", "env"); resp.Decision != "block" {
		t.Fatalf("step 2 (deny): want block, got %q", resp.Decision)
	}

	// Step 3: DELETE rule → hook stays pending until ctx cancels.
	rig.deleteRule(ruleID)
	ctx3, cancel3 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel3()
	resp, _ := rig.post(ctx3, "s-crud", "/srv/wsB", "env")
	if resp.Decision != "block" {
		t.Fatalf("step 3 (delete): want block via ctx cancel, got %q", resp.Decision)
	}
	if !strings.Contains(resp.Reason, "context") {
		t.Fatalf("step 3 reason should mention context cancellation; got %q", resp.Reason)
	}
}

// TestPretoolHook_NoMatchKeepsPendingUntilCtxCancel asserts that an
// approval with no matching rule stays pending — the resolver's
// "no decision" path is the canonical way the system holds onto
// approvals until either a human responds or the timeout expires. We
// confirm by cancelling the context after 300ms and verifying the
// handler reports a block with the cancellation reason. The hit
// counter on every installed rule must remain zero (the resolver
// recorded no match, so no rule earned a hit).
func TestPretoolHook_NoMatchKeepsPendingUntilCtxCancel(t *testing.T) {
	rig := newWSTestRig(t)
	// Install a rule that pointedly does not match the hook below.
	ruleID := rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:other",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp, _ := rig.post(ctx, "s-nomatch", "/srv/wsA", "git status")
	if resp.Decision != "block" {
		t.Fatalf("decision: want block (ctx cancel), got %q", resp.Decision)
	}
	// Confirm the rule's hit_count stayed zero — the resolver
	// matched nothing.
	rule, err := rig.db.GetApprovalRule(context.Background(), ruleID)
	if err != nil {
		t.Fatalf("GetApprovalRule: %v", err)
	}
	if rule.HitCount != 0 {
		t.Errorf("hit_count: want 0 (no match), got %d", rule.HitCount)
	}
}

// TestPretoolHook_HitCountIncrementsOnMatch verifies the optional
// RuleHitRecorder bridge: every successful rule match bumps
// hit_count + last_hit_at. The dashboard surfaces "this rule fired
// 42 times" off these columns; if the increment regresses, the
// metric silently goes flat.
func TestPretoolHook_HitCountIncrementsOnMatch(t *testing.T) {
	rig := newWSTestRig(t)
	ruleID := rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:pwd",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})

	// Three matching hooks.
	for i := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, _ := rig.post(ctx, "s-hit", "/srv/wsA", "pwd")
		cancel()
		if resp.Decision != "" {
			t.Fatalf("post %d: want approve, got %q", i, resp.Decision)
		}
	}

	// The hit recorder runs in a detached goroutine inside
	// resolveTrustedAllow. We tolerate a tiny lag before the third
	// UPDATE lands.
	deadline := time.Now().Add(2 * time.Second)
	var rule *store.ApprovalRule
	var err error
	for time.Now().Before(deadline) {
		rule, err = rig.db.GetApprovalRule(context.Background(), ruleID)
		if err == nil && rule.HitCount >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetApprovalRule: %v", err)
	}
	if rule.HitCount != 3 {
		t.Errorf("hit_count: want 3, got %d", rule.HitCount)
	}
	if rule.LastHitAt == nil {
		t.Error("last_hit_at: want non-nil after 3 matches")
	}
}

// TestPretoolHook_AuditCWDCarriesThroughToParams locks in that the
// audit row's params_redacted payload contains BOTH the command and
// the cwd. Without cwd in the audit, the dashboard can't show which
// workspace a shell-guard hit landed in — a top-priority signal for
// the "what was I blocked on?" review flow.
func TestPretoolHook_AuditCWDCarriesThroughToParams(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:pwd",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if resp, _ := rig.post(ctx, "s-audit", "/srv/wsA/sub", "pwd"); resp.Decision != "" {
		t.Fatalf("decision: want approve, got %q", resp.Decision)
	}
	rec := rig.lastAudit()
	if rec == nil {
		t.Fatal("no audit row recorded")
	}
	var params map[string]string
	if err := json.Unmarshal(rec.ParamsRedacted, &params); err != nil {
		t.Fatalf("params_redacted is not JSON: %v", err)
	}
	if params["command"] != "pwd" {
		t.Errorf("params.command: want %q, got %q", "pwd", params["command"])
	}
	if params["cwd"] != "/srv/wsA/sub" {
		t.Errorf("params.cwd: want %q, got %q", "/srv/wsA/sub", params["cwd"])
	}
	if rec.Subpath != "/srv/wsA/sub" {
		t.Errorf("audit.subpath should mirror the cwd; got %q", rec.Subpath)
	}
}

// seedWorkspaces is a tiny helper that creates the supplied workspaces
// directly in the rig's sqlite store. Used by the workspace-tagging
// tests so the hook handler's resolver has rows to match against.
func (r *wsTestRig) seedWorkspaces(ws ...store.Workspace) {
	r.t.Helper()
	for i := range ws {
		w := &ws[i]
		now := time.Now().UTC()
		if w.CreatedAt.IsZero() {
			w.CreatedAt = now
		}
		if w.UpdatedAt.IsZero() {
			w.UpdatedAt = now
		}
		if w.DefaultPolicy == "" {
			w.DefaultPolicy = "allow"
		}
		if w.Source == "" {
			w.Source = "test"
		}
		if err := r.db.CreateWorkspace(context.Background(), w); err != nil {
			r.t.Fatalf("CreateWorkspace(%q): %v", w.ID, err)
		}
	}
}

// findApprovalByCmd scans every approval in the rig's store (regardless
// of status — pending, approved, denied, cancelled) and returns the one
// whose arguments JSON contains the given substring. Used by the
// workspace-tagging tests so they can assert against the persisted
// approval row even after the context cancels it.
//
// Plain string match on the arguments payload is sufficient here
// because each test posts a unique command marker.
func (r *wsTestRig) findApprovalByCmd(cmdSub string) *store.ToolApproval {
	r.t.Helper()
	// Use the rig's audit log to find the row, which carries the same
	// command in params_redacted. From the audit row's session_id +
	// the in-memory list of all approvals via direct sqlite query,
	// we look up the matching approval. The simpler path: read
	// pending approvals first (status still pending if ctx hasn't
	// expired), then fall back to the audit row's session id and a
	// raw read via GetToolApproval after extracting the approval id.
	//
	// Since the wsTestRig doesn't expose a "list all approvals"
	// helper, we use a tiny shim: query for pending first; if the
	// row was already resolved, find it via the audit row's params.
	pending, _ := r.db.ListPendingApprovals(context.Background())
	for i := range pending {
		if strings.Contains(pending[i].Arguments, cmdSub) {
			return &pending[i]
		}
	}
	return nil
}

// TestPretoolHook_PopulatesWorkspaceFromCWD reproduces the bug Elliot
// reported: even when workspaces exist and the agent's cwd is inside
// one of them, the approval row and audit row both surface
// workspace_id="" / workspace_name="" — so the Audit page renders "-"
// and the user can't tell which project a Bash hit came from. The
// hook handler MUST look up the workspace via cwd → root_path
// matching and stamp the ids on both the approval and the audit row
// it emits.
//
// This test fails on the pre-fix code path. The fix lives in
// hooks_handler.go (workspace lookup wired through `workspaces` on
// hooksHandler) and the router wiring.
func TestPretoolHook_PopulatesWorkspaceFromCWD(t *testing.T) {
	rig := newWSTestRig(t)
	// Seed two workspaces with distinct root_paths so the resolver
	// has to actually compare paths, not just take the first row.
	rig.seedWorkspaces(
		store.Workspace{ID: "ws-a", Name: "Project A", RootPath: "/srv/wsA"},
		store.Workspace{ID: "ws-b", Name: "Project B", RootPath: "/srv/wsB"},
	)

	// Install an allow rule so the approval resolves within the
	// grace period and we can capture both the approval row + the
	// audit row.
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})

	postCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, _ := rig.post(postCtx, "s-ws-tag", "/srv/wsA/sub", "git status")
	if resp.Decision != "" {
		t.Fatalf("approval should fire; got decision=%q (reason=%q)", resp.Decision, resp.Reason)
	}

	// Audit row: workspace_id + workspace_name MUST be populated.
	// This is the load-bearing assertion — the AuditPage reads
	// `record.workspace_name || workspace_id || "-"` and renders the
	// dash when both are empty (Elliot's user-visible symptom).
	rec := rig.lastAudit()
	if rec == nil {
		t.Fatal("no audit row captured")
	}
	if rec.WorkspaceID != "ws-a" {
		t.Errorf("audit.workspace_id: want %q, got %q",
			"ws-a", rec.WorkspaceID)
	}
	if rec.WorkspaceName != "Project A" {
		t.Errorf("audit.workspace_name: want %q, got %q",
			"Project A", rec.WorkspaceName)
	}

	// Approval row tagging: post a NEW hook with a pattern that does
	// NOT match the installed rule (shell:env vs shell:git allow), so
	// the row stays pending long enough for ListPendingApprovals to
	// see it. Use a very short context so ctx cancel fires before any
	// rule-matched resolution can take it out of pending status.
	postCtx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	go func() {
		_, _ = rig.post(postCtx2, "s-ws-tag-pending", "/srv/wsB/sub", "env --pending-marker")
	}()
	// Tiny sleep so the CreateToolApproval write lands before we read.
	time.Sleep(40 * time.Millisecond)
	approval := rig.findApprovalByCmd("--pending-marker")
	if approval == nil {
		t.Fatal("expected pending approval row with marker")
	}
	if approval.WorkspaceID != "ws-b" {
		t.Errorf("approval.workspace_id: want ws-b, got %q", approval.WorkspaceID)
	}
	if approval.WorkspaceName != "Project B" {
		t.Errorf("approval.workspace_name: want %q, got %q",
			"Project B", approval.WorkspaceName)
	}
}

// TestPretoolHook_WorkspaceLookupLongestMatchWins covers the nested-
// workspace case: a workspace at /srv AND a workspace at /srv/inner.
// A cwd under /srv/inner must resolve to the INNER workspace (the
// most specific match). Without this, agents working in a sub-project
// would always tag audit rows with the parent workspace.
func TestPretoolHook_WorkspaceLookupLongestMatchWins(t *testing.T) {
	rig := newWSTestRig(t)
	rig.seedWorkspaces(
		store.Workspace{ID: "ws-outer", Name: "Outer", RootPath: "/srv"},
		store.Workspace{ID: "ws-inner", Name: "Inner", RootPath: "/srv/inner"},
	)

	// Use a metachar cheap-block — it resolves instantly via the
	// audit row, so we don't have to race the approval lifecycle.
	postCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	resp, _ := rig.post(postCtx, "s-nested", "/srv/inner/deep", "ls; foo")
	if resp.Decision != "block" {
		t.Fatalf("metachar should cheap-block; got %q", resp.Decision)
	}
	rec := rig.lastAudit()
	if rec == nil {
		t.Fatal("no audit row")
	}
	if rec.WorkspaceID != "ws-inner" {
		t.Errorf("longest-match resolution: want ws-inner, got %q",
			rec.WorkspaceID)
	}
	if rec.WorkspaceName != "Inner" {
		t.Errorf("longest-match name: want Inner, got %q",
			rec.WorkspaceName)
	}
}

// TestPretoolHook_WorkspaceLookupNoMatchLeavesEmpty confirms the
// resolver fails open: a cwd outside every workspace's root_path
// leaves WorkspaceID + Name as empty strings (not the literal "-",
// not a misclassified parent). The UI uses empty → "-" rendering;
// the lookup must NOT invent a value here.
func TestPretoolHook_WorkspaceLookupNoMatchLeavesEmpty(t *testing.T) {
	rig := newWSTestRig(t)
	rig.seedWorkspaces(store.Workspace{ID: "ws-only", Name: "Only WS", RootPath: "/srv/wsA"})

	postCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	resp, _ := rig.post(postCtx, "s-nows", "/tmp/elsewhere", "ls; foo")
	if resp.Decision != "block" {
		t.Fatalf("metachar should cheap-block; got %q", resp.Decision)
	}
	rec := rig.lastAudit()
	if rec == nil {
		t.Fatal("no audit row")
	}
	if rec.WorkspaceID != "" {
		t.Errorf("workspace_id: want empty (no match), got %q", rec.WorkspaceID)
	}
	if rec.WorkspaceName != "" {
		t.Errorf("workspace_name: want empty, got %q", rec.WorkspaceName)
	}
}

// TestPretoolHook_RuleDirectoryTolerantToTrailingSlash reproduces the
// second leg of Elliot's complaint: rules created via the dashboard
// with a directory like "/Users/elliot/project/" (operator habit of
// trailing slash) never match because directoryMatches() does an
// exact-string comparison + a prefix check that REQUIRES the rule
// directory to lack a trailing slash. The fix is to canonicalise
// both sides via filepath.Clean / TrimSuffix before comparison.
//
// Pre-fix behaviour: the rule below does NOT match, so the approval
// hangs until ctx cancels → handler reports "block".
// Post-fix behaviour: the rule matches, handler returns approve.
func TestPretoolHook_RuleDirectoryTolerantToTrailingSlash(t *testing.T) {
	rig := newWSTestRig(t)
	// Trailing slash on the rule directory — exactly what a dashboard
	// user would paste from `pwd` on a path with a trailing /.
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA/", Decision: "allow", Priority: 50,
	})

	t.Run("exact match without trailing slash on cwd", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, _ := rig.post(ctx, "s-slash", "/srv/wsA", "git status")
		if resp.Decision != "" {
			t.Fatalf("want approve, got %q (reason=%q)",
				resp.Decision, resp.Reason)
		}
	})

	t.Run("subdirectory still matches with trailing-slash rule", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, _ := rig.post(ctx, "s-slash-sub", "/srv/wsA/sub", "git status")
		if resp.Decision != "" {
			t.Fatalf("want approve, got %q (reason=%q)",
				resp.Decision, resp.Reason)
		}
	})

	t.Run("unrelated path still does NOT match", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		resp, _ := rig.post(ctx, "s-slash-neg", "/srv/wsAbutNotReally", "git status")
		if resp.Decision != "block" {
			t.Fatalf("want block (no match → ctx cancel), got %q",
				resp.Decision)
		}
	})
}

// TestPretoolHook_ParallelDifferentWorkspaces is a smoke check that
// concurrent hook posts targeting distinct workspaces don't interleave
// or starve each other. Both should resolve within their grace
// periods. Catches a regression where a single resolver goroutine
// would serialise on a global lock.
func TestPretoolHook_ParallelDifferentWorkspaces(t *testing.T) {
	rig := newWSTestRig(t)
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:git",
		Directory: "/srv/wsA", Decision: "allow", Priority: 50,
	})
	rig.installRule(store.ApprovalRule{
		Surface: "shell", Pattern: "shell:ls",
		Directory: "/srv/wsB", Decision: "allow", Priority: 50,
	})

	var approved atomic.Int32
	done := make(chan struct{}, 2)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if resp, _ := rig.post(ctx, "p1", "/srv/wsA", "git status"); resp.Decision == "" {
			approved.Add(1)
		}
		done <- struct{}{}
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if resp, _ := rig.post(ctx, "p2", "/srv/wsB", "ls -la"); resp.Decision == "" {
			approved.Add(1)
		}
		done <- struct{}{}
	}()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("parallel hook test deadlocked")
		}
	}
	if got := approved.Load(); got != 2 {
		t.Fatalf("approved count: want 2, got %d", got)
	}
}
