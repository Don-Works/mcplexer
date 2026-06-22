import { Link, useLocation } from 'react-router-dom'
import {
  Archive,
  Bell,
  Bot,
  Brain,
  FileText,
  FolderOpen,
  GitBranch,
  Layers,
  LayoutDashboard,
  Link2,
  ListTodo,
  Menu,
  QrCode,
  Radio,
  Route as RouteIcon,
  Settings,
  ShieldCheck,
  Sliders,
  Sparkles,
  Wrench,
} from 'lucide-react'
import { useActiveWorkers, useWorkerLiveCount } from './use-worker-live-count'
import { useWorkerApprovalCount } from './use-worker-approval-count'
import { useMemoryCounts } from './use-memory-counts'
import { useTaskOffersCount } from './use-task-offers-count'
import { useApprovalStream } from '@/hooks/use-approval-stream'
import { useBrainStatus } from '@/hooks/use-brain-status'
import { NowRunningStrip } from './NowRunningStrip'
import {
  DangerousModeBanner,
  DangerousModeProvider,
  DangerousModeToggle,
  DangerousModeViewportFrame,
} from './dangerous-mode'
import { cn } from '@/lib/utils'
import { useCallback, useState } from 'react'
import { SignalTray } from '@/components/notifications/SignalTray'
import { SignalFlash } from '@/components/notifications/SignalFlash'
import { SignalSidebarTrigger } from '@/components/notifications/SignalSidebarTrigger'
import { useSignal } from '@/components/notifications/use-signal'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import { useApi } from '@/hooks/use-api'
import { getHealth, revealSystemPath, type HealthResponse } from '@/api/client'
import { toast } from 'sonner'
import { ConfigureWithAI } from '@/components/ConfigureWithAI'
import { hasCapability, isServerProfile, serverProfileLabel } from '@/lib/server-profile'

interface NavItem {
  label: string
  href: string
  icon: React.ReactNode
  // hint — one-line tooltip rendered as the link's `title` attribute.
  // Helps first-time users figure out what each surface is for.
  hint?: string
  // liveBadge — when set, the item renders a small pulsing pill with
  // the live count next to its label. Used today by Workers to show
  // how many runs are currently in flight (polled every 5s).
  liveBadge?: number
  // alertBadge — when > 0, the item renders a small red dot to draw
  // attention to a backlog the operator should clear. Used by Workers
  // to flag pending propose-mode approvals (M1).
  alertBadge?: number
  // warnBadge — amber pill, used for "deserves your eye soon" counts
  // (e.g. pending tool-call approvals on the Approvals nav entry).
  // The tone difference from `liveBadge` keeps emerald reserved for
  // "things are flowing", amber for "things are waiting on you".
  warnBadge?: number
  // infoBadge — sky-toned pill, used for ambient counts that aren't
  // urgent (e.g. incoming memory offers from peers). Quiet by default.
  infoBadge?: number
}

// Setup: the first-run path. Connect the client first, then add integrations.
const setupNav: NavItem[] = [
  { label: 'AI Harnesses', href: '/harness-setup', icon: <Wrench className="h-4 w-4" />, hint: 'Check MCP wiring, native Pi setup, bootstrap skills, and initialization for each AI harness' },
  { label: 'Add integration', href: '/setup', icon: <Sparkles className="h-4 w-4" />, hint: 'Connect a tool server like GitHub, Linear, Postgres, or ClickUp to a workspace' },
]

// Workspaces: the primary access-control concept. Clicking into a workspace
// shows servers, routes, memory scope, and tasks scoped to it.
const workspaceNav: NavItem[] = [
  { label: 'Workspace access', href: '/workspaces', icon: <Layers className="h-4 w-4" />, hint: 'Control what AI can use in each workspace folder' },
  { label: 'Routing rules', href: '/workspaces/routes', icon: <RouteIcon className="h-4 w-4" />, hint: 'Create, order, and reload workspace route rules' },
  { label: 'Workspace settings', href: '/workspaces/manage', icon: <FolderOpen className="h-4 w-4" />, hint: 'Create workspaces and edit root paths and default policy' },
]

