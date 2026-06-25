import { useCallback } from 'react'

import {
  listApprovals,
  queryAuditLogs,
} from '@/api/client'
import type { AuditRecord, PaginatedResponse, RouteRule } from '@/api/types'
import { memoryStats as getMemoryStats } from '@/api/memory'
import { listTasks } from '@/api/tasks'
import {
  listDelegations,
  listWorkerApprovals,
  listWorkers,
} from '@/api/workers'
import { useApi } from '@/hooks/use-api'
import type { WorkspaceCommandCenterData } from './WorkspaceCommandCenter'

const EMPTY_AUDIT: PaginatedResponse<AuditRecord> = { data: [], total: 0 }

export function useWorkspaceOperations(
  workspaceID: string,
  routes: RouteRule[],
): WorkspaceCommandCenterData {
  const approvalsFetcher = useCallback(() => listApprovals('pending'), [])
  const workersFetcher = useCallback(() => listWorkers(), [])
  const workerApprovalsFetcher = useCallback(() => listWorkerApprovals({ status: 'pending' }), [])
  const delegationsFetcher = useCallback(
    () => workspaceID ? listDelegations({ workspaceId: workspaceID, limit: 50 }) : Promise.resolve([]),
    [workspaceID],
  )
  const tasksFetcher = useCallback(
    () => workspaceID ? listTasks({ workspace_id: workspaceID, state: 'open', limit: 50 }) : Promise.resolve([]),
    [workspaceID],
  )
  const memoryStatsFetcher = useCallback(
    () => workspaceID ? getMemoryStats(workspaceID) : Promise.resolve(null),
    [workspaceID],
  )
  const auditFetcher = useCallback(
    () => workspaceID ? queryAuditLogs({ workspace_id: workspaceID, limit: 5 }) : Promise.resolve(EMPTY_AUDIT),
    [workspaceID],
  )

  const { data: pendingApprovals } = useApi(approvalsFetcher)
  const { data: workers } = useApi(workersFetcher)
  const { data: workerApprovals } = useApi(workerApprovalsFetcher)
  const { data: delegations } = useApi(delegationsFetcher)
  const { data: tasks } = useApi(tasksFetcher)
  const { data: workspaceMemoryStats } = useApi(memoryStatsFetcher)
  const { data: recentAudit } = useApi(auditFetcher)

  return {
    routes,
    pendingApprovals: pendingApprovals ?? [],
    workerApprovals: workerApprovals ?? [],
    workers: workers ?? [],
    delegations: delegations ?? [],
    tasks: tasks ?? [],
    memoryStats: workspaceMemoryStats ?? null,
    recentAudit: recentAudit?.data ?? [],
  }
}
