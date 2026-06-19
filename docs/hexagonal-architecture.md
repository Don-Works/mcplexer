# Hexagonal Architecture (Ports & Adapters) for mcplexer

> **Status**: Design document — not implementation.
> **Scope**: Decouple inputs (events arriving) from core domain (task/mesh/memory/worker) from outputs (responses going out).

---

## Table of Contents

1. [Current State Map](#1-current-state-map)
2. [Port & Adapter Design](#2-port--adapter-design)
3. [Migration Plan](#3-migration-plan)
4. [New Integration Template](#4-new-integration-template)
5. [Impact on Existing Subsystems](#5-impact-on-existing-subsystems)
6. [Testing Strategy](#6-testing-strategy)
7. [Backward Compatibility](#7-backward-compatibility)

---

## 1. Current State Map

### 1.1 Telegram (`internal/telegram/`)

**Input path** (inbound):
- **Trigger**: Telegram long-poll via `Client.Run()` → `models.Message` / `models.CallbackQuery`
- **Parse**: `ParseMessage()` / `ParseCallback()` normalises raw Telegram models into `IncomingMessage` (a local struct with Platform, ChatNativeID, Text, PairingCode, etc.)
- **Routing**: `DecideInbound()` is a pure function that maps `IncomingMessage` + `RoutingConfig` → `RoutingDecision` (skip or `mesh.SendRequest`)
- **Dispatch**: `Manager.handleInbound()` calls `mesh.Send()` to inject the message into the core mesh
- **Coupling**: Tightly coupled to `mesh.SessionMeta`, `mesh.SendRequest`, `store.Store` for chat lookup/pairing, `notify.Bus` for outbound. The `IncomingMessage` type is local to the telegram package.

**Output path** (outbound):
- **Trigger**: `notify.Bus` subscription → `notify.Event`
- **Dispatch**: `handleNotify()` looks up workspace → active chats → filters by priority → calls `client.Send()` / `client.EditMessage()`
- **Also**: `SendByChatID()` and `BroadcastWorkspace()` for direct MCP-tool-driven sends
- **Coupling**: Directly reads `store.GetMeshMessage()` for reply-to resolution, `store.ListActiveTelegramChatsByWorkspace()` for routing. The thinking-placeholder cache is tightly woven into the outbound path.

**Tightly coupled**:
- `Manager.handleInbound()` calls `mesh.Send()` directly — no abstraction boundary
- `Manager.handleNotify()` calls `store.GetMeshMessage()` directly for reply_to resolution
- `IncomingMessage` / `OutgoingMessage` types are platform-specific structs with no shared supertype
- Pairing logic (`ConsumePairing()`) is embedded in the Manager

**Loosely coupled**:
- `MeshSender` interface (narrow: `Send(ctx, meta, req)`)
- `DecideInbound()` is a pure function — easily testable
- `Client` is a concrete HTTP wrapper — could be interface-extracted

### 1.2 Google Chat (`internal/googlechat/`)

**Input path** (inbound):
- **Trigger**: HTTP webhook pushes `IncomingMessage` onto `Manager.Inbound()` channel
- **Dispatch**: `handleMessage()` → `DecideInbound()` → `mesh.Send()`
- **Coupling**: Nearly identical pattern to Telegram but with its own `IncomingMessage` type, its own `RoutingConfig`, its own `DecideInbound()`, and its own `MeshSender` interface (duplicated definition).

**Output path** (outbound):
- **Trigger**: `notify.Bus` subscription → `handleNotify()`
- **Dispatch**: workspace lookup → active spaces → priority filter → `client.Send()`
- **Coupling**: Same store-reading pattern as Telegram for workspace resolution.

**Tightly coupled**: Same as Telegram — direct `mesh.Send()`, direct store reads, duplicated `MeshSender` interface.

**Loosely coupled**: Same pattern — `DecideInbound()` is pure, `Client` is a thin HTTP wrapper.

### 1.3 Worker Runner (`internal/workers/runner/`)

**Input path** (triggers):
- **Schedule**: `Scheduler` calls `WorkerExecutor.Run(workerID)` → `Runner.RunWithOpts()`
- **Mesh trigger**: Gateway detects mesh message matching a worker's trigger config → calls `Runner.RunWithOpts()` with `RunOpts.TriggerKind="mesh"`
- **Manual**: Admin UI / MCP tool calls `Runner.RunWithOpts()` with `RunOpts.TriggerKind="manual"`

**Core loop**: `Runner.runLoop()` drives model ↔ tool iteration bounded by `Caps`. Dependencies are all interface-injected via `Deps`:
- `SecretReader` — reads API keys
- `SkillReader` — fetches skill bodies
- `ToolDispatcher` — lists + dispatches tool calls
- `MeshSender` — emits lifecycle signals (started/finished/tool_call)
- `Auditor` — writes audit records
- `Clock` — time abstraction

**Output path** (channels):
- `emitOutputs()` fans run output to every configured `outputChannel`
- `dispatchChannel()` is a **hardcoded switch** over `ch.Type`:
  - `"mesh"` → `emitMeshOutput()` → `MeshSender.Send()`
  - `"file"` → `writeFileOutput()` → filesystem
  - `"webhook"` → `emitWebhookOutput()` → HTTP POST
  - `"slack_webhook"` → `emitSlackWebhookOutput()` → Slack-shaped HTTP POST
  - `"clickup_task"` → `emitClickUpTaskOutput()` → ClickUp API
  - `"github_issue"` → `emitGitHubIssueOutput()` → GitHub API

**Tightly coupled**:
- `dispatchChannel()` switch is the main coupling point — adding a new output type requires modifying the runner
- Each output implementation (`output_webhook.go`, `output_slack.go`, `output_clickup.go`, `output_github.go`) is a package-level function taking `outputContext` + `outputChannel`
- `outputContext` is a 16-field struct bundling everything any channel might need

**Loosely coupled**:
- The `Deps` struct with its interfaces is textbook hexagonal — the runner never imports `internal/mesh`, `internal/secrets`, or `internal/gateway` directly
- `MeshSender` interface is runner-shaped (`Send(ctx, MeshOutbound)`) not mesh-shaped (`Send(ctx, SessionMeta, SendRequest)`)
- `outputChannel` is a wide JSON-tagged struct — each type consumes its subset

### 1.4 Approval System (`internal/approval/`)

**Input path**:
- **Tool call**: Gateway's pre-tool hook calls `Manager.RequestApproval()` → blocks on channel until resolved
- **Shell hook**: `HasAllowMetacharsMatch()` for the shell metachar cheap-block
- **AFK policy**: `PolicyResolver` auto-resolves after grace period

**Output path**:
- `Bus` (SSE) publishes `ApprovalEvent` for dashboard
- `NotifyPublisher` publishes to `notify.Bus` for Signal tray + OS notifications
- `store.ResolveToolApproval()` persists the decision

**Coupling**: The `Manager` depends on `store.ToolApprovalStore` (narrow) and `Bus` (local). The `PolicyResolver` depends on `store.ApprovalRule` and `PeerApprover`. Reasonably well-decomposed.

### 1.5 Scheduler (`internal/scheduler/`)

**Input path**:
- Timer-based: `jobHeap` priority queue → `fire()` → `dispatch()`
- Catch-up: `catchUp()` at boot for missed fires

**Output path**:
- `KindWorker`: calls `WorkerExecutor.Run()` (interface — satisfied by `runner.Runner`)
- `KindAuditPrune`: calls `PruneExecutor`
- Shell kinds: `executeAndApprove()` → `os/exec` + `Approver.RequestApproval()`

**Coupling**: Well-decomposed. `WorkerExecutor`, `WorkerLookup`, `Approver`, `Auditor` are all narrow interfaces. The scheduler is already hexagonal.

### 1.6 Task Service (`internal/tasks/`)

**Input path**:
- MCP tool calls (`task__create`, `task__update`, etc.) → `Service.Create()`, `Service.Update()`, etc.
- REST API handlers → same `Service` methods
- Mesh-triggered task events (gossip) → `Service` via `gossip_apply.go`

**Output path**:
- `Bus` (SSE) publishes `Event` for dashboard real-time updates
- `Emitter` publishes `task_event:*` mesh messages for cross-agent visibility
- `BrainHook` dual-writes canonical `.md` files

**Coupling**: The `Service` depends on `store.TaskStore` (narrow), `Bus` (optional), `Emitter` (optional), `BrainHook` (optional). Well-decomposed with post-construction wiring (`SetBus`, `SetEmitter`, `SetBrainHook`).

### 1.7 Mesh (`internal/mesh/`)

**Role**: Core messaging infrastructure. Not an adapter — it IS the hexagon's interior for messaging.

**Input**: `Manager.Send()` / `Manager.Receive()` — called by adapters
**Output**: `notify.Bus` for human-facing notifications, `p2pTransport` for cross-machine delivery, `AgentBroadcaster` for peer gossip

**Coupling**: The `Manager` depends on `store.MeshStore` (narrow), `notify.Bus` (optional), `p2pTransport` (optional). Well-decomposed with many post-construction setters.

### 1.8 Concierge (`internal/concierge/`)

**Role**: Cross-channel chat classification service.

**Input**: `Service.Record()` — called by the concierge worker after each turn
**Output**: `ChatTurnSignalStore` persistence, `Classifier` label assignment

**Coupling**: Very thin. Depends on `store.ChatTurnSignalStore` (narrow). Already hexagonal.

---

## 2. Port & Adapter Design

### 2.1 Core Principle

The hexagon's interior contains:
- **Domain services**: Task, Mesh, Memory, Skill, Approval, Worker
- **Domain events**: The unified `Event` type that flows through the system
- **Port interfaces**: What adapters implement or consume

Adapters sit outside:
- **Input adapters**: Telegram, Google Chat, Scheduler, REST API, MCP tools, Webhooks
- **Output adapters**: Telegram, Google Chat, Slack, ClickUp, GitHub, File, Webhook, Mesh

### 2.2 Unified Event Type (Inbound)

```go
// package domain

// Event represents any inbound stimulus that can trigger domain actions.
// Input adapters produce Events; the Dispatcher routes them to domain
// services and output adapters.
type Event struct {
    // Identity
    ID        string    // ULID, assigned by the input adapter
    Source    string    // "telegram" | "googlechat" | "scheduler" | "rest" | "mcp" | "webhook"
    Timestamp time.Time

    // Content
    Kind     string // "message" | "callback" | "command" | "schedule_tick" | "webhook_payload"
    Content  string // the human-readable payload
    Metadata map[string]any // source-specific metadata (chat_id, space_name, etc.)

    // Routing
    WorkspaceID string
    SessionID   string
    Audience    string // "*" | "role:<name>" | specific session ID
    Priority    string // "critical" | "high" | "normal" | "low"
    Tags        []string
    ReplyTo     string // mesh message ID for threading

    // Authz
    SenderName   string
    SenderRole   string
    IsAuthenticated bool
    PairingCode  string // non-empty = pairing request
}
```

### 2.3 Unified Action Type (Outbound)

```go
// package domain

// Action represents an outbound effect that an output adapter delivers.
// The Dispatcher produces Actions; output adapters consume them.
type Action struct {
    // Identity
    ID        string // ULID
    Source    string // which domain service produced this
    Timestamp time.Time

    // Content
    Kind     string // "message" | "alert" | "finding" | "event" | "task_update"
    Content  string // the human-readable payload
    Title    string // optional headline
    Metadata map[string]any // action-specific metadata

    // Routing
    Target       ActionTarget // where to deliver
    Priority     string
    Tags         []string
    ReplyTo      string // thread linkage
    NotifyUser   bool   // should this ping the human?

    // Run context (for worker output actions)
    WorkerID     string
    RunID        string
    Status       string
    CostUSD      float64
    InputTokens  int
    OutputTokens int
}

// ActionTarget describes where an Action should be delivered.
type ActionTarget struct {
    // Channel selects the output adapter family.
    Channel string // "telegram" | "googlechat" | "slack" | "clickup" | "github" | "webhook" | "file" | "mesh"

    // Channel-specific routing fields. Each adapter reads its subset.
    ChatID        string // telegram chat ID
    SpaceID       string // googlechat space ID
    SlackChannel  string // slack #channel
    SlackWebhook  string // slack incoming webhook URL
    ClickUpListID string
    GitHubRepo    string // owner/repo
    WebhookURL    string
    FilePath      string
    MeshAudience  string
    ToPeer        string // libp2p peer ID for cross-machine

    // Headers for HTTP-based adapters
    Headers map[string]string

    // Mode for file adapter
    Mode string // "append" | "overwrite"

    // SecretScopeID for adapters that need API tokens
    SecretScopeID string

    // IncludeMetadata for webhook adapter
    IncludeMetadata bool
}
```

### 2.4 Input Port Interface

```go
// package domain

// InputPort is what every input adapter implements to feed Events into
// the hexagon. The adapter owns the transport (long-poll, webhook,
// timer, MCP tool call) and normalises platform-specific payloads into
// domain.Events.
type InputPort interface {
    // Name returns the adapter's identifier (e.g. "telegram", "scheduler").
    Name() string

    // Run starts the adapter's event loop. It pushes Events onto the
    // provided channel. Returns when ctx is cancelled.
    Run(ctx context.Context, out chan<- Event) error

    // Stop gracefully shuts down the adapter.
    Stop() error
}

// PairingInputPort is an optional extension for adapters that support
// device/space pairing (Telegram, Google Chat).
type PairingInputPort interface {
    InputPort
    ConsumePairing(ctx context.Context, platform, code string) (workspaceID string, err error)
}
```

### 2.5 Output Port Interface

```go
// package domain

// OutputPort is what every output adapter implements to deliver Actions
// to external systems.
type OutputPort interface {
    // Channel returns the adapter's channel identifier (e.g. "telegram",
    // "slack_webhook", "webhook").
    Channel() string

    // Deliver sends one Action to the external system. Returns an error
    // on delivery failure; the Dispatcher handles retry/alerting.
    Deliver(ctx context.Context, action Action) error

    // CanDeliver reports whether this adapter can handle the given Action
    // (e.g. has a configured client, valid target). Used by the
    // Dispatcher for routing decisions.
    CanDeliver(action Action) bool
}

// BroadcastOutputPort is an optional extension for adapters that support
// workspace-wide broadcast (Telegram, Google Chat).
type BroadcastOutputPort interface {
    OutputPort
    BroadcastWorkspace(ctx context.Context, workspaceID, content, priority string) (int, error)
}

// DirectOutputPort is an optional extension for adapters that support
// targeted sends by ID (Telegram chat ID, Google Chat space ID).
type DirectOutputPort interface {
    OutputPort
    SendByID(ctx context.Context, id, content, priority string) error
}
```

### 2.6 Event Router (Inbound → Domain)

```go
// package domain

// EventRouter classifies inbound Events and routes them to the
// appropriate domain service. It is the "use case" layer in hexagonal
// terms — it orchestrates domain operations in response to events.
type EventRouter struct {
    taskService   *tasks.Service
    meshManager   *mesh.Manager
    workerRunner  *runner.Runner
    approvalMgr   *approval.Manager
    scheduler     *scheduler.Scheduler
    dispatcher    *ActionDispatcher
}

// Route processes one Event. It:
// 1. Authenticates/authorises the event source
// 2. Classifies the event (pairing, message, command, schedule)
// 3. Invokes the appropriate domain service
// 4. Optionally produces Actions for output adapters
func (r *EventRouter) Route(ctx context.Context, evt Event) error {
    // Pairing events
    if evt.PairingCode != "" {
        return r.handlePairing(ctx, evt)
    }

    // Command events (task create, mesh send, etc.)
    if evt.Kind == "command" {
        return r.handleCommand(ctx, evt)
    }

    // Message events → mesh
    if evt.Kind == "message" || evt.Kind == "callback" {
        return r.handleMessage(ctx, evt)
    }

    // Schedule ticks → worker dispatch
    if evt.Kind == "schedule_tick" {
        return r.handleScheduleTick(ctx, evt)
    }

    return fmt.Errorf("unknown event kind: %s", evt.Kind)
}
```

### 2.7 Action Dispatcher (Domain → Outbound)

```go
// package domain

// ActionDispatcher routes Actions to the appropriate OutputPort(s).
// It replaces the hardcoded switch in runner.dispatchChannel().
type ActionDispatcher struct {
    ports map[string]OutputPort // keyed by Channel()
    rules []RoutingRule
}

// RoutingRule maps an Action pattern to one or more output channels.
type RoutingRule struct {
    // Match conditions (all must be true)
    SourcePattern  string // glob pattern on Action.Source
    KindPattern    string // glob pattern on Action.Kind
    TagIncludes    []string
    PriorityMin    string

    // Target channels
    Channels []string
}

// Dispatch sends an Action to all matching output ports.
func (d *ActionDispatcher) Dispatch(ctx context.Context, action Action) error {
    channels := d.resolveChannels(action)
    var errs []error
    for _, ch := range channels {
        port, ok := d.ports[ch]
        if !ok {
            errs = append(errs, fmt.Errorf("no output port for channel: %s", ch))
            continue
        }
        if !port.CanDeliver(action) {
            continue
        }
        if err := port.Deliver(ctx, action); err != nil {
            errs = append(errs, fmt.Errorf("deliver to %s: %w", ch, err))
        }
    }
    return errors.Join(errs...)
}

// resolveChannels determines which output channels should receive this
// Action based on routing rules and the Action's own Target.Channel
// field (explicit routing wins over rules).
func (d *ActionDispatcher) resolveChannels(action Action) []string {
    if action.Target.Channel != "" {
        return []string{action.Target.Channel}
    }
    var channels []string
    for _, rule := range d.rules {
        if d.ruleMatches(rule, action) {
            channels = append(channels, rule.Channels...)
        }
    }
    return channels
}
```

### 2.8 Event Bus (Internal Pub/Sub)

```go
// package domain

// EventBus is the internal pub/sub that decouples domain services from
// output adapters. Domain services publish Events; the ActionDispatcher
// subscribes and routes to OutputPorts.
//
// This replaces the current notify.Bus with a richer, domain-aware
// event model.
type EventBus struct {
    subscribers map[string][]chan Action
    mu          sync.RWMutex
}

// Publish sends an Action to all subscribers of its Kind.
func (b *EventBus) Publish(action Action) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subscribers[action.Kind] {
        select {
        case ch <- action:
        default:
            // drop on full — best-effort delivery
        }
    }
}

// Subscribe returns a channel that receives Actions of the given kinds.
func (b *EventBus) Subscribe(kinds ...string) <-chan Action {
    ch := make(chan Action, 64)
    b.mu.Lock()
    defer b.mu.Unlock()
    for _, k := range kinds {
        b.subscribers[k] = append(b.subscribers[k], ch)
    }
    return ch
}
```

### 2.9 Complete Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                        INPUT ADAPTERS                               │
│                                                                     │
│  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌──────┐  ┌────────┐  │
│  │ Telegram  │  │ Google    │  │Scheduler │  │ REST │  │ MCP    │  │
│  │ (long-    │  │ Chat      │  │ (timer)  │  │ API  │  │ Tools  │  │
│  │  poll)    │  │ (webhook) │  │          │  │      │  │        │  │
│  └────┬─────┘  └─────┬─────┘  └────┬─────┘  └──┬───┘  └───┬────┘  │
│       │              │              │            │          │        │
│       │   domain.Event (unified)    │            │          │        │
│       └──────────────┴──────────────┴────────────┴──────────┘        │
│                               │                                     │
│                               ▼                                     │
│                    ┌──────────────────┐                              │
│                    │   EventRouter    │                              │
│                    │  (use-case layer)│                              │
│                    └────────┬─────────┘                              │
│                             │                                       │
│              ┌──────────────┼──────────────┐                        │
│              ▼              ▼              ▼                         │
│     ┌──────────────┐ ┌──────────┐ ┌──────────────┐                  │
│     │ Task Service │ │  Mesh    │ │ Worker Runner│                  │
│     │              │ │ Manager  │ │              │                  │
│     └──────┬───────┘ └────┬─────┘ └──────┬───────┘                  │
│            │              │              │                           │
│            │   domain.Action (unified)   │                           │
│            └──────────────┴──────────────┘                           │
│                             │                                       │
│                             ▼                                       │
│                    ┌──────────────────┐                              │
│                    │ ActionDispatcher │                              │
│                    │  (routing rules) │                              │
│                    └────────┬─────────┘                              │
│                             │                                       │
│       ┌─────────────────────┼─────────────────────┐                 │
│       ▼                     ▼                     ▼                  │
│  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌──────────┐           │
│  │ Telegram │  │ Google    │  │  Slack   │  │ ClickUp  │  ...      │
│  │ Output   │  │ Chat      │  │ Webhook  │  │  Task    │           │
│  │ Port     │  │ Output    │  │ Port     │  │  Port    │           │
│  └──────────┘  └───────────┘  └──────────┘  └──────────┘           │
│                                                                     │
│                     OUTPUT ADAPTERS                                  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 3. Migration Plan

### 3.1 Phase 1: Extract Output Ports from Worker Runner

The runner's `dispatchChannel()` switch is the highest-value refactoring target — it's where new output types require modifying core code.

**Step 1.1**: Define the `OutputPort` interface in `internal/domain/ports.go`.

**Step 1.2**: Extract each output channel into an `OutputPort` implementation:

| Current file | New package | Implements |
|---|---|---|
| `output.go:emitMeshOutput` | `internal/adapters/output/mesh` | `OutputPort` |
| `output.go:writeFileOutput` | `internal/adapters/output/file` | `OutputPort` |
| `output_webhook.go` | `internal/adapters/output/webhook` | `OutputPort` |
| `output_slack.go` | `internal/adapters/output/slack` | `OutputPort` |
| `output_clickup.go` | `internal/adapters/output/clickup` | `OutputPort` |
| `output_github.go` | `internal/adapters/output/github` | `OutputPort` |

**Step 1.3**: Replace the `dispatchChannel()` switch with an `ActionDispatcher` lookup:

```go
// Before (runner/output.go)
func (r *Runner) dispatchChannel(ctx context.Context, octx outputContext, ch outputChannel) string {
    switch ch.Type {
    case "mesh": ...
    case "file": ...
    case "webhook": ...
    }
}

// After
func (r *Runner) dispatchChannel(ctx context.Context, octx outputContext, ch outputChannel) string {
    action := octx.toAction(ch) // convert outputContext + outputChannel → domain.Action
    port := r.dispatcher.Port(ch.Type)
    if port == nil {
        slog.Warn("unknown output channel type", "type", ch.Type)
        return ""
    }
    if err := port.Deliver(ctx, action); err != nil {
        r.reportChannelError(ctx, octx, ch.Type, err)
    }
    return action.ID
}
```

**Step 1.4**: Keep backward compatibility — the `outputChannel` JSON schema and `OutputChannelsJSON` field on `store.Worker` stay identical. The adapter registration is wired at daemon startup.

**Dependencies**: None — this is a pure internal refactoring of the runner.

### 3.2 Phase 2: Unify Chat Input Adapters

**Step 2.1**: Define `domain.Event` and `domain.InputPort`.

**Step 2.2**: Refactor Telegram:

```go
// internal/adapters/input/telegram/adapter.go

type Adapter struct {
    manager *telegram.Manager
    out     chan<- domain.Event
}

func (a *Adapter) Name() string { return "telegram" }

func (a *Adapter) Run(ctx context.Context, out chan<- domain.Event) error {
    a.out = out
    // Manager.Run() already has an inbound channel; we bridge it
    return a.manager.Run(ctx)
}

// The Manager's handleInbound() is modified to push Events:
func (a *Adapter) onInbound(msg telegram.IncomingMessage) {
    a.out <- domain.Event{
        ID:          newULID(),
        Source:      "telegram",
        Kind:        "message",
        Content:     msg.Text,
        WorkspaceID: "...", // from chat lookup
        SessionID:   msg.Platform + "-" + msg.ChatNativeID,
        Metadata: map[string]any{
            "platform":        msg.Platform,
            "chat_native_id":  msg.ChatNativeID,
            "chat_type":       msg.ChatType,
            "callback_data":   msg.CallbackData,
        },
        PairingCode: msg.PairingCode,
        SenderName:  msg.SenderName,
    }
}
```

**Step 2.3**: Refactor Google Chat identically.

**Step 2.4**: Extract shared chat-adapter concerns:
- Pairing logic → `internal/domain/pairing/` (shared service)
- Priority filtering → `internal/domain/priority/` (shared helper)
- Reply-to resolution → `internal/domain/threading/` (shared service)

**Dependencies**: Phase 1 (the `domain` package exists).

### 3.3 Phase 3: Refactor Chat Output Adapters

**Step 3.1**: Refactor Telegram outbound as an `OutputPort`:

```go
// internal/adapters/output/telegram/port.go

type Port struct {
    manager *telegram.Manager
}

func (p *Port) Channel() string { return "telegram" }

func (p *Port) Deliver(ctx context.Context, action domain.Action) error {
    if action.Target.ChatID != "" {
        return p.manager.SendByChatID(ctx, action.Target.ChatID, action.Content, action.Priority)
    }
    return p.manager.BroadcastWorkspace(ctx, action.WorkspaceID, action.Content, action.Priority)
}

func (p *Port) CanDeliver(action domain.Action) bool {
    return p.manager.HasClient()
}
```

**Step 3.2**: Refactor Google Chat outbound identically.

**Step 3.3**: Wire the thinking-placeholder cache as a decorator:

```go
// internal/adapters/output/telegram/thinking_decorator.go

type ThinkingDecorator struct {
    inner   OutputPort
    cache   *thinkingCache
    client  *telegram.Client
}

func (d *ThinkingDecorator) Deliver(ctx context.Context, action domain.Action) error {
    // Post thinking placeholder before first chunk
    // Edit placeholder when reply arrives
    return d.inner.Deliver(ctx, action)
}
```

**Dependencies**: Phase 1 + Phase 2.

### 3.4 Phase 4: EventRouter + ActionDispatcher

**Step 4.1**: Implement `EventRouter` that replaces the current per-adapter `DecideInbound()` + `handleInbound()` pattern.

**Step 4.2**: Implement `ActionDispatcher` that replaces the runner's `dispatchChannel()` switch AND the per-adapter `handleNotify()` pattern.

**Step 4.3**: Wire at daemon startup:

```go
// cmd/mcplexer/main.go (simplified)

// Input adapters
telegramIn := inputtelegram.NewAdapter(telegramMgr)
gchatIn := inputgchat.NewAdapter(gchatMgr)
schedulerIn := inputscheduler.NewAdapter(sched)

// Output ports
telegramOut := outputtelegram.NewPort(telegramMgr)
gchatOut := outputgchat.NewPort(gchatMgr)
slackOut := outputslack.NewPort(httpClient)
webhookOut := outputwebhook.NewPort(httpClient)
meshOut := outputmesh.NewPort(meshMgr)
fileOut := outputfile.NewPort(outputsDir)

// Router + Dispatcher
router := domain.NewEventRouter(taskSvc, meshMgr, workerRunner, approvalMgr)
dispatcher := domain.NewActionDispatcher(map[string]domain.OutputPort{
    "telegram":      telegramOut,
    "googlechat":    gchatOut,
    "slack_webhook": slackOut,
    "webhook":       webhookOut,
    "mesh":          meshOut,
    "file":          fileOut,
}, routingRules)

// Start input adapters
for _, in := range []domain.InputPort{telegramIn, gchatIn, schedulerIn} {
    go in.Run(ctx, router.EventChannel())
}
```

**Dependencies**: Phases 1–3.

### 3.5 Migration Order

| Phase | Effort | Risk | Value | Order |
|---|---|---|---|---|
| 1. Output ports from runner | Medium | Low | High | First — no external API changes |
| 2. Chat input adapters | Medium | Medium | Medium | Second — enables new input sources |
| 3. Chat output adapters | Low | Low | Medium | Third — completes chat decoupling |
| 4. Router + Dispatcher | High | Medium | High | Last — ties everything together |

---

## 4. New Integration Template

### 4.1 Adding an Email Webhook Input Adapter

**Step 1**: Create the adapter package.

```
internal/adapters/input/email/
  adapter.go    — implements domain.InputPort
  parse.go      — normalises inbound email → domain.Event
  types.go      — email-specific types
```

**Step 2**: Implement `InputPort`:

```go
// internal/adapters/input/email/adapter.go

package email

import "github.com/don-works/mcplexer/internal/domain"

type Adapter struct {
    webhookPort int
    out         chan<- domain.Event
}

func NewAdapter(port int) *Adapter {
    return &Adapter{webhookPort: port}
}

func (a *Adapter) Name() string { return "email" }

func (a *Adapter) Run(ctx context.Context, out chan<- domain.Event) error {
    a.out = out
    // Start HTTP server on a.webhookPort
    // Parse inbound email webhook payloads
    // Push normalised Events onto `out`
    return httpServer.ListenAndServe()
}

func (a *Adapter) Stop() error {
    return httpServer.Shutdown(context.Background())
}
```

**Step 3**: Define the parse function:

```go
// internal/adapters/input/email/parse.go

func ParseWebhook(payload EmailWebhookPayload) (domain.Event, error) {
    return domain.Event{
        ID:       ulid.Make().String(),
        Source:   "email",
        Kind:     "message",
        Content:  payload.BodyText,
        Metadata: map[string]any{
            "from":    payload.From,
            "to":      payload.To,
            "subject": payload.Subject,
            "headers": payload.Headers,
        },
        SenderName: payload.From,
    }, nil
}
```

**Step 4**: Register in the daemon startup:

```go
emailIn := email.NewAdapter(8080)
go emailIn.Run(ctx, router.EventChannel())
```

**Step 5**: Add routing rules so the EventRouter knows what to do with email events:

```go
// In EventRouter configuration
routingRules = append(routingRules, RoutingRule{
    Source: "email",
    Action: "mesh_broadcast", // send to mesh with audience="*"
    OutputChannels: []string{"telegram", "slack_webhook"}, // also forward to these
})
```

### 4.2 Adding a Slack Output Adapter

**Step 1**: Create the adapter package.

```
internal/adapters/output/slack/
  port.go       — implements domain.OutputPort
  format.go     — Slack-specific message formatting
```

**Step 2**: Implement `OutputPort`:

```go
// internal/adapters/output/slack/port.go

type Port struct {
    httpClient *http.Client
}

func (p *Port) Channel() string { return "slack_webhook" }

func (p *Port) Deliver(ctx context.Context, action domain.Action) error {
    payload := buildSlackPayload(action)
    body, _ := json.Marshal(payload)
    req, _ := http.NewRequestWithContext(ctx, "POST", action.Target.SlackWebhook, bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        return fmt.Errorf("slack: http %d", resp.StatusCode)
    }
    return nil
}

func (p *Port) CanDeliver(action domain.Action) bool {
    return action.Target.SlackWebhook != ""
}
```

**Step 3**: Register in daemon startup:

```go
slackOut := &slack.Port{httpClient: httpClient}
dispatcher.Register("slack_webhook", slackOut)
```

### 4.3 Adding a New Output Channel to Workers

With the hexagonal architecture, adding a new worker output channel requires:

1. Implement `domain.OutputPort` in a new package
2. Register it with the `ActionDispatcher`
3. Add the channel type name to `outputChannel.Type` enum documentation
4. **No changes to the runner itself**

```go
// Worker config JSON — the existing format still works
{
  "output_channels": [
    {"type": "discord_webhook", "url": "https://discord.com/api/webhooks/...", "priority": "normal"}
  ]
}
```

---

## 5. Impact on Existing Subsystems

### 5.1 `mcpx__provision_mcp` — Should It Become an Output Port?

**Current state**: `mcpx__provision_mcp` is a gateway admin tool that creates/updates MCP server configs. It operates on `~/.mcplexer/` directly.

**Recommendation**: **No — keep it as a gateway admin tool.** It's an infrastructure mutation, not an output delivery. The hexagonal pattern is for event → action flow, not for configuration management. However, the *trigger* for provisioning (e.g. "a new Telegram chat was paired → provision a workspace-scoped MCP server") should flow through the EventRouter:

```
Telegram pairing event → EventRouter → domain service provisions MCP → Action{kind: "config_updated"}
```

### 5.2 Worker Delegation — Should It Become an Output Port?

**Current state**: `mcpx__delegate_worker` creates a worker run. The runner's `RunWithOpts()` is the execution engine.

**Recommendation**: **Partially.** The worker *execution* stays in the hexagon's core (it's a domain service). But the worker's *output routing* should use the `ActionDispatcher`:

- **Before**: Runner has a hardcoded switch for each output channel type
- **After**: Runner produces `domain.Action` objects and hands them to the `ActionDispatcher`

The delegation *trigger* (MCP tool call → worker run) already flows through the gateway → runner path. No change needed there.

### 5.3 Task Creation — Should It Accept Events Directly?

**Current state**: `tasks.Service.Create()` takes `CreateOptions` — a structured input with Title, Description, Status, etc.

**Recommendation**: **Yes — add an `EventToTask` converter in the EventRouter.**

```go
// In EventRouter
func (r *EventRouter) handleCommand(ctx context.Context, evt domain.Event) error {
    if evt.Kind == "command" && evt.Metadata["command"] == "task_create" {
        _, err := r.taskService.Create(ctx, tasks.CreateOptions{
            WorkspaceID:        evt.WorkspaceID,
            Title:              evt.Metadata["title"].(string),
            SourceKind:         evt.Source,
            SourceSessionID:    evt.SessionID,
            CreatedBySessionID: evt.SessionID,
        })
        return err
    }
    // ...
}
```

This is additive — existing MCP tool calls continue to use `CreateOptions` directly. The EventRouter provides a second entry point for event-driven task creation.

### 5.4 Approval System — Should It Gate Event→Action Mappings?

**Current state**: `approval.Manager.RequestApproval()` blocks on tool calls that need human approval. The `PolicyResolver` auto-resolves based on rules.

**Recommendation**: **Yes — the approval system should gate Action delivery for write-class output channels.**

```go
// In ActionDispatcher
func (d *ActionDispatcher) Dispatch(ctx context.Context, action Action) error {
    // Check if this action requires approval
    if d.requiresApproval(action) {
        approved, err := d.approvalMgr.RequestApproval(ctx, &store.ToolApproval{
            Surface:     "output_channel",
            ToolName:    action.Target.Channel,
            Justification: fmt.Sprintf("deliver %s to %s", action.Kind, action.Target.Channel),
        })
        if err != nil || !approved {
            return fmt.Errorf("action delivery denied: %w", err)
        }
    }
    // ... deliver to output ports
}
```

This extends the existing approval model to cover output delivery, not just tool calls. The `PolicyResolver` rules (trusted-allowlist, AFK policy, mesh-peer) apply uniformly.

---

## 6. Testing Strategy

### 6.1 Testing Input Adapters in Isolation

Each input adapter's `Parse*` function is a pure function — test with table-driven tests:

```go
func TestParseWebhook(t *testing.T) {
    tests := []struct {
        name    string
        payload EmailWebhookPayload
        want    domain.Event
        wantErr bool
    }{
        {
            name:    "basic email",
            payload: EmailWebhookPayload{From: "a@b.com", Subject: "Hello", BodyText: "World"},
            want:    domain.Event{Source: "email", Kind: "message", Content: "World"},
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ParseWebhook(tt.payload)
            if (err != nil) != tt.wantErr {
                t.Errorf("ParseWebhook() error = %v, wantErr %v", err, tt.wantErr)
            }
            // Assert on key fields
            if got.Source != tt.want.Source {
                t.Errorf("Source = %v, want %v", got.Source, tt.want.Source)
            }
        })
    }
}
```

### 6.2 Testing Output Adapters in Isolation

Each output adapter's `Deliver()` is tested by injecting a fake HTTP client:

```go
func TestSlackPort_Deliver(t *testing.T) {
    var receivedBody []byte
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(200)
    }))
    defer srv.Close()

    port := &slack.Port{httpClient: srv.Client()}
    action := domain.Action{
        Content: "Hello from worker",
        Target:  domain.ActionTarget{SlackWebhook: srv.URL},
    }
    err := port.Deliver(context.Background(), action)
    if err != nil {
        t.Fatalf("Deliver() error = %v", err)
    }
    // Assert on receivedBody shape
}
```

### 6.3 Testing the EventRouter

The EventRouter is tested by injecting mock domain services:

```go
func TestEventRouter_Route_TelegramMessage(t *testing.T) {
    meshMock := &mockMeshManager{}
    router := domain.NewEventRouter(nil, meshMock, nil, nil)

    evt := domain.Event{
        Source:  "telegram",
        Kind:    "message",
        Content: "Hello bot",
    }
    err := router.Route(context.Background(), evt)
    if err != nil {
        t.Fatalf("Route() error = %v", err)
    }
    if meshMock.sendCount != 1 {
        t.Errorf("expected 1 mesh send, got %d", meshMock.sendCount)
    }
}
```

### 6.4 Testing the ActionDispatcher

```go
func TestActionDispatcher_RoutingRules(t *testing.T) {
    telegramMock := &mockOutputPort{channel: "telegram"}
    slackMock := &mockOutputPort{channel: "slack_webhook"}

    dispatcher := domain.NewActionDispatcher(
        map[string]domain.OutputPort{
            "telegram":      telegramMock,
            "slack_webhook": slackMock,
        },
        []domain.RoutingRule{
            {SourcePattern: "worker", Channels: []string{"telegram", "slack_webhook"}},
        },
    )

    action := domain.Action{Source: "worker.finished", Content: "done"}
    err := dispatcher.Dispatch(context.Background(), action)
    if err != nil {
        t.Fatalf("Dispatch() error = %v", err)
    }
    if telegramMock.deliverCount != 1 {
        t.Errorf("expected telegram delivery, got %d", telegramMock.deliverCount)
    }
    if slackMock.deliverCount != 1 {
        t.Errorf("expected slack delivery, got %d", slackMock.deliverCount)
    }
}
```

### 6.5 Integration Testing

The full pipeline is tested by wiring real adapters with mock external services:

```go
func TestFullPipeline_TelegramToSlack(t *testing.T) {
    // 1. Start Telegram input adapter with a mock Telegram API
    // 2. Wire EventRouter → ActionDispatcher → Slack output port (mock HTTP)
    // 3. Send a Telegram message
    // 4. Assert Slack webhook received the formatted message
}
```

---

## 7. Backward Compatibility

### 7.1 MCP Tool Surface

All existing MCP tools (`task__*`, `mesh__*`, `memory__*`, `mcpx__*`) continue to work unchanged. The hexagonal refactoring is internal — the MCP tool handlers call domain services directly, not through the EventRouter.

### 7.2 Worker Output Channels JSON

The `OutputChannelsJSON` field on `store.Worker` keeps its current schema:

```json
[
  {"type": "mesh", "priority": "normal", "notify_user": true},
  {"type": "telegram", "chat_id": "..."},
  {"type": "webhook", "url": "https://..."},
  {"type": "slack_webhook", "url": "https://..."},
  {"type": "clickup_task", "list_id": "..."},
  {"type": "github_issue", "repo": "owner/repo"},
  {"type": "file", "path": "output.txt"}
]
```

The `ActionDispatcher` maps each `type` value to the registered `OutputPort.Channel()`. New types are added by registering new ports — no schema migration needed.

### 7.3 Notify Bus

The existing `notify.Bus` is replaced by the richer `domain.EventBus` over time. During migration, both coexist:

```go
// Bridge: forward notify.Event → domain.Action for output adapters
notifyCh := notifyBus.Subscribe()
go func() {
    for evt := range notifyCh {
        action := domain.Action{
            Kind:    evt.Kind,
            Content: evt.Body,
            Target:  domain.ActionTarget{Channel: "telegram"}, // default
        }
        actionDispatcher.Dispatch(ctx, action)
    }
}()
```

### 7.4 Mesh Sender Interface

The current `MeshSender` interface in `internal/telegram/` and `internal/googlechat/`:

```go
type MeshSender interface {
    Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error)
}
```

This is replaced by the `domain.InputPort` → `EventRouter` → `mesh.Manager.Send()` path. During migration, the existing interface is preserved as a compatibility shim.

### 7.5 Store Access

Adapters that currently read the store directly (for chat lookup, workspace resolution, reply-to threading) are migrated to go through domain services:

| Current direct store call | Replacement |
|---|---|
| `store.GetTelegramChatByNative()` | `domain.ChatLookup.GetByNativeID()` |
| `store.ListActiveTelegramChatsByWorkspace()` | `domain.ChatLookup.ListActiveByWorkspace()` |
| `store.GetMeshMessage()` | `domain.Threading.ResolveReplyTo()` |
| `store.GetWorkspace()` | `domain.WorkspaceLookup.Get()` |

These are thin service facades over the existing store — no schema changes, no new tables.

---

## Appendix A: File Organisation

```
internal/
  domain/                          # Hexagon interior
    events.go                      # Event type definition
    actions.go                     # Action type definition
    ports.go                       # InputPort, OutputPort interfaces
    router.go                      # EventRouter
    dispatcher.go                  # ActionDispatcher
    rules.go                       # RoutingRule types + matching
    pairing.go                     # Shared pairing service
    threading.go                   # Shared reply-to resolution
    priority.go                    # Shared priority helpers

  adapters/
    input/
      telegram/
        adapter.go                 # InputPort implementation
        parse.go                   # (existing, unchanged)
        types.go                   # (existing, unchanged)
      googlechat/
        adapter.go                 # InputPort implementation
        parse.go                   # (existing, unchanged)
        types.go                   # (existing, unchanged)
      scheduler/
        adapter.go                 # InputPort implementation (wraps scheduler.Scheduler)
      rest/
        adapter.go                 # InputPort implementation (wraps HTTP handlers)
      mcp/
        adapter.go                 # InputPort implementation (wraps MCP tool handlers)

    output/
      telegram/
        port.go                    # OutputPort implementation
        thinking_decorator.go      # Thinking-placeholder decorator
      googlechat/
        port.go                    # OutputPort implementation
      mesh/
        port.go                    # OutputPort implementation (wraps mesh.Manager.Send)
      slack/
        port.go                    # OutputPort implementation
        format.go                  # Slack-specific formatting
      webhook/
        port.go                    # OutputPort implementation
      clickup/
        port.go                    # OutputPort implementation
      github/
        port.go                    # OutputPort implementation
      file/
        port.go                    # OutputPort implementation

  # Existing packages (unchanged or minimally modified)
  mesh/                            # Core — stays as-is
  tasks/                           # Core — stays as-is
  approval/                        # Core — stays as-is
  scheduler/                       # Core — stays as-is
  workers/runner/                  # Core — output routing delegated to ActionDispatcher
  concierge/                       # Core — stays as-is
  store/                           # Core — stays as-is
```

## Appendix B: Interface Summary

| Interface | Location | Implementors | Purpose |
|---|---|---|---|
| `domain.InputPort` | `internal/domain/ports.go` | Telegram, GoogleChat, Scheduler, REST, MCP adapters | Feed events into the hexagon |
| `domain.OutputPort` | `internal/domain/ports.go` | Telegram, GoogleChat, Mesh, Slack, Webhook, ClickUp, GitHub, File adapters | Deliver actions from the hexagon |
| `domain.EventRouter` | `internal/domain/router.go` | Single implementation | Route events to domain services |
| `domain.ActionDispatcher` | `internal/domain/dispatcher.go` | Single implementation | Route actions to output ports |
| `runner.MeshSender` | `internal/workers/runner/deps.go` | Mesh adapter shim | Worker lifecycle signals |
| `runner.SecretReader` | `internal/workers/runner/deps.go` | `secrets.Manager` | API key resolution |
| `runner.SkillReader` | `internal/workers/runner/deps.go` | Skill registry shim | Skill body loading |
| `runner.ToolDispatcher` | `internal/workers/runner/deps.go` | Gateway | Tool listing + dispatch |
| `scheduler.WorkerExecutor` | `internal/scheduler/exec_worker.go` | `runner.Runner` | Worker execution |
| `scheduler.Approver` | `internal/scheduler/scheduler.go` | `approval.Manager` | Tool approval |
| `telegram.MeshSender` | `internal/telegram/manager.go` | `mesh.Manager` | (replaced by InputPort) |
| `googlechat.MeshSender` | `internal/googlechat/manager.go` | `mesh.Manager` | (replaced by InputPort) |