const monitorNav: NavItem[] = [
  { label: 'Dashboard', href: '/', icon: <LayoutDashboard className="h-4 w-4" />, hint: 'Live view of tool calls, sessions, errors' },
  { label: 'Notifications', href: '/signals', icon: <Bell className="h-4 w-4" />, hint: 'Notifications and high-priority events from agents and peers' },
  { label: 'Audit', href: '/audit', icon: <FileText className="h-4 w-4" />, hint: 'Searchable history of every tool call routed through the gateway' },
]

const inboxNav: NavItem[] = [
  { label: 'Approvals', href: '/approvals', icon: <ShieldCheck className="h-4 w-4" />, hint: 'Pending tool-call approvals waiting on your decision' },
  { label: 'Worker proposals', href: '/worker-approvals', icon: <ShieldCheck className="h-4 w-4" />, hint: 'Worker propose-mode changes waiting on your decision' },
]

// Workers (M0.6): always-on AI agents. Only a single entry today; the
// per-worker pages live under /workers/:id and use breadcrumb-style
// navigation rather than dedicated sidebar items. The live badge fires
// when at least one run is currently in flight.
const workersNavBase: NavItem = {
  label: 'Workers',
  href: '/workers',
  icon: <Bot className="h-4 w-4" />,
}

const automationNavBase: NavItem[] = [
  workersNavBase,
  { label: 'Delegations', href: '/delegations', icon: <GitBranch className="h-4 w-4" />, hint: 'Parent and worker context trees with token savings and review scores' },
]

// Brain: the human-curated knowledge surface. It now sits beside Memory,
// Tasks, and Skills so the product presents one knowledge story.
const brainNav: NavItem[] = [
  { label: 'Brain notes', href: '/brain/browse', icon: <Brain className="h-4 w-4" />, hint: 'Agent ledger for notes, facts, and tasks your MCP tools can read' },
  { label: 'Brain sync', href: '/brain', icon: <GitBranch className="h-4 w-4" />, hint: 'Git status, push, and validation for your brain repo' },
]

const knowledgeNavBase: NavItem[] = [
  { label: 'Memory', href: '/memory', icon: <Brain className="h-4 w-4" />, hint: 'Cross-harness facts and notes your agents have learned; share with peers' },
  { label: 'Tasks', href: '/tasks', icon: <ListTodo className="h-4 w-4" />, hint: 'Operational work items per workspace; agents create them, you triage' },
  { label: 'Skills', href: '/skills', icon: <Sparkles className="h-4 w-4" />, hint: 'Shared library of reusable agent instructions' },
]

const networkNav: NavItem[] = [
  { label: 'Mesh', href: '/mesh', icon: <Radio className="h-4 w-4" />, hint: 'Peer-to-peer agent network: live agents, messages, file claims' },
  { label: 'Paired devices', href: '/pairing', icon: <QrCode className="h-4 w-4" />, hint: 'Pair another machine with this MCPlexer' },
  { label: 'Linked workspaces', href: '/workspace-links', icon: <Link2 className="h-4 w-4" />, hint: 'Sync a workspace across paired machines' },
]

const settingsNav: NavItem[] = [
  { label: 'Safety rules', href: '/guards', icon: <ShieldCheck className="h-4 w-4" />, hint: 'Guards and approval rules that protect local tools' },
  { label: 'Backups', href: '/backups', icon: <Archive className="h-4 w-4" />, hint: 'Backup and restore MCPlexer data' },
  { label: 'Advanced', href: '/advanced', icon: <Sliders className="h-4 w-4" />, hint: 'Credentials, OAuth providers, guards, descriptions' },
  { label: 'Settings', href: '/settings', icon: <Settings className="h-4 w-4" /> },
]

