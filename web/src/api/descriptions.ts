import type {
  PaginatedResponse,
  ToolDescriptionFilter,
  ToolDescriptionVersion,
} from './types'
import { request } from './transport'

// Description Refinement
export function listDescriptionVersions(
  filter: ToolDescriptionFilter = {},
): Promise<PaginatedResponse<ToolDescriptionVersion>> {
  const params = new URLSearchParams()
  if (filter.tool_name) params.set('tool_name', filter.tool_name)
  if (filter.status) params.set('status', filter.status)
  if (filter.source) params.set('source', filter.source)
  if (filter.limit) params.set('limit', String(filter.limit))
  if (filter.offset) params.set('offset', String(filter.offset))
  const qs = params.toString()
  return request(`/descriptions${qs ? `?${qs}` : ''}`)
}

export function getDescriptionVersion(id: string): Promise<ToolDescriptionVersion> {
  return request(`/descriptions/${id}`)
}

export function acceptDescription(
  id: string,
  reviewNote?: string,
): Promise<{ status: string }> {
  return request(`/descriptions/${id}/accept`, {
    method: 'POST',
    body: JSON.stringify({ review_note: reviewNote || '' }),
  })
}

export function rejectDescription(
  id: string,
  reviewNote: string,
): Promise<{ status: string }> {
  return request(`/descriptions/${id}/reject`, {
    method: 'POST',
    body: JSON.stringify({ review_note: reviewNote }),
  })
}

export function submitDescription(data: {
  tool_name: string
  description: string
  rationale?: string
}): Promise<ToolDescriptionVersion> {
  return request('/descriptions', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}
