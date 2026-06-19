// Package assist implements the Brain GUI's lean AI augmentation path.
// It constructs a models.Adapter DIRECTLY
// — never the worker runner — because the runner is built for scheduled,
// tool-using, billed jobs, not for the sub-100ms interactive latency the
// ghost-text + memory-candidate surfaces need (RESEARCH 2).
//
// Two behaviours:
//   - Complete: ghost-text continuation of the text the user is typing.
//   - MemoryCandidates: 0..N decision-with-rationale candidates worth
//     persisting as memories, deduped by content-hash, signal-classified.
//
// Both resolve a models.ModelProfile -> models.Config, fetch the optional
// API key from the SecretReader, and call adapter.Send once. The adapter
// interface is one-shot (Send), so true per-token streaming lives in the
// HTTP layer (word-chunked SSE over the single Send result) rather than in
// every provider adapter — a deliberate scope decision documented at the
// SSE handler.
//
// When no usable model profile exists, every call returns ErrNoProfile so
// the GUI degrades silently (ghost text simply absent, no nag) — the
// explicit law from DESIGN §3.4.
package assist

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

// ErrNoProfile is returned by Complete / MemoryCandidates when no model
// profile can be resolved (none configured, or the requested one is
// missing). The HTTP layer maps it to 204 so the GUI degrades silently.
var ErrNoProfile = errors.New("assist: no usable model profile configured")

// SecretReader reads one key out of an AuthScope. Implemented by
// *secrets.Manager (its Get signature already matches). nil-safe: when the
// resolved profile carries no SecretScopeID the reader is never consulted.
type SecretReader interface {
	Get(ctx context.Context, scopeID, key string) ([]byte, error)
}

// MemoryCandidate is one existing memory the link-related-memory nudge can
// ground against: the unique name (the slug the [[ref]] resolves to) plus a
// short title for the model to judge relevance. Kept deliberately slim so the
// assist package never depends on brain's record types.
type MemoryCandidate struct {
	Name  string
	Title string
}

// MemoryIndex returns the existing memories in a workspace's fused scope so the
// link-related-memory nudge proposes a [[ref]] that ACTUALLY resolves, instead
// of letting the model invent a kebab-case slug (DESIGN §4.4). Implemented by a
// thin adapter over the brain Editor's Search (the same FTS5 path the GUI
// typeahead uses). nil-safe: when no index is wired the nudge is suppressed
// (an ungrounded nudge would mint dangling refs — worse than no nudge).
type MemoryIndex interface {
	SearchMemories(ctx context.Context, query, workspace string, limit int) ([]MemoryCandidate, error)
}

// adapterFactory builds a ModelAdapter from a Config. Defaults to
// models.NewAdapter; tests inject a fake that records the rendered prompt
// and returns a canned completion.
type adapterFactory func(cfg models.Config) (models.ModelAdapter, error)

// Assistant is the lean assist engine. Construct via New; the store +
// secrets are the only collaborators (no gateway, no runner).
type Assistant struct {
	store   store.ModelProfileStore
	secrets SecretReader
	index   MemoryIndex
	client  *http.Client
	factory adapterFactory
}

// New wires an Assistant. secrets may be nil (profiles without a key still
// work — e.g. an openai_compat / opencode-local local endpoint). client
// defaults to a short-timeout client tuned for interactive latency.
func New(s store.ModelProfileStore, secrets SecretReader, client *http.Client) *Assistant {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Assistant{store: s, secrets: secrets, client: client, factory: models.NewAdapter}
}

// WithMemoryIndex returns a (copy of the) Assistant wired to ground the
// link-related-memory nudge against the live index. Without it that nudge is
// suppressed so the GUI never mints a dangling [[ref]] (DESIGN §4.4).
func (a *Assistant) WithMemoryIndex(idx MemoryIndex) *Assistant {
	if a == nil {
		return nil
	}
	cp := *a
	cp.index = idx
	return &cp
}

// CompleteRequest is one ghost-text continuation ask. Context is the text
// BEFORE the caret (already truncated client-side to a sane window); Cursor
// is the (optional) text AFTER the caret for fill-in-the-middle awareness.
type CompleteRequest struct {
	Context      string `json:"context"`
	Cursor       string `json:"cursor,omitempty"`
	Field        string `json:"field,omitempty"` // title|description|content|name
	Workspace    string `json:"workspace,omitempty"`
	ModelProfile string `json:"model_profile,omitempty"`
}

// Complete returns the ghost-text continuation for req. The model is asked
// for a short continuation only — no preamble, no restatement — so the
// returned string can be appended verbatim after the caret. Returns
// ErrNoProfile when no profile resolves (caller -> 204).
func (a *Assistant) Complete(ctx context.Context, req CompleteRequest) (string, string, error) {
	cfg, profileName, err := a.resolveConfig(ctx, req.ModelProfile)
	if err != nil {
		return "", "", err
	}
	adapter, err := a.factory(cfg)
	if err != nil {
		// A resolved-but-undrivable profile (gated CLI, key/endpoint that
		// didn't load) must degrade silently, not 502 on every keystroke
		// (DESIGN §3.4). Reserve the hard-error path for Send() runtime
		// failures below.
		if isUnusableProfileErr(err) {
			return "", "", ErrNoProfile
		}
		return "", "", err
	}
	out, err := adapter.Send(ctx, models.SendRequest{
		System:    completeSystemPrompt(req.Field),
		Messages:  []models.Message{{Role: models.RoleUser, Content: completeUserPrompt(req)}},
		MaxTokens: 96,
		Stop:      []string{"\n\n"},
	})
	if err != nil {
		return "", profileName, err
	}
	return cleanCompletion(out.Text, req.Context), profileName, nil
}

