// Package runner implements the M0.3 worker tool-use loop: load a Worker
// config from the store, resolve its model API key from secrets, render a
// prompt with optional skill body + parameter substitution, drive a
// model ↔ tool loop bounded by Caps, persist the WorkerRun, and emit
// lifecycle + output signals to the mesh and configured channels.
//
// The runner has zero direct dependencies on internal/gateway,
// internal/secrets, internal/mesh, or internal/skillregistry — every
// collaborator is taken through one of the small interfaces below, so
// the loop stays fast to test and the wiring layer carries the
// adapter responsibility.
package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

// Auditor writes audit records. Implemented by *audit.Logger; nil-safe
// (every call site is guarded). The runner shares this interface with
// the rest of the daemon so worker_run.* records land in the same
// audit ledger the dashboard already reads.
type Auditor interface {
	Record(ctx context.Context, rec *store.AuditRecord) error
}

// SecretReader reads a single key out of an AuthScope. Implemented by
// *secrets.Manager (the underlying type's Get signature already matches).
type SecretReader interface {
	Get(ctx context.Context, scopeID, key string) ([]byte, error)
}

// SkillReader fetches a skill body by name + version (empty version =
// latest stable). Implemented by a thin shim around *skillregistry.Registry
// (constructed at wiring time so it can pass through the right
// SkillScope and translate the version-ref convention). The workspaceID
// (from worker.WorkspaceID) is used for workspace-then-global fallback.
type SkillReader interface {
	GetSkillBody(ctx context.Context, workspaceID, name, version string) (body string, err error)
}

// ToolDispatcher is the runner's gateway hook: list available tools and
// dispatch a call. Implemented by the gateway. The runner does NOT
// import the gateway directly.
//
// Classify reports whether the named tool is write-class (side-effect
// producing). This is consulted BEFORE DispatchTool so propose-mode
// workers can short-circuit to awaiting_approval WITHOUT performing
// the write — DispatchTool's WriteClass return is preserved as a
// defensive layer (in case Classify is wrong / a tool re-classifies
// after dispatch).
type ToolDispatcher interface {
	ListTools(ctx context.Context, allowlist []string) ([]models.ToolSchema, error)
	DispatchTool(ctx context.Context, call ToolCallRequest) (ToolCallResult, error)
	Classify(name string) bool
}

// browserSessionReleaser is an OPTIONAL capability a ToolDispatcher may
// implement to tear down a finished worker's per-session browser process. The
// runner type-asserts for it in RunWithOpts's finalize defer; dispatchers not
// wired to a real downstream manager (test fakes) simply don't implement it,
// so the release is skipped. Mirrors the gateway's SessionReleaser pattern.
type browserSessionReleaser interface {
	ReleaseBrowserSession(sessionID string)
}

