# ADR 0006: Server Profiles

## Status

Accepted.

## Context

MCPlexer started as a local operator control panel, but some deployments are
shared appliances. A `shared-skills` server should behave like a skills hub,
and a task relay should focus the task surfaces, without presenting local-only
workstation setup, downstream wiring, or worker control as the primary product.

P2P is intentionally orthogonal to this decision. Any profile can run with or
without the p2p build tag and runtime p2p setting.

## Decision

Expose a runtime server profile from config and health:

- `full`: local workstation, current default.
- `skills`: shared skills registry / hub.
- `tasks`: shared task and offer coordination server.
- `skills+tasks`: combined hub for skills and tasks.

Configuration is accepted through:

- `MCPLEXER_SERVER_PROFILE=full|skills|tasks|skills+tasks`
- `mcplexer serve --server-profile=...`
- `mcplexer serve --profile=...` as a short alias.

The backend normalizes `skills,tasks` and `tasks+skills` to `skills+tasks` and
publishes the active profile plus a capability map in `/health`:

| Capability | full | skills | tasks | skills+tasks |
| --- | --- | --- | --- | --- |
| `skills` | yes | yes | no | yes |
| `tasks` | yes | no | yes | yes |
| `signals` | yes | yes | yes | yes |
| `server_settings` | yes | yes | yes | yes |
| local setup, approvals, audit, guards, memory, workers, model routing | yes | no | no | no |

The frontend reads `system.server_profile` and `system.capabilities` from
`/health`. In server profiles:

- `/` redirects to `/skills` when skills are enabled, otherwise `/tasks` when
  tasks are enabled.
- The sidebar uses a focused server navigation instead of the full local
  workstation navigation.
- Workstation-only panels are hidden behind capability checks.
- The skills UI treats a missing local default skills directory as acceptable
  in server mode.

## Route Matrix

| Route group | full | skills | tasks | skills+tasks |
| --- | --- | --- | --- | --- |
| `/skills`, skill registry details, publish/import flows | yes | primary | hidden | primary |
| `/tasks`, task detail, offers/activity | yes | hidden | primary | primary |
| `/settings`, `/signals` | yes | available | available | available |
| `/`, dashboard home | dashboard | redirects `/skills` | redirects `/tasks` | redirects `/skills` |
| local setup, downstreams, workers, approvals, audit, guards, memory | yes | hidden | hidden | hidden |

## Consequences

Shared deployments can reuse the same binary and embedded web bundle while
presenting a focused hub UI. Operators can still enable p2p independently for
mesh sync or pairing. Older daemons that do not advertise capabilities continue
to behave like `full`.
