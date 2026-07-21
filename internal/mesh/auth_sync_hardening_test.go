package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// grantedTarget builds a receiver Manager that has granted mesh.auth_sync to
// peer-source, ready to apply inbound snapshots.
func grantedTarget(t *testing.T) (*Manager, *fakeAuthSyncStore, *secrets.AgeEncryptor, *age.X25519Identity) {
	t.Helper()
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	st := newFakeAuthSyncStore()
	st.peers["peer-source"] = &store.P2PPeer{PeerID: "peer-source", Scopes: []string{AuthSyncScopeName}}
	mgr := NewManager(nil)
	mgr.SetAuthSync(st, enc, key)
	return mgr, st, enc, key
}

// makeAuthEnvelope encrypts plain to recipient and frames it as a mesh
// auth_sync envelope from peer-source with the given snapshot id + exported_at.
func makeAuthEnvelope(
	t *testing.T,
	recipient string,
	plain authSnapshotPlain,
	snapshotID string,
	exportedAt time.Time,
) p2p.MeshEnvelope {
	t.Helper()
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := secrets.EncryptToRecipient(recipient, data)
	if err != nil {
		t.Fatal(err)
	}
	wire := AuthSnapshotWire{
		SnapshotID: snapshotID,
		ScopeName:  plain.Scope.Name,
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
		ExportedAt: exportedAt,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return p2p.MeshEnvelope{
		Kind:         AuthSyncKind,
		Tags:         AuthSyncTag,
		SenderPeerID: "peer-source",
		Content:      string(body),
	}
}

func plainSnap(scopeID, scopeName, botToken string) authSnapshotPlain {
	return authSnapshotPlain{
		Schema:   authSyncSchema,
		Exported: time.Unix(0, 0).UTC(),
		Scope:    authScopeSnapshot{ID: scopeID, Name: scopeName, Type: "oauth2"},
		Secrets:  map[string]string{"bot_token": botToken},
	}
}

// Gate 5: sender refuses to send when the destination peer has not been
// granted mesh.auth_sync (HasPeerScope false), even with a transfer recipient.
func TestAuthSync_SenderRefusesWithoutGrant(t *testing.T) {
	ctx := context.Background()
	sourceEnc, _ := secrets.NewEphemeralEncryptor()
	targetKey, _ := age.GenerateX25519Identity()
	sourceStore := newFakeAuthSyncStore()
	sourceStore.peers["peer-target"] = &store.P2PPeer{
		PeerID:                  "peer-target",
		SecretTransferRecipient: targetKey.Recipient().String(),
		// no Scopes — grant absent
	}
	sourceStore.scopes["scope-slack"] = scopeForTest(t, sourceEnc)
	ft := newFakeTransport()
	source := NewManager(nil)
	source.SetP2PTransport(ft, "peer-source")
	source.SetAuthSync(sourceStore, sourceEnc, nil)

	if err := source.SendAuthScopeSnapshotToPeer(ctx, "peer-target", "scope-slack"); err != nil {
		t.Fatalf("SendAuthScopeSnapshotToPeer: %v", err)
	}
	if got := len(ft.targetedSends()); got != 0 {
		t.Fatalf("targeted sends = %d, want 0 (no grant)", got)
	}
}

// Gate 5: sender skips a revoked peer entirely.
func TestAuthSync_SenderSkipsRevokedPeer(t *testing.T) {
	ctx := context.Background()
	sourceEnc, _ := secrets.NewEphemeralEncryptor()
	targetKey, _ := age.GenerateX25519Identity()
	revoked := time.Unix(100, 0).UTC()
	sourceStore := newFakeAuthSyncStore()
	sourceStore.peers["peer-target"] = &store.P2PPeer{
		PeerID:                  "peer-target",
		SecretTransferRecipient: targetKey.Recipient().String(),
		Scopes:                  []string{AuthSyncScopeName},
		RevokedAt:               &revoked,
	}
	sourceStore.scopes["scope-slack"] = scopeForTest(t, sourceEnc)
	ft := newFakeTransport()
	source := NewManager(nil)
	source.SetP2PTransport(ft, "peer-source")
	source.SetAuthSync(sourceStore, sourceEnc, nil)

	if err := source.SendAuthScopeSnapshotToPeer(ctx, "peer-target", "scope-slack"); err != nil {
		t.Fatalf("SendAuthScopeSnapshotToPeer: %v", err)
	}
	if got := len(ft.targetedSends()); got != 0 {
		t.Fatalf("targeted sends = %d, want 0 (revoked peer)", got)
	}
}

// Gate 5: an oversize ciphertext is rejected before decrypt; nothing imported.
func TestAuthSync_DropsOversizeCiphertext(t *testing.T) {
	ctx := context.Background()
	target, st, _, _ := grantedTarget(t)
	wire := AuthSnapshotWire{
		SnapshotID: "snap",
		ScopeName:  "slack",
		Ciphertext: strings.Repeat("A", maxAuthSyncCiphertextChars+1),
		ExportedAt: time.Unix(10, 0).UTC(),
	}
	body, _ := json.Marshal(wire)
	target.applyAuthSync(ctx, p2p.MeshEnvelope{
		Kind: AuthSyncKind, Tags: AuthSyncTag, SenderPeerID: "peer-source", Content: string(body),
	})
	if _, err := st.GetAuthScope(ctx, "scope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAuthScope err = %v, want ErrNotFound", err)
	}
}

