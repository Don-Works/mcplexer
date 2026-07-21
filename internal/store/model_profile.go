package store

import (
	"context"
	"time"
)

// ModelProfile is a reusable bundle of (provider + endpoint + secret +
// known model list) that a Worker can reference instead of carrying the
// same fields inline. See migration 056 for the table shape and the
// design trade-off.
//
// Builtin profiles are managed by the daemon (e.g. the auto-created
// opencode-local profile from Layer 3). The admin layer refuses to
// mutate or delete rows with Builtin=true so a misclick can't take the
// local default offline.
//
// SecretScopeID is optional for CLI providers: claude_cli, opencode_cli,
// grok_cli, and mimo_cli lean on host-installed credentials (the binary path lives
// in EndpointURL), so no AuthScope is required. The admin layer enforces
// "secret required for everyone else" — the store layer trusts whatever
// the caller writes.
type ModelProfile struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Provider      string    `json:"provider"` // anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli
	EndpointURL   string    `json:"endpoint_url"`
	SecretScopeID string    `json:"secret_scope_id,omitempty"`
	KnownModels   []string  `json:"known_models"`
	Builtin       bool      `json:"builtin"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ModelProfileStore manages ModelProfile rows. Get and GetByName return
// the generic ErrNotFound sentinel; Create returns ErrAlreadyExists on
// the unique-name constraint so the admin layer can map it to 409.
// Delete is hard-delete — worker.model_profile_id is set to NULL by the
// ON DELETE SET NULL FK declared in migration 056.
type ModelProfileStore interface {
	ListModelProfiles(ctx context.Context) ([]ModelProfile, error)
	GetModelProfile(ctx context.Context, id string) (ModelProfile, error)
	GetModelProfileByName(ctx context.Context, name string) (ModelProfile, error)
	CreateModelProfile(ctx context.Context, p *ModelProfile) error
	UpdateModelProfile(ctx context.Context, p *ModelProfile) error
	DeleteModelProfile(ctx context.Context, id string) error
}
