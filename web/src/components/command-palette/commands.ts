import { useCallback, useEffect, useState } from 'react'
import type { NavigateFunction } from 'react-router-dom'
import { Activity, AlertTriangle, Archive, Bell, Bot, Brain as BrainIcon, DollarSign, FileText, Gauge, GitBranch, Globe, Key, KeyRound, Laptop, Layers, LayoutDashboard, Link2, ListTodo, Lock, Package, Plus, Radio, Search, Server, Settings, ShieldCheck, Sliders, Sparkles, UsersRound, Wrench, Zap } from 'lucide-react'
import { createElement } from 'react'
import { getDashboard, listAuthScopes, listDownstreams, listRoutes, listWorkspaces } from '@/api/client'
import { listNotifications, type StoredNotification } from '@/api/notifications'
import { listWorkers, runWorkerNow } from '@/api/workers'
import { toast } from 'sonner'
import { brainVerbEntries } from './brainCommands'
import { useBrainStatus } from '@/hooks/use-brain-status'

export type CommandStatus = 'ok' | 'warn' | 'err' | 'idle'

export interface CommandEntry {
  id: string
  label: string
  // Secondary text used for matching but not displayed.
  keywords?: string
  // Right-aligned uppercase hint, e.g. shortcut, kind, scope policy.
  hint?: string
  // Optional left-side glyph.
  icon?: React.ReactNode
  // Optional health dot rendered just before the hint (servers/services).
  statusDot?: CommandStatus
  // Navigation target or custom action. run() takes precedence.
  // setQuery primes the palette input (used by entries that switch the
  // palette into a typeahead mode, e.g. audit search seeds a leading `/`)
  // instead of navigating away.
  to?: string
  run?: (ctx: { navigate: NavigateFunction; setQuery: (q: string) => void }) => void
}

export interface CommandGroup {
  id: string
  label: string
  entries: CommandEntry[]
}

const iconClass = 'h-3.5 w-3.5'