// Gate 5: a snapshot with an unknown schema version is dropped.
func TestAuthSync_DropsSchemaMismatch(t *testing.T) {
	ctx := context.Background()
	target, st, _, key := grantedTarget(t)
	plain := plainSnap("scope", "slack", "remote")
	plain.Schema = authSyncSchema + 1
	env := makeAuthEnvelope(t, key.Recipient().String(), plain, "snap", time.Unix(10, 0).UTC())
	target.applyAuthSync(ctx, env)
	if _, err := st.GetAuthScope(ctx, "scope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAuthScope err = %v, want ErrNotFound (schema mismatch)", err)
	}
}

// Gate 5: inbound from a revoked peer is dropped (HasPeerScope returns false).
func TestAuthSync_DropsRevokedPeerInbound(t *testing.T) {
	ctx := context.Background()
	target, st, _, key := grantedTarget(t)
	revoked := time.Unix(100, 0).UTC()
	st.peers["peer-source"].RevokedAt = &revoked
	plain := plainSnap("scope", "slack", "remote")
	env := makeAuthEnvelope(t, key.Recipient().String(), plain, "snap", time.Unix(10, 0).UTC())
	target.applyAuthSync(ctx, env)
	if _, err := st.GetAuthScope(ctx, "scope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAuthScope err = %v, want ErrNotFound (revoked peer)", err)
	}
}

// Gate 5: a wire whose plaintext scope name disagrees with the envelope is
// rejected as tampered/misrouted.
func TestAuthSync_DropsScopeNameMismatch(t *testing.T) {
	ctx := context.Background()
	target, st, _, key := grantedTarget(t)
	plain := plainSnap("scope", "slack", "remote")
	env := makeAuthEnvelope(t, key.Recipient().String(), plain, "snap", time.Unix(10, 0).UTC())
	// Tamper the outer wire scope name.
	var wire AuthSnapshotWire
	if err := json.Unmarshal([]byte(env.Content), &wire); err != nil {
		t.Fatal(err)
	}
	wire.ScopeName = "not-slack"
	body, _ := json.Marshal(wire)
	env.Content = string(body)
	target.applyAuthSync(ctx, env)
	if _, err := st.GetAuthScope(ctx, "scope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAuthScope err = %v, want ErrNotFound (scope mismatch)", err)
	}
}

// Gate 2: replaying the same snapshot id is a no-op — the second apply is
// dropped before it can re-create a row the user had removed.
func TestAuthSync_RejectsReplay(t *testing.T) {
	ctx := context.Background()
	target, st, _, key := grantedTarget(t)
	env := makeAuthEnvelope(t, key.Recipient().String(),
		plainSnap("scope-replay", "slack", "remote"), "snap-1", time.Unix(10, 0).UTC())

	target.applyAuthSync(ctx, env)
	if _, err := st.GetAuthScope(ctx, "scope-replay"); err != nil {
		t.Fatalf("first apply should create scope: %v", err)
	}
	// User deletes the imported scope; a replay of the same snapshot id must
	// not silently resurrect it.
	delete(st.scopes, "scope-replay")
	target.applyAuthSync(ctx, env)
	if _, err := st.GetAuthScope(ctx, "scope-replay"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replay re-applied snapshot; err = %v, want ErrNotFound", err)
	}
}

