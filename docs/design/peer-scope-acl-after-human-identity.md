# Shared Skills / Server ACL — Design After the Human Identity Foundation

Status: **[DESIGN]** — no enforcement code in this change. Captures the
shape of the next ACL pass so a coding worker can implement it
unambiguously. Parent of design/pure-mode-toggle and
design/p2p-skill-hub-sync-gaps; intentionally stays orthogonal to
either (this doc is about *who is allowed to do what*; those docs
are about *how the share is transported*).

Epic reference: 01KTY2T0S6NS4KC0YPRN1RE9C4.

## 1. Problem

The current ACL is **peer-scoped** and lives almost entirely in
`p2p_peers.scopes` (a JSON array of opaque strings on each paired
peer). Nine scopes ship today; all are booleans on the peer record
(`peerscope.Known` in `internal/peerscope/registry.go`). The model
predates the human identity layer (M7.1: `users.is_self`, paired
peers linked to a `user_id`, `consent.Tier` for same-user / same-org
/ cross-org), and it conflates three concerns that should be split
once we have a stable notion of *who* the human is:

1. **Who** is asking? A peer (libp2p ID) is a machine, but every
   decision eventually is taken on behalf of a human. Today we infer
   that from the M7.1 user link on the peer record, but the ACL
   surface itself only knows the peer.
2. **What surface** is being touched? Skills, registry entries,
   installed bundles, server configs, routes, auth scopes, and
   OAuth tokens each have different blast radius — yet the only
   graduated mechanism we have is the `Severity` hint on the
   scope registry entry (a UI affordance, not an enforcement
   point).
3. **How trust was established**? Tier 1 same-user (auto-pair),
   Tier 2 same-org (explicit human grant), Tier 3 cross-org
   (explicit grant + boundary check) is captured in
   `internal/consent/consent.go` and stamped onto the
   `mesh__grant_peer_scope` audit row — but the scopes
   themselves don't know which tier authorized them, so the
   "is this grant still trustworthy?" question has no
   machine-checkable answer at the dispatch site.

Today the **near-term enforcement point** is the peer scope check
itself (`store.HasPeerScope` called from
`p2p/skill_share_stream_p2p.go:128`,
`p2p/registry_share_p2p.go` via
`registryShareScopeName`, `mesh/auth_sync.go` etc.). The desired
follow-up scopes — `mesh.skill_read`, `mesh.skill_write`, and
`mesh.server_access` — need to land on top of the human identity
foundation, not in parallel with it.

## 2. Goals & non-goals

### Goals

- A single declarative source of truth for "what scope means what,
  who can grant it, and which tier(s) authorize it".
- Enforcement decisions at one choke point per surface (skill
  share, registry share, server access) that read all three
  signals: peer grant, M7.1 user link, consent tier.
- Backward compatibility with the existing nine scopes — no
  revoke on upgrade; existing grants keep working.
- Tier-aware grant UI: the dashboard's grant picker shows each
  scope with a tier badge and a "who already has it" view keyed
  by user (not by peer), because a same-user pair of machines
  shouldn't look like two strangers.
- A typed `denial.code` extension that captures the new
  failure modes (`tier_too_low`, `no_user_link`,
  `server_access_workspace_mismatch`) without breaking the four
  existing `scopes.Denial*` codes.

### Non-goals (v1 of THIS design — not v1 of the product)

