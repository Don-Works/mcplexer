package p2p

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	CollaborationInviteVersion = 1
	collaborationInvitePrefix  = "mcplexer-invite-v1:"
)

var (
	ErrInvalidCollaborationInvite = errors.New("p2p collaboration: invalid invitation")
	ErrSSHAgentUnavailable        = errors.New("p2p collaboration: SSH agent unavailable")
)

// CollaborationInvitationPayload is the copy/QR-safe invitation. Token is a
// bearer secret and is returned only at creation time; the database stores
// only its SHA-256 digest.
type CollaborationInvitationPayload struct {
	Version                int       `json:"version"`
	InvitationID           string    `json:"invitation_id"`
	Token                  string    `json:"token"`
	HomePeerID             string    `json:"home_peer_id"`
	HomeAddrs              []string  `json:"home_addrs,omitempty"`
	IdentityKeyFingerprint string    `json:"identity_key_fingerprint"`
	ExpiresAt              time.Time `json:"expires_at"`
}

type CollaborationJoinOptions struct {
	Invitation string `json:"invitation"`
	DeviceName string `json:"device_name"`
	DeviceKind string `json:"device_kind"`
}

type CollaborationJoinResult struct {
	PrincipalID string                         `json:"principal_id"`
	Device      *store.PrincipalDevice         `json:"device"`
	Grants      []store.WorkspaceGrant         `json:"grants"`
	Workspaces  []CollaborationWorkspaceAccess `json:"workspaces"`
}

// CollaborationWorkspaceAccess is the minimum non-sensitive description a
// joining daemon needs to create a local mirror mapping. It contains no root
// path, task content, evidence, or authority: the listed capabilities are a
// cache and every operation remains subject to the home node's live checks.
type CollaborationWorkspaceAccess struct {
	ShareID           string                            `json:"share_id"`
	HomePeerID        string                            `json:"home_peer_id"`
	RemoteWorkspaceID string                            `json:"remote_workspace_id"`
	WorkspaceName     string                            `json:"workspace_name"`
	AccessEpoch       int64                             `json:"access_epoch"`
	Capabilities      []string                          `json:"capabilities"`
	Policy            *store.WorkspacePublicationPolicy `json:"policy,omitempty"`
}

// CollaborationInviteService owns the proof-bound invitation transport. The
// same shape exists in slim builds; Join then returns ErrP2PNotBuiltIn.
type CollaborationInviteService struct {
	host    *Host
	store   store.CollaborationStore
	limiter collaborationInviteLimiter
}

const (
	collaborationInviteRateWindow  = 5 * time.Minute
	collaborationInviteRemoteBurst = 30
	collaborationInviteTokenBurst  = 8
)

type collaborationInviteLimiter struct {
	mu      sync.Mutex
	remote  map[string][]time.Time
	invites map[string][]time.Time
}

func (l *collaborationInviteLimiter) allow(remotePeerID, invitationID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.remote == nil {
		l.remote = make(map[string][]time.Time)
		l.invites = make(map[string][]time.Time)
	}
	cutoff := now.Add(-collaborationInviteRateWindow)
	prune := func(values []time.Time) []time.Time {
		first := 0
		for first < len(values) && values[first].Before(cutoff) {
			first++
		}
		return append([]time.Time(nil), values[first:]...)
	}
	remote := prune(l.remote[remotePeerID])
	inviteKey := remotePeerID + "\x00" + invitationID
	invite := prune(l.invites[inviteKey])
	if len(remote) >= collaborationInviteRemoteBurst || len(invite) >= collaborationInviteTokenBurst {
		l.remote[remotePeerID] = remote
		l.invites[inviteKey] = invite
		return false
	}
	l.remote[remotePeerID] = append(remote, now)
	l.invites[inviteKey] = append(invite, now)
	return true
}

func (s *CollaborationInviteService) LocalPeerID() string {
	if s == nil || s.host == nil {
		return ""
	}
	return s.host.PeerID()
}

func (s *CollaborationInviteService) LocalAddrs() []string {
	if s == nil || s.host == nil {
		return []string{}
	}
	addrs := s.host.Addrs()
	if addrs == nil {
		return []string{}
	}
	return addrs
}

func EncodeCollaborationInvitation(payload CollaborationInvitationPayload) (string, error) {
	if err := validateCollaborationInvitationPayload(payload); err != nil {
		return "", err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode collaboration invitation: %w", err)
	}
	return collaborationInvitePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodeCollaborationInvitation(encoded string) (CollaborationInvitationPayload, error) {
	encoded = strings.TrimSpace(encoded)
	if !strings.HasPrefix(encoded, collaborationInvitePrefix) {
		return CollaborationInvitationPayload{}, ErrInvalidCollaborationInvite
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, collaborationInvitePrefix))
	if err != nil || len(raw) == 0 || len(raw) > 32*1024 {
		return CollaborationInvitationPayload{}, ErrInvalidCollaborationInvite
	}
	var payload CollaborationInvitationPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CollaborationInvitationPayload{}, ErrInvalidCollaborationInvite
	}
	if err := validateCollaborationInvitationPayload(payload); err != nil {
		return CollaborationInvitationPayload{}, err
	}
	return payload, nil
}

func validateCollaborationInvitationPayload(payload CollaborationInvitationPayload) error {
	if payload.Version != CollaborationInviteVersion || strings.TrimSpace(payload.InvitationID) == "" ||
		strings.TrimSpace(payload.HomePeerID) == "" || strings.TrimSpace(payload.IdentityKeyFingerprint) == "" ||
		payload.ExpiresAt.IsZero() {
		return ErrInvalidCollaborationInvite
	}
	token, err := base64.RawURLEncoding.DecodeString(payload.Token)
	if err != nil || len(token) != 32 {
		return ErrInvalidCollaborationInvite
	}
	if len(payload.HomeAddrs) > 32 {
		return ErrInvalidCollaborationInvite
	}
	return nil
}

// Join is implemented per build mode because the live path opens a libp2p
// stream and asks ssh-agent to sign the server challenge.
func (s *CollaborationInviteService) Join(ctx context.Context, options CollaborationJoinOptions) (*CollaborationJoinResult, error) {
	return s.join(ctx, options)
}
