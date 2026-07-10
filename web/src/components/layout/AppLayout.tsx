import { Link, useLocation } from 'react-router-dom'
import {
  Activity,
  Archive,
  Bell,
  Bot,
  Brain,
  FileText,
  FolderOpen,
  Gauge,
  GitBranch,
  Inbox,
  Layers,
  LayoutDashboard,
  Laptop,
  Link2,
  ListTodo,
  Menu,
  MessageSquare,
  Radio,
  Route as RouteIcon,
  Search as SearchIcon,
  Settings,
  ShieldCheck,
  Sliders,
  Sparkles,
  UsersRound,
  Wrench,
} from 'lucide-react'
import { useActiveWorkers, useLiveDelegationCount, useWorkerLiveCount } from './use-worker-live-count'
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
import { useState } from 'react'
import { SignalTray } from '@/components/notifications/SignalTray'
import { SignalFlash } from '@/components/notifications/SignalFlash'
import { SignalSidebarTrigger } from '@/components/notifications/SignalSidebarTrigger'
import { useSignal } from '@/components/notifications/use-signal'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import { useHealth } from '@/hooks/use-health'
import { revealSystemPath, type HealthResponse } from '@/api/client'
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
  { label: 'Command center', href: '/workspaces', icon: <Layers className="h-4 w-4" />, hint: 'Start from a workspace: access, routes, actions, agents, and knowledge' },
  { label: 'Server access', href: '/workspaces/routes', icon: <RouteIcon className="h-4 w-4" />, hint: 'Choose which servers, credentials, approvals, and matching rules each workspace can use' },
  { label: 'Workspace setup', href: '/workspaces/manage', icon: <FolderOpen className="h-4 w-4" />, hint: 'Create workspace records and edit root paths, tags, and default policy' },
]

const monitorNav: NavItem[] = [
  { label: 'Dashboard', href: '/', icon: <LayoutDashboard className="h-4 w-4" />, hint: 'Live view of tool calls, sessions, errors' },
  { label: 'AI usage', href: '/usage', icon: <Gauge className="h-4 w-4" />, hint: 'Subscription allowances and observed consumption across providers' },
  { label: 'Notifications', href: '/signals', icon: <Bell className="h-4 w-4" />, hint: 'Notifications and high-priority events from agents and peers' },
  { label: 'Audit', href: '/audit', icon: <FileText className="h-4 w-4" />, hint: 'Searchable history of every tool call routed through the gateway' },
]

const inboxNav: NavItem[] = [
  { label: 'Human inbox', href: '/app', icon: <Inbox className="h-4 w-4" />, hint: 'Mobile-first approvals and human task queue' },
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
  { label: 'Tasks', href: '/tasks', icon: <ListTodo className="h-4 w-4" />, hint: 'Operational work items per workspace; agents create them, you triage' },
  workersNavBase,
  { label: 'Delegations', href: '/delegations', icon: <GitBranch className="h-4 w-4" />, hint: 'Parent and worker context trees with token savings and review scores' },
  { label: 'Monitoring', href: '/monitoring', icon: <Activity className="h-4 w-4" />, hint: 'Remote docker logs distilled to templates; alert channels and the log-watch worker' },
]

// Brain: the human-curated knowledge surface. It now sits beside Memory,
// Tasks, and Skills so the product presents one knowledge story.
const brainNav: NavItem[] = [
  { label: 'Brain notes', href: '/brain/browse', icon: <Brain className="h-4 w-4" />, hint: 'Agent ledger for notes, facts, and tasks your MCP tools can read' },
  { label: 'Brain sync', href: '/brain', icon: <GitBranch className="h-4 w-4" />, hint: 'Git status, push, and validation for your brain repo' },
]

const knowledgeNavBase: NavItem[] = [
  { label: 'Memory', href: '/memory', icon: <Brain className="h-4 w-4" />, hint: 'Cross-harness facts and notes your agents have learned; share with peers' },
  { label: 'Skills', href: '/skills', icon: <Sparkles className="h-4 w-4" />, hint: 'Shared library of reusable agent instructions' },
]

