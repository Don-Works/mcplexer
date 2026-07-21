# P2P collaboration: people, machines, workspaces, and task visibility

This design turns P2P from device pairing plus implicit scopes into an operator
workflow for inviting collaborators and machine publishers. The security model
is defined in `docs/adr/0008-p2p-principals-and-workspace-collaboration.md`.

## Product outcomes

An operator must be able to complete these workflows without editing JSON or
copying libp2p peer IDs:

1. Add a person from an OpenSSH public key, grant read access to selected
   workspaces, and send a single-use invitation.
2. Add a person's second laptop without duplicating the person or grants.
3. Add a monitor server as a machine principal and grant it publish-only access
   to an operations workspace.
4. Let authorized people keep local task mirrors, create tasks, and submit
   base-checked task edits when their explicit capabilities allow it.
5. Let a person create a task locally and publish it to a shared workspace,
   including while temporarily offline.
6. Make a task private, visible to the workspace, or restricted to named
   collaborators without letting an agent exceed workspace policy.
7. Revoke one device, one workspace grant, or an entire principal and see the
   exact forward-security consequence before confirming.

## Information architecture

The canonical route is `/workspaces/access`, labelled **Access** in the
Workspaces navigation group. Workspace remains the primary mental model. The
page has two views over the same data:

- **Matrix**: principals as rows and workspaces as columns, for comparing and
  editing effective capabilities.
- **Principals**: people and machines with expandable keys and devices, for
  invitations, rotation, and revocation.

The task list and detail surfaces show visibility and synchronization state,
but do not become a second access-management UI.

## Matrix page

The desktop matrix is a flat, dense table. The principal column is sticky and
each workspace column is horizontally scrollable. People and machines are
separate row groups. A principal row shows display name, kind, status, and a
small active-device count. Expanding the row shows devices and fingerprints
without adding another card layer.

Each cell shows the actual effective capability set using short, documented
labels rather than one role name:

```text
See  Read  Create  Edit  Publish
```

Common presets appear only in the cell editor:

| Preset | Explicit capabilities |
| --- | --- |
| Viewer | workspace view, task read |
| Contributor | Viewer plus local create-and-publish |
| Editor | Contributor plus base-checked task edits |
| Publisher | Constrained sanitized publish only; no discovery or read |
| Custom | Any other explicit combination |

The API currently advertises only these five capabilities. Reserved vocabulary
for comments, assignment, deletion, evidence, delegation, resharing, and
administration is deliberately not exposed until an end-to-end wire operation
exists for each capability.

Selecting a cell opens an inspector dialog containing presets and exact
capability checkboxes. Workspace policy is opened separately from the workspace
column header. Destructive principal and device actions require an explicit
control and are recorded in the collaboration audit log.

The matrix uses the existing dark, square, restrained design system. It does
not introduce nested cards, decorative status color, rounded controls, or a
custom grid interaction. A semantic HTML table remains available to screen
readers; cells have labels such as “Monitor server, Production operations,
Read off, Publish on.”

On narrow screens the matrix becomes the Principals list with expandable
workspace rows. It does not squeeze dozens of checkboxes into a mobile table.

## Add person

The primary action is **Invite person**.

1. Enter a display name and paste an OpenSSH `ssh-ed25519` public key.
2. MCPlexer validates the key on submission. Only `ssh-ed25519` is accepted;
   invalid, duplicate, and revoked keys produce an explicit error.
3. Select initial workspaces and presets or capabilities. The default is no
   access.
4. Create a single-use invitation, verify the returned SHA256 fingerprint and
   exact selected profiles, then copy its invitation code through a private
   channel. Raw evidence and unrestricted task metadata are never included in
   the current task-sync protocol.

The one-time result shows expiry and fingerprint. After proof succeeds, the
principal list shows the active key and newly bound device. Invitation-history
management is follow-up UI; the plaintext invitation token is intentionally
not recoverable from storage. The UI never claims that a display name or copied
invitation alone is verified.

If a person has no suitable key, the operator can create one with:

```text
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519_mcplexer -C "mcplexer identity"
```

Private key material is never pasted into the dashboard.

## Add machine

**Add machine** uses the same public-key proof. The UI asks for a purpose label,
such as “production log monitor,” and offers the machine-publisher profile in
each selected workspace.

The recommended monitor setup grants only constrained `tasks.publish` into a
selected operations workspace. The machine generates a dedicated key, loads it
into its local non-forwarded SSH agent, and joins with the invitation. Its SSH
host key and logwatch login key are not offered as shortcuts. With publish-only
access it can submit sanitized tasks and receive acknowledgements, but cannot
browse team tasks.

## Task visibility

The task detail visibility control uses one subdued badge:

- Private
- Workspace
- Restricted · N people

