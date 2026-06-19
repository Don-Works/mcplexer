// AttentionCard — the "needs your eye" surface. One card that gathers
// every operator-blocking signal across the gateway and renders them
// as a short, scannable list with one-click deep links.
//
// Empty state: a calm "all clear" line, not a sad blank. We render an
// idle dot + a single line of text so the dashboard never feels
// melodramatic when nothing is wrong.
//
// Items are sorted by severity (critical > warn > info) and each row
// links into the surface that resolves it. Nothing here triggers a
// new SSE subscription — every input is already streamed elsewhere
// and passed in as a prop.

import { Link } from 'react-router-dom'
import {
  AlertTriangle,
  CheckCircle2,
  Inbox,
  KeyRound,
  LayoutGrid,
  Lock,
  ShieldCheck,
  WifiOff,
  Workflow,
} from 'lucide-react'
import { cn } from '@/lib/utils'

export type AttentionTone = 'critical' | 'warn' | 'info'

export interface AttentionItem {
  key: string
  tone: AttentionTone
  icon: React.ReactNode
  label: React.ReactNode
  to: string
  cta: string
}

const toneRank: Record<AttentionTone, number> = { critical: 0, warn: 1, info: 2 }

const toneBorder: Record<AttentionTone, string> = {
  critical: 'border-red-500/40',
  warn: 'border-amber-500/40',
  info: 'border-sky-500/30',
}

const toneText: Record<AttentionTone, string> = {
  critical: 'text-red-300',
  warn: 'text-amber-300',
  info: 'text-sky-300',
}

export interface AttentionInput {
  pendingApprovals: number
  pendingWorkerApprovals: number
  unhealthyWorkers: number
  offlinePeers: number
  pendingMemoryOffers: number
  expiringSecrets: number
  // Connection-health fields — derived by useConnectionHealth in DashboardPage.
  // serversNeedingCreds: array of {id, name} for servers missing credentials.
  // workspacesWithoutRoutes: array of {id, name} for workspaces with no routes.
  serversNeedingCreds?: Array<{ id: string; name: string }>
  workspacesWithoutRoutes?: Array<{ id: string; name: string }>
  // Unreviewed delegations (review_required && !reviewed) — drives the
  // review-sweep attention item for parent models to score prior work.
  unreviewedDelegations?: number
}

export function AttentionCard({ input }: { input: AttentionInput }) {
  const items = buildItems(input)

  return (
    <section
      data-testid="dash-attention"
      className="border border-border bg-card/40"
    >
      <header className="flex items-center justify-between border-b border-border/60 px-4 py-2">
        <h2 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          Needs your attention
        </h2>
        <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
          {items.length === 0 ? 'clear' : items.length}
        </span>
      </header>
      {items.length === 0 ? <EmptyAttention /> : <AttentionList items={items} />}
    </section>
  )
}

