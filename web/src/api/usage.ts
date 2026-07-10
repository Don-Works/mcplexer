import { request } from './client'

// --- Types matching the JSON contract ---

export type ProviderStatus = 'ok' | 'partial' | 'unconfigured' | 'unavailable' | 'error'
export type WindowUnit = 'percent' | 'requests' | 'credits' | 'usd' | 'tokens'

export interface UsageWindow {
  id: string
  label: string
  used_percent?: number
  used?: number
  limit?: number
  remaining?: number
  unit: WindowUnit
  resets_at?: string
  duration_minutes?: number
}

export interface ObservedTotals {
  requests: number
  total_tokens?: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  accounting_missing_runs: number
}

export interface ProviderUsage {
  provider: string
  label: string
  plan?: string
  status: ProviderStatus
  source: string
  source_label: string
  observed: ObservedTotals
  windows: UsageWindow[]
  updated_at?: string
  stale: boolean
  error?: string
  detail?: string
  allowance_status?: ProviderStatus
  allowance_source?: string
  allowance_source_label?: string
  allowance_updated_at?: string
  allowance_stale?: boolean
  allowance_error?: string
  observed_source?: string
  observed_source_label?: string
  observed_updated_at?: string
  observed_cost_kind?: 'estimate' | 'metered'
}

export interface OpenRouterCredits {
  usage?: number
  limit?: number
  remaining?: number
  usage_daily?: number
  usage_weekly?: number
  usage_monthly?: number
}

export interface OpenRouterModel {
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
}

export interface OpenRouterHarness {
  harness: string
  requests: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  cost_kind?: 'estimate' | 'metered'
  accounting_missing_runs: number
  models: OpenRouterModel[]
}

export interface OpenRouterUsage {
  status: ProviderStatus
  credits: OpenRouterCredits
  by_harness: OpenRouterHarness[]
  updated_at?: string
  stale: boolean
  error?: string
}

export interface UsageResponse {
  generated_at: string
  window_days: number
  providers: ProviderUsage[]
  openrouter: OpenRouterUsage
}

// --- API functions ---

export function getUsage(days = 30): Promise<UsageResponse> {
  return request(`/usage?days=${days}`)
}

export function refreshUsage(days = 30): Promise<UsageResponse> {
  return request(`/usage/refresh?days=${days}`, { method: 'POST' }, { timeoutMs: 60_000 })
}
