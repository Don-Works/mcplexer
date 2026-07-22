package mesh

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/don-works/mcplexer/internal/idtrunc"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// SessionMeta holds session context for mesh operations.
type SessionMeta struct {
	SessionID    string
	WorkspaceIDs []string // ancestor chain, most specific first
	ClientType   string
	ModelHint    string

	// WorkspacePath is the session's clientRoot — the absolute path the
	// MCP client reported via roots/list, or the deduced CWD. Used as
	// the auto-fill source for repo/branch/workspace metadata when the
	// caller doesn't pass them explicitly on SendRequest. Empty when
	// the session hasn't announced a root (e.g. stdio clients pre-roots).
	WorkspacePath string
}

// SendRequest holds parameters for sending a mesh message.
type SendRequest struct {
	Kind       string
	Content    string
	Priority   string
	Audience   string
	Tags       string
	ReplyTo    string
	NotifyUser bool
	// ActorKind identifies what fired the send so notify gates can
	// suppress worker-driven mutations from buzzing the user.
	// Empty defaults to "agent". Valid: "agent" | "worker" | "user" |
	// "peer-import" | "system". Stamped onto MeshMessage.ActorKind.
	ActorKind string
	// ToPeer routes the message to a specific paired libp2p peer ID
	// (cross-machine). Empty string = local + (when audience == "*")
	// broadcast to every paired peer unless LocalOnly is true.
	ToPeer string
	// LocalOnly suppresses libp2p dispatch even when Audience resolves to
	// "*". The message is still inserted into the local mesh store and
	// local subscribers are notified. Worker lifecycle/output plumbing uses
	// this so scheduled worker chatter stays on the daemon unless a channel
	// deliberately opts into peer delivery.
	LocalOnly bool
	// ToAgent targets a specific agent by friendly Name. Resolved against
	// the mesh_agents table: if the agent's origin is "local", we route to
	// it via audience=session_id on the local bus; if origin is "peer:<id>",
	// we set ToPeer and let the receiver's mesh manager dispatch by
	// audience=session_id on the remote side. Mutually exclusive with
	// explicit Audience/ToPeer — when ToAgent is set those fill in.
	ToAgent string

	// M7.3 — optional explicit repo/branch/workspace overrides. When
	// any of these is non-empty it wins over auto-detection from the
	// context's workspace path; empty strings fall through to git probe.
	Repo          string
	Branch        string
	WorkspacePath string
	RepoRemote    string

	// ToWorkspace overrides which workspace the message is filed under.
	// Empty (default): the sender's own workspace. "*" or "global":
	// the global namespace (WorkspaceID=""), visible to every session
	// on this daemon regardless of their workspace. A specific
	// workspace ID: files the message in that workspace's mesh, so a
	// session bound to that workspace sees it via the normal
	// workspace filter. Cross-workspace writes are open in the MVP
	// (any session can target any workspace); the sender's session
	// id is still recorded on the row so writes are auditable.
	ToWorkspace string
}

// ReceiveRequest holds parameters for receiving mesh messages.
type ReceiveRequest struct {
	Filter       string // new|all|thread
	Tags         string
	SinceMinutes int
	MaxResults   int
	ThreadID     string
	Name         string
	Role         string

	// Optional terminal locator the agent passes on first call so the
	// dashboard's "Focus" button knows which tmux pane to switch to.
	// Empty fields = no locator; UI greys the focus button. Persisted
	// onto the mesh_agents row so it survives across the agent's process
	// restarts (inherited via the same identity-port-forward as Name).
	TmuxSession string
	TmuxWindow  string
	TmuxPane    string

	// M7.3 — optional repo/branch/workspace filters. Empty means "any".
	Repo          string
	Branch        string
	WorkspacePath string

	// Kind-level filters (comma-separated). Kinds whitelists message
	// kinds; ExcludeKinds blacklists. Default behaviour (both empty):
	// kind=task_event is EXCLUDED — task lifecycle echoes are machine
	// plumbing, not conversation. Include "task_event" in Kinds to opt in.
	Kinds        string
	ExcludeKinds string

	// Actor-kind filters (comma-separated): "agent", "worker", "user",
	// "peer-import", "system". ActorKinds whitelists; ExcludeActorKinds
	// blacklists (e.g. ExcludeActorKinds:"worker" hides worker chatter).
	ActorKinds        string
	ExcludeActorKinds string
}

// ReceiveResult holds the result of a receive operation.
type ReceiveResult struct {
	Messages []store.MeshMessage
	Agents   []store.MeshAgent
	Stats    MeshStats
	// TaskEventsExcluded is true when the default kind=task_event
	// exclusion was applied, so the envelope hint can surface the opt-in.
	TaskEventsExcluded bool
}

// MeshStats holds summary statistics for the mesh.
type MeshStats struct {
	LiveMessages int
	ActiveAgents int
	NewForYou    int
}

// Priority TTL base durations.
var priorityTTL = map[string]time.Duration{
	"critical": 48 * time.Hour,
	"high":     8 * time.Hour,
	"normal":   2 * time.Hour,
	"low":      30 * time.Minute,
}

const maxLiveMessages = 5000

const (
	// MaxSendContentBytes is the hard upper bound for a single mesh
	// message body. Larger payloads should move through task attachments,
	// memory/task shares, or another content-addressed channel.
	MaxSendContentBytes = 64 * 1024

	DefaultReceiveMaxResults = 20
	MaxReceiveResults        = 50

	DefaultReceivePreviewBytes = 512
	MaxReceivePreviewBytes     = 2 * 1024

	DefaultHydrateContentBytes = 16 * 1024
	MaxHydrateContentBytes     = MaxSendContentBytes
)

// AgentBroadcaster is the optional hook the wire layer registers via
// SetAgentBroadcaster. Manager calls BroadcastDelta after a local
// mesh_agents row is inserted, updated, or expired so paired peers
// see the directory change in near-real-time.
//
// Implementations debounce + fan out — Manager calls fire-and-forget,
// no synchronisation needed. Nil-safe: when no broadcaster is wired
// (slim builds, tests), the calls are no-ops at the call site.
type AgentBroadcaster interface {
	// BroadcastDelta tells the wire layer about local agents added or
	// removed since the last call. added carries full MeshAgent rows;
	// removed is session_ids only.
	BroadcastDelta(ctx context.Context, added []store.MeshAgent, removed []string)
}

