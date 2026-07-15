package approval

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// AFKPolicy describes what the Guards system should do when a human
// approval request can't reach the local human (no UI subscriber, no
// active session). Values are per Guard surface.
type AFKPolicy string

// Policy constants.
const (
	PolicyDeny         AFKPolicy = "deny"
	PolicyQueue        AFKPolicy = "queue"
	PolicyMeshPeer     AFKPolicy = "mesh-peer-approve"
	PolicyTrustedAllow AFKPolicy = "trusted-allowlist"
)

// ErrQueueRequested signals "PolicyQueue selected — leave the approval
// pending in the Manager and let the normal timeout path own it".
var ErrQueueRequested = errors.New("queue requested")

// PolicyResolver picks the AFK decision for one ToolApproval given the
// active policy and the available trusted-allowlist rules. Rules can be
// mutated at runtime via SetRules — the resolver protects reads with a
// RW mutex so the HTTP CRUD path can install a new ruleset without
// racing in-flight approval evaluations.
type PolicyResolver struct {
	Policy       AFKPolicy
	Rules        []store.ApprovalRule
	PeerApprover PeerApprover
	TrustedPeers []string // ordered list of peer IDs to consult; empty → fail

	mu     sync.RWMutex
	hitRec RuleHitRecorder // installed via SetHitRecorder; nil = no telemetry
}

// SetRules atomically replaces the active ruleset. Safe to call from
// any goroutine; in-flight Resolve calls finish against the previous
// snapshot (slices are copied into Resolve's local frame).
func (r *PolicyResolver) SetRules(rules []store.ApprovalRule) {
	r.mu.Lock()
	r.Rules = rules
	r.mu.Unlock()
}

// snapshotRules returns the current rules under read-lock. Returned
// slice is the live backing array (Resolve never mutates it).
func (r *PolicyResolver) snapshotRules() []store.ApprovalRule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Rules
}

// RuleHitRecorder is the narrow surface the resolver uses to increment
// hit_count + last_hit_at after a trusted-allowlist match. Optional —
// nil disables hit telemetry. Defined here to avoid the policy package
// depending on the full Store interface.
type RuleHitRecorder interface {
	IncrementHitCount(ctx context.Context, id string, hitAt time.Time) error
}

// SetHitRecorder installs the hit-counter sink. Best-effort: errors
// from IncrementHitCount are logged but never block the resolution.
func (r *PolicyResolver) SetHitRecorder(rec RuleHitRecorder) {
	r.mu.Lock()
	r.hitRec = rec
	r.mu.Unlock()
}

func (r *PolicyResolver) hitRecorder() RuleHitRecorder {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hitRec
}

// Resolve returns the final decision (approved/denied + reason + matched rule ID
// for attribution). It implements:
//   - PolicyDeny: always deny with reason "afk policy: deny"
//   - PolicyQueue: return ErrQueueRequested → Manager keeps it pending
//   - PolicyTrustedAllow: scan Rules for a match (surface + pattern +
//     cwd + session); if any decision="allow" rule matches, approve.
//     Otherwise return (false,"","",nil) — Manager keeps it pending.
//   - PolicyMeshPeer: call PeerApprover.Ask with TrustedPeers; bubble
//     up its result.
//
// The ruleID return is non-empty only when a TrustedAllow rule matched;
// callers use it to write structured approver attribution
// (e.g. "rule:<id>") into the audit trail. For all other policies it
// is always empty.
//
// The "(false, false, nil)" signal means "policy could not make a
// determination and the approval should remain pending"; the caller
// inspects both (approved, decided) — here mapped to (approved, err)
// where err==nil + approved==false in TrustedAllow's no-match path is
// distinguished by the second bool return being false.
func (r *PolicyResolver) Resolve(
	ctx context.Context, a *store.ToolApproval,
) (approved bool, reason string, ruleID string, err error) {
	switch r.Policy {
	case PolicyDeny:
		return false, "afk policy: deny", "", nil
	case PolicyQueue:
		return false, "", "", ErrQueueRequested
	case PolicyTrustedAllow:
		return r.resolveTrustedAllow(a)
	case PolicyMeshPeer:
		approved, reason, err = r.resolveMeshPeer(ctx, a)
		return approved, reason, "", err
	default:
		// Unknown policy → behave like queue (safest: stay pending).
		return false, "", "", ErrQueueRequested
	}
}

// resolveTrustedAllow scans the allowlist for a matching rule. Lower
// Priority wins. When no rule matches, returns (false, "", "", nil) so
// the caller treats this as "no decision; keep pending". On a match,
// also bumps hit_count + last_hit_at via the optional RuleHitRecorder
// so the dashboard can show "matched 42 times, last 3m ago" per rule.
//
// The third return value is the winning rule's ID — callers use it to
// populate structured approver attribution ("rule:<id>") in the audit
// trail so operators can see WHICH rule auto-approved a given request.
func (r *PolicyResolver) resolveTrustedAllow(
	a *store.ToolApproval,
) (bool, string, string, error) {
	now := time.Now().UTC()
	cwd := extractCWD(a.Arguments)
	rules := r.snapshotRules()

	matches := make([]store.ApprovalRule, 0, len(rules))
	for _, rule := range rules {
		if !ruleMatches(rule, a, cwd, now) {
			continue
		}
		matches = append(matches, rule)
	}
	if len(matches) == 0 {
		return false, "", "", nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Priority < matches[j].Priority
	})
	winner := matches[0]
	if rec := r.hitRecorder(); rec != nil {
		if err := rec.IncrementHitCount(context.Background(), winner.ID, now); err != nil {
			slog.Warn("approval rule hit count", "rule_id", winner.ID, "error", err)
		}
	}
	if winner.Decision == "allow" {
		return true, "afk policy: trusted-allowlist match " + winner.ID, winner.ID, nil
	}
	// A denying rule won; treat as denied.
	if winner.Decision == "deny" {
		return false, "afk policy: trusted-allowlist deny " + winner.ID, winner.ID, nil
	}
	// "prompt" or unknown → no decision.
	return false, "", "", nil
}

