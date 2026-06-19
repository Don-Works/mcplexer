package models

import (
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestNewAdapterValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{
			name:    "missing model id",
			cfg:     Config{Provider: ProviderAnthropic, APIKey: "k"},
			wantErr: ErrMissingModelID,
		},
		{
			name:    "anthropic without key",
			cfg:     Config{Provider: ProviderAnthropic, ModelID: "claude-opus-4-7"},
			wantErr: ErrMissingAPIKey,
		},
		{
			name:    "openai without key",
			cfg:     Config{Provider: ProviderOpenAI, ModelID: "gpt-4o"},
			wantErr: ErrMissingAPIKey,
		},
		{
			name:    "compat without endpoint",
			cfg:     Config{Provider: ProviderOpenAICompat, ModelID: "x", APIKey: "k"},
			wantErr: ErrMissingEndpoint,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewAdapter(c.cfg)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestNewAdapterUnknownProvider(t *testing.T) {
	_, err := NewAdapter(Config{Provider: "fake", ModelID: "x"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestNewAdapterReturnsRightTypes(t *testing.T) {
	// claude_cli cases need the env opt-in (H4) — set once for the whole
	// table so the non-cli cases don't notice and the cli cases succeed.
	t.Setenv(claudeCLIAllowEnvVar, "1")
	t.Setenv(grokCLIAllowEnvVar, "1")
	t.Setenv(mimoCLIAllowEnvVar, "1")
	t.Setenv(codexCLIAllowEnvVar, "1")
	t.Setenv(piCLIAllowEnvVar, "1")
	cases := []struct {
		name string
		cfg  Config
	}{
		{"anthropic", Config{Provider: ProviderAnthropic, ModelID: "claude-opus-4-7", APIKey: "k"}},
		{"openai", Config{Provider: ProviderOpenAI, ModelID: "gpt-4o", APIKey: "k"}},
		{"compat", Config{Provider: ProviderOpenAICompat, ModelID: "ds", APIKey: "k", EndpointURL: "https://api.deepseek.com"}},
		{"claude_cli", Config{Provider: ProviderClaudeCLI, ModelID: "sonnet"}},
		{"claude_cli with binary path", Config{Provider: ProviderClaudeCLI, ModelID: "sonnet", EndpointURL: "/usr/local/bin/claude"}},
		{"grok_cli", Config{Provider: ProviderGrokCLI, ModelID: "grok-build"}},
		{"grok_cli with binary path", Config{Provider: ProviderGrokCLI, ModelID: "grok-build", EndpointURL: "/usr/local/bin/grok"}},
		{"mimo_cli", Config{Provider: ProviderMiMoCLI, ModelID: "xiaomi/mimo-v2.5"}},
		{"mimo_cli with binary path", Config{Provider: ProviderMiMoCLI, ModelID: "xiaomi/mimo-v2.5", EndpointURL: "/usr/local/bin/mimo"}},
		{"codex_cli", Config{Provider: ProviderCodexCLI, ModelID: "o3"}},
		{"codex_cli with binary path", Config{Provider: ProviderCodexCLI, ModelID: "o3", EndpointURL: "/usr/local/bin/codex"}},
		{"pi_cli", Config{Provider: ProviderPiCLI, ModelID: "local-model"}},
		{"pi_cli with binary path", Config{Provider: ProviderPiCLI, ModelID: "local-model", EndpointURL: "/usr/local/bin/pi"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := NewAdapter(c.cfg)
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			if a == nil {
				t.Fatal("adapter is nil")
			}
		})
	}
}

// TestNewAdapterClaudeCLIRequiresEnvOptIn covers the H4 gate: without
// MCPLEXER_ALLOW_CLAUDE_CLI=1, constructing a claude_cli adapter must
// fail with the documented sentinel. With the env set, construction
// succeeds. The env gate exists because claude_cli runs with
// NetworkHost (unrestricted egress) until the mcplexer-proxy UDS lands.
func TestNewAdapterClaudeCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		// t.Setenv with "" still SETS the var. To assert the unset path
		// reliably we explicitly Unsetenv via the deferred restorer.
		t.Setenv(claudeCLIAllowEnvVar, "")
		_ = os.Unsetenv(claudeCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderClaudeCLI, ModelID: "sonnet"})
		if !errors.Is(err, ErrClaudeCLINotAllowed) {
			t.Fatalf("err = %v, want ErrClaudeCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(claudeCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderClaudeCLI, ModelID: "sonnet"})
		if !errors.Is(err, ErrClaudeCLINotAllowed) {
			t.Fatalf("err = %v, want ErrClaudeCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(claudeCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderClaudeCLI, ModelID: "sonnet"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*claudeCLIAdapter); !ok {
			t.Fatalf("type = %T, want *claudeCLIAdapter", a)
		}
	})
}

// TestNewAdapterOpenCodeCLIRequiresEnvOptIn covers H1: opencode_cli
// ALSO runs with NetworkHost, so it carries the same egress blast
// radius as claude_cli. Mirrors the claude_cli env-gate test — without
// MCPLEXER_ALLOW_OPENCODE_CLI=1 the factory must refuse to construct
// the adapter, with the env set construction succeeds.
func TestNewAdapterOpenCodeCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		t.Setenv(opencodeCLIAllowEnvVar, "")
		_ = os.Unsetenv(opencodeCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderOpenCodeCLI, ModelID: "minimax/MiniMax-M3"})
		if !errors.Is(err, ErrOpenCodeCLINotAllowed) {
			t.Fatalf("err = %v, want ErrOpenCodeCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(opencodeCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderOpenCodeCLI, ModelID: "minimax/MiniMax-M3"})
		if !errors.Is(err, ErrOpenCodeCLINotAllowed) {
			t.Fatalf("err = %v, want ErrOpenCodeCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(opencodeCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderOpenCodeCLI, ModelID: "minimax/MiniMax-M3"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*opencodeCLIAdapter); !ok {
			t.Fatalf("type = %T, want *opencodeCLIAdapter", a)
		}
	})
}

func TestNewAdapterGrokCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		t.Setenv(grokCLIAllowEnvVar, "")
		_ = os.Unsetenv(grokCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderGrokCLI, ModelID: "grok-build"})
		if !errors.Is(err, ErrGrokCLINotAllowed) {
			t.Fatalf("err = %v, want ErrGrokCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(grokCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderGrokCLI, ModelID: "grok-build"})
		if !errors.Is(err, ErrGrokCLINotAllowed) {
			t.Fatalf("err = %v, want ErrGrokCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(grokCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderGrokCLI, ModelID: "grok-build"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*grokCLIAdapter); !ok {
			t.Fatalf("type = %T, want *grokCLIAdapter", a)
		}
	})
}

func TestNewAdapterMiMoCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		t.Setenv(mimoCLIAllowEnvVar, "")
		_ = os.Unsetenv(mimoCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderMiMoCLI, ModelID: "xiaomi/mimo-v2.5"})
		if !errors.Is(err, ErrMiMoCLINotAllowed) {
			t.Fatalf("err = %v, want ErrMiMoCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(mimoCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderMiMoCLI, ModelID: "xiaomi/mimo-v2.5"})
		if !errors.Is(err, ErrMiMoCLINotAllowed) {
			t.Fatalf("err = %v, want ErrMiMoCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(mimoCLIAllowEnvVar, "1")
		t.Setenv(codexCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderMiMoCLI, ModelID: "xiaomi/mimo-v2.5"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*mimoCLIAdapter); !ok {
			t.Fatalf("type = %T, want *mimoCLIAdapter", a)
		}
	})
}

func TestNewAdapterGeminiCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		t.Setenv(geminiCLIAllowEnvVar, "")
		_ = os.Unsetenv(geminiCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderGeminiCLI, ModelID: "gemini-2.5-pro"})
		if !errors.Is(err, ErrGeminiCLINotAllowed) {
			t.Fatalf("err = %v, want ErrGeminiCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(geminiCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderGeminiCLI, ModelID: "gemini-2.5-pro"})
		if !errors.Is(err, ErrGeminiCLINotAllowed) {
			t.Fatalf("err = %v, want ErrGeminiCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(geminiCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderGeminiCLI, ModelID: "gemini-2.5-pro"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*geminiCLIAdapter); !ok {
			t.Fatalf("type = %T, want *geminiCLIAdapter", a)
		}
	})
}

func TestNewAdapterCodexCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		t.Setenv(codexCLIAllowEnvVar, "")
		_ = os.Unsetenv(codexCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderCodexCLI, ModelID: "o3"})
		if !errors.Is(err, ErrCodexCLINotAllowed) {
			t.Fatalf("err = %v, want ErrCodexCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(codexCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderCodexCLI, ModelID: "o3"})
		if !errors.Is(err, ErrCodexCLINotAllowed) {
			t.Fatalf("err = %v, want ErrCodexCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(codexCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderCodexCLI, ModelID: "o3"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*codexCLIAdapter); !ok {
			t.Fatalf("type = %T, want *codexCLIAdapter", a)
		}
	})
}

