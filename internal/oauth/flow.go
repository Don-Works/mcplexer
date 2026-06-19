package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// FlowManager orchestrates OAuth2 authorization code flows.
type FlowManager struct {
	store             store.Store
	encryptor         *secrets.AgeEncryptor
	stateStore        *StateStore
	externalURL       string
	preferExternalURL bool
	tokenHook         func(context.Context, string)

	trustedProxies []*net.IPNet
}

// NewFlowManager creates a FlowManager.
func NewFlowManager(s store.Store, enc *secrets.AgeEncryptor, externalURL string) *FlowManager {
	return &FlowManager{
		store:       s,
		encryptor:   enc,
		stateStore:  NewStateStore(),
		externalURL: strings.TrimRight(externalURL, "/"),
	}
}

// SetPreferExternalURL makes the startup-configured external URL authoritative
// for request-derived callback URLs. Leave this disabled when externalURL is
// only a local fallback inferred from the bind address.
func (fm *FlowManager) SetPreferExternalURL(enabled bool) {
	fm.preferExternalURL = enabled
}

// SetTokenChangeHook registers an optional callback fired after OAuth token
// data is written or cleared for an auth scope.
func (fm *FlowManager) SetTokenChangeHook(fn func(context.Context, string)) {
	fm.tokenHook = fn
}

// SetTrustedProxies configures the CIDR ranges that are allowed to set
// X-Forwarded-Proto / X-Forwarded-Host headers. Without this list, forwarded
// headers are ignored and only the direct connection properties (r.TLS, r.Host)
// are used.
func (fm *FlowManager) SetTrustedProxies(cidrs []string) error {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if !strings.Contains(cidr, "/") {
			cidr += "/32"
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}
	fm.trustedProxies = nets
	return nil
}

func (fm *FlowManager) emitTokenChange(ctx context.Context, authScopeID string) {
	if fm != nil && fm.tokenHook != nil {
		fm.tokenHook(ctx, authScopeID)
	}
}

// AuthorizeURL builds the OAuth2 authorization URL for an auth scope.
// The request `r` is used to derive a request-relative callback URL when the
// provider has no stored RedirectURI; existing providers with a stored URI
// keep using it so the redirect matches what was registered with the auth
// server. `r` may be nil for non-HTTP callers, in which case the startup
// externalURL is used as the fallback.
func (fm *FlowManager) AuthorizeURL(ctx context.Context, authScopeID string, r *http.Request) (string, error) {
	scope, err := fm.store.GetAuthScope(ctx, authScopeID)
	if err != nil {
		return "", fmt.Errorf("get auth scope: %w", err)
	}
	if scope.OAuthProviderID == "" {
		return "", fmt.Errorf("auth scope %q has no oauth provider", authScopeID)
	}

	provider, err := fm.store.GetOAuthProvider(ctx, scope.OAuthProviderID)
	if err != nil {
		return "", fmt.Errorf("get oauth provider: %w", err)
	}

	var codeVerifier string
	if provider.UsePKCE {
		codeVerifier, err = GenerateCodeVerifier()
		if err != nil {
			return "", fmt.Errorf("generate pkce verifier: %w", err)
		}
	}

	redirectURI := fm.RedirectURIFor(provider, r)

	state, err := fm.stateStore.Create(authScopeID, codeVerifier, redirectURI)
	if err != nil {
		return "", fmt.Errorf("create oauth state: %w", err)
	}
	return fm.buildAuthorizeURL(provider, state, codeVerifier, redirectURI)
}

func (fm *FlowManager) buildAuthorizeURL(
	p *store.OAuthProvider, state, codeVerifier, redirectURI string,
) (string, error) {
	u, err := parseOAuthURL(p.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("invalid authorize url: %w", err)
	}

	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)

	var scopes []string
	if len(p.Scopes) > 0 {
		if err := json.Unmarshal(p.Scopes, &scopes); err != nil {
			return "", fmt.Errorf("parse provider scopes: %w", err)
		}
	}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}

	if codeVerifier != "" {
		q.Set("code_challenge", CodeChallenge(codeVerifier))
		q.Set("code_challenge_method", "S256")
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseOAuthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url must use http or https")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url must include host")
	}
	return u, nil
}