// Manager coordinates mesh send/receive operations.
type Manager struct {
	store            store.MeshStore
	notifyBus        *notify.Bus        // optional — if nil, notify_user flag is a no-op
	p2p              p2pTransport       // optional — set via SetP2PTransport
	selfPeerID       string             // own libp2p peer ID, used to stamp outgoing envelopes
	displayNameFn    func() string      // optional — returns local Settings.DisplayName
	peerRenamer      DisplayNameUpdater // optional — set via SetPeerRenamer
	peerLister       PeerLister         // optional — set via SetPeerLister; backs name → peer-id resolve + mesh__list_peers
	auditor          Auditor            // optional — set via SetAuditor; mesh sends/receives audited
	agentBroadcaster AgentBroadcaster   // optional — set via SetAgentBroadcaster; cross-peer agent gossip
	outboundQueue    *OutboundQueue     // optional — set via SetOutboundQueue; offline-delivery queue for to_peer sends

	// v0.13.0 — mesh__send_secret plumbing. All optional + nil-safe.
	peerIdentityUpdater PeerIdentityUpdater // set via SetPeerIdentityUpdater; persists peer's age recipient
	transferRecipientFn func() string       // set via SetTransferRecipientProvider; returns local age recipient
	secretOfferStager   SecretOfferStager   // set via SetSecretOfferStager; persists inbound secret offers
	skillOfferStager    SkillOfferStager    // set via SetSkillOfferStager; persists inbound skill-push offers

	// mesh.auth_sync plumbing. Optional + nil-safe; disabled until the
	// daemon wires the store, local secret encryptor, and transfer key.
	authSyncStore       authSyncStore
	authSyncEncryptor   *secrets.AgeEncryptor
	authSyncTransferKey *age.X25519Identity
	authSyncRefreshHook func()

	// Replay + staleness guard for inbound snapshots. Keyed per sender
	// peer so one peer cannot influence freshness tracking for another.
	// In-memory: defends against in-session replay/rollback; a process
	// restart resets the window, which is acceptable because a restart
	// also re-establishes peer sessions.
	authSyncGuardMu   sync.Mutex
	authSyncSeen      map[string]struct{}  // peerID\x00snapshotID
	authSyncFreshness map[string]time.Time // peerID\x00scopeName -> last accepted exported_at

	// consentResolver classifies cross-boundary mesh__send rows by
	// trust tier so the audit ledger carries tier + accepted_by per
	// epic 01KSK91Q4W8TNED9MAF0CTRVKC. Optional + nil-safe — when
	// unset the audit row is recorded without the consent envelope.
	consentResolver ConsentResolver
	// selfAcceptedBy holds the (user_id, agent_id) of the local
	// human who'd own the Tier 2/3 consent envelope. Snapshot at
	// construct/SetIdentity time.
	selfUserID  string
	selfAgentID string

	// subs is the M4 subscription registry. Lazily initialised on first
	// Subscribe so the field stays zero-valued on Managers that never
	// receive a subscriber (every existing test path).
	subs *subscribers

	// liveCeiling overrides maxLiveMessages when > 0. Test hook — the
	// production ceiling stays the package const.
	liveCeiling int
}

// liveMessageCeiling returns the per-namespace live-message cap.
func (m *Manager) liveMessageCeiling() int {
	if m != nil && m.liveCeiling > 0 {
		return m.liveCeiling
	}
	return maxLiveMessages
}

// SetOutboundQueue wires the offline-delivery queue so targeted to_peer
// sends that fail at the libp2p layer (peer offline / unreachable) get
// parked for later delivery instead of erroring. Nil disables the queue.
func (m *Manager) SetOutboundQueue(q *OutboundQueue) {
	if m == nil {
		return
	}
	m.outboundQueue = q
}

// OutboundQueue returns the configured queue, or nil when not wired.
// Exposed for the mesh__list_queue gateway handler + admin tooling.
func (m *Manager) OutboundQueue() *OutboundQueue {
	if m == nil {
		return nil
	}
	return m.outboundQueue
}

// SetAgentBroadcaster wires the cross-peer agent-directory broadcaster.
// When set, Manager fires BroadcastDelta after every local agent
// upsert/delete so paired peers see the change in near-real-time.
// Safe to call once at construction; subsequent calls overwrite.
func (m *Manager) SetAgentBroadcaster(b AgentBroadcaster) {
	m.agentBroadcaster = b
}

// NewManager creates a new mesh Manager.
func NewManager(s store.MeshStore) *Manager {
	return &Manager{store: s}
}

// SetNotifyBus wires a notification bus so notify_user=true messages surface
// to Electron / web UI subscribers. Safe to call once at construction.
func (m *Manager) SetNotifyBus(b *notify.Bus) {
	m.notifyBus = b
}

// ulidEntropy is MONOTONIC, not plain random, and newULID is the only
// caller — the difference is load-bearing.
//
// mesh_messages.id is the receive cursor, and the cursor filter is a
// lexicographic `id > ?`. With plain random entropy, two ULIDs minted in
// the SAME millisecond sort in random order (measured: ~52% inverted), so
// the invariant selectOldestBatch documents — "ULIDs sort lexicographically
// by creation time" — was false at millisecond resolution. The consequence
// is silent message loss: when a receive poll lands between two same-
// millisecond sends, it advances the cursor to the delivered id, and the
// second message sorts BELOW that cursor half the time and is never
// delivered. Bursty traffic (task_event broadcasts fire on every status
// transition) makes same-millisecond sends routine.
//
// ulid.Monotonic guarantees strictly increasing ids within a millisecond,
// which makes the documented invariant actually true. The mutex is required
// because a monotonic entropy source carries state across calls and mesh
// sends run concurrently.
var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

