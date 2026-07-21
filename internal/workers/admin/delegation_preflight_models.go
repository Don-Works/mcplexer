package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
)

// preflightKnownModelIDs validates each CLI-provider candidate's model id
// against the union of KnownModels declared by registered model profiles
// for the same provider. Providers with no declared catalog are skipped —
// there is nothing to validate against. This catches the "doomed before
// first token" class where a mistyped or hallucinated model id (e.g.
// grok-composer-2.5-fast) burns the whole wall-clock budget before the
// CLI reports the id is unknown. Only CLI providers are checked: their id
// space is fixed by the installed CLI, whereas API/compat providers ship
// new ids faster than any local catalog tracks.
func (s *Service) preflightKnownModelIDs(ctx context.Context, candidates []delegationResolvedModelCandidate) error {
	if s.modelProfiles == nil {
		return nil
	}
	known := map[string]map[string]struct{}{} // provider -> declared model ids
	loaded := false
	for _, c := range candidates {
		if !models.IsCLIProvider(c.ModelProvider) {
			continue
		}
		if !loaded {
			profiles, err := s.modelProfiles.ListModelProfiles(ctx)
			if err != nil {
				return fmt.Errorf("preflight: list model profiles: %w", err)
			}
			for _, p := range profiles {
				for _, id := range p.KnownModels {
					if id = strings.TrimSpace(id); id == "" {
						continue
					}
					if known[p.Provider] == nil {
						known[p.Provider] = map[string]struct{}{}
					}
					known[p.Provider][id] = struct{}{}
				}
			}
			loaded = true
		}
		set := known[c.ModelProvider]
		if len(set) == 0 {
			continue
		}
		if _, ok := set[c.ModelID]; ok {
			continue
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return fmt.Errorf(
			"preflight: model_id %q is not a known model for provider %q (known: %s); correct the id or add it to a model profile's known models",
			c.ModelID, c.ModelProvider, strings.Join(ids, ", "))
	}
	return nil
}