The task detail page contains a Visibility control for the home-authoritative
task. It explains that narrowing prevents future delivery but cannot recall
copies already received. Disclosure receipts remain available to the backend
audit path; a complete delivery-history UI is follow-up work.

The home-side visibility API accepts `visibility` and
`audience_principal_ids`. A remote content edit preserves the accepted home
visibility and cannot widen it. Agent-facing tool descriptions tell the model:

- omit visibility to use the workspace default;
- choose private when context may contain sensitive data;
- request restricted/workspace only when collaboration materially helps;
- expect a denial or local approval when widening is outside policy.

The daemon, not the prompt, enforces the rule. A model cannot make a private
workspace publishable by describing its intent differently.

## Workspace publication policy

Each workspace has an explicit policy edited from the permissions page:

- default visibility for human and agent-created tasks;
- maximum visibility an agent may choose without approval;
- whether widening an existing task always needs approval;
- machine publishers are authorized through their exact `tasks.publish` grant
  and published tasks receive the workspace's forced default visibility;
- the named egress sanitizer profile (task sync always omits evidence, notes,
  attachments, and unrestricted metadata in the current protocol);
- workspace home device and current access epoch.

The safe initial default is private, agent ceiling private, and no grants. An
operations workspace can deliberately use workspace-visible sanitized
summaries and grant a named monitor principal `tasks.publish`. Attempts to
enable remote evidence are rejected because that feature is not implemented.

## Collaborative task state

The implemented protocol has explicit accepted, pending, rejected, and
conflict offer states. Offline publications stay in a durable outbox and retry
only after an authenticated reconnect and membership refresh. A remote edit
includes the last home revision it observed; if that revision is no longer
current, the home rejects the whole edit as a conflict and leaves the canonical
task unchanged. The safe resolution flow is sync, review the new home state,
reapply the intended edit, and publish again.

The permissions page shows local mirror status, exact capabilities, access
epoch, and a manual sync action. Rich per-field conflict comparison and task
header badges are useful follow-up UI, but the protocol intentionally does not
claim field-level merge or append-only remote notes today.

## Revocation

Revocation is available at three levels:

- **Device**: blocks one laptop/server peer; the principal and other devices
  remain active.
- **Workspace access**: removes selected capabilities and stops future
  synchronization for that workspace.
- **Principal**: revokes all keys, devices, invitations, and grants.

The confirmation boundary must make the consequence clear: future home reads
and writes stop as soon as the home commits the revocation; a remote mirror
learns the newer epoch on its next authenticated sync. Previously received task
content cannot be remotely erased. No purge acknowledgement is treated as a
security guarantee, and the current implementation does not claim remote purge.

## Empty, loading, and error states

- Loading uses table skeleton rows sized to the final matrix.
- No principals explains why pairing is not collaboration and leads to Invite
  person or Add machine.
- No shared workspaces leads to workspace selection, not an empty matrix.
- A legacy-unverified peer appears in a migration section with Verify identity
  and Remove actions; it receives no implicit grant.
- Partial save failures keep the inspector open, preserve selections, and show
  the stable backend error code plus actionable copy.
- An offline home node permits durable task-publication queuing. Grant changes
  remain home-authoritative and are learned by mirrors only through a
  successful authenticated sync.

## Audit vocabulary

The backend collaboration ledger records explicit invitation creation and
revocation, device activation and revocation, key revocation, principal
revocation, workspace grant changes, publication-policy changes, and task
visibility changes. Task disclosures have a separate immutable receipt carrying
the recipient principal/device, projection hash, and access/visibility epochs;
offer rows retain accepted, conflicted, declined, and rejected outcomes.

Rows name the resolved actor principal and, when the action arrived over P2P,
the bound device. Agent session and tool-call IDs remain supplementary
provenance, not identity authority. A dedicated collaboration activity-feed UI
is follow-up work; the current permissions page focuses on effective state.

## Acceptance scenarios

1. A person with Viewer access receives workspace-visible tasks locally but
   cannot mutate, assign, reshare, trigger a worker, or fetch evidence.
2. The same person adds a second laptop by proving the same key; revoking the
   first laptop does not affect the second.
3. A monitor machine publishes a sanitized task without gaining read access to
   any existing task.
4. A task created privately on a laptop never appears on another principal's
   mirror.
5. A restricted task appears only for named principals who also retain
   `tasks.read`.
6. A model requesting workspace visibility in a private-ceiling workspace is
   denied or routed to approval.
7. A contributor creates a task while the home is offline; the durable outbox
   retries after authenticated reconnect and it becomes canonical only after
   home acknowledgement.
8. Any edit based on a stale home revision is rejected as a conflict without
   changing the canonical task; sync-and-reapply succeeds against the new base.
9. Revoking a workspace grant stops later revisions and rejects stale queued
   writes at the old epoch.
10. Legacy paired peers and old linked-workspace rows receive no capability
    until explicitly migrated.
