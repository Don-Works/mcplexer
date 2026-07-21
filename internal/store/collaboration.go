package store

import (
	"encoding/json"
	"time"
)

// Principal kinds and lifecycle states. A principal is stable across key and
// device rotation; legacy_unverified is deliberately never authorization-
// bearing.
const (
	PrincipalKindPerson  = "person"
	PrincipalKindMachine = "machine"

	PrincipalStatusPending          = "pending"
	PrincipalStatusActive           = "active"
	PrincipalStatusLegacyUnverified = "legacy_unverified"
	PrincipalStatusRevoked          = "revoked"

	PrincipalKeyStatusPending = "pending"
	PrincipalKeyStatusActive  = "active"
	PrincipalKeyStatusRevoked = "revoked"

	PrincipalDeviceStatusActive           = "active"
	PrincipalDeviceStatusLegacyUnverified = "legacy_unverified"
	PrincipalDeviceStatusRevoked          = "revoked"
)

const (
	InvitationPurposeNewPrincipal = "new_principal"
	InvitationPurposeAddDevice    = "add_device"
	InvitationPurposeRotateKey    = "rotate_key"

	DeviceBindingVersionV1 = "MCPLEXER-DEVICE-BINDING-V1"
)

const (
	WorkspaceShareStatusActive  = "active"
	WorkspaceShareStatusRevoked = "revoked"

	TaskVisibilityPrivate    = "private"
	TaskVisibilityRestricted = "restricted"
	TaskVisibilityWorkspace  = "workspace"
)

// Workspace capabilities are exact values. Prefix and wildcard matching are
// forbidden by both service validation and migration 136 CHECK constraints.
const (
	CapabilityWorkspaceView  = "workspace.view"
	CapabilityTasksRead      = "tasks.read"
	CapabilityTasksCreate    = "tasks.create"
	CapabilityTasksPublish   = "tasks.publish"
	CapabilityTasksComment   = "tasks.comment"
	CapabilityTasksEdit      = "tasks.edit"
	CapabilityTasksAssign    = "tasks.assign"
	CapabilityTasksShare     = "tasks.share"
	CapabilityTasksDelete    = "tasks.delete"
	CapabilityEvidenceRead   = "evidence.read"
	CapabilityMeshRead       = "mesh.read"
	CapabilityMeshSend       = "mesh.send"
	CapabilityWorkerTrigger  = "worker.trigger"
	CapabilityWorkspaceAdmin = "workspace.admin"
)

var workspaceCapabilities = map[string]struct{}{
	CapabilityWorkspaceView: {}, CapabilityTasksRead: {}, CapabilityTasksCreate: {},
	CapabilityTasksPublish: {}, CapabilityTasksComment: {}, CapabilityTasksEdit: {},
	CapabilityTasksAssign: {}, CapabilityTasksShare: {}, CapabilityTasksDelete: {},
	CapabilityEvidenceRead: {}, CapabilityMeshRead: {}, CapabilityMeshSend: {},
	CapabilityWorkerTrigger: {}, CapabilityWorkspaceAdmin: {},
}

func ValidWorkspaceCapability(capability string) bool {
	_, ok := workspaceCapabilities[capability]
	return ok
}

func ValidTaskVisibility(visibility string) bool {
	switch visibility {
	case TaskVisibilityPrivate, TaskVisibilityRestricted, TaskVisibilityWorkspace:
		return true
	default:
		return false
	}
}

// Principal is a durable person or machine identity above individual libp2p
// devices. Exactly one local person may be IsLocalOwner.
type Principal struct {
	ID                     string     `json:"id"`
	Kind                   string     `json:"kind"`
	DisplayName            string     `json:"display_name"`
	Status                 string     `json:"status"`
	ControllingPrincipalID string     `json:"controlling_principal_id,omitempty"`
	IsLocalOwner           bool       `json:"is_local_owner"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	ActivatedAt            *time.Time `json:"activated_at,omitempty"`
	RevokedAt              *time.Time `json:"revoked_at,omitempty"`
	RevocationReason       string     `json:"revocation_reason,omitempty"`
}

// PrincipalIdentityKey stores public OpenSSH signing material only. Private
// material remains with the endpoint's SSH agent and never enters MCPlexer
// storage.
type PrincipalIdentityKey struct {
	ID                 string     `json:"id"`
	PrincipalID        string     `json:"principal_id"`
	CanonicalPublicKey string     `json:"canonical_public_key"`
	Fingerprint        string     `json:"fingerprint"`
	Algorithm          string     `json:"algorithm"`
	Status             string     `json:"status"`
	ReplacesKeyID      string     `json:"replaces_key_id,omitempty"`
	Comment            string     `json:"comment,omitempty"`
	AddedByPrincipalID string     `json:"added_by_principal_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	VerifiedAt         *time.Time `json:"verified_at,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
}

