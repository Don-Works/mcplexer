// model_profile_service.go — the shared validation + mutation core for
// ModelProfile rows. Both the HTTP handlers (model_profile.go) and the
// CWD-gated mcplexer__*_model_profile MCP tools (internal/control)
// dispatch through this single service so the two surfaces can never
// drift on the load-bearing rules:
//
//   - Builtin=true rows refuse Update / Delete (the local default
//     profile must stay unmuteable).
//   - secret_scope_id is required for the non-CLI providers
//     (anthropic / openai / openai_compat); CLI providers
//     (claude_cli / opencode_cli / grok_cli / mimo_cli / gemini_cli /
//     codex_cli / pi_cli) are exempt because they own their own creds.
//   - endpoint_url is required for openai_compat.
//   - the API/MCP layer can never mint or flip Builtin=true.
//
// Validation lives in validateModelProfile (model_profile.go); this file
// owns the orchestration (lookup → Builtin guard → validate → store) and
// the typed sentinel errors the two transport layers map to their own
// status vocabulary.
package admin

import (
	"context"
	"errors"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrModelProfileBuiltin is returned by UpdateModelProfile /
// DeleteModelProfile when the target row is daemon-managed (Builtin=true).
// The HTTP layer maps it to 403; the MCP layer maps it to a structured
// errorResult. Distinct from store.ErrNotFound so callers can tell
// "doesn't exist" apart from "exists but protected".
var ErrModelProfileBuiltin = errors.New("model profile is builtin and cannot be modified")

// ModelProfileValidationError wraps a validateModelProfile failure so the
// transport layers can map "the caller sent something invalid" (400 /
// structured error) apart from a store failure (500). The HTTP handler
// validated before touching the store; now that validation runs inside
// the core, this typed wrapper restores that distinction.
type ModelProfileValidationError struct{ Err error }

func (e ModelProfileValidationError) Error() string { return e.Err.Error() }
func (e ModelProfileValidationError) Unwrap() error { return e.Err }

// isModelProfileValidationErr reports whether err is (or wraps) a
// ModelProfileValidationError.
func isModelProfileValidationErr(err error) bool {
	var v ModelProfileValidationError
	return errors.As(err, &v)
}

// ModelProfileCore is the transport-neutral facade over
// store.ModelProfileStore. Construct via NewModelProfileCore, or reach
// it through *Service (which embeds one) so the worker-admin MCP tools
// and the HTTP handlers share the exact same instance behaviour.
type ModelProfileCore struct {
	store store.ModelProfileStore
}

// NewModelProfileCore wraps a ModelProfileStore.
func NewModelProfileCore(s store.ModelProfileStore) *ModelProfileCore {
	return &ModelProfileCore{store: s}
}

// ModelProfilePatch carries the mutable fields for a partial update.
// A nil pointer means "leave this field unchanged"; a non-nil pointer
// (including the empty value it points at) means "set to this value".
// The HTTP full-replace handler populates every field; the MCP
// update_model_profile tool populates only what the caller sent.
type ModelProfilePatch struct {
	Name          *string   `json:"name,omitempty"`
	Provider      *string   `json:"provider,omitempty"`
	EndpointURL   *string   `json:"endpoint_url,omitempty"`
	SecretScopeID *string   `json:"secret_scope_id,omitempty"`
	KnownModels   *[]string `json:"known_models,omitempty"`
}

// List returns every profile ordered by name ASC. nil is normalised to
// an empty slice so transport layers always serialise a JSON array.
func (c *ModelProfileCore) List(ctx context.Context) ([]store.ModelProfile, error) {
	profiles, err := c.store.ListModelProfiles(ctx)
	if err != nil {
		return nil, err
	}
	if profiles == nil {
		profiles = []store.ModelProfile{}
	}
	return profiles, nil
}

// Get returns one profile by id. Surfaces store.ErrNotFound verbatim.
func (c *ModelProfileCore) Get(ctx context.Context, id string) (store.ModelProfile, error) {
	return c.store.GetModelProfile(ctx, id)
}

// Create validates and inserts a new profile. Builtin is always forced
// false — neither the API nor the MCP surface may mint a daemon-managed
// row. Returns store.ErrAlreadyExists on a unique-name conflict.
func (c *ModelProfileCore) Create(ctx context.Context, p *store.ModelProfile) (store.ModelProfile, error) {
	// Builtin is daemon-managed — refuse to let any caller mint a builtin row.
	p.Builtin = false
	if err := validateModelProfile(p); err != nil {
		return store.ModelProfile{}, ModelProfileValidationError{Err: err}
	}
	if err := c.store.CreateModelProfile(ctx, p); err != nil {
		return store.ModelProfile{}, err
	}
	return *p, nil
}

// Update applies a partial patch to an existing profile. It refuses
// Builtin rows (ErrModelProfileBuiltin), re-validates the merged result,
// and preserves the existing Builtin flag. Returns store.ErrNotFound
// when the id is unknown and store.ErrAlreadyExists on a rename clash.
func (c *ModelProfileCore) Update(
	ctx context.Context, id string, patch ModelProfilePatch,
) (store.ModelProfile, error) {
	existing, err := c.store.GetModelProfile(ctx, id)
	if err != nil {
		return store.ModelProfile{}, err
	}
	if existing.Builtin {
		return store.ModelProfile{}, ErrModelProfileBuiltin
	}
	merged := applyModelProfilePatch(existing, patch)
	merged.ID = id
	// Builtin is daemon-managed; preserve whatever the existing row had
	// (which we've already confirmed is false above).
	merged.Builtin = existing.Builtin
	if err := validateModelProfile(&merged); err != nil {
		return store.ModelProfile{}, ModelProfileValidationError{Err: err}
	}
	if err := c.store.UpdateModelProfile(ctx, &merged); err != nil {
		return store.ModelProfile{}, err
	}
	return merged, nil
}

// SetKnownModels is the convenience path for the core use case: curating
// the delegation candidate pool without read-modify-writing the whole
// profile. It is Update with only KnownModels set, so it inherits the
// Builtin guard and full re-validation.
func (c *ModelProfileCore) SetKnownModels(
	ctx context.Context, id string, models []string,
) (store.ModelProfile, error) {
	if models == nil {
		models = []string{}
	}
	return c.Update(ctx, id, ModelProfilePatch{KnownModels: &models})
}

// Delete hard-deletes a profile after the Builtin guard. Workers
// referencing it have model_profile_id set to NULL by the FK's
// ON DELETE SET NULL clause (migration 056).
func (c *ModelProfileCore) Delete(ctx context.Context, id string) error {
	existing, err := c.store.GetModelProfile(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return ErrModelProfileBuiltin
	}
	return c.store.DeleteModelProfile(ctx, id)
}

// applyModelProfilePatch returns a copy of base with each non-nil patch
// field overlaid. KnownModels is replaced wholesale (set semantics, not
// append) — the curation tools own the whole list.
func applyModelProfilePatch(base store.ModelProfile, patch ModelProfilePatch) store.ModelProfile {
	out := base
	if patch.Name != nil {
		out.Name = *patch.Name
	}
	if patch.Provider != nil {
		out.Provider = *patch.Provider
	}
	if patch.EndpointURL != nil {
		out.EndpointURL = *patch.EndpointURL
	}
	if patch.SecretScopeID != nil {
		out.SecretScopeID = *patch.SecretScopeID
	}
	if patch.KnownModels != nil {
		out.KnownModels = *patch.KnownModels
	}
	return out
}
