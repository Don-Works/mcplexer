import type { DashboardData } from './types'
import { request } from './transport'

// Dashboard
export function getDashboard(range_?: string): Promise<DashboardData> {
  const params = range_ ? `?range=${range_}` : ''
  return request(`/dashboard${params}`)
}