func newULID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	ts := ulid.Timestamp(time.Now())
	if id, err := ulid.New(ts, ulidEntropy); err == nil {
		return id.String()
	}
	// Monotonic entropy exhausted within this millisecond (needs ~2^80
	// mints in one ms). Fall back to random entropy rather than panicking:
	// a theoretically mis-ordered id beats a dead daemon.
	return ulid.MustNew(ts, rand.Reader).String()
}

// Send creates a message in the mesh.
func (m *Manager) Send(ctx context.Context, meta SessionMeta, req SendRequest) (*store.MeshMessage, error) {
	// Non-blank, not just non-empty: a whitespace-only body renders as an
	// empty inbox entry that still burns a pending-count nag + a hydrate.
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content is required (non-blank)")
	}
	if len(req.Content) > MaxSendContentBytes {
		return nil, fmt.Errorf("content too large (%d bytes; max %d)", len(req.Content), MaxSendContentBytes)
	}
	if req.Kind == "" {
		req.Kind = "event"
	}
	if !validKind(req.Kind) {
		return nil, fmt.Errorf("invalid kind %q: must be %s", req.Kind, ValidKindsHint())
	}

	priority := req.Priority
	if priority == "" {
		priority = "normal"
	}
	ttl, ok := priorityTTL[priority]
	if !ok {
		return nil, fmt.Errorf("invalid priority %q: must be critical|high|normal|low", priority)
	}

	audience := req.Audience
	if audience == "" {
		audience = "*"
	}
	// Persist the resolved audience back onto the request so the downstream
	// dispatchP2P broadcast guard (which inspects req.Audience) sees the
	// canonical "*" for the default-broadcast case. Without this, an
	// empty-audience send — the common mesh__send-with-no-audience path —
	// lands locally with audience="*" but is silently NOT transmitted to
	// paired peers, because dispatchP2P reads the still-empty req.Audience
	// and short-circuits. See the regression in dispatch_test.go.
	req.Audience = audience

	// Resolve to_peer when the caller passed a friendly device name or the
	// 10-char short id rendered by mesh__list_peers. Lets agents say
	// `to_peer: "morgan"` or `to_peer: "rpynr8M1cr"` and have it land on
	// the right paired machine. Full-shape peer IDs pass through unchanged
	// so cross-machine sends to an as-yet-unpaired peer still work the
	// same way (some flows rely on that — e.g. responding to a pairing
	// request mid-handshake). Empty/unknown short/name input → fail loudly
	// so the caller doesn't accidentally broadcast to everyone.
	if req.ToPeer != "" && !looksLikePeerID(req.ToPeer) {
		resolved, err := m.ResolvePeer(ctx, req.ToPeer)
		if err != nil {
			return nil, errors.New(FormatPeerNotPairedError(req.ToPeer, err))
		}
		req.ToPeer = resolved
	}

	// Resolve to_agent: look up the named agent in mesh_agents and project
	// it onto Audience (+ ToPeer for remote agents). Done after to_peer so
	// an explicit to_peer wins on conflict; we only fill in what's empty.
	//
	// toAgentWSSet/toAgentWS carry the resolved LOCAL target's own workspace
	// down to the wsID switch below, so the message is filed where the target
	// can actually read it. See the switch's case "" for why.
	var toAgentWS string
	var toAgentWSSet bool
	if req.ToAgent != "" {
		agent, err := m.ResolveAgentNameInWorkspaces(ctx, req.ToAgent, meta.WorkspaceIDs)
		if err != nil {
			return nil, err
		}
		if audience == "*" || audience == "" {
			audience = agent.SessionID
		}
		// peer:<id> → route over libp2p; the remote mesh manager will see
		// audience=session_id and deliver to the matching local session.
		if remote := agentRemotePeerID(agent); remote != "" && req.ToPeer == "" {
			req.ToPeer = remote
		} else if remote == "" {
			// Local target: remember its registration workspace so the empty
			// to_workspace path files the row there instead of under the
			// sender's most-specific workspace (which the target may not read).
			toAgentWS = agent.WorkspaceID
			toAgentWSSet = true
		}
		// Keep req.Audience in lock-step with the resolved local audience so
		// dispatchP2P sees the same value the message row is stamped with —
		// a targeted to_agent collapses audience to a session id, which the
		// broadcast guard must observe as non-"*" (it routes via ToPeer).
		req.Audience = audience
	}
	if req.LocalOnly && req.ToPeer != "" {
		return nil, fmt.Errorf("local_only cannot be combined with cross-peer routing")
	}

	// Auto-infer workspace_path from the session's clientRoot when the
	// caller didn't pass an explicit override. Saves agents from having
	// to threading workspace_path through every mesh__send call; the
	// gateway already knows which directory the session is rooted under
	// from the MCP roots/list handshake. Explicit req.WorkspacePath
	// still wins so cross-workspace messages remain possible.
	if req.WorkspacePath == "" {
		req.WorkspacePath = meta.WorkspacePath
	}

	// Resolve target workspace. `to_workspace` overrides the sender's
	// own — "*"/"global" lands in the global namespace (WorkspaceID="")
	// visible to every session; any other value targets that specific
	// workspace's mesh. Empty value falls through to the sender's first
	// non-blank workspace ID.
	//
	// CRITICAL: an empty result must NOT silently become the global
	// namespace. The global namespace ("") is read by EVERY session on
	// the daemon (see Receive's readableWorkspaceIDs, which unconditionally
	// appends ""), so a hand-built SessionMeta that omits WorkspaceIDs —
	// or carries a blank [""] — would broadcast routine traffic to every
	// workspace and burn every agent's context on each mesh__receive. That
	// footgun produced real cross-workspace chatter (system auto-recovery
	// alerts with no WorkspaceIDs; empty-workspace task events fired on
	// every status transition). We therefore reach "" ONLY on an explicit
	// "*"/"global" request; an unbound sender is isolated to a per-session
	// sentinel so its traffic stays out of unrelated workspaces' inboxes.
	wsID := ""
	switch req.ToWorkspace {
	case "*", "global":
		wsID = "" // explicit global broadcast — the only path to the shared namespace
	case "":
		// A targeted to_agent send addressed to a LOCAL agent files the row in
		// that agent's own registration workspace, not the sender's. The
		// sender's readable set spans its whole ancestor chain, so it can
		// legally resolve an agent registered under an ANCESTOR workspace; but
		// stamping the row with the sender's most-specific (descendant)
		// workspace — the default just below — puts it in a workspace the
		// target does NOT read, and the addressed message is silently dropped
		// while the sender gets a success receipt. Filing in the target's
		// workspace does not broadcast: the audience gate (canReadMessage) has
		// already collapsed to the target's session id, so only the target
		// reads it. toAgentWS may legitimately be "" (a globally-registered
		// target), which is exactly where that target reads.
		if toAgentWSSet {
			wsID = toAgentWS
			break
		}
		for _, id := range meta.WorkspaceIDs {
			if id != "" {
				wsID = id
				break
			}
		}
		if wsID == "" && meta.SessionID != "" {
			wsID = "session:" + meta.SessionID
		}
	default:
		wsID = req.ToWorkspace
	}

	now := time.Now().UTC()
	repoMeta := resolveRepoMeta(ctx, req)
	actorKind := req.ActorKind
	if actorKind == "" {
		actorKind = "agent"
	}
	msg := &store.MeshMessage{
		ID:            newULID(),
		WorkspaceID:   wsID,
		SessionID:     meta.SessionID,
		AgentName:     m.senderDisplayName(ctx, meta),
		Kind:          req.Kind,
		Priority:      priority,
		Content:       req.Content,
		Audience:      audience,
		Tags:          normalizeTags(req.Tags),
		Status:        "live",
		ExpiresAt:     now.Add(ttl),
		CreatedAt:     now,
		Repo:          repoMeta.Repo,
		Branch:        repoMeta.Branch,
		WorkspacePath: repoMeta.WorkspacePath,
		RepoRemote:    repoMeta.RepoRemote,
		ActorKind:     actorKind,
	}

	// Handle replies: set thread_root, increment parent reply count, extend parent TTL.
	if req.ReplyTo != "" {
		parent, err := m.store.GetMeshMessage(ctx, req.ReplyTo)
		if err != nil {
			return nil, fmt.Errorf("reply_to message not found: %w", err)
		}
		// SECURITY (M2): cross-workspace reply rejection. Without this
		// gate, a worker in workspace A could reply to any message in
		// workspace B and (a) extend that parent's TTL toward its 2x
		// ceiling and (b) inflate its reply_count. Concretely: a low-
		// privilege workspace's worker could keep messages alive in a
		// high-privilege workspace's stream, or use the reply_count
		// channel as a covert ping. The reply CONTENT lives in the
		// sender's workspace (so this isn't direct content injection
		// into B's stream), but parent-row mutation is enough of a
		// cross-workspace side channel to deserve a hard reject.
		// Empty parent workspace = legacy/global messages, allowed.
		if parent.WorkspaceID != "" && parent.WorkspaceID != msg.WorkspaceID {
			return nil, fmt.Errorf(
				"reply_to message %s belongs to a different workspace "+
					"(parent=%s, sender=%s); cross-workspace replies are denied",
				parent.ID, parent.WorkspaceID, msg.WorkspaceID,
			)
		}
		if parent.ThreadRoot != "" {
			msg.ThreadRoot = parent.ThreadRoot
		} else {
			msg.ThreadRoot = parent.ID
		}
		msg.ReplyTo = req.ReplyTo

		_ = m.store.IncrementReplyCount(ctx, req.ReplyTo)

		// Extend parent's expiry by 10% of its original TTL, capped at 2x.
		parentTTL := priorityTTL[parent.Priority]
		extension := parentTTL / 10
		maxExpiry := parent.CreatedAt.Add(parentTTL * 2)
		newExpiry := parent.ExpiresAt.Add(extension)
		if newExpiry.After(maxExpiry) {
			newExpiry = maxExpiry
		}
		_ = m.store.ExtendMessageExpiry(ctx, parent.ID, newExpiry)
	}

	if err := m.store.InsertMeshMessage(ctx, msg); err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}
	// M4 — notify subscribers (trigger dispatcher, etc.) after the
	// insert is durable. Subscribers are documented as fast; any heavier
	// work runs in their own goroutine to avoid blocking p2p dispatch.
	m.notifySubscribers(ctx, msg)
	// Enforce the live-message ceiling. Applies to EVERY namespace,
	// including the global one (wsID="") — global broadcasts are exactly
	// the bucket every session reads, so an unbounded backlog there is
	// the worst-case context burn.
	ceiling := m.liveMessageCeiling()
	count, cErr := m.store.CountLiveMessages(ctx, wsID)
	if cErr == nil && count > ceiling {
		excess := count - ceiling
		_, _ = m.store.ArchiveLowestPriority(ctx, wsID, excess)
	}

	// Cross-machine: libp2p transmit (no-op when transport is nil).
	if err := m.dispatchP2P(ctx, req, msg); err != nil {
		m.recordSend(ctx, meta, req, msg, "error", "dispatch_failed", err.Error())
		return nil, fmt.Errorf("dispatch p2p: %w", err)
	}

	// Auto-register agent.
	_ = m.ensureAgent(ctx, meta, "", "")

	// Audit the successful send (async, fire-and-forget).
	m.recordSend(ctx, meta, req, msg, "success", "", "")

	if req.NotifyUser && m.notifyBus != nil {
		role := ""
		if a, err := m.store.GetMeshAgent(ctx, meta.SessionID); err == nil && a != nil {
			role = a.Role
		}
		m.notifyBus.Publish(notify.Event{
			MessageID: msg.ID,
			Source:    "mesh",
			AgentName: msg.AgentName,
			Role:      role,
			Kind:      msg.Kind,
			Priority:  msg.Priority,
			Title:     buildNotifyTitle(msg, role),
			Body:      strings.TrimSpace(msg.Content),
			Tags:      msg.Tags,
			Link:      notifyLinkForMessage(msg),
			CreatedAt: msg.CreatedAt,
		})
	}

	return msg, nil
}

