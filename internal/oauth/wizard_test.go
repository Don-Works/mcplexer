package oauth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newTestEnv builds the dependencies the wizard needs: an in-memory SQLite
// database, an ephemeral age encryptor, and a FlowManager pointing at a
// throwaway external URL. Cleanup is registered with t.Cleanup.
func newTestEnv(t *testing.T) (*sqlite.DB, *secrets.AgeEncryptor, *oauth.FlowManager) {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("ephemeral encryptor: %v", err)
	}
	fm := oauth.NewFlowManager(db, enc, "http://127.0.0.1:13333")
	return db, enc, fm
}

func TestWizard_PKCEHappyPath(t *testing.T) {
	db, enc, fm := newTestEnv(t)
	wiz := oauth.NewWizard(db, db, fm, enc)
	spec := oauth.WizardSpec{
		AuthScopeName: "weatherco",
		ParentServer:  "weatherco-server",
		AuthURL:       "https://api.weather.co/oauth/authorize",
		TokenURL:      "https://api.weather.co/oauth/token",
		Scopes:        []string{"forecast.read", "alerts.read"},
		ClientID:      "client-pub-123",
		UsePKCE:       true,
		GrantType:     "authorization_code",
	}
	res, err := wiz.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AuthScope == nil || res.AuthScope.ID == "" {
		t.Fatal("expected auth scope to be created")
	}
	if res.AuthScope.Type != "oauth2" {
		t.Errorf("auth scope type = %q, want oauth2", res.AuthScope.Type)
	}
	if res.Provider == nil || res.Provider.ID == "" {
		t.Fatal("expected provider to be created")
	}
	if !res.Provider.UsePKCE {
		t.Error("expected provider.UsePKCE = true")
	}
	if !res.HumanApprovalRequired {
		t.Error("expected human_approval_required for authorization_code")
	}
	if !strings.Contains(res.AuthorizeURL, "code_challenge=") {
		t.Errorf("authorize URL missing PKCE challenge: %s", res.AuthorizeURL)
	}
	if !strings.Contains(res.AuthorizeURL, "client_id=client-pub-123") {
		t.Errorf("authorize URL missing client_id: %s", res.AuthorizeURL)
	}
	if !strings.Contains(res.AuthorizeURL, "scope=forecast.read+alerts.read") {
		t.Errorf("authorize URL missing scopes: %s", res.AuthorizeURL)
	}
}

func TestWizard_ClientCredentials(t *testing.T) {
	db, enc, fm := newTestEnv(t)
	wiz := oauth.NewWizard(db, db, fm, enc)
	spec := oauth.WizardSpec{
		AuthScopeName: "metricsco",
		ParentServer:  "metrics-server",
		TokenURL:      "https://api.metrics.co/oauth/token",
		ClientID:      "svc-client",
		ClientSecret:  "svc-secret",
		GrantType:     "client_credentials",
	}
	res, err := wiz.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.HumanApprovalRequired {
		t.Error("client_credentials should not require human approval")
	}
	if res.AuthorizeURL != "" {
		t.Errorf("client_credentials should not return authorize URL, got %q", res.AuthorizeURL)
	}
	if res.Provider == nil || len(res.Provider.EncryptedClientSecret) == 0 {
		t.Error("expected encrypted client secret to be stored")
	}
}

func TestWizard_RejectsImplicitGrant(t *testing.T) {
	db, enc, fm := newTestEnv(t)
	wiz := oauth.NewWizard(db, db, fm, enc)
	cases := []string{"implicit", "token", "Implicit", " IMPLICIT "}
	for _, gt := range cases {
		t.Run(gt, func(t *testing.T) {
			spec := oauth.WizardSpec{
				AuthScopeName: "x",
				AuthURL:       "https://example.com/a",
				TokenURL:      "https://example.com/t",
				ClientID:      "c",
				GrantType:     gt,
			}
			_, err := wiz.Run(context.Background(), spec)
			if !errors.Is(err, oauth.ErrImplicitGrantNotSupported) {
				t.Fatalf("expected ErrImplicitGrantNotSupported, got %v", err)
			}
		})
	}
}

func TestWizard_ValidationErrors(t *testing.T) {
	db, enc, fm := newTestEnv(t)
	wiz := oauth.NewWizard(db, db, fm, enc)
	tests := []struct {
		name    string
		spec    oauth.WizardSpec
		wantSub string
	}{
		{
			name: "missing token url",
			spec: oauth.WizardSpec{
				AuthScopeName: "x", AuthURL: "https://a", ClientID: "c",
				GrantType: "authorization_code",
			},
			wantSub: "token_url",
		},
		{
			name: "missing auth url for ac",
			spec: oauth.WizardSpec{
				AuthScopeName: "x", TokenURL: "https://t", ClientID: "c",
				GrantType: "authorization_code",
			},
			wantSub: "auth_url",
		},
		{
			name: "client_credentials missing secret",
			spec: oauth.WizardSpec{
				AuthScopeName: "x", TokenURL: "https://t", ClientID: "c",
				GrantType: "client_credentials",
			},
			wantSub: "client_secret",
		},
		{
			name: "unknown grant",
			spec: oauth.WizardSpec{
				AuthScopeName: "x", TokenURL: "https://t", ClientID: "c",
				GrantType: "device_code",
			},
			wantSub: "device_code",
		},
		{
			name: "missing grant",
			spec: oauth.WizardSpec{
				AuthScopeName: "x", TokenURL: "https://t", ClientID: "c",
			},
			wantSub: "grant_type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := wiz.Run(context.Background(), tt.spec)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tt.wantSub, err)
			}
		})
	}
}
