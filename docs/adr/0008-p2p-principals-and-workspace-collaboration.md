# ADR 0008: Authenticated P2P principals and workspace collaboration

- Status: Accepted for implementation
- Date: 2026-07-16
- Tracking: `01KXN49WJM54BQ7HWWC268WTFY`
- Supersedes: self-reported P2P `user_id`, inferred same-organisation trust,
  device-wide wildcard workspace scopes, and linked-workspace-as-authority

## Decision

MCPlexer will treat network transport, device identity, principal identity,
workspace authorization, and task visibility as five separate decisions.

1. libp2p continues to authenticate and encrypt the connection between two
   device keys.
2. A person or machine is a durable principal. A laptop, server, or daemon is
   a separately revocable device bound to exactly one principal.
3. An OpenSSH-format Ed25519 signing key proves control of a principal. The
   proof binds the key, principal, invitation, connected libp2p peer IDs, a
   nonce, and an expiry in one domain-separated signed transcript.
4. Explicit per-workspace capability grants determine what the authenticated
   principal may do. Pairing alone grants no application access.
5. Per-task visibility determines which otherwise-authorized principals may
   receive a task. A model can choose visibility only within an operator-set
   publication policy.

Shared workspaces are replicated local workspaces with one home node. The home
node is the ordering authority for grants and task mutations. Readers keep
local mirrors. A publish attempted while disconnected is retained as a durable
pending offer and retried after an authenticated home sync. In the current wire
version, edits carry the last observed home HLC and are accepted only when that
base is still current; stale snapshots become explicit conflicts rather than
silently overwriting home state.

## Why SSH keys, but not SSH login trust

OpenSSH keys solve an operator usability problem: people already understand
public keys and fingerprints, SSH agents can prove possession without exposing
private material, and machine keys work unattended. MCPlexer uses the key as a
general signing identity. It does not infer MCPlexer access from
`authorized_keys`, an SSH account, an SSH host key, or access to a repository.

The recommended human setup is a dedicated Ed25519 identity key held in an SSH
agent. Reusing an existing personal signing key is supported but not required.
Machines also use a dedicated Ed25519 identity key loaded into a local,
non-forwarded SSH agent. MCPlexer stores its public key and proof receipt, not
the private key; unattended agent/key lifecycle is therefore an endpoint
operator responsibility. SSH host keys and logwatch login keys are never
silently reused as MCPlexer identity keys.

The initial implementation accepts `ssh-ed25519`. Additional OpenSSH key types
require a separate compatibility and algorithm-policy review. Principal IDs
are random stable identifiers, not fingerprints, so keys can rotate without
changing ownership or grants.

## Trust objects

| Object | Proves or controls | Lifetime |
| --- | --- | --- |
| Principal | A person or machine named by the operator | Stable across keys and devices |
| Identity key | Control of a principal | Rotatable and revocable |
| Device binding | One libp2p peer key is acting for one principal | Revocable independently |
| Invitation | Permission to establish one principal/key/device binding | Single-use and expiring |
| Workspace grant | A principal may perform one capability in one workspace | Explicit, expiring or revoked |
| Task visibility | Which authorized principals may receive one task | Mutable, with disclosure history |

A principal can have multiple active identity keys during a bounded rotation
window and multiple devices. A device has one principal. A machine principal
may have a controlling person principal, but never inherits that person's
workspace grants implicitly.

## Identity and invitation protocol

An invitation records the intended principal kind and display name, the exact
SSH public-key fingerprint, expiry, one-use limit, inviter, and proposed grants.
It contains no private key and does not become authority merely because its ID
is known.

The proof transcript is canonical and domain-separated:

```text
MCPLEXER-DEVICE-BINDING-V1
invitation_id=<id>
principal_id=<id>
principal_kind=<person|machine>
identity_key_fingerprint=<SHA256:...>
initiator_peer_id=<libp2p peer id>
responder_peer_id=<libp2p peer id>
challenge_id=<id>
nonce=<base64url random bytes>
issued_at=<unix seconds>
expires_at=<unix seconds>
```

The responder sends the nonce over the already-authenticated libp2p stream.
The initiator signs the exact transcript through an SSH agent, a locally held
dedicated identity key, or a compatible detached signing flow. The responder
checks the pinned public key, signature algorithm, both live peer IDs, clock
window, unused challenge, unused invitation, and intended principal before one
transaction consumes the invitation and creates the device binding and grants.

The transcript prevents a signature collected for one invitation, device,
peer, or responder from being replayed or substituted elsewhere. Authentication
failures do not reveal whether an unguessable invitation ID exists. Rate limits
apply per remote peer and per invitation.

