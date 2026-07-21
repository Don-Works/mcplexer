# MCPlexer Technical Feature Document

**Product:** MCPlexer -- MCP Gateway & Multiplexer
**Vendor:** Don Works
**Repository:** github.com/don-works/mcplexer
**Licence:** AGPL-3.0-or-later, with commercial license exceptions available
**Last Updated:** 2026-06-10

---

## 1. Product Overview

MCPlexer is a Model Context Protocol (MCP) gateway that sits between AI clients
and downstream MCP servers. It acts as a security, routing, and management layer
for every tool call an AI agent makes.

AI clients -- Claude Desktop, Claude Code, Cursor, Windsurf, and any
MCP-compatible agent -- connect to MCPlexer as their single MCP endpoint.
MCPlexer routes each tool call to the correct downstream server based on the
client's working directory, enforces approval policies, injects credentials,
caches responses, and writes a full audit trail.

The shortest description: **direnv for MCP**. The current working directory
determines which tools are available, which credentials are injected, and what
policies apply -- with zero configuration changes between projects.

### Architecture Summary

```
AI Client (Claude Code, Cursor, ...)
        |
        |  MCP (stdio / HTTP / socket)
        v
  +-----------+
  | MCPlexer  |  single Go binary
  |           |  - route resolution
  |           |  - policy enforcement
  |           |  - credential injection
  |           |  - audit logging
  |           |  - caching
  |           |  - approval workflow
  +-----------+
   /    |    \
  v     v     v
 MCP   MCP   MCP    downstream servers
 srv   srv   srv    (GitHub, Linear, filesystem, etc.)
```

### Design Principles

- **Local-first.** MCPlexer runs on the developer's machine. No cloud service,
  no telemetry, no phone-home. Secrets never leave the host.
- **Single binary.** Pure Go, zero CGO dependencies. The web dashboard is
  embedded via `go:embed`. One file to distribute, one file to run.
- **Deny-first.** Route evaluation treats deny rules as dominant. If a deny
  and an allow exist at the same priority, the deny wins. Security defaults
  to closed.
- **AI-native.** MCPlexer exposes its own configuration as MCP tools, so an AI
  agent can introspect and manage the gateway without human intervention.

---

## 2. Directory-Scoped Routing

Directory-scoped routing is the core differentiator. It binds security policy to
the filesystem, so switching between projects automatically switches tool access,
credentials, and approval requirements.

### 2.1 Workspaces

A workspace is a named scope bound to a directory tree via `root_path`.

```yaml
workspaces:
  - name: acme-backend
    root_path: /Users/dev/acme/backend
  - name: personal-blog
    root_path: /Users/dev/personal/blog
```

When an AI client invokes a tool, MCPlexer reads the client's current working
directory and resolves it against the workspace tree.

### 2.2 CWD Resolution

Resolution follows these rules in order:

1. **Exact match.** If the CWD matches a workspace's `root_path` exactly, that
   workspace is selected.
2. **Longest prefix match.** If the CWD is a subdirectory of multiple
   workspaces, the workspace with the longest matching prefix wins. This means
   the most specific workspace always takes precedence.
3. **Ancestor fallback.** If no workspace matches the CWD directly, MCPlexer
   walks up the directory tree looking for a parent directory that matches a
   workspace. This allows workspaces to cover entire directory subtrees.

In stdio mode, the CWD is obtained via `os.Getwd()`, which reads the process's
working directory from the operating system. The AI client cannot spoof this
value -- it is tamper-proof by design.

### 2.3 Subpath Routing

Within a workspace, route rules can be further scoped to subdirectories using
glob patterns:

```yaml
routes:
  - workspace: acme-backend
    subpath: "src/**"
    tools: ["github__*"]
    action: allow
  - workspace: acme-backend
    subpath: "migrations/**"
    tools: ["db__migrate", "db__rollback"]
    action: allow
```

This allows fine-grained control: a developer working in `src/` has access to
GitHub tools, while database migration tools are only available when working in
the `migrations/` directory.

### 2.4 Route Evaluation

Route rules are evaluated using a deny-first, priority-ordered system.

**Priority levels:**

| Range   | Intended Use          |
|---------|-----------------------|
| 1-10    | Critical / security   |
| 10-50   | Project-specific      |
| 50-100  | Standard / team       |
| 100+    | Fallback / defaults   |

**Evaluation order:**

1. All matching routes are collected (workspace match + subpath match + tool
   pattern match).
2. Routes are sorted by priority (ascending -- lower number = higher priority).
3. At each priority level, deny rules dominate. If both a deny and an allow
   exist at the same priority for the same tool, the deny wins.
4. The first decisive match (allow or deny) determines the outcome.

**Tool pattern matching** supports three forms:

- **Exact match:** `github__create_issue` -- matches one specific tool.
- **Prefix wildcard:** `github__*` -- matches all tools from the `github`
  namespace.
- **Infix wildcard:** `*__list_*` -- matches any tool from any namespace that
  contains `list_` in the tool name.

### 2.5 Route Resolution Cache

Resolved routes are cached for **30 seconds**. This avoids re-evaluating the
full rule set on every tool call while still responding to configuration changes
within a reasonable window.

The cache key is the tuple of (workspace_id, subpath, tool_name). Cache entries
are evicted on configuration change or after TTL expiry.

---

## 3. Tool Namespacing

Every tool exposed through MCPlexer is prefixed with a namespace derived from
the downstream server's identifier, using a double-underscore separator:

```
{namespace}__{toolname}
```

Examples:

- `github__create_issue`
- `linear__list_issues`
- `filesystem__read_file`
- `data__ingest` / `data__query` / `data__search` for workspace-scoped
  scratch datasets; see [Data Workbench](data-workbench.md)

### 3.1 Collision Prevention

Without namespacing, multiple MCP servers that expose tools with the same name
(e.g., `search`, `list`) would collide. Namespacing guarantees uniqueness across
any combination of downstream servers.

### 3.2 Namespace Awareness

Route rules and approval policies reference tools by their namespaced name. This
means policies are unambiguous -- `github__delete_repo` and
`gitlab__delete_repo` can have entirely different approval requirements.

---

## 4. Tool Approvals (Human-in-the-Loop)

MCPlexer implements a full human-in-the-loop approval workflow for sensitive tool
calls. This prevents AI agents from executing destructive or high-risk operations
without explicit human authorization.

### 4.1 Approval Flow

The approval lifecycle follows a two-phase pattern:

```
Tool call arrives
       |
       v
  Route matched → approval_required: true?
       |                    |
       no                  yes
       |                    |
       v                    v
  Execute              Agent provides justification
  immediately               |
                             v
                        Request enters approval queue
                        (state: PENDING)
                             |
                             v
                        SSE event pushed to dashboard
                             |
                     +-------+-------+
                     |       |       |
                     v       v       v
                  APPROVED DENIED  TIMEOUT
                     |       |       |
                     v       v       v
                  Execute  Return  Return
                  tool     error   error
```

