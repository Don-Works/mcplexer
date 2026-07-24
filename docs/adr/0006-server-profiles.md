# ADR 0006: Server Profiles

## Status

Accepted.

## Context

MCPlexer started as a local operator control panel, but some deployments are
shared appliances. A `shared-skills` server should behave like a skills hub,
and a task relay should focus the task surfaces, without presenting local-only
workstation setup, downstream wiring, or worker control as the primary product.

P2P remains orthogonal for the established `full`, `skills`, `tasks`, and
`skills+tasks` profiles. The construction-time `core` profile deliberately
disables the collaboration module, so a p2p runtime setting is ignored there.

## Decision

Expose a runtime server profile from config and health:

- `full`: local workstation, current default.
- `core`: gateway control plane without optional product modules.
- `skills`: shared skills registry / hub.
- `tasks`: shared task and offer coordination server.
- `skills+tasks`: combined hub for skills and tasks.

Configuration is accepted through:

- `MCPLEXER_SERVER_PROFILE=core|full|skills|tasks|skills+tasks`
- `mcplexer serve --server-profile=...`
- `mcplexer serve --profile=...` as a short alias.

The backend normalizes `skills,tasks` and `tasks+skills` to `skills+tasks` and
publishes the active profile plus a capability map in `/health`:

| Capability | core | full | skills | tasks | skills+tasks |
| --- | --- | --- | --- | --- | --- |
| `skills` | no | yes | yes | no | yes |
| `tasks` | no | yes | no | yes | yes |
| `signals` | yes | yes | yes | yes | yes |
| `server_settings` | yes | yes | yes | yes | yes |
| local setup, approvals, audit, guards, downstreams | yes | yes | no | no | no |
| brain, memory, workers, delegations, model routing | no | yes | no | no | no |

The frontend reads `system.server_profile` and `system.capabilities` from
`/health`. In server profiles:

- `core` is treated as server mode and uses the capability-driven navigation
  instead of falling back to the full workstation runtime/sidebar.
- `/` redirects to `/skills` when skills are enabled, otherwise `/tasks` when
  tasks are enabled.
- The sidebar uses a focused server navigation instead of the full local
  workstation navigation.
- Workstation-only panels are hidden behind capability checks.
- The skills UI treats a missing local default skills directory as acceptable
  in server mode.

## Runtime module plan

Server profiles now produce a pure construction plan. The plan groups optional
products without introducing a plugin framework or changing the single-binary,
single-SQLite architecture:

| Runtime group | core | full | skills | tasks | skills+tasks |
| --- | --- | --- | --- | --- | --- |
| gateway core | yes | yes | yes | yes | yes |
| agent services | no | yes | yes | yes | yes |
| automation | no | yes | yes | yes | yes |
| collaboration | no | yes | yes | yes | yes |
| operations | no | yes | yes | yes | yes |
| experimental | no | yes | yes | yes | yes |

The established server profiles intentionally retain their historical full
construction path in this first runtime-isolation slice. This avoids changing a
shared skills/task appliance's background behaviour as a side effect of adding
`core`.

Construction gates implemented for `core` now:

- **Collaboration:** secret-transfer identity, self-user/consent bootstrap,
  libp2p host/pairing/discovery/reconnect/liveness, collaboration manager,
  local mesh, Telegram/Google Chat/Hammerspoon bridges, peer share/sync
  services, and silent replication are not constructed. The stdio path also
  skips local mesh construction.
- **Experimental:** Brain configuration is not resolved and its
  indexer/serializer/editor/assistant, watcher, Git backplane, repo discovery,
  and SOPS integration are not constructed even if Brain is enabled in
  environment or persisted settings.

The `agent`, `automation`, and `operations` fields are explicit target-state
gates but are not yet applied to all constructors. Skills, memory, tasks, code
index, workers/scheduler/model catalog, and monitoring still follow the legacy
construction path under `core` in this slice. Their UI capability flags remain
off; follow-up slices must make their consumers nil-safe before turning each
plan field into a construction boundary. This limitation is intentional and
documented rather than presenting capability-only filtering as runtime
isolation.

## Route Matrix

| Route group | core | full | skills | tasks | skills+tasks |
| --- | --- | --- | --- | --- | --- |
| `/skills`, skill registry details, publish/import flows | hidden | yes | primary | hidden | primary |
| `/tasks`, task detail, offers/activity | hidden | yes | hidden | primary | primary |
| `/settings`, `/signals` | available | yes | available | available | available |
| `/`, dashboard home | dashboard | dashboard | redirects `/skills` | redirects `/tasks` | redirects `/skills` |
| local setup, downstreams, approvals, audit, guards | yes | yes | hidden | hidden | hidden |
| workers, brain, memory, collaboration | hidden | yes | hidden | hidden | hidden |

## Consequences

Shared deployments can reuse the same binary and embedded web bundle while
presenting a focused hub UI. Operators can still enable p2p independently for
mesh sync or pairing outside `core`. A `core` daemon retains the gateway,
routing/downstream lifecycle, auth/secrets, approvals/audit, settings and
minimum management UI/API while avoiding construction of the collaboration and
experimental groups. Older daemons that do not advertise capabilities continue
to behave like `full`.