func notifyLinkForMessage(msg *store.MeshMessage) string {
	if msg == nil {
		return "/mesh"
	}
	if msg.Kind == KindTaskEvent {
		if taskID := tagValue(msg.Tags, "task_id:"); taskID != "" {
			link := "/tasks/" + url.PathEscape(taskID)
			if workspaceID := tagValue(msg.Tags, "workspace:"); workspaceID != "" {
				link += "?workspace=" + url.QueryEscape(workspaceID)
			}
			return link
		}
	}
	return "/mesh?msg=" + url.QueryEscape(msg.ID)
}

func tagValue(tags, prefix string) string {
	for _, tag := range strings.Split(tags, ",") {
		tag = strings.TrimSpace(tag)
		if strings.HasPrefix(tag, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(tag, prefix))
		}
	}
	return ""
}

func buildNotifyTitle(msg *store.MeshMessage, role string) string {
	who := msg.AgentName
	if role != "" {
		who = fmt.Sprintf("%s (%s)", msg.AgentName, role)
	}
	return fmt.Sprintf("MCPlexer: %s from %s", msg.Kind, who)
}

// NB: the notify event Body is the full mesh content. Downstream
// consumers handle display caps themselves — the PWA Signal tray uses
// line-clamp CSS, OS notifications truncate visually, and the Telegram
// bridge calls telegram.SplitBody() to fan the content across multiple
// 4096-char chat messages. Capping here would silently lose data on
// every Telegram delivery (regression: see fix/telegram-no-truncate).