// PrincipalDevice binds one libp2p transport peer to one principal and the
// identity key that authorized the binding. Legacy rows deliberately have no
// key or proof and cannot resolve as active devices.
type PrincipalDevice struct {
	ID                    string     `json:"id"`
	PeerID                string     `json:"peer_id"`
	PrincipalID           string     `json:"principal_id"`
	IdentityKeyID         string     `json:"identity_key_id,omitempty"`
	DisplayName           string     `json:"display_name"`
	Kind                  string     `json:"kind"`
	Status                string     `json:"status"`
	BindingVersion        string     `json:"binding_version,omitempty"`
	BindingTranscriptHash string     `json:"binding_transcript_hash,omitempty"`
	BindingSignature      []byte     `json:"-"`
	CreatedAt             time.Time  `json:"created_at"`
	VerifiedAt            *time.Time `json:"verified_at,omitempty"`
	RevokedAt             *time.Time `json:"revoked_at,omitempty"`
	RevocationReason      string     `json:"revocation_reason,omitempty"`
}

type PrincipalInvitation struct {
	ID                   string     `json:"id"`
	TokenHash            []byte     `json:"-"`
	Purpose              string     `json:"purpose"`
	PrincipalID          string     `json:"principal_id"`
	IdentityKeyID        string     `json:"identity_key_id"`
	CreatedByPrincipalID string     `json:"created_by_principal_id"`
	CreatedAt            time.Time  `json:"created_at"`
	ExpiresAt            time.Time  `json:"expires_at"`
	ConsumedAt           *time.Time `json:"consumed_at,omitempty"`
	ConsumedByPeerID     string     `json:"consumed_by_peer_id,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
}

type InvitationWorkspaceGrant struct {
	InvitationID    string          `json:"invitation_id"`
	ShareID         string          `json:"share_id"`
	Capability      string          `json:"capability"`
	ConstraintsJSON json.RawMessage `json:"constraints,omitempty"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
}