- Per-resource argument-level ACL (e.g. "this peer can read skill
  X but not skill Y"). Already covered by the existing route-level
  `scope_policy` (`docs/mcplexer-features.md` §11); we do not
  duplicate it here.
- Org-pair binding semantics. The `cross_org_boundary` code is a
  placeholder; the data model (which org a peer belongs to, which
  orgs may pair) is a separate workstream and we hook the boundary
  check in via the same `consent.Resolver` interface used today
  with the `NopResolver` fallback returning `TierCrossOrg`.
- Replacing the `mesh.auth_sync` high-trust scope. It already
  works and is the only end-to-end path that touches
  routes + downstream server command/args + OAuth tokens.
  `mesh.server_access` is a NEW scope that lets a peer read (not
  write) a curated subset of that surface; the write path stays
  on `mesh.auth_sync`.
- Multi-org / multi-tenant installs. v1 is "one human, one
  user_id, one (optional) org". Multi-org per install is
  out-of-scope until the data model in §3 is settled.

## 3. Data model

### 3.1 New columns on `users` (M7.1)

| column              | type    | meaning                                         |
|---------------------|---------|-------------------------------------------------|
| `default_org`       | TEXT    | Org label the user grants from. Empty = no org. |
| `grant_policy`      | TEXT    | JSON: per-scope tier ceiling + per-tier defaults. |
| `created_at`        | TIMESTAMP | existing, surfaced for grant-picker "first seen" |

The `default_org` mirrors the `MCPLEXER_SELF_ORG` env var used by
`config.BootstrapSelfUser` (`internal/config/bootstrap_user.go:30`)
and the `consent.Resolver.TierFor` consumer. We persist it
explicitly so the grant UI can show "scopes granted in the context
of *Acme* vs *Personal*" without a settings round-trip.

`grant_policy` is the per-user override surface. Shape (JSON):

```json
{
  "tier_ceiling": "same_org",        // max tier this user grants at
  "scopes": {
    "mesh.skill_request": {
      "default_tier":   "same_user",
      "allow_wildcard": false,
      "max_instances":  3
    },
    "mesh.server_access": {
      "default_tier":   "same_org",
      "allow_wildcard": false,
      "max_instances":  1
    }
  }
}
```

Defaults (when `grant_policy.scopes.<name>` is absent) are encoded
in the `peerscope.ScopeDef` entry (§4.1) — the user's policy is
the override, the registry is the fallback.

### 3.2 New columns on `p2p_peers.scopes` (no schema break)

The current shape is a JSON array of strings. Two additive changes:

- **Tier stamp on grant.** We keep the array shape but introduce
  the convention that an entry MAY be either a bare string
  (`"mesh.skill_request"`) or a structured object
  (`{"scope":"mesh.skill_request","tier":"same_org","granted_by":"<user_id>","granted_at":"<rfc3339>","max_instances":1}`).
  Bare strings are read as "tier unknown, behaves as Tier 1
  (auto-pair) for backwards compat" — this is the entire
  migration story for the data already in production.
- **Ancillary `peer_tier` column on the peer row** is *not*
  needed: the tier is on the scope entry, and different scopes on
  the same peer can be at different tiers. The peer record still
  carries the existing M7.1 `user_id` link; we don't need a
  second one.

This is a **wire-compatible migration** because `HasPeerScope`
already does prefix matching; an unknown structured form is
parsed by a new helper that recognises both shapes.

### 3.3 New table `mesh_share_consents` (audit-only)

```sql
CREATE TABLE mesh_share_consents (
  id              TEXT PRIMARY KEY,    -- ulid
  peer_id         TEXT NOT NULL,
  user_id         TEXT,                -- nullable for legacy peers
  scope           TEXT NOT NULL,
  tier            TEXT NOT NULL,       -- same_user | same_org | cross_org
  action          TEXT NOT NULL,       -- grant | revoke | check | use
  grant_id        TEXT,                -- ulid of mesh__grant_peer_scope row
  ts              TIMESTAMP NOT NULL,
  extra           TEXT                 -- JSON: workspace_id, skill_name, etc.
);
CREATE INDEX mesh_share_consents_peer_scope ON mesh_share_consents(peer_id, scope);
```

This is the join table that lets the dashboard answer "who
authorized this share, at what tier, when, for what resource?"
without having to walk the mesh audit log. The existing
`mesh__grant_peer_scope` / `mesh__revoke_peer_peer_scope` /
`mesh__check_scope` (planned) audit rows are the source; this
table is a denormalized read projection.

### 3.4 New: `mesh.server_access` scope

Boolean (no resource suffix) for v1. A peer that holds
`mesh.server_access` can call the new REST surface
`GET /api/v2/mesh/peer/<id>/servers` which returns a read-only
projection of the peer's downstream server configs (name,
namespace, transport, idle_timeout, max_instances — NOT
command/args and NOT auth_scope material). It is the **read**
side of the trust split from `mesh.auth_sync` (which is the
**write** side, with explicit interactive approval).

Rationale for the split: an operator on a same-user pair wants
to be able to *see* what the other machine has running without
hand-granting the full auth_sync surface (which mirrors secrets
back to the requester and registers downstream servers on the
responder). v1 deliberately ships only the read side; the write
side stays where it is.

## 4. Scope names and shapes

### 4.1 The new `peerscope.ScopeDef` entries

Append to `internal/peerscope/registry.go::Known`:

| Scope string              | ResourceKind | Wildcard | Tier ceiling (default) | Severity | Description |
|---|---|---|---|---|---|
| `mesh.skill_read`         | `""`         | false    | `same_user`            | `low`    | Read skill inventory (names, versions, hashes, signer pubkeys) of installed bundles on the responder. Does not return bundle bytes. |
| `mesh.skill_write`        | `""`         | false    | `same_org`             | `high`   | Write skill inventory to the responder: the responder accepts a signed offer and stores it for the local user to install. Mirrors the existing `mesh.skill_request` semantics but routes through the registry surface (offers are author-tagged, not minisign-signed). |
| `mesh.server_access`      | `""`         | false    | `same_org`             | `medium` | Read-only access to the peer's downstream server config projection (name, namespace, transport, profiles, idle_timeout). Does NOT include command/args or any auth_scope. Read endpoint: `GET /api/v2/mesh/peer/<id>/servers`. |

The `Tier ceiling` column is a NEW field on `peerscope.ScopeDef`:

```go
type ScopeDef struct {
    // ... existing fields
    DefaultTier TierCeiling `json:"default_tier,omitempty"`
}

// TierCeiling is the highest trust tier at which a user is
// permitted to grant a scope by default. A user's grant_policy
// can lower this; it cannot raise it.
type TierCeiling string

const (
    TierCeilingSameUser TierCeiling = "same_user"
    TierCeilingSameOrg  TierCeiling = "same_org"
    TierCeilingAny      TierCeiling = "any" // reserved for mesh.auth_sync
)
```

The wildcard is `false` for all three new scopes in v1. We
deliberately do not support a per-server or per-skill wildcard in
v1; if the use-case emerges, the registry entry is the only
place to add it (one line in `Known` + a new
`peerscope_consistency_test.go` case).

### 4.2 The relationship to existing scopes

| Existing scope            | Status under this design |
|---|---|
| `trigger_worker:`         | unchanged — same shape, same enforcement, no tier stamp required (Tier 1 default) |
| `task_offer:` / `task_assign:` / `task_sync:` | unchanged — workspace-scoped colon prefixes, M7.1 user link already consulted by the worker code path |
| `mesh.memory_request`     | unchanged — already covered by `mesh.skill_request` semantics family |
| `mesh.skill_request`      | unchanged — but a **future** ADR may collapse it into `mesh.skill_read` + `mesh.skill_write`; do not do it in this change. |
| `mesh.registry_request`   | unchanged — distinct surface from skill install; the new `mesh.skill_read` is read-only on local inventory and does not replace registry share |
| `mesh.attachment_request` | unchanged |
| `mesh.auth_sync`          | unchanged — remains the only "writes server configs" path; gets the `TierCeilingAny` ceiling |

The hierarchy rule: **`mesh.skill_request` > `mesh.skill_read`**.
Holding the existing `mesh.skill_request` is sufficient to pass
the new `mesh.skill_read` check (because the requester is asking
for the bundle, which is at least as much as the inventory). The
enforcement helper (§5) implements this subsumption rule
explicitly so a peer holding the older scope doesn't need to be
re-granted the new one.

## 5. Enforcement points

The single-rule-for-each-surface principle from ADR 0004 carries
over: every decision reads the same three signals at one
choke point. The helper lives in a new
`internal/peerscope/enforce.go` package so all four
subsystems (skill share, registry share, server access, future
memory write) can call it.

```go
// Decision is the typed outcome of a scope check.
type Decision struct {
    Allowed       bool
    EffectiveTier consent.Tier
    Denial        scopes.Denial  // zero value when Allowed
}

// CheckPeerScope evaluates (peer, scope) under the M7.1-aware
// model. It is the only function in the codebase that returns a
// peer-scope decision.
func CheckPeerScope(
    ctx context.Context,
    store Store,                     // HasPeerScope + GetPairedPeer + users + resolver
    peerID, scope, resource string,  // resource="" for booleans
) Decision
```

### 5.1 Skill share (`/mcplexer/skill/1.0.0`)

`internal/p2p/skill_share_stream_p2p.go::checkRemoteAllowed` stays
the dispatcher. The new code path is:

1. `peer, _ := store.GetPairedPeer(peerID)` — 404 if missing
2. `userID := peer.UserID` (from the M7.1 link)
3. `tier := resolver.TierFor(peerID)` — returns
   `TierCrossOrg` if no resolver is wired (NopResolver default)