Adding a second device repeats proof of possession against an invitation made
for the existing principal. Key rotation requires an active old key or an
explicit local-owner recovery approval. Recovery never accepts a display name,
email address, or matching libp2p address as identity proof.

## Workspace capabilities

Grants are allow-only rows. There are no string-prefix wildcards and no
same-user or same-organisation shortcuts. Preset roles are UI conveniences that
write explicit capabilities; authorization evaluates the rows.

The five capabilities below are operational on the collaboration wire in this
implementation and are the only ones advertised by the permissions UI:

| Capability | Meaning |
| --- | --- |
| `workspace.view` | Discover the workspace and its non-sensitive metadata |
| `tasks.read` | Receive task projections permitted by task visibility |
| `tasks.create` | Create ordinary tasks in the workspace |
| `tasks.publish` | Create policy-constrained tasks without general read access |
| `tasks.edit` | Edit mutable task fields |

`tasks.comment`, `tasks.assign`, `tasks.share`, `tasks.delete`, evidence, mesh,
worker-trigger, and delegated workspace administration remain reserved schema
vocabulary. They are deliberately rejected by the collaboration REST/UI layer
until a corresponding authenticated wire operation exists. This avoids a
permission checkbox that appears effective but does nothing. A monitor
publisher normally receives only `tasks.publish`; it does not need workspace
discovery or general task read access.

Every P2P operation resolves:

```text
connected peer
  -> active device binding
  -> active principal and identity-key lineage
  -> active workspace grant for the exact capability
  -> grant constraints and access epoch
  -> task visibility and field classification, when applicable
```

Any missing or revoked link denies the operation. Application payloads may not
supply or override the resolved principal.

## Task ownership and visibility

Tasks gain an owning principal, actor-principal audit fields, and one of three
visibility values:

- `private`: only the owning principal's active devices receive the task.
- `workspace`: every principal with `tasks.read` in the workspace may receive
  the safe task projection.
- `restricted`: only named audience principals that also hold `tasks.read` may
  receive the safe task projection.

The audience never grants capability. The effective readers are the
intersection of an active grant and the visibility rule. The owner remains able
to see its own task. A publish-only machine may receive acknowledgements for its
own mutations without gaining general workspace read access.

Home-side task visibility tools accept explicit visibility and audience fields.
When omitted, ordinary tasks remain private; machine publications receive the
home workspace's configured default. Agents can request narrower visibility
only through the local policy-enforced tool. Remote content edits cannot alter
visibility as a side effect. Out-of-policy requests fail clearly instead of
being silently broadened or clamped.

Changing a task back to a narrower visibility stops future delivery but cannot
erase information already disclosed. The disclosure ledger records every
principal and revision sent.

## Evidence and log-monitor tasks

Task collaboration and raw production evidence are different resources.
P2P projection uses an explicit DTO and egress policy; it never serializes a
database task row wholesale. Title, description, status, priority, due date,
and tags pass a sanitizer before leaving the home node. Evidence references,
log samples, attachments, notes, opaque metadata, and source-specific metadata
are unconditionally omitted in wire v1.

Logwatch creates a sanitized summary and a local evidence reference. A monitor
workspace may be configured by an operator to publish summaries using the
workspace's forced default visibility. The current P2P wire never transfers
evidence, attachments, task notes, or raw log samples. The model cannot opt raw
evidence into a shared task, and recognized-secret redaction is defense in depth
rather than the authorization boundary.

## Shared-workspace authority and synchronization

A shared workspace has a stable `share_id`, one home peer, an owning principal,
an access epoch, and a local-workspace mapping on each member daemon. Grants
refer to `share_id`, not another machine's local database ID.

The home node sequences accepted task mutations. Current wire v1 sends a
sanitized task snapshot with the mirror's last observed home HLC as its base.
The home authorizes the live device, principal, exact grant, and access epoch,
then:

- applies a new task under the home publication policy;
- applies an edit only when its base HLC exactly matches the current home HLC;
- records and returns a typed conflict when the base is stale, leaving the home
  row unchanged; and
- preserves authoritative visibility on every content edit.

This deliberately conservative compare-and-set rule prevents lost updates.
Field-level disjoint auto-merge, note mutations, assignment, tombstones, and a
conflict-resolution editor are deferred to a future mutation protocol; the UI
does not advertise those capabilities today.

