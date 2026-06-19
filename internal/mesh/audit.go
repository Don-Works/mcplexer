package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// Auditor is the narrow surface of *audit.Logger that the mesh package needs.
// Defined here (rather than importing internal/audit) to keep the dependency
// direction one-way and let tests pass a slice-collecting fake.
type Auditor interface {
	Record(ctx context.Context, rec *store.AuditRecord) error
}

// ConsentResolver is the narrow tier-resolution surface mesh uses to
// stamp peer-addressed mesh__send audit rows with tier + auto_pair
// status. Mirrors consent.Resolver methods we actually need so the
// mesh package stays free of a consent import (avoiding a future cycle
// risk + keeping the test surface tiny).
type ConsentResolver interface {
	// TierForString returns the string form ("same_user", "same_org",
	// "cross_org") of the peer's trust tier. Empty string means
	// "unknown" — the row is recorded without a tier field.
	TierForString(ctx context.Context, peerID string) string
	// AutoPairAccepted reports whether the peer is paired under the
	// auto-pair (same-user) flow.
	AutoPairAccepted(ctx context.Context, peerID string) bool
}

// SetAuditor wires an audit logger so every mesh send / receive / decision is
// recorded as an audit row. Nil disables audit (no-op fire-and-forget).
func (m *Manager) SetAuditor(a Auditor) {
	if m == nil {
		return
	}
	m.auditor = a
}

// SetConsentResolver wires the tier resolver used by recordSend to
// stamp peer-addressed audit rows with tier + accepted_by. Nil is
// permitted (rows are recorded without the consent envelope). selfUserID
// + selfAgentID are the local human identifiers folded into Tier 2/3
// human envelopes — passing empty strings is permitted (the envelope
// gets empty user_id/agent_id, which the consent_audit scenario will
// then SKIP+PENDING rather than FAIL).
func (m *Manager) SetConsentResolver(r ConsentResolver, selfUserID, selfAgentID string) {
	if m == nil {
		return
	}
	m.consentResolver = r
	m.selfUserID = selfUserID
	m.selfAgentID = selfAgentID
}

// auditAsync writes the audit row on a goroutine so the send/receive path is
// never blocked by SQLite contention. Errors are logged, never returned —
// audit failures must NEVER break the user's mesh activity.
//
// CorrelationID propagation: audit.Logger.Record auto-stamps rec.CorrelationID
// from audit.FromCtx(ctx) when empty. Because we detach to context.Background()
// to survive request cancellation, we MUST capture the correlation id from the
// caller's ctx BEFORE detaching and re-seed it onto the background ctx —
// otherwise every mesh audit row lands with correlation_id="" and forensics
// can't join mesh activity to its triggering request.
func (m *Manager) auditAsync(ctx context.Context, rec *store.AuditRecord) {
	if m == nil || m.auditor == nil || rec == nil {
		return
	}
	// Capture the correlation id from the live ctx BEFORE detaching, then
	// re-seed it onto the background ctx so audit.Logger.Record's
	// FromCtx() lookup still produces the same value the request had.
	correlationID := audit.FromCtx(ctx)
	// Detach from the request ctx so a cancelled call still records its
	// outcome. We only need ctx values, not its cancellation signal.
	bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if correlationID != "" {
		bg = audit.WithCorrelation(bg, correlationID)
	}
	// Capture the event name BEFORE the goroutine so a panic in Record
	// (which may mutate the record in-flight) still has a stable label
	// to log against.
	event := rec.ToolName
	go func() {
		defer cancel()
		// Recover any panic in the audit pipeline — a nil deref in a
		// downstream store, a JSON marshal blow-up, or a future code-mode
		// handler bug must NEVER terminate the daemon. Audit is
		// best-effort; the user's mesh activity must keep working.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("mesh audit goroutine panicked",
					"panic", fmt.Sprintf("%v", r),
					"event", event)
			}
		}()
		if err := m.auditor.Record(bg, rec); err != nil {
			slog.Warn("mesh audit record failed",
				"tool", rec.ToolName, "status", rec.Status, "err", err)
		}
	}()
}

// recordSend writes an audit row for a mesh__send invocation. body content is
// NEVER included — only recipient + kind + body length, mirroring the email
// send redaction policy from M4.1.
func (m *Manager) recordSend(
	ctx context.Context, meta SessionMeta, req SendRequest, msg *store.MeshMessage,
	status, errCode, errMsg string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	recipientKind, recipientValue := recipientFromRequest(req)
	args := map[string]any{
		"recipient_kind":  recipientKind,
		"recipient_value": recipientValue,
		"kind":            req.Kind,
		"body_length":     len(req.Content),
		"priority":        req.Priority,
	}
	if req.ToPeer != "" {
		args["to_peer"] = req.ToPeer
	}
	if req.LocalOnly {
		args["local_only"] = true
	}
	params, _ := json.Marshal(args)

	wsID := ""
	if len(meta.WorkspaceIDs) > 0 {
		wsID = meta.WorkspaceIDs[0]
	}
	now := time.Now().UTC()
	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		SessionID:      meta.SessionID,
		ClientType:     meta.ClientType,
		Model:          meta.ModelHint,
		WorkspaceID:    wsID,
		ToolName:       "mesh__send",
		ParamsRedacted: params,
		Status:         status,
		ErrorCode:      errCode,
		ErrorMessage:   errMsg,
		CreatedAt:      now,
		ActorKind:      "mesh",
		ActorID:        meta.SessionID,
	}
	// Cross-boundary consent envelope. Only peer-addressed mesh__send
	// counts as a cross-boundary share — broadcasts and audience filters
	// are public-by-design and don't need a per-recipient acknowledgement.
	// (See test/integration/scenario_consent_audit.sh::is_recipient_addressed_mesh.)
	if req.ToPeer != "" && m.consentResolver != nil {
		m.stampConsentEnvelope(ctx, rec, req.ToPeer, status, errMsg)
	}
	if msg != nil {
		// Stash the resulting message ID in error_message slot when status is
		// success; we don't have a generic result_json column on AuditRecord
		// so we surface message_id by appending it to params.
		args["message_id"] = msg.ID
		params, _ = json.Marshal(args)
		rec.ParamsRedacted = params
	}
	m.auditAsync(ctx, rec)
}

