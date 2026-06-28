package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/readiness"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/telegram"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/oklog/ulid/v2"
)

// Notifier sends JSON-RPC notifications to the connected client.
type Notifier interface {
	Notify(method string, params any) error
}

// Server is the MCP gateway server.
type Server struct {
	handler           *handler
	mu                sync.Mutex    // protects stdout writes
	w                 io.Writer     // set at start of run(), used for notifications
	keepaliveInterval time.Duration // 0 = disabled
	readiness         *readiness.Tracker
}

// NewServer creates a new MCP gateway server.
func NewServer(
	s store.Store,
	engine *routing.Engine,
	manager ToolLister,
	auditor *audit.Logger,
	transport TransportMode,
	opts ...ServerOption,
) *Server {
	var sopts serverOptions
	for _, o := range opts {
		o.apply(&sopts)
	}
	h := newHandler(
		s, engine, manager, auditor, transport,
		sopts.approvals, sopts.mesh, sopts.bridge, sopts.settingsSvc,
		sopts.addonRegistry, sopts.addonExecutor, sopts.sessionBus,
		sopts.secretPrompts, sopts.secretsManager,
		sopts.skillShare, sopts.skillRegistry, sopts.registryShare, sopts.memorySvc, sopts.memoryShare, sopts.adminGate,
	)
	if sopts.tasksSvc != nil {
		h.setTasksSvc(sopts.tasksSvc)
	}
	if sopts.workerAdmin != nil {
		h.setWorkerAdmin(sopts.workerAdmin)
	}
	if sopts.conciergeSvc != nil {
		h.setConciergeSvc(sopts.conciergeSvc)
	}
	if sopts.brainEditor != nil {
		h.setBrainEditor(sopts.brainEditor)
	}
	if sopts.secretTransferKey != nil {
		h.setSecretTransferKey(sopts.secretTransferKey)
	}
	if sopts.repoBrainDiscovery != nil {
		h.sessions.SetRepoBrainDiscovery(sopts.repoBrainDiscovery)
	}
	return &Server{
		handler:           h,
		keepaliveInterval: sopts.keepaliveInterval,
		readiness:         sopts.readiness,
	}
}

// serverOptions holds optional configuration applied via ServerOption.
type serverOptions struct {
	approvals          *approval.Manager
	mesh               *mesh.Manager
	bridge             *telegram.Manager
	settingsSvc        *config.SettingsService
	addonRegistry      *addon.Registry
	addonExecutor      *addon.Executor
	secretPrompts      *ephemeral.Manager
	secretsManager     *secrets.Manager
	sessionBus         *session.Bus
	skillShare         *p2p.SkillShareService
	skillRegistry      *skillregistry.Registry
	registryShare      *p2p.RegistryShareService
	memorySvc          *memory.Service
	memoryShare        *p2p.MemoryShareService
	tasksSvc           *tasks.Service
	workerAdmin        *workersadmin.Service
	conciergeSvc       *concierge.Service
	brainEditor        *brain.Editor
	adminGate          *AdminCWDGate
	secretTransferKey  *age.X25519Identity
	readiness          *readiness.Tracker
	keepaliveInterval  time.Duration
	repoBrainDiscovery func(ctx context.Context, clientRoot, workspaceID string)
}

// ServerOption configures optional server features.
type ServerOption interface {
	apply(opts *serverOptions)
}

type withApprovals struct{ m *approval.Manager }

func (o withApprovals) apply(opts *serverOptions) { opts.approvals = o.m }

// WithApprovals enables the tool call approval system.
func WithApprovals(m *approval.Manager) ServerOption { return withApprovals{m} }

type withAdminGate struct{ g *AdminCWDGate }

func (o withAdminGate) apply(opts *serverOptions) { opts.adminGate = o.g }