// resolveConfig picks the model profile (by name, else the first usable
// one), fetches its optional API key, and returns the models.Config + the
// profile name (for the GUI's `model · <profile>` provenance label).
func (a *Assistant) resolveConfig(ctx context.Context, name string) (models.Config, string, error) {
	if a.store == nil {
		return models.Config{}, "", ErrNoProfile
	}
	prof, ok, err := a.pickProfile(ctx, name)
	if err != nil {
		return models.Config{}, "", err
	}
	if !ok {
		return models.Config{}, "", ErrNoProfile
	}
	cfg, err := a.buildConfig(ctx, prof)
	if err != nil {
		return models.Config{}, "", err
	}
	return cfg, prof.Name, nil
}

// buildConfig assembles the models.Config for a resolved profile: its first
// known model, its optional API key, endpoint, and the shared HTTP client.
// Returns ErrNoProfile when the profile has no known model (can't be driven
// directly) so the GUI degrades rather than 500ing.
func (a *Assistant) buildConfig(ctx context.Context, prof store.ModelProfile) (models.Config, error) {
	modelID := firstKnownModel(prof)
	if modelID == "" {
		return models.Config{}, ErrNoProfile
	}
	apiKey, err := a.resolveKey(ctx, prof)
	if err != nil {
		return models.Config{}, err
	}
	return models.Config{
		Provider:    prof.Provider,
		ModelID:     modelID,
		APIKey:      apiKey,
		EndpointURL: prof.EndpointURL,
		HTTPClient:  a.client,
	}, nil
}

// isUnusableProfileErr reports whether err from the adapter factory means the
// profile is RESOLVED but cannot be driven under the current env/secret state
// — a gated CLI (no opt-in), or a provider missing its required key/endpoint.
// These degrade to ErrNoProfile (GUI shows nothing) rather than a 502 nag.
// ErrNoProfile itself (from buildConfig's model-less guard) also counts.
func isUnusableProfileErr(err error) bool {
	return errors.Is(err, ErrNoProfile) ||
		errors.Is(err, models.ErrClaudeCLINotAllowed) ||
		errors.Is(err, models.ErrOpenCodeCLINotAllowed) ||
		errors.Is(err, models.ErrMissingAPIKey) ||
		errors.Is(err, models.ErrMissingEndpoint) ||
		errors.Is(err, models.ErrMissingModelID)
}

// pickProfile resolves the named profile, or — when name is empty — the first
// profile whose adapter is actually constructible under the current
// env/secret state (so the GUI works out of the box against whatever the
// operator configured, falling through a gated claude_cli / key-less provider
// to a later usable openai_compat/local profile). Reports ok=false when
// nothing usable exists.
//
// The named-profile branch stays strict: an explicit choice is returned as-is
// so Complete/MemoryCandidates can surface WHY it can't run (or degrade per
// the silent-degrade contract) rather than silently swapping in a different
// model the operator didn't ask for.
func (a *Assistant) pickProfile(ctx context.Context, name string) (store.ModelProfile, bool, error) {
	if strings.TrimSpace(name) != "" {
		p, err := a.store.GetModelProfileByName(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			return store.ModelProfile{}, false, nil
		}
		if err != nil {
			return store.ModelProfile{}, false, err
		}
		return p, true, nil
	}
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		return store.ModelProfile{}, false, err
	}
	for _, p := range profiles {
		if a.profileUsable(ctx, p) {
			return p, true, nil
		}
	}
	return store.ModelProfile{}, false, nil
}

// profileUsable dry-runs the adapter factory for p and reports whether it
// yields a constructible adapter under the current env/secret state. Used by
// the auto-select branch to skip a gated/keyless first-listed profile and
// fall through to a usable one. Any build error (model-less, gated CLI,
// missing key/endpoint) makes the profile non-usable for auto-select.
func (a *Assistant) profileUsable(ctx context.Context, p store.ModelProfile) bool {
	cfg, err := a.buildConfig(ctx, p)
	if err != nil {
		return false
	}
	if _, err := a.factory(cfg); err != nil {
		return false
	}
	return true
}

// resolveKey loads the profile's API key when it carries a SecretScopeID.
// claude_cli / opencode_cli / grok_cli / mimo_cli inherit host credentials and need no key; an
// openai_compat local endpoint may also run keyless — both short-circuit
// to "" and let models.NewAdapter decide whether the empty key is fatal.
func (a *Assistant) resolveKey(ctx context.Context, p store.ModelProfile) (string, error) {
	if p.SecretScopeID == "" ||
		p.Provider == models.ProviderClaudeCLI ||
		p.Provider == models.ProviderOpenCodeCLI ||
		p.Provider == models.ProviderGrokCLI ||
		p.Provider == models.ProviderMiMoCLI ||
		p.Provider == models.ProviderGeminiCLI ||
		p.Provider == models.ProviderPiCLI {
		return "", nil
	}
	if a.secrets == nil {
		return "", nil
	}
	v, err := a.secrets.Get(ctx, p.SecretScopeID, "api_key")
	if err != nil {
		// A missing key is not fatal for keyless endpoints; return empty and
		// let the adapter constructor reject if the provider truly requires
		// one. This keeps the silent-degrade contract intact.
		return "", nil
	}
	return string(v), nil
}

// firstKnownModel returns the profile's first known model id, or "".
func firstKnownModel(p store.ModelProfile) string {
	for _, m := range p.KnownModels {
		if strings.TrimSpace(m) != "" {
			return m
		}
	}
	return ""
}
