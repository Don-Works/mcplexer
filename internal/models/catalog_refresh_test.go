package models

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeProber struct {
	provider string
	res      ProbeResult
	err      error
}

func (f fakeProber) Provider() string                           { return f.provider }
func (f fakeProber) Probe(context.Context) (ProbeResult, error) { return f.res, f.err }

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestRefresherLiveAndStaticFallback pins the core contract: a provider with
// a working live probe is labelled live; a provider that reports no live
// source or errors falls back to declared KnownModels and is labelled static.
func TestRefresherLiveAndStaticFallback(t *testing.T) {
	when := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	r := NewRefresher(RefresherOptions{
		Probers: []ModelProber{
			fakeProber{provider: ProviderGrokCLI, res: ProbeResult{
				Models: []string{"grok-4.5", "grok-4.5"}, AuthState: ModelAuthOK,
			}},
			fakeProber{provider: ProviderMiMoCLI, err: ErrNoLiveModelSource},
			fakeProber{provider: ProviderCodexCLI, err: errors.New("boom: exit status 1")},
		},
		Static: func(context.Context) (map[string][]string, error) {
			return map[string][]string{
				ProviderMiMoCLI:   {"xiaomi/mimo-v2.5"},
				ProviderCodexCLI:  {"codex-fallback"},
				ProviderClaudeCLI: {"claude-sonnet-4-5"},
			}, nil
		},
		Providers: []string{ProviderPiCLI},
		Clock:     fixedClock(when),
	})

	cat := r.Refresh(context.Background())
	if !cat.RefreshedAt.Equal(when) {
		t.Fatalf("RefreshedAt = %v, want %v", cat.RefreshedAt, when)
	}

	// Live grok, deduped.
	grok, ok := cat.Provider(ProviderGrokCLI)
	if !ok || grok.Source != ModelSourceLive || !reflect.DeepEqual(grok.Models, []string{"grok-4.5"}) {
		t.Fatalf("grok entry = %+v", grok)
	}
	if grok.AuthState != ModelAuthOK {
		t.Fatalf("grok auth = %q", grok.AuthState)
	}

	// mimo: prober said no live source -> static fallback, labelled static.
	mimo, _ := cat.Provider(ProviderMiMoCLI)
	if mimo.Source != ModelSourceStatic || !reflect.DeepEqual(mimo.Models, []string{"xiaomi/mimo-v2.5"}) {
		t.Fatalf("mimo entry = %+v", mimo)
	}
	if !strings.Contains(mimo.Note, "no live source") {
		t.Fatalf("mimo note = %q", mimo.Note)
	}

	// codex: hard probe error -> static fallback, note explains the failure.
	codex, _ := cat.Provider(ProviderCodexCLI)
	if codex.Source != ModelSourceStatic || codex.AuthState != ModelAuthUnknown {
		t.Fatalf("codex entry = %+v", codex)
	}
	if !strings.Contains(codex.Note, "live probe failed") {
		t.Fatalf("codex note = %q", codex.Note)
	}

	// claude: no prober, static only.
	claude, _ := cat.Provider(ProviderClaudeCLI)
	if claude.Source != ModelSourceStatic || !reflect.DeepEqual(claude.Models, []string{"claude-sonnet-4-5"}) {
		t.Fatalf("claude entry = %+v", claude)
	}

	// pi: seeded universe, no prober, no static -> present but empty + static.
	pi, ok := cat.Provider(ProviderPiCLI)
	if !ok || pi.Source != ModelSourceStatic || len(pi.Models) != 0 {
		t.Fatalf("pi entry = %+v (ok=%v)", pi, ok)
	}

	// Cached snapshot equals the returned catalog.
	if got := r.Catalog(); !got.RefreshedAt.Equal(when) || len(got.Providers) != len(cat.Providers) {
		t.Fatalf("cached catalog drifted: %+v", got)
	}
}

// TestRefresherAuthStatePropagates pins that an unauthenticated live probe
// keeps its list but is flagged unauthenticated with a note.
func TestRefresherAuthStatePropagates(t *testing.T) {
	r := NewRefresher(RefresherOptions{
		Probers: []ModelProber{
			fakeProber{provider: ProviderGrokCLI, res: ProbeResult{
				Models: []string{"grok-4.5"}, AuthState: ModelAuthUnauthenticated,
				Note: "unauthenticated — showing the CLI default model only",
			}},
		},
		Clock: fixedClock(time.Unix(0, 0).UTC()),
	})
	cat := r.Refresh(context.Background())
	grok, _ := cat.Provider(ProviderGrokCLI)
	if grok.AuthState != ModelAuthUnauthenticated {
		t.Fatalf("auth = %q, want unauthenticated", grok.AuthState)
	}
	if !strings.Contains(grok.Note, "unauthenticated") {
		t.Fatalf("note = %q", grok.Note)
	}
	if grok.Source != ModelSourceLive {
		t.Fatalf("source = %q, want live", grok.Source)
	}
}

// TestGrokProberParsesListing drives a prober end-to-end through the real
// parser with an injected command runner returning captured output — proving
// the probe→parse wiring turns provider output into model ids.
func TestGrokProberParsesListing(t *testing.T) {
	p := &grokModelsProber{
		binary: "grok",
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(grokModelsFixture), nil
		},
	}
	res, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !reflect.DeepEqual(res.Models, []string{"grok-4.5"}) {
		t.Fatalf("models = %v", res.Models)
	}
	if res.AuthState != ModelAuthOK {
		t.Fatalf("auth = %q", res.AuthState)
	}
	if p.Provider() != ProviderGrokCLI {
		t.Fatalf("provider = %q", p.Provider())
	}
}

// TestPiProberReadsFile drives the pi file prober through an injected reader.
func TestPiProberReadsFile(t *testing.T) {
	p := &piModelsFileProber{
		path: "/fake/models.json",
		read: func(string) ([]byte, error) { return []byte(piModelsFixture), nil },
	}
	res, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	found := false
	for _, id := range res.Models {
		if id == "qwen-local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected alias qwen-local in %v", res.Models)
	}
}
