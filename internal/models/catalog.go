package models

import (
	"sort"
	"strings"
	"time"
)

// ModelSourceKind labels where a provider's model list in the catalog came
// from. "live" means the provider itself was asked (its own listing command
// or config file); "static" means we fell back to the declared KnownModels
// because the provider had no usable live source. The distinction is exposed
// so nobody trusts a stale/static list as if it were freshly probed.
type ModelSourceKind string

const (
	// ModelSourceLive — enumerated from the provider itself this refresh.
	ModelSourceLive ModelSourceKind = "live"
	// ModelSourceStatic — declared KnownModels fallback (no live source, or
	// the live probe failed). Treated as advisory, not authoritative.
	ModelSourceStatic ModelSourceKind = "static"
)

// ModelAuthState records whether we could authenticate well enough to
// enumerate a provider's models. It exists so a provider we cannot log in
// to (grok today) reports "unauthenticated — showing the CLI default only"
// instead of silently looking like a complete list.
type ModelAuthState string

const (
	// ModelAuthOK — the provider answered an authenticated listing.
	ModelAuthOK ModelAuthState = "ok"
	// ModelAuthUnauthenticated — the provider needs credentials we do not
	// have; the model list is a default/guess, not the real catalog.
	ModelAuthUnauthenticated ModelAuthState = "unauthenticated"
	// ModelAuthUnknown — the live probe errored for a non-auth reason, or
	// we never reached the auth stage.
	ModelAuthUnknown ModelAuthState = "unknown"
	// ModelAuthNotApplicable — no auth concept (local file / no live source).
	ModelAuthNotApplicable ModelAuthState = "not_applicable"
)

// ProviderCatalog is the catalog entry for one provider: the model ids it
// currently offers, where that list came from, when it was refreshed, and
// the auth state we observed while probing.
type ProviderCatalog struct {
	Provider      string          `json:"provider"`
	Models        []string        `json:"models"`
	Source        ModelSourceKind `json:"source"`
	AuthState     ModelAuthState  `json:"auth_state"`
	LastRefreshed time.Time       `json:"last_refreshed"`
	// Note is a short, operator-facing explanation — e.g. why a provider
	// fell back to static, or that only the CLI default is shown.
	Note string `json:"note,omitempty"`
}

// HasModel reports whether id is in this provider's current model set.
// Matching is exact (trimmed); the caller has already normalised ids.
func (p ProviderCatalog) HasModel(id string) bool {
	id = strings.TrimSpace(id)
	for _, m := range p.Models {
		if m == id {
			return true
		}
	}
	return false
}

// Catalog is an immutable snapshot of every probed provider, taken at
// RefreshedAt. Providers is sorted by name for stable API output.
type Catalog struct {
	Providers   []ProviderCatalog `json:"providers"`
	RefreshedAt time.Time         `json:"refreshed_at"`
}

// Provider returns the entry for name, or ok=false when the catalog has no
// row for it (a cold or partial catalog). Callers treat !ok as "nothing to
// validate against" and fall back to their own rules.
func (c Catalog) Provider(name string) (ProviderCatalog, bool) {
	name = strings.TrimSpace(name)
	for _, p := range c.Providers {
		if p.Provider == name {
			return p, true
		}
	}
	return ProviderCatalog{}, false
}

// CatalogReader is the read side of the model catalog, satisfied by
// *Refresher. Consumers (delegation preflight, the API handler) depend on
// this interface so they never trigger a live probe themselves — they only
// read the last cached snapshot.
type CatalogReader interface {
	Catalog() Catalog
}

// dedupeSortModels trims, drops blanks, de-duplicates and sorts model ids so
// every catalog entry has a canonical, stable list regardless of probe order.
func dedupeSortModels(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