4. `def := peerscope.FindByPrefix(scope)` — 403
   `scope_out_of_band` if unknown
5. Apply subsumption: if `scope == "mesh.skill_read"` and
   `hasScope(peer.Scopes, "mesh.skill_request")` → allow (with
   audit row noting the subsumption)
6. Check `peer.Scopes` for either the bare string or a
   structured entry where `entry.tier <= def.DefaultTier`
7. If no grant, `tier` is `TierCrossOrg`, and `def.DefaultTier`
   is `TierCeilingSameUser` → return 403 `tier_too_low` (NEW
   denial code)
8. If grant found at a lower tier than `def.DefaultTier`
   permits → return 403 `tier_too_low`
9. Else: write a `mesh_share_consents` row, return
   `Decision{Allowed: true, EffectiveTier: tier}`

### 5.2 Server access (`GET /api/v2/mesh/peer/<id>/servers`)

New REST handler, gated entirely on `mesh.server_access`. The
returned projection is built in the handler (NOT by exposing the
full `/api/v1/servers` endpoint to peers) so the `command`,
`args`, and any auth-scope material are stripped before
serialisation. This is the only new endpoint in v1 of this
design.

### 5.3 The hub-search path

`docs/design/p2p-skill-hub-sync-gaps` covers hub search
transport; this design covers its ACL. Hub index and search
requests use `mesh.skill_read` (read inventory) and
`mesh.skill_write` (push offers). The hub service in
`internal/p2p/hub_sync.go` will call `CheckPeerScope` at the
top of each request handler — the wire format is unchanged, only
the gate changes.

### 5.4 Backward compat: legacy peers

A peer paired before M7.1 has no `user_id` and a bare-string
scope array. The helper treats:

- missing `user_id` + bare-string scope + `Tier` resolver
  returning `TierCrossOrg` → **fail closed**: no_scope (we do
  not pretend the legacy grant is "same user"). The user is
  asked to re-grant on the next mesh__list_peers visit.

This is the only place we break compatibility, and it is
defensible: a legacy grant was a peer-scope grant with no
identity, which is the exact surface the new model is
tightening. M7.1 has been live long enough that the bulletproof
e2e rig already exercises the re-grant path.

## 6. UI / API implications

### 6.1 Grant picker

The current `GET /api/v1/scopes` (`internal/api/scopes_handler.go`)
adds two fields per entry:

- `default_tier` (string, one of `same_user`, `same_org`, `any`)
- `subsumes` (array of scope strings that this entry subsumes)

The dashboard's "Grant a scope" sheet groups scopes by tier
ceiling — "Same-user (low friction)" vs "Same-org (explicit
human grant)" vs "Any (you really mean it)" — and the picker
disables the row when the peer's `Tier` is above the scope's
ceiling. The user is told *why* it's disabled ("Peer B is in
org *Other*, this scope requires *same_org* or lower").

### 6.2 Scopes list per peer

The existing `GET /api/p2p/peers/{id}` payload adds a
`scope_entries` array (parallel to the legacy `scopes` array of
bare strings) so the UI can render the tier stamp and the
"granted by" user row. The legacy `scopes` field is preserved
as a derived convenience (bare strings, sorted unique) so
older clients keep working.

### 6.3 Denial codes

Two new entries in `internal/scopes/denial.go`:

| Code              | Meaning |
|---|---|
| `tier_too_low`    | Peer holds the scope but the grant's recorded tier is higher than the scope's `DefaultTier` ceiling. The user must re-grant at a lower tier (e.g. pair a same-user machine instead of a same-org one) or the scope's ceiling must be raised by the operator. |
| `no_user_link`    | Peer is paired but its M7.1 `user_id` is missing. Treated as cross-org for safety. The remediation is to re-pair (M7.1 sends the user_id on the second protocol line). |

Both codes are appended to the `DenialCode` const set and to
the `Valid()` switch — append-only across releases, same rule
as the existing four codes.

### 6.4 Audit rows

Every successful `CheckPeerScope` call writes a
`mesh_share_consents` row (the new table) keyed by
`(peer_id, scope, action)`. The existing
`mesh__grant_peer_scope` audit row stays as the **grant**
record; `mesh_share_consents` is the **use** record. The
dashboard's "Recent activity" pane can show both, distinguished
by `action` field.

## 7. Migration concerns

### 7.1 Data