### 4.2 Justification Requirement

When a route rule has `approval_required: true`, the AI agent must provide a
natural-language justification for the tool call. This justification is recorded
in the audit log and displayed to the human reviewer in the dashboard.

### 4.3 Self-Approval Prevention

An approval request cannot be approved from the same session that created it.
This prevents an AI agent from approving its own requests. A different session --
typically a human using the web dashboard -- must approve or deny the request.

### 4.4 Configurable Timeouts

Each route rule can specify an approval timeout. The default is **300 seconds**
(5 minutes). If no human acts within the timeout, the request transitions to
the TIMEOUT state and the tool call fails with an error returned to the agent.

### 4.5 Approval States

| State      | Description                                          |
|------------|------------------------------------------------------|
| `pending`  | Awaiting human review                                |
| `approved` | Human approved; tool call will execute               |
| `denied`   | Human denied; error returned to agent                |
| `timeout`  | No action within timeout; error returned to agent    |
| `cancelled`| Request cancelled (e.g., agent disconnected)         |

### 4.6 Real-Time Dashboard Integration

Approval requests are streamed to the web dashboard via Server-Sent Events (SSE).
The dashboard displays the approval queue with:

- Tool name and namespace
- Calling workspace and session
- Agent-provided justification
- Parameter summary (with redaction applied)
- Approve / Deny buttons
- Time remaining before timeout

### 4.7 MCP Tools for Approval Management

MCPlexer exposes approval management as MCP tools, enabling AI-driven approval
workflows:

- `mcpx__list_pending_approvals` -- list all pending approval requests
- `mcpx__approve_tool_call` -- approve a pending request (from a different session)
- `mcpx__deny_tool_call` -- deny a pending request

### 4.8 Approval Metrics

The system tracks and exposes:

- **Pending count:** number of requests currently awaiting review
- **Approval rate:** percentage of requests approved vs. denied
- **Average wait time:** mean time between request creation and human action

These metrics are available via the dashboard and the REST API.

---

## 5. Authentication & Credential Injection

MCPlexer manages credentials centrally and injects them into downstream MCP
server connections automatically. Credentials are scoped, encrypted, and
redacted from logs.

### 5.1 Auth Scope Types

Three auth scope types cover the full spectrum of credential patterns:

| Type     | Use Case                              | Injection Point       |
|----------|---------------------------------------|-----------------------|
| `env`    | API keys, tokens                      | Environment variables |
| `header` | Bearer tokens, custom headers         | HTTP headers          |
| `oauth2` | OAuth 2.0 flows with user consent     | Token injection       |

### 5.2 Environment Variable Scopes

Environment variable scopes inject credentials as environment variables into the
downstream server's process. This is the most common pattern for API key-based
services:

```yaml
auth_scopes:
  - name: github-token
    type: env
    env:
      GITHUB_TOKEN: "<github-token>"
```

### 5.3 HTTP Header Scopes

Header scopes inject credentials as HTTP headers for HTTP-based downstream
servers:

```yaml
auth_scopes:
  - name: api-auth
    type: header
    headers:
      Authorization: "Bearer <api-token>"
```

### 5.4 OAuth 2.0 Scopes

OAuth scopes handle the full OAuth 2.0 authorization code flow with PKCE:

- Browser-based authorization redirect
- Code exchange with PKCE verification
- Token storage (encrypted at rest)
- Automatic token refresh before expiry

### 5.5 Built-In OAuth Provider Templates

MCPlexer ships with pre-configured OAuth provider templates for common services:

| Provider    | Scopes Supported                      |
|-------------|---------------------------------------|
| GitHub      | repo, read:org, workflow, etc.        |
| GitLab      | api, read_user, read_repository, etc. |
| Google      | Workspace, Cloud, Drive, etc.         |
| Notion      | Read/write pages, databases           |
| Linear      | Issues, projects, teams               |
| ClickUp     | Tasks, spaces, lists                  |
| Microsoft   | Graph API, Azure DevOps               |
| Vercel      | Deployments, projects, teams          |

Each template pre-fills the authorization URL, token URL, and default scopes. Adding new servers is straightforward. Configure a downstream server definition with a namespace, transport, and optional auth scope. MCPlexer handles discovery, lifecycle, and credential injection automatically.
The user only needs to provide their client ID and client secret.

### 5.6 Automatic Token Refresh

For OAuth scopes, MCPlexer monitors token expiry and automatically refreshes
tokens before they expire. The refresh happens transparently -- downstream
servers always receive a valid token.

### 5.7 Per-Auth-Scope Instances

The same downstream MCP server can have multiple running instances, each with
different credentials. Instance pooling is keyed by the tuple
`(server_id, auth_scope_id)`. This enables scenarios such as:

- One GitHub MCP server instance authenticated as the developer's personal
  account
- Another GitHub MCP server instance authenticated as a service account
- Route rules determine which instance handles each tool call based on
  workspace context

### 5.8 Encryption at Rest

All secrets are encrypted at rest using **age** encryption
(`filippo.io/age`). Age is a modern, audited encryption tool designed for
simplicity and correctness. Encrypted secrets are stored in the SQLite database
and decrypted only in memory when needed.

### 5.9 Redaction Hints

Auth scopes can declare redaction hints -- field names that should be
automatically scrubbed from audit logs. Default redaction patterns include:

- `token`
- `key`
- `secret`
- `password`
- `authorization`

Custom redaction patterns can be added per auth scope.

---

## 6. Audit Logging

Every tool call that passes through MCPlexer is logged with full context. The
audit log serves as both a compliance record and a debugging aid.

### 6.1 Log Entry Fields

Each audit log entry captures:

| Field            | Description                                          |
|------------------|------------------------------------------------------|
| `timestamp`      | ISO 8601 timestamp of the tool call                  |
| `session_id`     | Unique identifier for the client session             |
| `workspace`      | Resolved workspace name                              |
| `tool_name`      | Fully namespaced tool name                           |
| `params`         | Tool call parameters (with redaction applied)        |
| `route_rule`     | ID of the route rule that matched                    |
| `downstream`     | Target MCP server identifier                         |
| `auth_scope`     | Auth scope used for credential injection             |
| `status`         | Result status (success, error, denied, timeout)      |
| `latency_ms`     | End-to-end latency in milliseconds                   |
| `response_size`  | Size of the response payload in bytes                |
| `cache_hit`      | Whether the response was served from cache           |

### 6.2 Parameter Redaction

Sensitive parameters are automatically redacted before being written to the audit
log. Redaction is applied based on field name matching against known sensitive
patterns (token, key, secret, password, authorization) and any custom patterns
defined in the auth scope's redaction hints.

Redacted values are replaced with `[REDACTED]` in the log. The original values
are never persisted.

### 6.3 Query API

The audit log is queryable via the REST API with filters:

```
GET /api/v1/audit?session=abc&workspace=acme&tool=github__*&status=error&from=2026-03-01&to=2026-03-25
```