// Gate 2: a snapshot with an older exported_at than one already applied is
// rejected, so a captured-and-replayed older state cannot roll credentials back.
func TestAuthSync_RejectsStaleExportedAt(t *testing.T) {
	ctx := context.Background()
	target, st, targetEnc, key := grantedTarget(t)
	recipient := key.Recipient().String()
	t1 := time.Unix(100, 0).UTC()
	t2 := time.Unix(200, 0).UTC()

	newer := makeAuthEnvelope(t, recipient, plainSnap("scope-stale", "slack", "new-token"), "snap-new", t2)
	target.applyAuthSync(ctx, newer)
	older := makeAuthEnvelope(t, recipient, plainSnap("scope-stale", "slack", "old-token"), "snap-old", t1)
	target.applyAuthSync(ctx, older)

	got, err := st.GetAuthScope(ctx, "scope-stale")
	if err != nil {
		t.Fatalf("scope missing: %v", err)
	}
	secretsMap, err := decryptSecretMap(targetEnc, got.EncryptedData)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if secretsMap["bot_token"] != "new-token" {
		t.Fatalf("bot_token = %q, want new-token (stale apply must be rejected)", secretsMap["bot_token"])
	}
}

// Gate 3: an inbound snapshot must not clobber a locally-authored scope of the
// same name; the local secret is preserved and provenance is respected.
func TestAuthSync_PreservesLocallyAuthoredScope(t *testing.T) {
	ctx := context.Background()
	target, st, targetEnc, key := grantedTarget(t)
	localSecret, err := encryptSecretMap(targetEnc, map[string]string{"bot_token": "local-secret"})
	if err != nil {
		t.Fatal(err)
	}
	st.scopes["scope-local"] = &store.AuthScope{
		ID:            "scope-local",
		Name:          "slack",
		Type:          "oauth2",
		EncryptedData: localSecret,
		Source:        "api", // locally authored
	}
	env := makeAuthEnvelope(t, key.Recipient().String(),
		plainSnap("scope-remote", "slack", "remote-secret"), "snap", time.Unix(10, 0).UTC())
	target.applyAuthSync(ctx, env)

	got, err := st.GetAuthScope(ctx, "scope-local")
	if err != nil {
		t.Fatalf("local scope vanished: %v", err)
	}
	if got.Source != "api" {
		t.Fatalf("local scope source = %q, want api (import overwrote provenance)", got.Source)
	}
	secretsMap, err := decryptSecretMap(targetEnc, got.EncryptedData)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if secretsMap["bot_token"] != "local-secret" {
		t.Fatalf("bot_token = %q, want local-secret (import clobbered local row)", secretsMap["bot_token"])
	}
}

// Gate 3: a same-peer refresh DOES update its own previously-imported row, and
// imported rows carry the mesh-import provenance marker.
func TestAuthSync_SamePeerImportRefreshes(t *testing.T) {
	ctx := context.Background()
	target, st, targetEnc, key := grantedTarget(t)
	recipient := key.Recipient().String()

	first := makeAuthEnvelope(t, recipient, plainSnap("scope-refresh", "slack", "v1"), "snap-1", time.Unix(100, 0).UTC())
	target.applyAuthSync(ctx, first)
	got, err := st.GetAuthScope(ctx, "scope-refresh")
	if err != nil {
		t.Fatalf("first import missing: %v", err)
	}
	if want := meshImportSource("peer-source"); got.Source != want {
		t.Fatalf("imported source = %q, want %q", got.Source, want)
	}

	second := makeAuthEnvelope(t, recipient, plainSnap("scope-refresh", "slack", "v2"), "snap-2", time.Unix(200, 0).UTC())
	target.applyAuthSync(ctx, second)
	got, err = st.GetAuthScope(ctx, "scope-refresh")
	if err != nil {
		t.Fatalf("refresh missing: %v", err)
	}
	secretsMap, err := decryptSecretMap(targetEnc, got.EncryptedData)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if secretsMap["bot_token"] != "v2" {
		t.Fatalf("bot_token = %q, want v2 (same-peer refresh should update)", secretsMap["bot_token"])
	}
}