const networkNav: NavItem[] = [
  { label: 'Mesh', href: '/mesh', icon: <Radio className="h-4 w-4" />, hint: 'Peer-to-peer agent network: live agents, messages, file claims' },
  { label: 'Chat', href: '/chat', icon: <MessageSquare className="h-4 w-4" />, hint: 'Send and receive mesh broadcast messages' },
  { label: 'People', href: '/pairing?tab=people', icon: <UsersRound className="h-4 w-4" />, hint: 'People and the devices linked to them' },
  { label: 'Devices', href: '/pairing?tab=devices', icon: <Laptop className="h-4 w-4" />, hint: 'Paired machines and device ownership' },
  { label: 'Linked workspaces', href: '/workspace-links', icon: <Link2 className="h-4 w-4" />, hint: 'Sync a workspace across paired machines' },
]

const settingsNav: NavItem[] = [
  { label: 'Safety rules', href: '/guards', icon: <ShieldCheck className="h-4 w-4" />, hint: 'Guards and approval rules that protect local tools' },
  { label: 'Backups', href: '/backups', icon: <Archive className="h-4 w-4" />, hint: 'Backup and restore MCPlexer data' },
  { label: 'Advanced', href: '/advanced', icon: <Sliders className="h-4 w-4" />, hint: 'Credentials, OAuth providers, guards, descriptions' },
  { label: 'Compression', href: '/settings/compression', icon: <Gauge className="h-4 w-4" />, hint: 'Token-compression mode and observed savings' },
  { label: 'Settings', href: '/settings', icon: <Settings className="h-4 w-4" /> },
]

function splitNavHref(href: string): { path: string; search: string } {
  const [rawPath, search = ''] = href.split('?')
  return { path: rawPath || '/', search }
}

function navTestID(href: string): string {
  return `nav-${href.replace(/^\//, '').replace(/[/?=&]/g, '-') || 'dashboard'}`
}

function navSearchMatches(path: string, expectedSearch: string, currentSearch: string): boolean {
  const expected = new URLSearchParams(expectedSearch)
  const current = new URLSearchParams(currentSearch)
  for (const [key, expectedValue] of expected.entries()) {
    const actualValue = current.get(key)
    if (actualValue === expectedValue) continue
    if (path === '/pairing' && key === 'tab' && expectedValue === 'devices' && actualValue === null) {
      continue
    }
    return false
  }
  return true
}

