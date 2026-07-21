//go:build p2p

package p2p

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

const (
	CollaborationInviteProtocol   protocol.ID = "/mcplexer/collaboration-invite/1.0.0"
	collaborationInviteDeadline               = 45 * time.Second
	collaborationInviteFrameLimit             = 64 * 1024
)

type collaborationJoinStart struct {
	InvitationID string `json:"invitation_id"`
	Token        string `json:"token"`
	DeviceName   string `json:"device_name"`
	DeviceKind   string `json:"device_kind"`
}

type collaborationJoinProof struct {
	Signature string `json:"signature"`
}

type collaborationJoinFrame struct {
	Challenge  *DeviceBindingChallenge        `json:"challenge,omitempty"`
	Device     *store.PrincipalDevice         `json:"device,omitempty"`
	Grants     []store.WorkspaceGrant         `json:"grants,omitempty"`
	Workspaces []CollaborationWorkspaceAccess `json:"workspaces,omitempty"`
	Error      string                         `json:"error,omitempty"`
}

func NewCollaborationInviteService(host *Host, collaborationStore store.CollaborationStore) *CollaborationInviteService {
	s := &CollaborationInviteService{host: host, store: collaborationStore}
	if host != nil && collaborationStore != nil {
		host.Inner().SetStreamHandler(CollaborationInviteProtocol, s.handleCollaborationInviteStream)
	}
	return s
}

func (s *CollaborationInviteService) join(ctx context.Context, options CollaborationJoinOptions) (*CollaborationJoinResult, error) {
	if s == nil || s.host == nil || s.store == nil {
		return nil, ErrP2PNotBuiltIn
	}
	payload, err := DecodeCollaborationInvitation(options.Invitation)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(payload.ExpiresAt) {
		return nil, fmt.Errorf("%w: expired", ErrInvalidCollaborationInvite)
	}
	deviceName := strings.TrimSpace(options.DeviceName)
	if deviceName == "" {
		return nil, fmt.Errorf("device_name is required")
	}
	deviceKind := strings.TrimSpace(options.DeviceKind)
	if deviceKind == "" {
		deviceKind = "unknown"
	}
	remote, err := peer.Decode(payload.HomePeerID)
	if err != nil {
		return nil, ErrInvalidCollaborationInvite
	}
	if err := s.connectInvitationHome(ctx, remote, payload.HomeAddrs); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithTimeout(ctx, collaborationInviteDeadline)
	defer cancel()
	stream, err := s.host.Inner().NewStream(streamCtx, remote, CollaborationInviteProtocol)
	if err != nil {
		return nil, fmt.Errorf("open collaboration invitation stream: %w", err)
	}
	defer stream.Close() //nolint:errcheck
	_ = stream.SetDeadline(time.Now().Add(collaborationInviteDeadline))
	enc := json.NewEncoder(stream)
	dec := json.NewDecoder(io.LimitReader(stream, collaborationInviteFrameLimit*3))
	if err := enc.Encode(collaborationJoinStart{
		InvitationID: payload.InvitationID,
		Token:        payload.Token,
		DeviceName:   deviceName,
		DeviceKind:   deviceKind,
	}); err != nil {
		return nil, fmt.Errorf("send invitation claim: %w", err)
	}
	var challengeFrame collaborationJoinFrame
	if err := dec.Decode(&challengeFrame); err != nil {
		return nil, fmt.Errorf("read identity challenge: %w", err)
	}
	if challengeFrame.Error != "" || challengeFrame.Challenge == nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCollaborationInvite, safeInviteError(challengeFrame.Error))
	}
	if challengeFrame.Challenge.IdentityKeyFingerprint != payload.IdentityKeyFingerprint ||
		challengeFrame.Challenge.InvitationID != payload.InvitationID ||
		challengeFrame.Challenge.InitiatorPeerID != s.host.PeerID() ||
		challengeFrame.Challenge.ResponderPeerID != payload.HomePeerID {
		return nil, ErrInvalidBindingChallenge
	}
	signature, err := SignDeviceBindingWithSSHAgent(ctx, payload.IdentityKeyFingerprint, *challengeFrame.Challenge)
	if err != nil {
		return nil, err
	}
	if err := enc.Encode(collaborationJoinProof{Signature: base64.RawURLEncoding.EncodeToString(signature)}); err != nil {
		return nil, fmt.Errorf("send identity proof: %w", err)
	}
	var result collaborationJoinFrame
	if err := dec.Decode(&result); err != nil {
		return nil, fmt.Errorf("read activation result: %w", err)
	}
	if result.Error != "" || result.Device == nil {
		return nil, fmt.Errorf("identity activation failed: %s", safeInviteError(result.Error))
	}
	if result.Grants == nil {
		result.Grants = []store.WorkspaceGrant{}
	}
	if result.Workspaces == nil {
		result.Workspaces = []CollaborationWorkspaceAccess{}
	}
	for _, workspace := range result.Workspaces {
		if workspace.HomePeerID != payload.HomePeerID || workspace.ShareID == "" ||
			workspace.RemoteWorkspaceID == "" || strings.TrimSpace(workspace.WorkspaceName) == "" ||
			len(workspace.WorkspaceName) > 512 || workspace.AccessEpoch < 1 {
			return nil, ErrInvalidCollaborationInvite
		}
		for _, capability := range workspace.Capabilities {
			if !store.ValidWorkspaceCapability(capability) {
				return nil, ErrInvalidCollaborationInvite
			}
		}
	}
	return &CollaborationJoinResult{
		PrincipalID: result.Device.PrincipalID,
		Device:      result.Device,
		Grants:      result.Grants,
		Workspaces:  result.Workspaces,
	}, nil
}

