package mesh

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrUnknownAgent is returned by WaitForMessage when the supplied AgentName
// does not resolve to an active LOCAL mesh session. The HTTP layer maps this
// sentinel to a 400 so the caller knows to register via mesh__receive first.
var ErrUnknownAgent = errors.New("unknown agent: name does not resolve to an active local session")

// waitChanCap bounds the trigger channel. The channel is a wake SIGNAL, not a
// queue — on every wake we re-query the store, so a full channel just means
// "you're already going to re-scan", and dropping the extra trigger is safe.
const waitChanCap = 16

// maxWaitTimeout caps how long a single wait may block server-side, mirroring
// the HTTP contract's 3600s ceiling. Enforced here too so non-HTTP callers
// can't park a goroutine indefinitely.
const maxWaitTimeout = 3600 * time.Second

// WaitCriteria describes which mesh messages should wake a blocked agent.
// The audience match set is always {resolved session_id}, PLUS the agent's
// role when IncludeRole, PLUS "*" broadcast when IncludeBroadcast. Tags,
// AllTags, Kinds, status transitions, and FromPeer filters are applied on top
// (AND across filter categories; ANY within Tags/Kinds; ALL within AllTags).
type WaitCriteria struct {
	SessionID        string
	AgentName        string
	Role             string
	Tags             []string
	AllTags          []string
	Kinds            []string
	FromPeer         string
	StatusFrom       string
	StatusTo         string
	IncludeRole      bool
	IncludeBroadcast bool
	Consume          bool
	WorkspaceID      string
}

// WaitForMessage blocks until a mesh message targeting the named agent
// arrives, the timeout elapses, or ctx is cancelled. It is event-driven: it
// registers an M4 subscriber (which fires for both local mesh__send and
// p2p-ingested remote messages) and does ZERO polling. Returns:
//
//   - matched messages + nil error on a hit (newest-first, store order),
//   - (nil, nil) on timeout,
//   - (nil, ctx.Err()) on cancellation,
//   - (nil, ErrUnknownAgent) when neither SessionID nor AgentName resolves to
//     an active local session.
//
// With Consume the agent's mesh_agents.cursor is advanced past the newest
// returned message (mirroring Receive); otherwise the cursor is left intact
// so the woken agent can mesh__receive(filter:new) to actually consume.
func (m *Manager) WaitForMessage(ctx context.Context, c WaitCriteria, timeout time.Duration) ([]*store.MeshMessage, error) {
	if m == nil || m.store == nil {
		return nil, ErrUnknownAgent
	}
	agent, err := m.resolveWaitAgent(ctx, c)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}

	role := c.Role
	if role == "" {
		role = agent.Role
	}
	pred := m.buildMatcher(c, agent.SessionID, role)

	// Register the subscriber FIRST, then run the arming-race query. Doing it
	// in this order closes the gap where a message lands between the initial
	// scan and the subscribe — such a message fires the (already-registered)
	// subscriber, so the select below wakes and re-scans.
	trigger := make(chan struct{}, waitChanCap)
	unsub := m.Subscribe(func(_ context.Context, msg *store.MeshMessage) {
		if msg == nil || !workspaceMatches(c.WorkspaceID, msg.WorkspaceID) || !pred(msg) {
			return
		}
		select {
		case trigger <- struct{}{}:
		default: // full — we'll re-query on the pending wake anyway
		}
	})
	defer unsub()

	// Arming-race close: one query before blocking. A match already past the
	// cursor returns immediately.
	if msgs, err := m.scanWaiting(ctx, c, agent.SessionID, pred); err != nil {
		return nil, err
	} else if len(msgs) > 0 {
		return m.finishWait(ctx, c, agent.SessionID, msgs)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		case <-trigger:
			msgs, err := m.scanWaiting(ctx, c, agent.SessionID, pred)
			if err != nil {
				return nil, err
			}
			if len(msgs) == 0 {
				continue // spurious / raced-away match — keep waiting
			}
			return m.finishWait(ctx, c, agent.SessionID, msgs)
		}
	}
}

