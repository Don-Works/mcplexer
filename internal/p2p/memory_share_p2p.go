//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/don-works/mcplexer/internal/scopes"
)

// MemoryShareProtocol is the libp2p protocol id for cross-peer memory
// transfer. Mirror of SkillShareProtocol: one stream per offer/request,
// JSON envelopes only (no binary frames — memory content is small
// markdown, ≤ MaxMemoryBytes).
const MemoryShareProtocol protocol.ID = "/mcplexer/memory/1.0.0"

// MaxMemoryBytes caps a single memory's content length on the wire.
// Higher than typical notes (kB) but bounded so a misbehaving peer
// can't OOM the receiver with a single payload.
const MaxMemoryBytes int64 = 1 * 1024 * 1024

// memoryShareReadDeadline caps how long a single stream read can block.
const memoryShareReadDeadline = 30 * time.Second

// memoryShareScopeName is the boolean scope on a paired peer that must
// be granted before memory requests are honored. Mirrors the skill-share
// scope-grant model.
const memoryShareScopeName = "mesh.memory_request"

// MemoryShareErrors are surfaced over the wire as stable codes the
// gateway can map to user-friendly messages.
var (
	// ErrMemoryNotFound is returned when an OFFER lookup or REQUEST
	// lookup misses on the offering peer.
	ErrMemoryNotFound = errors.New("p2p: memory not found on peer")
	// ErrMemoryTooLarge wraps a size cap violation seen on the wire.
	ErrMemoryTooLarge = errors.New("p2p: memory exceeds size cap")
	// ErrMemoryShareDenied is the generic "request refused" code —
	// the wire never reveals whether the cause is pairing or scope.
	ErrMemoryShareDenied = errors.New("p2p: memory share denied")
)

// EntityLink is the wire shape for a "this memory is about X" link
// (migration 076). Receivers re-link via store.LinkMemoryEntity after
// import. Peer-local kinds (see IsEntityKindPeerLocal) are stripped on
// the SEND side so we never fabricate e.g. a "place:/Users/example" link
// on a different machine.
type EntityLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

// MemoryOffer is the thin descriptor a peer sends BEFORE the receiver
// pulls the full content. Preview is the first ~512 chars; metadata +
// embed_model help the receiver decide whether to import + whether to
// keep the embedding (mismatched embed_model → drop the vector).
// EntitiesPreview lists up to 5 of the memory's entity links so the
// receiver can render "this memory is about ⟨task T, person Alice⟩"
// before deciding accept/decline. The full set rides on MemoryPayload.
type MemoryOffer struct {
	Type            string          `json:"type"` // always "offer"
	RemoteID        string          `json:"remote_id"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"` // fact|note
	Description     string          `json:"description,omitempty"`
	Preview         string          `json:"preview,omitempty"`
	TagsJSON        json.RawMessage `json:"tags,omitempty"`
	MetadataJSON    json.RawMessage `json:"metadata,omitempty"`
	EmbedModel      string          `json:"embed_model,omitempty"`
	SizeBytes       int64           `json:"size_bytes"`
	EntitiesPreview []EntityLink    `json:"entities_preview,omitempty"`
}

// peerLocalEntityKinds is the set of entity kinds whose IDs only make
// sense on the originating peer (paths, locally-minted ULIDs, etc.).
// Outgoing payloads + offers must strip these so the receiver doesn't
// import a meaningless link. Kept as a package-private map so the rule
// is a code patch, not a wire-protocol change.
var peerLocalEntityKinds = map[string]struct{}{
	"place": {}, // absolute paths are host-specific
	"event": {}, // local ULIDs aren't globally meaningful
}

// IsEntityKindPeerLocal reports whether links of this kind should be
// stripped before crossing the mesh. Case-insensitive. Returns false
// for any kind not explicitly listed — globally identifiable is the
// safe default.
func IsEntityKindPeerLocal(kind string) bool {
	_, ok := peerLocalEntityKinds[normalizeEntityKindForLocal(kind)]
	return ok
}