function NavLink({ item, onNavigate }: { item: NavItem; onNavigate?: () => void }) {
  const location = useLocation()
  const active =
    location.pathname === item.href ||
    // Workspaces: keep access, routing, and settings distinct in the sidebar.
    (item.href === '/workspaces' && location.pathname === '/workspaces') ||
    (item.href === '/workspaces/routes' && location.pathname.startsWith('/workspaces/routes')) ||
    (item.href === '/workspaces/manage' && location.pathname.startsWith('/workspaces/manage')) ||
    // Advanced: /advanced and all sub-paths (/advanced/credentials, etc.).
    (item.href === '/advanced' && location.pathname.startsWith('/advanced')) ||
    // Legacy /config/* deep links still highlight Advanced.
    (item.href === '/advanced' && location.pathname.startsWith('/config')) ||
    (item.href === '/guards' && location.pathname.startsWith('/guards')) ||
    // Workers detail/edit subpaths (/workers/:id, /workers/new) keep the
    // parent entry active — but sibling tabs (/workers/cost) are their own
    // NavLinks and must NOT also light up the Workers parent. Match /workers
    // exactly OR /workers/<id> where <id> isn't a sibling-tab slug.
    (item.href === '/workers' &&
      (location.pathname === '/workers' ||
        (location.pathname.startsWith('/workers/') &&
          !location.pathname.startsWith('/workers/cost') &&
          !location.pathname.startsWith('/workers/model-leaderboard')))) ||
    // Guards "Overview" matches /guards exactly; the sub-page entries
    // (Shell/Sanitizer/etc.) match their own path. Avoid both lighting
    // up when on /guards/shell by anchoring the overview match to the
    // exact path only.
    (item.href !== '/guards' && item.href.startsWith('/guards/') && location.pathname.startsWith(item.href)) ||
    // Memory: landing + all subpages light the same nav entry.
    (item.href === '/memory' && location.pathname.startsWith('/memory')) ||
    // Tasks: list + detail (/tasks/:id) share one entry.
    (item.href === '/tasks' && location.pathname.startsWith('/tasks')) ||
    // Delegations: context ledger and launch surface.
    (item.href === '/delegations' && location.pathname.startsWith('/delegations')) ||
    // Brain browser: the editor entry stays active on deep-linked records
    // (/brain/browse/:ws/:kind/:id). The "Sync & status" entry (/brain) is
    // matched by the exact-path check above only, so the two never both light.
    (item.href === '/brain/browse' && location.pathname.startsWith('/brain/browse')) ||
    (item.href === '/harness-setup' && (location.pathname === '/harness-setup' || location.pathname === '/install'))

  return (
    <Link
      to={item.href}
      onClick={onNavigate}
      title={item.hint}
      data-testid={`nav-${item.href.replace(/^\//, '').replace(/\//g, '-') || 'dashboard'}`}
      className={cn(
        'group relative flex items-center gap-3 px-4 py-2 text-[13px] tracking-wide transition-colors duration-150',
        active
          ? 'bg-sidebar-accent text-sidebar-accent-foreground'
          : 'text-sidebar-foreground hover:bg-sidebar-accent/40 hover:text-foreground',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'shrink-0 font-mono text-[11px] leading-none transition-opacity duration-150',
          active ? 'text-primary opacity-100' : 'opacity-0',
        )}
      >
        ›
      </span>
      <span
        className={cn(
          'shrink-0 transition-colors duration-150',
          active ? 'text-primary' : 'text-sidebar-foreground group-hover:text-foreground',
        )}
      >
        {item.icon}
      </span>
      <span className="flex-1 truncate">{item.label}</span>
      <span className="ml-auto inline-flex items-center gap-1">
        {typeof item.liveBadge === 'number' && item.liveBadge > 0 && (
          <span
            data-testid={`nav-${item.href.replace(/^\//, '')}-live-badge`}
            className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-emerald-500/15 px-1.5 font-mono text-[10px] font-medium tabular-nums text-emerald-300"
            title={`${item.liveBadge} running`}
          >
            {item.liveBadge}
          </span>
        )}
        {typeof item.warnBadge === 'number' && item.warnBadge > 0 && (
          <span
            data-testid={`nav-${item.href.replace(/^\//, '')}-warn-badge`}
            className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-amber-500/15 px-1.5 font-mono text-[10px] font-medium tabular-nums text-amber-300"
            title={`${item.warnBadge} waiting on you`}
          >
            {item.warnBadge}
          </span>
        )}
        {typeof item.infoBadge === 'number' && item.infoBadge > 0 && (
          <span
            data-testid={`nav-${item.href.replace(/^\//, '')}-info-badge`}
            className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-sky-500/15 px-1.5 font-mono text-[10px] font-medium tabular-nums text-sky-300"
            title={`${item.infoBadge} incoming`}
          >
            {item.infoBadge}
          </span>
        )}
        {typeof item.alertBadge === 'number' && item.alertBadge > 0 && (
          <span
            data-testid={`nav-${item.href.replace(/^\//, '')}-alert-badge`}
            className="inline-flex h-2 w-2 rounded-full bg-red-500"
            title={`${item.alertBadge} pending approval${item.alertBadge === 1 ? '' : 's'}`}
          />
        )}
      </span>
    </Link>
  )
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-4 pb-1.5 pt-1 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/60">
      {children}
    </div>
  )
}

