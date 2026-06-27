# Full UI Browser Pass - 2026-06-26

## Scope

This pass covered every canonical route in `web/src/App.tsx`, plus the main modal,
drawer, tab, selected-row, and dynamic-detail states that are reachable from those
routes.

Legacy redirect routes were not treated as separate screens because they do not
render distinct UI:

- `/connections`
- `/servers`
- `/servers/available`
- `/config/downstreams`
- `/config/routes`
- `/config/auth-scopes`
- `/config/oauth-providers`
- `/config/workspaces`
- `/descriptions`
- `/install`
- `/advanced/routes`

## Method

- Route inventory came from `web/src/App.tsx`.
- Browser checks were run at desktop `1440x900` and narrow/mobile `390x844`.
- The DOM/layout probe checked:
  - page-level horizontal overflow
  - visible headings
  - unlabeled visible inputs, selects, and textareas
  - clipped visible text
  - undersized visible interactive controls
  - console warnings/errors
- Key interactive states were opened manually: task create, task edit, memory detail drawer, audit selected row, pairing tabs, task detail, worker detail, brain deep links.

Note: the `brw_chromium` browser surface hit MCPlexer's browser-origin guard for
`localhost` and returned `cross-origin browser request denied`. Playwright through
MCPlexer was used for the actual route pass. Screenshot capture was flaky in this
session, so this report is based on live DOM, layout metrics, console output, and
manual interaction states rather than saved screenshots.

## Scorecard

| Area | Score | Rationale |
| --- | ---: | --- |
| Accessibility | 2 / 4 | Several important controls have visible text nearby but no programmatic label. The memory detail sheet also emits a missing description warning. |
| Responsive layout | 2 / 4 | No route had page-level horizontal overflow, but the task edit dialog clips fields on mobile and dense pages have many sub-44px controls. |
| Usability | 2 / 4 | The app is functional, but task/memory/audit operations are dense and some mobile controls are hard to target. |
| Theming/design consistency | 4 / 4 | The square, dark, dense operational style is consistent and mostly restrained. No major "AI generated" visual anti-patterns. |
| Runtime health | 3 / 4 | Most routes are quiet. Worker cost emits a Recharts size warning. |

Overall: 13 / 20. The shell and route coverage are solid, but there are a few
high-impact polish and accessibility fixes before this feels reliable on mobile.

## Executive Summary

P0 findings: 0.

P1 findings: 4.

P2 findings: 7.

P3 findings: 3.

The biggest issue is the task edit modal on mobile: the dialog shell fits, but
the editable fields inside it render at desktop widths and clip horizontally.
That is a real usability defect. The next largest class is missing programmatic
labels: the UI often displays a nearby label, but the input/select/textarea is
not associated with it, so automated accessibility and keyboard/screen-reader
behavior are weaker than they should be.

There was no page-level horizontal overflow on any canonical route in the
desktop or mobile pass.

## Priority Findings

### P1 - Task edit dialog clips fields on mobile

Route/state:

- `/tasks/:id`
- open "Edit task" modal
- viewport `390x844`

Evidence:

- Dialog outer box: approximately `374px` wide at `x=8`.
- Inner controls measured around `825px` wide.
- Affected fields included title, description, status, meta, and other form controls.

Likely source:

- `web/src/pages/tasks/TaskEditDialog.tsx:305`
- `web/src/pages/tasks/TaskEditDialog.tsx:306`
- `web/src/pages/tasks/TaskEditDialog.tsx:354`
- `web/src/pages/tasks/TaskEditDialog.tsx:391`

Recommendation:

- Make dialog content explicitly mobile-constrained with `w-[calc(100vw-...)]` or equivalent.
- Add `min-w-0` to grid/form children.
- Ensure `Input`, `Textarea`, and custom tag containers cannot inherit a desktop min width.
- Re-test both create and edit modes at `390px`.

### P1 - Task create/edit field labels are not associated with controls

Route/state:

- `/tasks/all`, open "New task"
- `/tasks/:id`, open "Edit task"

Affected controls include workspace, title, description, status, due, tags,
compose-into, meta, and in some states assignee.

Likely source:

- `web/src/pages/tasks/TaskEditDialog.tsx:317`
- `web/src/pages/tasks/TaskEditDialog.tsx:335`
- `web/src/pages/tasks/TaskEditDialog.tsx:345`
- `web/src/pages/tasks/TaskEditDialog.tsx:537`

The local `Field` helper renders a visual `Label`, but it does not pass `htmlFor`
to a corresponding input id.

Recommendation:

- Change `Field` to accept `htmlFor`.
- Give each input/select/textarea a stable `id`.
- For composite controls, use `aria-labelledby` or fieldset/legend semantics.

### P1 - Memory detail drawer is missing a dialog description

Route/state:

- `/memory/all?selected=...`

Console warning:

- `Warning: Missing Description or aria-describedby={undefined}`

Likely source:

- `web/src/pages/memory/MemoryDetailDrawer.tsx:200`
- `web/src/pages/memory/MemoryDetailDrawer.tsx:202`
- `web/src/pages/memory/MemoryDetailDrawer.tsx:206`

Recommendation:

- Add `SheetDescription`, or pass an explicit `aria-describedby`.
- If the description is visually redundant, render it in an sr-only style.

### P1 - Several pages lack a visible page heading

Routes:

- `/advanced`
- `/advanced/credentials`
- `/advanced/oauth-providers`
- `/advanced/descriptions`
- `/workspace-links`

Likely sources:

- `web/src/pages/config/ConfigPage.tsx:84`
- `web/src/pages/config/ConfigPage.tsx:122`
- `web/src/pages/config/LinkedWorkspacesPage.tsx:105`

Recommendation:

- Add a compact page header/landmark before tab content.
- Keep it dense and operational; no hero copy is needed.

## Secondary Findings

### P2 - Mobile hit targets are frequently below 44px

This is systemic. The mobile nav trigger is `32x32` on every screen, and dense
pages such as tasks, mesh, skills, signals, memory consolidation, and worker
cost contain many small row actions, links, tabs, and icon buttons.

Recommendation:

- Keep the desktop density, but introduce mobile-specific touch sizing.
- Prioritize controls that mutate state, open drawers, or navigate.
- Good first targets: mobile nav trigger, task row actions, mesh row actions,
  signals actions, skill action buttons, guard tabs, and memory consolidation actions.

### P2 - Worker cost chart logs a runtime sizing warning

Route:

- `/workers/cost`

Console warning:

- `The width(-1) and height(-1) of chart should be greater than 0`

Likely source:

- `web/src/pages/workers/WorkerCostDashboardPage.tsx:401`
- `web/src/pages/workers/WorkerCostSparkline.tsx`

Recommendation:

- Give sparkline containers a stable nonzero width and height before mounting
  `ResponsiveContainer`, or gate chart rendering until dimensions exist.

### P2 - Mesh message body can clip on mobile

Route:

- `/mesh`

Observed clipped text example:

- a message paragraph beginning `TASK REVIEW 3/N: Phase1 Sender...`

Likely source:

- `web/src/pages/MeshPage.tsx`

Recommendation:

- Apply `min-w-0`, `break-words`, and overflow handling to the message content
  column and markdown wrapper.

### P2 - Dry Run select text clips on mobile

Route:

- `/dry-run`

Observed clipped control:

- workspace/server select with long option text.

Likely source:

- `web/src/pages/DryRunPage.tsx`

Recommendation:

- Add stronger `min-w-0`, `max-w-full`, and truncation/tooltip treatment for
  long select option displays.

### P2 - Search/filter controls are often unlabeled

Routes:

- `/audit`
- `/setup`
- `/workspaces`
- `/workers`
- `/delegations`
- `/mesh`
- `/pairing?tab=people`
- `/tasks/all`

Recommendation:

- Give every search/filter control either a visible associated label or an
  `aria-label` that describes the searched entity.
- Do not rely on placeholder text as the accessible name.

### P2 - Dense operational screens need stronger mobile hierarchy

Routes most affected:

- `/signals`
- `/tasks/all`
- `/mesh`
- `/skills`
- `/workers/cost`
- `/memory/consolidation`
- `/memory/conflicts`

These screens do not break, but they compress too much functionality into small
rows and tiny actions on mobile.

