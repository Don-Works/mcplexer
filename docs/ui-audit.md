# UI AI-Accessibility Audit (M51 Phase 1)

Lens: every page must be drivable by an AI agent as easily as a human. Stable
identifiers, keyboard reachability, and deep-linkable URLs are the bar.

Convention: `data-testid="<noun>-<verb>"` (e.g. `secret-submit`, `route-delete`).
Nav links use `nav-<route-segments>` and are already in place.

---

## Sidebar / Layout — `src/components/layout/AppLayout.tsx`

JTBD: Navigate to any page. Toggle Configuration section. Open mobile sheet.

- Already has `data-testid="nav-..."` per route. Done.
- Mobile menu button (icon-only `Menu`) — needs `aria-label="Open navigation"`.
- Desktop Configuration toggle button has visible text — fine.

---

## DashboardPage — `/`

Route: `/` | JTBD: at-a-glance view of sessions, errors, latency, recent calls;
drill into a record. Key actions: change time range (1h/6h/24h/7d), click pending
approvals banner, click recent-call row, change selected workspace.

Pain points (AI-driveability):
- Time-range toggle has no test ids — added `dash-range-{1h|6h|24h|7d}`.
- "Pending approvals" banner is a `<Link>` not labeled distinctly.
- Time-range NOT deep-linkable (`?range=24h`). **Pick for deep-link upgrade.**

---

## QuickSetupPage — `/setup`

Route: `/setup` | JTBD: connect a downstream HTTP integration via OAuth in 3-4
steps. Multi-step wizard (`pick → configure → workspace → review → connecting →
success`). Already syncs OAuth result via `?oauth=success`.

Pain points:
- Step state held only in component; cannot land on step 3 by URL. (Out of scope
  for phase 1 — needs router state.)
- Buttons (Next, Connect, Cancel/Back) lack stable ids — added.
- Server-pick cards are `<button>`s — add `data-testid="setup-pick-${ds.id}"`.

---

## AuditPage — `/audit`

Route: `/audit` | JTBD: filter and read tool-call audit log; pivot by session or
execution. Already supports `?execution_id=...&session_id=...`.

Key actions: change workspace, status, tool-name filter, paginate, click row to
inspect, click session/execution badge to pivot, clear filters.

Pain points:
- Workspace/status/tool/before/after filters NOT in URL. **Highest-traffic page
  on the app — pick for deep-link upgrade.**
- Pagination chevrons are icon-only — `aria-label` added.
- Filter inputs lack ids — added.

---

## ApprovalsPage — `/approvals`

Route: `/approvals` | JTBD: approve/deny pending tool calls within timeout; see
recent history. Key actions: type reason, click Approve/Deny, expand args.

Pain points:
- Approve/Deny rendered per-card; needed `data-testid="approval-approve"` /
  `approval-deny"` keyed on approval id. Added.
- "Arguments" disclosure button is keyboard-reachable already.
- Reason input had no id — added.

---

## MeshPage — `/mesh`

Route: `/mesh` | JTBD: see active agents and recent inter-agent messages.
Mostly read-only (no actions besides auto-refresh). Disabled-state has a single
link to Settings.

Pain points: minimal — added `data-testid="mesh-enable-link"` on the Settings
link rendered in the disabled state.

---

## DescriptionsPage — `/descriptions`

Route: `/descriptions` | JTBD: review AI-suggested tool description changes;
accept/reject; view history; restore older versions.

Key actions: type review note, Accept, Reject, Restore (icon button), filter by
status (chip group).

Pain points:
- Filter chips (`All / active / pending / superseded / rejected`) NOT
  deep-linkable. **Pick for deep-link upgrade** (`?status=pending`).
- Accept / Reject / Restore buttons had no ids — added.
- Filter chip array lacks ids — added.

---

## PairingPage — `/pairing`

Route: `/pairing` | JTBD: see paired devices, revoke them, pair a new one (show
or enter a 6-digit code).

Already has `pair-show-code-btn` and `pair-submit-btn`. Revoke button on each
peer row is icon-only; row has no stable id (only display name).

Pain points:
- Revoke button on `PeerRowItem` had no test id — added `peer-revoke` keyed by
  `peer_id`.
- Modal triggers ("Pair this device", "Enter code") could open via
  `/pairing/show` and `/pairing/enter` — out of scope for phase 1.