// HasAllowMetacharsMatch returns true when at least one currently-active
// rule (a) matches the approval under the same predicate as
// resolveTrustedAllow, (b) has decision="allow", and (c) carries the
// per-rule AllowMetachars opt-in. Used by the shell hook to decide
// whether to short-circuit its metachar cheap-block: when a matching
// rule exists, the hook lets the request through to the normal approval
// path (where that rule will then auto-approve). Read-only — does NOT
// bump hit_count; the eventual resolveTrustedAllow pass handles that.
//
// Returns false when the policy isn't TrustedAllow (other policies have
// no rule snapshot to consult) or when no matching rule has the flag set.
func (r *PolicyResolver) HasAllowMetacharsMatch(a *store.ToolApproval) bool {
	if r == nil || a == nil {
		return false
	}
	if r.Policy != PolicyTrustedAllow {
		return false
	}
	now := time.Now().UTC()
	cwd := extractCWD(a.Arguments)
	for _, rule := range r.snapshotRules() {
		if !rule.AllowMetachars || rule.Decision != "allow" {
			continue
		}
		if ruleMatches(rule, a, cwd, now) {
			return true
		}
	}
	return false
}

// resolveMeshPeer fans the approval out to TrustedPeers and bubbles
// PeerApprover's decision back. Missing dependencies fail closed.
func (r *PolicyResolver) resolveMeshPeer(
	ctx context.Context, a *store.ToolApproval,
) (bool, string, error) {
	if r.PeerApprover == nil {
		return false, "afk policy: mesh-peer-approve has no PeerApprover", nil
	}
	if len(r.TrustedPeers) == 0 {
		return false, "afk policy: mesh-peer-approve has no trusted peers", nil
	}
	approved, reason, err := r.PeerApprover.Ask(ctx, a, r.TrustedPeers)
	if err != nil {
		// Timeout / no-peers / send failure → no decision, keep pending.
		if errors.Is(err, ErrPeerTimeout) || errors.Is(err, ErrNoPeerTargets) {
			return false, "", nil
		}
		return false, "", err
	}
	if reason == "" {
		if approved {
			reason = "afk policy: peer approved"
		} else {
			reason = "afk policy: peer denied"
		}
	}
	return approved, reason, nil
}

// ruleMatches returns true when rule covers approval a under the given
// cwd + now reference.
func ruleMatches(
	rule store.ApprovalRule,
	a *store.ToolApproval,
	cwd string,
	now time.Time,
) bool {
	if rule.Surface != a.Surface {
		return false
	}
	if rule.ExpiresAt != nil && !rule.ExpiresAt.After(now) {
		return false
	}
	if rule.Pattern == "" || !routing.GlobMatch(rule.Pattern, a.ToolName) {
		return false
	}
	if rule.Directory != "" {
		if cwd == "" {
			return false
		}
		if !directoryMatches(rule.Directory, cwd) {
			return false
		}
	}
	if rule.AISessionID != "" && rule.AISessionID != a.RequestSessionID {
		return false
	}
	return true
}

// directoryMatches returns true when cwd equals dir or is a subpath.
// Trailing slashes are normalised so a rule directory like
// "/srv/wsA/" (operator habit of pasting paths from `pwd` or copying
// trailing-slash forms out of file managers) still matches the
// canonical "/srv/wsA". Without this normalisation the rule would
// silently fail to match every legitimate cwd — the symptom Morgan
// hit in the field where every Bash invocation required approval
// despite having per-project rules set up.
//
// Inputs are also passed through TrimSpace so an accidentally
// space-padded form value from the dashboard form (yes, this happens)
// doesn't poison the comparison.
func directoryMatches(dir, cwd string) bool {
	dir = strings.TrimRight(strings.TrimSpace(dir), "/")
	cwd = strings.TrimRight(strings.TrimSpace(cwd), "/")
	if dir == "" {
		// An all-slash / all-space rule directory is treated as "no
		// directory restriction"; ruleMatches already short-circuits
		// before calling here when rule.Directory is empty, but this
		// branch makes the helper safe under direct unit tests too.
		return true
	}
	if dir == cwd {
		return true
	}
	return strings.HasPrefix(cwd, dir+"/")
}

// extractCWD attempts to pull a "cwd" string out of the approval's
// arguments JSON. Returns "" when the arguments aren't JSON or don't
// carry a cwd field. Recognised keys: "cwd", "working_directory".
func extractCWD(arguments string) string {
	if arguments == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(arguments), &obj); err != nil {
		return ""
	}
	for _, key := range []string{"cwd", "working_directory"} {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