Supported filter parameters:

- `session` -- filter by session ID
- `workspace` -- filter by workspace name
- `tool` -- filter by tool name (supports wildcard patterns)
- `status` -- filter by result status
- `from` / `to` -- filter by time range (ISO 8601)

### 6.4 Real-Time SSE Stream

Audit events are pushed in real time to connected dashboard clients via
Server-Sent Events. This provides a live view of all AI agent activity across
all workspaces and sessions.

### 6.5 Dashboard Metrics

The audit system feeds several dashboard metrics:

- **Tool leaderboard:** most-called tools ranked by frequency
- **Server health:** success/error rates per downstream server
- **Error breakdown:** error categorization (auth failures, timeouts, tool
  errors, policy denials)
- **Time series:** tool call volume and latency over time

---

## 7. Caching

MCPlexer implements a multi-layer caching strategy to reduce latency and
downstream load while preserving correctness for mutation operations.

### 7.1 Tool Call Cache

Tool call responses are cached based on pattern matching against tool names.

**Default cacheable patterns** (read operations):

- `get_*` -- single-resource reads
- `list_*` -- collection reads
- `search_*` -- search queries

**Default bypass patterns** (mutations):

- `create_*` -- resource creation
- `update_*` -- resource modification
- `delete_*` -- resource deletion

These defaults can be overridden per server.

### 7.2 Automatic Invalidation on Mutations

When a mutation tool call is executed (matching a bypass pattern), MCPlexer
automatically invalidates cached entries from the same namespace. This ensures
that a `list_issues` call after a `create_issue` call returns fresh data.

### 7.3 Custom Invalidation Rules

Per-server invalidation rules allow fine-grained control over cache behaviour:

```yaml
servers:
  - name: github
    cache:
      invalidation_rules:
        - trigger: "create_issue"
          invalidates: ["list_issues", "search_issues"]
        - trigger: "merge_pull_request"
          invalidates: ["get_pull_request", "list_pull_requests"]
```

### 7.4 Cache Busting

Any tool call can bypass the cache by including the `_cache_bust` parameter. This
forces a fresh call to the downstream server and updates the cache with the new
response.

### 7.5 Route Resolution Cache

Route resolution results are cached for **30 seconds**. This avoids re-running
the priority-ordered, deny-first evaluation on every tool call while remaining
responsive to configuration changes.

### 7.6 Tools/List Cache

The `tools/list` response (the catalog of available tools from each downstream
server) is cached for **15 seconds**. The cache is automatically invalidated
when a downstream server sends a `notifications/tools/list_changed` notification
via the MCP protocol.

### 7.7 Cache Statistics

Cache performance metrics are exposed via the REST API and the dashboard:

- **Hit rate:** percentage of tool calls served from cache
- **Miss rate:** percentage of tool calls that required a downstream call
- **Entry count:** number of entries currently in cache
- **Eviction count:** number of entries evicted (TTL expiry or invalidation)

### 7.8 Flush API

The cache can be programmatically cleared via:

- **REST API:** `POST /api/v1/cache/flush`
- **MCP tool:** `mcpx__flush_cache`

This is useful during development or after bulk data changes.

---

## 8. Process Lifecycle Management

MCPlexer manages the lifecycle of downstream MCP server processes, including
startup, idle management, crash recovery, and instance pooling.

### 8.1 Lazy Spawning

Downstream server processes are not started at MCPlexer boot time. Instead, they
are spawned on demand when the first tool call targeting that server arrives.
This reduces startup time and memory consumption when many servers are configured
but not all are actively used.

### 8.2 Idle Timeout

After a configurable period of inactivity, downstream server processes are
automatically stopped. The default idle timeout is **300 seconds** (5 minutes).
Processes are re-spawned on the next tool call.

This prevents resource exhaustion on developer machines where many MCP servers
may be configured across multiple workspaces.

### 8.3 Crash Recovery

Recovery is lazy and call-driven: when an instance has stopped (idle timeout
or crash), the next tool call targeting that server evicts the dead instance
and spawns a fresh one transparently. Per-server health is tracked
(consecutive failures, last error, last failure time) and surfaced on the
dashboard; sustained failures can trigger the auto-reload hook, which
restarts the server, writes an audit row, and raises a mesh alert.

A `restart_policy` field (`on-failure` | `always` | `never`) is accepted in
server configuration but is not currently enforced by the lifecycle manager —
supervised always-on restart is planned; today every policy behaves as
lazy respawn-on-next-call.

### 8.4 Instance Pooling

Server instances are pooled by the composite key `(server_id, auth_scope_id)`.
This means the same server binary can have multiple concurrent instances, each
running with different credentials.

The `max_instances` setting controls the maximum number of concurrent instances
per server (default: **1**). For servers that support concurrent access, this
can be increased to improve throughput.

---

## 9. Control Server (AI-Native Configuration)

MCPlexer exposes its own configuration as MCP tools via a dedicated control
server. This enables AI agents to introspect, configure, and manage MCPlexer
programmatically.

### 9.1 Available Control Tools

MCPlexer provides **19 MCP tools** for configuration management, organized
by resource type:

**Workspace management:**
- List workspaces
- Create workspace
- Update workspace
- Delete workspace

**Server management:**
- List servers
- Create server
- Update server
- Delete server

**Route management:**
- List routes
- Create route
- Update route
- Delete route

**Auth scope management:**
- List auth scopes
- Create auth scope
- Update auth scope
- Delete auth scope

**OAuth provider management:**
- List OAuth providers
- Create OAuth provider
- Update OAuth provider

### 9.2 Read-Only Mode

For environments where AI-driven configuration changes are undesirable, the
control server can be set to read-only mode:

```bash
MCPLEXER_CONTROL_READONLY=true
```

In read-only mode, all list/get operations succeed, but create/update/delete
operations return an error. This allows AI agents to introspect the configuration
without risk of unintended changes.

### 9.3 Use Cases

- An AI agent discovering what tools are available in the current workspace
- An AI agent suggesting configuration changes based on the project structure
- Automated onboarding: an AI reads a project's setup docs and configures
  MCPlexer workspaces, servers, and routes accordingly

---

## 10. Agent Mesh

MCPlexer provides inter-agent communication via a built-in message-passing
system. Multiple AI agents connected to the same MCPlexer gateway can
coordinate, share findings, and delegate tasks.

### 10.1 Message Passing

Agents communicate through two MCP tools:

- `mesh__send` -- send a message to another agent connected to the gateway
- `mesh__receive` -- check for pending messages and receive bounded previews
- `mesh__hydrate` / `mesh__thread` -- explicitly read one message or thread

### 10.2 Agent Discovery

When an agent calls `mesh__receive`, the response includes active agents visible
in the current workspace plus message previews. Full message bodies are not
returned by default; the response includes message IDs for explicit hydration.
The default receive payload is capped at 20 messages and 512 preview bytes per
message.

### 10.3 Coordination Patterns

The agent mesh enables several coordination patterns:

- **Fan-out:** A lead agent distributes subtasks to specialist agents (e.g.,
  one agent handles frontend changes, another handles backend).
- **Pipeline:** Agents pass work products through a chain (e.g., code
  generation, then review, then testing).
- **Consultation:** An agent working on a task asks another agent for domain
  expertise without delegating the task.

### 10.4 Message Delivery

Messages are delivered asynchronously. Pending messages are automatically
appended to tool results, so agents receive messages without explicit polling.
Agents can also call `mesh__receive` to explicitly check for new messages.

### 10.5 Context Bounds

Mesh payloads are bounded before ranking or formatting:

- `mesh__send` rejects bodies over 64 KiB.
- `mesh__receive` is preview-only by default.
- `mesh__hydrate` and `mesh__thread` are explicit full-read calls with their own
  content caps.
- Agent-directory lookups are scoped to the caller's readable workspace chain.
- `task__recent_activity({dedupe:true})` returns bounded lexical clusters for
  noisy repeated workspace events; omit `dedupe` to hydrate the full rows.
- `task__list({q, semantic:true})` TF-IDF-ranks already scoped and filtered task
  candidates before returning preview rows.
- `mcpx__context_cost_stats` reports process-local tool-result byte counters and
  the active context cap settings.

---

## 11. Resource-Scoped Tool Access (Scope Policy)

AI agents with broad credentials (GitHub tokens, Slack tokens, database access)
can interact with any resource those credentials permit. MCPlexer's scope policy
system restricts which resources a tool call can target, at the gateway level,
for any downstream server type.

### 11.1 How It Works

Each route rule has an optional `scope_policy` field containing resource-type
allowlists. When a tool call is dispatched, MCPlexer extracts resource
identifiers from the arguments and checks them against the policy. If any
extracted value is not in the allowlist, the call is blocked.

```yaml
routes:
  - workspace: work
    tools: ["github__*"]
    scope_policy:
      org: ["acme-corp"]
      repo: ["acme-corp/api", "acme-corp/web"]
    action: allow
```

An empty or absent scope policy means no enforcement (permissive by default).
Users opt in to restrictions when ready.

### 11.2 Generic Across Server Types

The same mechanism works for any downstream server. The resource type names
are conventions defined by each server's extractor:

```yaml
# Slack: restrict to specific channels
scope_policy:
  channel: ["#engineering", "#deployments"]

# PostgreSQL: restrict to specific schemas and tables
scope_policy:
  schema: ["public"]
  table: ["users", "orders"]

# Linear: restrict to specific teams
scope_policy:
  team: ["ENG"]
```

### 11.3 GitHub Argument Extraction

For GitHub tools, MCPlexer includes a built-in extractor that understands
multiple argument formats:

- Direct fields: `owner`, `repo`, `org`, `organization`, `full_name`
- GitHub URLs: `https://github.com/owner/repo/...`
- API URLs: `https://api.github.com/repos/owner/repo`
- Search queries: `repo:owner/repo` and `org:name` qualifiers
- Nested objects: `repository.owner` and `repository.name`

### 11.4 Pluggable Extractors

Each server type registers a `ScopeExtractor` that knows how to pull resource
identifiers from tool arguments. Phase 1 ships with a GitHub extractor.
Additional extractors for Slack, Linear, and other servers can be added with
no changes to the core policy engine.

### 11.5 Audit Logging

Blocked tool calls are recorded in the audit log with a clear error message
indicating the policy violation, the resource that was targeted, and which
values are allowed. This provides visibility into agent behaviour that would
have violated scope boundaries.

---

## 12. Desktop Experience (PWA + Daemon)

MCPlexer's desktop experience is an installable Progressive Web App backed by
a native background daemon. (An earlier Electron shell was removed — the Go
`setup` command and the PWA together cover everything it did, with no
Chromium runtime to bundle or patch.)

### 12.1 Installable PWA

The dashboard at `http://localhost:3333` ships a web app manifest and service
worker, so Chrome / Edge / Arc can install it as a standalone app:

- Runs in its own window with its own Dock / taskbar icon
- App shortcuts for Approvals, Mesh, and Servers
- OS notifications (approval requests, high/critical mesh signals) via the
  Web Notifications API while the PWA is open
- Offline shell fallback for navigations

### 12.2 Background Daemon

The Go binary runs as a user service — launchd on macOS
(`com.mcplexer.daemon`, `KeepAlive=true` for native crash-restart), systemd
on Linux. `mcplexer setup` installs the service, wires detected MCP clients
(Claude Desktop, Claude Code, Cursor, ...) to the gateway, and opens the
dashboard — no manual JSON editing.

### 12.3 Platform Support

| Platform         | Architecture        | Status   |
|------------------|---------------------|----------|
| macOS            | Apple Silicon (arm) | Shipped  |
| macOS            | Intel (x86_64)      | Shipped  |
| Linux            | x86_64              | Shipped  |

---

## 13. Web Dashboard

MCPlexer includes a full-featured web dashboard embedded in the Go binary.

### 13.1 Technology Stack

- **React 19** with TypeScript
- **Vite** for build tooling
- **Tailwind CSS v4** for styling
- **shadcn/ui** component library
- Embedded in the Go binary via `go:embed` -- no separate web server required

### 13.2 Dashboard Sections

**Real-time metrics:**
- Tool call volume (time series)
- Latency distribution
- Cache hit/miss rates
- Active sessions and workspaces

**Approval queue:**
- Pending approval requests with justification
- Approve / Deny actions
- Historical approval decisions

**Audit stream:**
- Live feed of all tool calls (SSE-powered)
- Filterable by workspace, tool, status, session
- Parameter details with redaction applied

**Configuration editor:**
- Workspace management (create, edit, delete)
- Server management (add, configure, remove downstream servers)
- Route management (define routing rules and policies)
- Auth scope management (configure credentials and OAuth flows)

### 13.3 Embedding

The web dashboard is compiled into the Go binary at build time using Go's
`go:embed` directive. This means the dashboard is always available when MCPlexer
is running in HTTP or daemon mode -- no additional files to deploy, no CDN
dependency, no CORS configuration.

---

## 14. Configuration Methods

MCPlexer supports four configuration methods, from graphical to programmatic.

### 14.1 Web UI / PWA

The recommended method for most users. The web dashboard (installable as a
PWA — see Section 12) provides a graphical editor for all configuration
objects (workspaces, servers, routes, auth scopes). Changes take effect
immediately.

### 14.2 YAML Configuration File

Configuration can be defined in YAML at `~/.mcplexer/mcplexer.yaml`. This
method is suitable for version-controlled configuration and team sharing.

The YAML file seeds the SQLite database on startup. Subsequent changes via the
UI or API are persisted to the database and take precedence.

```yaml
workspaces:
  - name: acme
    root_path: /Users/dev/acme

servers:
  - name: github
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    namespace: github

routes:
  - workspace: acme
    tools: ["github__*"]
    action: allow
    priority: 50

auth_scopes:
  - name: github-token
    type: env
    server: github
    env:
      GITHUB_TOKEN: "${GITHUB_TOKEN}"
```

