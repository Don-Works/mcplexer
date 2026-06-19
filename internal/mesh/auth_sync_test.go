package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

func TestAuthSync_RoundTripReencryptsForReceiver(t *testing.T) {
	ctx := context.Background()
	sourceEnc, _ := secrets.NewEphemeralEncryptor()
	targetEnc, _ := secrets.NewEphemeralEncryptor()
	targetTransferKey, _ := age.GenerateX25519Identity()

	sourceStore := newFakeAuthSyncStore()
	targetStore := newFakeAuthSyncStore()
	targetStore.peers["peer-source"] = &store.P2PPeer{
		PeerID: "peer-source",
		Scopes: []string{AuthSyncScopeName},
	}
	targetStore.bindings[bindingKey("peer-source", "ws-remote")] = &store.WorkspacePeerBinding{
		PeerID:            "peer-source",
		RemoteWorkspaceID: "ws-remote",
		LocalWorkspaceID:  "ws-local",
	}

	sourceStore.peers["peer-target"] = &store.P2PPeer{
		PeerID:                  "peer-target",
		SecretTransferRecipient: targetTransferKey.Recipient().String(),
		Scopes:                  []string{AuthSyncScopeName},
	}
	sourceStore.providers["prov-slack"] = providerForTest(t, sourceEnc)
	sourceStore.scopes["scope-slack"] = scopeForTest(t, sourceEnc)
	sourceStore.servers["srv-slack"] = serverForTest()
	sourceStore.routes["route-slack"] = routeForTest()

	ft := newFakeTransport()
	source := NewManager(nil)
	source.SetP2PTransport(ft, "peer-source")
	source.SetAuthSync(sourceStore, sourceEnc, nil)
	if err := source.SendAuthScopeSnapshotToPeer(ctx, "peer-target", "scope-slack"); err != nil {
		t.Fatalf("SendAuthScopeSnapshotToPeer: %v", err)
	}
	sends := ft.targetedSends()
	if len(sends) != 1 {
		t.Fatalf("targeted sends = %d, want 1", len(sends))
	}

	target := NewManager(nil)
	target.SetAuthSync(targetStore, targetEnc, targetTransferKey)
	refreshes := 0
	target.SetAuthSyncRefreshHook(func() { refreshes++ })
	target.applyAuthSync(ctx, *sends[0].env)

	gotScope, err := targetStore.GetAuthScope(ctx, "scope-slack")
	if err != nil {
		t.Fatalf("target scope missing: %v", err)
	}
	gotSecrets, err := decryptSecretMap(targetEnc, gotScope.EncryptedData)
	if err != nil {
		t.Fatalf("decrypt target secrets: %v", err)
	}
	if gotSecrets["bot_token"] != "bot-token-target-test" {
		t.Fatalf("bot token = %q", gotSecrets["bot_token"])
	}
	gotToken, err := decryptOAuthTokenData(targetEnc, gotScope.OAuthTokenData)
	if err != nil {
		t.Fatalf("decrypt target token: %v", err)
	}
	if gotToken.AccessToken != "oauth-access" || gotToken.RefreshToken != "oauth-refresh" {
		t.Fatalf("oauth token mismatch: %#v", gotToken)
	}
	gotProvider, err := targetStore.GetOAuthProvider(ctx, gotScope.OAuthProviderID)
	if err != nil {
		t.Fatalf("target provider missing: %v", err)
	}
	clientSecret, err := decryptString(targetEnc, gotProvider.EncryptedClientSecret)
	if err != nil {
		t.Fatalf("decrypt target client secret: %v", err)
	}
	if clientSecret != "slack-client-secret" {
		t.Fatalf("client secret = %q", clientSecret)
	}
	gotServer, err := targetStore.GetDownstreamServer(ctx, "srv-slack")
	if err != nil {
		t.Fatalf("target server missing: %v", err)
	}
	if gotServer.ToolNamespace != "slack" || gotServer.Command != "slack-mcp" {
		t.Fatalf("server mismatch: %#v", gotServer)
	}
	gotRoute, err := targetStore.GetRouteRule(ctx, "route-slack")
	if err != nil {
		t.Fatalf("target route missing: %v", err)
	}
	if gotRoute.WorkspaceID != "ws-local" {
		t.Fatalf("route workspace = %q, want ws-local", gotRoute.WorkspaceID)
	}
	if gotRoute.DownstreamServerID != gotServer.ID {
		t.Fatalf("route server = %q, want %q", gotRoute.DownstreamServerID, gotServer.ID)
	}
	if gotRoute.AuthScopeID != gotScope.ID {
		t.Fatalf("route auth scope = %q, want %q", gotRoute.AuthScopeID, gotScope.ID)
	}
	if refreshes != 1 {
		t.Fatalf("refresh hook calls = %d, want 1", refreshes)
	}
}