// stampConsentEnvelope populates rec.Tier + rec.AcceptedBy on a peer-
// addressed mesh__send audit row. Mirrors share_envelope.go in
// cmd/mcplexer but inline here so the mesh package doesn't pull a
// consent.Resolver dependency through. Tier 1 → auto_pair (no human
// fields); Tier 2/3 → human envelope with the local user_id + agent_id
// captured by SetConsentResolver.
func (m *Manager) stampConsentEnvelope(
	ctx context.Context, rec *store.AuditRecord,
	peerID, status, errMsg string,
) {
	tier := m.consentResolver.TierForString(ctx, peerID)
	if tier != "" {
		rec.Tier = tier
	}
	autoPair := m.consentResolver.AutoPairAccepted(ctx, peerID)
	envelope := map[string]any{}
	if autoPair {
		envelope["kind"] = "auto_pair"
	} else {
		envelope["kind"] = "human"
		if m.selfUserID != "" {
			envelope["user_id"] = m.selfUserID
		}
		if m.selfAgentID != "" {
			envelope["agent_id"] = m.selfAgentID
		}
		envelope["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	}
	if raw, err := json.Marshal(envelope); err == nil {
		rec.AcceptedBy = raw
	}
	// Best-effort denial reason on rejection rows. Aligned with the
	// internal/scopes/denial.go DenialCode vocabulary (no_scope,
	// scope_revoked, scope_out_of_band, cross_org_boundary) landed by
	// the parallel BUG-DENIAL agent. Wire-only extensions (not_paired)
	// supplement that vocabulary with audit-layer-specific reasons.
	if status == "denied" || status == "error" {
		switch {
		case errMsg == "":
			// status=error with no message — leave empty
		case containsCI(errMsg, "not paired"):
			rec.DenialReason = "not_paired"
		case containsCI(errMsg, "revoked"):
			rec.DenialReason = "scope_revoked"
		case containsCI(errMsg, "scope") && containsCI(errMsg, "required"):
			if tier == "cross_org" {
				rec.DenialReason = "cross_org_boundary"
			} else {
				rec.DenialReason = "no_scope"
			}
		case containsCI(errMsg, "denied"):
			if tier == "cross_org" {
				rec.DenialReason = "cross_org_boundary"
			} else {
				rec.DenialReason = "no_scope"
			}
		}
	}
}

// containsCI is a small case-insensitive substring helper. Inline to
// avoid pulling strings into this file just for ToLower (which the
// file already uses elsewhere transitively but not directly).
func containsCI(s, substr string) bool {
	return indexCI(s, substr) >= 0
}

func indexCI(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(s) < len(substr) {
		return -1
	}
	// Tiny, allocation-free loop — bytes only, sufficient for ASCII
	// error strings (the wire-layer error messages are ASCII-only).
	for i := 0; i+len(substr) <= len(s); i++ {
		ok := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// recordReceive writes an audit row for a single inbound libp2p envelope.
// Sender peer ID, body length, and was-paired flag go into params; body
// content is NEVER included.
func (m *Manager) recordReceive(
	ctx context.Context, senderPeerID, senderUserID, kind string,
	bodyLength int, wasPaired bool, envelopeID, status, errCode string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	args := map[string]any{
		"sender_peer_id": senderPeerID,
		"kind":           kind,
		"body_length":    bodyLength,
		"was_paired":     wasPaired,
		"envelope_id":    envelopeID,
	}
	if senderUserID != "" {
		args["sender_user_id"] = senderUserID
	}
	params, _ := json.Marshal(args)
	now := time.Now().UTC()
	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		ToolName:       "mesh__receive",
		ParamsRedacted: params,
		Status:         status,
		ErrorCode:      errCode,
		CreatedAt:      now,
		ActorKind:      "mesh",
		ActorID:        senderPeerID,
	}
	m.auditAsync(ctx, rec)
}

// recipientFromRequest derives an audit-friendly (kind, value) pair from a
// SendRequest. Falls back to "audience" + "*" for the default broadcast case.
func recipientFromRequest(req SendRequest) (string, string) {
	if req.ToPeer != "" {
		return "peer", req.ToPeer
	}
	if req.Audience != "" {
		return "audience", req.Audience
	}
	return "audience", "*"
}
