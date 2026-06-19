import { useEffect, useMemo, useState } from 'react'
import {
  listBrainTaskRecords,
  listBrainMemoryRecords,
  type BrainTaskRecord,
  type BrainMemoryRecord,
} from '@/api/brainBrowser'
import type { RecordTab } from './components/RecordList'

interface Args {
  workspace: string | null
  source: 'central' | 'repo' | null
  activeTab: RecordTab
  refreshKey: number
  tag: string | null
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : 'Unknown error'
}

function tagFilter<T extends { tags?: string[] }>(rows: T[] | null, tag: string | null): T[] {
  if (!rows) return []
  if (!tag) return rows
  return rows.filter((r) => (r.tags ?? []).includes(tag))
}

export function useBrainBrowserRecords({
  workspace,
  source,
  activeTab,
  refreshKey,
  tag,
}: Args) {
  const [tasks, setTasks] = useState<BrainTaskRecord[] | null>(null)
  const [facts, setFacts] = useState<BrainMemoryRecord[] | null>(null)
  const [notes, setNotes] = useState<BrainMemoryRecord[] | null>(null)
  const [loadingTab, setLoadingTab] = useState<RecordTab | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setTasks(null)
    setFacts(null)
    setNotes(null)
    setError(null)
  }, [workspace, source])

  useEffect(() => {
    if (!workspace) return
    let active = true
    const tab = activeTab

    setLoadingTab(tab)
    setError(null)

    const sourceFilter = source ?? undefined
    const load =
      tab === 'tasks'
        ? listBrainTaskRecords(workspace, { source: sourceFilter }).then((rows) => {
            if (active) setTasks(rows)
          })
        : listBrainMemoryRecords(workspace, {
            source: sourceFilter,
            memoryKind: tab === 'memory' ? 'fact' : 'note',
          }).then((rows) => {
            if (!active) return
            if (tab === 'memory') setFacts(rows)
            else setNotes(rows)
          })

    load
      .catch((err) => {
        if (active) setError(message(err))
      })
      .finally(() => {
        if (active) setLoadingTab(null)
      })

    return () => {
      active = false
    }
  }, [workspace, source, activeTab, refreshKey])

  const visibleTasks = useMemo(() => tagFilter(tasks, tag), [tasks, tag])
  const visibleFacts = useMemo(() => tagFilter(facts, tag), [facts, tag])
  const visibleNotes = useMemo(() => tagFilter(notes, tag), [notes, tag])

  const currentLoaded =
    activeTab === 'tasks' ? tasks !== null : activeTab === 'memory' ? facts !== null : notes !== null

  return {
    tasks: visibleTasks,
    memories: visibleFacts,
    notes: visibleNotes,
    allMemories: [...visibleFacts, ...visibleNotes],
    listLoading: Boolean(workspace) && !currentLoaded && loadingTab === activeTab,
    error,
  }
}