- **`users` columns.** `ALTER TABLE users ADD COLUMN default_org
  TEXT NOT NULL DEFAULT ''` and `ADD COLUMN grant_policy TEXT NOT
  NULL DEFAULT '{}'`. Defaults are the existing behaviour
  (no org, no overrides). Backfill is not required.
- **`p2p_peers.scopes`.** The JSON array is read by a new parser
  that recognises both bare strings and structured objects; on
  every `GrantPeerScope` call, the row is rewritten in the
  structured form so we never *write* the bare form. The
  existing index on `scopes` (used by `HasPeerScope` today)
  remains valid because `json_extract` is prefix-tolerant. We
  need to verify with `peerscope_consistency_test.go` that
  every existing test still passes against the new parser.
- **`mesh_share_consents`.** New table, backfilled at first
  daemon start by replaying the last 30 days of
  `mesh__grant_peer_scope` and `mesh__use_scope` audit rows
  into a `use` action. Older rows have `tier='unknown'`,
  `user_id=null`, and the dashboard flags them in the UI as
  "Legacy grant — re-grant to upgrade to M7.1 tier semantics".

### 7.2 Wire

- All three new scopes are **new strings**. No existing wire
  field changes. The structured-object form of a scope grant
  goes through `mesh__grant_peer_scope` and is treated as
  opaque by older daemons (they store the JSON-encoded string
  verbatim in the `scopes` array — verify with the
  `peerscope_consistency_test.go` shape test).
- The denial codes `tier_too_low` and `no_user_link` are new
  values for the existing `denial.code` field. Old clients
  switch on the absence of the field; new clients switch on
  the new codes.

### 7.3 Feature flag

The whole ACL split is gated by `MCPLEXER_ACL_V2=1` (env var,
default off in the next minor, on by default two minors later).
The flag is checked in the `CheckPeerScope` helper; when off,
the helper falls through to the existing `HasPeerScope` call
(no behavioural change). The flag is **not** consulted in the
data-migration path — the structured-object form is written
unconditionally so we can flip the flag on with no second
data-migration.

## 8. Phased rollout

### Phase 0 — Design (this doc, no code)

Land the design. Open a follow-up epic per phase so the
backlog stays small and testable. The bulletproof e2e test
(`/test-mcplexer`) is the canary — it must stay green at every
phase boundary.

### Phase 1 — Data + structured grant

- `users` columns + migration.
- `mesh_share_consents` table + backfill.
- New `peerscope.ScopeDef.DefaultTier` field.
- `mesh__grant_peer_scope` writes structured form.
- `MCPLEXER_ACL_V2=1` flag, default OFF.
- The `peerscope_consistency_test.go` adds cases for the
  structured-form parser.

Exit criteria: daemon runs with flag off identical to current;
with flag on, the `scopes` JSON in `p2p_peers` is the structured
form. No denial-code change yet.

### Phase 2 — `CheckPeerScope` helper + denial codes

- New `internal/peerscope/enforce.go`.
- `internal/scopes/denial.go` gains `tier_too_low` and
  `no_user_link`.
- All four enforcement sites (§5.1, §5.2, §5.3, plus the
  memory share stub at `internal/p2p/memory_share_stub.go:110`)
  swap to the helper.
- `mesh__check_scope` MCP tool surfaces the new denial codes
  in the existing `POST /api/p2p/peers/{id}/scopes/check`
  shape.
- Flag default: ON.

Exit criteria: all existing scopes continue to work; legacy
peers fail closed (per §5.4) and the dashboard prompts the
user to re-grant; `peerscope_consistency_test.go` covers
both code paths.

### Phase 3 — `mesh.skill_read` / `mesh.skill_write`

- Two new entries in `peerscope.Known`.
- Skill inventory read endpoint
  (`GET /api/v2/mesh/peer/<id>/skills`) gated on
  `mesh.skill_read` (with `mesh.skill_request` subsumption).
- Skill offer push endpoint
  (`POST /api/v2/mesh/peer/<id}/skill-offer`) gated on
  `mesh.skill_write`.
- `docs/design/p2p-skill-hub-sync-gaps` is updated to point at
  the new gates (it stays the source of truth for the hub
  *transport*; we are only adding the ACL layer).

Exit criteria: the e2e hub-sync test exercises
`mesh.skill_read` and `mesh.skill_write` grant + use + deny
flows across the same-user, same-org, and cross-org tiers.

