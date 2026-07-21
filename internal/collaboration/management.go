package collaboration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

const (
	DefaultInvitationTTL = 7 * 24 * time.Hour
	MaxInvitationTTL     = 30 * 24 * time.Hour
)

type InvitationGrantInput struct {
	ShareID      string   `json:"share_id"`
	Capabilities []string `json:"capabilities"`
}

type CreateInvitationInput struct {
	Purpose                string                 `json:"purpose"`
	PrincipalID            string                 `json:"principal_id,omitempty"`
	Kind                   string                 `json:"kind,omitempty"`
	DisplayName            string                 `json:"display_name,omitempty"`
	ControllingPrincipalID string                 `json:"controlling_principal_id,omitempty"`
	PublicKey              string                 `json:"public_key"`
	ReplacesKeyID          string                 `json:"replaces_key_id,omitempty"`
	WorkspaceGrants        []InvitationGrantInput `json:"workspace_grants,omitempty"`
	ExpiresIn              time.Duration          `json:"-"`
}

type InvitationResult struct {
	Principal   *store.Principal            `json:"principal"`
	IdentityKey *store.PrincipalIdentityKey `json:"identity_key"`
	Invitation  *store.PrincipalInvitation  `json:"invitation"`
	InviteCode  string                      `json:"invite_code"`
}

// ManagementStore keeps collaboration opt-in instead of inflating the global
// store.Store interface (which is implemented by many unrelated test fakes).
type ManagementStore interface {
	store.Store
	store.CollaborationStore
	store.CollaborationMembershipStore
}

// InviteTransport is the narrow live-P2P surface collaboration management
// needs. Keeping it as an interface makes invitation policy independently
// testable without weakening the production transport implementation.
type InviteTransport interface {
	LocalPeerID() string
	LocalAddrs() []string
	Join(context.Context, p2p.CollaborationJoinOptions) (*p2p.CollaborationJoinResult, error)
}

type Manager struct {
	store     ManagementStore
	transport InviteTransport
	now       func() time.Time
	random    io.Reader
}

func NewManager(st ManagementStore, transport InviteTransport) *Manager {
	return &Manager{
		store: st, transport: transport,
		now: func() time.Time { return time.Now().UTC() }, random: rand.Reader,
	}
}

func (m *Manager) LocalPeerID() string {
	if m == nil || m.transport == nil {
		return ""
	}
	return m.transport.LocalPeerID()
}

func (m *Manager) LocalAddrs() []string {
	if m == nil || m.transport == nil {
		return []string{}
	}
	return m.transport.LocalAddrs()
}

// EnsureLocalOwner closes the fresh-install ordering gap: schema migrations
// run before BootstrapSelfUser creates users.is_self, so the migration cannot
// always seed the corresponding collaboration principal itself.
func (m *Manager) EnsureLocalOwner(ctx context.Context, self *store.User) (*store.Principal, error) {
	if m == nil || m.store == nil || self == nil || strings.TrimSpace(self.UserID) == "" {
		return nil, fmt.Errorf("local user is required")
	}
	principals, err := m.store.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	for i := range principals {
		if principals[i].IsLocalOwner {
			return &principals[i], nil
		}
	}
	now := m.now()
	principal := &store.Principal{
		ID: self.UserID, Kind: store.PrincipalKindPerson,
		DisplayName: self.DisplayName, Status: store.PrincipalStatusActive,
		IsLocalOwner: true, CreatedAt: now, UpdatedAt: now, ActivatedAt: &now,
	}
	if err := m.store.CreatePrincipal(ctx, principal); err != nil {
		return nil, fmt.Errorf("create local collaboration owner: %w", err)
	}
	return principal, nil
}

