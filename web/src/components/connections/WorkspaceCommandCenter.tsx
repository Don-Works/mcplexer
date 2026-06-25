import { Link } from 'react-router-dom'
import {
  Bot,
  Brain,
  FileText,
  ListTodo,
  Plug,
  Route,
} from 'lucide-react'

import type { AuditRecord, RouteRule, ToolApproval, Workspace } from '@/api/types'
import type { MemoryStats } from '@/api/memory'
import type { Task } from '@/api/tasks'
import type {
  DelegationContext,
  WorkerApproval,
  WorkerSummary,
} from '@/api/workers'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  ActionFeed,
  buildActionItems,
} from './WorkspaceActionFeed'
import {
  memoryDetail,
  MetricStrip,
  RecentAudit,
  ScopeMap,
} from './WorkspaceCommandSections'
import type {
  WorkspaceConnectionRow,
  WorkspaceConnectionSummary,
} from './connection-model'

export interface WorkspaceCommandCenterData {
  routes: RouteRule[]
  pendingApprovals: ToolApproval[]
  workerApprovals: WorkerApproval[]
  workers: WorkerSummary[]
  delegations: DelegationContext[]
  tasks: Task[]
  memoryStats: MemoryStats | null
  recentAudit: AuditRecord[]
}

interface Props {
  workspace: Workspace
  summary?: WorkspaceConnectionSummary
  rows: WorkspaceConnectionRow[]
  operations: WorkspaceCommandCenterData
  onOpenConnection: (row: WorkspaceConnectionRow) => void
}

export function WorkspaceCommandCenter({
  workspace,
  summary,
  rows,
  operations,
  onOpenConnection,
}: Props) {
  const workspaceRoutes = operations.routes.filter((route) => route.workspace_id === workspace.id)
  const pendingToolApprovals = operations.pendingApprovals.filter(
    (approval) =>
      approval.workspace_id === workspace.id ||
      approval.originating_workspace === workspace.id ||
      approval.workspace_name === workspace.name,
  )
  const workerByID = new Map(operations.workers.map((worker) => [worker.id, worker]))
  const workspaceWorkers = operations.workers.filter((worker) => worker.workspace_id === workspace.id)
  const pendingWorkerApprovals = operations.workerApprovals.filter(
    (approval) => workerByID.get(approval.worker_id)?.workspace_id === workspace.id,
  )
  const workspaceDelegations = operations.delegations.filter((d) => d.workspace_id === workspace.id)
  const needsReview = workspaceDelegations.filter((d) => d.review_required && !d.review?.reviewed)
  const runningDelegations = workspaceDelegations.filter(
    (d) => d.aggregate.running > 0 || d.status === 'running',
  )
  const missingCredentials = rows.filter((row) => row.state.kind === 'needs-auth')
  const enabledWorkers = workspaceWorkers.filter((worker) => worker.enabled)
  const liveWorkers = workspaceWorkers.filter(
    (worker) => worker.last_run_status === 'running' || worker.last_run_status === 'awaiting_approval',
  )
  const openTasks = operations.tasks.filter((task) => !task.closed_at)
  const urgentTasks = openTasks.filter((task) => task.priority === 'critical' || task.priority === 'high')
  const protectedRoutes = workspaceRoutes.filter((route) => route.approval_mode !== 'none')
  const auditProblems = operations.recentAudit.filter(
    (record) => record.status === 'error' || record.status === 'blocked',
  )
  const actions = buildActionItems({
    workspace,
    missingCredentials,
    pendingToolApprovals,
    pendingWorkerApprovals,
    needsReview,
    runningDelegations,
    urgentTasks,
    auditProblems,
    onOpenConnection,
  })

  return (
    <section className="border border-border/50 bg-card/30">
      <div className="border-b border-border/50 px-4 py-3">
        <div className="flex flex-col gap-3 xl:flex-row xl:items-start xl:justify-between">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="truncate text-lg font-semibold">{workspace.name}</h2>
              <Badge variant="outline" tone={workspace.default_policy === 'allow' ? 'success' : 'muted'}>
                default {workspace.default_policy}
              </Badge>
              <Badge variant="outline" tone={actions.length > 0 ? 'warn' : 'success'}>
                {actions.length > 0 ? `${actions.length} action${actions.length === 1 ? '' : 's'}` : 'clear'}
              </Badge>
            </div>
            <p className="truncate font-mono text-xs text-muted-foreground/60" title={workspace.root_path}>
              {workspace.root_path}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" asChild>
              <Link to={`/workspaces/routes?workspace=${encodeURIComponent(workspace.id)}`}>
                <Route className="h-3.5 w-3.5" />
                Server access
              </Link>
            </Button>
            <Button variant="outline" size="sm" asChild>
              <Link to={`/tasks/all?workspace=${encodeURIComponent(workspace.id)}`}>
                <ListTodo className="h-3.5 w-3.5" />
                Tasks
              </Link>
            </Button>
            <Button variant="outline" size="sm" asChild>
              <Link to={`/audit?workspace_id=${encodeURIComponent(workspace.id)}`}>
                <FileText className="h-3.5 w-3.5" />
                Audit
              </Link>
            </Button>
          </div>
        </div>
      </div>

      <div className="grid divide-y divide-border/50 xl:grid-cols-[minmax(0,1.1fr)_minmax(24rem,0.9fr)] xl:divide-x xl:divide-y-0">
        <div className="space-y-4 p-4">
          <MetricStrip
            items={[
              {
                icon: <Plug className="h-4 w-4" />,
                label: 'Access',
                value: `${summary?.connected ?? 0}`,
                detail: missingCredentials.length > 0 ? `${missingCredentials.length} need credentials` : `${summary?.available ?? 0} available`,
                tone: missingCredentials.length > 0 ? 'warn' : 'success',
              },
              {
                icon: <Route className="h-4 w-4" />,
                label: 'Access rules',
                value: String(workspaceRoutes.length),
                detail: protectedRoutes.length > 0 ? `${protectedRoutes.length} require approval` : 'no approval gates',
                tone: protectedRoutes.length > 0 ? 'info' : 'muted',
              },
              {
                icon: <Bot className="h-4 w-4" />,
                label: 'Workers',
                value: String(enabledWorkers.length),
                detail: liveWorkers.length > 0 ? `${liveWorkers.length} live now` : `${workspaceWorkers.length} configured`,
                tone: liveWorkers.length > 0 ? 'success' : 'muted',
              },
              {
                icon: <Brain className="h-4 w-4" />,
                label: 'Memory',
                value: String(operations.memoryStats?.total_memories ?? 0),
                detail: memoryDetail(operations.memoryStats),
                tone: (operations.memoryStats?.decay_pressure ?? 0) > 0.6 ? 'warn' : 'muted',
              },
            ]}
          />
          <ScopeMap
            routeCount={workspaceRoutes.length}
            protectedRouteCount={protectedRoutes.length}
            openTaskCount={openTasks.length}
            urgentTaskCount={urgentTasks.length}
            delegationReviewCount={needsReview.length}
            runningDelegationCount={runningDelegations.length}
          />
        </div>

        <div className="space-y-4 p-4">
          <ActionFeed items={actions} />
          <RecentAudit rows={operations.recentAudit} workspaceId={workspace.id} />
        </div>
      </div>
    </section>
  )
}
