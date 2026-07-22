package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
)

// preflightKnownModelIDs validates each CLI-provider candidate's model id
// against the set of models the provider currently offers. This catches the
// "doomed before first token" class where a mistyped or hallucinated model id
// (e.g. grok-composer-2.5-fast) burns the whole wall-clock budget before the
// CLI reports the id is unknown. Only CLI providers are checked: their id
// space is fixed by the installed CLI/credentials, whereas API/compat
// providers ship new ids faster than any local catalog tracks.
//
// Source of truth, in order:
//  1. The LIVE model catalog (models.CatalogReader) when wired — reflects
//     what each provider ACTUALLY offers right now (live-probed, or static
//     KnownModels where a provider has no live source), with an observable
//     last-refreshed timestamp. A cold/partial catalog (no entry for the
//     provider) falls through to (2).
//  2. The static union of KnownModels declared by registered model profiles,
//     the legacy behaviour, used when no catalog is wired or the catalog has
//     no row for the provider yet.
func (s *Service) preflightKnownModelIDs(ctx context.Context, candidates []delegationResolvedModelCandidate) error {
	var legacy map[string]map[string]struct{} // provider -> declared ids (lazy)
	legacyLoaded := false
	for _, c := range candidates {
		if !models.IsCLIProvider(c.ModelProvider) {
			continue
		}
		// (1) live catalog.
		if s.modelCatalog != nil {
			if entry, ok := s.modelCatalog.Catalog().Provider(c.ModelProvider); ok && len(entry.Models) > 0 {
				if entry.HasModel(c.ModelID) {
					continue
				}
				return catalogModelRejectError(c.ModelID, entry)
			}
		}
		// (2) legacy static profile union.
		if !legacyLoaded {
			var err error
			if legacy, err = s.loadDeclaredModelIDs(ctx); err != nil {
				return err
			}
			legacyLoaded = true
		}
		set := legacy[c.ModelProvider]
		if len(set) == 0 {
			continue
		}
		if _, ok := set[c.ModelID]; ok {
			continue
		}
		return legacyModelRejectError(c.ModelID, c.ModelProvider, set)
	}
	return nil
}

// candidateCatalogUnavailable returns a non-empty reason when the LIVE catalog
// definitively reports that a CLI-provider candidate's model id is not offered
// right now. It is used to DROP such a candidate from the auto-expanded capacity
// pool, rather than let preflightKnownModelIDs hard-reject the whole delegation
// on one bad id buried in a ranked pool the caller never chose. The hard-reject
// stays for explicitly-pinned candidates (single/ranked/side_by_side), where a
// bad id IS the operator's mistake to surface.
//
// Returns "" (keep) for non-CLI providers, when no catalog is wired, or when the
// catalog has no live entry for the provider — "unknown" is not "unavailable",
// so a provider the catalog cannot see yet falls through to preflight's static
// fallback for the survivors.
func (s *Service) candidateCatalogUnavailable(c delegationResolvedModelCandidate) string {
	if s.modelCatalog == nil || !models.IsCLIProvider(c.ModelProvider) {
		return ""
	}
	entry, ok := s.modelCatalog.Catalog().Provider(c.ModelProvider)
	if !ok || len(entry.Models) == 0 || entry.HasModel(c.ModelID) {
		return ""
	}
	return fmt.Sprintf("candidate %q dropped: not in the current catalog for provider %q",
		c.ModelProvider+"/"+c.ModelID, c.ModelProvider)
}

// providerUnauthenticated reports whether the live catalog says this provider
// is currently unauthenticated. Its models cannot actually be reached, so
// capacity routing must sink it below any authenticated candidate rather than
// rank it #1 and waste a dispatch on a doomed provider (the auth-alert has
// already told the operator to log in). It is DEPRIORITISED, not dropped: auth
// can be transient and the catalog can be stale, so it stays a last resort when
// nothing authenticated is available.
func (s *Service) providerUnauthenticated(provider string) bool {
	if s.modelCatalog == nil {
		return false
	}
	entry, ok := s.modelCatalog.Catalog().Provider(provider)
	return ok && entry.AuthState == models.ModelAuthUnauthenticated
}

// loadDeclaredModelIDs builds provider -> declared KnownModels from the model
// profile store (the legacy static catalog).
func (s *Service) loadDeclaredModelIDs(ctx context.Context) (map[string]map[string]struct{}, error) {
	out := map[string]map[string]struct{}{}
	if s.modelProfiles == nil {
		return out, nil
	}
	profiles, err := s.modelProfiles.ListModelProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("preflight: list model profiles: %w", err)
	}
	for _, p := range profiles {
		for _, id := range p.KnownModels {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			if out[p.Provider] == nil {
				out[p.Provider] = map[string]struct{}{}
			}
			out[p.Provider][id] = struct{}{}
		}
	}
	return out, nil
}

// catalogModelRejectError formats the fast-reject error from a live catalog
// entry: it names the currently-available ids, the source (live vs static
// fallback), and the freshness so the operator can trust or distrust it.
func catalogModelRejectError(modelID string, entry models.ProviderCatalog) error {
	source := "static fallback"
	if entry.Source == models.ModelSourceLive {
		source = "live-probed"
	}
	msg := fmt.Sprintf(
		"preflight: model_id %q is not currently available for provider %q. "+
			"Available now (%s, refreshed %s): %s",
		modelID, entry.Provider, source,
		entry.LastRefreshed.UTC().Format("2006-01-02T15:04:05Z"),
		strings.Join(entry.Models, ", "))
	if entry.AuthState == models.ModelAuthUnauthenticated {
		msg += " (provider unauthenticated — the real catalog may be larger; log the CLI in to see more)"
	}
	msg += ". Correct the id, or if the model is newly released wait for the next catalog refresh."
	return fmt.Errorf("%s", msg)
}

// legacyModelRejectError formats the profile-union rejection, preserving the
// historical "not a known model" wording relied on by existing callers/tests.
func legacyModelRejectError(modelID, provider string, set map[string]struct{}) error {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Errorf(
		"preflight: model_id %q is not a known model for provider %q (known: %s); "+
			"correct the id or add it to a model profile's known models",
		modelID, provider, strings.Join(ids, ", "))
}