const MX_FONT: Record<string, string[]> = {
  M: ['10001', '11011', '10101', '10101', '10001', '10001', '10001'],
  X: ['10001', '10001', '01010', '00100', '01010', '10001', '10001'],
}

// Dot-peen "MX" monogram — same 5x7 pin-stamp matrix as the MCPLEXER wordmark.
function McplexerLogo({ className }: { className?: string }) {
  const p = 12
  const glyphW = 4 * p, gap = p * 1.45, r = p * 0.34, padX = p * 1.6, padY = p * 1.6
  const lineW = 2 * glyphW + gap
  const width = +(lineW + padX * 2 + 2 * r).toFixed(2)
  const height = +(6 * p + 2 * r + padY * 2).toFixed(2)
  const dots: { cx: number; cy: number }[] = []
  let cx0 = (width - lineW) / 2 + r
  const oy = padY + r
  for (const ch of 'MX') {
    const rows = MX_FONT[ch]
    for (let y = 0; y < 7; y++)
      for (let x = 0; x < 5; x++)
        if (rows[y][x] === '1') dots.push({ cx: +(cx0 + x * p).toFixed(2), cy: +(oy + y * p).toFixed(2) })
    cx0 += glyphW + gap
  }
  return (
    <svg viewBox={`0 0 ${width} ${height}`} className={className} xmlns="http://www.w3.org/2000/svg" aria-label="MCPlexer" role="img">
      <g fill="currentColor">
        {dots.map((d, i) => (
          <circle key={i} cx={d.cx} cy={d.cy} r={r} />
        ))}
      </g>
    </svg>
  )
}

function BrandHeader() {
  return (
    <div className="flex h-14 items-center gap-2.5 border-b border-sidebar-border px-4">
      <span className="relative inline-flex items-center justify-center">
        <McplexerLogo className="relative z-10 h-5 w-5 text-primary" />
        <span className="absolute inset-0 bg-primary/20 blur-md" aria-hidden="true" />
      </span>
      <span className="font-mono text-[15px] font-bold uppercase tracking-tight text-foreground">
        MCPLEXER
      </span>
    </div>
  )
}

function shortenHomePath(p?: string) {
  if (!p) return ''
  // Display ~/.mcplexer instead of /Users/foo/.mcplexer for the common case.
  const m = p.match(/^\/(?:Users|home)\/[^/]+(\/.*)?$/)
  if (m) return '~' + (m[1] ?? '')
  return p
}

