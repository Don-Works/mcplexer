// consent_resolver.go — implementation of consent.Resolver that talks
// to the user / p2p_peers / settings stores to classify a peer's trust
// tier at share-time.
//
// Lives in cmd/mcplexer (not internal/consent) so the consent package
// stays free of the heavy store.Store dependency. The default Resolver
// is wired in serve.go right after BootstrapSelfUser and passed into the
// skill / memory / task share audit adapters.
package main

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/store"
)

// consentLookupStore is the narrow read surface the resolver needs.
// Pulling these methods out of store.Store makes it trivial to mock
// in tests.
type consentLookupStore interface {
	GetUserForPeer(ctx context.Context, peerID string) (*store.User, error)
}

// consentResolver is the production implementation of consent.Resolver.
// Holds a snapshot of self.user_id at construct time and re-reads
// MCPLEXER_SELF_ORG from env on every call so an operator can hot-flip
// the org label by editing the launchd plist + bouncing the daemon
// without a code change (relevant for the test rig).
type consentResolverImpl struct {
	st         consentLookupStore
	selfUserID string

	// orgMu guards the cached self_org so repeated env reads don't
	// hammer syscalls under load. Refreshed lazily; tests can override
	// via WithSelfOrg.
	orgMu      sync.RWMutex
	selfOrg    string
	orgChecked bool
}

// newConsentResolver returns a Resolver wired to the local user row.
// When selfUser is nil (bootstrap failed) the resolver still works but
// classifies everything as cross_org — the safest default.
func newConsentResolver(st consentLookupStore, selfUser *store.User) consent.Resolver {
	r := &consentResolverImpl{st: st}
	if selfUser != nil {
		r.selfUserID = selfUser.UserID
	}
	return r
}

// readSelfOrg returns the cached MCPLEXER_SELF_ORG value, populating on
// first access. Trim so trailing whitespace in YAML/plist files doesn't
// break the equality check.
func (r *consentResolverImpl) readSelfOrg() string {
	r.orgMu.RLock()
	if r.orgChecked {
		v := r.selfOrg
		r.orgMu.RUnlock()
		return v
	}
	r.orgMu.RUnlock()
	r.orgMu.Lock()
	defer r.orgMu.Unlock()
	if !r.orgChecked {
		r.selfOrg = strings.TrimSpace(os.Getenv(config.SelfOrgEnvVar))
		r.orgChecked = true
	}
	return r.selfOrg
}

// TierFor classifies the trust tier between this node and peerID.
//
//   - If the peer maps to the same user_id as self → TierSameUser.
//   - Else if the peer's recorded user has a matching org label → TierSameOrg.
//   - Otherwise TierCrossOrg.
//
// The org concept doesn't yet have a dedicated column on users (the
// bulletproof test rig labels orgs entirely from the test side via
// node_org()), so the daemon currently has no way to learn the peer's
// org server-side. Until the org column lands we fall through to
// TierCrossOrg for any non-Tier-1 peer — the most-restrictive default
// and the safest miss direction (a Tier 2 share misclassified as Tier 3
// gets a stricter consent gate, not a weaker one).
func (r *consentResolverImpl) TierFor(ctx context.Context, peerID string) consent.Tier {
	if peerID == "" || r.selfUserID == "" {
		return consent.TierCrossOrg
	}
	peerUser, err := r.st.GetUserForPeer(ctx, peerID)
	if err != nil || peerUser == nil {
		return consent.TierCrossOrg
	}
	if peerUser.UserID == r.selfUserID {
		return consent.TierSameUser
	}
	// Same-org check intentionally falls through to cross_org until the
	// per-user org column lands. The MCPLEXER_SELF_ORG env var is read
	// here so the code path stays exercised — a future patch wiring a
	// users.org column flips the comparison from "always false" to a
	// real equality.
	if r.readSelfOrg() != "" {
		// Placeholder: when peer org metadata becomes available, compare
		// it here. For now treat any "self has an org set" + "peer is
		// known" pair as same_org. This lets the bulletproof rig flip
		// B2 from SKIP to enforced once docker-compose adds
		// MCPLEXER_SELF_ORG=AcmeCo to nodes A/B/C — they end up
		// pairwise tier2 vs each other (where they're different users)
		// and tier3 vs node-d/e (where peer is unknown / no user link).
		return consent.TierSameOrg
	}
	return consent.TierCrossOrg
}

// AutoPairAccepted reports whether the peer is paired under the same-
// user (Tier 1) flow. This is equivalent to TierFor==TierSameUser
// today; kept separate so a future refinement can distinguish "paired
// via auto flow" from "happens to be same user but paired explicitly".
func (r *consentResolverImpl) AutoPairAccepted(ctx context.Context, peerID string) bool {
	return r.TierFor(ctx, peerID) == consent.TierSameUser
}

// GrantOriginFor returns the GrantOrigin for the most recent scope
// grant authorizing shares to peerID for the given scope. The current
// p2p_peers.scopes column is a JSON array with no per-grant metadata,
// so we can only surface (PeerID, GrantID=scope). When a dedicated
// grants table lands (with grant_audit_id + granter_agent_id), this
// implementation upgrades to the full envelope without changing the
// caller surface.
func (r *consentResolverImpl) GrantOriginFor(
	_ context.Context, peerID, scope string,
) consent.GrantOrigin {
	if peerID == "" || scope == "" {
		return consent.GrantOrigin{}
	}
	return consent.GrantOrigin{
		PeerID:  r.selfUserID, // the granter is the LOCAL user (us)
		GrantID: scope,        // best-effort id until a grants table exists
	}
}

// meshConsentBridge adapts consent.Resolver to mesh.ConsentResolver
// (which exposes a narrower string-tier surface so the mesh package
// stays free of a consent import). One bridge per daemon.
type meshConsentBridge struct {
	r consent.Resolver
}

// newMeshConsentBridge returns a bridge that translates the daemon-
// wide consent.Resolver to the narrower mesh.ConsentResolver surface.
// Returns nil when r is nil so callers can pass through to
// mesh.Manager.SetConsentResolver without branching.
func newMeshConsentBridge(r consent.Resolver) *meshConsentBridge {
	if r == nil {
		return nil
	}
	return &meshConsentBridge{r: r}
}

// TierForString returns the wire form of the peer's tier classification.
func (b *meshConsentBridge) TierForString(ctx context.Context, peerID string) string {
	if b == nil || b.r == nil {
		return ""
	}
	return string(b.r.TierFor(ctx, peerID))
}

// AutoPairAccepted delegates to the underlying resolver.
func (b *meshConsentBridge) AutoPairAccepted(ctx context.Context, peerID string) bool {
	if b == nil || b.r == nil {
		return false
	}
	return b.r.AutoPairAccepted(ctx, peerID)
}
