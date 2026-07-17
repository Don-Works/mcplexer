//go:build p2p

// Package p2p — /mcplexer/task-sync/1.0.0 protocol.
//
// Cross-peer task gossip: a request/response NDJSON stream that
// replays per-workspace task mutations using a per-row Hybrid Logical
// Clock (HLC) as the watermark. Unlike /mcplexer/task/1.0.0 (which
// models typed work delegation via offer + accept), task-sync is
// read-only state replication for paired peers that hold the
// `task_sync:<workspace>` scope.
//
// Wire shape (NDJSON, one frame per line, single bidirectional stream):
//
//	Client → Server: Hello { workspaces: [ {id, since_hlc} ] }
//	Server → Client: TaskEvent { task_id, hlc, by_session, by_peer,
//	                              workspace_id, field_patches_json }
//	                 ... repeats ...
//	Server → Client: Bye { server_hlc }
//
// Per-workspace scope gate runs on the server before any TaskEvent is
// streamed; a Hello entry naming a workspace the peer lacks scope for
// produces ONE error frame for that workspace and the server moves on
// to the next requested workspace. A peer that never holds any scope
// receives nothing but Bye.
//
// V1 limitation: field_patches_json carries the full task field set
// (title, status, priority, etc.) — not a literal patch diff. Future
// versions may shrink to per-field deltas; the framing accommodates
// that without a protocol version bump (the receiver already merges
// per-field).
//
// V1 does NOT gossip task_notes body content — events only — to keep
// payload size bounded. Notes ride the separate task_event mesh
// kind (Phase 2 emitter) and / or the offer protocol's payload phase
// for inbound bulk fetch. See gossip_apply.go header for rationale.
package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// TaskSyncProtocol is the libp2p protocol ID for the cross-peer task
// gossip stream.
const TaskSyncProtocol protocol.ID = "/mcplexer/task-sync/1.0.0"

// TaskSyncProtocolVersion is the wire-format version exchanged in the
// hello frame. Receivers reject unknown versions cleanly. Bump on any
// breaking change to TaskSyncEvent / TaskSyncHello / TaskSyncBye.
const TaskSyncProtocolVersion = 1

// MaxTaskSyncFrameBytes caps a single NDJSON line. 256 KiB is enough
// for a fat task (description + meta + tags) without giving a buggy or
// hostile sender room to OOM the receiver in one frame.
const MaxTaskSyncFrameBytes = 256 * 1024

// MaxTaskSyncWorkspacesPerHello caps how many workspaces a single
// Hello may request in one stream. Receivers reject hellos beyond this
// to bound the catch-up query cost.
const MaxTaskSyncWorkspacesPerHello = 256

// taskSyncBatchSize is how many tasks the server pulls from
// ListTasksSinceHLC per round. Bounded so a 10k-task workspace doesn't
// allocate a giant slice on the server.
const taskSyncBatchSize = 500

// taskSyncReadDeadline caps how long a single read on the sync stream
// may block. Re-armed per-frame so an idle long-lived stream isn't
// killed between sparse bursts.
const taskSyncReadDeadline = 90 * time.Second

// taskSyncWriteDeadline caps a single frame write.
const taskSyncWriteDeadline = 30 * time.Second

// Frame "type" discriminators. Stable wire constants — bumping requires
// a protocol-version bump too.
const (
	taskSyncFrameHello  = "hello"
	taskSyncFrameAccess = "workspace_access"
	taskSyncFrameEvent  = "task_event"
	taskSyncFrameBye    = "bye"
	taskSyncFrameError  = "error"
)

// TaskSyncHelloWorkspace names one workspace + watermark the client
// wants to catch up on. Empty SinceHLC = "send me every row".
type TaskSyncHelloWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	SinceHLC    string `json:"since_hlc"`
	// LocalWorkspaceID is receiver-only routing metadata. The server never
	// trusts or uses it; the client maps the authenticated home workspace
	// into its local mirror immediately before applying each event.
	LocalWorkspaceID string `json:"local_workspace_id,omitempty"`
}