type PrincipalIdentityChallenge struct {
	ID              string     `json:"id"`
	InvitationID    string     `json:"invitation_id"`
	InitiatorPeerID string     `json:"initiator_peer_id"`
	ResponderPeerID string     `json:"responder_peer_id"`
	NonceHash       []byte     `json:"-"`
	TranscriptHash  string     `json:"transcript_hash"`
	IssuedAt        time.Time  `json:"issued_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ConsumedAt      *time.Time `json:"consumed_at,omitempty"`
}

type InvitedDeviceActivation struct {
	InvitationID          string
	InvitationTokenHash   []byte
	ChallengeID           string
	PeerID                string
	ResponderPeerID       string
	DisplayName           string
	DeviceKind            string
	BindingVersion        string
	BindingTranscriptHash string
	BindingSignature      []byte
	At                    time.Time
}

type WorkspaceShare struct {
	ShareID          string     `json:"share_id"`
	LocalWorkspaceID string     `json:"local_workspace_id"`
	HomePeerID       string     `json:"home_peer_id"`
	OwnerPrincipalID string     `json:"owner_principal_id"`
	Status           string     `json:"status"`
	AccessEpoch      int64      `json:"access_epoch"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

type WorkspaceGrant struct {
	ID                   string          `json:"id"`
	ShareID              string          `json:"share_id"`
	PrincipalID          string          `json:"principal_id"`
	Capability           string          `json:"capability"`
	ConstraintsJSON      json.RawMessage `json:"constraints,omitempty"`
	CreatedByPrincipalID string          `json:"created_by_principal_id"`
	GrantedEpoch         int64           `json:"granted_epoch"`
	CreatedAt            time.Time       `json:"created_at"`
	ExpiresAt            *time.Time      `json:"expires_at,omitempty"`
	RevokedAt            *time.Time      `json:"revoked_at,omitempty"`
}

type WorkspaceGrantSet struct {
	ShareID              string
	PrincipalID          string
	Capabilities         []string
	ConstraintsJSON      json.RawMessage
	CreatedByPrincipalID string
	ExpiresAt            *time.Time
	At                   time.Time
}

type WorkspacePublicationPolicy struct {
	ShareID                  string    `json:"share_id"`
	DefaultVisibility        string    `json:"default_visibility"`
	AgentVisibilityCeiling   string    `json:"agent_visibility_ceiling"`
	WideningRequiresApproval bool      `json:"widening_requires_approval"`
	EgressProfile            string    `json:"egress_profile"`
	AllowRemoteEvidence      bool      `json:"allow_remote_evidence"`
	UpdatedByPrincipalID     string    `json:"updated_by_principal_id"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// WorkspaceMembership is the joining daemon's local, non-authoritative view
// of a workspace hosted by another peer. Cached capabilities are UX/routing
// hints only: the home re-authorizes every read or mutation against its live
// grant rows and access epoch.
type WorkspaceMembership struct {
	ShareID           string     `json:"share_id"`
	HomePeerID        string     `json:"home_peer_id"`
	RemoteWorkspaceID string     `json:"remote_workspace_id"`
	LocalWorkspaceID  string     `json:"local_workspace_id"`
	WorkspaceName     string     `json:"workspace_name"`
	Capabilities      []string   `json:"capabilities"`
	AccessEpoch       int64      `json:"access_epoch"`
	CursorHLC         string     `json:"cursor_hlc,omitempty"`
	Status            string     `json:"status"`
	JoinedAt          time.Time  `json:"joined_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
}

type CollaborationAuditEvent struct {
	ID               string          `json:"id"`
	ShareID          string          `json:"share_id,omitempty"`
	Event            string          `json:"event"`
	ActorPrincipalID string          `json:"actor_principal_id,omitempty"`
	ActorPeerID      string          `json:"actor_peer_id,omitempty"`
	SubjectKind      string          `json:"subject_kind"`
	SubjectID        string          `json:"subject_id"`
	DetailsJSON      json.RawMessage `json:"details,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

type TaskAccess struct {
	TaskID                         string     `json:"task_id"`
	WorkspaceID                    string     `json:"workspace_id"`
	ShareID                        string     `json:"share_id,omitempty"`
	OwnerPrincipalID               string     `json:"owner_principal_id,omitempty"`
	Visibility                     string     `json:"visibility"`
	VisibilityEpoch                int64      `json:"visibility_epoch"`
	VisibilityUpdatedByPrincipalID string     `json:"visibility_updated_by_principal_id,omitempty"`
	VisibilityUpdatedAt            *time.Time `json:"visibility_updated_at,omitempty"`
	AudiencePrincipalIDs           []string   `json:"audience_principal_ids,omitempty"`
	VisibilityEditable             bool       `json:"visibility_editable"`
}

type TaskVisibilityChange struct {
	TaskID               string
	Visibility           string
	AudiencePrincipalIDs []string
	ActorPrincipalID     string
	At                   time.Time
}

// TaskDisclosure is an immutable receipt for a sanitized projection. It never
// stores task content, only who received which projection hash under which
// access and visibility epochs.
type TaskDisclosure struct {
	ID                   string    `json:"id"`
	TaskID               string    `json:"task_id"`
	ShareID              string    `json:"share_id"`
	RecipientPrincipalID string    `json:"recipient_principal_id"`
	RecipientDeviceID    string    `json:"recipient_device_id"`
	RecipientPeerID      string    `json:"recipient_peer_id"`
	AccessEpoch          int64     `json:"access_epoch"`
	VisibilityEpoch      int64     `json:"visibility_epoch"`
	ProjectionSHA256     string    `json:"projection_sha256"`
	ProjectionBytes      int64     `json:"projection_bytes"`
	EgressProfile        string    `json:"egress_profile"`
	DisclosedAt          time.Time `json:"disclosed_at"`
}