// Receive retrieves messages and agent status from the mesh.
func (m *Manager) Receive(ctx context.Context, meta SessionMeta, req ReceiveRequest) (*ReceiveResult, error) {
	// Auto-register/update agent.
	_ = m.ensureAgent(ctx, meta, req.Name, req.Role)
	_ = m.store.TouchMeshAgent(ctx, meta.SessionID)
	// Optionally persist the agent's tmux locator so the dashboard's
	// Focus button works. Best-effort — failure here shouldn't block
	// the receive path.
	if req.TmuxSession != "" || req.TmuxWindow != "" || req.TmuxPane != "" {
		_ = m.store.SetMeshAgentTerminalLocator(ctx, meta.SessionID,
			req.TmuxSession, req.TmuxWindow, req.TmuxPane, time.Now().UTC())
	}

	agent, _ := m.store.GetMeshAgent(ctx, meta.SessionID)
	agentRole := ""
	if agent != nil {
		agentRole = agent.Role
	}

	wsID := ""
	if len(meta.WorkspaceIDs) > 0 {
		wsID = meta.WorkspaceIDs[0]
	}

	// readableWorkspaceIDs is the set the message filter scopes to:
	// the session's own workspaces (most specific first) plus the
	// global namespace (WorkspaceID="") so messages sent with
	// to_workspace:"*" / "global" are visible to every session. The
	// store query uses an IN clause so harmless duplicates / ordering
	// don't matter; we append unconditionally rather than checking
	// for presence.
	readableWorkspaceIDs := append([]string{}, meta.WorkspaceIDs...)
	readableWorkspaceIDs = append(readableWorkspaceIDs, "")

	filter := req.Filter
	if filter == "" {
		filter = "new"
	}

	maxResults := NormalizeReceiveMaxResults(req.MaxResults)

	// Kind-level filtering: task_event rows are hidden by default (see
	// resolveKindFilters). Applies to the new/all polling paths only —
	// filter=thread is an explicit read and stays inclusive.
	kinds, excludeKinds, taskEventsExcluded := resolveKindFilters(req)
	actorKinds := splitCSVList(req.ActorKinds)
	excludeActorKinds := splitCSVList(req.ExcludeActorKinds)

	var msgs []store.MeshMessage
	var err error

	switch filter {
	case "new":
		cursor := ""
		if agent != nil {
			cursor = agent.Cursor
		}
		// Burst-safe delivery (see cursor note below): we must never advance
		// the cursor past a message we didn't actually return, or that
		// message is lost forever. The store orders by priority then id DESC
		// and truncates with LIMIT, so the highest-priority rows win — not
		// the lowest-id ones. If we then advanced the cursor to the max
		// returned id, any lower-priority message with a smaller id that got
		// truncated would fall <= cursor and never be re-fetched.
		//
		// Fix: pull the full new-since-cursor window (capped at the workspace
		// live ceiling), deliver the OLDEST maxResults by id, and advance the
		// cursor to exactly that delivered batch's max id. Truncation then
		// only ever drops the NEWEST rows, which the next poll re-fetches.
		// Within the delivered batch we still present priority-first so the
		// caller sees the urgent items at the top.
		window, qErr := m.store.QueryMeshMessages(ctx, store.MeshMessageFilter{
			WorkspaceIDs: readableWorkspaceIDs,
			SinceID:      cursor,
			Audience:     meta.SessionID,
			AgentRole:    agentRole,
			Tags:         req.Tags,
			StatusLive:   true,
			// Own-session messages (e.g. the task_event broadcasts fired by
			// this agent's own task mutations) are never "new for you" —
			// without this they perpetually re-trigger the pending nag.
			// filter=all and thread reads stay inclusive for catch-up.
			ExcludeSessionID: meta.SessionID,
			// Scan oldest-first so a cursorScanLimit truncation drops only the
			// NEWEST rows (re-fetched next poll). Without this, a priority-first
			// LIMIT over a readable-workspace union larger than cursorScanLimit
			// keeps high-priority new rows but silently drops older low-priority
			// ones — and advancing the cursor past them loses them forever.
			OrderOldest:       true,
			Limit:             cursorScanLimit,
			Repo:              req.Repo,
			Branch:            req.Branch,
			WorkspacePath:     req.WorkspacePath,
			Kinds:             kinds,
			ExcludeKinds:      excludeKinds,
			ActorKinds:        actorKinds,
			ExcludeActorKinds: excludeActorKinds,
		})
		msgs, err = selectOldestBatch(window, maxResults), qErr

	case "all":
		sinceMinutes := req.SinceMinutes
		if sinceMinutes <= 0 {
			sinceMinutes = 60
		}
		since := time.Now().UTC().Add(-time.Duration(sinceMinutes) * time.Minute)
		msgs, err = m.store.QueryMeshMessages(ctx, store.MeshMessageFilter{
			WorkspaceIDs:      readableWorkspaceIDs,
			SinceTime:         &since,
			Audience:          meta.SessionID,
			AgentRole:         agentRole,
			Tags:              req.Tags,
			StatusLive:        true,
			Limit:             maxResults,
			Repo:              req.Repo,
			Branch:            req.Branch,
			WorkspacePath:     req.WorkspacePath,
			Kinds:             kinds,
			ExcludeKinds:      excludeKinds,
			ActorKinds:        actorKinds,
			ExcludeActorKinds: excludeActorKinds,
		})

	case "thread":
		if req.ThreadID == "" {
			return nil, fmt.Errorf("thread_id is required when filter=thread")
		}
		// Audience scoping MUST match the new/all branches: without Audience +
		// AgentRole the store applies no audience predicate and returns every
		// message in the thread regardless of audience, leaking replies
		// addressed to a specific session/role to any agent in the workspace.
		msgs, err = m.store.QueryMeshMessages(ctx, store.MeshMessageFilter{
			WorkspaceIDs: readableWorkspaceIDs,
			ThreadRoot:   req.ThreadID,
			Audience:     meta.SessionID,
			AgentRole:    agentRole,
			StatusLive:   true,
			Limit:        maxResults,
		})

	default:
		return nil, fmt.Errorf("invalid filter %q: must be new|all|thread", filter)
	}

	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}

	// Advance the cursor to the max id among the delivered batch. Because
	// selectOldestBatch returned the OLDEST maxResults messages by id, every
	// id at or below this boundary was just delivered — no un-returned id can
	// sit below the new cursor, so nothing is skipped. Any truncated (newer)
	// message keeps a higher id and is re-fetched on the next poll.
	//
	// B1 — but ONLY for a non-narrowed poll. A caller-narrowed filter=new
	// (kinds/actor-kinds/tags/repo/branch/path) filters the window at the
	// store, so the delivered batch's max id can sit ABOVE non-matching
	// messages with lower ids. Advancing the cursor there would push it past
	// those unmatched-but-unread rows and strand the broader backlog forever
	// (the documented failure: a kinds:"task_event" poll silently buries every
	// normal message below the cursor). A narrowed read is therefore a
	// NON-CONSUMING PEEK — it returns matching rows without moving the cursor;
	// the caller consumes via the canonical unfiltered poll. See
	// receiveIsNarrowed.
	if filter == "new" && len(msgs) > 0 && !receiveIsNarrowed(req) {
		latest := msgs[0].ID
		for _, msg := range msgs[1:] {
			if msg.ID > latest {
				latest = msg.ID
			}
		}
		_ = m.store.UpdateAgentCursor(ctx, meta.SessionID, latest)
	}

	// List active agents.
	activeSince := time.Now().UTC().Add(-30 * time.Minute)
	agents, _ := m.store.ListActiveMeshAgents(ctx, wsID, activeSince)

	// Compute stats.
	liveCount := 0
	if wsID != "" {
		liveCount, _ = m.store.CountLiveMessages(ctx, wsID)
	}

	return &ReceiveResult{
		Messages: msgs,
		Agents:   agents,
		Stats: MeshStats{
			LiveMessages: liveCount,
			ActiveAgents: len(agents),
			NewForYou:    len(msgs),
		},
		// Thread reads are explicit + inclusive; the kind filters only
		// applied to the new/all polling paths above.
		TaskEventsExcluded: taskEventsExcluded && filter != "thread",
	}, nil
}