// FilterEntitiesForMesh drops any link whose kind is peer-local. Returns
// a fresh slice — callers can compare lengths to learn whether anything
// was stripped (useful for audit / debugging).
func FilterEntitiesForMesh(in []EntityLink) []EntityLink {
	if len(in) == 0 {
		return nil
	}
	out := make([]EntityLink, 0, len(in))
	for _, e := range in {
		if IsEntityKindPeerLocal(e.Kind) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func normalizeEntityKindForLocal(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// MemoryRequest asks the offering peer for the full content of one
// memory. The reply is MemoryPayload (or memoryShareError).
type MemoryRequest struct {
	Type     string `json:"type"` // always "request"
	RemoteID string `json:"remote_id"`
}

// MemoryPayload is the full memory transferred from offering peer to
// requesting peer. Tags / metadata are preserved verbatim; embedding is
// optional (peer might not have computed one). Entities carries the
// "what is this memory about" link set (migration 076), already filtered
// through FilterEntitiesForMesh on the send side.
type MemoryPayload struct {
	Type         string          `json:"type"` // always "memory"
	RemoteID     string          `json:"remote_id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Content      string          `json:"content"`
	TagsJSON     json.RawMessage `json:"tags,omitempty"`
	MetadataJSON json.RawMessage `json:"metadata,omitempty"`
	EmbedModel   string          `json:"embed_model,omitempty"`
	EmbedVersion int             `json:"embed_version,omitempty"`
	Embedding    []float32       `json:"embedding,omitempty"`
	Entities     []EntityLink    `json:"entities,omitempty"`
	// RemoteWorkspaceID is the sender's workspace id for this memory (empty
	// for a global memory). The receiver resolves it to a LOCAL workspace
	// via workspace_peer_bindings so a memory written in a linked workspace
	// lands in the bound local workspace rather than global. Additive +
	// omitempty: older peers that don't send it (or global memories) fall
	// back to the prior global-write behaviour. See linked-workspaces.
	RemoteWorkspaceID string `json:"remote_workspace_id,omitempty"`
}

// memoryShareError is the on-the-wire failure shape. Stream closes after.
//
// Two failure flavours share the envelope, distinguished by Code:
//
//   - "bad_request"  — request envelope was malformed (type field missing,
//     JSON decode failed, etc.). Carries a Message because the sender
//     needs to debug their own marshalling — there's no leak risk on a
//     "you sent me garbage" reply.
//   - "denied"       — every other failure mode (not paired, scope missing,
//     memory not found, memory in an un-granted workspace, payload too
//     large). MUST be a CONSTANT shape with no resource-specific tokens
//     so the requester can't side-channel-infer the un-granted memory's
//     existence or content. The Denial body is the typed `scopes.Denial`
//     vocabulary; Scope/Message are deliberately omitted on the wire
//     even though the local audit row captures them.
//
// Security claim: the bytes a Tier-2/3 peer receives on a denied request
// MUST be identical regardless of whether the target memory doesn't
// exist, exists in a granted workspace, or exists in an un-granted
// workspace. The audit ROW (local-only) carries the full story; the
// wire reply does not.
type memoryShareError struct {
	Type    string         `json:"type"`              // "error"
	Code    string         `json:"code"`              // "denied" | "bad_request"
	Message string         `json:"message,omitempty"` // populated ONLY for "bad_request"
	Denial  *scopes.Denial `json:"denial,omitempty"`  // populated for "denied"
}

// newDenyError builds the constant-shape deny envelope for the cross-peer
// memory protocol. peerID is the libp2p remote peer (caller); included so
// the requester can confirm the daemon talked to the right side, but no
// scope name / memory id / content fragment is ever returned.
//
// The four failure modes (not paired, scope missing, memory not found,
// memory in un-granted workspace) all funnel through this single shape —
// callers MUST NOT add a per-cause variant or the side-channel reopens.
func newDenyError(peerID string) memoryShareError {
	d := scopes.New(scopes.DenialNoScope, "", peerID)
	return memoryShareError{
		Type:   "error",
		Code:   "denied",
		Denial: &d,
	}
}

// MemoryProvider is the offering-side hook: given the requesting peer's
// identity + remote_id, returns the full MemoryPayload if AND only if the
// peer is entitled to that specific memory's workspace scope. The
// implementation MUST push the scope check down to the SQL query so
// un-granted rows never load into Go memory (defeats accidental leak via
// debug logs, count fields, or partial response construction).
//
// Returns:
//
//   - ErrMemoryNotFound — the id is unknown OR the requester isn't
//     scoped for it. The two cases are intentionally indistinguishable
//     to the caller; the local audit log records the real cause.
//   - ErrMemoryShareDenied — only for the (rare) coarse-scope failure
//     the caller already knows about (e.g. the boolean mesh.memory_request
//     grant was revoked between stream-open and lookup). Per-memory
//     scope failures must map to ErrMemoryNotFound to preserve the
//     constant-shape posture.
type MemoryProvider interface {
	GetMemoryPayload(
		ctx context.Context, peerID, remoteID string, peerScopes []string,
	) (*MemoryPayload, error)
}

// MemoryReceiver is the receiving-side hook: invoked after the receiver
// pulls a payload and decides to import. The implementation persists the
// memory locally (with origin_peer_id populated) and returns the new
// local memory id for the offer-accept bookkeeping. The OfferAcceptor
// chooses NOT to import by returning ErrMemoryShareDenied.
type MemoryReceiver interface {
	HandleIncomingMemory(
		ctx context.Context, peerID string, payload *MemoryPayload,
	) (localID string, err error)
}

// MemoryOfferRecorder stashes incoming offers into local persistence so
// the dashboard / agent can browse them and decide accept/decline.
type MemoryOfferRecorder interface {
	RecordOffer(ctx context.Context, peerID, peerName string, offer *MemoryOffer) error
}

// MemoryShareAuditor is the audit hook for every offer/request/install
// transition. Optional; nil = no audit.
type MemoryShareAuditor interface {
	RecordMemoryShare(
		ctx context.Context, action, peerID, remoteID, status, errMsg string,
	)
}

// MemoryAutoPuller is the policy hook that decides whether a freshly-
// received offer should be pulled SILENTLY (without a manual
// mesh__request_memory). The implementation owns the trust-tier check
// (Tier-1 SameUser only) AND the peer's opt-out (mesh.auto_replicate_off)
// AND the already-present check (don't re-pull an offer already imported).
//
// Returning true makes the service fire RequestMemory in the background.
// Optional; nil disables auto-pull entirely (offers stay OFFER-only).
//
// ShouldAutoPull MUST be cheap + side-effect-free: it runs inline on the
// offer receive path before the goroutine is spawned. OnAutoPulled is the
// post-success callback (best-effort, runs in the pull goroutine) so the
// owner can stamp the offer row accepted — making the already-present
// guard effective across a re-offer.
type MemoryAutoPuller interface {
	ShouldAutoPull(ctx context.Context, peerID string, offer *MemoryOffer) bool
	OnAutoPulled(ctx context.Context, peerID, remoteID, localID string)
}

// MemoryShareService is the libp2p stream handler + offer/request
// client. One instance per Host. The set of collaborators (provider /
// receiver / recorder / auditor) determines which directions are
// supported — a read-only peer would pass nil receiver, an outbound-
// only peer would pass nil provider.
type MemoryShareService struct {
	host       *Host
	lookup     PairedPeerLookup
	provider   MemoryProvider
	receiver   MemoryReceiver
	recorder   MemoryOfferRecorder
	auditor    MemoryShareAuditor
	autoPuller MemoryAutoPuller
	logger     *slog.Logger

	mu     sync.Mutex
	offers map[string]MemoryOffer // peerID|remoteID -> last offer received
	// inflight dedups concurrent auto-pulls of the same (peer, remote)
	// offer so a re-offer while a pull is mid-flight doesn't fire a second
	// RequestMemory. Keyed peerID|remoteID; entry removed when the pull
	// goroutine returns.
	inflight map[string]struct{}
	// pullFn is the function the auto-pull goroutine invokes to fetch the
	// payload. Defaults to s.RequestMemory; overridable in tests so the
	// auto-pull decision logic can be exercised without a live host +
	// connected peer.
	pullFn func(ctx context.Context, peerID, remoteID string) (string, error)
	// autoPullSem bounds concurrent auto-pull goroutines so an offer flood
	// with many distinct remote_ids can't spawn unbounded goroutines +
	// streams. Buffered to autoPullMaxConcurrent; a full channel means the
	// pull is dropped (re-offer or manual request retries).
	autoPullSem chan struct{}
}

// autoPullMaxConcurrent caps in-flight background auto-pulls per service.
// Small on purpose: silent replication is a best-effort background task,
// not a throughput path — dropped pulls are recovered by the next
// re-offer or a manual mesh__request_memory.
const autoPullMaxConcurrent = 8

// NewMemoryShareService wires the libp2p stream handler onto host.
// lookup, provider, receiver, recorder may individually be nil to
// disable that direction; auditor + logger may be nil.
func NewMemoryShareService(
	host *Host,
	lookup PairedPeerLookup,
	provider MemoryProvider,
	receiver MemoryReceiver,
	recorder MemoryOfferRecorder,
	auditor MemoryShareAuditor,
	logger *slog.Logger,
) *MemoryShareService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &MemoryShareService{
		host:        host,
		lookup:      lookup,
		provider:    provider,
		receiver:    receiver,
		recorder:    recorder,
		auditor:     auditor,
		logger:      logger,
		offers:      make(map[string]MemoryOffer),
		inflight:    make(map[string]struct{}),
		autoPullSem: make(chan struct{}, autoPullMaxConcurrent),
	}
	s.pullFn = s.RequestMemory
	if host != nil {
		host.Inner().SetStreamHandler(MemoryShareProtocol, s.handleMemoryStream)
	}
	return s
}

// SetAutoPuller installs the Tier-1 auto-pull policy hook. Optional —
// called once during wiring after construction. Passing nil (or never
// calling this) leaves the service in OFFER-only mode (no silent pulls).
func (s *MemoryShareService) SetAutoPuller(p MemoryAutoPuller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoPuller = p
}

// OfferMemory dials peerID and sends an offer descriptor. The receiver
// decides asynchronously whether to call RequestMemory back.
func (s *MemoryShareService) OfferMemory(
	ctx context.Context, peerID string, offer *MemoryOffer,
) error {
	if offer == nil {
		return errors.New("OfferMemory: nil offer")
	}
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, MemoryShareProtocol)
	if err != nil {
		return fmt.Errorf("open memory stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	offer.Type = "offer"
	_ = stream.SetWriteDeadline(time.Now().Add(memoryShareReadDeadline))
	if err := writeJSONLine(stream, offer); err != nil {
		s.recordAudit(ctx, "offer", peerID, offer.RemoteID, "error", err.Error())
		return err
	}
	s.recordAudit(ctx, "offer", peerID, offer.RemoteID, "ok", "")
	return nil
}

// RequestMemory dials peerID and asks for the full payload of the named
// memory. On success the payload is handed to the receiver and the new
// local id is returned. The caller (dashboard / accept flow) uses the
// id to stamp accepted_as_id on the memory_offers row.
func (s *MemoryShareService) RequestMemory(
	ctx context.Context, peerID, remoteID string,
) (string, error) {
	if s.receiver == nil {
		return "", errors.New("RequestMemory: no receiver configured")
	}
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return "", err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return "", fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, MemoryShareProtocol)
	if err != nil {
		return "", fmt.Errorf("open memory stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(memoryShareReadDeadline))
	req := MemoryRequest{Type: "request", RemoteID: remoteID}
	if err := writeJSONLine(stream, req); err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	payload, err := readMemoryPayload(stream)
	if err != nil {
		s.recordAudit(ctx, "request", peerID, remoteID, "error", err.Error())
		return "", err
	}
	localID, err := s.receiver.HandleIncomingMemory(ctx, peerID, payload)
	if err != nil {
		s.recordAudit(ctx, "install", peerID, remoteID, "error", err.Error())
		return "", fmt.Errorf("install: %w", err)
	}
	s.recordAudit(ctx, "install", peerID, remoteID, "ok", "")
	return localID, nil
}

// LastOfferFor returns the most recent offer received from peerID for
// remoteID, or (zero, false) if none. The dashboard uses this to render
// the accept/decline modal without re-fetching over the wire.
func (s *MemoryShareService) LastOfferFor(peerID, remoteID string) (MemoryOffer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers[peerID+"|"+remoteID]
	return o, ok
}

// assertPeerPaired returns nil iff peerID is in the paired list +
// active. Mirror of skill-share's assertPeerPaired.
func (s *MemoryShareService) assertPeerPaired(ctx context.Context, peerID string) error {
	if s.lookup == nil {
		return fmt.Errorf("%w: no lookup configured", ErrPeerNotPaired)
	}
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	return nil
}

// recordAudit is a nil-safe wrapper around the auditor hook.
func (s *MemoryShareService) recordAudit(
	ctx context.Context, action, peerID, remoteID, status, errMsg string,
) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordMemoryShare(ctx, action, peerID, remoteID, status, errMsg)
}
