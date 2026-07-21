package runner

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/delegscope"
)

// allCLIProviders is the provider set models.IsCLIProvider recognises, used by
// the default-exemption tests to prove EVERY CLI provider runs on the default.
var allCLIProviders = []string{
	models.ProviderClaudeCLI, models.ProviderOpenCodeCLI, models.ProviderGrokCLI,
	models.ProviderMiMoCLI, models.ProviderGeminiCLI, models.ProviderCodexCLI,
	models.ProviderPiCLI,
}

func TestCLIScopeUnenforceable(t *testing.T) {
	tests := []struct {
		name      string
		worker    *store.Worker
		wantErr   bool
		wantNames []string
	}{
		{
			name:   "nil worker",
			worker: nil,
		},
		{
			name: "api provider with a capability profile is enforceable",
			worker: &store.Worker{
				ModelProvider:         models.ProviderAnthropic,
				CapabilityProfileJSON: `{"preset":"researcher"}`,
				ToolAllowlistJSON:     `["task__*"]`,
			},
		},
		{
			name: "cli provider without scope columns runs unscoped by design",
			worker: &store.Worker{
				ModelProvider: models.ProviderClaudeCLI,
			},
		},
		{
			name: "cli provider with empty-string scope columns",
			worker: &store.Worker{
				ModelProvider:         models.ProviderGrokCLI,
				ToolAllowlistJSON:     "   ",
				CapabilityProfileJSON: "",
			},
		},
		{
			name: "cli provider with JSON null scope columns",
			worker: &store.Worker{
				ModelProvider:         models.ProviderCodexCLI,
				ToolAllowlistJSON:     "null",
				CapabilityProfileJSON: " null ",
			},
		},
		{
			name: "cli provider with a capability profile",
			worker: &store.Worker{
				ModelProvider:         models.ProviderClaudeCLI,
				CapabilityProfileJSON: `{"preset":"researcher"}`,
			},
			wantErr:   true,
			wantNames: []string{"capability_profile_json"},
		},
		{
			name: "cli provider with a tool allowlist",
			worker: &store.Worker{
				ModelProvider:     models.ProviderOpenCodeCLI,
				ToolAllowlistJSON: `["task__create"]`,
			},
			wantErr:   true,
			wantNames: []string{"tool_allowlist_json"},
		},
		{
			name: "cli provider with both scope columns names both",
			worker: &store.Worker{
				ModelProvider:         models.ProviderPiCLI,
				ToolAllowlistJSON:     `["task__create"]`,
				CapabilityProfileJSON: `{"preset":"minimal"}`,
			},
			wantErr:   true,
			wantNames: []string{"tool_allowlist_json", "capability_profile_json"},
		},
		{
			// "[]" is what store/sqlite applyWorkerDefaults writes for every
			// worker that leaves the column empty. Treating it as an operator
			// request would refuse every CLI worker in existence.
			name: "cli provider with the sqlite default allowlist",
			worker: &store.Worker{
				ModelProvider:     models.ProviderMiMoCLI,
				ToolAllowlistJSON: `[]`,
			},
		},
		{
			// The capability column has no default backfill, so an empty
			// object is operator-authored and the gate would be active.
			name: "cli provider with an empty capability object",
			worker: &store.Worker{
				ModelProvider:         models.ProviderMiMoCLI,
				ToolAllowlistJSON:     `[]`,
				CapabilityProfileJSON: `{}`,
			},
			wantErr:   true,
			wantNames: []string{"capability_profile_json"},
		},
		{
			// The dispatcher turns an unparseable column into a
			// deny-everything profile, so it is a requested scope too.
			name: "cli provider with a corrupt capability column",
			worker: &store.Worker{
				ModelProvider:         models.ProviderGeminiCLI,
				CapabilityProfileJSON: `{not valid json`,
			},
			wantErr:   true,
			wantNames: []string{"capability_profile_json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cliScopeUnenforceable(tc.worker)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("cliScopeUnenforceable() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("cliScopeUnenforceable() = nil, want an error")
			}
			if !errors.Is(err, ErrCLIScopeUnenforceable) {
				t.Errorf("error does not wrap ErrCLIScopeUnenforceable: %v", err)
			}
			if !strings.Contains(err.Error(), tc.worker.ModelProvider) {
				t.Errorf("error omits the provider %q: %v", tc.worker.ModelProvider, err)
			}
			for _, name := range tc.wantNames {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error omits the requested scope column %q: %v", name, err)
				}
			}
		})
	}
}

// TestCLIScopeUnenforceableCoversEveryCLIProvider pins the guard to the
// provider set models.IsCLIProvider recognises. A new CLI provider added
// there must land here too, or it silently reopens the hole.
func TestCLIScopeUnenforceableCoversEveryCLIProvider(t *testing.T) {
	for _, provider := range []string{
		models.ProviderClaudeCLI, models.ProviderOpenCodeCLI, models.ProviderGrokCLI,
		models.ProviderMiMoCLI, models.ProviderGeminiCLI, models.ProviderCodexCLI,
		models.ProviderPiCLI,
	} {
		t.Run(provider, func(t *testing.T) {
			if !models.IsCLIProvider(provider) {
				t.Fatalf("models.IsCLIProvider(%q) = false; test list is stale", provider)
			}
			err := cliScopeUnenforceable(&store.Worker{
				ModelProvider:         provider,
				CapabilityProfileJSON: `{"preset":"researcher"}`,
			})
			if !errors.Is(err, ErrCLIScopeUnenforceable) {
				t.Fatalf("provider %q: err = %v, want ErrCLIScopeUnenforceable", provider, err)
			}
		})
	}
}