// EnsureAgent registers or refreshes the local session in the mesh agent
// directory without reading or consuming messages. It mirrors the identity
// side-effect of Receive and exists so long-running wait tools can arm a
// session before blocking.
func (m *Manager) EnsureAgent(ctx context.Context, meta SessionMeta, name, role string) error {
	if m == nil || m.store == nil {
		return ErrUnknownAgent
	}
	if err := m.ensureAgent(ctx, meta, name, role); err != nil {
		return err
	}
	_ = m.store.TouchMeshAgent(ctx, meta.SessionID)
	return nil
}

// resolveWaitAgent resolves either an explicit local session id (preferred by
// MCP handlers that already know the caller) or a friendly agent name
// (preserved for older/internal callers).
func (m *Manager) resolveWaitAgent(ctx context.Context, c WaitCriteria) (*store.MeshAgent, error) {
	if strings.TrimSpace(c.SessionID) != "" {
		agent, err := m.store.GetMeshAgent(ctx, strings.TrimSpace(c.SessionID))
		if err != nil || agent == nil || agent.Origin != store.MeshAgentOriginLocal {
			return nil, ErrUnknownAgent
		}
		return agent, nil
	}
	return m.resolveLocalAgent(ctx, c.AgentName)
}

// resolveLocalAgent resolves a friendly name to the most-recent active LOCAL
// session. Unlike ResolveAgentName it (a) ignores peer-origin rows — a wait
// targets THIS daemon's session — and (b) on duplicate names picks the most
// recently seen rather than erroring, since the waiter named itself.
func (m *Manager) resolveLocalAgent(ctx context.Context, name string) (*store.MeshAgent, error) {
	want := strings.TrimSpace(name)
	if want == "" {
		return nil, ErrUnknownAgent
	}
	since := time.Now().UTC().Add(-agentDirectoryActiveWindow)
	agents, err := m.store.ListActiveMeshAgents(ctx, "", since)
	if err != nil {
		return nil, err
	}
	var best *store.MeshAgent
	for i := range agents {
		a := agents[i]
		if a.Origin != store.MeshAgentOriginLocal {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Name), want) {
			continue
		}
		if best == nil || a.LastSeenAt.After(best.LastSeenAt) {
			cp := a
			best = &cp
		}
	}
	if best == nil {
		return nil, ErrUnknownAgent
	}
	return best, nil
}

// scanWaiting runs one "new since cursor" query (reusing the same store
// machinery Receive uses) and filters the result through pred. The store-side
// audience filter is intentionally left permissive (we pass no Audience/Role
// so the SQL doesn't force-include "*"); pred enforces the locked WAKE SCOPE.
func (m *Manager) scanWaiting(ctx context.Context, c WaitCriteria, sessionID string, pred func(*store.MeshMessage) bool) ([]*store.MeshMessage, error) {
	cursor := ""
	if a, err := m.store.GetMeshAgent(ctx, sessionID); err == nil && a != nil {
		cursor = a.Cursor
	}
	var wsIDs []string
	if c.WorkspaceID != "" {
		wsIDs = []string{c.WorkspaceID, ""}
	}
	rows, err := m.store.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: wsIDs,
		SinceID:      cursor,
		StatusLive:   true,
		Limit:        200,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*store.MeshMessage, 0, len(rows))
	for i := range rows {
		if pred(&rows[i]) {
			cp := rows[i]
			out = append(out, &cp)
		}
	}
	return out, nil
}

// finishWait optionally advances the cursor (Consume) then returns msgs.
func (m *Manager) finishWait(ctx context.Context, c WaitCriteria, sessionID string, msgs []*store.MeshMessage) ([]*store.MeshMessage, error) {
	if c.Consume && len(msgs) > 0 {
		newest := msgs[0].ID
		for _, msg := range msgs[1:] {
			if msg.ID > newest {
				newest = msg.ID
			}
		}
		_ = m.store.UpdateAgentCursor(ctx, sessionID, newest)
	}
	return msgs, nil
}

