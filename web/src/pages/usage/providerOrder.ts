import type { ProviderUsage } from '@/api/usage'

export const PROVIDER_ORDER_STORAGE_KEY = 'mcplexer.usage.provider-order.v1'

export function readProviderOrder(): string[] {
  if (typeof window === 'undefined') return []
  try {
    const parsed = JSON.parse(window.localStorage.getItem(PROVIDER_ORDER_STORAGE_KEY) ?? '[]')
    return Array.isArray(parsed) ? parsed.filter((value): value is string => typeof value === 'string') : []
  } catch {
    return []
  }
}

export function writeProviderOrder(order: string[]): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(PROVIDER_ORDER_STORAGE_KEY, JSON.stringify(order))
  } catch {
    // Best effort: private mode and storage quotas must not break the dashboard.
  }
}

export function sortProviders(providers: ProviderUsage[], order: string[]): ProviderUsage[] {
  const rank = new Map(order.map((provider, index) => [provider, index]))
  return providers
    .map((provider, index) => ({ provider, index }))
    .sort((a, b) => {
      const aRank = rank.get(a.provider.provider)
      const bRank = rank.get(b.provider.provider)
      if (aRank != null && bRank != null) return aRank - bRank
      if (aRank != null) return -1
      if (bRank != null) return 1
      return a.index - b.index
    })
    .map(({ provider }) => provider)
}

export function moveProvider(order: string[], from: string, to: string): string[] {
  if (from === to) return order
  const fromIndex = order.indexOf(from)
  const toIndex = order.indexOf(to)
  if (fromIndex < 0 || toIndex < 0) return order
  const next = [...order]
  const [moved] = next.splice(fromIndex, 1)
  next.splice(toIndex, 0, moved)
  return next
}
