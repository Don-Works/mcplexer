# P2P collaboration security review and operator guide

Date: 2026-07-16

Decision record: [ADR 0008](adr/0008-p2p-principals-and-workspace-collaboration.md)

Implementation design: [P2P collaboration](design/p2p-collaboration.md)
Tracking epic: `01KXN49WJM54BQ7HWWC268WTFY`

## Verdict

The collaboration surface is suitable for the intended deployment: one
operator's laptops and servers, explicitly invited teammates, and dedicated
monitor machines. It is safe only when workspace grants are kept narrow,
production evidence stays on its source machine, and every participating
endpoint is treated as a plaintext trust boundary.

It is not a retroactive-secrecy system. An authorized reader can retain
anything already delivered, a compromised endpoint can read its local mirror,
and the workspace home necessarily sees canonical shared task content.
Revocation stops future reads and writes; it cannot erase old copies.

## Security invariants

P2P collaboration now requires all of these independent checks:

1. libp2p authenticates and encrypts the device-to-device transport.
2. A one-time invitation is pinned to one OpenSSH Ed25519 public key.
3. The joining daemon proves possession through its local SSH agent. The signed
   transcript binds both live peer IDs, the principal, key, invitation, nonce,
   and short expiry. MCPlexer never receives the private key.
4. The connected peer must have an active device binding to an active person
   or machine principal. A claimed user ID, display name, organisation string,
   old peer scope, or linked-workspace row grants nothing.
5. The principal must hold the exact capability for the workspace. Grants are
   allow-only and do not support wildcards.
6. Reads must also pass the individual task's visibility rule.
7. Only the explicit safe task projection crosses the wire. Evidence, log
   samples, attachments, notes, and unrestricted metadata stay local.

Invitation bearer tokens are random, single-use, expiring, and stored only as
SHA-256 digests. Possessing the invitation without the pinned private key is
insufficient. Replays and peer/transcript substitution fail.

## Effective permission matrix

The UI exposes only capabilities backed by a current authenticated wire
operation:

| Profile | Exact capabilities | Intended use |
| --- | --- | --- |
| Reader | `workspace.view`, `tasks.read` | A teammate such as Morgan can keep a local read mirror |
| Contributor | Reader + `tasks.create` | Create a local task and explicitly publish it home |
| Editor | Contributor + `tasks.edit` | Also submit edits against the last synced home revision |
| Machine publisher | `tasks.publish` only | A log monitor can publish sanitized findings but cannot browse tasks |
| No access | none | Default for every principal/workspace pair |

Comments, assignment, deletion, evidence access, worker triggering, resharing,
and delegated administration are intentionally not shown. Their schema names
are reserved, but granting them through the collaboration API is rejected until
the corresponding P2P operation exists.

## Task visibility

Workspace access and task visibility intersect; neither replaces the other:

```text
effective read = active device
              AND active principal
              AND workspace.view
              AND tasks.read
              AND task visibility includes that principal
```

- **Private**: only the owning principal's active devices and the local
  workspace owner can see the task.
- **Named people**: only selected principals who also retain Reader access.
- **Workspace**: all principals with both read capabilities.

An agent may choose visibility only beneath the workspace's agent ceiling.
Widening an existing task can require a human action in the task UI. Remote
content edits preserve the home task's accepted visibility, and a joined mirror
cannot change visibility locally; the workspace home owns that decision.

## Add a teammate such as Morgan

1. On the workspace-home daemon, open **Workspaces → Access** and enroll the
   local device with a dedicated `ssh-ed25519` key already loaded in
   `ssh-agent`.
2. Ask Morgan for the public `.pub` line, never the private key.
3. Choose **Add person**, paste the public key, and select Reader, Contributor,
   or Editor independently for each workspace. Unselected cells remain empty.
4. Verify the returned SHA256 fingerprint with Morgan and send the one-time
   invitation through a private channel.
5. Morgan loads the matching key into the SSH agent used by their MCPlexer daemon,
   chooses **Join with invite**, and names the device. A local workspace mirror
   is created only after proof succeeds.