func TestAuthSync_DropsInboundWithoutGrant(t *testing.T) {
	ctx := context.Background()
	targetEnc, _ := secrets.NewEphemeralEncryptor()
	targetTransferKey, _ := age.GenerateX25519Identity()
	plain := authSnapshotPlain{
		Schema:   authSyncSchema,
		Exported: time.Now().UTC(),
		Scope:    authScopeSnapshot{ID: "scope", Name: "slack", Type: "oauth2"},
	}
	data, _ := json.Marshal(plain)
	ct, _ := secrets.EncryptToRecipient(targetTransferKey.Recipient().String(), data)
	wire := AuthSnapshotWire{
		SnapshotID: "snap",
		ScopeName:  "slack",
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
		ExportedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(wire)
	targetStore := newFakeAuthSyncStore()
	targetStore.peers["peer-source"] = &store.P2PPeer{PeerID: "peer-source"}
	target := NewManager(nil)
	target.SetAuthSync(targetStore, targetEnc, targetTransferKey)
	target.applyAuthSync(ctx, p2p.MeshEnvelope{
		Kind:         AuthSyncKind,
		Tags:         AuthSyncTag,
		SenderPeerID: "peer-source",
		Content:      string(body),
	})

	if _, err := targetStore.GetAuthScope(ctx, "scope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAuthScope err = %v, want ErrNotFound", err)
	}
}

func providerForTest(t *testing.T, enc *secrets.AgeEncryptor) *store.OAuthProvider {
	t.Helper()
	clientSecret, err := encryptString(enc, "slack-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	return &store.OAuthProvider{
		ID:                    "prov-slack",
		Name:                  "slack",
		AuthorizeURL:          "https://slack.com/oauth/v2/authorize",
		TokenURL:              "https://slack.com/api/oauth.v2.access",
		ClientID:              "slack-client-id",
		EncryptedClientSecret: clientSecret,
		Scopes:                json.RawMessage(`["chat:write"]`),
		UsePKCE:               true,
		Source:                "api",
	}
}

func scopeForTest(t *testing.T, enc *secrets.AgeEncryptor) *store.AuthScope {
	t.Helper()
	secretBlob, err := encryptSecretMap(enc, map[string]string{"bot_token": "bot-token-target-test"})
	if err != nil {
		t.Fatal(err)
	}
	tokenBlob, err := encryptOAuthTokenData(enc, &store.OAuthTokenData{
		AccessToken:  "oauth-access",
		RefreshToken: "oauth-refresh",
		TokenType:    "Bearer",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &store.AuthScope{
		ID:              "scope-slack",
		Name:            "slack",
		DisplayName:     "Slack",
		Type:            "oauth2",
		EncryptedData:   secretBlob,
		RedactionHints:  json.RawMessage(`["bot-token-"]`),
		OAuthProviderID: "prov-slack",
		OAuthTokenData:  tokenBlob,
		Source:          "api",
	}
}

func serverForTest() *store.DownstreamServer {
	return &store.DownstreamServer{
		ID:             "srv-slack",
		Name:           "Slack MCP",
		Transport:      "stdio",
		Command:        "slack-mcp",
		Args:           json.RawMessage(`["--stdio"]`),
		ToolNamespace:  "slack",
		Discovery:      "dynamic",
		CacheConfig:    json.RawMessage(`{"ttl_seconds":60}`),
		IdleTimeoutSec: 30,
		MaxInstances:   1,
		RestartPolicy:  "on-failure",
		Source:         "api",
	}
}

func routeForTest() *store.RouteRule {
	return &store.RouteRule{
		ID:                 "route-slack",
		Name:               "Slack route",
		Priority:           50,
		WorkspaceID:        "ws-remote",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["slack__*"]`),
		ScopePolicy:        json.RawMessage(`{}`),
		DownstreamServerID: "srv-slack",
		AuthScopeID:        "scope-slack",
		Policy:             "allow",
		ApprovalMode:       "none",
		Source:             "api",
	}
}

func cloneScope(a *store.AuthScope) *store.AuthScope {
	cp := *a
	cp.EncryptedData = cloneBytes(a.EncryptedData)
	cp.RedactionHints = cloneRaw(a.RedactionHints)
	cp.OAuthTokenData = cloneBytes(a.OAuthTokenData)
	return &cp
}

func cloneProvider(p *store.OAuthProvider) *store.OAuthProvider {
	cp := *p
	cp.EncryptedClientSecret = cloneBytes(p.EncryptedClientSecret)
	cp.Scopes = cloneRaw(p.Scopes)
	return &cp
}