// CallbackURL returns the startup-configured OAuth callback URL.
// Prefer RequestCallbackURL(r) when an *http.Request is available — it adapts
// to whatever Host the user is actually browsing on, which is what OAuth
// authorization servers compare against.
func (fm *FlowManager) CallbackURL() string {
	return fm.externalURL + "/api/v1/oauth/callback"
}

// RequestCallbackURL derives the OAuth callback URL from the incoming request.
// X-Forwarded-Proto / X-Forwarded-Host are honoured only when the request
// originates from a trusted proxy (configured via SetTrustedProxies). Otherwise
// only the direct connection properties (r.TLS, r.Host) are used. Falls back
// to the startup-configured externalURL when r is nil.
func (fm *FlowManager) RequestCallbackURL(r *http.Request) string {
	if fm != nil && fm.preferExternalURL && fm.externalURL != "" {
		return fm.CallbackURL()
	}
	if r == nil {
		return fm.CallbackURL()
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fm.isTrustedProxy(r) {
		if v := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); v == "http" || v == "https" {
			scheme = v
		}
		if v := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); v != "" && !strings.ContainsAny(v, "\r\n/") {
			host = v
		}
	}
	if host == "" {
		return fm.CallbackURL()
	}
	return scheme + "://" + host + "/api/v1/oauth/callback"
}

func firstForwardedValue(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// isTrustedProxy reports whether the request's RemoteAddr falls within any of
// the configured trusted proxy CIDRs. When no trusted proxies are configured,
// forwarded headers are never trusted.
func (fm *FlowManager) isTrustedProxy(r *http.Request) bool {
	if fm == nil || r == nil || len(fm.trustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range fm.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// RedirectURIFor returns the redirect URI to use for a given OAuth provider.
// Stored RedirectURI wins (must match what was registered with the auth
// server). Empty means legacy/unknown — fall back to the current request's
// callback URL, then to startup externalURL.
func (fm *FlowManager) RedirectURIFor(p *store.OAuthProvider, r *http.Request) string {
	if p != nil && p.RedirectURI != "" {
		return p.RedirectURI
	}
	return fm.RequestCallbackURL(r)
}

// HandleCallback processes the OAuth2 callback, exchanging the code for tokens.
// The redirect_uri used during the authorization phase is bound into the state
// entry and reused verbatim for the token exchange, preventing mismatch attacks.
func (fm *FlowManager) HandleCallback(
	ctx context.Context, state, code string, _ *http.Request,
) (authScopeID string, err error) {
	entry, ok := fm.stateStore.Validate(state)
	if !ok {
		return "", fmt.Errorf("invalid or expired oauth state")
	}

	scope, err := fm.store.GetAuthScope(ctx, entry.AuthScopeID)
	if err != nil {
		return "", fmt.Errorf("get auth scope: %w", err)
	}

	provider, err := fm.store.GetOAuthProvider(ctx, scope.OAuthProviderID)
	if err != nil {
		return "", fmt.Errorf("get oauth provider: %w", err)
	}

	clientSecret, err := fm.decryptClientSecret(provider)
	if err != nil {
		return "", err
	}

	td, err := fm.exchangeCode(ctx, provider, clientSecret, code, entry.CodeVerifier, entry.RedirectURI)
	if err != nil {
		return "", err
	}

	encrypted, err := fm.encryptTokenData(td)
	if err != nil {
		return "", err
	}

	if err := fm.store.UpdateAuthScopeTokenData(ctx, entry.AuthScopeID, encrypted); err != nil {
		return "", fmt.Errorf("store token data: %w", err)
	}
	fm.emitTokenChange(ctx, entry.AuthScopeID)
	return entry.AuthScopeID, nil
}