// WithAdminGate restricts admin tool visibility to sessions whose CWD is
// at or under the configured data directory. Without this option, every
// session sees every admin tool — appropriate for tests but not for
// production. Pass nil to disable the gate explicitly.
func WithAdminGate(g *AdminCWDGate) ServerOption { return withAdminGate{g} }

type withSettings struct{ s *config.SettingsService }

func (o withSettings) apply(opts *serverOptions) { opts.settingsSvc = o.s }

// WithSettings provides the settings service to the gateway handler.
func WithSettings(s *config.SettingsService) ServerOption { return withSettings{s} }

type withAddons struct {
	r *addon.Registry
	e *addon.Executor
}

func (o withAddons) apply(opts *serverOptions) {
	opts.addonRegistry = o.r
	opts.addonExecutor = o.e
}

// WithAddons enables addon tools that bridge gaps in downstream MCP servers.
func WithAddons(r *addon.Registry, e *addon.Executor) ServerOption {
	return withAddons{r: r, e: e}
}

type withKeepalive struct{ d time.Duration }

func (o withKeepalive) apply(opts *serverOptions) { opts.keepaliveInterval = o.d }

// WithKeepalive enables periodic keepalive writes to detect stale connections.
// When a write fails (e.g. after system sleep/wake), the connection is closed.
func WithKeepalive(d time.Duration) ServerOption { return withKeepalive{d} }

type withSessionBus struct{ b *session.Bus }

func (o withSessionBus) apply(opts *serverOptions) { opts.sessionBus = o.b }

// WithSessionBus publishes session connect/disconnect events to the bus.
func WithSessionBus(b *session.Bus) ServerOption { return withSessionBus{b} }

type withMesh struct{ m *mesh.Manager }

func (o withMesh) apply(opts *serverOptions) { opts.mesh = o.m }

// WithMesh enables the agent mesh communication system.
func WithMesh(m *mesh.Manager) ServerOption { return withMesh{m} }

type withBridge struct{ m *telegram.Manager }

func (o withBridge) apply(opts *serverOptions) { opts.bridge = o.m }

// WithTelegram enables the chat bridge MCP tool surface (chat__*).
func WithTelegram(m *telegram.Manager) ServerOption { return withBridge{m} }

type withSecretPrompts struct{ m *ephemeral.Manager }

func (o withSecretPrompts) apply(opts *serverOptions) { opts.secretPrompts = o.m }

// WithSecretPrompts enables the secret__prompt MCP tool, which lets agents
// request a secret from the human without ever seeing the value (the agent
// only receives the file path). Pass nil to disable the feature.
func WithSecretPrompts(m *ephemeral.Manager) ServerOption { return withSecretPrompts{m} }

type withSecretsManager struct{ m *secrets.Manager }

func (o withSecretsManager) apply(opts *serverOptions) { opts.secretsManager = o.m }

// WithSecretsManager wires the encrypted auth-scope storage into the gateway.
// Required by mcpx__provision_mcp so captured human secrets can be persisted
// without ever exposing them to the agent. Pass nil to disable provisioning.
func WithSecretsManager(m *secrets.Manager) ServerOption { return withSecretsManager{m} }

type withSkillShare struct{ s *p2p.SkillShareService }

func (o withSkillShare) apply(opts *serverOptions) { opts.skillShare = o.s }

// WithSkillShare enables the M2.7 mesh__offer_skill / mesh__request_skill
// agent tools that share signed skill bundles between paired peers. Pass
// nil (or omit) to disable the feature; the gateway returns "p2p not
// enabled" replies in that case.
func WithSkillShare(s *p2p.SkillShareService) ServerOption {
	return withSkillShare{s: s}
}

type withSkillRegistry struct{ r *skillregistry.Registry }

func (o withSkillRegistry) apply(opts *serverOptions) { opts.skillRegistry = o.r }

// WithSkillRegistry enables the universal mcpx__skill_search/get/publish/
// list tools backed by the agent-facing skills registry. Pass nil (or
// omit) to disable; the tools then return "registry not enabled" replies.
func WithSkillRegistry(r *skillregistry.Registry) ServerOption {
	return withSkillRegistry{r: r}
}