### 14.3 REST API

Full CRUD operations are available via the REST API at `/api/v1`:

```
GET    /api/v1/workspaces
POST   /api/v1/workspaces
PUT    /api/v1/workspaces/:id
DELETE /api/v1/workspaces/:id

GET    /api/v1/servers
POST   /api/v1/servers
PUT    /api/v1/servers/:id
DELETE /api/v1/servers/:id

GET    /api/v1/routes
POST   /api/v1/routes
...
```

The API listens on localhost only and requires the local API token on every
`/api/*` request — as an `Authorization: Bearer <token>` header for CLI
callers, or via the session cookie the SPA receives on page load. The token
is generated at install time and stored at `~/.mcplexer/api-key` (mode 0600).

### 14.4 CLI Commands

MCPlexer provides CLI commands for common operations:

| Command                | Description                                    |
|------------------------|------------------------------------------------|
| `mcplexer serve`       | Start MCPlexer in HTTP mode with dashboard     |
| `mcplexer init`        | Initialize default configuration               |
| `mcplexer setup`       | Guided setup wizard                            |
| `mcplexer status`      | Show running servers, active sessions, stats   |
| `mcplexer dry-run`     | Test route resolution without executing        |
| `mcplexer secret`      | Manage encrypted secrets                       |
| `mcplexer daemon`      | Run as a background daemon                     |
| `mcplexer connect`     | Bridge a client to a running daemon            |
| `mcplexer control-server` | Start the AI-native control MCP server      |

---

## 15. Deployment Modes

MCPlexer supports three deployment modes to fit different workflows.

### 15.1 Stdio Mode (Default)

```
AI Client  --stdio-->  MCPlexer  --stdio-->  Downstream Servers
```

In stdio mode, the AI client spawns MCPlexer as a child process. Communication
happens over stdin/stdout using the MCP protocol's stdio transport. This is the
simplest deployment -- no ports, no networking, no daemon.

**Characteristics:**
- Single client per MCPlexer instance
- MCPlexer lifetime is tied to the client session
- No web dashboard (no HTTP listener)
- CWD is tamper-proof (inherited from the spawning client)

### 15.2 HTTP Mode

```
AI Client  --HTTP/SSE-->  MCPlexer (:3333)  --stdio-->  Downstream Servers
                              |
                         Web Dashboard
```

In HTTP mode, MCPlexer runs as a persistent server. It listens for MCP
connections over HTTP (with SSE for server-to-client messages) and serves the
web dashboard on the same port.

**Characteristics:**
- Multiple clients can connect simultaneously
- Web dashboard available for monitoring and configuration
- Persistent process -- survives client disconnects
- Suitable for team environments or always-on setups

### 15.3 Socket / Daemon Mode

```
AI Client  --stdio-->  mcplexer connect  --socket-->  mcplexer daemon
                                                          |
                                                     Downstream Servers
                                                          |
                                                     Web Dashboard
```

In daemon mode, MCPlexer runs as a background process (managed via systemd on
Linux or launchd on macOS). The `mcplexer connect` command creates a stdio
bridge between the AI client and the running daemon.

**Characteristics:**
- Background process persists across client sessions
- Multiple clients can connect via separate `mcplexer connect` bridges
- Web dashboard available
- Suitable for developers who want MCPlexer always running

### 15.4 System Service Integration

MCPlexer can be managed as a system service:

- **macOS:** launchd plist for automatic startup
- **Linux:** systemd unit file for service management

---

## 16. Technology Stack

### 16.1 Backend

| Component        | Technology                                           |
|------------------|------------------------------------------------------|
| Language         | Go 1.25+                                             |
| Database         | SQLite via modernc.org/sqlite (pure Go, zero CGO)    |
| Encryption       | age (filippo.io/age)                                 |
| MCP protocol     | Custom implementation (stdio + HTTP/SSE transports)  |
| Binary size      | Single binary, all assets embedded                   |

### 16.2 Frontend

| Component        | Technology                                           |
|------------------|------------------------------------------------------|
| Framework        | React 19                                             |
| Language         | TypeScript (strict mode)                             |
| Build tool       | Vite                                                 |
| Styling          | Tailwind CSS v4                                      |
| Components       | shadcn/ui                                            |
| Embedding        | go:embed into Go binary                              |

### 16.3 Desktop

| Component        | Technology                                           |
|------------------|------------------------------------------------------|
| App shell        | Installable PWA (manifest + service worker)          |
| Background       | launchd (macOS) / systemd (Linux) user service       |
| Bundled UI       | React web dashboard (go:embed)                       |

### 16.4 Build & Distribution

- **Pure Go, zero CGO.** No C compiler required. Cross-compilation is trivial.
- **Single binary.** The Go binary contains the server, the web dashboard, and
  the SQLite engine. No runtime dependencies.
- **Runs anywhere.** Compiles for macOS (arm64, amd64), Linux (arm64, amd64),
  and Windows (amd64).

---

## 17. Security Model

### 17.1 Local-First

MCPlexer runs entirely on the local machine. There is no cloud component, no
telemetry, no data exfiltration path. All data -- configuration, credentials,
audit logs -- stays on the host.

### 17.2 Tamper-Proof CWD

In stdio mode, the working directory is obtained from the operating system via
`os.Getwd()`. The AI client cannot override or spoof this value. This is the
foundation of directory-scoped routing security -- the client's position in the
filesystem determines its permissions.

### 17.3 Deny-First Evaluation

The route evaluation engine defaults to deny. If no route rule explicitly allows
a tool call, it is blocked. When deny and allow rules exist at the same
priority, the deny rule wins. This ensures that the default posture is
restrictive.

### 17.4 Credential Isolation

Credentials are:

- Encrypted at rest using age encryption
- Scoped to specific auth scope instances
- Injected only into the downstream server process that requires them
- Never exposed to the AI client
- Redacted from audit logs

### 17.5 Approval Barriers

Sensitive operations can require human approval before execution. The
self-approval prevention mechanism ensures that an AI agent cannot approve its
own requests, requiring a human in a separate session to authorize the action.

---

## 18. Performance Characteristics

### 18.1 Caching Layers

| Cache Layer        | TTL         | Invalidation Trigger                     |
|--------------------|-------------|------------------------------------------|
| Tool call cache    | Configurable| Mutation calls, custom rules, flush API  |
| Route resolution   | 30 seconds  | Configuration change, TTL expiry         |
| Tools/list         | 15 seconds  | notifications/tools/list_changed, TTL    |

### 18.2 Lazy Resource Usage

- Downstream servers start on first use, not at boot
- Idle servers are stopped after 300 seconds of inactivity
- Instance pooling prevents duplicate processes for the same server+credential pair

### 18.3 Binary Footprint

Single binary with embedded web assets. No runtime dependencies. No container
required. No JVM, no Node.js runtime, no Python interpreter.

