package models

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Provider names accepted by NewAdapter.
const (
	ProviderAnthropic    = "anthropic"
	ProviderOpenAI       = "openai"
	ProviderOpenAICompat = "openai_compat"
	// ProviderClaudeCLI shells out to the locally-installed `claude`
	// binary. Bills via whatever credentials that install uses (OAuth
	// subscription → free under the user's Pro/Max plan; api key →
	// per-token). APIKey + EndpointURL on Config are ignored; the only
	// inputs are ModelID (passed to --model) and an optional binary
	// path supplied through EndpointURL when overriding the default
	// `claude` lookup on PATH (typically for tests).
	ProviderClaudeCLI = "claude_cli"
	// ProviderOpenCodeCLI shells out to the locally-installed `opencode`
	// CLI in non-interactive JSON mode. opencode itself owns provider
	// routing and credentials, so this adapter ignores APIKey entirely;
	// ModelID is the `provider/model` slug opencode expects (e.g.
	// `minimax/MiniMax-M3`, `anthropic/claude-sonnet-4-6`). EndpointURL
	// can override the default `opencode` binary lookup on PATH, or point
	// at a long-lived opencode server URL. HTTP(S) endpoint URLs are passed
	// through as `opencode run --attach <url>` to avoid concurrent
	// per-process session DB writers.
	ProviderOpenCodeCLI = "opencode_cli"
	// ProviderGrokCLI shells out to xAI's locally-installed `grok` CLI
	// in headless JSON mode. Credentials remain with the host Grok
	// install (`grok login` or XAI_API_KEY). EndpointURL can override
	// the default `grok` binary lookup, typically for tests or custom
	// install locations.
	ProviderGrokCLI = "grok_cli"
	// ProviderMiMoCLI shells out to Xiaomi's native `mimo` / mimocode CLI
	// in non-interactive JSON mode. Credentials remain with the host MiMo
	// install (`mimo providers login` / auth.json). EndpointURL can override
	// the default `mimo` binary lookup, or point at a long-lived mimocode
	// server URL via `mimo run --attach <url>`.
	ProviderMiMoCLI = "mimo_cli"
	// ProviderGeminiCLI shells out to Google's `gemini` CLI in
	// non-interactive JSON mode. Credentials remain with the host Gemini
	// install (GEMINI_API_KEY or `gemini auth`). EndpointURL can override
	// the default `gemini` binary lookup.
	ProviderGeminiCLI = "gemini_cli"
	ProviderCodexCLI  = "codex_cli"
	// ProviderPiCLI shells out to the locally-installed `pi` CLI (the Pi
	// coding harness, pi.dev / @earendil-works/pi-coding-agent) in
	// non-interactive JSON mode. The model + provider routing live in the
	// host's ~/.pi/agent/models.json — Pi resolves the provider key from
	// ModelID itself — so the adapter ignores APIKey and passes only
	// --model <ModelID>. EndpointURL can override the default `pi` binary
	// lookup, typically for tests or custom install locations.
	ProviderPiCLI = "pi_cli"
)

// IsCLIProvider reports subprocess CLI adapters whose tool calls bypass
// the runner's gateway dispatch loop (claude_cli, opencode_cli, grok_cli,
// mimo_cli, gemini_cli, codex_cli, pi_cli).
func IsCLIProvider(provider string) bool {
	switch provider {
	case ProviderClaudeCLI, ProviderOpenCodeCLI, ProviderGrokCLI, ProviderMiMoCLI, ProviderGeminiCLI, ProviderCodexCLI, ProviderPiCLI:
		return true
	default:
		return false
	}
}

// Config describes one model adapter. APIKey is plaintext; callers are
// responsible for resolving secrets from the AuthScope store before
// constructing an adapter.
type Config struct {
	Provider    string
	ModelID     string
	APIKey      string
	EndpointURL string // empty for native providers; required for openai_compat
	HTTPClient  *http.Client
	// Thinking is an optional OpenAI-compatible extension. "enabled" and
	// "disabled" are forwarded as {"thinking":{"type":...}} only by the
	// openai_compat adapter; native OpenAI requests remain unchanged.
	Thinking string
}

// ErrMissingAPIKey is returned when Config.APIKey is empty for a provider
// that requires authentication.
var ErrMissingAPIKey = errors.New("models: missing API key")

// ErrMissingModelID is returned when Config.ModelID is empty.
var ErrMissingModelID = errors.New("models: missing model id")

// ErrMissingEndpoint is returned when an openai_compat config has no
// EndpointURL.
var ErrMissingEndpoint = errors.New("models: openai_compat requires EndpointURL")

