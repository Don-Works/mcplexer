# ADR 0009: Capability-Aware Execution and Runtime Modules

## Status

Accepted for staged delivery.

## Context

MCPlexer's slim surface prevents a large downstream tool catalogue from
occupying every model request. The five stable entry points have kept the
default schema cost small, and Code Mode gives models an efficient way to
compose, filter, and aggregate multiple calls.

Two different concerns have nevertheless become coupled:

1. Every hidden tool call must currently be expressed as JavaScript, including
   a single small lookup that needs no composition.
2. The daemon presents a slim model surface but constructs gateway, agent
   state, automation, collaboration, operations, and experimental subsystems
   together under the `full` runtime.

MCP clients and model APIs also expose different capabilities. Some can defer
tool definitions or execute programmatic calls natively; others need
MCPlexer's compatibility surface. MCP protocol revisions add optional
capabilities without making them universally available.

## Decision

### 1. Let the model choose the execution shape

The slim surface exposes two complementary execution paths:

- `mcpx__call_tool` invokes one discovered tool with an argument object.
- `mcpx__execute_code` composes multiple calls and performs JavaScript
  control flow, parallelism, filtering, aggregation, parsing, or polling.

Both paths enter the same authoritative gateway call pipeline. Routing,
workspace policy, worker and skill capabilities, admin trust, approvals,
argument guards, downstream dispatch, sanitization, compression, and audit
remain enforcement-time decisions. The simple path is not a direct-call
bypass and cannot recursively invoke itself.

The `using-mcplexer` skill owns the selection guidance:

| Workload | Preferred path |
| --- | --- |
| One independent call with a small result | `mcpx__call_tool` |
| Independent fan-out or batch | `mcpx__execute_code` |
| A later call depends on an earlier result | `mcpx__execute_code` |
| Filter, aggregate, transform, or poll before returning | `mcpx__execute_code` |
| A tool needs immediate human interaction | Direct top-level interaction tool |

This is guidance, not an opaque gateway heuristic. The model can see the
trade-off and choose from the task shape.

### 2. Negotiate capabilities rather than infer them from client names

The gateway maintains an explicit set of supported MCP protocol revisions and
retains the client capability object for the session. Unknown or future
protocol revisions are not echoed as if supported.

Harness-name compatibility remains available for naming differences, but
protocol behavior should increasingly be selected from negotiated
capabilities. Standard MCP mechanisms such as URL elicitation or resources
may replace compatibility tools only when the client advertises and correctly
surfaces them.

### 3. Make server profiles construction-time module plans

The daemon remains one Go binary backed by SQLite. A pure runtime module plan
maps a server profile to these product groups:

- **gateway core:** routing, downstream lifecycle, auth and secret references,
  approvals, audit, settings, and required API/UI plumbing;
- **agent services:** memory, tasks, skills, and code index;
- **automation:** workers, delegation, scheduler, and model routing;
- **collaboration:** local mesh, P2P, replication, and chat bridges;
- **operations:** monitoring, logwatch, and usage collection;
- **experimental:** Brain and other alternate state systems.

`full` preserves existing behavior. `core` constructs only the gateway core.
Focused `skills`, `tasks`, and `skills+tasks` profiles retain the services
needed for their advertised purpose. Delivery is incremental: a profile must
not claim a group is absent until construction actually consults the module
plan and nil consumers degrade safely.

### 4. Keep boundaries small and local

Consumers depend on the narrow store and service interfaces they use. The
ordered tool-call pipeline may be split into named policy stages, but it
remains one enforcement chain.

This decision does not introduce a general plugin system, service locator,
microservices, a second database, or a universal Event/Action abstraction.

## Security Invariants

- Hidden downstream tools remain uncallable through arbitrary top-level
  `tools/call` requests.
- The simple wrapper reuses the authoritative inner-call pipeline.
- Worker isolation, allowlists, capability presets, and filesystem guards
  apply equally to both execution shapes.
- Skill namespace capabilities cannot be widened by the wrapper.
- Secret plaintext never enters model context or the audit log.
- Every inner target call remains independently auditable and correlated with
  its outer execution.
- A disabled runtime module does not leave a reachable half-configured tool or
  HTTP route.

## Delivery Sequence

1. Add and test the simple invocation path.
2. Correct protocol negotiation and retain client capabilities.
3. Introduce the pure runtime module plan and gate substantial optional
   subsystem construction for `core`.
4. Integrate and run combined gateway, downstream, worker, command, and context
   cost tests.
5. Publish concise `using-mcplexer` selection guidance.
6. Forward-test fresh agents on single-call, batch, dependent, filtering, and
   human-interaction scenarios without disclosing the expected choice.
7. Expand construction gating only after each module has nil-safe consumers
   and regression coverage.

## Consequences

Models get a lower-ceremony path without losing Code Mode's compositional
advantage. Existing clients retain the compatibility surface, while capable
clients can progressively use native protocol and model features.

The daemon's code remains a modular monolith, but operators can run a genuinely
smaller core. During migration, some optional groups may still be constructed
under profiles whose module plan has not yet been fully enforced; documentation
and capability reporting must state that boundary accurately.