function StatusBar() {
  const fetcher = useCallback(() => getHealth().catch(() => null), [])
  const { data } = useApi(fetcher)
  const [revealing, setRevealing] = useState(false)
  const displayHost = window.location.host || 'localhost'
  const dataDir = data?.system?.data_dir
  const mode = data?.mode || data?.system?.mode
  const version = data?.version
  const daemonLabel = isServerProfile(data?.system)
    ? serverProfileLabel(data?.system)
    : `${mode || 'http'} daemon`

  async function handleReveal() {
    if (!dataDir) return
    setRevealing(true)
    try {
      await revealSystemPath('data_dir')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to open folder')
    } finally {
      setRevealing(false)
    }
  }

  return (
    <div className="min-w-0 overflow-hidden border-t border-sidebar-border px-4 py-3 space-y-1">
      <div className="flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground">
        <span className="h-2 w-2 shrink-0 rounded-full bg-emerald-500" />
        <span className="capitalize">{daemonLabel}</span>
        {version && <span className="text-muted-foreground/50">v{version}</span>}
      </div>
      <div className="truncate font-mono text-[10px] text-muted-foreground/50" title={displayHost}>
        {displayHost}
      </div>
      {/* Signal — sibling row to the daemon line above. Two-line system
          status report: the daemon is alive, and N things want you. */}
      <SignalSidebarTrigger />
      {dataDir && (
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleReveal}
            disabled={revealing}
            title={`Reveal ${dataDir} in Finder`}
            aria-label="Reveal mcplexer data directory in file manager"
            data-testid="status-reveal-data-dir"
            className="group flex min-w-0 flex-1 items-center gap-1.5 text-[10px] text-muted-foreground/60 transition-colors hover:text-foreground"
          >
            <FolderOpen className="h-3 w-3 shrink-0" />
            <span className="truncate font-mono text-[10px]">{shortenHomePath(dataDir)}</span>
          </button>
        </div>
      )}
    </div>
  )
}

function PaletteHint() {
  // Display the platform's modifier name so cmd vs ctrl is correct.
  // Falls back to "ctrl" outside a browser context (SSR / tests).
  const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
  const mod = isMac ? '⌘' : 'ctrl'
  return (
    <button
      type="button"
      data-testid="cmdk-hint"
      onClick={() => {
        // Dispatch the same shortcut event so the global hook opens the
        // palette — keeps a single source of truth for the open state.
        window.dispatchEvent(
          new KeyboardEvent('keydown', { key: 'k', metaKey: isMac, ctrlKey: !isMac, bubbles: true }),
        )
      }}
      className="group flex w-full items-center justify-between border border-border bg-card/40 px-2.5 py-1.5 font-mono text-[11px] text-muted-foreground transition-colors hover:border-border/80 hover:bg-card hover:text-foreground"
      aria-label={`Open command palette (${mod}+K)`}
    >
      <span className="inline-flex items-center gap-1.5">
        <span className="text-foreground/40">›</span>
        <span>jump anywhere</span>
      </span>
      <kbd className="border border-border bg-background/40 px-1 py-px text-[10px] text-foreground/70 transition-colors group-hover:text-foreground">
        {mod}K
      </kbd>
    </button>
  )
}

function SidebarNav({ onNavigate }: { onNavigate?: () => void }) {
  const fetcher = useCallback(() => getHealth().catch(() => null), [])
  const { data: health } = useApi(fetcher)

  if (isServerProfile(health?.system)) {
    return <ServerSidebarNav health={health} onNavigate={onNavigate} />
  }

  return <FullSidebarNav onNavigate={onNavigate} />
}

