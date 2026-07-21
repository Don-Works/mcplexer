import type { DryRunRequest, DryRunResult, ToolApproval } from './types'
import { request } from './transport'

// Approvals
export function listApprovals(status?: string): Promise<ToolApproval[]> {
  const params = new URLSearchParams()
  if (status) params.set('status', status)
  const qs = params.toString()
  return request(`/approvals${qs ? `?${qs}` : ''}`)
}

export function getApproval(id: string): Promise<ToolApproval> {
  return request(`/approvals/${id}`)
}

export function resolveApproval(
  id: string,
  data: { approved: boolean; reason: string },
): Promise<{ status: string }> {
  return request(`/approvals/${id}/resolve`, {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

// Dry Run
export function dryRun(params: DryRunRequest): Promise<DryRunResult> {
  return request('/dry-run', {
    method: 'POST',
    body: JSON.stringify(params),
  })
}