function NavLink({ item, onNavigate }: { item: NavItem; onNavigate?: () => void }) {
  const location = useLocation()
  const { path: itemPath, search: itemSearch } = splitNavHref(item.href)
  const navID = navTestID(item.href)
  const hasSearch = itemSearch.length > 0
  const active =
    (hasSearch && location.pathname === itemPath && navSearchMatches(itemPath, itemSearch, location.search)) ||
    (!hasSearch &&
      (location.pathname === itemPath ||
        // Workspaces: keep access, routing, and settings distinct in the sidebar.
        (itemPath === '/workspaces' && location.pathname === '/workspaces') ||
        (itemPath === '/workspaces/routes' && location.pathname.startsWith('/workspaces/routes')) ||
        (itemPath === '/workspaces/manage' && location.pathname.startsWith('/workspaces/manage')) ||
        // Advanced: /advanced and all sub-paths (/advanced/credentials, etc.).
        (itemPath === '/advanced' && location.pathname.startsWith('/advanced')) ||
        // Legacy /config/* deep links still highlight Advanced.
        (itemPath === '/advanced' && location.pathname.startsWith('/config')) ||
        (itemPath === '/guards' && location.pathname.startsWith('/guards')) ||
        // Workers detail/edit subpaths (/workers/:id, /workers/new) keep the
        // parent entry active — but sibling tabs (/workers/cost) are their own
        // NavLinks and must NOT also light up the Workers parent. Match /workers
        // exactly OR /workers/<id> where <id> isn't a sibling-tab slug.
        (itemPath === '/workers' &&
          (location.pathname === '/workers' ||
            (location.pathname.startsWith('/workers/') &&
              !location.pathname.startsWith('/workers/cost') &&
              !location.pathname.startsWith('/workers/model-leaderboard')))) ||
        // Guards "Overview" matches /guards exactly; the sub-page entries
        // (Shell/Sanitizer/etc.) match their own path. Avoid both lighting
        // up when on /guards/shell by anchoring the overview match to the
        // exact path only.
        (itemPath !== '/guards' && itemPath.startsWith('/guards/') && location.pathname.startsWith(itemPath)) ||
        // Memory: landing + all subpages light the same nav entry.
        (itemPath === '/memory' && location.pathname.startsWith('/memory')) ||
        // Tasks: list + detail (/tasks/:id) share one entry.
        (itemPath === '/tasks' && location.pathname.startsWith('/tasks')) ||
        // Delegations: context ledger and launch surface.
        (itemPath === '/delegations' && location.pathname.startsWith('/delegations')) ||
        // Brain browser: the editor entry stays active on deep-linked records
        // (/brain/browse/:ws/:kind/:id). The "Sync & status" entry (/brain) is
        // matched by the exact-path check above only, so the two never both light.
        (itemPath === '/brain/browse' && location.pathname.startsWith('/brain/browse')) ||
        (itemPath === '/harness-setup' && (location.pathname === '/harness-setup' || location.pathname === '/install'))))

  return (
    <Link
      to={item.href}
      onClick={onNavigate}
      title={item.hint}
      data-testid={navID}
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
            data-testid={`${navID}-live-badge`}
            className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-emerald-500/15 px-1.5 font-mono text-[10px] font-medium tabular-nums text-emerald-300"
            title={`${item.liveBadge} running`}
          >
            {item.liveBadge}
          </span>
        )}
        {typeof item.warnBadge === 'number' && item.warnBadge > 0 && (
          <span
            data-testid={`${navID}-warn-badge`}
            className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-amber-500/15 px-1.5 font-mono text-[10px] font-medium tabular-nums text-amber-300"
            title={`${item.warnBadge} waiting on you`}
          >
            {item.warnBadge}
          </span>
        )}
        {typeof item.infoBadge === 'number' && item.infoBadge > 0 && (
          <span
            data-testid={`${navID}-info-badge`}
            className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-sky-500/15 px-1.5 font-mono text-[10px] font-medium tabular-nums text-sky-300"
            title={`${item.infoBadge} incoming`}
          >
            {item.infoBadge}
          </span>
        )}
        {typeof item.alertBadge === 'number' && item.alertBadge > 0 && (
          <span
            data-testid={`${navID}-alert-badge`}
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
    <div className="flex h-14 shrink-0 items-center gap-2.5 border-b border-sidebar-border px-4">
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
  const { data } = useHealth()
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
    <div className="min-w-0 shrink-0 overflow-hidden border-t border-sidebar-border px-4 py-3 space-y-1">
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

// isMacPlatform — cmd vs ctrl. Falls back to ctrl outside a browser (SSR/tests).
function isMacPlatform(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
}

// openCommandPalette dispatches the same modifier+K shortcut the global hook
// listens for, so every affordance that opens the palette shares one source of
// truth for the open state (the useCommandPalette hook at the App root).
function openCommandPalette() {
  const isMac = isMacPlatform()
  window.dispatchEvent(
    new KeyboardEvent('keydown', { key: 'k', metaKey: isMac, ctrlKey: !isMac, bubbles: true }),
  )
}

// TopBarSearch — the primary palette affordance, promoted out of the sidebar
// into the global header. A wide, VSCode-command-bar-feel box on >=md; an icon
// button on small screens (where header real estate is scarce). Both open the
// command palette via the shared dispatch. It is not a live input: focusing,
// clicking, or typing into it opens the full palette, which owns the query.
function TopBarSearch() {
  const isMac = isMacPlatform()
  const mod = isMac ? '⌘' : 'ctrl'

  // <md: a single icon button. Keeps the mobile branding + menu room.
  const mobile = (
    <button
      type="button"
      data-testid="cmdk-hint-mobile"
      onClick={openCommandPalette}
      aria-label={`Search everything (${mod}K)`}
      className="flex h-8 w-8 items-center justify-center rounded-none border border-border bg-card text-muted-foreground transition-colors hover:border-primary/60 hover:text-foreground md:hidden"
    >
      <SearchIcon className="h-4 w-4" />
    </button>
  )

  // >=md: the prominent wide box. Rendered as a button so it is a single
  // tab-stop that opens the palette on click/focus/Enter — no duplicate input.
  const desktop = (
    <button
      type="button"
      data-testid="cmdk-hint"
      onClick={openCommandPalette}
      onFocus={openCommandPalette}
      aria-label={`Search everything (${mod}K)`}
      className="group hidden h-9 w-full max-w-2xl flex-1 items-center gap-2.5 rounded-none border border-border bg-card px-3 font-mono text-[13px] text-muted-foreground transition-colors hover:border-border/80 hover:text-foreground focus:outline-none focus-visible:border-primary focus-visible:ring-1 focus-visible:ring-primary md:flex"
    >
      <span className="text-foreground/40 transition-colors group-focus-visible:text-primary group-hover:text-foreground/70" aria-hidden>
        ›
      </span>
      <span className="flex-1 truncate text-left text-muted-foreground/70 group-hover:text-foreground/80">
        Search everything
      </span>
      <kbd className="shrink-0 border border-border bg-background/40 px-1.5 py-0.5 font-mono text-[10px] text-foreground/70 transition-colors group-hover:text-foreground">
        {mod}K
      </kbd>
    </button>
  )

  return (
    <>
      {mobile}
      {desktop}
    </>
  )
}

function SidebarNav({ onNavigate }: { onNavigate?: () => void }) {
  const { data: health } = useHealth()

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
  const delegationLive = useLiveDelegationCount()
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
    if (item.href === '/app') {
      return { ...item, warnBadge: pendingToolApprovals.length }
    }
    if (item.href === '/approvals') {
      return { ...item, warnBadge: pendingToolApprovals.length }
    }
    if (item.href === '/worker-approvals') {
      return { ...item, alertBadge: workerApprovals }
    }
    return item
  })

  const automationNav: NavItem[] = automationNavBase.map((item) => {
    if (item.href === '/tasks') {
      return { ...item, infoBadge: taskOffersPending }
    }
    if (item.href === '/workers') {
      return { ...item, liveBadge: workerLive, alertBadge: workerApprovals }
    }
    if (item.href === '/delegations') {
      return { ...item, liveBadge: delegationLive }
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
    return item
  })

  return (
    <nav className="flex-1 min-h-0 overflow-y-auto space-y-0.5 py-3 pr-2">
      <div className="pt-1">
        <SectionLabel>Workspace</SectionLabel>
        {workspaceNav.map((item) => (
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
        <SectionLabel>Work</SectionLabel>
        {automationNav.map((item) => (
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
        <SectionLabel>Setup</SectionLabel>
        {setupNav.map((item) => (
          <NavLink key={item.href} item={item} onNavigate={onNavigate} />
        ))}
        <ConfigureWithAI className="mx-4 mt-2" />
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
      label: 'Human inbox',
      href: '/app',
      icon: <Inbox className="h-4 w-4" />,
      hint: 'Mobile-first approvals and human task queue',
    })
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
      ? [
          { label: 'People', href: '/pairing?tab=people', icon: <UsersRound className="h-4 w-4" /> },
          { label: 'Devices', href: '/pairing?tab=devices', icon: <Laptop className="h-4 w-4" /> },
        ]
      : []),
    { label: 'Backups', href: '/backups', icon: <Archive className="h-4 w-4" /> },
    { label: 'Settings', href: '/settings', icon: <Settings className="h-4 w-4" /> },
  ]

  return (
    <nav className="flex-1 min-h-0 overflow-y-auto space-y-0.5 py-3 pr-2">
      <div className="px-4 pb-2 pt-1">
        <div className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground/70">
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
      <div className="box-border flex h-[100dvh] min-h-[100dvh] overflow-hidden [padding-left:env(safe-area-inset-left)] [padding-right:env(safe-area-inset-right)] [padding-top:env(safe-area-inset-top)]">
        {/* Desktop sidebar */}
        <aside className="hidden w-56 flex-col overflow-hidden border-r border-sidebar-border bg-sidebar-background md:flex">
          <BrandHeader />
          <SidebarNav />
          <StatusBar />
        </aside>

        {/* Mobile sheet */}
        <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
          <SheetContent side="left" className="flex w-72 max-w-[calc(100vw-1rem)] flex-col overflow-hidden gap-0 bg-sidebar-background p-0 [padding-bottom:env(safe-area-inset-bottom)] [padding-top:env(safe-area-inset-top)] sm:w-80 md:w-56" aria-describedby={undefined}>
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
          <header className="flex h-14 shrink-0 items-center gap-2.5 border-b border-border bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/70">
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
            {/* On <md the search collapses to an icon (rendered inside
                TopBarSearch); the spacer keeps it + the toggle on the right.
                On >=md the box itself is flex-1 and fills the center. */}
            <div className="flex-1 md:hidden" />
            <TopBarSearch />
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