func (m *Manager) LocalOwner(ctx context.Context) (*store.Principal, error) {
	if m == nil || m.store == nil {
		return nil, store.ErrNotFound
	}
	principals, err := m.store.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	for i := range principals {
		if principals[i].IsLocalOwner && principals[i].Status == store.PrincipalStatusActive {
			return &principals[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// EnsureWorkspaceShares creates only local, default-private share metadata.
// It does not grant anybody access or publish a task.
func (m *Manager) EnsureWorkspaceShares(ctx context.Context) ([]store.WorkspaceShare, error) {
	owner, err := m.LocalOwner(ctx)
	if err != nil {
		return nil, err
	}
	peerID := m.LocalPeerID()
	if peerID == "" {
		return nil, p2p.ErrP2PNotBuiltIn
	}
	workspaces, err := m.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for i := range workspaces {
		if _, err := m.store.GetWorkspaceShareByLocalWorkspaceID(ctx, workspaces[i].ID); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		share := &store.WorkspaceShare{
			LocalWorkspaceID: workspaces[i].ID, HomePeerID: peerID,
			OwnerPrincipalID: owner.ID, Status: store.WorkspaceShareStatusActive,
			CreatedAt: m.now(),
		}
		if err := m.store.CreateWorkspaceShare(ctx, share); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
			return nil, err
		}
	}
	return m.store.ListWorkspaceShares(ctx)
}

func (m *Manager) CreateInvitation(ctx context.Context, input CreateInvitationInput) (*InvitationResult, error) {
	if m == nil || m.store == nil || m.transport == nil || m.LocalPeerID() == "" {
		return nil, p2p.ErrP2PNotBuiltIn
	}
	owner, err := m.LocalOwner(ctx)
	if err != nil {
		return nil, err
	}
	parsedKey, err := p2p.ParseIdentityPublicKey(input.PublicKey)
	if err != nil {
		return nil, err
	}
	purpose := strings.TrimSpace(input.Purpose)
	if purpose == "" {
		purpose = store.InvitationPurposeNewPrincipal
	}
	now := m.now().UTC().Truncate(time.Second)
	ttl := input.ExpiresIn
	if ttl <= 0 {
		ttl = DefaultInvitationTTL
	}
	if ttl > MaxInvitationTTL {
		return nil, fmt.Errorf("invitation expiry exceeds %s", MaxInvitationTTL)
	}
	token := make([]byte, 32)
	if _, err := io.ReadFull(m.random, token); err != nil {
		return nil, fmt.Errorf("generate invitation token: %w", err)
	}
	tokenHash := sha256.Sum256(token)
	var principal *store.Principal
	var identityKey *store.PrincipalIdentityKey
	invitation := &store.PrincipalInvitation{
		ID: ulid.Make().String(), Purpose: purpose,
		CreatedByPrincipalID: owner.ID, CreatedAt: now, ExpiresAt: now.Add(ttl),
		TokenHash: append([]byte(nil), tokenHash[:]...),
	}
	staged, err := normalizeInvitationGrants(invitation.ID, input.WorkspaceGrants)
	if err != nil {
		return nil, err
	}
	err = m.store.Tx(ctx, func(base store.Store) error {
		tx, ok := base.(ManagementStore)
		if !ok {
			return fmt.Errorf("transaction store does not support collaboration")
		}
		switch purpose {
		case store.InvitationPurposeNewPrincipal:
			principal, identityKey, err = prepareNewPrincipal(tx, ctx, owner, input, parsedKey, now)
		case store.InvitationPurposeAddDevice:
			principal, identityKey, err = prepareExistingDevice(tx, ctx, input, parsedKey)
		case store.InvitationPurposeRotateKey:
			principal, identityKey, err = prepareRotatedKey(tx, ctx, owner, input, parsedKey, now)
		default:
			return fmt.Errorf("unknown invitation purpose %q", purpose)
		}
		if err != nil {
			return err
		}
		invitation.PrincipalID = principal.ID
		invitation.IdentityKeyID = identityKey.ID
		if err := tx.CreatePrincipalInvitation(ctx, invitation, staged); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{
			"purpose": purpose, "expires_at": invitation.ExpiresAt,
			"key_fingerprint": identityKey.Fingerprint,
		})
		return tx.AppendCollaborationAudit(ctx, &store.CollaborationAuditEvent{
			Event: "invitation.created", ActorPrincipalID: owner.ID,
			SubjectKind: "invitation", SubjectID: invitation.ID,
			DetailsJSON: details, CreatedAt: now,
		})
	})
	if err != nil {
		return nil, err
	}
	code, err := p2p.EncodeCollaborationInvitation(p2p.CollaborationInvitationPayload{
		Version: p2p.CollaborationInviteVersion, InvitationID: invitation.ID,
		Token:      base64.RawURLEncoding.EncodeToString(token),
		HomePeerID: m.LocalPeerID(), HomeAddrs: m.LocalAddrs(),
		IdentityKeyFingerprint: identityKey.Fingerprint, ExpiresAt: invitation.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	return &InvitationResult{
		Principal: principal, IdentityKey: identityKey,
		Invitation: invitation, InviteCode: code,
	}, nil
}

func normalizeInvitationGrants(invitationID string, groups []InvitationGrantInput) ([]store.InvitationWorkspaceGrant, error) {
	var grants []store.InvitationWorkspaceGrant
	seen := make(map[string]struct{})
	for _, group := range groups {
		shareID := strings.TrimSpace(group.ShareID)
		if shareID == "" {
			return nil, fmt.Errorf("workspace grant share_id is required")
		}
		for _, capability := range group.Capabilities {
			capability = strings.TrimSpace(capability)
			if !store.ValidWorkspaceCapability(capability) {
				return nil, fmt.Errorf("unknown workspace capability %q", capability)
			}
			key := shareID + "\x00" + capability
			if _, ok := seen[key]; ok {
				return nil, fmt.Errorf("duplicate workspace capability %q", capability)
			}
			seen[key] = struct{}{}
			grants = append(grants, store.InvitationWorkspaceGrant{
				InvitationID: invitationID, ShareID: shareID,
				Capability: capability, ConstraintsJSON: json.RawMessage(`{}`),
			})
		}
	}
	return grants, nil
}

func prepareNewPrincipal(
	tx ManagementStore,
	ctx context.Context,
	owner *store.Principal,
	input CreateInvitationInput,
	parsed p2p.IdentityPublicKey,
	now time.Time,
) (*store.Principal, *store.PrincipalIdentityKey, error) {
	kind := strings.TrimSpace(input.Kind)
	if kind != store.PrincipalKindPerson && kind != store.PrincipalKindMachine {
		return nil, nil, fmt.Errorf("kind must be person or machine")
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		return nil, nil, fmt.Errorf("display_name is required")
	}
	controller := strings.TrimSpace(input.ControllingPrincipalID)
	if kind == store.PrincipalKindMachine && controller == "" {
		controller = owner.ID
	}
	if kind == store.PrincipalKindMachine {
		controllerPrincipal, err := tx.GetPrincipal(ctx, controller)
		if err != nil || controllerPrincipal.Status != store.PrincipalStatusActive ||
			controllerPrincipal.Kind != store.PrincipalKindPerson {
			return nil, nil, fmt.Errorf("machine controller must be an active person: %w", coalesceStoreError(err))
		}
	}
	principal := &store.Principal{
		ID: ulid.Make().String(), Kind: kind, DisplayName: displayName,
		Status: store.PrincipalStatusPending, ControllingPrincipalID: controller,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.CreatePrincipal(ctx, principal); err != nil {
		return nil, nil, err
	}
	key := &store.PrincipalIdentityKey{
		ID: ulid.Make().String(), PrincipalID: principal.ID,
		CanonicalPublicKey: parsed.AuthorizedKey, Fingerprint: parsed.Fingerprint,
		Algorithm: parsed.Algorithm, Comment: parsed.Comment,
		Status: store.PrincipalKeyStatusPending, AddedByPrincipalID: owner.ID,
		CreatedAt: now,
	}
	if err := tx.AddPrincipalIdentityKey(ctx, key); err != nil {
		return nil, nil, err
	}
	return principal, key, nil
}

func prepareExistingDevice(
	tx ManagementStore,
	ctx context.Context,
	input CreateInvitationInput,
	parsed p2p.IdentityPublicKey,
) (*store.Principal, *store.PrincipalIdentityKey, error) {
	principal, err := tx.GetPrincipal(ctx, strings.TrimSpace(input.PrincipalID))
	if err != nil || principal.Status != store.PrincipalStatusActive {
		return nil, nil, fmt.Errorf("active principal is required: %w", coalesceStoreError(err))
	}
	key, err := tx.GetPrincipalIdentityKeyByFingerprint(ctx, parsed.Fingerprint)
	if err != nil || key.PrincipalID != principal.ID || key.Status != store.PrincipalKeyStatusActive {
		return nil, nil, fmt.Errorf("an active key belonging to the principal is required: %w", coalesceStoreError(err))
	}
	return principal, key, nil
}

func prepareRotatedKey(
	tx ManagementStore,
	ctx context.Context,
	owner *store.Principal,
	input CreateInvitationInput,
	parsed p2p.IdentityPublicKey,
	now time.Time,
) (*store.Principal, *store.PrincipalIdentityKey, error) {
	principal, err := tx.GetPrincipal(ctx, strings.TrimSpace(input.PrincipalID))
	if err != nil || principal.Status != store.PrincipalStatusActive {
		return nil, nil, fmt.Errorf("active principal is required: %w", coalesceStoreError(err))
	}
	existing, lookupErr := tx.GetPrincipalIdentityKeyByFingerprint(ctx, parsed.Fingerprint)
	if lookupErr == nil {
		if existing.PrincipalID == principal.ID && existing.Status == store.PrincipalKeyStatusPending {
			return principal, existing, nil
		}
		return nil, nil, fmt.Errorf("identity key is already registered: %w", store.ErrConflict)
	}
	if !errors.Is(lookupErr, store.ErrNotFound) {
		return nil, nil, lookupErr
	}
	replacesKeyID := strings.TrimSpace(input.ReplacesKeyID)
	if replacesKeyID != "" {
		replaced, err := tx.GetPrincipalIdentityKey(ctx, replacesKeyID)
		if err != nil || replaced.PrincipalID != principal.ID || replaced.Status != store.PrincipalKeyStatusActive {
			return nil, nil, fmt.Errorf("replacement target must be an active key for the same principal: %w", coalesceStoreError(err))
		}
	}
	key := &store.PrincipalIdentityKey{
		ID: ulid.Make().String(), PrincipalID: principal.ID,
		CanonicalPublicKey: parsed.AuthorizedKey, Fingerprint: parsed.Fingerprint,
		Algorithm: parsed.Algorithm, Comment: parsed.Comment,
		Status: store.PrincipalKeyStatusPending, ReplacesKeyID: replacesKeyID,
		AddedByPrincipalID: owner.ID, CreatedAt: now,
	}
	if err := tx.AddPrincipalIdentityKey(ctx, key); err != nil {
		return nil, nil, err
	}
	return principal, key, nil
}

func coalesceStoreError(err error) error {
	if err == nil {
		return store.ErrConflict
	}
	return err
}

// EnrollLocalIdentity performs the one-time owner bootstrap against
// ssh-agent. The private key never crosses the agent socket and the same
// replay-safe invitation/challenge transaction is used as remote joins.
func (m *Manager) EnrollLocalIdentity(
	ctx context.Context,
	publicKey string,
	deviceName string,
	deviceKind string,
) (*p2p.CollaborationJoinResult, error) {
	owner, err := m.LocalOwner(ctx)
	if err != nil {
		return nil, err
	}
	peerID := m.LocalPeerID()
	if peerID == "" {
		return nil, p2p.ErrP2PNotBuiltIn
	}
	parsed, err := p2p.ParseIdentityPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	replacesKeyID := ""
	if device, principal, resolveErr := m.store.ResolveActivePrincipalForPeer(ctx, peerID); resolveErr == nil && principal.ID == owner.ID {
		currentKey, keyErr := m.store.GetPrincipalIdentityKey(ctx, device.IdentityKeyID)
		if keyErr != nil {
			return nil, keyErr
		}
		if currentKey.Fingerprint == parsed.Fingerprint {
			return &p2p.CollaborationJoinResult{PrincipalID: owner.ID, Device: device, Grants: []store.WorkspaceGrant{}}, nil
		}
		replacesKeyID = currentKey.ID
	} else if resolveErr != nil && !errors.Is(resolveErr, store.ErrNotFound) {
		return nil, resolveErr
	}
	purpose := store.InvitationPurposeRotateKey
	if existing, lookupErr := m.store.GetPrincipalIdentityKeyByFingerprint(ctx, parsed.Fingerprint); lookupErr == nil {
		if existing.PrincipalID != owner.ID || existing.Status == store.PrincipalKeyStatusRevoked {
			return nil, fmt.Errorf("identity key is not active for the local owner: %w", store.ErrConflict)
		}
		if existing.Status == store.PrincipalKeyStatusActive {
			purpose = store.InvitationPurposeAddDevice
		}
	} else if !errors.Is(lookupErr, store.ErrNotFound) {
		return nil, lookupErr
	}
	created, err := m.CreateInvitation(ctx, CreateInvitationInput{
		Purpose: purpose, PrincipalID: owner.ID, PublicKey: publicKey,
		ReplacesKeyID: replacesKeyID,
	})
	if err != nil {
		return nil, err
	}
	payload, err := p2p.DecodeCollaborationInvitation(created.InviteCode)
	if err != nil {
		return nil, err
	}
	token, _ := base64.RawURLEncoding.DecodeString(payload.Token)
	tokenHash := sha256.Sum256(token)
	now := m.now().UTC().Truncate(time.Second)
	challenge, err := p2p.NewDeviceBindingChallenge(nil, now, created.Invitation.ID, owner.ID,
		p2p.PrincipalKindPerson, parsed.Fingerprint, peerID, peerID)
	if err != nil {
		return nil, err
	}
	transcriptHash, err := challenge.TranscriptSHA256()
	if err != nil {
		return nil, err
	}
	nonce, _ := base64.RawURLEncoding.DecodeString(challenge.Nonce)
	nonceHash := sha256.Sum256(nonce)
	if err := m.store.CreatePrincipalIdentityChallenge(ctx, &store.PrincipalIdentityChallenge{
		ID: challenge.ChallengeID, InvitationID: created.Invitation.ID,
		InitiatorPeerID: peerID, ResponderPeerID: peerID,
		NonceHash: nonceHash[:], TranscriptHash: transcriptHash,
		IssuedAt: challenge.IssuedAt, ExpiresAt: challenge.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	signature, err := p2p.SignDeviceBindingWithSSHAgent(ctx, parsed.Fingerprint, challenge)
	if err != nil {
		return nil, err
	}
	if _, err := p2p.VerifyDeviceBinding(parsed.AuthorizedKey, challenge, signature, m.now()); err != nil {
		return nil, err
	}
	if strings.TrimSpace(deviceName) == "" {
		deviceName = "this device"
	}
	if strings.TrimSpace(deviceKind) == "" {
		deviceKind = "unknown"
	}
	device, grants, err := m.store.ActivateInvitedDevice(ctx, store.InvitedDeviceActivation{
		InvitationID: created.Invitation.ID, InvitationTokenHash: tokenHash[:],
		ChallengeID: challenge.ChallengeID, PeerID: peerID, ResponderPeerID: peerID,
		DisplayName: deviceName, DeviceKind: deviceKind,
		BindingVersion:        p2p.DeviceBindingTranscriptVersion,
		BindingTranscriptHash: transcriptHash, BindingSignature: signature,
		At: m.now(),
	})
	if err != nil {
		return nil, err
	}
	if grants == nil {
		grants = []store.WorkspaceGrant{}
	}
	return &p2p.CollaborationJoinResult{PrincipalID: owner.ID, Device: device, Grants: grants}, nil
}

func (m *Manager) Join(ctx context.Context, options p2p.CollaborationJoinOptions) (*p2p.CollaborationJoinResult, error) {
	if m == nil || m.transport == nil {
		return nil, p2p.ErrP2PNotBuiltIn
	}
	result, err := m.transport.Join(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := m.installWorkspaceMemberships(ctx, result.Workspaces); err != nil {
		return nil, fmt.Errorf("install shared workspace: %w", err)
	}
	return result, nil
}

func (m *Manager) installWorkspaceMemberships(ctx context.Context, accesses []p2p.CollaborationWorkspaceAccess) error {
	if len(accesses) == 0 {
		return nil
	}
	return m.store.Tx(ctx, func(base store.Store) error {
		tx, ok := base.(ManagementStore)
		if !ok {
			return fmt.Errorf("transaction store does not support collaboration memberships")
		}
		for _, access := range accesses {
			if access.ShareID == "" || access.HomePeerID == "" || access.RemoteWorkspaceID == "" || access.AccessEpoch < 1 {
				return p2p.ErrInvalidCollaborationInvite
			}
			workspaceName := strings.TrimSpace(access.WorkspaceName)
			if workspaceName == "" {
				workspaceName = "Shared workspace " + access.ShareID
			}
			localWorkspaceID := "shared:" + access.ShareID
			if existing, err := tx.GetWorkspaceMembership(ctx, access.ShareID); err == nil {
				if existing.HomePeerID != access.HomePeerID || existing.RemoteWorkspaceID != access.RemoteWorkspaceID {
					return fmt.Errorf("share identity changed: %w", store.ErrConflict)
				}
				localWorkspaceID = existing.LocalWorkspaceID
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if _, err := tx.GetWorkspace(ctx, localWorkspaceID); errors.Is(err, store.ErrNotFound) {
				if err := tx.CreateWorkspace(ctx, &store.Workspace{
					ID: localWorkspaceID, Name: workspaceName, RootPath: "",
					Tags: json.RawMessage(`["p2p-shared"]`), DefaultPolicy: "deny", Source: "p2p",
				}); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if err := tx.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
				ShareID: access.ShareID, HomePeerID: access.HomePeerID,
				RemoteWorkspaceID: access.RemoteWorkspaceID, LocalWorkspaceID: localWorkspaceID,
				WorkspaceName: workspaceName, Capabilities: access.Capabilities,
				AccessEpoch: access.AccessEpoch, Status: store.WorkspaceShareStatusActive,
				UpdatedAt: m.now(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
