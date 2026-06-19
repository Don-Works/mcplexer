//go:build !p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

// MaxMemoryBytes mirrors the p2p-build constant for stub mode.
const MaxMemoryBytes int64 = 1 * 1024 * 1024

// Memory share sentinels — declared in stub mode too so gateway tools
// can use errors.Is without a build-tag fence.
var (
	ErrMemoryNotFound    = errors.New("p2p: memory not found on peer")
	ErrMemoryTooLarge    = errors.New("p2p: memory exceeds size cap")
	ErrMemoryShareDenied = errors.New("p2p: memory share denied")
)

// EntityLink mirrors the p2p-build shape so import sites compile.
type EntityLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

// MemoryOffer mirrors the p2p-build shape so import sites compile.
type MemoryOffer struct {
	Type            string          `json:"type"`
	RemoteID        string          `json:"remote_id"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Description     string          `json:"description,omitempty"`
	Preview         string          `json:"preview,omitempty"`
	TagsJSON        json.RawMessage `json:"tags,omitempty"`
	MetadataJSON    json.RawMessage `json:"metadata,omitempty"`
	EmbedModel      string          `json:"embed_model,omitempty"`
	SizeBytes       int64           `json:"size_bytes"`
	EntitiesPreview []EntityLink    `json:"entities_preview,omitempty"`
}

// MemoryRequest mirrors the p2p-build shape so import sites compile.
type MemoryRequest struct {
	Type     string `json:"type"`
	RemoteID string `json:"remote_id"`
}

// MemoryPayload mirrors the p2p-build shape so import sites compile.
type MemoryPayload struct {
	Type              string          `json:"type"`
	RemoteID          string          `json:"remote_id"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind"`
	Content           string          `json:"content"`
	TagsJSON          json.RawMessage `json:"tags,omitempty"`
	MetadataJSON      json.RawMessage `json:"metadata,omitempty"`
	EmbedModel        string          `json:"embed_model,omitempty"`
	EmbedVersion      int             `json:"embed_version,omitempty"`
	Embedding         []float32       `json:"embedding,omitempty"`
	Entities          []EntityLink    `json:"entities,omitempty"`
	RemoteWorkspaceID string          `json:"remote_workspace_id,omitempty"`
}

// IsEntityKindPeerLocal mirrors the p2p-build helper. Slim builds
// always run alone so the rule never matters at runtime — but the
// function must exist so callers compile.
func IsEntityKindPeerLocal(kind string) bool {
	switch normalizeEntityKindForLocal(kind) {
	case "place", "event":
		return true
	}
	return false
}

// FilterEntitiesForMesh mirrors the p2p-build helper.
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

// MemoryProvider declared in stub mode for compile-time binary parity.
// Same signature as the p2p-build interface — see memory_share_p2p.go
// for the load-bearing security semantics (per-peer scope check at the
// SQL layer, NO leak of un-granted memories through error paths).
type MemoryProvider interface {
	GetMemoryPayload(
		ctx context.Context, peerID, remoteID string, peerScopes []string,
	) (*MemoryPayload, error)
}

// MemoryReceiver declared in stub mode for compile-time binary parity.
type MemoryReceiver interface {
	HandleIncomingMemory(
		ctx context.Context, peerID string, payload *MemoryPayload,
	) (localID string, err error)
}

// MemoryOfferRecorder declared in stub mode for compile-time binary parity.
type MemoryOfferRecorder interface {
	RecordOffer(ctx context.Context, peerID, peerName string, offer *MemoryOffer) error
}

// MemoryShareAuditor declared in stub mode for compile-time binary parity.
type MemoryShareAuditor interface {
	RecordMemoryShare(
		ctx context.Context, action, peerID, remoteID, status, errMsg string,
	)
}

// MemoryAutoPuller declared in stub mode for compile-time binary parity.
// See memory_share_p2p.go for the Tier-1 silent-replication semantics.
type MemoryAutoPuller interface {
	ShouldAutoPull(ctx context.Context, peerID string, offer *MemoryOffer) bool
	OnAutoPulled(ctx context.Context, peerID, remoteID, localID string)
}

// MemoryShareService is a stub when the binary is built without `-tags p2p`.
// Methods return ErrP2PNotBuiltIn so callers branch on the sentinel and
// surface "p2p not enabled" replies to the agent.
type MemoryShareService struct{}

// NewMemoryShareService returns a non-nil stub so route registration in
// the gateway works in both build modes (mirrors NewSkillShareService).
func NewMemoryShareService(
	_ *Host, _ PairedPeerLookup, _ MemoryProvider,
	_ MemoryReceiver, _ MemoryOfferRecorder,
	_ MemoryShareAuditor, _ *slog.Logger,
) *MemoryShareService {
	return &MemoryShareService{}
}

// SetAutoPuller is a no-op in stub mode — there's no stream handler to
// receive offers, so auto-pull never fires.
func (s *MemoryShareService) SetAutoPuller(_ MemoryAutoPuller) {}

// OfferMemory returns ErrP2PNotBuiltIn in stub mode.
func (s *MemoryShareService) OfferMemory(
	_ context.Context, _ string, _ *MemoryOffer,
) error {
	return ErrP2PNotBuiltIn
}

// RequestMemory returns ErrP2PNotBuiltIn in stub mode.
func (s *MemoryShareService) RequestMemory(
	_ context.Context, _, _ string,
) (string, error) {
	return "", ErrP2PNotBuiltIn
}

// LastOfferFor always returns (zero, false) in stub mode — no offers
// can ever have been received.
func (s *MemoryShareService) LastOfferFor(_, _ string) (MemoryOffer, bool) {
	return MemoryOffer{}, false
}