// Static groups — pages and built-in actions. Available immediately on
// palette open without any network round-trip.
const PAGES: CommandEntry[] = [
  { id: 'page-workspaces', label: 'Workspaces', to: '/workspaces', keywords: 'workspace project folder routes servers policy access actions workers memory tasks', icon: createElement(Layers, { className: iconClass }) },
  { id: 'page-workspace-routes', label: 'Workspace access', to: '/workspaces?view=access&advanced=1', keywords: 'route routing rules policy match workspace server access credentials approvals', icon: createElement(Wrench, { className: iconClass }) },
  { id: 'page-workspace-setup', label: 'Workspace settings', to: '/workspaces?view=settings', keywords: 'workspace settings project root folder default policy tags', icon: createElement(Globe, { className: iconClass }) },
  { id: 'page-approvals', label: 'Approvals', to: '/approvals', keywords: 'approve deny pending wait inbox', icon: createElement(ShieldCheck, { className: iconClass }) },
  { id: 'page-worker-approvals', label: 'Worker proposals', to: '/worker-approvals', keywords: 'pending approve propose worker write inbox', icon: createElement(ShieldCheck, { className: iconClass }) },
  { id: 'page-tasks', label: 'Tasks', to: '/tasks', keywords: 'work items offers shared operational queue', icon: createElement(ListTodo, { className: iconClass }) },
  { id: 'page-workers', label: 'Workers', to: '/workers', keywords: 'agent cron scheduled ai run automation', icon: createElement(Bot, { className: iconClass }) },
  { id: 'page-delegations', label: 'Delegations', to: '/delegations', keywords: 'worker context review subagent capacity token savings', icon: createElement(GitBranch, { className: iconClass }) },
  { id: 'page-dashboard', label: 'Dashboard', to: '/', keywords: 'home overview monitor', icon: createElement(LayoutDashboard, { className: iconClass }) },
  { id: 'page-signals', label: 'Notifications', to: '/signals', keywords: 'signal notifications log feed alert', icon: createElement(Bell, { className: iconClass }) },
  { id: 'page-audit', label: 'Audit', to: '/audit', keywords: 'logs trail history monitor', icon: createElement(FileText, { className: iconClass }) },
  { id: 'page-memory', label: 'Memory', to: '/memory', keywords: 'facts notes offers shared learned knowledge', icon: createElement(BrainIcon, { className: iconClass }) },
  { id: 'page-skills', label: 'Skills', to: '/skills', keywords: 'recipe skill md registry knowledge', icon: createElement(Sparkles, { className: iconClass }) },
  { id: 'page-brain', label: 'Brain notes', to: '/brain/browse', keywords: 'brain ledger records tasks memories notes knowledge', icon: createElement(BrainIcon, { className: iconClass }) },
  { id: 'page-mesh', label: 'Mesh', to: '/mesh', keywords: 'p2p agents network inter peer', icon: createElement(Radio, { className: iconClass }) },
  { id: 'page-network-people', label: 'People', to: '/pairing?tab=people', keywords: 'people owners humans p2p network devices', icon: createElement(UsersRound, { className: iconClass }) },
  { id: 'page-network-devices', label: 'Devices', to: '/pairing?tab=devices', keywords: 'peer p2p pair network device owner machine', icon: createElement(Laptop, { className: iconClass }) },
  { id: 'page-linked-workspaces', label: 'Linked workspaces', to: '/workspace-links', keywords: 'sync paired machines network workspace links', icon: createElement(Link2, { className: iconClass }) },
  { id: 'page-harnesses', label: 'AI Harnesses', to: '/harness-setup', keywords: 'wire mcp ide claude cursor codex opencode gemini mimo pi harness bootstrap setup', icon: createElement(Wrench, { className: iconClass }) },
  { id: 'page-setup', label: 'Add server to workspace', to: '/workspaces?view=add-server', keywords: 'quick setup add server service tool github linear postgres clickup', icon: createElement(Sparkles, { className: iconClass }) },
  { id: 'page-guards', label: 'Safety rules', to: '/guards', keywords: 'guards approvals shell sanitizer schedule sandbox safety policy', icon: createElement(ShieldCheck, { className: iconClass }) },
  { id: 'page-backups', label: 'Backups', to: '/backups', keywords: 'backup restore snapshot export settings', icon: createElement(Archive, { className: iconClass }) },
  { id: 'page-advanced', label: 'Advanced', to: '/advanced', keywords: 'credentials oauth providers descriptions config', icon: createElement(Sliders, { className: iconClass }) },
  { id: 'page-settings', label: 'Settings', to: '/settings', keywords: 'preferences config settings', icon: createElement(Settings, { className: iconClass }) },
  { id: 'page-usage', label: 'AI usage', to: '/usage', keywords: 'usage subscription allowance provider tokens cost ai dashboard', icon: createElement(Gauge, { className: iconClass }) },
  { id: 'page-worker-cost', label: 'Worker cost', to: '/workers/cost', keywords: 'spend dollars budget mtd cost dashboard ai', icon: createElement(DollarSign, { className: iconClass }) },
  { id: 'page-model-ranks', label: 'Model ranks', to: '/delegations/models', keywords: 'models ranking delegation capacity quality review task kind mimo minimax grok', icon: createElement(Gauge, { className: iconClass }) },
  { id: 'page-model-leaderboard', label: 'Model leaderboard', to: '/workers/model-leaderboard', keywords: 'models ranking leaderboard delegation capacity quality mimo minimax grok', icon: createElement(Gauge, { className: iconClass }) },
]

const WORKER_ACTIONS: CommandEntry[] = [
  { id: 'action-new-worker',    label: 'New Worker',                       to: '/workers/new',        keywords: 'create worker scheduled ai agent',           icon: createElement(Plus, { className: iconClass }),     hint: 'flow' },
]