// TestCLIScopeUnenforceableExemptsSystemDefaults is the regression for the CLI
// delegation break: the delegation admin layer stamps the system default
// allowlist (delegscope.DefaultToolsJSON / DefaultReviewToolsJSON) onto every
// delegated worker, so the column is non-empty. Before the fix the guard read
// that as an operator scope and refused EVERY default CLI delegation before it
// ran. The default must run; a real operator scope alongside it must still be
// refused.
func TestCLIScopeUnenforceableExemptsSystemDefaults(t *testing.T) {
	for _, provider := range allCLIProviders {
		for _, def := range []struct {
			label string
			json  string
		}{
			{"execute default", delegscope.DefaultToolsJSON},
			{"review default", delegscope.DefaultReviewToolsJSON},
		} {
			t.Run(provider+"/"+def.label, func(t *testing.T) {
				if err := cliScopeUnenforceable(&store.Worker{
					ModelProvider:     provider,
					ToolAllowlistJSON: def.json,
				}); err != nil {
					t.Fatalf("default allowlist refused for CLI provider %q: %v", provider, err)
				}
			})
		}
	}

	// A default allowlist does not launder an explicit capability profile:
	// capability_profile_json is operator-authored (no system default backfills
	// it) and its gate still cannot reach the CLI child, so the run is refused
	// on the capability column while the default allowlist is NOT named.
	t.Run("default allowlist plus explicit capability still refused", func(t *testing.T) {
		err := cliScopeUnenforceable(&store.Worker{
			ModelProvider:         models.ProviderClaudeCLI,
			ToolAllowlistJSON:     delegscope.DefaultToolsJSON,
			CapabilityProfileJSON: `{"preset":"researcher"}`,
		})
		if !errors.Is(err, ErrCLIScopeUnenforceable) {
			t.Fatalf("err = %v, want ErrCLIScopeUnenforceable", err)
		}
		if strings.Contains(err.Error(), "tool_allowlist_json") {
			t.Errorf("default allowlist wrongly named as a scope: %v", err)
		}
		if !strings.Contains(err.Error(), "capability_profile_json") {
			t.Errorf("capability column not named: %v", err)
		}
	})

	// A near-default allowlist (the default minus one tool) is a genuine
	// operator narrowing and must still be refused, naming the allowlist column.
	t.Run("near-default operator allowlist still refused", func(t *testing.T) {
		var names []string
		if err := json.Unmarshal([]byte(delegscope.DefaultToolsJSON), &names); err != nil {
			t.Fatal(err)
		}
		narrowed, err := json.Marshal(names[:len(names)-1])
		if err != nil {
			t.Fatal(err)
		}
		got := cliScopeUnenforceable(&store.Worker{
			ModelProvider:     models.ProviderGrokCLI,
			ToolAllowlistJSON: string(narrowed),
		})
		if !errors.Is(got, ErrCLIScopeUnenforceable) {
			t.Fatalf("narrowed allowlist not refused: %v", got)
		}
		if !strings.Contains(got.Error(), "tool_allowlist_json") {
			t.Errorf("allowlist column not named: %v", got)
		}
	})
}

// TestWorkerAllowlistOperatorScopeSet pins the operator-vs-default distinction
// the guard fires on, independent of provider.
func TestWorkerAllowlistOperatorScopeSet(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "empty", raw: "", want: false},
		{name: "null", raw: "null", want: false},
		{name: "sqlite default []", raw: "[]", want: false},
		{name: "execute system default", raw: delegscope.DefaultToolsJSON, want: false},
		{name: "review system default", raw: delegscope.DefaultReviewToolsJSON, want: false},
		{name: "operator restrictive list", raw: `["task__create"]`, want: true},
		{name: "operator superset of default", raw: `["mcpx__execute_code","x__extra"]`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerAllowlistOperatorScopeSet(tc.raw); got != tc.want {
				t.Errorf("workerAllowlistOperatorScopeSet(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestWorkerScopeColumnPredicates(t *testing.T) {
	tests := []struct {
		raw            string
		wantAllowlist  bool
		wantCapability bool
	}{
		{raw: "", wantAllowlist: false, wantCapability: false},
		{raw: "   ", wantAllowlist: false, wantCapability: false},
		{raw: "null", wantAllowlist: false, wantCapability: false},
		{raw: " null ", wantAllowlist: false, wantCapability: false},
		// The divergence: "[]" is the sqlite default for the allowlist
		// column, but operator-authored for the capability column.
		{raw: "[]", wantAllowlist: false, wantCapability: true},
		{raw: " [] ", wantAllowlist: false, wantCapability: true},
		{raw: "{}", wantAllowlist: true, wantCapability: true},
		{raw: `["task__create"]`, wantAllowlist: true, wantCapability: true},
		{raw: `{"preset":"coder"}`, wantAllowlist: true, wantCapability: true},
		{raw: "{not valid json", wantAllowlist: true, wantCapability: true},
		// "null" is the absent sentinel; "NULL" is not — the enforcement side
		// compares exactly, so this must not widen to a case-insensitive match
		// and let a scope column read as absent.
		{raw: "NULL", wantAllowlist: true, wantCapability: true},
	}
	for _, tc := range tests {
		if got := workerAllowlistScopeSet(tc.raw); got != tc.wantAllowlist {
			t.Errorf("workerAllowlistScopeSet(%q) = %v, want %v", tc.raw, got, tc.wantAllowlist)
		}
		if got := workerCapabilityScopeSet(tc.raw); got != tc.wantCapability {
			t.Errorf("workerCapabilityScopeSet(%q) = %v, want %v", tc.raw, got, tc.wantCapability)
		}
	}
}