func TestNewAdapterPiCLIRequiresEnvOptIn(t *testing.T) {
	t.Run("unset rejects", func(t *testing.T) {
		t.Setenv(piCLIAllowEnvVar, "")
		_ = os.Unsetenv(piCLIAllowEnvVar)
		_, err := NewAdapter(Config{Provider: ProviderPiCLI, ModelID: "local-model"})
		if !errors.Is(err, ErrPiCLINotAllowed) {
			t.Fatalf("err = %v, want ErrPiCLINotAllowed", err)
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv(piCLIAllowEnvVar, "0")
		_, err := NewAdapter(Config{Provider: ProviderPiCLI, ModelID: "local-model"})
		if !errors.Is(err, ErrPiCLINotAllowed) {
			t.Fatalf("err = %v, want ErrPiCLINotAllowed", err)
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv(piCLIAllowEnvVar, "1")
		a, err := NewAdapter(Config{Provider: ProviderPiCLI, ModelID: "local-model"})
		if err != nil {
			t.Fatalf("NewAdapter: %v", err)
		}
		if _, ok := a.(*piCLIAdapter); !ok {
			t.Fatalf("type = %T, want *piCLIAdapter", a)
		}
	})
}

func TestNewAdapterAcceptsInjectedClient(t *testing.T) {
	custom := &http.Client{}
	a, err := NewAdapter(Config{
		Provider:   ProviderAnthropic,
		ModelID:    "claude-opus-4-7",
		APIKey:     "k",
		HTTPClient: custom,
	})
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	concrete, ok := a.(*anthropicAdapter)
	if !ok {
		t.Fatalf("type = %T", a)
	}
	if concrete.client != custom {
		t.Error("custom http.Client was not threaded through")
	}
}