const SIGNAL_FILTERS: CommandEntry[] = [
  { id: 'signal-unread', label: 'Notifications: unread only', to: '/signals?unread=true', keywords: 'signal notifications unread new', icon: createElement(Bell, { className: iconClass }), hint: 'unread' },
  { id: 'signal-mesh', label: 'Notifications: mesh', to: '/signals?filter=mesh', keywords: 'signal notifications mesh agent', icon: createElement(Bell, { className: iconClass }), hint: 'filter' },
  { id: 'signal-approval', label: 'Notifications: approvals', to: '/signals?filter=approval', keywords: 'signal notifications approval queue', icon: createElement(Bell, { className: iconClass }), hint: 'filter' },
  { id: 'signal-system', label: 'Notifications: system', to: '/signals?filter=system', keywords: 'signal notifications system daemon', icon: createElement(Bell, { className: iconClass }), hint: 'filter' },
  { id: 'signal-secret', label: 'Notifications: secret prompts', to: '/signals?filter=secret', keywords: 'signal notifications secret prompt', icon: createElement(Bell, { className: iconClass }), hint: 'filter' },
]

const CONFIG_TABS: CommandEntry[] = [
  { id: 'config-servers', label: 'Server library', to: '/workspaces?view=servers', keywords: 'server downstream mcp config workspace access actions', icon: createElement(Server, { className: iconClass }), hint: 'workspace' },
  { id: 'config-routes', label: 'Advanced access rules', to: '/workspaces?view=access&advanced=1', keywords: 'rule route routing policy match config workspace server credentials approvals', icon: createElement(Wrench, { className: iconClass }), hint: 'workspace' },
  { id: 'config-workspaces', label: 'Workspace settings', to: '/workspaces?view=settings', keywords: 'project root folder policy config tags', icon: createElement(Globe, { className: iconClass }), hint: 'workspace' },
  { id: 'config-credentials', label: 'Credentials', to: '/advanced/credentials', keywords: 'auth scope secret api config advanced', icon: createElement(Lock, { className: iconClass }), hint: 'advanced' },
  { id: 'config-oauth', label: 'OAuth providers', to: '/advanced/oauth-providers', keywords: 'provider token config advanced', icon: createElement(KeyRound, { className: iconClass }), hint: 'advanced' },
  { id: 'config-descriptions', label: 'Tool descriptions', to: '/advanced/descriptions', keywords: 'description override config advanced', icon: createElement(FileText, { className: iconClass }), hint: 'advanced' },
]

const ACTIONS: CommandEntry[] = [
  // Switches the palette into the `/` audit-search mode by priming the input
  // with a leading slash; the body becomes the AuditSearchMode listbox.
  { id: 'action-search-audit', label: 'Search audit logs', keywords: 'audit log search semantic trail history find tool call error', icon: createElement(Search, { className: iconClass }), hint: '/', run: ({ setQuery }) => setQuery('/') },
  { id: 'action-create-task', label: 'Create a task', to: '/tasks?new=1', keywords: 'new task todo issue ticket create human', icon: createElement(Plus, { className: iconClass }), hint: 'task' },
  { id: 'action-quick-setup', label: 'Add a server to a workspace', to: '/workspaces?view=add-server', keywords: 'add new server service tool install', icon: createElement(Plus, { className: iconClass }), hint: 'flow' },
  { id: 'action-wire-ide', label: 'Set up an AI harness', to: '/harness-setup', keywords: 'claude cursor codex opencode grok gemini mimo mimocode pi ide', icon: createElement(Zap, { className: iconClass }), hint: 'flow' },
  { id: 'action-custom-mcp',  label: 'Build a custom MCP server',           to: '/create-mcp', keywords: 'openapi wizard custom',            icon: createElement(Package, { className: iconClass }),  hint: 'flow' },
  { id: 'action-dry-run',     label: 'Dry-run a route rule',                to: '/dry-run',    keywords: 'test route policy preview',        icon: createElement(Activity, { className: iconClass }), hint: 'flow' },
  { id: 'action-backups',     label: 'Backup & restore',                    to: '/backups',    keywords: 'snapshot restore export',          icon: createElement(AlertTriangle, { className: iconClass }), hint: 'flow' },
]

