import { Link } from 'react-router-dom'
import {
  AlertTriangle,
  ArrowUpRight,
  Bot,
  CheckCircle2,
  Clock,
  GitBranch,
  KeyRound,
  ListTodo,
  ShieldCheck,
} from 'lucide-react'

import type { AuditRecord, ToolApproval, Workspace } from '@/api/types'
import type { Task } from '@/api/tasks'
import type { DelegationContext, WorkerApproval } from '@/api/workers'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { getErrorReason } from '@/lib/audit-semantics'
import type { WorkspaceConnectionRow } from './connection-model'

export type CommandTone = 'warn' | 'critical' | 'info' | 'success' | 'muted'

export interface ActionItem {
  key: string
  tone: CommandTone
  icon: React.ReactNode
  title: string
  detail: string
  href?: string
  cta: string
  onClick?: () => void
}

export function buildActionItems(input: {
  workspace: Workspace
  missingCredentials: WorkspaceConnectionRow[]
  pendingToolApprovals: ToolApproval[]
  pendingWorkerApprovals: WorkerApproval[]
  needsReview: DelegationContext[]
  runningDelegations: DelegationContext[]
  urgentTasks: Task[]
  auditProblems: AuditRecord[]
  onOpenConnection: (row: WorkspaceConnectionRow) => void
}): ActionItem[] {
  const items: ActionItem[] = []
  const firstMissing = input.missingCredentials[0]
  if (firstMissing) {
    items.push({
      key: 'missing-creds',
      tone: 'warn',
      icon: <KeyRound className="h-4 w-4" />,
      title: `${input.missingCredentials.length} connection${input.missingCredentials.length === 1 ? '' : 's'} need credentials`,
      detail: `${firstMissing.server.name} cannot run in this workspace yet.`,
      cta: 'Fix first',
      onClick: () => input.onOpenConnection(firstMissing),
    })
  }

  const firstApproval = input.pendingToolApprovals[0]
  if (firstApproval) {
    items.push({
      key: 'tool-approvals',
      tone: 'warn',
      icon: <ShieldCheck className="h-4 w-4" />,
      title: `${input.pendingToolApprovals.length} tool approval${input.pendingToolApprovals.length === 1 ? '' : 's'}`,
      detail: firstApproval.summary || firstApproval.tool_name,
      href: `/approvals?selected=${encodeURIComponent(firstApproval.id)}`,
      cta: 'Review',
    })
  }

  const firstWorkerApproval = input.pendingWorkerApprovals[0]
  if (firstWorkerApproval) {
    items.push({
      key: 'worker-approvals',
      tone: 'critical',
      icon: <Bot className="h-4 w-4" />,
      title: `${input.pendingWorkerApprovals.length} worker proposal${input.pendingWorkerApprovals.length === 1 ? '' : 's'}`,
      detail: firstWorkerApproval.tool_name,
      href: `/worker-approvals?run_id=${encodeURIComponent(firstWorkerApproval.run_id)}`,
      cta: 'Decide',
    })
  }

  if (input.needsReview.length > 0) {
    items.push({
      key: 'delegation-review',
      tone: 'info',
      icon: <GitBranch className="h-4 w-4" />,
      title: `${input.needsReview.length} delegation${input.needsReview.length === 1 ? '' : 's'} need review`,
      detail: input.needsReview[0].objective,
      href: '/delegations',
      cta: 'Score',
    })
  } else if (input.runningDelegations.length > 0) {
    items.push({
      key: 'delegation-running',
      tone: 'success',
      icon: <Clock className="h-4 w-4" />,
      title: `${input.runningDelegations.length} delegation${input.runningDelegations.length === 1 ? '' : 's'} running`,
      detail: input.runningDelegations[0].objective,
      href: '/delegations',
      cta: 'Watch',
    })
  }

  if (input.urgentTasks.length > 0) {
    items.push({
      key: 'urgent-tasks',
      tone: 'warn',
      icon: <ListTodo className="h-4 w-4" />,
      title: `${input.urgentTasks.length} high-priority task${input.urgentTasks.length === 1 ? '' : 's'}`,
      detail: input.urgentTasks[0].title,
      href: `/tasks/all?workspace=${encodeURIComponent(input.workspace.id)}&priority=high`,
      cta: 'Triage',
    })
  }

  if (input.auditProblems.length > 0) {
    const first = input.auditProblems[0]
    // Use the shared error-reason semantics so the banner reads the same as the
    // audit row/inspector ("blocked" / "no route" folded), falling back to the
    // tool name when there's no descriptor. Detection itself is unchanged: the
    // caller still scopes auditProblems to error+blocked rows.
    items.push({
      key: 'audit-problems',
      tone: 'critical',
      icon: <AlertTriangle className="h-4 w-4" />,
      title: `${input.auditProblems.length} recent audit problem${input.auditProblems.length === 1 ? '' : 's'}`,
      detail: getErrorReason(first) || first.tool_name,
      href: `/audit?workspace_id=${encodeURIComponent(input.workspace.id)}`,
      cta: 'Inspect',
    })
  }

  return items.slice(0, 5)
}

export function ActionFeed({ items }: { items: ActionItem[] }) {
  return (
    <div className="border border-border/50">
      <div className="flex items-center justify-between gap-3 border-b border-border/50 px-3 py-2">
        <h3 className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
          Needs attention
        </h3>
        <Badge variant="outline" tone={items.length > 0 ? 'warn' : 'success'}>
          {items.length > 0 ? items.length : 'clear'}
        </Badge>
      </div>
      {items.length === 0 ? (
        <div className="flex items-center gap-2 px-3 py-4 text-sm text-muted-foreground">
          <CheckCircle2 className="h-4 w-4 text-emerald-400" />
          No workspace actions waiting.
        </div>
      ) : (
        <div className="divide-y divide-border/40">
          {items.map((item) => (
            <ActionRow key={item.key} item={item} />
          ))}
        </div>
      )}
    </div>
  )
}

function ActionRow({ item }: { item: ActionItem }) {
  const body = (
    <>
      <span className={cn('mt-0.5 shrink-0', toneClass(item.tone))}>{item.icon}</span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium text-foreground">{item.title}</span>
        <span className="block truncate text-xs text-muted-foreground">{item.detail}</span>
      </span>
      <span className="inline-flex shrink-0 items-center gap-1 font-mono text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
        {item.cta}
        <ArrowUpRight className="h-3 w-3" />
      </span>
    </>
  )
  const className = 'flex w-full items-start gap-3 px-3 py-2.5 text-left transition-colors hover:bg-muted/30'
  if (item.onClick) return <button type="button" onClick={item.onClick} className={className}>{body}</button>
  return <Link to={item.href ?? '#'} className={className}>{body}</Link>
}

function toneClass(tone: CommandTone): string {
  switch (tone) {
    case 'critical':
      return 'text-red-300'
    case 'warn':
      return 'text-amber-300'
    case 'info':
      return 'text-sky-300'
    case 'success':
      return 'text-emerald-300'
    default:
      return 'text-muted-foreground'
  }
}