---

## 19. Competitive Position

### 19.1 Current Alternatives

| Approach                          | Limitation                              |
|-----------------------------------|-----------------------------------------|
| Direct MCP server connection      | No routing, no policy, no audit         |
| Manual permission management      | Error-prone, not context-aware          |
| Custom middleware                  | High engineering cost, maintenance      |
| Cloud MCP proxies                 | Credentials leave the machine           |

### 19.2 MCPlexer Differentiators

- **Directory-scoped routing.** No other MCP gateway binds security policy to
  the filesystem. Switching projects automatically switches permissions.
- **Tamper-proof CWD.** The working directory cannot be spoofed by the AI
  client in stdio mode, providing a trustworthy security anchor.
- **Deny-first policy engine.** Security defaults to closed. Explicit allow
  rules are required for every tool call.
- **Integrated approval workflows.** Human-in-the-loop is built in, not bolted
  on. Approval state, justification, and metrics are first-class concepts.
- **Agent mesh.** Multi-agent coordination through a shared gateway is unique
  to MCPlexer.
- **Local-first, single binary.** No cloud dependency, no SaaS subscription,
  no data leaving the machine. One binary, zero dependencies.
- **AI-native configuration.** 19 MCP tools for self-management means AI agents
  can configure their own tooling.

---

## 20. Context Window Optimisation and Token Reduction

MCPlexer's architecture dramatically reduces token consumption and keeps AI context windows smaller, which directly improves model reasoning quality.

### 20.1 The 2-Tool Approach

Instead of exposing all downstream tools in the `tools/list` response (which can be 100+ tools across multiple servers), MCPlexer advertises only two core tools to the AI client:

1. **`mcpx__search_tools`** — keyword and semantic search to discover tools on demand
2. **`mcpx__execute_code`** — JavaScript sandbox where discovered tools are called

All downstream tool definitions are hidden from the initial context. The AI only loads the signatures it needs, when it needs them.

Additional built-in tools (approval management, mesh, cache flush) are also listed, but the total tools/list response typically contains fewer than 10 tools regardless of how many downstream servers are configured.

With `slim_surface=true`, workflow and observability built-ins such as
`mesh__receive`, `task__recent_activity`, and `mcpx__context_cost_stats` stay out
of the static list and are discovered through `mcpx__search_tools`.

### 20.2 Codegen: Batching Multiple Tool Calls

The `execute_code` tool provides a JavaScript execution environment where multiple tool calls are batched into a single MCP invocation:

```javascript
// Single execute_code call that batches 3 downstream tool calls:
const repo = github__get_repo({owner: "acme", repo: "platform"});
const issues = github__list_issues({owner: "acme", repo: "platform"});
const prs = github__list_pull_requests({owner: "acme", repo: "platform"});
print(`Issues: ${issues.length}, PRs: ${prs.length}`);
```

This replaces three separate MCP roundtrips with one. The AI writes code that chains tool calls together, reducing both token usage and latency.

A `parallel()` helper executes up to 10 tool calls concurrently within a single `execute_code` invocation:

```javascript
const [repo, issues, prs] = parallel([
  { tool: "github__get_repo", args: {owner: "acme", repo: "platform"} },
  { tool: "github__list_issues", args: {owner: "acme", repo: "platform"} },
  { tool: "github__list_pull_requests", args: {owner: "acme", repo: "platform"} }
]);
```

### 20.3 Schema Minification (Slim Tools)

Enabled by default (`MCPLEXER_SLIM_TOOLS`). Strips non-essential metadata from tool schemas before serving them to AI clients:

**Removed:** property-level descriptions, `additionalProperties`, `examples`, `default` values, `title`, `$schema`

**Preserved:** `type`, `properties`, `required`, `enum`, `items`, constraints (`minimum`, `maximum`, `minLength`, `maxLength`, `pattern`), composition (`oneOf`, `anyOf`, `allOf`)

This typically reduces tool definition size by approximately 70%.

### 20.4 Response Compaction

Enabled by default (`CompactResponses`). Compresses verbose tool results before returning them to the AI client:

- Columnar compression for arrays of objects (e.g., 50-item API results converted to column format)
- Null and empty field pruning (removes `null`, `""`, `[]` fields)
- JSON minification (whitespace removal)

Typical reduction: approximately 40% smaller tool results.

### 20.5 Preview + Hydrate Retrieval

Search/list surfaces return previews and IDs by default. Full content requires a
specific hydrate/get call:

- `mesh__receive` returns message previews; use `mesh__hydrate` or
  `mesh__thread`.
- `task__list` returns preview rows; use `task__get({id})` or opt in with
  `task__list({full:true})`.
- `memory__recall` and skill search return previews; use the corresponding get
  call for the full body.

### 20.6 Tool Call Summaries

When `execute_code` completes, MCPlexer returns a summary rather than raw JSON for each tool call. Only failed calls include arguments and error details. Successful calls show tool name, status, and latency.

### 20.7 Search-on-Demand Discovery

`search_tools` returns two detail levels:

- **summary**: tool names and one-line descriptions (lightweight, for deciding which tools to use)
- **full**: TypeScript signatures for code generation (loaded only when the AI is ready to write code)

Maximum 20 results per query. Results sorted by relevance using keyword matching with TF-IDF semantic fallback.

### 20.8 Why Smaller Context Improves AI Quality

Token reduction is not just a cost benefit. AI models perform better when their context window contains less noise:

- Fewer tool definitions means less distraction when the model is reasoning about which tool to use
- Batched execution means fewer back-and-forth turns, preserving conversation context for the actual task
- Compact responses mean the model can process more tool results within the same context budget
- On-demand discovery means the model only sees tools relevant to the current task

The combined effect is both reduced token usage and higher quality output (the model spends more of its context budget on the task, not on tool metadata). For teams on usage-based billing, fewer tokens can translate directly to lower AI provider costs.

### 20.9 Estimated Impact

| Mechanism | Token Reduction |
|-----------|----------------|
| 2-tool approach (hide 100+ tools from initial context) | ~95% fewer tool definitions |
| Codegen batching (1 call instead of N) | Proportional to batch size |
| Schema minification | ~70% smaller per tool definition |
| Response compaction | ~40% smaller per tool result |
| Search-on-demand (load only what is needed) | Proportional to total tool count |

For a typical multi-server setup (5-10 downstream servers, 50-100+ tools), the combined effect is an estimated 50-70% reduction in token usage compared to exposing all tools directly.

#### Worked Example: 5 Servers, 60 Tools, 6 Tool Calls in a Session