- Modal submit / cancel buttons in `ShowCodeModal` / `EnterCodeModal` — added.

---

## InstallMCPPage — `/install`

Route: `/install` | JTBD: install/uninstall MCPlexer into an MCP client (Claude
Desktop, Cursor, etc.). Already has `mcp-install-btn`.

Key actions: click Install on a tool card → preview dialog → confirm. Uninstall.
Copy raw JSON for "Other" tools.

Pain points:
- Install/Uninstall buttons keyed by client id needed (multiple cards). Added
  `data-testid="mcp-install-${id}"` and `mcp-uninstall-${id}`.
- Preview dialog Install/Cancel had no ids — added.

---

## CreateMCPPage — `/create-mcp`

Route: `/create-mcp` | JTBD: scaffold a custom HTTP-based MCP addon by
declaring endpoints. Wizard: basics → auth → endpoints → review.

Pain points: many fields, only Next/Back/Create buttons need stable ids.
Wizard step state not in URL — out of scope for phase 1.

---

## DryRunPage — `/dry-run`

Route: `/dry-run` | JTBD: simulate a tool call against a workspace + downstream
to see how routing decides. Form: workspace, subpath, server, tool, args JSON.

Pain points: form lacks ids — added `dryrun-submit`, `dryrun-discover`.

---

## SettingsPage — `/settings`

Route: `/settings` | JTBD: tweak global settings (slim_surface, slim_tools,
compact_responses, mesh caps, mesh_enabled, log_level, etc.); manage tool
description overrides.

Pain points:
- Switches use plain `<button role="switch">` with `aria-checked` — already
  accessible. Added `data-testid="settings-toggle-${key}"` per switch.
- Top "Save" button — added id.
- "Reset" per-override (icon-only) — added `aria-label`.

---

## Workspaces / Routes / AuthScopes / Downstreams / OAuthProviders (`/config/*`)

JTBD: CRUD configuration objects. Common pattern: list + Add/Edit/Delete in
modals. Each table action (edit, delete, duplicate, key, revoke) is icon-only
with a Tooltip wrapper — these need `aria-label` AND a `data-testid` keyed by
row id.

Surgical adds (per page):
- WorkspacesPage: `workspace-add`, `workspace-edit-${id}`, `workspace-delete-${id}`,
  `workspace-save`, `workspace-cancel`, `workspace-tag-add`.
- RoutesPage: `route-add`, `route-save`, `route-cancel`. Per-route actions live
  inside `RouteWorkspaceGroup` — left for follow-up to keep this PR surgical.
- AuthScopesPage: `auth-scope-add`, `auth-scope-{edit,delete,duplicate,key,authenticate,revoke}-${id}`.
- DownstreamsPage: `downstream-add-custom`, search field id, tab triggers id'd,
  category filter chips.
- OAuthProvidersPage: `oauth-provider-add` plus row actions.

---

## Modals (`Dialog`) — focus management

shadcn/Radix dialogs already trap focus and Esc-close. No change needed. Submit
buttons in each modal got their own ids (e.g. `secret-submit`, `route-save`).

---

## Keyboard nav exceptions

Mouse-only patterns identified:
- WorkspacesPage tag-pill removal: clicking a Badge deletes a tag. Keyboard
  alternative — Badges are not focusable. Not fixed in this pass; flagged for
  follow-up.
- AuditPage row click → opens detail dialog. Row is not a button. Workaround:
  the same dialog opens via session/execution badge if you tab there. Flagged.

Everything else (forms, modals, navigation) is reachable via Tab + Enter, and
Esc closes modals.

---

## Deep-linkable URLs (priority 3)

Audited — top 3 candidates by traffic:
1. **AuditPage** — `?status=&workspace=&tool=&after=&before=` already partially
   deep-linkable (execution_id/session_id). Phase 1: extend to status/workspace.
2. **DashboardPage** — `?range=1h|6h|24h|7d`. Phase 1: implemented.
3. **DescriptionsPage** — `?status=pending|active|...`. Phase 1: implemented.

Out of scope (phase 2 IA work): wizard step in URL, modal-as-route.

---

## Tests

`web/src/__tests__/a11y.test.ts` — Vitest + axe-core, runs each page through
jsdom, asserts no `serious` or `critical` violations on the busiest 3 pages
(Dashboard, Audit, Approvals).