// TaskSyncHello is the first frame on the stream, sent by the dialing
// (client) side. PeerID is checked against stream.Conn().RemotePeer()
// — a mismatch closes the stream as anti-spoof. ProtoVersion lets
// future versions negotiate without a new protocol ID.
type TaskSyncHello struct {
	Type         string                   `json:"type"` // taskSyncFrameHello
	PeerID       string                   `json:"peer_id"`
	ProtoVersion int                      `json:"proto_version"`
	Workspaces   []TaskSyncHelloWorkspace `json:"workspaces"`
	TS           time.Time                `json:"ts"`
}

// TaskSyncEvent is one task mutation replayed from the server.
// FieldPatchesJSON carries the post-mutation field set as JSON (see
// header note on v1 not being a true patch diff). Receivers MUST apply
// these via the LWW + tiebreak rules in gossip_apply.go.
type TaskSyncEvent struct {
	Type             string          `json:"type"` // taskSyncFrameEvent
	TaskID           string          `json:"task_id"`
	WorkspaceID      string          `json:"workspace_id"`
	HLC              string          `json:"hlc"`
	BySession        string          `json:"by_session,omitempty"`
	ByPeer           string          `json:"by_peer,omitempty"`
	FieldPatchesJSON json.RawMessage `json:"field_patches_json"`
}

// TaskSyncWorkspaceAccess is the home-authoritative membership receipt sent
// before task data for each requested workspace. It keeps a joined daemon's
// cached capability set and access epoch fresh after an owner edits the
// permissions matrix. LocalWorkspaceID is receiver-only routing metadata and
// is never accepted from the wire.
type TaskSyncWorkspaceAccess struct {
	Type             string   `json:"type"`
	ShareID          string   `json:"share_id"`
	WorkspaceID      string   `json:"workspace_id"`
	LocalWorkspaceID string   `json:"-"`
	AccessEpoch      int64    `json:"access_epoch"`
	Capabilities     []string `json:"capabilities"`
	Status           string   `json:"status"`
}

// TaskSyncBye is the server's signal that the catch-up is complete.
// ServerHLC is the highest HLC the server holds at the moment of
// sending — receivers MAY persist it as their new watermark when no
// events have been streamed (i.e. they're already caught up).
type TaskSyncBye struct {
	Type      string    `json:"type"` // taskSyncFrameBye
	ServerHLC string    `json:"server_hlc"`
	TS        time.Time `json:"ts"`
}