type withRegistryShare struct{ s *p2p.RegistryShareService }

func (o withRegistryShare) apply(opts *serverOptions) { opts.registryShare = o.s }

// WithRegistryShare enables the mesh__skill_hub_index /
// mesh__skill_hub_search / mesh__skill_hub_pull agent tools for discovering
// and pulling skill-registry entries from a paired hub peer.
// Pass nil (or omit) to disable; the tools return "registry hub sync not
// enabled" replies in that case.
func WithRegistryShare(s *p2p.RegistryShareService) ServerOption {
	return withRegistryShare{s: s}
}

type withMemory struct{ s *memory.Service }

func (o withMemory) apply(opts *serverOptions) { opts.memorySvc = o.s }

// WithMemory enables the universal memory__save/recall/search/list/forget
// tools backed by the memory subsystem (migration 058). Pass nil (or
// omit) to disable; the tools then return "memory not enabled" replies.
func WithMemory(s *memory.Service) ServerOption { return withMemory{s: s} }

type withMemoryShare struct{ s *p2p.MemoryShareService }

func (o withMemoryShare) apply(opts *serverOptions) { opts.memoryShare = o.s }

// WithMemoryShare enables the memory__offer_memory / memory__request_memory
// agent tools that ride the /mcplexer/memory/1.0.0 libp2p protocol.
// Pass nil to disable; the tools then return "p2p memory share not
// enabled" replies.
func WithMemoryShare(s *p2p.MemoryShareService) ServerOption {
	return withMemoryShare{s: s}
}

type withTasks struct{ s *tasks.Service }

func (o withTasks) apply(opts *serverOptions) { opts.tasksSvc = o.s }

// WithTasks enables the universal task__* tools backed by the tasks
// subsystem (migration 061). Pass nil (or omit) to disable; the tools
// then return "tasks not enabled" replies.
func WithTasks(s *tasks.Service) ServerOption { return withTasks{s: s} }

type withWorkerAdmin struct{ s *workersadmin.Service }

func (o withWorkerAdmin) apply(opts *serverOptions) { opts.workerAdmin = o.s }

// WithWorkerAdmin enables mcpx__delegate_worker and related delegation
// ledger helpers. Pass nil or omit to keep the surface disabled.
func WithWorkerAdmin(s *workersadmin.Service) ServerOption { return withWorkerAdmin{s: s} }

type withConcierge struct{ s *concierge.Service }

func (o withConcierge) apply(opts *serverOptions) { opts.conciergeSvc = o.s }

// WithConcierge enables the concierge__record_signal tool backed by the
// self-improving chat signal log (migration 080). Pass nil (or omit) to
// disable; the tool then returns "concierge not enabled" replies.
func WithConcierge(s *concierge.Service) ServerOption { return withConcierge{s: s} }

type withBrainEditor struct{ e *brain.Editor }

func (o withBrainEditor) apply(opts *serverOptions) { opts.brainEditor = o.e }

// WithBrainEditor enables the agent-facing brain__* tools (tree/list/get/
// search + write_note) backed by the canonical Markdown brain (M0-M7). Pass
// nil (or omit) to disable; the tools then return "brain subsystem is not
// enabled" replies.
func WithBrainEditor(e *brain.Editor) ServerOption { return withBrainEditor{e: e} }

type withSecretTransferKey struct{ k *age.X25519Identity }

func (o withSecretTransferKey) apply(opts *serverOptions) { opts.secretTransferKey = o.k }

// WithSecretTransferKey wires the local age X25519 identity used by the
// mesh__accept_secret tool to decrypt inbound secret-offer ciphertexts.
// Pass nil (or omit) to disable the receive half of mesh__send_secret;
// the send half still works.
func WithSecretTransferKey(k *age.X25519Identity) ServerOption {
	return withSecretTransferKey{k: k}
}