// NormalizeReceiveMaxResults applies the manager-level hard cap for every
// mesh read path. Callers may warn when the returned value is lower than the
// requested positive value, but Manager itself always enforces the bound.
func NormalizeReceiveMaxResults(n int) int {
	if n <= 0 {
		return DefaultReceiveMaxResults
	}
	if n > MaxReceiveResults {
		return MaxReceiveResults
	}
	return n
}

// NormalizeReceivePreviewBytes applies the hard per-message preview cap used
// by mesh__receive. Explicit hydrate/thread reads use a larger cap.
func NormalizeReceivePreviewBytes(n int) int {
	if n <= 0 {
		return DefaultReceivePreviewBytes
	}
	if n > MaxReceivePreviewBytes {
		return MaxReceivePreviewBytes
	}
	return n
}

// NormalizeHydrateContentBytes applies the hard content cap for explicit
// hydrate/thread reads.
func NormalizeHydrateContentBytes(n int) int {
	if n <= 0 {
		return DefaultHydrateContentBytes
	}
	if n > MaxHydrateContentBytes {
		return MaxHydrateContentBytes
	}
	return n
}

// Hydrate retrieves one visible mesh message by id. It is intentionally
// separate from Receive so cheap polling only returns previews.
func (m *Manager) Hydrate(ctx context.Context, meta SessionMeta, messageID string) (*store.MeshMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	msg, err := m.store.GetMeshMessage(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("message not found: %w", err)
	}
	if !m.canReadMessage(ctx, meta, msg) {
		return nil, fmt.Errorf("message %s is not visible to this session", messageID)
	}
	return msg, nil
}