Recommendation:

- Use stacked row layouts on mobile.
- Move secondary metadata into a second line.
- Keep destructive or state-changing actions full-size enough to hit reliably.

### P2 - Advanced descriptions has unlabeled textareas

Route:

- `/advanced/descriptions`

Recommendation:

- Label the override textareas with the corresponding built-in description name,
  using `htmlFor`/`id` or `aria-labelledby`.

## P3 Polish

### P3 - Global "Copy config path" is undersized on desktop

Observed on many pages:

- `configure-with-ai-copy-path`, approximately `183x27`

Recommendation:

- This is not urgent, but it should match the app's standard button height.

### P3 - The active nav indicator appears as clipped text in automation

Observed as probe noise:

- `span 8/12`

This appears to be the active marker rather than a user-visible content issue.
No immediate fix needed unless visual inspection shows it bleeding into text.

### P3 - Some empty states are correct but visually repetitive

This is minor. Empty states across approvals, worker approvals, embeddings, and
conflicts are clear, but could eventually share a tighter reusable pattern.

## Screen-by-Screen Review

| Area | Route/state | Desktop | Mobile/narrow | Notes |
| --- | --- | --- | --- | --- |
| Dashboard | `/` | Pass | Minor density | No page overflow. Dashboard table cells can clip long tool names on mobile. |
| Harness setup | `/harness-setup` | Pass | Minor touch sizing | No overflow. |
| Quick setup | `/setup` | Unlabeled search | Unlabeled search | Search input needs accessible name. |
| Audit | `/audit` | Unlabeled facet filters | Unlabeled facet filters | Main audit table/rail fits. Filter controls need labels. |
| Audit selected row | `/audit?selected=...` | Same as audit | Same as audit | Selecting a row did not introduce overflow. |
| Approvals | `/approvals` | Pass | Pass | No major issue found. |
| Worker approvals | `/worker-approvals` | Pass | Pass | No major issue found. |
| Mesh | `/mesh` | Very dense | Dense, clipped message text | Search input unlabeled. Message body needs stronger wrapping. |
| Skills | `/skills` | Dense but stable | Many small controls | No overflow, but mobile action density is high. |
| Signals | `/signals` | Dense | Very dense | No overflow, but many small actions on mobile. |
| Tasks command | `/tasks` | Pass | Minor density | No major issue found. |
| Tasks all | `/tasks/all` | Dense, unlabeled search | Very dense | Row actions are numerous. Search needs label. |
| Task create modal | `/tasks/all` -> New task | Works | Fits but labels missing | No internal clipping seen in create state, but labels are not programmatically associated. |
| Task offers | `/tasks/offers` | Pass | Pass | No major issue found. |
| Task detail | `/tasks/:id` | Pass | Minor density | Append-note textarea lacks accessible label. |
| Task edit modal | `/tasks/:id` -> Edit task | Works | Fails responsive layout | Fields render wider than modal and clip. P1. |
| Memory landing | `/memory` | Pass | Minor density | No overflow. |
| All memories | `/memory/all` | Pass | Pass | No major issue found. |
| Memory detail drawer | `/memory/all?selected=...` | Missing dialog description | Missing dialog description | Drawer fits, but Radix warning indicates missing description. |
| Memory activity | `/memory/activity` | Pass | Minor touch sizing | No overflow. |
| Memory about entity | `/memory/about/:kind/:id` | Pass | Pass | No major issue found. |
| Shared memory | `/memory/shared` | Pass | Pass | No major issue found. |
| Memory consolidation | `/memory/consolidation` | Dense | Dense touch targets | No overflow, but mobile actions are small. |
| Memory embeddings | `/memory/embeddings` | Pass | Pass | No major issue found. |
| Memory conflicts | `/memory/conflicts` | Pass | Dense touch targets | No overflow, but mobile actions are small. |
| Guards overview | `/guards` | Pass | Pass | No major issue found. |
| Shell guard | `/guards/shell` | Unlabeled rule controls | Unlabeled rule controls | Rule form controls need labels. |
| Sanitizer guard | `/guards/sanitizer` | Pass | Pass | No major issue found. |
| Guard schedule | `/guards/schedule` | Pass | Dense touch targets | No overflow. |
| Sandbox guard | `/guards/sandbox` | Pass | Pass | No major issue found. |
| Workers list | `/workers` | Unlabeled search | Dense | Search input needs accessible name. |
| Worker detail | `/workers/:id` | Pass | Pass | No major issue found. |
| Worker edit | `/workers/:id/edit` | Pass | Dense form | No overflow, but mobile controls are compact. |
| Worker create | `/workers/new` | Pass | Dense form | No overflow, but mobile controls are compact. |
| Delegations | `/delegations` | Unlabeled search | Dense | Search input needs accessible name. |
| Delegation models | `/delegations/models` | Pass | Minor density | No major issue found. |
| Worker cost | `/workers/cost` | Chart warning, very dense | Chart warning | Recharts size warning needs fix. |
| Model leaderboard | `/workers/model-leaderboard` | Pass | Minor density | No major issue found. |
| Model providers | `/model-providers` | Pass | Pass | No major issue found. |
| Workspaces command | `/workspaces` | Unlabeled search | Unlabeled search | Search input needs accessible name. |
| Workspace routes | `/workspaces/routes` | Pass | Minor density | No overflow. |
| Workspace manage | `/workspaces/manage` | Pass | Dense | No overflow. |
| Workspace links | `/workspace-links` | Missing heading | Missing heading | Needs page-level heading/landmark. |
| Advanced credentials | `/advanced`, `/advanced/credentials` | Missing heading | Missing heading | Tabs render without page-level heading. |
| Advanced OAuth providers | `/advanced/oauth-providers` | Missing heading | Missing heading | Same shell issue. |
| Advanced descriptions | `/advanced/descriptions` | Missing heading, unlabeled textareas | Missing heading, unlabeled textareas | Needs heading and textarea labels. |
| Pairing people | `/pairing?tab=people` | Unlabeled person input | Unlabeled person input | People/devices UI fits. Person creation input needs label. |
| Pairing devices | `/pairing?tab=devices` | Pass | Pass | No major issue found. |
| Create MCP | `/create-mcp` | Unlabeled wizard fields | Unlabeled wizard fields | Name, description, and URL fields need associated labels. |
| Dry run | `/dry-run` | Unlabeled controls | Unlabeled controls, clipped select text | Select/input labels and long option display need work. |
| Settings | `/settings` | Pass | Pass | No major issue found. |
| Backups | `/backups` | Pass | Minor touch sizing | No overflow. |
| Brain overview | `/brain` | Pass | Pass | No major issue found. |
| Brain browse | `/brain/browse` | Pass | Pass | No major issue found. |
| Brain entity deep link | `/brain/browse/:ws/:kind/:id` | Pass | Pass | Deep link renders without overflow. |