type withRepoBrainDiscovery struct {
	fn func(ctx context.Context, clientRoot, workspaceID string)
}

func (o withRepoBrainDiscovery) apply(opts *serverOptions) { opts.repoBrainDiscovery = o.fn }

// WithRepoBrainDiscovery wires the per-repo .mcplexer/ discovery callback
// fired on session bind (M6 — federation, docs/brain.md Appendix C.2). The
// callback walks the client root for a repo-local brain folder and registers
// it with the indexer/watcher. Pass nil (or omit) to keep today's behaviour.
func WithRepoBrainDiscovery(fn func(ctx context.Context, clientRoot, workspaceID string)) ServerOption {
	return withRepoBrainDiscovery{fn: fn}
}

type withReadiness struct{ t *readiness.Tracker }

func (o withReadiness) apply(opts *serverOptions) { opts.readiness = o.t }

func WithReadiness(t *readiness.Tracker) ServerOption { return withReadiness{t: t} }

// RunStdio runs the MCP server over stdio (stdin/stdout).
func (s *Server) RunStdio(ctx context.Context) error {
	return s.run(ctx, os.Stdin, os.Stdout)
}

// CallTool invokes a single tool through the gateway's full pipeline
// (sanitize, approval, audit, code-mode sandbox when name is
// mcpx__execute_code) without going through stdio / socket transport.
//
// This is the entry point used by workers — they reach the same builtin
// surface that external MCP clients reach, but without ever performing
// an MCP initialize handshake. The session manager therefore stays
// uninitialized on a worker-bound Server, which the handler tolerates
// (clientRoot returns "", workspace lookups fall back to global routes,
// admin-CWD gating denies admin tools, etc.).
//
// Returns the raw MCP tools/call result envelope: a JSON object with
// "content" and "isError" keys. A non-nil err indicates a transport
// failure distinct from a tool-reported failure.
func (s *Server) CallTool(
	ctx context.Context, name string, args json.RawMessage,
) (json.RawMessage, error) {
	req := CallToolRequest{Name: name, Arguments: args}
	params, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal tool call: %w", err)
	}
	result, rpcErr := s.handler.handleToolsCall(ctx, params)
	if rpcErr != nil {
		return nil, fmt.Errorf("%s: %s", name, rpcErr.Message)
	}
	return result, nil
}

// WorkerToolSurface returns the two-tool schema list (mcpx__search_tools
// and mcpx__execute_code) that worker runs see in their tool inventory.
// Workers do NOT see downstream tools, mesh tools, secret tools, or any
// other builtin in their top-level list — everything is reachable from
// inside an mcpx__execute_code snippet via mcpx__search_tools discovery.
// The schemas are returned in the simplified shape the runner consumes:
// name + description + JSON input schema.
func (s *Server) WorkerToolSurface(ctx context.Context) []WorkerToolSchema {
	execTool, _ := s.handler.buildCodeExecuteTool(ctx)
	searchTool := searchToolsDefinition()
	return []WorkerToolSchema{
		toWorkerSchema(searchTool),
		toWorkerSchema(execTool),
	}
}

// WorkerToolSchema is the gateway's projection of a tool schema in the
// shape the worker runner expects (name + description + JSON input
// schema). The wiring layer translates this to models.ToolSchema; this
// shape keeps internal/gateway free of an internal/models dependency.
type WorkerToolSchema struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

func toWorkerSchema(t Tool) WorkerToolSchema {
	return WorkerToolSchema{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: t.InputSchema,
	}
}

// RunConn runs the MCP server over an arbitrary reader/writer pair.
func (s *Server) RunConn(ctx context.Context, r io.Reader, w io.Writer) error {
	return s.run(ctx, r, w)
}

