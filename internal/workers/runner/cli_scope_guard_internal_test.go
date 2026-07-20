package runner

import (
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

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