6. Set appropriate home tasks to Workspace or Named people. Morgan's daemon
   pulls allowed revisions periodically, on reconnect, or through **Sync**.
7. For a second laptop, choose **Add device** on Morgan's principal. It proves
   the same active key but receives a separately revocable device binding.

Creating or editing on a joined daemon is deliberately two-stage: make the
local change, then call `task__publish_home`. If the home is offline the
publication stays in a durable outbox and retries after authenticated sync.
It becomes canonical only after the home acknowledges it.

## Add a log-monitor machine

1. Generate a dedicated Ed25519 key for the monitor. Do not reuse its SSH host
   key, production-login key, or a human key. Load it into a local,
   non-forwarded SSH agent available to the daemon.
2. Choose **Add machine**, use a purpose label such as `client-a log monitor`,
   and select **Machine publisher** only for the intended operations workspace.
3. Configure that workspace's default visibility. Machine publications are
   forced to this home policy; the model cannot widen them.
4. Join from the server and have logwatch create a sanitized local task, then
   invoke `task__publish_home`. The server receives an acknowledgement but no
   task index or existing task bodies.
5. Keep raw evidence referenced locally on the monitor. Wire v1 always omits it
   even if a caller asks to share it.

Use a separate machine principal per monitor or client boundary. That makes a
single server revocable without affecting a person or another monitor.

## Synchronization and conflicts

Each shared workspace has one authoritative home and a stable `share_id`.
Every member maps it to its own local workspace ID. Before task revisions, the
home sends a proof-bound access receipt containing the exact current
capabilities and access epoch. This is how a remote UI learns reductions and
revocation; cached permissions never authorize the home.

Edits carry the last home HLC observed by the mirror. The home uses strict
compare-and-set semantics: a stale base produces an explicit conflict and does
not modify the canonical task. The safe workflow is sync, review, reapply, and
publish. This is conservative by design; wire v1 does not pretend to merge
fields or notes.

Pending publications are durable. A reconnect first authenticates and refreshes
membership, then retries with a new nonce, current epoch, and current base.
If the required capability was removed, the pending publication is declined
locally rather than unexpectedly resurfacing after a later re-grant.

## Revocation and key rotation

- Revoking a device blocks that peer while leaving the person's other devices.
- Revoking a key blocks every device proven with that key.
- Revoking a principal revokes its keys, devices, pending invitations, and all
  workspace grants.
- Removing a workspace cell advances the home access epoch. The remote mirror
  learns the empty/reduced capability set on its next authenticated sync.
- Rotating a key creates another proof-bound one-time invitation. The same
  device can rebind after proving the new key. Revoke the old key only after all
  intended devices have moved.

Security-sensitive invitation, identity, grant, policy, revocation, and
visibility mutations enter the collaboration audit ledger. Task disclosures
write immutable recipient/device/revision receipts without storing task
content in the receipt.

## Known limits

- Only `ssh-ed25519` identity keys are accepted in wire v1.
- The SSH agent and unattended machine-key lifecycle are endpoint operator
  responsibilities.
- The home must eventually be reachable for writes to finalize. Signed home
  authority transfer is not implemented.
- Previously disclosed plaintext cannot be remotely purged.
- Remote notes/comments, assignment, deletion, attachments/evidence, worker
  triggers, and delegated workspace administration are not implemented.
- Conflict storage and rejection are implemented; a rich field-by-field
  conflict editor and collaboration activity-feed UI are not.
- Endpoint/root compromise, an unlocked stolen identity key, and a malicious
  authorized reader are outside the protection boundary.

## Verification

The self-contained gate builds fresh P2P-enabled daemons and independent SSH
agents in Docker:

```bash
make test-collaboration-integration
```

It verifies identity proof, invitation replay rejection, exact human/machine
grants, local mirrors, private-task non-disclosure, credential redaction,
publish-only monitors, offline outbox recovery, device revocation, grant-epoch
refresh, exact re-grant behavior, and same-device SSH-key rotation. The current
scenario contains 38 assertions and must finish with zero failures or skips.