## Positive Findings

- No canonical route had page-level horizontal overflow at desktop or `390px`.
- The shell, left nav, and main content constraints are generally consistent.
- The visual language matches the product: dark, square, dense, operational.
- The app avoids obvious marketing-page or AI-slop UI patterns.
- Dynamic detail states for memory, tasks, workers, and brain deep links all rendered.
- Most issues are localizable to form helpers, mobile density, and a small number
  of layout containers.

## Suggested Fix Order

1. Fix the task edit modal mobile clipping.
2. Fix the shared `Field`/label pattern in task modals and similar form helpers.
3. Add the missing memory drawer description.
4. Add page headings to advanced/config and workspace links.
5. Add accessible names to all search/filter controls.
6. Stabilize worker cost sparkline dimensions.
7. Do a mobile touch-target pass on dense list/action screens.

## Verification Targets After Fixes

- Desktop: `1440x900`
- Mobile: `390x844`
- Routes:
  - `/tasks/all`
  - `/tasks/:id`
  - `/memory/all?selected=...`
  - `/audit`
  - `/mesh`
  - `/workers/cost`
  - `/advanced/descriptions`
  - `/workspace-links`
  - `/dry-run`
- Checks:
  - no page or modal horizontal overflow
  - no visible input/select/textarea without accessible name
  - no Radix dialog/sheet description warning
  - no Recharts negative-size warning
  - critical mobile controls at least 44px tall/wide, or intentionally exempt

