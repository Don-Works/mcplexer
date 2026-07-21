# MCPlexer Product Context

## Product Purpose

MCPlexer is a local-first MCP gateway and dashboard for routing AI tool calls by workspace. It sits between AI clients and downstream MCP servers, enforcing workspace-scoped access, approvals, credential injection, auditability, worker delegation, memory, tasks, and peer/mesh coordination.

## Register

product

## Primary Users

- Operators and developers running multiple AI harnesses against local projects.
- Power users who need clear control over what each workspace can access.
- Agent operators reviewing approvals, delegations, worker runs, memory, tasks, and audit trails.
- Open-source adopters evaluating trust, setup, and daily operability.

## Core UX Principle

Workspace is the primary mental model. A workspace represents a directory tree and the policy boundary around it. The UI should let users start from a workspace, understand what is connected, what is allowed, what needs attention, and which agents or knowledge primitives are active in that scope.

## Strategic Principles

- Preserve feature visibility while reducing taxonomy drift.
- Prefer operational command surfaces over marketing or explanatory screens.
- Show actionable work first: approvals, missing credentials, failed/running delegations, active workers, open tasks, fresh memory, and recent audit activity.
- Deep specialist pages remain available, but workspace-scoped rollups should explain where to go next.
- Keep the interface dense, dark, square, and utilitarian per DESIGN.md.
- Use copy that names the real system concepts: workspace, route, approval, delegation, worker, memory, task, audit.
- Avoid hiding advanced control behind vague labels.

## Anti-Patterns

- Competing navigation taxonomies for the same object.
- Separate global pages that cannot be understood from workspace context.
- Duplicate setup flows with unclear priority.
- Card grids that only repeat icon, heading, and prose.
- Decorative visual treatment that slows down scanning.
- Removing visibility to make the UI feel simpler.