interface DynamicSet {
  servers: CommandEntry[]
  routes: CommandEntry[]
  credentials: CommandEntry[]
  workspaces: CommandEntry[]
  signals: CommandEntry[]
  workers: CommandEntry[]
}

const EMPTY: DynamicSet = { servers: [], routes: [], credentials: [], workspaces: [], signals: [], workers: [] }

// useCommandEntries assembles the command set. Dynamic items
// (per-server / per-route / per-workspace jumps) are refreshed on every
// palette open so the first-result-after-open is current.
//
// Server entries are enriched with the latest tools/list status dot
// (ok/slow/timeout/error) from the dashboard payload so the palette
// reads as a live status board, not a stale directory.
export function useCommandEntries(open: boolean): { groups: CommandGroup[]; loading: boolean } {
  const [dynamic, setDynamic] = useState<DynamicSet>(EMPTY)
  const [loading, setLoading] = useState(false)
  const { enabled: brainEnabled } = useBrainStatus()

  const fetcher = useCallback(async () => {
    setLoading(true)
    try {
      const [servers, routes, scopes, workspaces, dash, notif, workers] = await Promise.all([
        listDownstreams().catch(() => []),
        listRoutes().catch(() => []),
        listAuthScopes().catch(() => []),
        listWorkspaces().catch(() => []),
        getDashboard('1h').catch(() => null),
        listNotifications({ unread: true, limit: 25 }).catch(() => ({ notifications: [] as StoredNotification[], unread_count: 0 })),
        listWorkers().catch(() => []),
      ])
      const timingByName = new Map<string, CommandStatus>()
      for (const t of dash?.server_timings ?? []) {
        timingByName.set(t.server_name, statusFor(t.status))
      }
      setDynamic({
        servers: servers.map((s) => ({
          id: `srv-${s.id}`,
          label: s.name,
          keywords: `server downstream ${s.tool_namespace || ''}`,
          hint: 'server',
          icon: createElement(Server, { className: iconClass }),
          statusDot: timingByName.get(s.name) ?? 'idle',
          to: `/workspaces?focus_server=${encodeURIComponent(s.id)}`,
        })),
        routes: routes.map((r) => {
          const matchLabel = r.tool_match && r.tool_match.length > 0 ? r.tool_match.join(', ') : ''
          return {
            id: `route-${r.id}`,
            label: matchLabel || r.path_glob || `Route ${r.id.slice(0, 6)}`,
            keywords: `route rule ${r.workspace_id || ''} ${r.policy} ${r.path_glob || ''}`,
            hint: r.policy,
            icon: createElement(Wrench, { className: iconClass }),
            to: `/workspaces?workspace=${encodeURIComponent(r.workspace_id)}&advanced=1&route=${encodeURIComponent(r.id)}`,
          }
        }),
        credentials: scopes.map((c) => ({
          id: `cred-${c.id}`,
          label: c.name,
          keywords: `credential auth scope ${c.type}`,
          hint: c.type,
          icon: createElement(Key, { className: iconClass }),
          to: `/advanced/credentials?credential=${c.id}`,
        })),
        workspaces: workspaces.map((w) => ({
          id: `ws-${w.id}`,
          label: w.name,
          keywords: `workspace project ${w.root_path || ''}`,
          hint: 'ws',
          icon: createElement(Globe, { className: iconClass }),
          to: `/workspaces?workspace=${encodeURIComponent(w.id)}`,
        })),
        // Every unread signal is searchable in the palette. Each one
        // is a deep-link to the originating page (via evt.Link) — the
        // palette becomes a "what's screaming at me" view as well as
        // a navigator. Cap at 25 to keep the list scannable.
        signals: (notif.notifications ?? []).map((n: StoredNotification) => ({
          id: `signal-${n.id}`,
          label: n.title,
          keywords: `signal notification ${n.source} ${n.kind} ${n.agent_name || ''} ${n.body.slice(0, 120)}`,
          hint: n.priority === 'critical' || n.priority === 'high' ? n.priority : (n.source || 'mesh'),
          icon: createElement(Bell, { className: iconClass }),
          statusDot: signalDotFor(n.priority),
          to: n.link || `/signals?filter=${n.source || 'mesh'}`,
        })),
        workers: workers.flatMap((w) => {
          const baseKeywords = `worker ${w.model_provider} ${w.model_id} ${w.schedule_spec} ${w.enabled ? 'enabled' : 'paused'}`
          const lastStatus = w.last_run_status ? statusDotForRun(w.last_run_status) : 'idle'
          return [
            {
              id: `worker-${w.id}`,
              label: w.name,
              keywords: `${baseKeywords} open detail`,
              hint: w.enabled ? 'worker' : 'paused',
              icon: createElement(Bot, { className: iconClass }),
              statusDot: lastStatus as CommandStatus,
              to: `/workers/${w.id}`,
            },
            {
              id: `worker-run-${w.id}`,
              label: `Run now · ${w.name}`,
              keywords: `${baseKeywords} run-now trigger fire dispatch`,
              hint: 'run',
              icon: createElement(Zap, { className: iconClass }),
              run: () => {
                runWorkerNow(w.id)
                  .then((r) => toast.success(`Run started · ${r.run_id.slice(-8)}`))
                  .catch((e) => toast.error(e instanceof Error ? e.message : 'Run failed'))
              },
            },
          ]
        }),
      })
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (open) void fetcher()
  }, [open, fetcher])

  const groups: CommandGroup[] = [
    { id: 'pages',       label: 'Pages',       entries: PAGES },
    { id: 'signals',     label: 'Notifications', entries: [...SIGNAL_FILTERS, ...dynamic.signals] },
    { id: 'config',      label: 'Workspace & advanced', entries: CONFIG_TABS },
    { id: 'servers',     label: 'Servers',     entries: dynamic.servers },
    { id: 'routes',      label: 'Access rules', entries: dynamic.routes },
    { id: 'credentials', label: 'Credentials', entries: dynamic.credentials },
    { id: 'workspaces',  label: 'Workspaces',  entries: dynamic.workspaces },
    { id: 'workers',     label: 'Workers',     entries: dynamic.workers },
    { id: 'brain',       label: 'Brain',       entries: brainEnabled !== false ? brainVerbEntries() : [] },
    { id: 'actions',     label: 'Actions',     entries: [...WORKER_ACTIONS, ...ACTIONS] },
  ]
  return { groups, loading }
}

// brainCmdEntries is the `>` command-verb set surfaced when the CommandSurface
// is in cmd mode (DESIGN §4.0). Exported so CommandPalette can filter to just
// these verbs without the full page/server catalog.
export function brainCmdEntries(): CommandEntry[] {
  return brainVerbEntries()
}

function statusFor(s: string): CommandStatus {
  if (s === 'ok') return 'ok'
  if (s === 'slow') return 'warn'
  if (s === 'timeout' || s === 'error') return 'err'
  return 'idle'
}

function signalDotFor(priority: string): CommandStatus {
  switch (priority) {
    case 'critical':
      return 'err'
    case 'high':
      return 'warn'
    default:
      return 'ok'
  }
}

function statusDotForRun(s: string): CommandStatus {
  switch (s) {
    case 'success':
      return 'ok'
    case 'running':
      return 'warn'
    case 'cap_exceeded':
    case 'awaiting_approval':
      return 'warn'
    case 'failure':
    case 'rejected':
      return 'err'
    default:
      return 'idle'
  }
}

// Sliders glyph re-export for callers that want a generic "tweak" icon.
export { Sliders }