func (s *CollaborationInviteService) connectInvitationHome(ctx context.Context, remote peer.ID, rawAddrs []string) error {
	addrs := make([]multiaddr.Multiaddr, 0, len(rawAddrs))
	for _, raw := range rawAddrs {
		addr, err := multiaddr.NewMultiaddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		if info, err := peer.AddrInfoFromP2pAddr(addr); err == nil {
			if info.ID != remote {
				continue
			}
			addrs = append(addrs, info.Addrs...)
			continue
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) > 0 {
		s.host.Inner().Peerstore().AddAddrs(remote, addrs, peerstore.TempAddrTTL)
		if err := s.host.ConnectAddrInfo(ctx, peer.AddrInfo{ID: remote, Addrs: addrs}); err == nil {
			return nil
		}
	}
	info, err := s.host.FindPeer(ctx, remote)
	if err != nil {
		return fmt.Errorf("locate invitation home %s: %w", remote, err)
	}
	if err := s.host.ConnectAddrInfo(ctx, info); err != nil {
		return fmt.Errorf("connect invitation home %s: %w", remote, err)
	}
	return nil
}

func (s *CollaborationInviteService) handleCollaborationInviteStream(stream network.Stream) {
	defer stream.Close() //nolint:errcheck
	_ = stream.SetDeadline(time.Now().Add(collaborationInviteDeadline))
	enc := json.NewEncoder(stream)
	dec := json.NewDecoder(io.LimitReader(stream, collaborationInviteFrameLimit*3))
	remotePeerID := stream.Conn().RemotePeer().String()
	var start collaborationJoinStart
	if err := dec.Decode(&start); err != nil {
		writeInviteError(enc, "invalid_request")
		return
	}
	start.InvitationID = strings.TrimSpace(start.InvitationID)
	start.DeviceName = strings.TrimSpace(start.DeviceName)
	start.DeviceKind = strings.TrimSpace(start.DeviceKind)
	if start.InvitationID == "" || len(start.InvitationID) > 256 ||
		start.DeviceName == "" || len(start.DeviceName) > 256 || strings.ContainsAny(start.DeviceName, "\x00\r\n") ||
		(start.DeviceKind != "laptop" && start.DeviceKind != "server" && start.DeviceKind != "daemon" && start.DeviceKind != "unknown") ||
		!s.limiter.allow(remotePeerID, start.InvitationID, time.Now().UTC()) {
		writeInviteError(enc, "invalid_or_expired")
		return
	}
	invitation, _, err := s.store.GetPrincipalInvitation(context.Background(), start.InvitationID)
	if err != nil {
		writeInviteError(enc, "invalid_or_expired")
		return
	}
	token, err := base64.RawURLEncoding.DecodeString(start.Token)
	if err != nil || len(token) != 32 {
		writeInviteError(enc, "invalid_or_expired")
		return
	}
	tokenHash := sha256.Sum256(token)
	now := time.Now().UTC()
	if subtle.ConstantTimeCompare(invitation.TokenHash, tokenHash[:]) != 1 || invitation.ConsumedAt != nil ||
		invitation.RevokedAt != nil || !invitation.ExpiresAt.After(now) {
		writeInviteError(enc, "invalid_or_expired")
		return
	}
	principal, err := s.store.GetPrincipal(context.Background(), invitation.PrincipalID)
	if err != nil {
		writeInviteError(enc, "identity_unavailable")
		return
	}
	key, err := s.store.GetPrincipalIdentityKey(context.Background(), invitation.IdentityKeyID)
	if err != nil || key.PrincipalID != principal.ID {
		writeInviteError(enc, "identity_unavailable")
		return
	}
	challenge, err := NewDeviceBindingChallenge(nil, now, invitation.ID, principal.ID,
		PrincipalKind(principal.Kind), key.Fingerprint, remotePeerID, s.host.PeerID())
	if err != nil {
		writeInviteError(enc, "challenge_failed")
		return
	}
	transcriptHash, err := challenge.TranscriptSHA256()
	if err != nil {
		writeInviteError(enc, "challenge_failed")
		return
	}
	nonce, _ := base64.RawURLEncoding.DecodeString(challenge.Nonce)
	nonceHash := sha256.Sum256(nonce)
	if err := s.store.CreatePrincipalIdentityChallenge(context.Background(), &store.PrincipalIdentityChallenge{
		ID: challenge.ChallengeID, InvitationID: invitation.ID,
		InitiatorPeerID: remotePeerID, ResponderPeerID: s.host.PeerID(),
		NonceHash: nonceHash[:], TranscriptHash: transcriptHash,
		IssuedAt: challenge.IssuedAt, ExpiresAt: challenge.ExpiresAt,
	}); err != nil {
		writeInviteError(enc, "challenge_failed")
		return
	}
	if err := enc.Encode(collaborationJoinFrame{Challenge: &challenge}); err != nil {
		return
	}
	var proof collaborationJoinProof
	if err := dec.Decode(&proof); err != nil {
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(proof.Signature)
	if err != nil {
		writeInviteError(enc, "proof_invalid")
		return
	}
	if _, err := VerifyDeviceBinding(key.CanonicalPublicKey, challenge, signature, time.Now().UTC()); err != nil {
		writeInviteError(enc, "proof_invalid")
		return
	}
	device, _, err := s.store.ActivateInvitedDevice(context.Background(), store.InvitedDeviceActivation{
		InvitationID: invitation.ID, InvitationTokenHash: tokenHash[:],
		ChallengeID: challenge.ChallengeID, PeerID: remotePeerID,
		ResponderPeerID: s.host.PeerID(), DisplayName: start.DeviceName,
		DeviceKind: start.DeviceKind, BindingVersion: DeviceBindingTranscriptVersion,
		BindingTranscriptHash: transcriptHash, BindingSignature: signature,
		At: time.Now().UTC(),
	})
	if err != nil {
		writeInviteError(enc, "activation_failed")
		return
	}
	grants, err := s.principalWorkspaceGrants(context.Background(), principal.ID, time.Now().UTC())
	if err != nil {
		writeInviteError(enc, "activation_failed")
		return
	}
	workspaces, err := s.workspaceAccesses(context.Background(), grants)
	if err != nil {
		writeInviteError(enc, "activation_failed")
		return
	}
	_ = enc.Encode(collaborationJoinFrame{Device: device, Grants: grants, Workspaces: workspaces})
}

func (s *CollaborationInviteService) principalWorkspaceGrants(ctx context.Context, principalID string, at time.Time) ([]store.WorkspaceGrant, error) {
	shares, err := s.store.ListWorkspaceShares(ctx)
	if err != nil {
		return nil, err
	}
	var result []store.WorkspaceGrant
	for _, share := range shares {
		grants, err := s.store.ListWorkspaceGrants(ctx, share.ShareID, false)
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			if grant.PrincipalID == principalID && grant.RevokedAt == nil &&
				(grant.ExpiresAt == nil || grant.ExpiresAt.After(at)) {
				result = append(result, grant)
			}
		}
	}
	if result == nil {
		result = []store.WorkspaceGrant{}
	}
	return result, nil
}

type collaborationWorkspaceReader interface {
	GetWorkspace(ctx context.Context, id string) (*store.Workspace, error)
}

func (s *CollaborationInviteService) workspaceAccesses(ctx context.Context, grants []store.WorkspaceGrant) ([]CollaborationWorkspaceAccess, error) {
	byShare := make(map[string][]string)
	order := make([]string, 0)
	for _, grant := range grants {
		if _, exists := byShare[grant.ShareID]; !exists {
			order = append(order, grant.ShareID)
		}
		byShare[grant.ShareID] = append(byShare[grant.ShareID], grant.Capability)
	}
	accesses := make([]CollaborationWorkspaceAccess, 0, len(order))
	workspaceReader, _ := s.store.(collaborationWorkspaceReader)
	for _, shareID := range order {
		share, err := s.store.GetWorkspaceShare(ctx, shareID)
		if err != nil || share.Status != store.WorkspaceShareStatusActive {
			return nil, store.ErrConflict
		}
		name := "Shared workspace"
		if workspaceReader != nil {
			if workspace, lookupErr := workspaceReader.GetWorkspace(ctx, share.LocalWorkspaceID); lookupErr == nil && strings.TrimSpace(workspace.Name) != "" {
				name = workspace.Name
			}
		}
		policy, _ := s.store.GetWorkspacePublicationPolicy(ctx, shareID)
		accesses = append(accesses, CollaborationWorkspaceAccess{
			ShareID: share.ShareID, HomePeerID: share.HomePeerID,
			RemoteWorkspaceID: share.LocalWorkspaceID, WorkspaceName: name,
			AccessEpoch:  share.AccessEpoch,
			Capabilities: append([]string(nil), byShare[shareID]...), Policy: policy,
		})
	}
	return accesses, nil
}

func writeInviteError(enc *json.Encoder, code string) {
	_ = enc.Encode(collaborationJoinFrame{Error: code})
}

func safeInviteError(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "protocol_error"
	}
	if len(code) > 64 {
		return "protocol_error"
	}
	return code
}

var _ = errors.Is
