//go:build p2p

package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/don-works/mcplexer/internal/store"
)

// PeerPersister is the subset of store.P2PPeerStore that PairingService
// needs in order to record a paired peer on the responder side of the
// handshake. Defined here (rather than reusing store.P2PPeerStore wholesale)
// to keep the pairing service's dependencies surgical and to make in-memory
// test fakes trivial.
type PeerPersister interface {
	AddPeer(ctx context.Context, p *store.P2PPeer) error
	UpdateLastSeen(ctx context.Context, peerID string, t time.Time) error
}

// peerUnrevoker is the optional capability the responder side uses to
// clear revoked_at when a re-pair handshake completes against a row that
// was previously revoked. Implemented by store.P2PPeerStore; test fakes
// may skip it.
type peerUnrevoker interface {
	UnrevokePeer(ctx context.Context, peerID string) error
}

// UserLinker is the subset of store.UserStore that PairingService needs
// to link a paired peer to a per-human user row (M7.1). UpsertUser creates
// the user if absent (display_name supplied) and returns the canonical row
// regardless. LinkPeerToUser writes the join-table row.
type UserLinker interface {
	UpsertUser(ctx context.Context, userID, displayName string) error
	LinkPeerToUser(ctx context.Context, peerID, userID string) error
}

// persistRemotePeerTimeout caps the DB call we make on the responder side
// of a handshake. Generous enough for SQLite under load; short enough that
// a wedged DB doesn't hold a libp2p stream open.
const persistRemotePeerTimeout = 5 * time.Second

// persistRemotePeer writes (or refreshes) the initiator's peer row on the
// responder side of a successful handshake. Logs and swallows errors — a
// failure here should not break the handshake the user just completed;
// we'll surface it through audit logs and the CLI list-peers UX.
//
// DisplayName resolution: if the initiator sent a v1 pair request with a
// display_name field we use that; otherwise we fall back to the short
// peer-prefix label so old binaries still produce a sensible row.
func (s *PairingService) persistRemotePeer(remote peer.ID) {
	s.mu.Lock()
	p := s.persister
	logger := s.logger
	s.mu.Unlock()
	if p == nil {
		return
	}
	now := time.Now().UTC()
	peerID := remote.String()
	display := s.takeRemoteDisplayName(peerID)
	if display == "" {
		display = shortPeerLabel(peerID)
	}
	row := &store.P2PPeer{
		PeerID:      peerID,
		DisplayName: display,
		PairedAt:    now,
		TrustLevel:  0,
		Scopes:      []string{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistRemotePeerTimeout)
	defer cancel()
	err := p.AddPeer(ctx, row)
	if err == nil {
		return
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		// Re-pair: clear any prior revocation before refreshing last_seen
		// (UpdateLastSeen no-ops on revoked rows by design).
		if unrevoker, ok := p.(peerUnrevoker); ok {
			if uerr := unrevoker.UnrevokePeer(ctx, peerID); uerr != nil {
				logger.Warn("p2p: unrevoke paired peer",
					"peer", peerID, "err", uerr)
			}
		}
		if uerr := p.UpdateLastSeen(ctx, peerID, now); uerr != nil {
			logger.Warn("p2p: refresh paired peer last_seen",
				"peer", peerID, "err", uerr)
		}
		// Best-effort rename if the remote sent a fresh display_name.
		// Old peers (v0) skip this branch — the existing label stays.
		if display != "" && display != shortPeerLabel(peerID) {
			if updater, ok := p.(displayNameUpdater); ok {
				if uerr := updater.UpdateDisplayName(ctx, peerID, display); uerr != nil {
					logger.Debug("p2p: refresh paired peer display_name",
						"peer", peerID, "err", uerr)
				}
			}
		}
		return
	}
	logger.Error("p2p: persist paired peer (responder side)",
		"peer", peerID, "err", err)
}

// displayNameUpdater is the optional capability the responder side uses to
// rename an already-paired peer when re-pair carries a fresh display_name.
// Implemented by store.P2PPeerStore (sqlite-backed); test fakes may skip it.
type displayNameUpdater interface {
	UpdateDisplayName(ctx context.Context, peerID, newName string) error
}

// shortPeerLabel mirrors the API handler's default display-name policy
// (last 8 chars prefixed with "peer-") so a peer's name is consistent
// across both sides of a pairing.
func shortPeerLabel(id string) string {
	if len(id) <= 8 {
		return "peer-" + id
	}
	return "peer-" + id[len(id)-8:]
}

// linkRemoteUser persists the (user, peer_users) rows for the initiator
// after a successful handshake (M7.1). Failures are logged and swallowed
// — peer rows already exist by this point and an audit-trail miss should
// not break the user-visible pairing flow.
//
// Backward compat: when frame.UserID is empty (legacy initiator) we use a
// deterministic synthetic user_id derived from the peer ID so the row
// shape is stable across re-pairs without colliding across machines.
func (s *PairingService) linkRemoteUser(peerID string, frame remoteIdentity) {
	s.mu.Lock()
	linker := s.userLinker
	logger := s.logger
	s.mu.Unlock()
	if linker == nil {
		return
	}
	userID := frame.UserID
	display := frame.DisplayName
	if userID == "" {
		userID = SyntheticUserIDForPeer(peerID)
	}
	if display == "" {
		display = shortPeerLabel(peerID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistRemotePeerTimeout)
	defer cancel()
	if err := linker.UpsertUser(ctx, userID, display); err != nil {
		logger.Warn("p2p: upsert remote user", "peer", peerID, "user", userID, "err", err)
		return
	}
	if err := linker.LinkPeerToUser(ctx, peerID, userID); err != nil {
		logger.Warn("p2p: link peer to user", "peer", peerID, "user", userID, "err", err)
	}
}

// SyntheticUserIDForPeer mirrors config.SyntheticUserIDForPeer so a legacy
// peer (one that pairs without sending its user_id) lands under the same
// stable ID regardless of whether the responder side (this package) or
// the initiator's HTTP handler (api package) writes the row first.
func SyntheticUserIDForPeer(peerID string) string {
	sum := sha256.Sum256([]byte("mcplexer.user.synthetic:" + peerID))
	h := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
