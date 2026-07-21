//go:build p2p

package p2p

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// writeEnvelope serialises env as length-prefixed JSON: a 4-byte big-endian
// payload length followed by the JSON bytes. Streams the encoder so we
// never hold both raw + JSON forms in memory.
func writeEnvelope(w io.Writer, env *MeshEnvelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if len(body) > MaxEnvelopeBytes {
		return errEnvelopeTooLarge
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(body)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return nil
}

// readEnvelope reads one length-prefixed JSON envelope from r. Caller is
// responsible for setting a read deadline. Bounded by MaxEnvelopeBytes so a
// malicious 4 GiB length prefix can't allocate.
func readEnvelope(r io.Reader) (MeshEnvelope, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return MeshEnvelope{}, fmt.Errorf("read length prefix: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 {
		return MeshEnvelope{}, errors.New("zero-length envelope")
	}
	if int64(n) > int64(MaxEnvelopeBytes) {
		return MeshEnvelope{}, errEnvelopeTooLarge
	}
	limited := io.LimitReader(r, int64(n))
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	var env MeshEnvelope
	if err := dec.Decode(&env); err != nil {
		return MeshEnvelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

// verifyAndAuthorize checks that the envelope signature was produced by the
// stream's remote peer key, that the remote is paired, and that timestamps
// are sane. Returns an empty string on success, or an audit reason on
// rejection.
func (t *MeshTransport) verifyAndAuthorize(remote peer.ID, env *MeshEnvelope) string {
	if env.SenderPeerID == "" || env.SenderPeerID != remote.String() {
		return "sender_mismatch"
	}
	if len(env.Signature) == 0 {
		return "missing_signature"
	}
	pub, err := remote.ExtractPublicKey()
	if err != nil || pub == nil {
		// Non-Ed25519 (RSA/etc) peers store pub key in peerstore instead.
		pub = t.host.Inner().Peerstore().PubKey(remote)
	}
	if pub == nil {
		return "no_public_key"
	}
	ok, err := pub.Verify(canonicalSigningBytes(env), env.Signature)
	if err != nil || !ok {
		return "invalid_signature"
	}
	if t.lookup == nil {
		return "no_peer_lookup"
	}
	paired, err := t.lookup.IsPaired(context.Background(), env.SenderPeerID)
	if err != nil {
		return "paired_lookup_failed"
	}
	if !paired {
		return "unpaired_peer"
	}
	if drift := envelopeAge(env); drift > 10*time.Minute || drift < -2*time.Minute {
		return "stale_or_future"
	}
	return ""
}

// dedupeWindow tracks the last N (sender, envelope id) pairs to reject
// replays. Both an LRU-ish map + insertion-order ring keep the data
// structure compact and cheap to evict.
type dedupeWindow struct {
	mu    sync.Mutex
	cap   int
	seen_ map[string]struct{}
	order []string
}

func newDedupeWindow(cap int) *dedupeWindow {
	if cap <= 0 {
		cap = 1
	}
	return &dedupeWindow{
		cap:   cap,
		seen_: make(map[string]struct{}, cap),
		order: make([]string, 0, cap),
	}
}

// seen returns true if (sender, envelopeID) was already observed in the
// window. Insert side-effect: the pair is recorded if it was new.
func (d *dedupeWindow) seen(sender, envelopeID string) bool {
	if d == nil {
		return false
	}
	key := sender + "|" + envelopeID
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen_[key]; ok {
		return true
	}
	if len(d.order) >= d.cap {
		evict := d.order[0]
		d.order = d.order[1:]
		delete(d.seen_, evict)
	}
	d.seen_[key] = struct{}{}
	d.order = append(d.order, key)
	return false
}
