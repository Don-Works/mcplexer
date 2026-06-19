import { createElement } from 'react'
import { FilePlus, FolderTree, Notebook, RefreshCw, RotateCw } from 'lucide-react'
import type { NavigateFunction } from 'react-router-dom'
import { toast } from 'sonner'
import { reindexBrain, syncBrain } from '@/api/brainBrowser'
import type { CommandEntry } from './commands'

// brainCommands.ts — the `>` command-verb set for the CommandSurface mode
// grammar (DESIGN §4.0): "> new task", "> new memory", "> reindex",
// "> switch scope <ws>", "> sync brain". These share the cmd+K surface with the
// filter / ref / tag modes so the operator learns one grammar. reindex + sync
// fire the brain endpoints directly; the new-* verbs navigate to the browser's
// New flow.
const iconClass = 'h-3.5 w-3.5'

export function brainVerbEntries(): CommandEntry[] {
  return [
    {
      id: 'brain-new-task',
      label: 'new task',
      keywords: 'brain create task record',
      hint: 'brain',
      icon: createElement(FilePlus, { className: iconClass }),
      run: ({ navigate }: { navigate: NavigateFunction }) => navigate('/brain/browse?new=task'),
    },
    {
      id: 'brain-new-memory',
      label: 'new memory',
      keywords: 'brain create memory fact note record',
      hint: 'brain',
      icon: createElement(Notebook, { className: iconClass }),
      run: ({ navigate }: { navigate: NavigateFunction }) => navigate('/brain/browse?new=memory'),
    },
    {
      id: 'brain-reindex',
      label: 'reindex',
      keywords: 'brain reindex rebuild index',
      hint: 'brain',
      icon: createElement(RotateCw, { className: iconClass }),
      run: () => {
        reindexBrain()
          .then(() => toast.success('Brain reindexed.'))
          .catch((e) => toast.error(e instanceof Error ? e.message : 'Reindex failed'))
      },
    },
    {
      id: 'brain-sync',
      label: 'sync brain',
      keywords: 'brain sync pull rebase git',
      hint: 'brain',
      icon: createElement(RefreshCw, { className: iconClass }),
      run: () => {
        syncBrain()
          .then((r) =>
            r.conflict
              ? toast.error(r.note ?? 'Sync hit a rebase conflict.')
              : toast.success('Brain synced (pull --rebase + reindex).'),
          )
          .catch((e) => toast.error(e instanceof Error ? e.message : 'Sync failed'))
      },
    },
    {
      id: 'brain-switch-scope',
      label: 'switch scope',
      keywords: 'brain switch scope workspace client',
      hint: 'brain',
      icon: createElement(FolderTree, { className: iconClass }),
      run: ({ navigate }: { navigate: NavigateFunction }) => navigate('/brain/browse'),
    },
  ]
}