// buildMatcher returns a cheap predicate implementing the locked WAKE SCOPE
// (audience match set) plus the tags/kinds/from filters. Kept allocation-free
// on the hot path: the slices are read-only and small.
func (m *Manager) buildMatcher(c WaitCriteria, sessionID, role string) func(*store.MeshMessage) bool {
	tags := normalizeFilterSet(c.Tags)
	allTags := normalizeFilterSet(c.AllTags)
	kinds := normalizeFilterSet(c.Kinds)
	from := strings.TrimSpace(c.FromPeer)
	statusFrom := strings.TrimSpace(c.StatusFrom)
	statusTo := strings.TrimSpace(c.StatusTo)
	return func(msg *store.MeshMessage) bool {
		if msg == nil {
			return false
		}
		if !audienceMatches(msg.Audience, sessionID, role, c.IncludeRole, c.IncludeBroadcast) {
			return false
		}
		if len(kinds) > 0 && !kinds[strings.ToLower(strings.TrimSpace(msg.Kind))] {
			return false
		}
		if (statusFrom != "" || statusTo != "") && msg.Kind != KindTaskEvent {
			return false
		}
		if !statusTransitionMatches(msg.Tags, statusFrom, statusTo) {
			return false
		}
		if from != "" && !senderMatches(msg, from) {
			return false
		}
		if len(tags) > 0 && !msgHasAnyTag(msg.Tags, tags) {
			return false
		}
		if len(allTags) > 0 && !msgHasAllTags(msg.Tags, allTags) {
			return false
		}
		return true
	}
}

// audienceMatches implements the locked WAKE SCOPE: always the resolved
// session_id; the role only when includeRole; "*" only when includeBroadcast.
func audienceMatches(audience, sessionID, role string, includeRole, includeBroadcast bool) bool {
	switch {
	case audience == sessionID && sessionID != "":
		return true
	case includeBroadcast && audience == "*":
		return true
	case includeRole && role != "" && audience == role:
		return true
	}
	return false
}

// senderMatches reports whether msg originated from the named sender/peer.
// Matches on session_id (local/peer routing key) or the friendly agent name.
func senderMatches(msg *store.MeshMessage, from string) bool {
	return strings.EqualFold(strings.TrimSpace(msg.SessionID), from) ||
		strings.EqualFold(strings.TrimSpace(msg.AgentName), from) ||
		strings.EqualFold(strings.TrimSpace(msg.SenderDisplayName), from)
}

// msgHasAnyTag reports whether the message's comma-separated tag list shares
// at least one tag with want (ANY semantics).
func msgHasAnyTag(rawTags string, want map[string]bool) bool {
	for _, t := range strings.Split(rawTags, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && want[t] {
			return true
		}
	}
	return false
}

// msgHasAllTags reports whether every requested tag is present in the message.
func msgHasAllTags(rawTags string, want map[string]bool) bool {
	if len(want) == 0 {
		return true
	}
	have := make(map[string]bool, len(want))
	for _, t := range strings.Split(rawTags, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			have[t] = true
		}
	}
	for tag := range want {
		if !have[tag] {
			return false
		}
	}
	return true
}

// statusTransitionMatches applies task lifecycle transition filters. A status
// filter only matches genuine task_event:status_changed messages; a user-made
// message with an injected status_to: tag is not enough.
func statusTransitionMatches(rawTags, wantFrom, wantTo string) bool {
	if wantFrom == "" && wantTo == "" {
		return true
	}
	if !msgHasAnyTag(rawTags, map[string]bool{"task_event:status_changed": true}) {
		return false
	}
	from, to := statusTransition(rawTags)
	if wantFrom != "" && wantFrom != from {
		return false
	}
	if wantTo != "" && wantTo != to {
		return false
	}
	return true
}

func statusTransition(rawTags string) (from, to string) {
	for _, tag := range strings.Split(rawTags, ",") {
		tag = strings.TrimSpace(tag)
		if rest, ok := strings.CutPrefix(tag, "status_from:"); ok {
			from = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(tag, "status_to:"); ok {
			to = strings.TrimSpace(rest)
		}
	}
	return from, to
}

// normalizeFilterSet lowercases + trims a slice into a lookup set, dropping
// empties. Returns nil for an empty/all-blank input so callers can skip the
// filter with a len check.
func normalizeFilterSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// workspaceMatches reports whether a message in msgWS should be visible to a
// waiter scoped to wantWS. Empty wantWS = no workspace constraint (match all).
func workspaceMatches(wantWS, msgWS string) bool {
	return wantWS == "" || msgWS == "" || wantWS == msgWS
}
