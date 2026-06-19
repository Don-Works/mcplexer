package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

type fakeStager struct {
	rows map[string]*store.SecretOffer
}

func newFakeStager() *fakeStager { return &fakeStager{rows: map[string]*store.SecretOffer{}} }

func (f *fakeStager) InsertSecretOffer(_ context.Context, o *store.SecretOffer) error {
	if _, ok := f.rows[o.OfferID]; ok {
		return store.ErrAlreadyExists
	}
	f.rows[o.OfferID] = o
	return nil
}

type fakeRecipientStore struct {
	recipients map[string]string
}

func newFakeRecipientStore() *fakeRecipientStore {
	return &fakeRecipientStore{recipients: map[string]string{}}
}

func (f *fakeRecipientStore) UpdateSecretTransferRecipient(_ context.Context, peerID, recipient string) error {
	f.recipients[peerID] = recipient
	return nil
}

func TestApplySecretOffer_RoundTrip(t *testing.T) {
	mgr := &Manager{}
	stager := newFakeStager()
	mgr.SetSecretOfferStager(stager)

	// Encrypt some plaintext to a fresh recipient.
	recipientIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("the-pi-ssh-key")
	ct, err := secrets.EncryptToRecipient(recipientIdentity.Recipient().String(), plaintext)
	if err != nil {
		t.Fatal(err)
	}

	wire := SecretOfferWire{
		OfferID:    "01HXYZ123456789ABCDEFGHJKM",
		Name:       "pi-ssh-key",
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
		Metadata:   map[string]string{"comment": "piclaw@picoclaw-pi"},
		ExpiresAt:  time.Now().Add(24 * time.Hour).UTC(),
	}
	if err := wire.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	envContent, err := envelopeContent(wire)
	if err != nil {
		t.Fatal(err)
	}
	env := p2p.MeshEnvelope{
		Kind:         SecretOfferKind,
		Tags:         SecretOfferTag,
		SenderPeerID: "12D3KooWPeerA",
		Content:      envContent,
	}

	if !isSecretOffer(env) {
		t.Fatal("isSecretOffer false for valid envelope")
	}

	mgr.applySecretOffer(context.Background(), env)

	got, ok := stager.rows[wire.OfferID]
	if !ok {
		t.Fatal("offer not staged")
	}
	if got.Direction != "inbound" || got.Status != "pending" {
		t.Fatalf("unexpected direction/status: %s %s", got.Direction, got.Status)
	}
	if got.PeerID != "12D3KooWPeerA" {
		t.Fatalf("peer_id = %q", got.PeerID)
	}
	if got.Name != "pi-ssh-key" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Metadata["comment"] != "piclaw@picoclaw-pi" {
		t.Fatalf("metadata lost: %v", got.Metadata)
	}

	decrypted, err := secrets.DecryptWithIdentity(recipientIdentity, got.Ciphertext)
	if err != nil {
		t.Fatalf("decrypt staged ciphertext: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("plaintext mismatch: %q vs %q", decrypted, plaintext)
	}
}

func TestApplySecretOffer_DuplicateIsSilent(t *testing.T) {
	mgr := &Manager{}
	stager := newFakeStager()
	stager.rows["DUP-OFFER-ID"] = &store.SecretOffer{OfferID: "DUP-OFFER-ID"}
	mgr.SetSecretOfferStager(stager)

	wire := SecretOfferWire{
		OfferID:    "DUP-OFFER-ID",
		Name:       "x",
		Ciphertext: "dGVzdA==",
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	content, _ := envelopeContent(wire)
	env := p2p.MeshEnvelope{
		Kind:         SecretOfferKind,
		Tags:         SecretOfferTag,
		SenderPeerID: "peer",
		Content:      content,
	}

	mgr.applySecretOffer(context.Background(), env) // must not panic
}

func TestApplyPeerIdentity_PersistsRecipient(t *testing.T) {
	mgr := &Manager{}
	recipStore := newFakeRecipientStore()
	mgr.SetPeerIdentityUpdater(recipStore)

	id, _ := age.GenerateX25519Identity()
	body := `{"secret_transfer_recipient":"` + id.Recipient().String() + `"}`
	env := p2p.MeshEnvelope{
		Kind:         PeerIdentityChangedKind,
		Tags:         PeerIdentityChangedTag,
		SenderPeerID: "12D3KooWPeerB",
		Content:      body,
	}

	if !isPeerIdentity(env) {
		t.Fatal("isPeerIdentity false for valid envelope")
	}
	mgr.applyPeerIdentity(context.Background(), env)
	if got := recipStore.recipients["12D3KooWPeerB"]; got != id.Recipient().String() {
		t.Fatalf("recipient = %q, want %q", got, id.Recipient().String())
	}
}

func TestSecretOfferWire_Validate(t *testing.T) {
	cases := []struct {
		name string
		w    SecretOfferWire
		ok   bool
	}{
		{"missing offer_id", SecretOfferWire{Name: "x", Ciphertext: "y", ExpiresAt: time.Now()}, false},
		{"missing name", SecretOfferWire{OfferID: "x", Ciphertext: "y", ExpiresAt: time.Now()}, false},
		{"missing ciphertext", SecretOfferWire{OfferID: "x", Name: "y", ExpiresAt: time.Now()}, false},
		{"missing expires_at", SecretOfferWire{OfferID: "x", Name: "y", Ciphertext: "z"}, false},
		{"ok", SecretOfferWire{OfferID: "x", Name: "y", Ciphertext: "z", ExpiresAt: time.Now().Add(time.Hour)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.w.validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected err, got nil")
			}
		})
	}
}

// envelopeContent re-serialises a SecretOfferWire to its JSON form using
// the same machinery the production sender uses.
func envelopeContent(w SecretOfferWire) (string, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
