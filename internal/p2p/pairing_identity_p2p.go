//go:build p2p

package p2p

import (
	"bufio"
	"encoding/json"
	"strings"
)

// SetSelfIdentity records the local human-user identity (M7.1) so the
// pairing handshake can advertise it in the QR payload + the initiator
// stream frame. Empty values are treated as "not yet bootstrapped" and
// are silently elided — the responder treats a missing user_id as a
// legacy peer and synthesizes one.
func (s *PairingService) SetSelfIdentity(userID, displayName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfUserID = userID
	s.selfDisplay = displayName
}

// SetUserLinker attaches a UserLinker (M7.1). When set, the responder
// side of a successful handshake inserts/links the initiator's user row.
func (s *PairingService) SetUserLinker(u UserLinker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userLinker = u
}

// remoteIdentity holds what the initiator sent on the second protocol line
// of the pairing handshake (M7.1). Both fields may be empty (legacy
// initiator or not-yet-bootstrapped local).
type remoteIdentity struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// readIdentityFrame parses the optional identity line from the wire. On
// any error we return a zero-value frame — the caller treats it as
// "legacy peer, no identity" and synthesizes a user_id from the peer ID
// (see SyntheticUserIDForPeer).
func readIdentityFrame(r *bufio.Reader) remoteIdentity {
	var out remoteIdentity
	line, err := r.ReadString('\n')
	if err != nil || strings.TrimSpace(line) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(line), &out)
	return out
}