// BuiltinToolCaller invokes mcplexer's built-in tool surface — primarily
// mcpx__search_tools and mcpx__execute_code — through the gateway's full
// pipeline (sanitize, approval, audit, code-mode sandbox). The worker
// dispatcher delegates the two-tool surface through this interface so
// workers get the same hardening external MCP clients get.
//
// CallBuiltin: args is the JSON-encoded tool input exactly as the model
// emitted it. The returned bytes are the JSON-encoded MCP tools/call
// result envelope (`{"content":[...],"isError":bool}`); the dispatcher
// parses isError out of that envelope. A non-nil err indicates a
// transport / wiring failure distinct from tool-reported failure (which
// surfaces inside the envelope).
//
// WorkerToolSurface: returns the two-tool schema list workers see in
// their model-facing tool inventory. The dispatcher returns this to the
// runner from ListTools; everything else is reachable from inside an
// mcpx__execute_code snippet.
type BuiltinToolCaller interface {
	CallBuiltin(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	WorkerToolSurface(ctx context.Context) []models.ToolSchema
}

// ToolCallRequest describes one tool dispatch in flight.
type ToolCallRequest struct {
	Name      string
	InputJSON string
	WorkerID  string
	RunID     string
}

// ToolCallResult is the outcome of one dispatched tool call.
//
// WriteClass=true when the dispatcher considers this tool side-effect-
// producing. Used by the runner for propose-first gating: a propose-mode
// Worker that hits a WriteClass tool short-circuits to awaiting_approval
// instead of executing the call. The dispatcher (which has visibility
// into route policy + tool semantics) decides — the runner just reacts.
type ToolCallResult struct {
	OutputJSON string
	IsError    bool
	WriteClass bool
}

// MeshSender emits lifecycle + output signals. Implemented by a thin
// shim around *mesh.Manager (the underlying Send signature takes a
// SessionMeta + SendRequest; the shim populates a worker-shaped meta).
type MeshSender interface {
	Send(ctx context.Context, msg MeshOutbound) (messageID string, err error)
}

// MeshOutbound is the runner-shaped projection of a mesh send. Wiring
// translates these into the underlying mesh.SendRequest shape.
type MeshOutbound struct {
	Kind     string // "event" | "finding" | "alert"
	Priority string // "critical" | "high" | "normal" | "low"
	Content  string
	Tags     string // comma-separated
	WorkerID string
	RunID    string
	// WorkspaceID stamps the worker's workspace on the outbound message
	// so subscribers that filter by workspace (telegram bridge, etc.)
	// see it. Wiring fills this from worker.WorkspaceID.
	WorkspaceID string
	// NotifyUser=true fires a notify-bus event for this emission so
	// out-of-band sinks (telegram bridge, PWA toast) deliver it to the
	// human. Use sparingly — every notify event is a real ping.
	NotifyUser bool
	// ReplyTo threads the emission as a reply to an upstream mesh
	// message id (typically the triggering message). Telegram + UI use
	// reply_to to keep conversations linked.
	ReplyTo string
	// ToPeer deliberately routes this emission to one paired device. Empty
	// means local daemon only unless BroadcastPeers is explicitly true.
	ToPeer string
	// BroadcastPeers deliberately fans this emission out to paired devices.
	// The default is false so routine worker lifecycle/output chatter stays
	// local and does not surprise other machines.
	BroadcastPeers bool
	// AgentDisplayName is the human-friendly label to record as the
	// sending agent's name. When non-empty, wiring sets it as
	// mesh.SessionMeta.ClientType so the mesh manager renders it via
	// agentDisplayName. Lets a concierge run surface as
	// "concierge [Telegram, opencode_cli:MiniMax-M3]" rather than
	// the generic "worker". Empty = wiring falls back to "worker".
	AgentDisplayName string
}

// SameUserPeerLister reports whether the local node currently has at
// least one Tier-1 (same-user) paired peer. Used by the
// memory-consolidator finalize path to gate the provenance broadcast
// ("alice@m1 ran consolidator at HH:MM — N consolidations") on the
// presence of a same-user peer that would actually consume it.
//
// HasSameUserPeer returns false on any lookup error — most-restrictive
// default so a misconfigured node never broadcasts to peers it can't
// classify. Implementations live in cmd/mcplexer where the consent
// resolver + p2p_peers store can be combined; the runner takes the
// narrow interface to avoid pulling those packages into its imports.
//
// Wiring is optional: nil → consolidator broadcasts unconditionally
// (single-machine + tests). The production wiring always populates this
// so the broadcast respects tier classification.
type SameUserPeerLister interface {
	HasSameUserPeer(ctx context.Context) bool
}

// Clock is the small time abstraction the runner uses, so tests can
// drive the wall-clock cap deterministically.
type Clock interface {
	Now() time.Time
}

// RealClock returns time.Now().
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// AdapterFactory builds a ModelAdapter from a Config. The default
// (used in production) is models.NewAdapter; tests inject a fake.
type AdapterFactory func(cfg models.Config) (models.ModelAdapter, error)

// DefaultMaxInputTokens is the aggregate worker-loop input cap used when
// a Worker does not set MaxInputTokens. It is intentionally generous:
// normal runs should never notice it, but runaway transcripts and large
// repeated tool outputs stop before the next model send.
const DefaultMaxInputTokens = 200_000

// Caps bounds a single Run. Zero-valued Caps inherits defaults via
// applyDefaults. MaxOutputTokens caps the per-turn assistant reply;
// MaxInputTokens / MaxToolCalls / MaxWallClock are aggregate ceilings
// across the whole loop.
//
// MaxInputTokens=0 inherits DefaultMaxInputTokens. Setting MaxInputTokens
// to a positive value tells the runner to abort the loop with
// status=cap_exceeded once cumulative input_tokens reach it.
type Caps struct {
	MaxIterations   int           // default 12
	MaxToolCalls    int           // default 50
	MaxWallClock    time.Duration // default 300s
	MaxOutputTokens int           // default 4096 per turn
	MaxInputTokens  int           // default DefaultMaxInputTokens
}

// applyDefaults fills any zero-valued cap with its default.
func (c Caps) applyDefaults() Caps {
	if c.MaxIterations <= 0 {
		c.MaxIterations = 12
	}
	if c.MaxToolCalls <= 0 {
		c.MaxToolCalls = 50
	}
	if c.MaxWallClock <= 0 {
		c.MaxWallClock = 300 * time.Second
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = 4096
	}
	if c.MaxInputTokens <= 0 {
		c.MaxInputTokens = DefaultMaxInputTokens
	}
	return c
}

// Runner executes one or more Worker runs synchronously. Construct via
// New; the caller wires real impls of each interface.
type Runner struct {
	store      store.Store
	secrets    SecretReader
	skills     SkillReader
	dispatcher ToolDispatcher
	mesh       MeshSender
	clock      Clock
	httpClient *http.Client
	adapter    AdapterFactory
	caps       Caps
	auditor    Auditor
	outputsDir string
	runBus     *RunBus
	preamble   string

	// peerTiers gates the memory-consolidator Tier-1 broadcast. Nil →
	// broadcast unconditionally; non-nil → broadcast only when at
	// least one same-user peer is present. See SameUserPeerLister.
	peerTiers SameUserPeerLister
	// selfDisplay is the human-friendly identifier rendered into the
	// consolidator's mesh broadcast ("alice@m1 ran consolidator…").
	// Empty → falls back to "self". Wired from selfUser.DisplayName +
	// hostname in workers_wiring.go.
	selfDisplay string

	// cliToolCounter counts audit_records for CLI subprocess tool calls
	// so max_tool_calls can be enforced at finalize. Nil → skip.
	cliToolCounter CLIToolCallCounter

	// activeRuns tracks live run execution contexts so operator hard-stop
	// (Cancel) can interrupt an in-flight RunWithOpts goroutine and its
	// model adapter (including killing CLI subprocess groups via the
	// per-run cancellable context propagated into adapter.Send). Keyed by
	// runID; the entry is registered BEFORE the run row is persisted so a
	// concurrent CancelRun never sees an observable row without a live
	// cancel handle. Access under activeMu.
	activeMu   sync.Mutex
	activeRuns map[string]*activeRun
}

// activeRun is one live run's hard-stop handle. cancel tears down the
// per-run execution context with errOperatorCancel as the cause so the
// runLoop can distinguish an operator hard-stop from a wall-clock cap or
// a parent-deadline cancellation. reason carries the operator-supplied
// cancel message so the runner's finalize can stamp it on the run row.
type activeRun struct {
	cancel context.CancelCauseFunc
	reason string
}

// Deps bundles every collaborator the Runner needs. Pass real impls in
// production; pass fakes in tests.
type Deps struct {
	Store      store.Store
	Secrets    SecretReader
	Skills     SkillReader
	Dispatcher ToolDispatcher
	Mesh       MeshSender
	Clock      Clock          // optional — defaults to RealClock
	HTTPClient *http.Client   // optional — defaults to a 60s timeout client
	Adapter    AdapterFactory // optional — defaults to models.NewAdapter
	Caps       Caps           // optional — defaults applied per-field
	// Auditor — optional. When wired, the runner emits worker_run.*
	// records for every lifecycle event the dashboard cares about.
	// When nil, every audit emission is a silent no-op.
	Auditor Auditor
	// OutputsDir — filesystem root that bounds where the "file" output
	// channel may write. Paths that escape this root are rejected at
	// emission time. Empty disables the file channel entirely
	// (writeFileOutput returns a configuration error).
	OutputsDir string
	// RunBus — optional pub/sub bus for live SSE subscribers. When
	// wired, the runner publishes status / text_delta / tool_call /
	// usage events on every state transition so the workers SSE handler
	// can fan them out to open browser tabs without polling the DB. Nil
	// is safe — every publish site nil-checks.
	RunBus *RunBus
	// Preamble — gateway-owned system-prompt prefix prepended before
	// skill bodies. Tells the worker it's running inside mcplexer and
	// that its tool surface is exactly mcpx__search_tools +
	// mcpx__execute_code. Empty string disables injection (used by tests
	// that want clean prompt assertions).
	Preamble string
	// PeerTiers — optional. Gates the memory-consolidator Tier-1
	// provenance broadcast on the presence of at least one same-user
	// paired peer. Nil → broadcast unconditionally (tests / single-
	// machine setups).
	PeerTiers SameUserPeerLister
	// SelfDisplay — optional human-readable identifier ("alice@m1")
	// rendered into the memory-consolidator Tier-1 mesh broadcast.
	// Empty → "self".
	SelfDisplay string
	// CLIToolCounter — optional. Enforces max_tool_calls for CLI adapter
	// families by counting audit_records at finalize. Nil → cap is only
	// flagged at read time in the admin layer.
	CLIToolCounter CLIToolCallCounter
}

// New constructs a Runner. The Store and Dispatcher are mandatory; the
// rest fall back to safe defaults (Mesh nil → lifecycle signals are
// skipped; Skills nil → SkillName workers fail loudly when they need a
// body; Secrets nil → workers without an embedded key fail loudly).
func New(deps Deps) *Runner {
	r := &Runner{
		store:          deps.Store,
		secrets:        deps.Secrets,
		skills:         deps.Skills,
		dispatcher:     deps.Dispatcher,
		mesh:           deps.Mesh,
		clock:          deps.Clock,
		httpClient:     deps.HTTPClient,
		adapter:        deps.Adapter,
		caps:           deps.Caps.applyDefaults(),
		auditor:        deps.Auditor,
		outputsDir:     deps.OutputsDir,
		runBus:         deps.RunBus,
		preamble:       deps.Preamble,
		peerTiers:      deps.PeerTiers,
		selfDisplay:    deps.SelfDisplay,
		cliToolCounter: deps.CLIToolCounter,
	}
	if r.clock == nil {
		r.clock = RealClock{}
	}
	if r.adapter == nil {
		r.adapter = models.NewAdapter
	}
	if r.activeRuns == nil {
		r.activeRuns = make(map[string]*activeRun)
	}
	return r
}
