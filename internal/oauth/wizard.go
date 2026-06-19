package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// WizardSpec is the input the OAuth wizard uses to materialise a provider +
// auth scope for a custom MCP addon. It mirrors addon.AuthSpec but stays in
// the oauth package so we don't import addon → oauth import cycle.
type WizardSpec struct {
	// Identity / linkage.
	AuthScopeName string `json:"auth_scope_name"`
	ParentServer  string `json:"parent_server"`

	// OAuth2 config.
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	Scopes       []string `json:"scopes"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	UsePKCE      bool     `json:"use_pkce"`
	GrantType    string   `json:"grant_type"`
}

// WizardResult is the structured output of a successful wizard run. The
// AuthorizeURL is populated for authorization_code flows; client_credentials
// returns it empty (the token is fetched on first downstream call).
type WizardResult struct {
	AuthScope             *store.AuthScope     `json:"auth_scope"`
	Provider              *store.OAuthProvider `json:"provider"`
	AuthorizeURL          string               `json:"authorize_url,omitempty"`
	HumanApprovalRequired bool                 `json:"human_approval_required"`
	Message               string               `json:"message"`
}

// Wizard creates OAuth providers + auth scopes for custom MCP addons.
// It wraps the existing FlowManager + provider/scope stores so we don't
// duplicate persistence or AuthorizeURL building logic.
type Wizard struct {
	store       store.Store
	providerOps store.OAuthProviderStore
	flow        *FlowManager
	encryptor   *secrets.AgeEncryptor
}

// NewWizard builds a Wizard that persists via s + providerOps and uses fm
// to compute authorize URLs / handle callback redemption.
func NewWizard(s store.Store, providerOps store.OAuthProviderStore, fm *FlowManager, enc *secrets.AgeEncryptor) *Wizard {
	return &Wizard{store: s, providerOps: providerOps, flow: fm, encryptor: enc}
}

// ErrImplicitGrantNotSupported is returned when a caller asks for the
// deprecated OAuth2 implicit grant. The wizard refuses it outright.
var ErrImplicitGrantNotSupported = errors.New("implicit grant is not supported (use authorization_code with PKCE)")

// Run validates the spec, creates an OAuthProvider, creates an AuthScope
// linked to it, and (for authorization_code) returns the authorize URL.
func (w *Wizard) Run(ctx context.Context, spec WizardSpec) (*WizardResult, error) {
	if err := w.validate(spec); err != nil {
		return nil, err
	}

	provider, err := w.materialiseProvider(ctx, spec)
	if err != nil {
		return nil, err
	}

	scope, err := w.materialiseScope(ctx, spec, provider.ID)
	if err != nil {
		return nil, err
	}

	res := &WizardResult{AuthScope: scope, Provider: provider}
	if spec.GrantType == "client_credentials" {
		res.Message = fmt.Sprintf("Auth scope %q ready for client_credentials.", spec.AuthScopeName)
		return res, nil
	}

	authURL, err := w.flow.AuthorizeURL(ctx, scope.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("build authorize url: %w", err)
	}
	res.AuthorizeURL = authURL
	res.HumanApprovalRequired = true
	res.Message = fmt.Sprintf(
		"Human approval required: open %s to grant access for %q.",
		authURL, spec.AuthScopeName,
	)
	return res, nil
}

// validate enforces the supported grant types and required fields. Implicit
// grant is rejected with a stable sentinel so callers can map to a 400.
func (w *Wizard) validate(spec WizardSpec) error {
	if strings.TrimSpace(spec.AuthScopeName) == "" {
		return errors.New("auth_scope_name is required")
	}
	if strings.TrimSpace(spec.TokenURL) == "" {
		return errors.New("token_url is required")
	}
	switch strings.ToLower(strings.TrimSpace(spec.GrantType)) {
	case "implicit", "token":
		return ErrImplicitGrantNotSupported
	case "authorization_code":
		if spec.AuthURL == "" {
			return errors.New("auth_url is required for authorization_code")
		}
		if spec.ClientID == "" && !spec.UsePKCE {
			return errors.New("client_id is required (or enable use_pkce for public clients)")
		}
	case "client_credentials":
		if spec.ClientID == "" || spec.ClientSecret == "" {
			return errors.New("client_id and client_secret are required for client_credentials")
		}
	case "":
		return errors.New("grant_type is required (authorization_code or client_credentials)")
	default:
		return fmt.Errorf("grant_type %q is not supported", spec.GrantType)
	}
	return nil
}

// materialiseProvider creates the OAuthProvider row, encrypting the client
// secret when one is supplied. Source is "wizard" so we can distinguish
// from "api"/"auto-discovery"/template flows in audits.
func (w *Wizard) materialiseProvider(ctx context.Context, spec WizardSpec) (*store.OAuthProvider, error) {
	now := time.Now().UTC()
	provider := &store.OAuthProvider{
		Name:         spec.AuthScopeName + " (addon)",
		AuthorizeURL: spec.AuthURL,
		TokenURL:     spec.TokenURL,
		ClientID:     spec.ClientID,
		UsePKCE:      spec.UsePKCE,
		Source:       "wizard",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if len(spec.Scopes) > 0 {
		raw, err := json.Marshal(spec.Scopes)
		if err != nil {
			return nil, fmt.Errorf("marshal scopes: %w", err)
		}
		provider.Scopes = raw
	}
	if spec.ClientSecret != "" {
		if w.encryptor == nil {
			return nil, errors.New("encryption not configured (no age key) — cannot store client_secret")
		}
		enc, err := w.encryptor.Encrypt([]byte(strings.TrimSpace(spec.ClientSecret)))
		if err != nil {
			return nil, fmt.Errorf("encrypt client_secret: %w", err)
		}
		provider.EncryptedClientSecret = enc
	}
	if err := w.providerOps.CreateOAuthProvider(ctx, provider); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			existing, lookupErr := w.providerOps.GetOAuthProviderByName(ctx, provider.Name)
			if lookupErr != nil {
				return nil, fmt.Errorf("provider %q already exists and lookup failed: %w", provider.Name, lookupErr)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("create oauth provider: %w", err)
	}
	return provider, nil
}

// materialiseScope creates the AuthScope row linking to the provider. Type
// is "oauth2" so the credential injector picks it up via the existing
// HeadersForDownstream switch.
func (w *Wizard) materialiseScope(ctx context.Context, spec WizardSpec, providerID string) (*store.AuthScope, error) {
	if existing, err := w.store.GetAuthScopeByName(ctx, spec.AuthScopeName); err == nil && existing != nil {
		// Idempotent: re-link provider if needed and return.
		if existing.OAuthProviderID == "" {
			existing.OAuthProviderID = providerID
			existing.UpdatedAt = time.Now().UTC()
			if err := w.store.UpdateAuthScope(ctx, existing); err != nil {
				return nil, fmt.Errorf("update existing auth scope: %w", err)
			}
		}
		return existing, nil
	}
	now := time.Now().UTC()
	scope := &store.AuthScope{
		Name:            spec.AuthScopeName,
		Type:            "oauth2",
		OAuthProviderID: providerID,
		Source:          "wizard",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := w.store.CreateAuthScope(ctx, scope); err != nil {
		return nil, fmt.Errorf("create auth scope: %w", err)
	}
	return scope, nil
}
