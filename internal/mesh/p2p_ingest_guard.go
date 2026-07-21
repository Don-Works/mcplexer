package mesh

import (
	"strings"

	"github.com/don-works/mcplexer/internal/p2p"
)

// validateInboundEnvelope applies the same content gates to a cross-peer
// envelope that Manager.Send applies to a local send: known kind,
// non-blank body, and bounded size. Returns an audit error code
// ("" = valid) so the ingest path can record the denial without giving
// the sender feedback.
//
// Metadata envelopes are routed before this check in ingestEnvelope and
// never reach it.
func validateInboundEnvelope(env p2p.MeshEnvelope) string {
	if !validKind(env.Kind) {
		return "invalid_kind"
	}
	if strings.TrimSpace(env.Content) == "" {
		return "blank_content"
	}
	if len(env.Content) > MaxSendContentBytes {
		return "content_too_large"
	}
	return ""
}

// clampPriority returns p when it is one of the known priorities,
// otherwise "normal". Used for inbound libp2p envelopes where the wire
// value is peer-controlled and absent on legacy peers.
func clampPriority(p string) string {
	if _, ok := priorityTTL[p]; ok {
		return p
	}
	return "normal"
}