| | Direct Connection | With MCPlexer |
|---|---|---|
| Tool definitions in context | 60 tools × ~450 tokens = **27,000 tokens** | 2 tools × ~300 tokens = **600 tokens** |
| Tool discovery | N/A (all tools pre-loaded) | 2 search queries + responses = **~1,500 tokens** |
| On-demand schema loads | N/A | 5 schemas loaded, minified (70% smaller) = **~1,000 tokens** |
| Tool call invocations | 6 roundtrips × ~400 tokens overhead = **2,400 tokens** | 2 batched execute_code calls (3 calls each) = **~2,500 tokens** |
| Tool responses | 6 × ~1,400 tokens = **8,400 tokens** | 6 × ~840 tokens (compacted, 40% smaller) = **~7,000 tokens** |
| **Total tool-related tokens** | **~37,800** | **~12,600** |

**Reduction: ~67%** (in the middle of the 50-70% range).

The range depends on the number of downstream tools (more tools = greater benefit from the 2-tool architecture), how many distinct tools the AI uses per session (fewer = greater benefit from on-demand loading), and how well calls can be batched. Setups with 30 tools and minimal batching see reductions closer to 50%. Setups with 100+ tools and good batching see reductions above 70%.

**Note:** Token usage reduction does not automatically mean lower AI provider costs. The impact on billing depends on the provider's pricing model. For teams on per-token or usage-based billing, fewer tokens translate directly to lower costs. For teams on seat-based or flat-rate plans, the primary benefit is improved AI reasoning quality and faster context window utilisation rather than cost savings.

---

## 21. Tool Description Refinement

MCPlexer includes a continuous improvement system for tool descriptions. AI models that use tools through MCPlexer can suggest improved descriptions based on their experience, creating a feedback loop that makes tools progressively easier to use.

### 21.1 How It Works

1. An AI model uses a tool and identifies that the description is unclear, incomplete, or could be improved
2. The model calls the built-in `mcpx__suggest_description` tool with the tool name, an improved description, and optionally a rationale explaining why the change is better
3. MCPlexer stores the suggestion as a pending version in the database
4. A human reviews the suggestion in the web dashboard, then approves or rejects it
5. If approved, the refined description is served to all future clients in place of the original

### 21.2 Refinement Modes

| Mode | Behaviour |
|------|-----------|
| `off` | Refinement disabled. The suggestion tool is hidden and no suggestions are accepted |
| `manual` | Default. Models suggest, humans review and approve via the dashboard |
| `auto` | Model suggestions are auto-accepted and activated immediately without human review |

Configurable via settings API or the `MCPLEXER_DESCRIPTION_REFINEMENT_MODE` environment variable.

### 21.3 Version History and Rollback

Every suggestion is stored as a versioned record with full metadata:

| Field | Description |
|-------|-------------|
| Tool name | Which tool the description applies to |
| Description | The proposed or active description text |
| Source | `model` (AI-suggested), `manual` (dashboard-submitted), or `original` (baseline) |
| Status | `pending`, `active`, `rejected`, or `superseded` |
| Rationale | Why the model or admin believes this description is better |
| Model | Which AI model made the suggestion |
| Reviewed by | Who approved or rejected the suggestion |
| Review note | Admin feedback on the decision |

The first time a suggestion is made for any tool, MCPlexer automatically captures the original description as a baseline. Admins can roll back to any previous version from the dashboard.

### 21.4 Anti-Spam and Deduplication

- One pending suggestion per tool per session (prevents duplicate suggestions)
- Baseline captured automatically on first suggestion for each tool
- Suggestions stored per workspace for multi-tenant support

### 21.5 Description Hierarchy

When serving tool descriptions to clients, MCPlexer applies a three-layer hierarchy:

1. **Original** (lowest priority). The description from the downstream MCP server
2. **Refined** (middle). The most recently approved model or manual suggestion
3. **Admin override** (highest). A permanent override set via the settings API

Refined descriptions are cached in memory and invalidated immediately when a new version is activated.

### 21.6 Dashboard UI

The dashboard provides a dedicated review interface:

- **Pending review section.** Cards showing proposed description, rationale, source model, and timestamp. Accept or reject with notes
- **Version history.** Filterable table of all versions (active, pending, superseded, rejected) with restore capability

### 21.7 API Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/descriptions` | GET | List versions (filter by tool, status, source) |
| `/api/v1/descriptions/{id}` | GET | Get a specific version |
| `/api/v1/descriptions/{id}/accept` | POST | Approve a pending suggestion |
| `/api/v1/descriptions/{id}/reject` | POST | Reject a pending suggestion (review note required) |
| `/api/v1/descriptions` | POST | Manually submit a description from the dashboard |

---

## Appendix A: Configuration Reference

### Workspace

```yaml
workspaces:
  - name: string           # Unique workspace name
    root_path: string       # Absolute path to workspace root
    description: string     # Optional description
```

### Server

```yaml
servers:
  - name: string            # Unique server name
    command: string          # Command to start the server
    args: [string]           # Command arguments
    namespace: string        # Tool namespace prefix
    max_instances: int       # Max concurrent instances (default: 1)
    idle_timeout: duration   # Idle timeout (default: 300s)
    restart_policy: string   # on-failure | always | never
    cache:
      cacheable: [string]    # Tool name patterns to cache
      bypass: [string]       # Tool name patterns to skip cache
      invalidation_rules:
        - trigger: string    # Tool that triggers invalidation
          invalidates: [string]  # Tools whose cache is cleared
```

### Route

```yaml
routes:
  - workspace: string       # Workspace name
    server: string           # Target server name
    tools: [string]          # Tool patterns (exact, prefix/*, infix)
    subpath: string          # Optional subpath glob pattern
    action: allow | deny     # Route action
    priority: int            # Priority (1 = highest)
    approval_required: bool  # Require human approval
    approval_timeout: int    # Timeout in seconds (default: 300)
    scope_policy:            # Resource-scoped access (optional)
      org: [string]          # Allowed orgs (GitHub)
      repo: [string]         # Allowed repos (GitHub)
      channel: [string]      # Allowed channels (Slack)
      # ... any resource type for any server
```

### Auth Scope

```yaml
auth_scopes:
  - name: string            # Unique scope name
    type: env | header | oauth2
    server: string           # Target server name
    env:                     # For type: env
      KEY: value
    headers:                 # For type: header
      Header-Name: value
    oauth:                   # For type: oauth2
      provider: string      # OAuth provider template name
      client_id: string
      client_secret: string
      scopes: [string]
    redaction_hints: [string]  # Additional fields to redact
```

---

## Appendix B: API Endpoints

### Configuration

| Method | Endpoint                    | Description                    |
|--------|-----------------------------|--------------------------------|
| GET    | /api/v1/workspaces          | List all workspaces            |
| POST   | /api/v1/workspaces          | Create workspace               |
| PUT    | /api/v1/workspaces/:id      | Update workspace               |
| DELETE | /api/v1/workspaces/:id      | Delete workspace               |
| GET    | /api/v1/servers             | List all servers               |
| POST   | /api/v1/servers             | Create server                  |
| PUT    | /api/v1/servers/:id         | Update server                  |
| DELETE | /api/v1/servers/:id         | Delete server                  |
| GET    | /api/v1/routes              | List all routes                |
| POST   | /api/v1/routes              | Create route                   |
| PUT    | /api/v1/routes/:id          | Update route                   |
| DELETE | /api/v1/routes/:id          | Delete route                   |
| GET    | /api/v1/auth-scopes         | List all auth scopes           |
| POST   | /api/v1/auth-scopes         | Create auth scope              |
| PUT    | /api/v1/auth-scopes/:id     | Update auth scope              |
| DELETE | /api/v1/auth-scopes/:id     | Delete auth scope              |

