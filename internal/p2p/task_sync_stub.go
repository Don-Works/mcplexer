//go:build !p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// TaskSyncProtocol mirrored for slim-build code paths that key off
// the string (audit rendering, dashboards). Same literal as the
// p2p-build constant.
const TaskSyncProtocol = "/mcplexer/task-sync/1.0.0"

// TaskSyncProtocolVersion mirrored for slim-build compile parity.
const TaskSyncProtocolVersion = 1

// MaxTaskSyncWorkspacesPerHello mirrored for slim-build compile parity
// (the catch-up scheduler chunks its Hello under this cap). Same
// literal as the p2p-build constant.
const MaxTaskSyncWorkspacesPerHello = 256

// Wire-error sentinels — declared in stub mode too so callers can use
// errors.Is without a build-tag fence.
var (
	ErrTaskSyncNotPaired       = errors.New("p2p task-sync: peer not paired")
	ErrTaskSyncVersionMismatch = errors.New("p2p task-sync: protocol version mismatch")
	ErrTaskSyncFrameTooLarge   = errors.New("p2p task-sync: frame exceeds size cap")
	ErrTaskSyncSpoofedHello    = errors.New("p2p task-sync: hello peer_id mismatch")
	ErrTaskSyncStopped         = errors.New("p2p task-sync: service stopped")
	ErrTaskSyncWorkspaceDenied = errors.New("p2p task-sync: workspace scope denied")
	ErrTaskSyncHelloTooLarge   = errors.New("p2p task-sync: hello exceeds workspace cap")
)

// TaskSyncHelloWorkspace mirrors the p2p-build wire shape so wiring
// code compiles in both modes.
type TaskSyncHelloWorkspace struct {
	WorkspaceID      string `json:"workspace_id"`
	SinceHLC         string `json:"since_hlc"`
	LocalWorkspaceID string `json:"local_workspace_id,omitempty"`
}

// TaskSyncHello mirrors the p2p-build wire shape.
type TaskSyncHello struct {
	Type         string                   `json:"type"`
	PeerID       string                   `json:"peer_id"`
	ProtoVersion int                      `json:"proto_version"`
	Workspaces   []TaskSyncHelloWorkspace `json:"workspaces"`
	TS           time.Time                `json:"ts"`
}

// TaskSyncEvent mirrors the p2p-build wire shape.
type TaskSyncEvent struct {
	Type             string          `json:"type"`
	TaskID           string          `json:"task_id"`
	WorkspaceID      string          `json:"workspace_id"`
	HLC              string          `json:"hlc"`
	BySession        string          `json:"by_session,omitempty"`
	ByPeer           string          `json:"by_peer,omitempty"`
	FieldPatchesJSON json.RawMessage `json:"field_patches_json"`
}

type TaskSyncWorkspaceAccess struct {
	Type             string   `json:"type"`
	ShareID          string   `json:"share_id"`
	WorkspaceID      string   `json:"workspace_id"`
	LocalWorkspaceID string   `json:"-"`
	AccessEpoch      int64    `json:"access_epoch"`
	Capabilities     []string `json:"capabilities"`
	Status           string   `json:"status"`
}

// TaskSyncBye mirrors the p2p-build wire shape.
type TaskSyncBye struct {
	Type      string    `json:"type"`
	ServerHLC string    `json:"server_hlc"`
	TS        time.Time `json:"ts"`
}

// TaskSyncError mirrors the p2p-build wire shape.
type TaskSyncError struct {
	Type        string `json:"type"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// TaskSyncSource declared here for compile parity.
type TaskSyncSource interface {
	ListTasksSinceHLC(
		ctx context.Context, recipientPeerID, workspaceID, sinceHLC string, limit int,
	) ([]TaskSyncEvent, error)
	MaxHLCForWorkspace(ctx context.Context, workspaceID string) (string, error)
}

// TaskSyncSink declared here for compile parity.
type TaskSyncSink interface {
	ApplyRemoteEvent(ctx context.Context, fromPeerID string, evt TaskSyncEvent) error
}

type TaskSyncAccessSource interface {
	WorkspaceAccess(ctx context.Context, recipientPeerID, workspaceID string) (*TaskSyncWorkspaceAccess, error)
}

type TaskSyncAccessSink interface {
	ApplyWorkspaceAccess(ctx context.Context, fromPeerID string, access TaskSyncWorkspaceAccess) error
}

// TaskSyncScopeChecker declared here for compile parity.
type TaskSyncScopeChecker interface {
	HasTaskSyncScope(ctx context.Context, peerID, workspaceID string) (bool, error)
}

// TaskSyncAuditor declared here for compile parity.
type TaskSyncAuditor interface {
	RecordTaskSync(ctx context.Context, action, peerID, workspaceID, status, errMsg string)
}

// PeerPairChecker is declared in agent_directory_stub.go; reused here.

// TaskSyncService is the stub when the binary is built without -tags p2p.
type TaskSyncService struct{}

// NewTaskSyncService returns a non-nil stub.
func NewTaskSyncService(
	_ *Host, _ PeerPairChecker,
	_ TaskSyncSource, _ TaskSyncSink,
	_ TaskSyncScopeChecker, _ TaskSyncAuditor,
	_ *slog.Logger,
) *TaskSyncService {
	return &TaskSyncService{}
}

// ConnectToPeer returns ErrP2PNotBuiltIn in stub mode.
func (s *TaskSyncService) ConnectToPeer(
	_ context.Context, _ string, _ []TaskSyncHelloWorkspace,
) error {
	return ErrP2PNotBuiltIn
}

// Stop is a no-op in stub mode.
func (s *TaskSyncService) Stop() {}