// ErrClaudeCLINotAllowed is returned when ProviderClaudeCLI is
// requested but the daemon was not started with
// MCPLEXER_ALLOW_CLAUDE_CLI=1. claude_cli runs with NetworkHost
// (unrestricted egress) until the mcplexer-proxy UDS lands — opt-in
// via env var reduces blast radius in the meantime.
//
// SECURITY (H4): A compromised `claude` binary or prompt-injection
// that triggers e.g. `curl evil.example.com` succeeds today because
// the sandbox profile leaves outbound network on. The env gate makes
// the operator acknowledge that risk explicitly rather than enabling
// it by default for everyone who installed mcplexer.
var ErrClaudeCLINotAllowed = errors.New(
	"models: claude_cli requires MCPLEXER_ALLOW_CLAUDE_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

// ErrOpenCodeCLINotAllowed mirrors ErrClaudeCLINotAllowed for the
// opencode_cli adapter. opencode_cli ALSO runs with NetworkHost (see
// opencodeCLISandboxConfig) so the same opt-in posture applies — the
// previous "no env gate needed" comment was wrong.
var ErrOpenCodeCLINotAllowed = errors.New(
	"models: opencode_cli requires MCPLEXER_ALLOW_OPENCODE_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

// ErrGrokCLINotAllowed mirrors the other subprocess adapters. grok_cli
// runs with NetworkHost until proxy-mediated egress lands, so the
// operator must explicitly opt in.
var ErrGrokCLINotAllowed = errors.New(
	"models: grok_cli requires MCPLEXER_ALLOW_GROK_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

// ErrMiMoCLINotAllowed mirrors the other subprocess adapters. mimo_cli
// runs with NetworkHost until proxy-mediated egress lands, so the
// operator must explicitly opt in.
var ErrMiMoCLINotAllowed = errors.New(
	"models: mimo_cli requires MCPLEXER_ALLOW_MIMO_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

// ErrGeminiCLINotAllowed mirrors the other subprocess adapters. gemini_cli
// runs with NetworkHost until proxy-mediated egress lands, so the
// operator must explicitly opt in.
var ErrGeminiCLINotAllowed = errors.New(
	"models: gemini_cli requires MCPLEXER_ALLOW_GEMINI_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

var ErrCodexCLINotAllowed = errors.New(
	"models: codex_cli requires MCPLEXER_ALLOW_CODEX_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

// ErrPiCLINotAllowed mirrors the other subprocess adapters. pi_cli runs
// with NetworkHost until proxy-mediated egress lands, so the operator must
// explicitly opt in.
var ErrPiCLINotAllowed = errors.New(
	"models: pi_cli requires MCPLEXER_ALLOW_PI_CLI=1 " +
		"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
)

// claudeCLIAllowEnvVar is the env var the daemon checks to permit
// claude_cli adapters. Any value other than "1" (including unset)
// disables the provider. Exported as a const so tests can t.Setenv
// without repeating the magic string.
const claudeCLIAllowEnvVar = "MCPLEXER_ALLOW_CLAUDE_CLI"

// opencodeCLIAllowEnvVar gates the opencode_cli adapter. Same semantics
// as claudeCLIAllowEnvVar.
const opencodeCLIAllowEnvVar = "MCPLEXER_ALLOW_OPENCODE_CLI"

// grokCLIAllowEnvVar gates the grok_cli adapter. Same semantics as the
// other subprocess adapter env gates.
const grokCLIAllowEnvVar = "MCPLEXER_ALLOW_GROK_CLI"

// mimoCLIAllowEnvVar gates the mimo_cli adapter. Same semantics as the
// other subprocess adapter env gates.
const mimoCLIAllowEnvVar = "MCPLEXER_ALLOW_MIMO_CLI"

// geminiCLIAllowEnvVar gates the gemini_cli adapter. Same semantics as the
// other subprocess adapter env gates.
const geminiCLIAllowEnvVar = "MCPLEXER_ALLOW_GEMINI_CLI"

const codexCLIAllowEnvVar = "MCPLEXER_ALLOW_CODEX_CLI"

// piCLIAllowEnvVar gates the pi_cli adapter. Same semantics as the other
// subprocess adapter env gates.
const piCLIAllowEnvVar = "MCPLEXER_ALLOW_PI_CLI"

// claudeCLIAllowed reports whether the env opt-in is set. Centralized
// so the factory and the admin validator agree on the rule.
func claudeCLIAllowed() bool {
	return os.Getenv(claudeCLIAllowEnvVar) == "1"
}

// opencodeCLIAllowed mirrors claudeCLIAllowed for opencode_cli.
func opencodeCLIAllowed() bool {
	return os.Getenv(opencodeCLIAllowEnvVar) == "1"
}

func grokCLIAllowed() bool {
	return os.Getenv(grokCLIAllowEnvVar) == "1"
}

func mimoCLIAllowed() bool {
	return os.Getenv(mimoCLIAllowEnvVar) == "1"
}

func geminiCLIAllowed() bool {
	return os.Getenv(geminiCLIAllowEnvVar) == "1"
}

func codexCLIAllowed() bool {
	return os.Getenv(codexCLIAllowEnvVar) == "1"
}

func piCLIAllowed() bool {
	return os.Getenv(piCLIAllowEnvVar) == "1"
}

// CLIProviderAllowed reports whether a subprocess CLI provider's env opt-in
// is set. Non-CLI providers are never env-gated, so they return true.
// Exported so surfaces like the consolidator status can explain WHY a
// scheduled CLI worker can't run without duplicating the env-var rules.
func CLIProviderAllowed(provider string) bool {
	switch provider {
	case ProviderClaudeCLI:
		return claudeCLIAllowed()
	case "opencode_cli":
		return opencodeCLIAllowed()
	case "grok_cli":
		return grokCLIAllowed()
	case "mimo_cli":
		return mimoCLIAllowed()
	case "gemini_cli":
		return geminiCLIAllowed()
	case "codex_cli":
		return codexCLIAllowed()
	case "pi_cli":
		return piCLIAllowed()
	default:
		return true
	}
}

// CLIProviderEnvVar returns the env var that gates a CLI provider, or "" for
// providers that aren't env-gated. Used to render actionable "set X=1"
// hints when a scheduled CLI worker is installed but can't run.
func CLIProviderEnvVar(provider string) string {
	switch provider {
	case ProviderClaudeCLI:
		return claudeCLIAllowEnvVar
	case "opencode_cli":
		return opencodeCLIAllowEnvVar
	case "grok_cli":
		return grokCLIAllowEnvVar
	case "mimo_cli":
		return mimoCLIAllowEnvVar
	case "gemini_cli":
		return geminiCLIAllowEnvVar
	case "codex_cli":
		return codexCLIAllowEnvVar
	case "pi_cli":
		return piCLIAllowEnvVar
	default:
		return ""
	}
}

// NewAdapter constructs the right adapter for cfg.Provider.
func NewAdapter(cfg Config) (ModelAdapter, error) {
	if cfg.ModelID == "" {
		return nil, ErrMissingModelID
	}
	client := cfg.HTTPClient
	if client == nil {
		// 180s, not 60s: reasoning models (GLM-5.2, o-series, …) can take
		// well over a minute to first byte on a large context before they
		// start streaming. The caller's context deadline (worker wall-clock)
		// is the real per-run bound; this client ceiling only guards against
		// a truly wedged connection. 60s was cutting off valid slow replies
		// with "Client.Timeout exceeded while awaiting headers".
		client = &http.Client{Timeout: 180 * time.Second}
	}

	switch cfg.Provider {
	case ProviderAnthropic:
		if cfg.APIKey == "" {
			return nil, ErrMissingAPIKey
		}
		return newAnthropicAdapter(cfg.APIKey, cfg.ModelID, cfg.EndpointURL, client), nil
	case ProviderOpenAI:
		if cfg.APIKey == "" {
			return nil, ErrMissingAPIKey
		}
		return newOpenAIAdapter(cfg.APIKey, cfg.ModelID, cfg.EndpointURL, client), nil
	case ProviderOpenAICompat:
		if cfg.EndpointURL == "" {
			return nil, ErrMissingEndpoint
		}
		if cfg.Thinking != "" && cfg.Thinking != "enabled" && cfg.Thinking != "disabled" {
			return nil, fmt.Errorf("models: openai_compat thinking must be enabled or disabled")
		}
		adapter := newOpenAICompatAdapter(cfg.APIKey, cfg.ModelID, cfg.EndpointURL, client)
		adapter.thinking = cfg.Thinking
		return adapter, nil
	case ProviderClaudeCLI:
		if !claudeCLIAllowed() {
			return nil, ErrClaudeCLINotAllowed
		}
		return newClaudeCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	case ProviderOpenCodeCLI:
		if !opencodeCLIAllowed() {
			return nil, ErrOpenCodeCLINotAllowed
		}
		return newOpenCodeCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	case ProviderGrokCLI:
		if !grokCLIAllowed() {
			return nil, ErrGrokCLINotAllowed
		}
		return newGrokCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	case ProviderMiMoCLI:
		if !mimoCLIAllowed() {
			return nil, ErrMiMoCLINotAllowed
		}
		return newMiMoCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	case ProviderGeminiCLI:
		if !geminiCLIAllowed() {
			return nil, ErrGeminiCLINotAllowed
		}
		return newGeminiCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	case ProviderCodexCLI:
		if !codexCLIAllowed() {
			return nil, ErrCodexCLINotAllowed
		}
		return newCodexCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	case ProviderPiCLI:
		if !piCLIAllowed() {
			return nil, ErrPiCLINotAllowed
		}
		return newPiCLIAdapter(cfg.EndpointURL, cfg.ModelID), nil
	default:
		return nil, fmt.Errorf("models: unknown provider %q", cfg.Provider)
	}
}