function buildItems(i: AttentionInput): AttentionItem[] {
  const items: AttentionItem[] = []
  if (i.pendingApprovals > 0) {
    items.push({
      key: 'tool-approvals',
      tone: 'critical',
      icon: <ShieldCheck className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{i.pendingApprovals}</strong> tool-call approval{i.pendingApprovals !== 1 ? 's' : ''} waiting
        </>
      ),
      to: '/approvals',
      cta: 'Review',
    })
  }
  if (i.pendingWorkerApprovals > 0) {
    items.push({
      key: 'worker-approvals',
      tone: 'warn',
      icon: <Workflow className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{i.pendingWorkerApprovals}</strong> worker run{i.pendingWorkerApprovals !== 1 ? 's' : ''} waiting for approval
        </>
      ),
      to: '/worker-approvals',
      cta: 'Review',
    })
  }
  if (i.unhealthyWorkers > 0) {
    items.push({
      key: 'unhealthy-workers',
      tone: 'warn',
      icon: <AlertTriangle className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{i.unhealthyWorkers}</strong> worker{i.unhealthyWorkers !== 1 ? 's' : ''} auto-paused or failing
        </>
      ),
      to: '/workers',
      cta: 'Inspect',
    })
  }
  if (i.offlinePeers > 0) {
    items.push({
      key: 'offline-peers',
      tone: 'warn',
      icon: <WifiOff className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{i.offlinePeers}</strong> paired peer{i.offlinePeers !== 1 ? 's' : ''} offline
        </>
      ),
      to: '/pairing',
      cta: 'Pairing',
    })
  }
  if (i.pendingMemoryOffers > 0) {
    items.push({
      key: 'memory-offers',
      tone: 'info',
      icon: <Inbox className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{i.pendingMemoryOffers}</strong> memory offer{i.pendingMemoryOffers !== 1 ? 's' : ''} from peers awaiting review
        </>
      ),
      to: '/memory/shared',
      cta: 'Review',
    })
  }
  if (i.expiringSecrets > 0) {
    items.push({
      key: 'expiring-secrets',
      tone: 'warn',
      icon: <KeyRound className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{i.expiringSecrets}</strong> credential{i.expiringSecrets !== 1 ? 's' : ''} expiring soon
        </>
      ),
      to: '/config',
      cta: 'Rotate',
    })
  }
  const credsCount = i.serversNeedingCreds?.length ?? 0
  if (credsCount > 0) {
    const servers = i.serversNeedingCreds!
    const to =
      credsCount === 1
        ? `/workspaces?focus_server=${servers[0].id}&action=fix-auth`
        : '/workspaces'
    items.push({
      key: 'servers-need-creds',
      tone: 'warn',
      icon: <Lock className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{credsCount}</strong> server{credsCount !== 1 ? 's' : ''} need{credsCount === 1 ? 's' : ''} credentials
        </>
      ),
      to,
      cta: 'Fix',
    })
  }
  const noRoutesCount = i.workspacesWithoutRoutes?.length ?? 0
  if (noRoutesCount > 0) {
    const wss = i.workspacesWithoutRoutes!
    const to =
      noRoutesCount === 1
        ? `/workspaces?focus_workspace=${wss[0].id}&action=add-server`
        : '/workspaces'
    items.push({
      key: 'workspaces-no-routes',
      tone: 'info',
      icon: <LayoutGrid className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{noRoutesCount}</strong> workspace{noRoutesCount !== 1 ? 's' : ''} {noRoutesCount === 1 ? 'has' : 'have'} no routes
        </>
      ),
      to,
      cta: 'Wire up',
    })
  }
  const unreviewed = i.unreviewedDelegations ?? 0
  if (unreviewed > 0) {
    items.push({
      key: 'unreviewed-delegations',
      tone: 'warn',
      icon: <Workflow className="h-3.5 w-3.5" />,
      label: (
        <>
          <strong className="font-medium">{unreviewed}</strong> delegation{unreviewed !== 1 ? 's' : ''} waiting for review
        </>
      ),
      to: '/delegations',
      cta: 'Review',
    })
  }
  items.sort((a, b) => toneRank[a.tone] - toneRank[b.tone])
  return items
}

function AttentionList({ items }: { items: AttentionItem[] }) {
  return (
    <ul className="divide-y divide-border/40">
      {items.map((it) => (
        <li key={it.key}>
          <Link
            to={it.to}
            data-testid={`dash-attention-${it.key}`}
            className="group flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-accent/20"
          >
            <span className={cn('shrink-0', toneText[it.tone])}>{it.icon}</span>
            <span className={cn('flex-1 text-[13px] leading-snug', toneText[it.tone])}>
              {it.label}
            </span>
            <span
              className={cn(
                'inline-flex shrink-0 items-center border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wider transition-colors',
                toneBorder[it.tone],
                toneText[it.tone],
                'group-hover:bg-accent/30',
              )}
            >
              {it.cta} ›
            </span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

function EmptyAttention() {
  return (
    <div className="flex items-center gap-2.5 px-4 py-3 text-[13px] text-muted-foreground">
      <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500/70" />
      <span>All clear. Nothing is blocked on you right now.</span>
    </div>
  )
}
