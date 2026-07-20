package mesh

import (
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/p2p"
)

// validateInboundEnvelope applies the same content gates to a cross-peer
// envelope that Manager.Send applies to a local send: well-formed id,
// known kind, non-blank body, and bounded size. Returns an audit error
// code ("" = valid) so the ingest path can record the denial without
// giving the sender feedback.
//
// Metadata envelopes are routed before this check in ingestEnvelope and
// never reach it.
func validateInboundEnvelope(env p2p.MeshEnvelope) string {
	if !validULID(env.ID) {
		return "invalid_id"
	}
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

// validULID reports whether s is a canonical 26-character Crockford
// base32 ULID. Inbound envelope ids are peer-controlled, and a malformed
// one is a signal the sender is not speaking our wire format — reject it
// rather than carry it into the audit ledger as a correlation key.
//
// This gate alone does NOT protect the receive cursor: a peer with a
// forward-skewed clock mints perfectly valid ULIDs that sort above every
// local id. Cursor safety comes from re-minting the stored row id at
// ingest (see ingestEnvelope); this check keeps env.ID trustworthy as an
// audit correlation value.
func validULID(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil
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
