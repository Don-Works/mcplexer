package approval

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// resolution carries the outcome of an approval decision.
type resolution struct {
	Approved bool
	Reason   string
}

// NotifyPublisher is the narrow surface of *notify.Bus the manager uses
// to push pending/resolved approvals into the Signal tray. Defined as an
// interface so the approval package doesn't depend on the notify package
// (would cause an import cycle through serve.go wiring) and so unit
// tests can inject a fake without spinning up the SSE infrastructure.
type NotifyPublisher interface {
	// link is the in-app destination the Signal tray + OS notification
	// should route to when clicked. Empty falls back to the dashboard's
	// kind-based destinationFor() (lands on /approvals for approval_*).
	Publish(messageID, agentName, role, kind, priority, title, body, tags, link string)
}

// Manager coordinates tool call approval requests and their resolution.
type Manager struct {
	store   store.ToolApprovalStore
	bus     *Bus
	notify  NotifyPublisher // optional; nil disables Signal-tray notifications
	mu      sync.Mutex
	pending map[string]chan resolution // keyed by approval ID

	// policy + gracePeriod are an additive AFK-routing hook (M5). When
	// policy is non-nil, each new approval spawns a delayed goroutine
	// that, after gracePeriod, asks the resolver what to do for any
	// still-pending request. Set via SetPolicyResolver. nil disables
	// AFK routing — the pre-M5 behaviour.
	policy      *PolicyResolver
	gracePeriod time.Duration

	// dangerousMode is a function the daemon installs at boot pointing
	// at `settingsSvc.Load(ctx).DangerousModeEnabled`. When it returns
	// true, every approval auto-resolves as approved with a "dangerous-
	// mode bypass" marker. Audit + notify still fire so the post-hoc
	// review pipeline can reconstruct exactly what would have been gated.
	// nil = always-false (the historical behaviour).
	dangerousMode func() bool
}

// NewManager creates a new approval manager.
func NewManager(s store.ToolApprovalStore, bus *Bus) *Manager {
	return &Manager{
		store:   s,
		bus:     bus,
		pending: make(map[string]chan resolution),
	}
}

// SetNotifyPublisher wires the Signal-tray bridge. When set, every
// pending and resolved approval also emits a notify.Event so the
// dashboard's persistent notification tray, OS notification path, and
// SSE subscribers all light up. Nil disables the bridge (the pre-fix
// behaviour where approvals only fired the approvals SSE stream that
// the tray doesn't subscribe to).
func (m *Manager) SetNotifyPublisher(n NotifyPublisher) {
	m.mu.Lock()
	m.notify = n
	m.mu.Unlock()
}

// notifierLocked returns the current publisher; caller must NOT hold mu.
func (m *Manager) notifier() NotifyPublisher {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.notify
}

// SetDangerousModeProvider installs an accessor for the runtime
// "dangerous mode" toggle. When the provider returns true,
// RequestApproval auto-resolves as approved with Resolution=
// "dangerous-mode bypass" instead of persisting + blocking on a human.
// The accessor is hot-path — the daemon wires it to a thin lambda that
// snapshots settingsSvc.Load(ctx).DangerousModeEnabled, so no context /
// import dependency from this package on config. Passing nil clears.
func (m *Manager) SetDangerousModeProvider(f func() bool) {
	m.mu.Lock()
	m.dangerousMode = f
	m.mu.Unlock()
}

// dangerousModeActive returns true when a provider is installed and
// reports true. Wrapped to centralise the nil-check + lock dance and to
// keep RequestApproval readable.
func (m *Manager) dangerousModeActive() bool {
	m.mu.Lock()
	f := m.dangerousMode
	m.mu.Unlock()
	if f == nil {
		return false
	}
	return f()
}

