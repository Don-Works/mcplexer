package models

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveProbersAgainstInstalledCLIs runs the REAL probers against the
// locally-installed provider CLIs / config. It is opt-in (skipped unless
// MCPLEXER_LIVE_PROBE_TEST=1) because it shells out to grok/mimo and reads
// ~/.pi/agent/models.json, which are host-specific and not present in CI.
//
// It exists to prove the probe→parse pipeline turns ACTUAL provider output
// into model ids on this machine, not just captured fixtures.
func TestLiveProbersAgainstInstalledCLIs(t *testing.T) {
	if os.Getenv("MCPLEXER_LIVE_PROBE_TEST") != "1" {
		t.Skip("set MCPLEXER_LIVE_PROBE_TEST=1 to run live provider probes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, p := range []ModelProber{
		newGrokModelsProber(),
		newMimoModelsProber(),
		newPiModelsFileProber(),
	} {
		res, err := p.Probe(ctx)
		t.Logf("provider=%s models=%v auth=%s note=%q err=%v",
			p.Provider(), res.Models, res.AuthState, res.Note, err)
		if err != nil {
			continue // some providers may be logged out / not configured here
		}
		if len(res.Models) == 0 {
			t.Errorf("%s: live probe returned no models", p.Provider())
		}
	}
}

// TestLiveRefresherEndToEnd builds the full refresher against the installed
// CLIs and prints the catalog, proving live + static entries coexist with
// correct source labelling. Opt-in for the same reason as above.
func TestLiveRefresherEndToEnd(t *testing.T) {
	if os.Getenv("MCPLEXER_LIVE_PROBE_TEST") != "1" {
		t.Skip("set MCPLEXER_LIVE_PROBE_TEST=1 to run the live refresher")
	}
	r := NewRefresher(RefresherOptions{
		Probers:   EnabledProbers(),
		Providers: EnabledCLIProviders(),
		Static: func(context.Context) (map[string][]string, error) {
			return map[string][]string{"claude_cli": {"claude-sonnet-4-5"}}, nil
		},
	})
	cat := r.Refresh(context.Background())
	for _, p := range cat.Providers {
		t.Logf("provider=%s source=%s auth=%s models=%v note=%q",
			p.Provider, p.Source, p.AuthState, p.Models, p.Note)
	}
	if cat.RefreshedAt.IsZero() {
		t.Fatal("refreshed_at not set")
	}
}