func (s *Server) run(ctx context.Context, r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	defer func() {
		cancel()
		wg.Wait()
		s.handler.bgWg.Wait()
		disconnCtx, disconnCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer disconnCancel()
		// Release task leases held by this session so working-status
		// tasks demote to "open" immediately instead of waiting for
		// the passive lease sweep (up to ~6 min).
		if s.handler.tasksSvc != nil {
			if n, err := s.handler.tasksSvc.ReleaseSessionTasks(disconnCtx, s.handler.sessions.sessionID()); err != nil {
				slog.Debug("tasks: release session tasks on disconnect", "err", err)
			} else if n > 0 {
				slog.Debug("tasks: released leases on disconnect", "count", n)
			}
		}
		// Stop any per-session browser-automation instances this session
		// spawned so its dedicated browser process dies now, rather than
		// lingering until the idle timer reaps it (up to ~5 min).
		if sr, ok := s.handler.manager.(SessionReleaser); ok {
			sr.ReleaseSession(s.handler.sessions.sessionID())
		}
		// Drop the ephemeral code-mode `session` object held in memory for
		// this session so it isn't retained for a dead connection.
		s.handler.clearSessionState(s.handler.sessions.sessionID())
		// Remove agent from mesh before disconnecting the session.
		if s.handler.mesh != nil {
			_ = s.handler.store.DeleteMeshAgent(disconnCtx, s.handler.sessions.sessionID())
		}
		_ = s.handler.sessions.disconnect(disconnCtx)
	}()

	s.w = w
	s.handler.setNotifier(s)
	s.handler.bgCtxMu.Lock()
	s.handler.bgCtx = ctx
	s.handler.bgCtxMu.Unlock()

	if s.keepaliveInterval > 0 {
		done := make(chan struct{})
		defer close(done)
		go s.runKeepalive(done, r)
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Copy: scanner reuses its buffer on the next Scan().
		msg := make([]byte, len(line))
		copy(msg, line)

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in dispatch", "error", r)
				}
			}()
			resp := s.dispatch(ctx, msg)
			if resp == nil {
				return
			}
			if err := s.writeResponse(w, resp); err != nil {
				slog.Warn("write failed, closing connection", "error", err)
				cancel()
				if c, ok := r.(io.Closer); ok {
					_ = c.Close()
				}
			}
		}()
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		slog.Warn("connection read error", "error", scanErr)
	} else {
		slog.Info("client disconnected (EOF)")
	}
	return scanErr
}