### Operations

| Method | Endpoint                    | Description                    |
|--------|-----------------------------|--------------------------------|
| GET    | /api/v1/audit               | Query audit log (with filters) |
| GET    | /api/v1/audit/stream        | SSE stream of audit events     |
| GET    | /api/v1/approvals           | List pending approvals         |
| POST   | /api/v1/approvals/:id       | Approve or deny a request      |
| GET    | /api/v1/cache/stats         | Cache hit/miss statistics      |
| POST   | /api/v1/cache/flush         | Flush all caches               |
| GET    | /api/v1/health              | Readiness state + version info |
| GET    | /healthz                    | Alias for /api/v1/health       |
| GET    | /api/v1/status              | System status and metrics      |

## 22. Daemon Restart and Client Reconnect

MCPlexer is a long-lived background daemon. Upgrades, config reloads, and
crashes all happen while clients (claude, codex, cursor, the dashboard,
mesh peers) are mid-session. The reconnect surface is designed so a daemon
restart looks like a hiccup in the activity log, not a session-killing
event.

### 22.1 Lifecycle Contract

Every MCPlexer daemon advertises a single readiness state machine to the
outside world:

| State      | `/healthz` HTTP | Body `status` | Behaviour                                                                                |
|------------|-----------------|---------------|------------------------------------------------------------------------------------------|
| `starting` | 503             | `"starting"`  | Migrations + downstream init still running. New MCP/REST tool calls refused.             |
| `ready`    | 200             | `"ready"`     | Steady state. All surfaces accept work.                                                  |
| `draining` | 503             | `"draining"`  | SIGTERM received. New tool calls return retriable error; in-flight have <=5s grace.       |

`GET /healthz` and `GET /api/v1/health` are the same endpoint and return
the same body; the alias exists because k8s probes, load balancers,
docker HEALTHCHECK, and most uptime monitors standardise on `/healthz`,
while existing dashboards + SDKs use `/api/v1/health`. Either path can be
used; pick whichever the calling tool expects.

On SIGTERM the daemon:

1. Flips `readiness` to `draining` (next health probe returns 503).
2. Refuses new `tools/call` dispatches with a JSON-RPC error so the
   harness sees a clean retriable failure instead of a connection drop.
3. Releases every session's task leases with the
   `"daemon restarting"` note stamped on the status history so the next
   agent to pick the task up can tell why it was demoted.
4. Runs `PRAGMA wal_checkpoint(TRUNCATE)` on SQLite so a fresh process
   starts on a flat WAL and off-process tooling (backups, the dashboard)
   sees the latest committed state immediately.
5. Closes listeners and exits 0.

### 22.2 stdio/socket Client Reconnect (claude, codex, cursor)

`mcplexer connect --socket=<path>` is the bridge every MCP harness uses to
talk to the running daemon. It owns the reconnect logic so the harness
never has to:

- **Auto-redial.** On a socket drop the bridge retries the dial on a
  fast-then-bounded schedule (20 ms -> 50 -> 100 -> 200 -> 500 -> 1 s, capped
  at 2 s) until the daemon is back or the harness cancels. The first
  retry lands inside 20 ms so a clean restart is invisible to the model.
- **Replays the MCP `initialize` handshake.** The bridge snoops the
  client's first `initialize` request and `notifications/initialized`
  notification at runtime and replays both on every reconnect, so the new
  daemon process sees a fresh, valid session without the harness having
  to retransmit anything.
- **User-visible message.** On every reconnect the bridge prints a single
  stderr line:

  ```
  mcplexer: daemon restarting; reconnecting (init handshake will replay automatically)...
  ```

  Stdout stays reserved for JSON-RPC frames the harness is parsing, so
  this line never confuses the parser, but it is loud enough to show up
  in `claude --debug` / `codex serve` log output, giving the operator a
  clear signal that the gap was a daemon restart, not a hung tool call.

**Per-harness behaviour summary:**

| Harness     | Transport         | Behaviour during daemon restart                                                                                  |
|-------------|-------------------|------------------------------------------------------------------------------------------------------------------|
| claude code | `mcplexer connect --socket` over stdio  | Tool call in flight returns the retriable error; next tool call succeeds via the auto-redialed bridge. No prompt to the user. |
| codex CLI   | `mcplexer connect --socket` over stdio  | Same as claude; bridge reconnect is transparent. The reconnect stderr line surfaces in `codex serve` logs.       |
| cursor IDE  | `mcplexer connect --socket` over stdio  | Same; cursor's MCP manager treats the retriable error as expected and re-issues on the next tool call.            |
| Dashboard   | HTTP/2 over loopback                    | EventSource subscriptions reconnect; `/healthz` 503 -> 200 transition surfaces as a "daemon restarted" toast.       |
| Mesh peers  | libp2p over QUIC/TCP                    | See section 22.3 below.                                                                                          |

If a harness times out before the daemon comes back (unusual; the
schedule above means even a 5-second restart is transparent), the user
sees the stderr message and can re-issue the command; the harness's own
MCP manager will then start a fresh connection and the new daemon picks
up at the next `initialize`.

### 22.3 Mesh Peer Re-handshake

Mesh peers (other MCPlexer machines you have paired with) reconnect over
libp2p, not the Unix socket. The reconnect path is event-driven on
libp2p's `Disconnected` notifier plus a 30-second safety tick:

- The `internal/p2p/Reconnector` registers a libp2p `network.Notifiee`
  that fires the moment a paired peer's connection drops. The
  reconnector immediately re-walks the DHT for that peer and re-dials.
- A periodic safety tick re-checks every paired peer every 30 seconds,
  catching cases where libp2p's notifier missed an edge (transient
  network glitch, NAT rebind, etc.).
- A per-peer offline throttle skips repeated DHT searches when a peer
  has been offline for >10 minutes, so the daemon does not burn CPU
  re-walking the DHT for peers that are obviously gone. A successful
  ping (via the `LivenessMonitor`) clears the throttle so the next
  reconnect attempt happens immediately.

No application-layer handshake is needed on top of libp2p's; once the
connection comes back, mesh `Send` and `Receive` resume from the
outbound queue, and skill/memory/task replication catches up via the
existing background reconciliation passes.

End-to-end tests for the reconnector live in
`internal/p2p/reconnector_e2e_p2p_test.go` (rediscover after disconnect,
rediscover with empty peerstore) and the broader behaviour matrix is in
`reconnector_p2p_test.go` (28 cases covering backoff schedule, online
observers, kick path, throttle, status surfacing).
