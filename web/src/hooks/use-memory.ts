// use-memory — small surface of React hooks around the /api/v1/memory/*
// surface. Mirrors the use-api / use-audit-stream conventions (callback
// fetchers + a stale-while-revalidate pattern via useApi).
//
// The list/offers/count hooks return { data, loading, error, refetch }.
// Mutating helpers are returned as bound async functions on the hook
// object so callers can await them and surface toasts.

import { useCallback, useMemo } from 'react'
import {
  acceptMemoryOffer,
  countMemories,
  createMemory,
  declineMemoryOffer,
  deleteMemory,
  forgetMemoriesBySource,
  getConsolidatorStatus,
  getMemory,
  invalidateMemory,
  listMemories,
  listMemoryOffers,
  memoryStats,
  searchMemories,
  setMemoryPinned,
  type MemoryCount,
  type MemoryEntry,
  type MemoryHit,
  type MemoryListParams,
  type MemoryOffer,
  type MemoryOffersParams,
  type MemorySearchParams,
  type MemoryStats,
} from '@/api/memory'
import { listWorkspaces } from '@/api/client'
import { useApi } from './use-api'

export function useMemoryList(params: MemoryListParams) {
  // Serialize params so the fetcher identity is stable across re-renders
  // with semantically-equal inputs. Parse back inside so the dependency
  // array references only the string — keeps the React Compiler happy.
  const key = JSON.stringify(params)
  const fetcher = useCallback(() => {
    const parsed = JSON.parse(key) as MemoryListParams
    return listMemories(parsed)
  }, [key])
  return useApi<MemoryEntry[]>(fetcher)
}

export function useMemoryCount() {
  const fetcher = useCallback(() => countMemories(), [])
  return useApi<MemoryCount>(fetcher)
}

// useMemoryStats fetches the aggregate "shape of the brain" payload that
// powers the landing-page header strip. Pass an optional workspace_id to
// narrow scope; unset = admin-wide.
export function useMemoryStats(workspaceID?: string) {
  const fetcher = useCallback(() => memoryStats(workspaceID), [workspaceID])
  return useApi<MemoryStats>(fetcher)
}

// useConsolidatorSummary rolls the per-workspace consolidator status up
// into one admin-wide signal for the landing tile. The consolidator runs
// per workspace (no global aggregate endpoint), so we list workspaces and
// fan out a status call each, then keep the most-recent run + an
// enabled/total count. Failures per workspace are swallowed (treated as
// "no status") so one bad workspace never blanks the tile.
export interface ConsolidatorSummary {
  lastRunAt: string | null
  lastRunStatus: string | null
  enabled: number
  total: number
}

export function useConsolidatorSummary() {
  const fetcher = useCallback(async (): Promise<ConsolidatorSummary> => {
    const workspaces = await listWorkspaces()
    const statuses = await Promise.all(
      workspaces.map((w) => getConsolidatorStatus(w.id).catch(() => null)),
    )
    let lastRunAt: string | null = null
    let lastRunStatus: string | null = null
    let enabled = 0
    for (const s of statuses) {
      if (!s) continue
      if (s.enabled) enabled += 1
      if (s.last_run_at && (!lastRunAt || s.last_run_at > lastRunAt)) {
        lastRunAt = s.last_run_at
        lastRunStatus = s.last_run_status ?? null
      }
    }
    return { lastRunAt, lastRunStatus, enabled, total: workspaces.length }
  }, [])
  return useApi<ConsolidatorSummary>(fetcher)
}

export function useMemoryDetail(id: string | null) {
  const fetcher = useCallback(async () => {
    if (!id) return null
    return getMemory(id)
  }, [id])
  return useApi<MemoryEntry | null>(fetcher)
}

export function useMemoryOffers(
  params: MemoryOffersParams = { pending_only: true },
) {
  const key = JSON.stringify(params)
  const fetcher = useCallback(() => {
    const parsed = JSON.parse(key) as MemoryOffersParams
    return listMemoryOffers(parsed)
  }, [key])
  return useApi<MemoryOffer[]>(fetcher)
}

// Mutations as a tiny bag — no hook state, just stable references. Pages
// call .invalidate(id) / .delete(id) / .search(...) and refetch their own
// list afterwards.
export function useMemoryMutations() {
  return useMemo(
    () => ({
      create: createMemory,
      invalidate: invalidateMemory,
      delete: deleteMemory,
      setPinned: setMemoryPinned,
      forgetBySource: forgetMemoriesBySource,
      search: (p: MemorySearchParams): Promise<MemoryHit[]> => searchMemories(p),
      acceptOffer: acceptMemoryOffer,
      declineOffer: declineMemoryOffer,
    }),
    [],
  )
}