function FullSidebarNav({ onNavigate }: { onNavigate?: () => void }) {
  // Workers live count drives the pulsing badge next to the Workers
  // entry — humans want to see "is anything running right now?" at a
  // glance without opening the page.
  const workerLive = useWorkerLiveCount()
  // Pending propose-mode approvals → red dot. Same 5s poll as live count.
  const workerApprovals = useWorkerApprovalCount()
  // Pending tool-call approvals — feeds the Runtime > Approvals warn badge.
  // Single shared EventSource via use-approval-stream (no new SSE slot).
  const { pending: pendingToolApprovals } = useApprovalStream()
  // Memory pending offers — sky info-badge on the Memory entry. Polled
  // alongside total count to avoid double round-trips.
  const memoryCounts = useMemoryCounts()
  // Cross-peer task offers awaiting accept/decline — same info-badge
  // treatment on the Tasks entry.
  const taskOffersPending = useTaskOffersCount()
  // Brain status — hide brain nav entries when disabled.
  const { enabled: brainEnabled } = useBrainStatus()
  const inboxNavWithBadges: NavItem[] = inboxNav.map((item) => {
    if (item.href === '/approvals') {
      return { ...item, warnBadge: pendingToolApprovals.length }
    }
    if (item.href === '/worker-approvals') {
      return { ...item, alertBadge: workerApprovals }
    }
    return item
  })

  const automationNav: NavItem[] = automationNavBase.map((item) => {
    if (item.href === '/workers') {
      return { ...item, liveBadge: workerLive, alertBadge: workerApprovals }
    }
    return item
  })

  const knowledgeNav: NavItem[] = [
    ...(brainEnabled !== false ? brainNav : []),
    ...knowledgeNavBase,
  ].map((item) => {
    if (item.href === '/memory') {
      return { ...item, infoBadge: memoryCounts.pendingOffers }
    }
    if (item.href === '/tasks') {
      return { ...item, infoBadge: taskOffersPending }
    }
    return item
  })

  return (
    <nav className="flex-1 min-h-0 overflow-y-auto space-y-0.5 py-3 pr-2">
      <div className="px-3 pb-2 space-y-1.5">
        <PaletteHint />
      </div>

      <div className="pt-3">
        <SectionLabel>Setup</SectionLabel>
        {setupNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
        <ConfigureWithAI className="mx-4 mt-2" />
      </div>

      <div className="pt-3">
        <SectionLabel>Workspace</SectionLabel>
        {workspaceNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Monitor</SectionLabel>
        {monitorNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Inbox</SectionLabel>
        {inboxNavWithBadges.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Automation</SectionLabel>
        {automationNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Knowledge</SectionLabel>
        {knowledgeNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Network</SectionLabel>
        {networkNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Settings</SectionLabel>
        {settingsNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>
    </nav>
  )
}

function ServerSidebarNav({
  health,
  onNavigate,
}: {
  health: HealthResponse | null
  onNavigate?: () => void
}) {
  const taskOffersPending = useTaskOffersCount()
  const system = health?.system
  const p2pEnabled = Boolean(system?.p2p_enabled)
  const serverNav: NavItem[] = []
  if (hasCapability(system, 'skills')) {
    serverNav.push({
      label: 'Skills',
      href: '/skills',
      icon: <Sparkles className="h-4 w-4" />,
      hint: 'Shared skills repository',
    })
  }
  if (hasCapability(system, 'tasks')) {
    serverNav.push({
      label: 'Tasks',
      href: '/tasks',
      icon: <ListTodo className="h-4 w-4" />,
      infoBadge: taskOffersPending,
      hint: 'Shared operational task hub',
    })
  }
  if (p2pEnabled) {
    serverNav.push({
      label: 'Mesh',
      href: '/mesh',
      icon: <Radio className="h-4 w-4" />,
      hint: 'Peer-to-peer server connectivity',
    })
  }
  if (hasCapability(system, 'signals')) {
    serverNav.push({
      label: 'Notifications',
      href: '/signals',
      icon: <Bell className="h-4 w-4" />,
      hint: 'Server notifications and high-priority events',
    })
  }

  const serverDeviceNav: NavItem[] = [
    ...(p2pEnabled
      ? [{ label: 'Paired Devices', href: '/pairing', icon: <QrCode className="h-4 w-4" /> }]
      : []),
    { label: 'Backups', href: '/backups', icon: <Archive className="h-4 w-4" /> },
    { label: 'Settings', href: '/settings', icon: <Settings className="h-4 w-4" /> },
  ]

  return (
    <nav className="flex-1 min-h-0 overflow-y-auto space-y-0.5 py-3 pr-2">
      <div className="px-3 pb-2 space-y-2">
        <PaletteHint />
        <div className="px-1 font-mono text-[10px] uppercase tracking-wider text-muted-foreground/70">
          {serverProfileLabel(system)}
        </div>
      </div>

      <div className="pt-3">
        <SectionLabel>Server</SectionLabel>
        {serverNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>

      <div className="pt-3">
        <SectionLabel>Device</SectionLabel>
        {serverDeviceNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
      </div>
    </nav>
  )
}

export function AppLayout({ children }: { children: React.ReactNode }) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const activeWorkers = useActiveWorkers()

  return (
    <DangerousModeProvider>
      <div className="flex h-[100dvh] min-h-[100dvh] overflow-hidden [padding-left:env(safe-area-inset-left)] [padding-right:env(safe-area-inset-right)] [padding-top:env(safe-area-inset-top)]">
        {/* Desktop sidebar */}
        <aside className="hidden w-56 flex-col border-r border-sidebar-border bg-sidebar-background md:flex overflow-y-auto">
          <BrandHeader />
          <SidebarNav />
          <StatusBar />
        </aside>

        {/* Mobile sheet */}
        <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
          <SheetContent side="left" className="w-72 max-w-[calc(100vw-1rem)] gap-0 bg-sidebar-background p-0 sm:w-80 md:w-56" aria-describedby={undefined}>
            <SheetTitle className="sr-only">Navigation</SheetTitle>
            <BrandHeader />
            <SidebarNav onNavigate={() => setMobileOpen(false)} />
            <StatusBar />
          </SheetContent>
        </Sheet>

        <div className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
          {/* Global app header — always visible on every page. Holds the
              mobile nav button (left) and the dangerous-mode pill (right).
              Desktop hides the mobile button via md:hidden but keeps the
              header bar so the toggle is on the same line regardless of
              viewport. */}
          <header className="flex h-12 shrink-0 items-center gap-2.5 border-b border-border bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/70">
            <button
              onClick={() => setMobileOpen(true)}
              aria-label="Open navigation menu"
              data-testid="nav-mobile-open"
              className="flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground md:hidden"
            >
              <Menu className="h-5 w-5" />
            </button>
            <div className="flex items-center gap-2 md:hidden">
              <McplexerLogo className="h-4 w-4 text-primary" />
              <span className="text-sm font-semibold tracking-tight">MCPlexer</span>
            </div>
            {/* Spacer pushes the dangerous-mode toggle to the right edge */}
            <div className="flex-1" />
            <DangerousModeToggle />
          </header>

          {/* Dangerous-mode banner — sits directly under the header so
              the warning is read in the same eye-line as the toggle that
              produced it. Renders nothing when the mode is off. */}
          <DangerousModeBanner />

          {/* Signal flash strip — inline, full-width, push-down. Only
              fires on critical/high priority events. */}
          <SignalFlash />
          {/* "Now Running" strip — visible only when at least one worker is
              actively chewing tokens. The heartbeat of the gateway. */}
          {activeWorkers.length > 0 && <NowRunningStrip workers={activeWorkers} />}
          <main className="min-w-0 flex-1 overflow-y-auto overflow-x-hidden p-4 pb-[calc(1rem+env(safe-area-inset-bottom))] md:p-6 md:pb-[calc(1.5rem+env(safe-area-inset-bottom))]">{children}</main>
        </div>

        {/* Signal tray — right-edge dock, sibling of the left sidebar.
            Toggles via cmd+J or the sidebar trigger. Reflows content. */}
        <SignalTrayDock />
        {/* Dangerous-mode chrome wash — fixed overlay above everything
            except modals. Only rendered when the mode is on. */}
        <DangerousModeViewportFrame />
      </div>
    </DangerousModeProvider>
  )
}

function SignalTrayDock() {
  // Render the tray only when open, so we don't pay the layout cost on
  // every page when it isn't visible.
  const { trayOpen } = useSignal()
  if (!trayOpen) return null
  return <SignalTray />
}