// TaskSyncError is the failure shape. Stream closes after when the
// error is fatal; for per-workspace denials the server continues with
// the next workspace.
type TaskSyncError struct {
	Type        string `json:"type"` // taskSyncFrameError
	Code        string `json:"code"`
	Message     string `json:"message"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// TaskSyncSource is the read-side hook the server uses to pull events
// to stream. The implementation lives in the wiring layer so the p2p
// package stays free of a store/tasks dependency.
type TaskSyncSource interface {
	// ListTasksSinceHLC returns up to limit rows in workspace whose
	// hlc_at > sinceHLC, ascending. The wiring adapter is a thin
	// pass-through to store.ListTasksSinceHLC.
	ListTasksSinceHLC(
		ctx context.Context, recipientPeerID, workspaceID, sinceHLC string, limit int,
	) ([]TaskSyncEvent, error)
	// MaxHLCForWorkspace returns the workspace's current high-water
	// HLC — sent in the Bye frame so a caught-up receiver can persist
	// the watermark even when zero events were streamed.
	MaxHLCForWorkspace(ctx context.Context, workspaceID string) (string, error)
}

// TaskSyncSink is the receive-side hook the client uses to apply
// remote events. Returns an error to signal the apply failed; the
// stream remains open (the next event tries fresh).
type TaskSyncSink interface {
	ApplyRemoteEvent(ctx context.Context, fromPeerID string, evt TaskSyncEvent) error
}

// TaskSyncAccessSource and TaskSyncAccessSink are optional extensions. Keeping
// them separate preserves wire compatibility for older/test adapters while a
// collaboration-aware daemon refreshes membership state on every sync.
type TaskSyncAccessSource interface {
	WorkspaceAccess(ctx context.Context, recipientPeerID, workspaceID string) (*TaskSyncWorkspaceAccess, error)
}

type TaskSyncAccessSink interface {
	ApplyWorkspaceAccess(ctx context.Context, fromPeerID string, access TaskSyncWorkspaceAccess) error
}

// TaskSyncScopeChecker gates the server-side stream by per-workspace
// scope. The wiring adapter calls store.HasPeerScope("task_sync:<ws>").
// nil-safe in tests (permissive).
type TaskSyncScopeChecker interface {
	HasTaskSyncScope(ctx context.Context, peerID, workspaceID string) (bool, error)
}

// TaskSyncAuditor emits one record per protocol transition so the
// audit dashboard can render cross-peer state-replication activity.
// nil-safe.
type TaskSyncAuditor interface {
	RecordTaskSync(ctx context.Context, action, peerID, workspaceID, status, errMsg string)
}

// Wire errors callers + tests assert on. The wire never differentiates
// "denied" from "not paired" so a peer can't probe pairing state.
var (
	// ErrTaskSyncNotPaired is returned by ConnectToPeer + the inbound
	// handler when the remote is not in p2p_peers.
	ErrTaskSyncNotPaired = errors.New("p2p task-sync: peer not paired")

	// ErrTaskSyncVersionMismatch is returned when the remote's hello
	// proto_version is unknown to this build.
	ErrTaskSyncVersionMismatch = errors.New("p2p task-sync: protocol version mismatch")

	// ErrTaskSyncFrameTooLarge wraps an oversize frame on the wire.
	ErrTaskSyncFrameTooLarge = errors.New("p2p task-sync: frame exceeds size cap")

	// ErrTaskSyncSpoofedHello is returned when the hello frame's
	// claimed peer_id doesn't match the libp2p stream's RemotePeer().
	ErrTaskSyncSpoofedHello = errors.New("p2p task-sync: hello peer_id mismatch")

	// ErrTaskSyncStopped is returned by ConnectToPeer after Stop has
	// been called.
	ErrTaskSyncStopped = errors.New("p2p task-sync: service stopped")

	// ErrTaskSyncWorkspaceDenied is returned (and surfaced in an error
	// frame) when a hello entry names a workspace the peer lacks the
	// task_sync:<workspace> scope for.
	ErrTaskSyncWorkspaceDenied = errors.New("p2p task-sync: workspace scope denied")

	// ErrTaskSyncHelloTooLarge is returned when a hello carries more
	// than MaxTaskSyncWorkspacesPerHello entries.
	ErrTaskSyncHelloTooLarge = errors.New("p2p task-sync: hello exceeds workspace cap")
)

// TaskSyncService is the libp2p stream handler + client. One per Host.
// Lifecycle:
//
//   - NewTaskSyncService registers the inbound handler.
//   - ConnectToPeer dials a paired peer with a Hello carrying the
//     local watermark per workspace, reads streamed events into the
//     sink, then receives the Bye frame and closes.
//   - Stop cancels future outbound dials.
type TaskSyncService struct {
	host         *Host
	lookup       PeerPairChecker
	source       TaskSyncSource
	sink         TaskSyncSink
	scopeChecker TaskSyncScopeChecker
	auditor      TaskSyncAuditor
	logger       *slog.Logger
	selfID       string

	mu      sync.Mutex
	stopped bool
}

// NewTaskSyncService wires the inbound stream handler onto host and
// returns a service ready for ConnectToPeer. host must be non-nil. The
// source / sink / lookup may be nil only if the corresponding direction
// will never be exercised (server-only or client-only setups). auditor
// + logger may be nil.
func NewTaskSyncService(
	host *Host,
	lookup PeerPairChecker,
	source TaskSyncSource,
	sink TaskSyncSink,
	scopeChecker TaskSyncScopeChecker,
	auditor TaskSyncAuditor,
	logger *slog.Logger,
) *TaskSyncService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &TaskSyncService{
		host:         host,
		lookup:       lookup,
		source:       source,
		sink:         sink,
		scopeChecker: scopeChecker,
		auditor:      auditor,
		logger:       logger,
	}
	if host != nil {
		s.selfID = host.PeerID()
		host.Inner().SetStreamHandler(TaskSyncProtocol, s.handleStream)
	}
	return s
}

// Stop marks the service shut down. In-flight outbound calls observe
// this on their next state check; inbound streams currently being
// served run to completion. Idempotent.
func (s *TaskSyncService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
}

// ConnectToPeer dials peerID, sends a Hello with the supplied
// watermarks, streams events through the sink, and reads the Bye. Each
// invocation opens its own short-lived stream — this is request /
// response style, not a long-lived gossip channel like agents.
//
// Watermarks carry the receiver-side max(hlc_at) per workspace. The
// caller assembles them by querying its own store before calling.
func (s *TaskSyncService) ConnectToPeer(
	ctx context.Context, peerID string, workspaces []TaskSyncHelloWorkspace,
) error {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return ErrTaskSyncStopped
	}
	if len(workspaces) == 0 {
		return errors.New("task-sync: workspaces required")
	}
	if len(workspaces) > MaxTaskSyncWorkspacesPerHello {
		return fmt.Errorf("%w: %d > %d",
			ErrTaskSyncHelloTooLarge, len(workspaces), MaxTaskSyncWorkspacesPerHello)
	}
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("task-sync: decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, TaskSyncProtocol)
	if err != nil {
		return fmt.Errorf("task-sync: open stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	hello := TaskSyncHello{
		Type:         taskSyncFrameHello,
		PeerID:       s.selfID,
		ProtoVersion: TaskSyncProtocolVersion,
		Workspaces:   workspaces,
		TS:           time.Now().UTC(),
	}
	if err := s.writeFrame(stream, hello); err != nil {
		s.recordAudit(ctx, "dial_hello", peerID, "", "error", err.Error())
		return err
	}
	s.recordAudit(ctx, "dial_hello", peerID, "", "ok", "")
	// Bind the inbound apply path to exactly the workspaces this client
	// requested. A serving peer can stream a TaskSyncEvent for ANY
	// workspace_id it likes; without this set the receiver would apply
	// it blindly and a hostile/buggy peer could inject or mutate task
	// rows in workspaces it was never granted. See readEventStream.
	allowed := make(map[string]string, len(workspaces))
	for _, ws := range workspaces {
		if ws.WorkspaceID != "" {
			localWorkspaceID := ws.LocalWorkspaceID
			if localWorkspaceID == "" {
				localWorkspaceID = ws.WorkspaceID
			}
			allowed[ws.WorkspaceID] = localWorkspaceID
		}
	}
	return s.readEventStream(ctx, stream, peerID, allowed)
}

// readEventStream pumps server frames into the sink until a Bye or
// error frame, or stream EOF. Returns nil on clean Bye, an error on
// any wire / sink failure (logged + audited).
func (s *TaskSyncService) readEventStream(
	ctx context.Context, stream network.Stream, peerID string,
	allowedWorkspaces map[string]string,
) error {
	br := bufio.NewReaderSize(stream, MaxTaskSyncFrameBytes)
	for {
		_ = stream.SetReadDeadline(time.Now().Add(taskSyncReadDeadline))
		line, err := readSyncLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			s.recordAudit(ctx, "read_event", peerID, "", "error", err.Error())
			return err
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			s.recordAudit(ctx, "read_event", peerID, "", "error", err.Error())
			return fmt.Errorf("task-sync: parse frame head: %w", err)
		}
		switch head.Type {
		case taskSyncFrameAccess:
			var access TaskSyncWorkspaceAccess
			if err := json.Unmarshal(line, &access); err != nil {
				return fmt.Errorf("task-sync: parse workspace access: %w", err)
			}
			localWorkspaceID, ok := allowedWorkspaces[access.WorkspaceID]
			if !ok || access.ShareID == "" || access.AccessEpoch < 1 {
				s.recordAudit(ctx, "reject_unbound_access", peerID,
					access.WorkspaceID, "denied", "workspace not requested in hello")
				continue
			}
			access.LocalWorkspaceID = localWorkspaceID
			if sink, ok := s.sink.(TaskSyncAccessSink); ok {
				if applyErr := sink.ApplyWorkspaceAccess(ctx, peerID, access); applyErr != nil {
					s.recordAudit(ctx, "apply_access", peerID, access.WorkspaceID, "error", applyErr.Error())
					return applyErr
				}
			}
			s.recordAudit(ctx, "apply_access", peerID, access.WorkspaceID, "ok", "")
		case taskSyncFrameEvent:
			var evt TaskSyncEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				s.recordAudit(ctx, "read_event", peerID, "", "error", err.Error())
				return fmt.Errorf("task-sync: parse event: %w", err)
			}
			// SECURITY: only apply events for workspaces this client
			// explicitly requested in its Hello. A serving peer that
			// streams an event for an unrequested (or empty) workspace is
			// buggy or hostile; drop it WITHOUT applying so it can't
			// inject/mutate local task rows in a workspace we never bound
			// to. Non-fatal: keep reading so legitimate events still flow.
			localWorkspaceID, ok := allowedWorkspaces[evt.WorkspaceID]
			if !ok {
				s.logger.Warn("task-sync: dropping event for unbound workspace",
					"peer", peerID, "workspace", evt.WorkspaceID, "task", evt.TaskID)
				s.recordAudit(ctx, "reject_unbound", peerID,
					evt.WorkspaceID, "denied", "workspace not requested in hello")
				continue
			}
			evt.WorkspaceID = localWorkspaceID
			if s.sink != nil {
				if applyErr := s.sink.ApplyRemoteEvent(ctx, peerID, evt); applyErr != nil {
					s.logger.Warn("task-sync: sink apply failed",
						"peer", peerID, "task", evt.TaskID, "err", applyErr)
					s.recordAudit(ctx, "apply_event", peerID,
						evt.WorkspaceID, "error", applyErr.Error())
					// Apply failure does not close the stream — the next
					// event tries fresh. The sink owns its own retry policy
					// if any.
					continue
				}
			}
			s.recordAudit(ctx, "apply_event", peerID, evt.WorkspaceID, "ok", "")
		case taskSyncFrameBye:
			s.recordAudit(ctx, "bye_recv", peerID, "", "ok", "")
			return nil
		case taskSyncFrameError:
			var e TaskSyncError
			if err := json.Unmarshal(line, &e); err != nil {
				return fmt.Errorf("task-sync: parse error frame: %w", err)
			}
			s.recordAudit(ctx, "error_recv", peerID, e.WorkspaceID, "error",
				fmt.Sprintf("%s: %s", e.Code, e.Message))
			// Per-workspace denial: log + keep reading (server moves to
			// the next workspace). Other codes are fatal.
			if e.Code == "workspace_denied" {
				continue
			}
			return fmt.Errorf("task-sync: remote error %s: %s", e.Code, e.Message)
		default:
			// Forward-compat: unknown frame type — log + skip.
			s.logger.Debug("task-sync: unknown frame type",
				"peer", peerID, "type", head.Type)
		}
	}
}

// handleStream is the libp2p inbound entry point — pairing + hello
// validation + per-workspace scope gates + event replay.
func (s *TaskSyncService) handleStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()
	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	if err := s.assertPeerPaired(ctx, remote); err != nil {
		s.logger.Info("task-sync: stream rejected", "peer", remote, "err", err)
		s.recordAudit(ctx, "stream_accept", remote, "", "denied", err.Error())
		_ = s.writeFrame(stream, TaskSyncError{
			Type: taskSyncFrameError, Code: "denied", Message: err.Error(),
		})
		return
	}
	hello, err := s.readHello(stream, remote)
	if err != nil {
		s.recordAudit(ctx, "stream_accept", remote, "", "error", err.Error())
		_ = s.writeFrame(stream, TaskSyncError{
			Type: taskSyncFrameError, Code: "bad_hello", Message: err.Error(),
		})
		return
	}
	s.recordAudit(ctx, "stream_accept", remote, "", "ok", "")
	s.serveCatchup(ctx, stream, remote, hello)
}

// readHello reads the first frame and validates type + version +
// peer_id anti-spoof + workspace cap.
func (s *TaskSyncService) readHello(stream network.Stream, remote string) (TaskSyncHello, error) {
	_ = stream.SetReadDeadline(time.Now().Add(taskSyncReadDeadline))
	br := bufio.NewReaderSize(stream, MaxTaskSyncFrameBytes)
	line, err := readSyncLine(br)
	if err != nil {
		return TaskSyncHello{}, fmt.Errorf("read hello: %w", err)
	}
	var hello TaskSyncHello
	if err := json.Unmarshal(line, &hello); err != nil {
		return TaskSyncHello{}, fmt.Errorf("decode hello: %w", err)
	}
	if hello.Type != taskSyncFrameHello {
		return TaskSyncHello{}, fmt.Errorf("expected hello, got %q", hello.Type)
	}
	if hello.PeerID != remote {
		return TaskSyncHello{}, ErrTaskSyncSpoofedHello
	}
	if hello.ProtoVersion != TaskSyncProtocolVersion {
		return TaskSyncHello{}, fmt.Errorf("%w: got %d, want %d",
			ErrTaskSyncVersionMismatch, hello.ProtoVersion, TaskSyncProtocolVersion)
	}
	if len(hello.Workspaces) > MaxTaskSyncWorkspacesPerHello {
		return TaskSyncHello{}, fmt.Errorf("%w: %d > %d",
			ErrTaskSyncHelloTooLarge, len(hello.Workspaces),
			MaxTaskSyncWorkspacesPerHello)
	}
	return hello, nil
}

// serveCatchup is the server-side replay loop. Per Hello workspace:
//
//  1. Check the peer holds the `task_sync:<workspace>` scope (or skip
//     with a single error frame and proceed to the next workspace).
//  2. Loop ListTasksSinceHLC(workspaceID, watermark, batch) until <
//     batch rows return; write each row as a TaskSyncEvent.
//  3. Move on to the next workspace.
//
// After every workspace is drained, write a Bye carrying the highest
// HLC observed across the served workspaces (best-effort) so a
// caught-up receiver can persist a watermark even when no events were
// streamed.
//
// Mid-stream scope revocation: the scope check is run BEFORE replay.
// If the peer's scope is revoked while replay is in flight, the next
// streamWorkspace call still proceeds (this catch-up cycle finishes)
// but the NEXT ConnectToPeer cycle catches the revocation. Hardening
// against in-flight revocation would require a per-event scope check
// — judged over-budget for v1; the audit trail still shows the leak.
func (s *TaskSyncService) serveCatchup(
	ctx context.Context, stream network.Stream, remote string, hello TaskSyncHello,
) {
	highest := ""
	for _, ws := range hello.Workspaces {
		if ws.WorkspaceID == "" {
			continue
		}
		if accessSource, ok := s.source.(TaskSyncAccessSource); ok {
			access, err := accessSource.WorkspaceAccess(ctx, remote, ws.WorkspaceID)
			if err != nil || access == nil {
				s.recordAudit(ctx, "workspace_denied", remote, ws.WorkspaceID, "denied", ErrTaskSyncWorkspaceDenied.Error())
				if writeErr := s.writeFrame(stream, TaskSyncError{
					Type: taskSyncFrameError, Code: "workspace_denied",
					Message: ErrTaskSyncWorkspaceDenied.Error(), WorkspaceID: ws.WorkspaceID,
				}); writeErr != nil {
					return
				}
				continue
			}
			access.Type = taskSyncFrameAccess
			access.WorkspaceID = ws.WorkspaceID
			if err := s.writeFrame(stream, access); err != nil {
				return
			}
			if access.Status != "active" {
				continue
			}
		}
		if err := s.checkWorkspaceScope(ctx, remote, ws.WorkspaceID); err != nil {
			s.recordAudit(ctx, "workspace_denied", remote, ws.WorkspaceID, "denied", err.Error())
			if writeErr := s.writeFrame(stream, TaskSyncError{
				Type:        taskSyncFrameError,
				Code:        "workspace_denied",
				Message:     ErrTaskSyncWorkspaceDenied.Error(),
				WorkspaceID: ws.WorkspaceID,
			}); writeErr != nil {
				// Receiver closed the stream during the deny frame — bail
				// before we try to keep streaming events.
				return
			}
			continue
		}
		streamHigh, err := s.streamWorkspace(ctx, stream, remote, ws)
		if err != nil {
			s.recordAudit(ctx, "stream_workspace", remote, ws.WorkspaceID, "error", err.Error())
			return
		}
		if streamHigh > highest {
			highest = streamHigh
		}
		if s.source != nil {
			if wsMax, err := s.source.MaxHLCForWorkspace(ctx, ws.WorkspaceID); err == nil && wsMax > highest {
				highest = wsMax
			}
		}
	}
	bye := TaskSyncBye{Type: taskSyncFrameBye, ServerHLC: highest, TS: time.Now().UTC()}
	if err := s.writeFrame(stream, bye); err != nil {
		s.logger.Debug("task-sync: bye write", "peer", remote, "err", err)
		return
	}
	s.recordAudit(ctx, "bye_send", remote, "", "ok", "")
}

// streamWorkspace pages through ListTasksSinceHLC until the result is
// smaller than the batch (i.e. caught up), writing one TaskSyncEvent
// per row. Returns the highest HLC observed in this workspace.
func (s *TaskSyncService) streamWorkspace(
	ctx context.Context, stream network.Stream, remote string, ws TaskSyncHelloWorkspace,
) (string, error) {
	if s.source == nil {
		return "", nil
	}
	watermark := ws.SinceHLC
	highest := watermark
	for {
		evts, err := s.source.ListTasksSinceHLC(ctx, remote, ws.WorkspaceID, watermark, taskSyncBatchSize)
		if err != nil {
			return highest, fmt.Errorf("list tasks since: %w", err)
		}
		if len(evts) == 0 {
			return highest, nil
		}
		for i := range evts {
			evts[i].Type = taskSyncFrameEvent
			evts[i].WorkspaceID = ws.WorkspaceID
			if err := s.writeFrame(stream, evts[i]); err != nil {
				return highest, fmt.Errorf("write event: %w", err)
			}
			if evts[i].HLC > highest {
				highest = evts[i].HLC
			}
		}
		s.recordAudit(ctx, "stream_batch", remote, ws.WorkspaceID, "ok", "")
		// Advance watermark + decide whether another batch is needed.
		watermark = evts[len(evts)-1].HLC
		if len(evts) < taskSyncBatchSize {
			return highest, nil
		}
	}
}

// checkWorkspaceScope returns nil iff peerID holds the
// task_sync:<workspaceID> scope (literal or wildcard "task_sync:*").
// nil scopeChecker is permissive (test path).
func (s *TaskSyncService) checkWorkspaceScope(ctx context.Context, peerID, workspaceID string) error {
	if s.scopeChecker == nil {
		return nil
	}
	ok, err := s.scopeChecker.HasTaskSyncScope(ctx, peerID, workspaceID)
	if err != nil {
		return fmt.Errorf("%w: scope check: %v", ErrTaskSyncWorkspaceDenied, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskSyncWorkspaceDenied, workspaceID)
	}
	return nil
}

// assertPeerPaired returns nil iff peerID is in p2p_peers. nil lookup
// is permissive (test path).
func (s *TaskSyncService) assertPeerPaired(ctx context.Context, peerID string) error {
	if s.lookup == nil {
		return nil
	}
	ok, err := s.lookup.IsPaired(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s (lookup: %v)", ErrTaskSyncNotPaired, peerID, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskSyncNotPaired, peerID)
	}
	return nil
}

// writeFrame encodes v as JSON, appends '\n', writes to stream. Caps
// at MaxTaskSyncFrameBytes.
func (s *TaskSyncService) writeFrame(stream network.Stream, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(data)+1 > MaxTaskSyncFrameBytes {
		return fmt.Errorf("%w: %d bytes", ErrTaskSyncFrameTooLarge, len(data)+1)
	}
	data = append(data, '\n')
	_ = stream.SetWriteDeadline(time.Now().Add(taskSyncWriteDeadline))
	if _, err := stream.Write(data); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// readSyncLine reads exactly one '\n'-terminated frame, enforcing the
// byte cap. Returns the line without the trailing newline.
//
// It uses ReadSlice on the cap-sized reader so an oversize frame surfaces as
// bufio.ErrBufferFull immediately, rather than ReadBytes buffering the whole
// (potentially multi-GB) newline-free line into memory before the length
// check can fire.
func readSyncLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("%w: > %d", ErrTaskSyncFrameTooLarge, MaxTaskSyncFrameBytes)
	}
	if len(line) > MaxTaskSyncFrameBytes {
		return nil, fmt.Errorf("%w: %d > %d",
			ErrTaskSyncFrameTooLarge, len(line), MaxTaskSyncFrameBytes)
	}
	if err != nil && len(line) == 0 {
		return nil, err
	}
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(line) == 0 {
		return nil, io.EOF
	}
	// ReadSlice returns a view into the shared buffer that the next read
	// overwrites; copy so callers can retain the frame safely.
	out := make([]byte, len(line))
	copy(out, line)
	return out, nil
}

// recordAudit forwards to the optional auditor.
func (s *TaskSyncService) recordAudit(ctx context.Context, action, peerID, workspaceID, status, errMsg string) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordTaskSync(ctx, action, peerID, workspaceID, status, errMsg)
}
