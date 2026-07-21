// validate_test.go — table-driven coverage for validateModelProvider,
// including the H4 env opt-in gate for claude_cli.
package admin

import (
	"os"
	"strings"
	"testing"
)

func TestValidateModelProvider_Standard(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		endpoint string
		wantErr  string // substring; "" = no error
	}{
		{"anthropic ok", "anthropic", "", ""},
		{"openai ok", "openai", "", ""},
		{"compat with endpoint ok", "openai_compat", "https://api.example.com", ""},
		{"compat without endpoint rejects", "openai_compat", "", "model_endpoint_url required"},
		{"unknown rejects", "fake", "", "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModelProvider(tc.provider, tc.endpoint)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateModelProvider_ClaudeCLIEnvGate covers the H4 rule: the
// validator must reject claude_cli when MCPLEXER_ALLOW_CLAUDE_CLI is
// unset (or any value other than "1") and accept it when set to "1".
// This stops a worker create/update from succeeding only to fail at
// schedule time with a confusing exec error.
func TestValidateModelProvider_ClaudeCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_CLAUDE_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_CLAUDE_CLI")
		err := validateModelProvider("claude_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_CLAUDE_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_CLAUDE_CLI", "0")
		if err := validateModelProvider("claude_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_CLAUDE_CLI", "1")
		if err := validateModelProvider("claude_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

// TestValidateCaps pins the sole guard against negative per-worker
// safety caps — a load-bearing budget / limit control with no other
// coverage. Zero means "default / no cap" and must pass; each cap
// negative in isolation must be rejected with a field-named message;
// large positive values pass (no upper bound is enforced by design).
func TestValidateCaps(t *testing.T) {
	cases := []struct {
		name      string
		inTokens  int
		outTokens int
		toolCalls int
		wallSecs  int
		cost      float64
		failures  int
		wantErr   string // substring; "" = no error
	}{
		{"all zero ok", 0, 0, 0, 0, 0, 0, ""},
		{"large positives ok", 10_000_000, 10_000_000, 100_000, 36_000, 9_999.99, 1_000, ""},
		{"neg input tokens", -1, 0, 0, 0, 0, 0, "max_input_tokens"},
		{"neg output tokens", 0, -1, 0, 0, 0, 0, "max_output_tokens"},
		{"neg tool calls", 0, 0, -1, 0, 0, 0, "max_tool_calls"},
		{"neg wall clock", 0, 0, 0, -1, 0, 0, "max_wall_clock_seconds"},
		{"neg consecutive failures", 0, 0, 0, 0, 0, -1, "max_consecutive_failures"},
		{"neg monthly cost", 0, 0, 0, 0, -0.01, 0, "max_monthly_cost_usd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCaps(tc.inTokens, tc.outTokens, tc.toolCalls,
				tc.wallSecs, tc.cost, tc.failures)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateModelProvider_OpenCodeCLIEnvGate covers H1: opencode_cli
// runs with NetworkHost (same as claude_cli), so it needs the same
// explicit opt-in at create/update time. The previous validator
// accepted opencode_cli unconditionally with a comment claiming
// "opencode runs without network-host privileges" — which contradicted
// the adapter (opencodeCLISandboxConfig sets Network: NetworkHost).
// This test pins the gate so a future revert wouldn't be silent.
func TestValidateModelProvider_OpenCodeCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_OPENCODE_CLI")
		err := validateModelProvider("opencode_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_OPENCODE_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "0")
		if err := validateModelProvider("opencode_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
		if err := validateModelProvider("opencode_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

func TestValidateModelProvider_GrokCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_GROK_CLI")
		err := validateModelProvider("grok_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_GROK_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "0")
		if err := validateModelProvider("grok_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
		if err := validateModelProvider("grok_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

func TestValidateModelProvider_MiMoCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_MIMO_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_MIMO_CLI")
		err := validateModelProvider("mimo_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_MIMO_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_MIMO_CLI", "0")
		if err := validateModelProvider("mimo_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_MIMO_CLI", "1")
		if err := validateModelProvider("mimo_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

func TestValidateModelProvider_GeminiCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_GEMINI_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_GEMINI_CLI")
		err := validateModelProvider("gemini_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_GEMINI_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_GEMINI_CLI", "0")
		if err := validateModelProvider("gemini_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_GEMINI_CLI", "1")
		if err := validateModelProvider("gemini_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

func TestValidateModelProvider_CodexCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_CODEX_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_CODEX_CLI")
		err := validateModelProvider("codex_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_CODEX_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_CODEX_CLI", "0")
		if err := validateModelProvider("codex_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_CODEX_CLI", "1")
		if err := validateModelProvider("codex_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

func TestValidateModelProvider_PiCLIEnvGate(t *testing.T) {
	t.Run("unset rejects with opt-in hint", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_PI_CLI", "")
		_ = os.Unsetenv("MCPLEXER_ALLOW_PI_CLI")
		err := validateModelProvider("pi_cli", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "MCPLEXER_ALLOW_PI_CLI=1") {
			t.Fatalf("err = %q, want opt-in hint", err.Error())
		}
	})
	t.Run("set to 0 rejects", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_PI_CLI", "0")
		if err := validateModelProvider("pi_cli", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
	t.Run("set to 1 accepts", func(t *testing.T) {
		t.Setenv("MCPLEXER_ALLOW_PI_CLI", "1")
		if err := validateModelProvider("pi_cli", ""); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

// TestMissingFieldExampleError locks in the copy-pasteable JSON the
// qwen/cheap-model repair path needs: the three model-related "X
// required" errors must include a corrected example derived from the
// accepted enum in this file
// (anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli),
// and other required fields (name/prompt/schedule/workspace) keep the
// bare "<field> required" form so we don't bloat unrelated errors with
// noise the model can't act on.
func TestMissingFieldExampleError(t *testing.T) {
	cases := []struct {
		field      string
		wantSub    string
		wantAbsent string
	}{
		{"model_provider", "Example:", ""},
		{"model_id", "Example:", ""},
		{"secret_scope_id", "Example:", ""},
		{"name", "", "Example:"},
		{"prompt_template", "", "Example:"},
		{"schedule_spec", "", "Example:"},
		{"workspace_id", "", "Example:"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			err := missingFieldExampleError(tc.field)
			if err == nil {
				t.Fatalf("missingFieldExampleError(%q) returned nil", tc.field)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.field+" required") {
				t.Errorf("err %q does not contain %q", msg, tc.field+" required")
			}
			if tc.wantSub != "" && !strings.Contains(msg, tc.wantSub) {
				t.Errorf("err %q does not contain %q", msg, tc.wantSub)
			}
			if tc.wantAbsent != "" && strings.Contains(msg, tc.wantAbsent) {
				t.Errorf("err %q should not contain %q", msg, tc.wantAbsent)
			}
		})
	}
}