func (s *Server) dispatch(ctx context.Context, line []byte) (resp *Response) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return &Response{
			JSONRPC: "2.0",
			Error: &RPCError{
				Code:    CodeParseError,
				Message: "invalid JSON: " + err.Error(),
			},
		}
	}

	// Belt-and-braces: catch panics inside handler dispatch and surface
	// them to the caller as a structured JSON-RPC error rather than
	// silently dropping the response (the outer recover in handleConn
	// previously logged the panic but returned nil, leaving agents in
	// retry loops — see internal/mesh/format.go slice-bounds panic).
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			slog.Error("panic in dispatch", "method", req.Method, "error", r, "stack", string(stack))
			resp = &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInternalError,
					Message: fmt.Sprintf("internal panic in %s: %v", req.Method, r),
				},
			}
		}
	}()

	// Notifications have no ID; don't send a response.
	if req.ID == nil {
		s.handleNotification(req)
		return nil
	}

	if s.readiness != nil && s.readiness.IsDraining() && req.Method == "tools/call" {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    CodeInternalError,
				Message: "daemon is draining — retry after reconnect",
			},
		}
	}

	// Seed correlation_id at the MCP gateway entry. Every handler
	// downstream (handleToolsCall → dispatch → audit emit → secrets
	// read) inherits this id, so a single request — whether stdio
	// JSON-RPC or an HTTP-bridged call — joins slog + audit on one
	// key. The id is fresh-minted per request; the internal
	// handleCodeExecute path still issues its own narrower
	// execution_id (uuid) for individual tool calls within a Code
	// Mode invocation, which lives in the AuditRecord.ExecutionID
	// column.
	ctx = audit.WithCorrelation(ctx, "mcp:"+ulid.Make().String())

	// Stamp the caller's identity (session + workspace) so deep emitters —
	// notably the secrets resolver, which fires several layers below this
	// entry on every `secret://` substitution and scope enumeration —
	// attribute their audit rows to the originating agent instead of a
	// detached scope placeholder. WithAttribution is a no-op when no session
	// is bound, so this is safe for every method.
	ctx = audit.WithAttribution(ctx, s.handler.callerAttribution(ctx))

	var result json.RawMessage
	var rpcErr *RPCError

	switch req.Method {
	case "initialize":
		result, rpcErr = s.handler.handleInitialize(ctx, req.Params)
	case "ping":
		result, _ = json.Marshal(map[string]any{})
	case "tools/list":
		result, rpcErr = s.handler.handleToolsList(ctx)
	case "tools/call":
		result, rpcErr = s.handler.handleToolsCall(ctx, req.Params)
	// MCP resources, prompts, and completion are explicitly unsupported.
	// mcplexer aggregates tools across downstream servers but does not
	// bridge resource/prompt/completion catalogs. These methods return a
	// descriptive MethodNotFound so clients get a clear signal rather
	// than a generic "unknown method" RPC error.
	case "resources/list",
		"resources/read",
		"resources/templates/list",
		"resources/subscribe",
		"resources/unsubscribe":
		rpcErr = &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("resources are not supported by this gateway: %s", req.Method),
		}
	case "prompts/list",
		"prompts/get":
		rpcErr = &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("prompts are not supported by this gateway: %s", req.Method),
		}
	case "completion/complete":
		rpcErr = &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("completion is not supported by this gateway: %s", req.Method),
		}
	default:
		rpcErr = &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method: %s", req.Method),
		}
	}

	resp = &Response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	return resp
}

func (s *Server) handleNotification(req Request) {
	switch req.Method {
	case "notifications/initialized":
		slog.Info("client initialized")
	default:
		slog.Debug("unhandled notification", "method", req.Method)
	}
}

// ToolsListStats returns cache statistics for the tools/list cache.
func (s *Server) ToolsListStats() cache.Stats {
	return s.handler.ToolsListStats()
}

// ContextCostStats returns process-local tool-result byte counters for this gateway.
func (s *Server) ContextCostStats() ContextCostStats {
	return s.handler.ContextCostStats()
}

// InvalidateAndNotifyToolsChanged flushes the tools/list cache and sends
// a tools/list_changed notification to the connected client.
func (s *Server) InvalidateAndNotifyToolsChanged() {
	s.handler.InvalidateAndNotifyToolsChanged()
}

// Notify sends a JSON-RPC notification (no id field) to the client.
func (s *Server) Notify(method string, params any) error {
	if s.w == nil {
		return fmt.Errorf("server not running")
	}

	notif := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(notif)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.w.Write(data)
	return err
}

// runKeepalive sends periodic ping notifications to detect stale connections.
// It tolerates up to maxKeepaliveFailures consecutive failures before closing,
// so transient issues (system sleep, I/O backpressure) don't kill the session.
func (s *Server) runKeepalive(done <-chan struct{}, r io.Reader) {
	const maxFailures = 3

	ticker := time.NewTicker(s.keepaliveInterval)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := s.Notify("notifications/ping", nil); err != nil {
				failures++
				slog.Warn("keepalive ping failed",
					"error", err,
					"consecutive_failures", failures,
					"max", maxFailures,
				)
				if failures >= maxFailures {
					slog.Warn("keepalive threshold exceeded, closing stale connection")
					if c, ok := r.(io.Closer); ok {
						_ = c.Close()
					}
					return
				}
			} else {
				failures = 0
			}
		}
	}
}

func (s *Server) writeResponse(w io.Writer, resp *Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
