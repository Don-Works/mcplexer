//go:build p2p

package p2p

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode"
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
	if out.UserID != "" {
		if len(out.UserID) > 128 || !isAlphanumericHyphen(out.UserID) {
			slog.Warn("p2p: invalid user_id in identity frame, discarding", "user_id", out.UserID)
			out.UserID = ""
		}
	}
	if out.DisplayName != "" {
		if len(out.DisplayName) > 256 || containsControlChars(out.DisplayName) {
			slog.Warn("p2p: invalid display_name in identity frame, discarding", "display_name", out.DisplayName)
			out.DisplayName = ""
		}
	}
	return out
}

func isAlphanumericHyphen(s string) bool {
	for _, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func containsControlChars(s string) bool {
	for _, c := range s {
		if unicode.IsControl(c) {
			return true
		}
	}
	return false
}