// SetPolicyResolver installs the AFK policy resolver. After Set, any
// approval that can't immediately resolve will, after a short grace
// period (default 5s — the human gets a chance), invoke the resolver
// before normal-timeout kicks in. Passing nil disables AFK routing.
func (m *Manager) SetPolicyResolver(resolver *PolicyResolver, gracePeriod time.Duration) {
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Second
	}
	m.mu.Lock()
	m.policy = resolver
	m.gracePeriod = gracePeriod
	m.mu.Unlock()
}

// ReloadPolicyRules pulls every approval_rule from the store and atomically
// installs them on the active PolicyResolver. Called from the approval-rules
// HTTP CRUD handlers after every create/update/delete so a user editing
// rules in the dashboard sees them take effect immediately, without a
// daemon restart. No-op when no resolver has been installed yet.
type RuleLister interface {
	ListApprovalRules(ctx context.Context, surface string) ([]store.ApprovalRule, error)
}

func (m *Manager) ReloadPolicyRules(ctx context.Context, lister RuleLister) error {
	m.mu.Lock()
	resolver := m.policy
	m.mu.Unlock()
	if resolver == nil {
		return nil
	}
	rules, err := lister.ListApprovalRules(ctx, "")
	if err != nil {
		return err
	}
	resolver.SetRules(rules)
	return nil
}

// PolicyResolverHandle exposes the installed resolver (or nil) for use
// by helpers like the HTTP CRUD that want to install a hit recorder
// without poking at Manager internals.
func (m *Manager) PolicyResolverHandle() *PolicyResolver {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policy
}

// HasAllowMetacharsMatch is the read-only probe used by the shell hook
// to decide whether to short-circuit its cheap-block on shell
// metacharacters. Delegates to the policy resolver — returns false when
// no resolver is installed. Safe to call from any goroutine.
func (m *Manager) HasAllowMetacharsMatch(a *store.ToolApproval) bool {
	resolver := m.PolicyResolverHandle()
	if resolver == nil {
		return false
	}
	return resolver.HasAllowMetacharsMatch(a)
}

// runPolicyHook is invoked from RequestApproval after the pending row
// is registered. It sleeps for the grace period, then — if the request
// is still pending — asks the resolver and, when the resolver returns
// a decision, feeds it back through the normal Resolve path. Bails
// silently on cancel/shutdown.
func (m *Manager) runPolicyHook(ctx context.Context, a *store.ToolApproval) {
	m.mu.Lock()
	resolver := m.policy
	grace := m.gracePeriod
	m.mu.Unlock()
	if resolver == nil {
		return
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(grace):
	}

	m.mu.Lock()
	_, stillPending := m.pending[a.ID]
	m.mu.Unlock()
	if !stillPending {
		return
	}

	approved, reason, ruleID, err := resolver.Resolve(ctx, a)
	if err != nil {
		if errors.Is(err, ErrQueueRequested) {
			return
		}
		slog.Warn("policy resolver failed", "id", a.ID, "err", err)
		return
	}
	if !approved && reason == "" {
		// No decision; keep pending.
		return
	}
	// approverSessionID encodes WHO made the call for attribution in the
	// audit timeline: "rule:<id>" for a matched allowlist rule,
	// "agent:<peer>" for a mesh-peer decision, or "system" for everything
	// else (timeout, no-peers, dangerous-mode bypass).
	approverSID := "system"
	if ruleID != "" {
		approverSID = "rule:" + ruleID
	}
	if err := m.Resolve(a.ID, approverSID, "afk-policy", reason, approved); err != nil {
		if !errors.Is(err, ErrAlreadyResolved) {
			slog.Warn("policy resolve failed", "id", a.ID, "err", err)
		}
	}
}