// Thread retrieves a visible thread root plus visible replies. The returned
// slice is capped and sorted chronologically by ULID so callers see the
// conversation in reading order.
func (m *Manager) Thread(ctx context.Context, meta SessionMeta, threadID string, maxResults int) ([]store.MeshMessage, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("thread_id is required")
	}
	limit := NormalizeReceiveMaxResults(maxResults)
	root, err := m.store.GetMeshMessage(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("thread root not found: %w", err)
	}
	if !m.canReadMessage(ctx, meta, root) {
		return nil, fmt.Errorf("thread %s is not visible to this session", threadID)
	}

	readableWorkspaceIDs := append([]string{}, meta.WorkspaceIDs...)
	readableWorkspaceIDs = append(readableWorkspaceIDs, "")
	replies, err := m.store.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: readableWorkspaceIDs,
		ThreadRoot:   threadID,
		StatusLive:   true,
		Limit:        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query thread: %w", err)
	}

	msgs := make([]store.MeshMessage, 0, 1+len(replies))
	msgs = append(msgs, *root)
	for _, msg := range replies {
		if len(msgs) >= limit {
			break
		}
		if m.canReadMessage(ctx, meta, &msg) {
			msgs = append(msgs, msg)
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })
	return msgs, nil
}

func (m *Manager) canReadMessage(ctx context.Context, meta SessionMeta, msg *store.MeshMessage) bool {
	if msg == nil || msg.Status != "live" {
		return false
	}
	if !workspaceReadable(meta.WorkspaceIDs, msg.WorkspaceID) {
		return false
	}
	if msg.Audience == "" || msg.Audience == "*" || msg.Audience == meta.SessionID {
		return true
	}
	if agent, err := m.store.GetMeshAgent(ctx, meta.SessionID); err == nil && agent != nil && agent.Role != "" {
		return msg.Audience == agent.Role
	}
	return false
}

func workspaceReadable(readable []string, workspaceID string) bool {
	if workspaceID == "" {
		return true
	}
	for _, id := range readable {
		if id == workspaceID {
			return true
		}
	}
	return false
}

// cursorScanLimit caps how many new-since-cursor messages a filter=new
// receive pulls before selecting the oldest batch. The workspace live
// ceiling is maxLiveMessages (5000), so this comfortably covers the full
// live window in one query while still bounding worst-case work.
const cursorScanLimit = maxLiveMessages