### Phase 4 — `mesh.server_access`

- New entry in `peerscope.Known`.
- New read-only REST endpoint
  (`GET /api/v2/mesh/peer/<id>/servers`).
- New projection type in `internal/api` (name, namespace,
  transport, profile, idle_timeout, max_instances; never
  `command`, never `args`, never auth-scope material).
- Dashboard "Peers" page adds a "Servers" tab per peer gated
  on the new scope.

Exit criteria: a same-org peer can pull the projection; a
cross-org peer with the grant is rejected with `tier_too_low`
when the local operator's `grant_policy.tier_ceiling` is
`same_user`; a same-user peer auto-pairs and the projection
appears without an explicit grant.

## 9. v1 now vs deferred

### Ships in this design (no code yet, but locked in)

- The data model in §3 (users columns, structured grant form,
  `mesh_share_consents` table, `mesh.server_access` scope).
- The denial-code extension in §6.3.
- The phased rollout in §8.
- The non-goals: per-resource ACL, multi-org per install,
  server-config write, wildcard for new scopes.

### Deferred (open follow-up epics)

- **Wildcard support for the new scopes** (e.g.
  `mesh.skill_read:foo` to grant read on a single skill name).
  Deferred because the inventory surface is small and the
  wildcard UX is non-trivial. Tracked as
  `epic/design-wildcard-skill-scopes`.
- **Org-pair binding semantics** that close the loop on
  `cross_org_boundary`. The data model here is compatible
  (we already stamp tier on the grant entry); the policy
  engine that compares orgs lives in the trust workstream
  and is tracked there.
- **Multi-org per install.** Each user gets one `default_org`;
  a user belonging to two orgs would need a per-scope `org`
  field on the grant. Not in v1.
- **Write side of `mesh.server_access`.** The decision to keep
  write on `mesh.auth_sync` is a v1 choice; if operators
  complain about auth_sync being too coarse, the v2 follow-up
  is to add `mesh.server_access:write` with a separate ceiling
  (Tier 2 only, interactive approval required).
- **Per-skill revocation list.** The structured grant form
  already has a `max_instances` field; what it doesn't have
  is a deny-list of skill names. Defer to the next ACL pass
  after we see real per-skill-not-just-inventory use cases.

## 10. Acceptance criteria (for the coding worker who picks this up)

A future change is "done" when:

1. `internal/peerscope/registry.go::Known` carries the three new
   entries with `DefaultTier` populated, and the
   `peerscope_consistency_test.go` covers each.
2. `internal/scopes/denial.go` carries `tier_too_low` and
   `no_user_link`; `Valid()` recognises them.
3. `internal/peerscope/enforce.go::CheckPeerScope` exists and is
   the only call site that returns a `Decision`; every existing
   call to `store.HasPeerScope` from a cross-peer stream handler
   routes through it.
4. `users` has `default_org` + `grant_policy` columns; the
   bootstrap path reads `MCPLEXER_SELF_ORG` into
   `users.default_org` and persists the user's policy to
   `users.grant_policy`.
5. `mesh__grant_peer_scope` writes the structured form; the
   bulletproof e2e covers grant + use + revoke + re-grant at
   Tier 1, Tier 2, and Tier 3.
6. The four-section enforcement sites (skill share, registry
   share, server access, memory share stub) all return one of
   the six `scopes.Denial*` codes on rejection; no new untyped
   403.
7. The dashboard's grant picker shows the `default_tier` badge
   and disables out-of-tier peers; the "Peers" page lists
   granted scopes with the granting user.
8. The `/test-mcplexer` bulletproof suite passes end-to-end on
   the branch, and the test for the "legacy peer" path
   (§5.4) is in the suite.

## 11. Cross-references

- ADR 0004 — Skill Capability Enforcement: the "one enforcement
  site per surface" principle carries over.
- `internal/peerscope/registry.go` — the registry we extend.
- `internal/scopes/denial.go` — the typed denial vocabulary we
  extend.
- `internal/consent/consent.go` — the tier resolver we plug
  into `CheckPeerScope`.
- `docs/design/p2p-skill-hub-sync-gaps` — the transport-layer
  sibling; this doc is the ACL layer.
- `docs/adr/0006-server-profiles.md` — orthogonal; server
  profile is what the daemon *is*, not what it shares.
- `docs/mcplexer-features.md` §11 — the existing route-level
  `scope_policy`; we do not duplicate it here.