// RequestApproval persists an approval record and blocks until it is
// resolved, times out, or the context is cancelled. Returns true if approved.
//
// When dangerous mode is active (see SetDangerousModeProvider) the
// approval is short-circuited: it lands in the store as already-approved
// with Resolution="dangerous-mode bypass", the audit + notify pipeline
// still fires (so the dashboard reflects what was bypassed and a follow-
// up review can reconstruct the timeline), and the method returns
// (true, nil) immediately — no pending entry, no policy hook, no timer.
//
// When a PolicyResolver is installed and the approval matches a
// deterministic rule (TrustedAllow allow/deny, or PolicyDeny), the
// request is resolved synchronously in one step: only the "resolved"
// notification fires, no transient "Approval requested" flash. The
// grace-period + goroutine path is reserved for cases that genuinely
// need a human window (PolicyQueue, PolicyMeshPeer round-trips, or no
// resolver at all).
func (m *Manager) RequestApproval(ctx context.Context, a *store.ToolApproval) (bool, error) {
	if m.dangerousModeActive() {
		return m.bypassAsApproved(ctx, a)
	}

	if approved, reason, ruleID, decided := m.tryResolveSync(ctx, a); decided {
		return m.recordPreDecided(ctx, a, approved, reason, ruleID)
	}

	a.Arguments = redactApprovalArguments(a.Arguments)
	if err := m.store.CreateToolApproval(ctx, a); err != nil {
		return false, err
	}

	ch := make(chan resolution, 1)
	m.mu.Lock()
	m.pending[a.ID] = ch
	m.mu.Unlock()

	if m.bus != nil {
		m.bus.Publish(ApprovalEvent{Type: "pending", Approval: a})
	}
	if n := m.notifier(); n != nil {
		// Surface differentiates shell-guard (Bash hook) hits from MCP
		// tool calls in the tray; the title leads with surface so a
		// burst of shell hits doesn't look like spammed MCP calls.
		title := approvalTitle(a, "Approval requested")
		n.Publish(a.ID, "mcplexer", "approval", "approval_pending",
			"high", title, a.Justification, "approval,"+a.Surface,
			"/approvals?selected="+a.ID)
	}

	// Additive M5 hook: when a PolicyResolver is installed, fire a
	// detached goroutine that may auto-decide this approval after a
	// short grace period. No effect when policy is nil.
	go m.runPolicyHook(ctx, a)

	timeout := time.Duration(a.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	timer := time.AfterFunc(timeout, func() {
		m.mu.Lock()
		if _, ok := m.pending[a.ID]; ok {
			delete(m.pending, a.ID)
			m.mu.Unlock()

			if err := m.store.ResolveToolApproval(
				context.Background(), a.ID, "timeout", "", "system", "timed out",
			); err != nil {
				slog.Warn("failed to expire approval", "id", a.ID, "err", err)
			}
			a.Status = "timeout"
			if m.bus != nil {
				m.bus.Publish(ApprovalEvent{Type: "resolved", Approval: a})
			}
			ch <- resolution{Approved: false, Reason: "timed out"}
		} else {
			m.mu.Unlock()
		}
	})
	defer timer.Stop()

	select {
	case res := <-ch:
		// Surface the resolution back to the caller's struct. Resolve()
		// mutates a freshly-fetched copy inside its own scope; the
		// caller's pointer here only sees Status="pending" /
		// Resolution="" unless we copy the verdict across explicitly.
		// Without this, callers like the shell-guard hook handler
		// fall back to the literal string "pending" as the block
		// reason, which lands in Claude Code's UI as a confusing
		// non-explanation. The timeout + ctx.Done() branches below
		// already do the equivalent mutation; this brings the
		// success branch into line.
		if res.Approved {
			a.Status = "approved"
		} else {
			a.Status = "denied"
		}
		a.Resolution = res.Reason
		return res.Approved, nil
	case <-ctx.Done():
		m.mu.Lock()
		if _, ok := m.pending[a.ID]; ok {
			delete(m.pending, a.ID)
			m.mu.Unlock()
			_ = m.store.ResolveToolApproval(
				context.Background(), a.ID, "cancelled", "", "system", "client disconnected",
			)
			a.Status = "cancelled"
			if m.bus != nil {
				m.bus.Publish(ApprovalEvent{Type: "resolved", Approval: a})
			}
		} else {
			m.mu.Unlock()
		}
		return false, ctx.Err()
	}
}

// bypassAsApproved short-circuits a normal RequestApproval call when
// dangerous mode is active. The approval row is created + resolved
// in one step (so it shows up in the audit timeline as "approved" with
// the bypass marker), the bus and notify subscribers see only the
// "resolved" event (no transient "Approval requested" flash for traffic
// the operator pre-blessed via dangerous mode), and the caller gets
// (true, nil) without ever blocking.
//
// Failures from the underlying store are surfaced — if we can't even
// record the bypass, returning (true, nil) would silently lose the
// audit trail, which is the whole point of keeping audit alive in
// dangerous mode. Better to fail loud.
func (m *Manager) bypassAsApproved(ctx context.Context, a *store.ToolApproval) (bool, error) {
	const reason = "dangerous-mode bypass"
	// Persist as pending first so the unique-id row exists for Resolve
	// to flip — every store implementation (sqlite + memStore in tests)
	// rejects ResolveToolApproval on rows that aren't pending. We then
	// immediately resolve it to "approved" in the same call path so
	// the audit timeline shows the row as approved with the bypass
	// marker.
	a.Status = "pending"
	a.Arguments = redactApprovalArguments(a.Arguments)
	if err := m.store.CreateToolApproval(ctx, a); err != nil {
		return false, err
	}
	if err := m.store.ResolveToolApproval(
		ctx, a.ID, "approved", "dangerous-mode", "system", reason,
	); err != nil {
		// Row exists but resolve failed — surface so the operator
		// notices something's wrong with the bypass path.
		return false, err
	}
	// Mirror the resolved state onto the caller's struct so callers
	// inspecting `a` after RequestApproval see the bypass marker.
	a.Status = "approved"
	a.ApproverType = "system"
	a.ApproverSessionID = "dangerous-mode"
	a.Resolution = reason

	if m.bus != nil {
		// Resolved only — the old pending intermezzo fired an
		// "Approval requested" flash for every dangerous-mode call,
		// then the front-end's message_id dedup dropped the resolved
		// follow-up so the tray row never visibly flipped to
		// "approved". Skipping the pending publish kills the noise at
		// the source.
		m.bus.Publish(ApprovalEvent{Type: "resolved", Approval: a})
	}
	if n := m.notifier(); n != nil {
		title := approvalTitle(a, "Dangerous-mode bypass")
		body := a.Justification
		if body == "" {
			body = reason
		}
		// Priority "low" — the user pre-blessed every approval by
		// flipping dangerous mode on; the resolution is informational
		// for the audit timeline, not actionable.
		n.Publish(a.ID, "mcplexer", "approval", "approval_approved",
			"low", title, body, "approval,"+a.Surface+",dangerous-mode",
			"/approvals?selected="+a.ID)
	}
	return true, nil
}

// tryResolveSync attempts to decide an approval up-front, before the
// pending row is published. Returns decided=true only for deterministic
// policies that don't need a human window or network round-trip —
// PolicyTrustedAllow rule hits and PolicyDeny. PolicyMeshPeer (network
// I/O) and PolicyQueue (intentionally pending) fall through to the
// async goroutine path.
//
// The 5s grace period was originally there so a human could click
// approve/deny before a pre-configured rule fired. But the user has
// already decided when they wrote the rule — the grace window just
// generates a spurious "Approval requested" flash for routine traffic
// and a duplicate "Approval approved" event that the dashboard's
// message_id dedup silently drops.
func (m *Manager) tryResolveSync(
	ctx context.Context, a *store.ToolApproval,
) (approved bool, reason string, ruleID string, decided bool) {
	m.mu.Lock()
	resolver := m.policy
	m.mu.Unlock()
	if resolver == nil {
		return false, "", "", false
	}
	switch resolver.Policy {
	case PolicyTrustedAllow, PolicyDeny:
		// Both are local + deterministic — safe to evaluate inline.
	default:
		// PolicyMeshPeer + PolicyQueue + unknown stay on the async
		// path: mesh-peer needs a network round-trip, queue is
		// intentionally pending, unknown is treated as queue.
		return false, "", "", false
	}
	approved, reason, ruleID, err := resolver.Resolve(ctx, a)
	if err != nil {
		// ErrQueueRequested wouldn't reach here (PolicyQueue is
		// filtered above); any other error means "no decision now" —
		// fall through to the async path which will retry under
		// runPolicyHook.
		return false, "", "", false
	}
	if !approved && reason == "" {
		// TrustedAllow no-match branch — keep pending, wait for human.
		return false, "", "", false
	}
	return approved, reason, ruleID, true
}

// recordPreDecided persists an approval that's already-decided (sync
// rule hit or PolicyDeny) and emits only a "resolved" notification. The
// row lands in the store with the correct final status in one shot, so
// the dashboard's pending feed never sees it appear — which is the
// whole point: pre-configured rules should be silent.
func (m *Manager) recordPreDecided(
	ctx context.Context, a *store.ToolApproval,
	approved bool, reason, ruleID string,
) (bool, error) {
	status := "denied"
	verb := "denied"
	if approved {
		status = "approved"
		verb = "approved"
	}
	approverSID := "system"
	if ruleID != "" {
		approverSID = "rule:" + ruleID
	}

	// Persist as pending first so ResolveToolApproval (which rejects
	// non-pending rows in every store implementation) can flip it. No
	// other goroutine can see the row between these two writes — the
	// row's ID hasn't been published to any subscriber yet — so the
	// two-step write is atomic from the caller's perspective.
	a.Status = "pending"
	a.Arguments = redactApprovalArguments(a.Arguments)
	if err := m.store.CreateToolApproval(ctx, a); err != nil {
		return false, err
	}
	if err := m.store.ResolveToolApproval(
		ctx, a.ID, status, approverSID, "afk-policy", reason,
	); err != nil {
		return false, err
	}
	a.Status = status
	a.ApproverType = "afk-policy"
	a.ApproverSessionID = approverSID
	a.Resolution = reason

	if m.bus != nil {
		m.bus.Publish(ApprovalEvent{Type: "resolved", Approval: a})
	}
	if n := m.notifier(); n != nil {
		title := approvalTitle(a, "Approval "+verb)
		body := reason
		if body == "" {
			body = "by afk-policy"
		}
		// Priority "low" — the operator pre-configured this outcome
		// with a rule. The resolution is informational for the audit
		// timeline, not a call to action.
		n.Publish(a.ID, "mcplexer", "approval", "approval_"+verb,
			"low", title, body, "approval,"+a.Surface+",pre-decided",
			"/approvals?selected="+a.ID)
	}
	return approved, nil
}

// Resolve approves or denies a pending approval. Self-approval (resolver
// session matching requester session) is rejected regardless of approver
// type — previously the "dashboard" type short-circuited this check, which
// allowed a malicious caller holding an API token to self-approve a tool
// call from the same MCP session.
func (m *Manager) Resolve(
	id, approverSessionID, approverType, reason string, approved bool,
) error {
	if approverType == "" {
		return ErrApproverTypeRequired
	}
	if approverType == "dashboard" && approverSessionID == "" {
		// Dashboard resolves must carry an identifier so they can be
		// distinguished from the requesting MCP session. The HTTP handler
		// derives this from the auth cookie / bearer token.
		return ErrApproverIdentityRequired
	}

	// Look up the approval to check self-approval.
	a, err := m.store.GetToolApproval(context.Background(), id)
	if err != nil {
		return err
	}
	if a.Status != "pending" {
		return ErrAlreadyResolved
	}

	// Prevent self-approval across all approver types: if the resolver is
	// the same session that requested the tool call, reject.
	if approverSessionID != "" && approverSessionID == a.RequestSessionID {
		return ErrSelfApproval
	}

	status := "denied"
	if approved {
		status = "approved"
	}

	if err := m.store.ResolveToolApproval(
		context.Background(), id, status, approverSessionID, approverType, reason,
	); err != nil {
		return err
	}

	a.Status = status
	a.ApproverSessionID = approverSessionID
	a.ApproverType = approverType
	a.Resolution = reason

	// Signal the blocked goroutine.
	m.mu.Lock()
	ch, ok := m.pending[id]
	if ok {
		delete(m.pending, id)
	}
	m.mu.Unlock()

	if ok {
		ch <- resolution{Approved: approved, Reason: reason}
	}

	if m.bus != nil {
		m.bus.Publish(ApprovalEvent{Type: "resolved", Approval: a})
	}
	if n := m.notifier(); n != nil {
		verb := "denied"
		if approved {
			verb = "approved"
		}
		title := approvalTitle(a, "Approval "+verb)
		body := reason
		if body == "" {
			body = "by " + approverType
		}
		n.Publish(a.ID, "mcplexer", "approval", "approval_"+verb,
			"normal", title, body, "approval,"+a.Surface,
			"/approvals?selected="+a.ID)
	}

	return nil
}

// approvalTitle renders a short tray-friendly headline for a pending or
// resolved approval. The surface ("shell", "tool", ...) prefixes the
// tool name so the user can scan a burst of related events at a glance.
func approvalTitle(a *store.ToolApproval, verb string) string {
	if a == nil {
		return verb
	}
	tool := a.ToolName
	if tool == "" {
		tool = a.Surface
	}
	if tool == "" {
		return verb
	}
	return verb + ": " + tool
}

// ListPending returns all in-memory pending approvals, optionally excluding
// those from a given session (so agents can't see their own requests).
func (m *Manager) ListPending(excludeSessionID string) []*store.ToolApproval {
	m.mu.Lock()
	ids := make([]string, 0, len(m.pending))
	for id := range m.pending {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var out []*store.ToolApproval
	for _, id := range ids {
		a, err := m.store.GetToolApproval(context.Background(), id)
		if err != nil {
			continue
		}
		if a.Status != "pending" {
			continue
		}
		if excludeSessionID != "" && a.RequestSessionID == excludeSessionID {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Shutdown resolves all in-memory pending approvals as cancelled.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	pending := m.pending
	m.pending = make(map[string]chan resolution)
	m.mu.Unlock()

	for id, ch := range pending {
		_ = m.store.ResolveToolApproval(
			context.Background(), id, "cancelled", "", "system", "server shutdown",
		)
		ch <- resolution{Approved: false, Reason: "server shutdown"}
	}
}

// PublishExternal fans out an externally-managed approval row on the
// approval bus + notify tray. Use this for kind=mesh_grant_consent and
// other entries that are persisted as already-resolved (status !=
// pending) and so never go through RequestApproval/Resolve. No-op when
// the bus is nil; notify is best-effort.
func (m *Manager) PublishExternal(a *store.ToolApproval) {
	if a == nil {
		return
	}
	if m.bus != nil {
		// Use "resolved" so SSE subscribers fold it into Recent History
		// rather than the Pending list.
		m.bus.Publish(ApprovalEvent{Type: "resolved", Approval: a})
	}
	if n := m.notifier(); n != nil {
		title := approvalTitle(a, "Consent recorded")
		body := a.Summary
		if body == "" {
			body = a.Resolution
		}
		n.Publish(a.ID, "mcplexer", "approval", "approval_"+a.Status,
			"low", title, body, "approval,"+a.Surface,
			"/approvals?selected="+a.ID)
	}
}

// ExpireStale marks orphaned pending approvals in the DB (from previous runs)
// as expired, so they don't accumulate.
func (m *Manager) ExpireStale(ctx context.Context) {
	n, err := m.store.ExpirePendingApprovals(ctx, time.Now().UTC())
	if err != nil {
		slog.Warn("failed to expire stale approvals", "err", err)
		return
	}
	if n > 0 {
		slog.Info("expired stale approvals from previous run", "count", n)
	}
}