// selectOldestBatch picks the oldest `limit` messages (by id) from a
// new-since-cursor window and returns them ordered priority-first (id DESC
// tiebreak), matching the store's display ordering.
//
// Delivering the OLDEST ids — not the highest-priority ones — is what makes
// filter=new cursor advancement burst-safe: the caller's cursor is advanced
// to this batch's max id, so every id at or below the cursor was actually
// returned, and any truncated (newer) message keeps a higher id and is
// re-fetched next poll. See the rationale in Receive's "new" case.
func selectOldestBatch(window []store.MeshMessage, limit int) []store.MeshMessage {
	if len(window) == 0 || limit <= 0 {
		return nil
	}
	// Oldest-first by id (ULIDs sort lexicographically by creation time).
	sort.Slice(window, func(i, j int) bool { return window[i].ID < window[j].ID })
	if len(window) > limit {
		window = window[:limit]
	}
	// Re-order the delivered batch priority-first (id DESC tiebreak) so the
	// caller still sees urgent items at the top, matching the prior contract.
	sort.SliceStable(window, func(i, j int) bool {
		pi, pj := priorityRank(window[i].Priority), priorityRank(window[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return window[i].ID > window[j].ID
	})
	return window
}

// priorityRank mirrors the store's CASE ordering: lower rank sorts first.
func priorityRank(p string) int {
	switch p {
	case "critical":
		return 0
	case "high":
		return 1
	case "normal":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

// RegisterAgent ensures the agent is registered in the mesh without
// sending or receiving messages. Called on session initialize so agents
// are discoverable immediately on connect.
func (m *Manager) RegisterAgent(ctx context.Context, meta SessionMeta) error {
	return m.ensureAgent(ctx, meta, "", "")
}

// SetAgentStatus persists a free-form status string for the agent
// identified by meta.SessionID. Auto-registers the agent first if not
// already known so the same call works on first turn. Returns an error
// when the status is empty or longer than agentStatusMaxLen.
func (m *Manager) SetAgentStatus(ctx context.Context, meta SessionMeta, status string) error {
	status = strings.TrimSpace(status)
	if status == "" {
		return fmt.Errorf("status is required")
	}
	if len(status) > agentStatusMaxLen {
		return fmt.Errorf("status too long (%d chars; max %d)", len(status), agentStatusMaxLen)
	}
	if err := m.ensureAgent(ctx, meta, "", ""); err != nil {
		return err
	}
	// Capture the prior status so the audit row records the transition,
	// not just the new value. Best-effort: a lookup miss yields "" which
	// the audit reader interprets as "first set".
	oldStatus := ""
	if prev, err := m.store.GetMeshAgent(ctx, meta.SessionID); err == nil && prev != nil {
		oldStatus = prev.Status
	}
	err := m.store.SetMeshAgentStatus(ctx, meta.SessionID, status, time.Now().UTC())
	auditStatus := "success"
	auditErrMsg := ""
	if err != nil {
		auditStatus = "error"
		auditErrMsg = err.Error()
	}
	m.RecordSetAgentStatus(ctx, meta, meta.SessionID, oldStatus, status, auditStatus, auditErrMsg)
	return err
}

// agentStatusMaxLen caps the persistent status string. Generous enough
// for "building agent-directory gossip, ETA 5m, branch feat/X" but
// short enough that a runaway agent can't pollute the directory with
// kilobytes of state.
const agentStatusMaxLen = 200

// meshMessageCounter is the OPTIONAL fast-count capability the sqlite store
// implements (CountMeshMessages): a covering COUNT(*) that loads no message
// bodies and does no sort. PendingCount uses it when available and falls back
// to the two-query QueryMeshMessages path otherwise, so a store that doesn't
// implement it still works. Kept off store.MeshStore deliberately — adding it
// there would break every full mock of the interface.
type meshMessageCounter interface {
	CountMeshMessages(ctx context.Context, f store.MeshMessageFilter, limit int) (int, error)
}

// pendingCountCap mirrors the saturation ceiling of the historical two-query
// PendingCount (it re-queried with Limit 100). The piggyback nag only needs
// "how many, roughly", so bounding the count keeps the hot path cheap.
const pendingCountCap = 100

// PendingCount returns the number of unread messages for the given agent.
// Used for piggyback delivery — fires on EVERY tool response, so it must stay
// cheap: with the fast counter it is a single covering COUNT(*), no ORDER BY,
// no content column.
func (m *Manager) PendingCount(ctx context.Context, meta SessionMeta) (int, error) {
	agent, err := m.store.GetMeshAgent(ctx, meta.SessionID)
	if err != nil {
		// Agent not registered yet — no pending messages.
		return 0, nil
	}

	// Scope-aligned with Receive: the session's own workspaces PLUS the
	// global namespace (""), so to_workspace:"*" broadcasts count toward
	// the nag exactly like Receive would deliver them. kind=task_event is
	// excluded to mirror the receive default — machine lifecycle echoes
	// must not inflate the "[mesh: N pending]" piggyback. This is exactly the
	// predicate subset CountMeshMessages supports, so the fast path counts
	// precisely what a default (unnarrowed) Receive would deliver.
	filter := store.MeshMessageFilter{
		WorkspaceIDs:     append(append([]string{}, meta.WorkspaceIDs...), ""),
		SinceID:          agent.Cursor,
		Audience:         meta.SessionID,
		AgentRole:        agent.Role,
		StatusLive:       true,
		ExcludeSessionID: meta.SessionID, // own sends are not pending reads
		ExcludeKinds:     []string{KindTaskEvent},
	}

	// Fast path: a single covering COUNT(*), saturated at pendingCountCap.
	if counter, ok := m.store.(meshMessageCounter); ok {
		return counter.CountMeshMessages(ctx, filter, pendingCountCap)
	}

	// Fallback for stores without the fast counter: probe existence, then
	// re-query capped to get the count — behaviour-identical to the fast path
	// (min(actual, pendingCountCap)), just via two full-row queries.
	filter.Limit = 1
	msgs, err := m.store.QueryMeshMessages(ctx, filter)
	if err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	filter.Limit = pendingCountCap
	all, err := m.store.QueryMeshMessages(ctx, filter)
	if err != nil {
		return 1, nil // We know there's at least 1.
	}
	return len(all), nil
}

func (m *Manager) ensureAgent(ctx context.Context, meta SessionMeta, name, role string) error {
	wsID := ""
	if len(meta.WorkspaceIDs) > 0 {
		wsID = meta.WorkspaceIDs[0]
	}

	// Identity inheritance: when no row exists yet for THIS session_id
	// (the typical post-restart case — session_id is per-process) and
	// the caller didn't supply a name, port name/role/status/locator
	// forward from the most-recent prior local row in the same
	// (workspace_id, client_type) bucket. This is what fixes the
	// "all my agents revert to 'Claude Code' on restart and the
	// status I set disappears" UX failure — without it every fresh
	// process showed up as an anonymous claude-code row.
	var inheritStatus, inheritTmuxSession, inheritTmuxWindow, inheritTmuxPane string
	if name == "" {
		existing, _ := m.store.GetMeshAgent(ctx, meta.SessionID)
		if existing == nil {
			prior, err := m.store.FindRecentLocalAgentByClient(ctx, wsID, meta.ClientType, meta.SessionID)
			if err == nil && prior != nil {
				name = prior.Name
				if role == "" {
					role = prior.Role
				}
				inheritStatus = prior.Status
				inheritTmuxSession = prior.TmuxSession
				inheritTmuxWindow = prior.TmuxWindow
				inheritTmuxPane = prior.TmuxPane
			}
		}
	}

	now := time.Now().UTC()
	row := &store.MeshAgent{
		SessionID:   meta.SessionID,
		WorkspaceID: wsID,
		Name:        name,
		Role:        role,
		ClientType:  meta.ClientType,
		ModelHint:   meta.ModelHint,
		Origin:      store.MeshAgentOriginLocal,
		Status:      inheritStatus,
		TmuxSession: inheritTmuxSession,
		TmuxWindow:  inheritTmuxWindow,
		TmuxPane:    inheritTmuxPane,
		LastSeenAt:  now,
		CreatedAt:   now,
	}
	if err := m.store.UpsertMeshAgent(ctx, row); err != nil {
		return err
	}
	// Fan out to paired peers so their mesh_agents directory picks up
	// the new/refreshed local agent. Best-effort, fire-and-forget — the
	// broadcaster debounces, so a hot ensureAgent loop becomes one
	// frame on the wire.
	if m.agentBroadcaster != nil {
		m.agentBroadcaster.BroadcastDelta(ctx, []store.MeshAgent{*row}, nil)
	}
	return nil
}

// clientTypeDisplayNames pretty-prints fallback ClientType values that
// would otherwise leak internal labels (e.g. "rest") into user-visible
// surfaces. Caller-supplied names pass through unchanged.
var clientTypeDisplayNames = map[string]string{
	"rest": "REST API",
}

func agentDisplayName(meta SessionMeta) string {
	if meta.ClientType == "" {
		return "unknown"
	}
	if pretty, ok := clientTypeDisplayNames[meta.ClientType]; ok {
		return pretty
	}
	return meta.ClientType
}

// senderDisplayName resolves the best available sender attribution for an
// outbound message. The registered mesh-agent name wins (it's what shows in
// the agent directory, so "from" lines stay consistent with it). System
// emitters like the task_event Emitter send with a bare SessionMeta (no
// ClientType), which used to render every task broadcast as "from unknown" —
// fall back to a short session id before surrendering to "unknown".
func (m *Manager) senderDisplayName(ctx context.Context, meta SessionMeta) string {
	if meta.SessionID != "" {
		if agent, err := m.store.GetMeshAgent(ctx, meta.SessionID); err == nil && agent != nil && agent.Name != "" {
			return agent.Name
		}
	}
	name := agentDisplayName(meta)
	if name == "unknown" && meta.SessionID != "" {
		return idtrunc.Short(meta.SessionID, 8)
	}
	return name
}

func normalizeTags(tags string) string {
	if tags == "" {
		return ""
	}
	parts := strings.Split(tags, ",")
	var cleaned []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return strings.Join(cleaned, ",")
}
