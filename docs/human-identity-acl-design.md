# Human Identity ACL Design

This note captures the v1 implementation path for shared skills/server access
control after human identity and human task assignees.

## Current V1 Boundary

- Human identity is `users.user_id`.
- Device ownership is `peer_users(peer_id, user_id)`.
- Task assignment can target `assignee_user_id`.
- Existing cross-machine enforcement remains peer-scope based.

V1 should not require Google/OIDC. OIDC can later prove account ownership, but
local-first ownership already has enough structure for user-owned devices.

## Scope Model

Keep the existing peer consent gate and add narrower scopes:

- `mesh.skill_read:<workspace>`: peer can read skill metadata and bodies for the workspace.
- `mesh.skill_write:<workspace>`: peer can publish/import skill versions into the workspace registry.
- `mesh.server_access:<workspace>:<server_id>`: peer can invoke or proxy a specific downstream server for that workspace.
- `mesh.server_access:<workspace>:*`: explicit workspace-wide server access.

Scopes are granted to peers, but the UI should display the owning user when
`peer_users` maps the peer to a user. Enforcement remains cryptographic peer ID
first; human ownership is attribution and policy grouping.

## Enforcement Points

- Skill hub index/search/pull must check `mesh.skill_read`.
- Skill import/publish from peer-origin traffic must check `mesh.skill_write`.
- Remote task or mesh actions that imply downstream tool use must check
  `mesh.server_access` before dispatch.
- Dashboard grants should present user/device grouping but persist concrete
  peer scopes to avoid ambiguous authority.

## Data Model

No new identity table is needed for v1. Add optional helper projections only:

- user summary: users plus linked peers
- peer effective scopes: current peer scopes plus owner user
- server access matrix: workspace, server, peer, owner user, granted scope

Fine-grained deny rows can be deferred until a real conflict appears. Start
with explicit allow scopes and default deny.

## Rollout

1. Ship read surfaces for users and devices.
2. Add scope constants and scope catalog entries.
3. Gate skill hub endpoints behind read/write scopes.
4. Gate remote server access behind `mesh.server_access`.
5. Add dashboard affordances for granting by user/device group while writing
   peer-specific scopes.
6. Add audit fields that record both peer ID and resolved owner user ID.

## Tests

- Peer without scope is denied for skill read/write and server access.
- Peer with workspace wildcard is allowed only in that workspace.
- Peer linked to a user is shown as user-owned but enforcement still keys on
  peer ID.
- Revoked peer scopes stop access even if the user owns another allowed peer.
