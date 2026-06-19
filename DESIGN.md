# MCPlexer — Design System (in-code)

This document is descriptive: it captures the design language already living in `web/src/`, so new pages match. Tokens come from `web/src/index.css`.

## Color

Deep dark, tinted toward `hue 224` (cool blue-grey) so neutrals never read flat-black. Primary is electric cyan-blue `hsl(205 95% 55%)` — reserved for active state, focus rings, the "alive" shimmer in NowRunningStrip, and selected nav.

**Strategy: Restrained.** Tinted neutrals plus one accent ≤10% of the visible surface. Status tones (critical/high/warn/info/success) are surgical, not decorative.

### Tokens (HSL)
| role | value | usage |
|---|---|---|
| background | `225 14% 5%` | page |
| card | `224 14% 9%` | cards, elevated surfaces |
| muted | `224 10% 13%` | strip backgrounds, fills |
| border | `224 12% 18%` | every divider |
| foreground | `210 20% 93%` | body |
| muted-fg | `215 14% 63%` | labels, secondary |
| primary | `205 95% 55%` | active, focus, "alive" |
| accent | `205 90% 14%` | tinted hover/selected |
| destructive | `0 72% 51%` | red, sparingly |
| chart-1..5 | cyan/emerald/violet/amber/rose | data viz |

### Tone vocabulary (Badge `tone` prop)
- `critical` — red, used for errors/blockers
- `high` — orange, used for priority=high
- `warn` — amber, "waiting on you"
- `info` — sky, neutral context
- `success` — emerald, "this flowed"
- `muted` — border-only, low-emphasis
- `mono` — uppercase tracking-wide, for kinds/scopes/literal strings

## Theme

Dark, fixed. Scene: *founder at midnight glancing at agent runs on a 16" laptop in a low-lit room while debugging.* This forces dark, low-chroma backgrounds with one electric accent. Not "dark mode because tools look cool dark" — dark because the operator is in a dim room and the app is ambient.

## Typography

- `--font-sans` system-ui for prose
- `--font-mono` JetBrains Mono — used for **IDs, tool names, status strings, latency values, file paths, peer IDs, code, terminal mocks**. Anywhere monospace says "this is verbatim machine output". Caller opts in with `font-mono`.
- Hierarchy through weight contrast (medium → semibold) more than scale. Pages tend to ship h1 around `text-xl font-semibold` then h2 `text-sm font-medium uppercase tracking-wide text-muted-foreground` for section labels.
- `text-[10px]` micro-labels carry meta (timestamps, ids).

## Radius

**Zero. Everywhere.** Tailwind radius tokens are all `0`. The whole app reads as squared/terminal. Don't introduce rounding.

## Layout

- Page shell: sidebar (240px) + main content. `AppLayout.tsx` owns this.
- Cards (`Card`, `CardHeader`, `CardContent`) for grouped content. Nested cards are forbidden.
- Section labels in the sidebar use uppercase tracking-wide muted-fg.
- Padding rhythm: `p-3` for dense rows, `p-4` for cards, `p-6` for page-level. Vary.
- Tables: `<table class="w-full text-sm">` with `<thead>` border-bottom. Keep them flat — don't add zebra striping.
- Lists: prefer `divide-y divide-border` over card-per-row.

## Motion

- `animate-shimmer` — horizontal sheen for "this surface is alive" (NowRunningStrip).
- `animate-pulse-slow` — 3s opacity pulse for "waiting on you" badges (awaiting_approval).
- `audit-in` keyframe — fades new rows in with a brief primary tint. Use for streaming list inserts.
- Curves: ease-out, no bounce.

## Components in use
- shadcn/ui: Badge, Button, Card, ConfirmDialog, CopyButton, Dialog, DropdownMenu, EmptyState, Input, Label, Select, Separator, Sheet, Sonner (toasts), StepIndicator, Table, Tabs, Textarea, Toggle, ToggleGroup, Tooltip
- App-specific: Pill (mesh status), AgentRow, MeshStatusStrip, SignalRow, EmptyState, P2PDebugPanel, CommandPalette (cmd+K)

## Iconography
- `lucide-react`. Sizing: `h-4 w-4` for inline, `h-3 w-3` for badge-glyphs. Match the existing nav choices (LayoutDashboard, Bell, ShieldCheck, FileText, Radio, Sparkles, Brain, Shield, Bot, ...).

## Identity callouts (don't accidentally violate)
- No emoji in UI.
- No em dashes in copy.
- No side-stripe `border-left` accents.
- No gradient text.
- No glassmorphism by default.
- No "hero metric" big-number-small-label tile templates.
- No identical card grids of icon+heading+text.

## Tasks-specific design intent

Tasks are *agent-emitted operational primitives* — not human-authored todos. They appear in mesh chatter and need to be cross-referenced by ID from many surfaces (audit, mesh, worker runs, notes). The UI must:

- Treat the task ID as the canonical reference, displayed in mono and copyable everywhere.
- Inline-link any task ID found in free text (mesh messages, notes, worker output) to its detail page.
- Group by workspace by default — workspace is the unit of agent scope.
- Show composition (parent epic ↔ children) as part of the row affordance, not buried in detail.
- Keep open vs closed as the primary toggle (matches the operator's "what needs my eye?" framing).
