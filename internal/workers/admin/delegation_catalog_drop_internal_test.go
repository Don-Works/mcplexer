package admin

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
)

type catalogStub struct{ cat models.Catalog }

func (c catalogStub) Catalog() models.Catalog { return c.cat }

// TestCandidateCatalogUnavailable pins F1: in capacity mode a CLI candidate the
// live catalog does not offer must be DROPPED (non-empty reason) so it can be
// filtered out of the auto-expanded pool, rather than hard-rejecting the whole
// delegation. Non-CLI, unknown-provider, and in-catalog candidates are KEPT
// (empty reason) — "unknown" is not "unavailable".
func TestCandidateCatalogUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	s := &Service{}
	s.SetModelCatalog(catalogStub{cat: models.Catalog{
		RefreshedAt: now,
		Providers: []models.ProviderCatalog{{
			Provider: "grok_cli", Models: []string{"grok-4.5"},
			Source: models.ModelSourceLive, AuthState: models.ModelAuthOK, LastRefreshed: now,
		}},
	}})

	cand := func(provider, id string) delegationResolvedModelCandidate {
		return delegationResolvedModelCandidate{
			DelegationModelCandidate: DelegationModelCandidate{ModelProvider: provider, ModelID: id},
		}
	}
	cases := []struct {
		name     string
		cand     delegationResolvedModelCandidate
		wantDrop bool
	}{
		{"cli id not in catalog -> DROP", cand("grok_cli", "grok-composer-2.5"), true},
		{"cli id in catalog -> keep", cand("grok_cli", "grok-4.5"), false},
		{"non-cli provider -> keep", cand("anthropic", "whatever"), false},
		{"provider not in catalog -> keep (unknown != unavailable)", cand("pi_cli", "qwen-local"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := s.candidateCatalogUnavailable(tc.cand)
			if (reason != "") != tc.wantDrop {
				t.Fatalf("candidateCatalogUnavailable=%q, wantDrop=%v", reason, tc.wantDrop)
			}
		})
	}

	// No catalog wired -> never drops (falls through to preflight static fallback).
	if r := (&Service{}).candidateCatalogUnavailable(cand("grok_cli", "anything")); r != "" {
		t.Fatalf("no-catalog service must keep every candidate, got %q", r)
	}
}

// TestProviderUnauthenticated pins F2: capacity routing must recognise a
// provider the live catalog reports unauthenticated so it can sink it below
// authenticated candidates. An ok/absent/no-catalog provider is not penalised.
func TestProviderUnauthenticated(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	s := &Service{}
	s.SetModelCatalog(catalogStub{cat: models.Catalog{
		RefreshedAt: now,
		Providers: []models.ProviderCatalog{
			{Provider: "grok_cli", Models: []string{"grok-4.5"}, AuthState: models.ModelAuthUnauthenticated, LastRefreshed: now},
			{Provider: "pi_cli", Models: []string{"qwen-local"}, AuthState: models.ModelAuthOK, LastRefreshed: now},
		},
	}})
	if !s.providerUnauthenticated("grok_cli") {
		t.Error("grok_cli is unauthenticated in the catalog but was not flagged")
	}
	if s.providerUnauthenticated("pi_cli") {
		t.Error("pi_cli is authenticated (ok) but was flagged unauthenticated")
	}
	if s.providerUnauthenticated("mimo_cli") {
		t.Error("mimo_cli is absent from the catalog; must not be flagged (unknown != unauthenticated)")
	}
	if (&Service{}).providerUnauthenticated("grok_cli") {
		t.Error("no catalog wired must never flag a provider unauthenticated")
	}
}