Readers pull an authorization-filtered revision stream and keep a durable
cursor. Before task data, the home sends an authenticated access receipt with
the exact current capabilities, share status, and access epoch. This makes
permission changes converge to the joined device and supplies the fresh epoch
used by queued writes. Writers keep pending outgoing offers until the home
acknowledges them; the sync scheduler rotates replay nonces and retries after a
successful authenticated reconnect. Remote and local workspace IDs are mapped
by `share_id`; no protocol request assumes they are equal.

The home node is an availability dependency, not a confidentiality relay: it
is selected from a device controlled by the workspace owner. A monitor or team
server is a good home for an operations workspace. Home-authority transfer is
not implemented in wire v1; changing it requires a future signed, audited
protocol rather than editing local mappings independently.

## Revocation and unlink

Revoking a device blocks that libp2p peer immediately without revoking the
person or machine. Revoking a principal invalidates all its devices and grants.
Revoking or changing a grant increments the workspace access epoch, stops
future fan-out, and rejects queued writes carrying the old epoch. On the next
authenticated sync, the remote membership receives the new exact capabilities
and epoch, making local mirror controls read-only where appropriate. The
current version does not claim remote purge or deletion acknowledgement.

No protocol can force a previously authorized or compromised peer to erase
plaintext it already received. MCPlexer will state this in the permissions UI.
Revocation provides forward access control and audit, not retroactive secrecy.

## Persistence and migration

New storage separates principals, identity keys, device bindings,
invitations/challenges, workspace grants/policies, disclosure records,
workspace share mappings, durable pending offers/cursors, and conflict offer
receipts. Grant and invitation mutations are transactional and append to an
audit ledger.

Upgrade behavior is default-deny:

1. The existing local `users.is_self` row becomes the local owner person
   principal. It receives no device authority until the operator explicitly
   enrolls an Ed25519 public key and proves possession through the local
   SSH agent; MCPlexer then binds the current libp2p device.
2. Existing remote `users` and `peer_users` become `legacy-unverified`
   principals and devices. Their names are retained only as labels.
3. Existing peer scopes and linked workspace bindings do not become grants.
   They remain visible for migration review, and collaboration pauses until an
   operator verifies a key and selects explicit capabilities.
4. Existing tasks become `private`. Their historical peer provenance is
   retained, but upgrade never widens their audience.
5. `MCPLEXER_SELF_ORG` and self-reported remote `user_id` stop affecting
   authorization. Legacy protocol peers can pair at the transport layer but
   receive no workspace access.

## Threat model

This design defends against a peer claiming another user's ID, a stolen pairing
code without the pinned private key, replay and transcript substitution,
grant-by-name mistakes, wildcard scope expansion, a compromised single device
after revocation, unauthorized task fan-out, model-initiated over-sharing,
stale queued writes after revocation, silent concurrent overwrite, and routine
secret leakage from monitor-task projections.

It does not defend against root compromise of an authorized endpoint, theft of
an unlocked principal key, a malicious authorized reader retaining or copying
data, traffic-analysis metadata, or a workspace owner deliberately granting
unsafe access. Local database and model execution security remain endpoint
boundaries.

## Rejected alternatives

### Treat a libp2p peer ID as the user

Rejected because people use multiple devices, machines need distinct policy,
and one lost laptop must be revocable without replacing a person or workspace.

### Trust a claimed user ID or matching organisation string

Rejected because neither is cryptographic proof. It turns a label into an
authorization bypass.

### Reuse SSH host keys or `authorized_keys`

Rejected because they prove control of a host or login path, not the intended
MCPlexer person/machine principal, and their rotation/authorization semantics
are unrelated.

### Let the model decide any visibility value

Rejected because visibility changes are disclosures. The model may choose only
inside a policy and exact capability ceiling set by the operator.

### Peer-to-peer last-write-wins

Rejected because clocks are not authority and silently losing status,
assignment, visibility, or description changes is unacceptable operationally.

### Copy the entire task row to every linked peer

Rejected because local workspace IDs, raw evidence, private metadata, and
implementation-only columns are not a stable or safe wire contract.

## Consequences

- Adding a teammate requires their public key or fingerprint and one proof of
  possession, but thereafter their devices and access are understandable and
  independently revocable.
- Machine publishers become first-class and can be write-only.
- Shared workspaces support safe read mirrors, new-task publishing, and
  base-checked content/status edits while preserving local copies and an
  offline publish queue.
- The home node simplifies ordering and conflict behavior but must eventually
  be reachable for writes to finalize.
- Legacy links stop syncing until migrated. This is intentionally safer than
  silently preserving ambiguous authority.
- The UI must expose effective capabilities and disclosure limits, not a single
  misleading trust level.
